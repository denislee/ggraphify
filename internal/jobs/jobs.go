// Package jobs supervises graphify subprocesses.
//
// Everything ggraphify does that changes something on disk happens here, as a
// Job: an exact argv, an exact environment, a bounded log, a cost, and a
// lifecycle the UI can watch and cancel. Nothing in this package imports GTK;
// the UI subscribes to a channel of events and folds them into widgets on the
// main thread.
//
// Three properties are the reason this is a package rather than a call to
// exec.Command at each button:
//
//   - Two lanes. Free (AST, clustering, exports, reads) fans out; Metered
//     (extract, label) runs one at a time and is never raised implicitly. A
//     GUI that made it easy to start 128 LLM extractions at once would be a
//     liability, not a convenience.
//   - A process group per job. graphify is a Python parent that spawns AST
//     worker subprocesses; killing only the parent leaves orphans holding CPU
//     and the output directory. Cancel signals the whole group.
//   - Bounded, non-blocking streaming. A chatty subprocess must not be able to
//     grow the heap or stall a frame.
package jobs

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/dns/ggraphify/internal/gfy"
	"github.com/dns/ggraphify/internal/ringbuf"
)

// Status is where a job is in its life.
type Status int

const (
	Queued Status = iota
	Running
	Succeeded
	Failed
	Canceled
)

func (s Status) String() string {
	switch s {
	case Queued:
		return "queued"
	case Running:
		return "running"
	case Succeeded:
		return "ok"
	case Failed:
		return "failed"
	case Canceled:
		return "canceled"
	}
	return "unknown"
}

// Done reports whether the status is terminal.
func (s Status) Done() bool { return s == Succeeded || s == Failed || s == Canceled }

// Job is one supervised graphify invocation.
//
// Fields written by the runner are guarded by the Runner's mutex; the UI reads
// them through Snapshot, never directly, so a render on the main thread cannot
// race a worker goroutine's completion.
type Job struct {
	ID    uint64
	Kind  string // a key of gfy.Known
	Repo  string // the checkout this job belongs to; "" for global commands
	Label string // what the UI calls it, e.g. "Update · cc-docsboard"
	Cost  gfy.Cost
	Argv  []string
	Env   gfy.Env
	Dir   string // working directory; normally the checkout root
	Out   string // the graphify-out directory this job reads or writes

	Status  Status
	Exit    int
	Err     error
	Queued  time.Time
	Started time.Time
	Ended   time.Time

	Log *ringbuf.Buf

	cancel context.CancelFunc
	pgid   int
	// paused is true between a Pause and the matching Resume: the process
	// group is stopped with SIGSTOP and holding whatever it had open. The
	// status stays Running, because it is — a stopped process has not ended,
	// still owns its output directory, and must not look finished to anything
	// that keys off Status.Done.
	paused bool
	// pausedAt and pausedFor are the elapsed clock's accounting. A stopped
	// process is not working, so the time it spends stopped is subtracted
	// from Elapsed — which is not cosmetic: Elapsed is what the run history
	// records as this job's duration.
	pausedAt  time.Time
	pausedFor time.Duration
	// done is closed exactly once, when the job reaches a terminal status.
	// It is what lets a chain wait for one step without polling Snapshot or
	// subscribing to the event channel, which has a single consumer (the UI)
	// and drops events under load — neither is a sound completion signal.
	done chan struct{}
}

// Done returns a channel closed when the job has finished, whether it
// succeeded, failed or was cancelled. Reading the outcome afterwards goes
// through the runner, which owns the fields.
func (j *Job) Done() <-chan struct{} { return j.done }

// finish closes the done channel once. Called with the runner's lock held.
func (j *Job) finish() {
	if j.done != nil {
		select {
		case <-j.done:
		default:
			close(j.done)
		}
	}
}

// Elapsed is how long the job has been running, or ran for, not counting any
// time it spent paused.
func (j *Job) Elapsed() time.Duration {
	return elapsed(j.Started, j.Ended, j.pausedAt, j.pausedFor, j.paused)
}

// held is how long the job has spent stopped, an ongoing pause included.
// Called with the runner's lock held.
func (j *Job) held(now time.Time) time.Duration {
	d := j.pausedFor
	if j.paused && !j.pausedAt.IsZero() {
		d += now.Sub(j.pausedAt)
	}
	return d
}

// elapsed is the one clock both Job and Snapshot read. While a job is paused
// the clock stands still at the moment it was stopped, rather than running on
// and being corrected afterwards.
func elapsed(started, ended, pausedAt time.Time, pausedFor time.Duration, paused bool) time.Duration {
	if started.IsZero() {
		return 0
	}
	end := ended
	if end.IsZero() {
		end = time.Now()
	}
	if paused && !pausedAt.IsZero() && pausedAt.Before(end) {
		end = pausedAt
	}
	d := end.Sub(started) - pausedFor
	if d < 0 {
		return 0
	}
	return d
}

// Command is the copy-pasteable form of what this job runs.
func (j *Job) Command() string { return gfy.Quote(j.Argv) }

// Snapshot is a value copy of the fields the UI renders, taken under the
// runner's lock. The log is shared by pointer: ringbuf.Buf is itself safe for
// concurrent use, and copying a quarter-megabyte buffer per frame would not be.
type Snapshot struct {
	ID     uint64
	Kind   string
	Repo   string
	Label  string
	Cost   gfy.Cost
	Argv   []string
	Status Status
	// Paused, PausedAt and Held are the pause state as of this snapshot. The
	// UI holds a snapshot across ticks and recomputes Elapsed from it, so the
	// clock has to be reconstructible from the value alone.
	Paused   bool
	PausedAt time.Time
	Held     time.Duration
	Exit     int
	Err      string
	Queued   time.Time
	Started  time.Time
	Ended    time.Time
	Log      *ringbuf.Buf
}

// Elapsed mirrors Job.Elapsed for a snapshot.
func (s Snapshot) Elapsed() time.Duration {
	return elapsed(s.Started, s.Ended, s.PausedAt, s.Held, s.Paused)
}

// Command is the copy-pasteable argv.
func (s Snapshot) Command() string { return gfy.Quote(s.Argv) }

// Event is what the UI subscribes to. One small typed message per transition,
// delivered on a buffered channel; the UI forwards it to the main thread with
// glib.IdleAdd and folds it into the board.
type Event struct {
	Job Snapshot
	// Output is true for a log-only event, which the UI coalesces: a running
	// job produces many of these and the log pane redraws on a tick, not on
	// each line.
	Output bool
	// Pause is true for a pause or resume, which is not a lifecycle change:
	// the job is Running before and after, and the UI only has to repaint.
	// The flag is what keeps it out of the run history and out of the
	// "started" log line.
	Pause bool
}

// Options configures a runner.
type Options struct {
	// FreeLanes and MeteredLanes are the two concurrency limits. Zero means
	// the defaults: min(4, NumCPU/2) free and exactly one metered.
	FreeLanes    int
	MeteredLanes int
	// LogBytes is the per-job log budget; zero means ringbuf.DefaultCap.
	LogBytes int
	// History is how many finished jobs are retained in memory. Zero means
	// DefaultHistory.
	History int
	// KillGrace is how long a cancelled process group has between SIGTERM and
	// SIGKILL. Zero means DefaultKillGrace.
	KillGrace time.Duration
	// Precheck is consulted for every job before it is queued; a non-nil error
	// refuses the submission and is returned to the caller.
	//
	// It lives here, on the one path every job takes, rather than at each
	// button: a precondition enforced per call site is a precondition that is
	// missing from the next call site somebody adds. The runner does not know
	// what a graph is — the UI supplies the rule — but it does guarantee that
	// whatever the rule is, nothing runs around it.
	Precheck func(*Job) error
}

// DefaultHistory bounds the in-memory job list.
const DefaultHistory = 200

// DefaultKillGrace is how long a subprocess has to exit on its own after
// SIGTERM. graphify flushes graph.json on the way out, and killing it mid-write
// is how an output directory ends up in StateBroken — so the grace is generous
// rather than snappy.
const DefaultKillGrace = 5 * time.Second

// Runner owns the queue and the running processes.
type Runner struct {
	opts Options

	mu      sync.Mutex
	nextID  uint64
	queue   []*Job
	running map[uint64]*Job
	history []*Job
	// busyRepo is the set of repositories with a mutating job in flight. Two
	// mutating jobs against one output directory would race each other's
	// graph.json; read-only jobs are unaffected and run freely.
	busyRepo map[string]uint64

	events chan Event
	wake   chan struct{}
	closed atomic.Bool
	wg     sync.WaitGroup
}

// New starts a runner. Close stops it.
func New(opts Options) *Runner {
	if opts.FreeLanes <= 0 {
		opts.FreeLanes = defaultFreeLanes()
	}
	// Never inferred. The metered lane is the one that spends money, and a
	// caller passing 0 is a caller that did not think about it.
	if opts.MeteredLanes <= 0 {
		opts.MeteredLanes = 1
	}
	if opts.History <= 0 {
		opts.History = DefaultHistory
	}
	if opts.KillGrace <= 0 {
		opts.KillGrace = DefaultKillGrace
	}
	r := &Runner{
		opts:     opts,
		running:  map[uint64]*Job{},
		busyRepo: map[string]uint64{},
		events:   make(chan Event, 256),
		wake:     make(chan struct{}, 1),
	}
	r.wg.Add(1)
	go r.loop()
	return r
}

func defaultFreeLanes() int {
	n := runtime.NumCPU() / 2
	if n < 1 {
		n = 1
	}
	if n > 4 {
		n = 4
	}
	return n
}

// Events is the UI's subscription. It is buffered; a UI that stops draining is
// dropped from rather than allowed to block a subprocess.
func (r *Runner) Events() <-chan Event { return r.events }

// SetLanes changes the concurrency limits live, from the settings dialog.
// Lowering a limit never kills a running job — it only stops the next one from
// starting.
func (r *Runner) SetLanes(free, metered int) {
	r.mu.Lock()
	if free > 0 {
		r.opts.FreeLanes = free
	}
	if metered > 0 {
		r.opts.MeteredLanes = metered
	}
	r.mu.Unlock()
	r.kick()
}

// Lanes reports the current limits.
func (r *Runner) Lanes() (free, metered int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.opts.FreeLanes, r.opts.MeteredLanes
}

// Submit queues a job and returns it. The caller has already built and shown
// the argv: this function does not decide what to run, only when.
func (r *Runner) Submit(j *Job) *Job {
	if j.Log == nil {
		j.Log = ringbuf.New(r.opts.LogBytes)
	}
	if j.done == nil {
		j.done = make(chan struct{})
	}
	r.mu.Lock()
	r.nextID++
	j.ID = r.nextID
	j.Status = Queued
	j.Queued = time.Now()
	r.queue = append(r.queue, j)
	snap := snapshot(j)
	r.mu.Unlock()

	r.emit(Event{Job: snap})
	r.kick()
	return j
}

// SubmitCmd is the common path: build the argv from a kind and parameters,
// wrap it in a job, queue it.
func (r *Runner) SubmitCmd(kind, repo, label string, p gfy.Params, env gfy.Env) (*Job, error) {
	argv := gfy.Argv(kind, p)
	if argv == nil {
		return nil, errors.New("jobs: no command builder for kind " + kind)
	}
	dir := p.Repo
	if dir == "" {
		dir = repo
	}
	j := &Job{
		Kind:  kind,
		Repo:  repo,
		Label: label,
		Cost:  gfy.CostOf(kind),
		Argv:  argv,
		// ClaudeCLIEnv is what makes a Claude Code install that is not on the
		// session PATH — the usual shape of a .desktop launch — reachable by
		// graphify, which launches the CLI by its bare name. ClaudeAccountEnv
		// then picks which login it runs as, for the backend and for the
		// installer alike.
		Env: gfy.ClaudeAccountEnv(gfy.ClaudeCLIEnv(gfy.JobEnv(env), p.Backend), p.ClaudeDir),
		Dir: dir,
		Out: p.Out,
	}
	if pre := r.precheck(); pre != nil {
		if err := pre(j); err != nil {
			return nil, err
		}
	}
	return r.Submit(j), nil
}

// precheck reads the hook under the lock, because SetLanes' sibling settings
// paths mutate opts live.
func (r *Runner) precheck() func(*Job) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.opts.Precheck
}

// Cancel signals a job. A queued job is dropped; a running one has its process
// group terminated.
func (r *Runner) Cancel(id uint64) bool {
	r.mu.Lock()
	for i, j := range r.queue {
		if j.ID != id {
			continue
		}
		r.queue = append(r.queue[:i], r.queue[i+1:]...)
		j.Status = Canceled
		j.Ended = time.Now()
		j.finish()
		r.retire(j)
		snap := snapshot(j)
		r.mu.Unlock()
		r.emit(Event{Job: snap})
		return true
	}
	j := r.running[id]
	r.mu.Unlock()
	if j == nil {
		return false
	}
	if j.cancel != nil {
		j.cancel()
	}
	return true
}

// Pause stops a running job's process group. Resume starts it again.
//
// It is SIGSTOP/SIGCONT on the whole group rather than on the parent, for the
// same reason Cancel signals the group: graphify is a Python parent whose AST
// workers hold the CPU, and stopping only the parent would pause the bookkeeping
// while the fan-out kept burning cores.
//
// Only a running job can be paused — a queued one is not consuming anything, and
// pausing it would mean "hold a lane", which is Cancel's job, not this one.
// Both report whether they changed anything.
func (r *Runner) Pause(id uint64) bool { return r.setPaused(id, true) }

// Resume continues a paused job's process group.
func (r *Runner) Resume(id uint64) bool { return r.setPaused(id, false) }

// Paused reports whether a job is currently stopped.
func (r *Runner) Paused(id uint64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	j := r.running[id]
	return j != nil && j.paused
}

func (r *Runner) setPaused(id uint64, want bool) bool {
	r.mu.Lock()
	j := r.running[id]
	// pgid is zero for the instant between dispatch and exec.Start; there is
	// no group to signal yet, so the caller is told nothing happened rather
	// than being given a pause that silently did not take.
	if j == nil || j.Status != Running || j.pgid == 0 || j.paused == want {
		r.mu.Unlock()
		return false
	}
	pgid := j.pgid
	r.mu.Unlock()

	sig := syscall.SIGCONT
	if want {
		sig = syscall.SIGSTOP
	}
	if err := syscall.Kill(-pgid, sig); err != nil {
		return false
	}

	now := time.Now()
	r.mu.Lock()
	if want {
		j.pausedAt = now
	} else {
		if !j.pausedAt.IsZero() {
			j.pausedFor += now.Sub(j.pausedAt)
		}
		j.pausedAt = time.Time{}
	}
	j.paused = want
	snap := snapshot(j)
	r.mu.Unlock()

	if want {
		j.Log.WriteString("\nggraphify: paused — SIGSTOP to the process group\n")
	} else {
		j.Log.WriteString("ggraphify: resumed — SIGCONT to the process group\n")
	}
	r.emit(Event{Job: snap, Pause: true})
	return true
}

// CancelAll cancels everything queued and running. The window's close handler
// calls it: a watcher started from the UI must not outlive the UI.
func (r *Runner) CancelAll() {
	for _, s := range r.Snapshot() {
		if !s.Status.Done() {
			r.Cancel(s.ID)
		}
	}
}

// Busy reports the id of the mutating job holding a repository, if any.
func (r *Runner) Busy(repo string) (uint64, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	id, ok := r.busyRepo[repo]
	return id, ok
}

// Snapshot returns every job the runner knows about, newest first.
func (r *Runner) Snapshot() []Snapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Snapshot, 0, len(r.queue)+len(r.running)+len(r.history))
	for _, j := range r.queue {
		out = append(out, snapshot(j))
	}
	for _, j := range r.running {
		out = append(out, snapshot(j))
	}
	for _, j := range r.history {
		out = append(out, snapshot(j))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out
}

// Active is the count of queued and running jobs, for the status bar.
func (r *Runner) Active() (queued, running int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.queue), len(r.running)
}

// Close stops the scheduler and cancels everything still in flight.
func (r *Runner) Close() {
	if r.closed.Swap(true) {
		return
	}
	r.CancelAll()
	r.kick()
	r.wg.Wait()
	close(r.events)
}

func (r *Runner) kick() {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// emit delivers an event without ever blocking. A full channel means the UI is
// behind; dropping a log-only event is invisible (the next tick re-reads the
// ring buffer), and dropping a transition is recovered by the UI's periodic
// reconciliation against Snapshot.
func (r *Runner) emit(ev Event) {
	if r.closed.Load() {
		return
	}
	select {
	case r.events <- ev:
	default:
	}
}

// loop is the scheduler. It wakes on submissions, completions and a slow
// timer, and starts whatever the lane limits allow.
func (r *Runner) loop() {
	defer r.wg.Done()
	t := time.NewTicker(500 * time.Millisecond)
	defer t.Stop()
	for {
		r.dispatch()
		if r.closed.Load() {
			r.mu.Lock()
			idle := len(r.running) == 0
			r.mu.Unlock()
			if idle {
				return
			}
		}
		select {
		case <-r.wake:
		case <-t.C:
		}
	}
}

// dispatch starts every job that can start right now.
func (r *Runner) dispatch() {
	for {
		r.mu.Lock()
		j := r.pick()
		if j == nil {
			r.mu.Unlock()
			return
		}
		j.Status = Running
		j.Started = time.Now()
		r.running[j.ID] = j
		if gfy.Known[j.Kind].Mutates && j.Repo != "" {
			r.busyRepo[j.Repo] = j.ID
		}
		snap := snapshot(j)
		r.mu.Unlock()

		r.emit(Event{Job: snap})
		go r.run(j)
	}
}

// pick chooses the next runnable job. Called with the mutex held.
//
// The queue is FIFO within a lane, and a job whose repository is already held
// by a mutating job is skipped rather than blocking the ones behind it — so a
// long extraction on one repository never stalls updates on the other 127.
func (r *Runner) pick() *Job {
	if r.closed.Load() {
		return nil
	}
	free, metered := 0, 0
	for _, j := range r.running {
		if j.Cost == gfy.Metered {
			metered++
		} else {
			free++
		}
	}
	for i, j := range r.queue {
		if j.Cost == gfy.Metered {
			if metered >= r.opts.MeteredLanes {
				continue
			}
		} else if free >= r.opts.FreeLanes {
			continue
		}
		if gfy.Known[j.Kind].Mutates && j.Repo != "" {
			if _, held := r.busyRepo[j.Repo]; held {
				continue
			}
		}
		r.queue = append(r.queue[:i], r.queue[i+1:]...)
		return j
	}
	return nil
}

// run executes one job to completion.
func (r *Runner) run(j *Job) {
	ctx, cancel := context.WithCancel(context.Background())
	r.mu.Lock()
	j.cancel = cancel
	r.mu.Unlock()
	defer cancel()

	j.Log.WriteString("$ " + j.Command() + "\n")
	if disp := j.Env.Display(); len(disp) > 0 {
		for _, line := range disp {
			j.Log.WriteString("  env " + line + "\n")
		}
	}

	cmd := exec.Command(j.Argv[0], j.Argv[1:]...)
	cmd.Dir = j.Dir
	cmd.Env = j.Env.Compose(os.Environ())
	cmd.Stdout = logWriter{buf: j.Log, r: r, job: j}
	cmd.Stderr = cmd.Stdout
	// stdin is /dev/null rather than inherited: a graphify that decides to
	// prompt must fail fast instead of hanging forever on a terminal the GUI
	// does not have.
	cmd.Stdin = nil
	// Its own process group, so cancel reaches the AST worker subprocesses too.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	// exited is closed the instant Wait returns, so the reaper can tell a
	// normal exit apart from a cancellation. Without it the deferred cancel
	// below wakes the reaper on every successful run and it logs a SIGTERM
	// that was never sent.
	exited := make(chan struct{})
	err := cmd.Start()
	if err == nil {
		r.mu.Lock()
		j.pgid = cmd.Process.Pid
		r.mu.Unlock()
		go r.reap(ctx, exited, j, cmd)
		err = cmd.Wait()
	}
	close(exited)

	r.mu.Lock()
	j.Ended = time.Now()
	// A cancelled job can be paused at the moment it dies: reap sends SIGCONT
	// so the SIGTERM lands. Close the open pause here so the final duration
	// counts the time it was stopped as stopped.
	if j.paused {
		if !j.pausedAt.IsZero() {
			j.pausedFor += j.Ended.Sub(j.pausedAt)
		}
		j.pausedAt = time.Time{}
		j.paused = false
	}
	switch {
	case ctx.Err() != nil:
		j.Status = Canceled
		j.Exit = -1
	case err == nil:
		j.Status = Succeeded
		j.Exit = 0
	default:
		j.Status = Failed
		j.Err = err
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			j.Exit = ee.ExitCode()
		} else {
			j.Exit = -1
			j.Log.WriteString("ggraphify: " + err.Error() + "\n")
		}
	}
	j.finish()
	r.retire(j)
	delete(r.running, j.ID)
	if id, ok := r.busyRepo[j.Repo]; ok && id == j.ID {
		delete(r.busyRepo, j.Repo)
	}
	snap := snapshot(j)
	r.mu.Unlock()

	r.emit(Event{Job: snap})
	r.kick()
}

// reap turns a cancelled context into a terminated process group: SIGTERM to
// the group, then SIGKILL after the grace period if it is still there.
func (r *Runner) reap(ctx context.Context, exited <-chan struct{}, j *Job, cmd *exec.Cmd) {
	select {
	case <-exited:
		return // it finished on its own; there is nothing to signal
	case <-ctx.Done():
	}
	if cmd.Process == nil {
		return
	}
	pgid := -cmd.Process.Pid
	j.Log.WriteString("\nggraphify: cancelled — sending SIGTERM to the process group\n")
	_ = syscall.Kill(pgid, syscall.SIGTERM)
	// A paused group is stopped, and a stopped process does not act on the
	// pending SIGTERM until it runs again. SIGCONT unconditionally: it is a
	// no-op on a group that was never paused, and without it cancelling a
	// paused job would wait out the whole grace period and then SIGKILL —
	// exactly the mid-write kill the grace exists to avoid.
	_ = syscall.Kill(pgid, syscall.SIGCONT)

	grace := time.NewTimer(r.opts.KillGrace)
	defer grace.Stop()
	done := make(chan struct{})
	go func() {
		// Poll rather than Wait: cmd.Wait is already owned by run().
		for {
			if err := syscall.Kill(pgid, 0); err != nil {
				close(done)
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
	}()
	select {
	case <-done:
	case <-grace.C:
		j.Log.WriteString("ggraphify: still alive after the grace period — SIGKILL\n")
		_ = syscall.Kill(pgid, syscall.SIGKILL)
	}
}

// retire moves a finished job into the bounded history. Called with the lock.
func (r *Runner) retire(j *Job) {
	r.history = append([]*Job{j}, r.history...)
	if len(r.history) > r.opts.History {
		r.history = r.history[:r.opts.History]
	}
}

func snapshot(j *Job) Snapshot {
	s := Snapshot{
		ID: j.ID, Kind: j.Kind, Repo: j.Repo, Label: j.Label, Cost: j.Cost,
		Argv: j.Argv, Status: j.Status, Exit: j.Exit,
		Queued: j.Queued, Started: j.Started, Ended: j.Ended, Log: j.Log,
		Paused: j.paused, PausedAt: j.pausedAt, Held: j.pausedFor,
	}
	if j.Err != nil {
		s.Err = j.Err.Error()
	}
	return s
}

// logWriter funnels a subprocess's output into the ring buffer and nudges the
// UI. The nudge is a single coalescing event: the pane renders from the buffer
// on its own tick, so one event per write is enough and a dropped one costs
// nothing.
type logWriter struct {
	buf *ringbuf.Buf
	r   *Runner
	job *Job
}

func (w logWriter) Write(p []byte) (int, error) {
	n, err := w.buf.Write(p)
	w.r.mu.Lock()
	snap := snapshot(w.job)
	w.r.mu.Unlock()
	w.r.emit(Event{Job: snap, Output: true})
	return n, err
}
