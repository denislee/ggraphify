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

	"github.com/dns/ggraphify/internal/applog"
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
	// Local is true for a metered job running against a model on this
	// machine. It is deliberately NOT a third value of Cost: the cost of a
	// kind is what decides whether a confirm dialog stands in front of it, and
	// a local extraction still needs that gate — it is hours of this machine,
	// started by one click. What it does not need is the metered lane, whose
	// whole purpose is to stop parallel API requests becoming a parallel bill.
	// A local run has no bill to parallelize, so it gets its own limit.
	Local bool
	Argv  []string
	Env   gfy.Env
	Dir   string // working directory; normally the checkout root
	Out   string // the graphify-out directory this job reads or writes

	// Held is true for a job that sits in the queue without being dispatched.
	// It is not a fourth status: the job IS queued, and everything that reads
	// Status — the row, the history, Active — is right to say so. What Held
	// adds is consent. A queue restored from the previous session is a queue
	// nobody clicked, so the jobs in it that cost something (an LLM bill, or
	// hours of this machine) come back held, and Release is the click that
	// was missing.
	Held bool

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
	// lease is this job's claim on the local model server, held for exactly
	// as long as its process is running and suspended while it is paused. It
	// lives on the job rather than as a local in run() because pausing
	// happens on another goroutine entirely — the one that handled the click
	// — and that goroutine has to be able to reach it.
	lease Lease
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
	ID    uint64
	Kind  string
	Repo  string
	Label string
	Cost  gfy.Cost
	Local bool
	Argv  []string
	// Dir and Out travel with the snapshot because a snapshot is what the
	// session outlives the process as: restoring a queue from the sidecar
	// needs the working directory and the output directory the job was built
	// for, and neither is recoverable from the argv alone.
	Dir    string
	Out    string
	Status Status
	Held   bool
	// Paused, PausedAt and PausedFor are the pause state as of this snapshot.
	// The UI holds a snapshot across ticks and recomputes Elapsed from it, so
	// the clock has to be reconstructible from the value alone.
	Paused    bool
	PausedAt  time.Time
	PausedFor time.Duration
	Exit      int
	Err       string
	Queued    time.Time
	Started   time.Time
	Ended     time.Time
	Log       *ringbuf.Buf
}

// Elapsed mirrors Job.Elapsed for a snapshot.
func (s Snapshot) Elapsed() time.Duration {
	return elapsed(s.Started, s.Ended, s.PausedAt, s.PausedFor, s.Paused)
}

// Command is the copy-pasteable argv.
func (s Snapshot) Command() string { return gfy.Quote(s.Argv) }

// Event is what the UI subscribes to. One small typed message per transition,
// delivered on a buffered channel; the UI forwards it to the main thread with
// glib.IdleAdd and folds it into the board.
type Event struct {
	// ID is the job this event is about, on EVERY event. Prefer it to
	// Job.ID: an Output event carries no snapshot at all (see below), so
	// Job.ID is zero there and a consumer filtering on it silently drops the
	// entire log stream.
	ID uint64
	// Job is the job's state at the moment of the event — on transitions
	// only. It is deliberately NOT populated for an Output event: building it
	// meant taking the runner's global lock, the same one that serializes
	// dispatch, Snapshot, Cancel and completion, once per pipe read from every
	// parallel job, to copy a struct the UI then discarded. ID and Gen are
	// everything an output event actually says.
	Job Snapshot
	// Gen is the log ring buffer's generation as of this event, for an Output
	// event. A pane whose last rendered generation still matches has nothing
	// to redraw.
	Gen uint64
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
	// FreeLanes, MeteredLanes and LocalLanes are the three concurrency
	// limits. Zero means the defaults: min(4, NumCPU/2) free, exactly one
	// metered, and DefaultLocalLanes local.
	//
	// Local is separate from metered because the two limits answer different
	// questions. The metered lane bounds SPEND — one request at a time so a
	// fan-out cannot become a fan-out bill — and one is the only defensible
	// default for it. The local lane bounds this MACHINE, where the constraint
	// is cores, memory and the model server's own slot count, and where the
	// right number is whatever keeps all three busy without swapping.
	FreeLanes    int
	MeteredLanes int
	LocalLanes   int
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

	// LocalLease is consulted for every job just before its process starts,
	// and the handle it returns follows that process for the rest of its
	// life. It is how the local model server is brought up for a job that
	// needs one and taken down again when none do; a nil hook, or a nil
	// return, means the job simply runs.
	//
	// It lives here for the same reason Precheck does — this is the one path
	// every job takes, and a server started per call site is a server the
	// next call site forgets — and it is deliberately one hook returning its
	// own undo rather than a Before/After pair: the runner cannot leak a
	// lease it was never given the option of dropping.
	//
	// It is called on the job's own goroutine and MAY BLOCK for as long as a
	// cold server takes. That costs the lane the job is already holding and
	// nothing else, which is the correct thing to spend: the job cannot run
	// before its server answers anyway.
	LocalLease func(*Job) Lease
}

// Lease is a job's claim on a resource that exists only while it runs — in
// practice the local model server, though the runner is deliberately told
// nothing about what it is leasing.
//
// Three transitions, because a job has three ends and not one: it finishes
// (Release), and it can stop consuming the resource without finishing
// (Suspend) and start again later (Resume), which is what pausing it is. The
// runner calls them in exactly the places it signals the process group, so
// whatever is on the other end tracks the process rather than the job record.
//
// Every method must tolerate being called in any order and repeatedly: the
// runner pairs them correctly, but a cancelled-while-paused job reaches
// Release through a path no caller should have to reason about.
type Lease interface {
	// Suspend gives the resource up while the process is stopped.
	Suspend()
	// Resume takes it back, and MAY BLOCK: the runner does not send SIGCONT
	// until this has returned, so a resumed process never wakes into a
	// resource that is not there yet.
	Resume()
	// Release gives it up for good.
	Release()
}

// DefaultHistory bounds the in-memory job list.
const DefaultHistory = 200

// DefaultLocalLanes is how many jobs run at once against a model on this
// machine.
//
// Two, not one, because a graphify run is two phases with two different
// bottlenecks: a cpu_count-wide AST pass that touches the model server not at
// all, and a semantic pass that is nothing but requests to it. One job at a
// time leaves the model idle for every AST phase in the sweep; a second job
// fills exactly those gaps. Beyond two the gaps are already covered and the
// added jobs mostly contend for memory, which is why this is a default and a
// setting rather than a derived maximum.
//
// Two, not more, also because it is a default and defaults have to be safe on
// the smallest machine that runs this board — every extra lane is another
// graphify process holding another corpus in RAM.
const DefaultLocalLanes = 2

// lane is which of the three concurrency limits a job counts against. It is
// derived rather than stored so that a Job built by hand — the retry path, a
// test — cannot land in a lane that disagrees with its own cost.
type lane int

const (
	laneFree lane = iota
	laneMetered
	laneLocal
)

func laneOf(cost gfy.Cost, local bool) lane {
	switch {
	case cost != gfy.Metered:
		return laneFree
	case local:
		return laneLocal
	}
	return laneMetered
}

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
	// quit is closed at the start of Close, to release a status emit parked on
	// a full events channel. It is the only thing that unblocks such a send.
	quit   chan struct{}
	closed atomic.Bool
	wg     sync.WaitGroup
	// emitMu makes "is it closed?" and "send on it" one indivisible step.
	// Every emit holds it for reading, Close holds it for writing around the
	// close itself, so the channel cannot be closed between an emit's check
	// and its send. wg covers only loop(); the per-job goroutines are not in
	// it and outlive Close, which is exactly who this guards against.
	emitMu sync.RWMutex
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
	if opts.LocalLanes <= 0 {
		opts.LocalLanes = DefaultLocalLanes
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
		quit:     make(chan struct{}),
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
func (r *Runner) SetLanes(free, metered, local int) {
	r.mu.Lock()
	if free > 0 {
		r.opts.FreeLanes = free
	}
	if metered > 0 {
		r.opts.MeteredLanes = metered
	}
	if local > 0 {
		r.opts.LocalLanes = local
	}
	r.mu.Unlock()
	r.kick()
}

// Lanes reports the current limits.
func (r *Runner) Lanes() (free, metered, local int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.opts.FreeLanes, r.opts.MeteredLanes, r.opts.LocalLanes
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

	r.emit(Event{ID: snap.ID, Job: snap})
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
	// OpenCode Go is a custom provider, and `--backend opencode-go` means
	// nothing to graphify until it is written to ~/.graphify/providers.json.
	// Doing it here rather than at the call site is the same reason the env
	// overlays are composed here: ggraphify-job submits through this function
	// too, and a headless sweep must not be the one path that launches a
	// backend graphify will reject. The model goes in as well, because the
	// entry carries the price pair graphify estimates the run's cost from and
	// that pair is per model. A failure to write refuses the submit — the job
	// would otherwise fail per repository with graphify's own "unknown
	// backend", once for each of them.
	// One token asked of the gateway before twenty-five files are: a model can
	// be in the gateway's own listing and still refuse this account or region,
	// and graphify reports that as "all semantic chunks failed" with no cause.
	// Cached per model per process, so a sweep pays it once.
	if err := gfy.PreflightOpenCode(p.Backend, p.Model); err != nil {
		return nil, errors.New("jobs: " + err.Error())
	}
	if path, wrote, err := gfy.EnsureOpenCodeProvider(p.Backend, p.Model); err != nil {
		return nil, errors.New("jobs: cannot register the " + gfy.OpenCodeBackend +
			" provider in " + path + ": " + err.Error())
	} else if wrote {
		applog.Infof("registered the %s provider in %s (model %s)", gfy.OpenCodeBackend, path, p.Model)
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
		// Which lane this takes is decided here, from the backend the argv was
		// built for, rather than read back out of the argv later. The two can
		// only disagree if somebody rewrites one of them.
		Local: gfy.IsLocalBackend(p.Backend),
		Argv:  argv,
		// ClaudeCLIEnv is what makes a Claude Code install that is not on the
		// session PATH — the usual shape of a .desktop launch — reachable by
		// graphify, which launches the CLI by its bare name. ClaudeAccountEnv
		// then picks which login it runs as, for the backend and for the
		// installer alike. LocalEnv is the same shape for the other end of the
		// range: it unlocks graphify's ollama concurrency clamp, without which
		// the --max-concurrency already in the argv is discarded and a
		// multi-slot local server is driven one chunk at a time.
		//
		// All three belong here rather than at the call sites: ggraphify-job
		// submits through this function too, and an overlay applied in the GUI
		// only is an overlay a headless sweep runs without.
		Env: gfy.ClaudeAccountEnv(
			gfy.ClaudeCLIModelEnv(
				gfy.ClaudeCLIEnv(gfy.LocalEnv(gfy.JobEnv(env), p.Backend), p.Backend),
				p.Backend, p.Model),
			p.ClaudeDir),
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

// localLease reads the hook under the lock, for the same reason precheck does.
func (r *Runner) localLease() func(*Job) Lease {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.opts.LocalLease
}

// precheck reads the hook under the lock, because SetLanes' sibling settings
// paths mutate opts live.
func (r *Runner) precheck() func(*Job) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.opts.Precheck
}

// Restore puts jobs from a previous session back into the runner: the ones
// that had finished into the history, the ones that had not into the queue.
//
// It is deliberately not Submit in a loop. Submit is the path a click takes,
// and a click means somebody asked for this now; Restore is the path a
// restart takes, and what it reconstructs is a queue nobody has looked at
// yet. So it bypasses Precheck — every one of these jobs passed it when it
// was first submitted, and re-running a check against a repository that has
// changed since would silently drop work rather than let it fail visibly —
// and it honours Held, which is how a restored metered job waits for the
// click that a restored AST update does not need.
//
// Jobs are taken oldest first, so the ids they are given ascend in the order
// the previous session created them. No events are emitted: the UI calls this
// while it is still building itself, before anything is draining the channel,
// and it reloads its views from Snapshot once Restore returns.
func (r *Runner) Restore(js []*Job) int {
	n := 0
	r.mu.Lock()
	for _, j := range js {
		if j == nil {
			continue
		}
		if j.Log == nil {
			j.Log = ringbuf.New(r.opts.LogBytes)
		}
		if j.done == nil {
			j.done = make(chan struct{})
		}
		r.nextID++
		j.ID = r.nextID
		n++
		if j.Status.Done() {
			j.finish()
			// retire prepends, so feeding the history oldest-first leaves it
			// newest-first, which is the order every reader assumes.
			r.retire(j)
			continue
		}
		// Anything that was not finished is queued, the jobs that were mid-
		// flight included: their process group died with the last session, so
		// "running" is not a state this one can inherit.
		j.Status = Queued
		j.Started = time.Time{}
		j.Ended = time.Time{}
		r.queue = append(r.queue, j)
	}
	r.mu.Unlock()
	r.kick()
	return n
}

// Release lets a held job be dispatched. It reports whether it changed
// anything, so a caller can tell "started it" from "it was never held".
func (r *Runner) Release(id uint64) bool {
	r.mu.Lock()
	var snap Snapshot
	found := false
	for _, j := range r.queue {
		if j.ID != id || !j.Held {
			continue
		}
		j.Held = false
		snap = snapshot(j)
		found = true
		break
	}
	r.mu.Unlock()
	if !found {
		return false
	}
	r.emit(Event{ID: snap.ID, Job: snap})
	r.kick()
	return true
}

// ReleaseAll lets every held job go at once and returns how many that was.
func (r *Runner) ReleaseAll() int {
	r.mu.Lock()
	var snaps []Snapshot
	for _, j := range r.queue {
		if !j.Held {
			continue
		}
		j.Held = false
		snaps = append(snaps, snapshot(j))
	}
	r.mu.Unlock()
	for _, s := range snaps {
		r.emit(Event{ID: s.ID, Job: s})
	}
	if len(snaps) > 0 {
		r.kick()
	}
	return len(snaps)
}

// HeldCount is how many queued jobs are waiting for a click, for the button
// that offers to give them one.
func (r *Runner) HeldCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, j := range r.queue {
		if j.Held {
			n++
		}
	}
	return n
}

// ParseStatus turns a Status back from its String form, which is what the
// sidecar stores. The second result is false for anything it does not
// recognise, so a hand-edited or future-versioned state file degrades to "we
// do not know what this was" rather than to Queued, which would re-run it.
func ParseStatus(s string) (Status, bool) {
	switch s {
	case "queued":
		return Queued, true
	case "running":
		return Running, true
	case "ok":
		return Succeeded, true
	case "failed":
		return Failed, true
	case "canceled":
		return Canceled, true
	}
	return Queued, false
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
		r.emit(Event{ID: snap.ID, Job: snap})
		return true
	}
	j := r.running[id]
	// Copy the cancel out under the lock. Reading j.cancel after releasing it
	// races run()'s assignment, and the stale answer it can return is nil —
	// which would make this report a cancellation it never performed and leave
	// the process group running. dispatch now installs the cancel before the
	// job is in r.running, so a job found here always has one.
	var cancel context.CancelFunc
	if j != nil {
		cancel = j.cancel
	}
	r.mu.Unlock()
	if j == nil {
		return false
	}
	if cancel != nil {
		cancel()
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
// Both carry the job's Lease with them — suspended when the process stops,
// taken back before it is woken — so pausing a job that uses the local model
// server gives the server up too. Holding several gigabytes of weights
// resident for a process that is frozen and will not send another request is
// precisely what somebody pausing it asked not to happen.
//
// RESUME THEREFORE BLOCKS for as long as the server takes to come back, which
// is up to about ten seconds on a cold start. Call it off any thread that
// draws.
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
	lease := j.lease
	r.mu.Unlock()

	// A resume takes the lease back BEFORE the process is woken, and blocks
	// while whatever is on the other end gets ready. The order is the whole
	// point: a SIGCONT sent first would wake graphify into a model server
	// that the pause took away, and it would find that out as a connection
	// error rather than as a wait.
	if !want && lease != nil {
		lease.Resume()
	}

	sig := syscall.SIGCONT
	if want {
		sig = syscall.SIGSTOP
	}
	if err := syscall.Kill(-pgid, sig); err != nil {
		// A resume that could not signal leaves the job paused, so the lease
		// it just took has to go back — otherwise a job that is not running
		// holds the server up for as long as it stays that way.
		if !want && lease != nil {
			lease.Suspend()
		}
		return false
	}

	// A pause gives the lease up only once the process is actually stopped.
	// This way round because a frozen process will not send another request,
	// where one still running might send into a server being shut down — and
	// because the reverse order would make a failed SIGSTOP cost the server
	// for nothing.
	if want && lease != nil {
		lease.Suspend()
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
	r.emit(Event{ID: snap.ID, Job: snap, Pause: true})
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

// Get returns one job by id: a map lookup and a single copy.
//
// It exists because the callers that want exactly one job were reaching for
// Snapshot and scanning the result — which allocates a slice of every queued,
// running and historical job (up to DefaultHistory of them), copies each one,
// and sorts the lot, all under the runner's global lock, to answer a question
// about one. On a tick, per open pane.
//
// Snapshot stays for the views that genuinely render the whole list.
func (r *Runner) Get(id uint64) (Snapshot, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if j, ok := r.running[id]; ok {
		return snapshot(j), true
	}
	// Queue and history are slices rather than maps, but both are short and
	// bounded — the queue by what a person has submitted, the history by
	// opts.History — and a running job is the overwhelmingly common lookup.
	for _, j := range r.queue {
		if j.ID == id {
			return snapshot(j), true
		}
	}
	for _, j := range r.history {
		if j.ID == id {
			return snapshot(j), true
		}
	}
	return Snapshot{}, false
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
	// Released first, before anything that waits: a status change sends
	// blocking now, so an emit already past the closed check and parked on a
	// full channel has to be let go before Close can take emitMu below.
	close(r.quit)
	r.CancelAll()
	r.kick()
	r.wg.Wait()
	// closed is already true, so every emit that acquires the read lock from
	// here on returns without sending. Taking the write lock waits out the
	// ones that had already passed that check and were about to send — the
	// window that otherwise closes this channel underneath a live send and
	// panics the whole application on quit.
	r.emitMu.Lock()
	close(r.events)
	r.emitMu.Unlock()
}

func (r *Runner) kick() {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// emit delivers an event. Whether it may be dropped depends on what it says.
//
// A log-only event is droppable and always was: it means "there is more" and
// the next tick re-reads the ring buffer, so a full channel costs nothing but
// a few milliseconds of latency. There are thousands of them.
//
// A STATUS CHANGE is not droppable, and treating it as if it were is how a
// burst of output from several parallel jobs came to fill the 256-slot buffer
// just in time for the completion event behind it to be thrown away. In the
// GUI that leaves a row stuck at "running" until the next full redraw. In
// ggraphify-job, whose entire body is a range over this channel waiting for
// Status.Done(), it is a hang: the terminal event never arrives and nothing
// else ever closes the channel.
//
// So transitions send blocking. The only thing that can be waiting on the
// other side is a consumer that will drain, and a runner that blocks briefly
// on a slow UI is strictly better than one that silently loses the one event
// a caller cannot reconstruct.
func (r *Runner) emit(ev Event) {
	// Read-locked, so any number of jobs emit concurrently as before and only
	// Close is exclusive. The check has to happen inside the lock: outside it
	// it is a stale answer by the time the send runs.
	r.emitMu.RLock()
	defer r.emitMu.RUnlock()
	if r.closed.Load() {
		return
	}
	if ev.Output {
		select {
		case r.events <- ev:
		default:
		}
		return
	}
	// Blocking, but never forever: quit is closed at the top of Close, so a
	// shutdown that nobody is draining for cannot wedge the runner — and
	// cannot wedge Close itself behind the emitMu this send is holding.
	select {
	case r.events <- ev:
	case <-r.quit:
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
		// The cancel is installed here, under the same lock that publishes the
		// job into r.running, so there is no instant in which a job is
		// cancellable-looking but not actually cancellable. Creating it inside
		// run() left exactly that gap: dispatch had already published the job
		// and Cancel could find it with a nil cancel and do nothing.
		ctx, cancel := context.WithCancel(context.Background())
		j.cancel = cancel
		r.running[j.ID] = j
		if gfy.Known[j.Kind].Mutates && j.Repo != "" {
			r.busyRepo[j.Repo] = j.ID
		}
		snap := snapshot(j)
		r.mu.Unlock()

		r.emit(Event{ID: snap.ID, Job: snap})
		go r.run(j, ctx, cancel)
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
	var busy [3]int
	for _, j := range r.running {
		busy[laneOf(j.Cost, j.Local)]++
	}
	limit := [3]int{r.opts.FreeLanes, r.opts.MeteredLanes, r.opts.LocalLanes}
	for i, j := range r.queue {
		// A held job holds nothing: it is skipped where it stands and the
		// queue behind it runs, which is what makes a restored sweep of 128
		// free updates start immediately while the metered ones wait for a
		// click.
		if j.Held {
			continue
		}
		if l := laneOf(j.Cost, j.Local); busy[l] >= limit[l] {
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
// The context and its cancel are made by dispatch, not here, so that a job is
// never visible in r.running without the means to stop it.
func (r *Runner) run(j *Job, ctx context.Context, cancel context.CancelFunc) {
	defer cancel()

	j.Log.WriteString("$ " + j.Command() + "\n")
	if disp := j.Env.Display(); len(disp) > 0 {
		for _, line := range disp {
			j.Log.WriteString("  env " + line + "\n")
		}
	}

	// The model server this job needs, up before the process that will talk
	// to it and released once that process is gone.
	//
	// It is published on the job under the lock before the process starts, so
	// that a pause arriving the instant it does finds a lease to suspend.
	// setPaused cannot fire earlier than this: it refuses a job whose pgid is
	// still zero, and the pgid is not set until exec.Start below has returned.
	//
	// release gives the lease back, at most once however many times it is
	// called. The explicit call after the process exits is the one that
	// normally fires; the defer is there for an early return that never
	// reaches it, including a process that failed to start.
	//
	// Once rather than relying on the lease: the interface asks implementations
	// to tolerate repeated calls, but "the runner returns each lease exactly
	// once" is a property of the runner, and one a counting test double is
	// entitled to check.
	release := func() {}
	if hook := r.localLease(); hook != nil {
		if l := hook(j); l != nil {
			r.mu.Lock()
			j.lease = l
			r.mu.Unlock()
			var once sync.Once
			release = func() { once.Do(l.Release) }
			defer release()
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

	// The lease goes back HERE — the process is gone, so the resource it was
	// holding is genuinely free — and not on the way out of this function.
	//
	// A deferred release runs after the terminal bookkeeping below: after
	// j.finish() closes done, after the terminal event is published, and after
	// kick() has dispatched the next job. That ordering makes the job
	// observably complete while its lease is still outstanding, so Leases()
	// over-counts for that window and the next job takes its own lease before
	// this one has been returned — which is what a settings subtitle reading
	// "1 lease out" after the last job finished was reporting, correctly.
	release()

	r.mu.Lock()
	j.Ended = time.Now()
	// A cancelled job can be paused at the moment it dies: reap sends SIGCONT
	// so the SIGTERM lands. Close the open pause here so the final duration
	// counts the time it was stopped as stopped.
	if j.paused {
		j.pausedFor = j.held(j.Ended)
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

	r.emit(Event{ID: snap.ID, Job: snap})
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
		Local: j.Local,
		Argv:  j.Argv, Dir: j.Dir, Out: j.Out,
		Status: j.Status, Held: j.Held, Exit: j.Exit,
		Queued: j.Queued, Started: j.Started, Ended: j.Ended, Log: j.Log,
		Paused: j.paused, PausedAt: j.pausedAt, PausedFor: j.pausedFor,
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

// Write is on the hot path of every running job — once per pipe read — so it
// touches no shared state but the ring buffer's own mutex.
//
// It used to take the runner's global lock to build a full Snapshot, which put
// every parallel job's output in contention with dispatch, cancellation and
// completion in order to produce a struct copy the UI read two fields of. The
// event says "job N is at generation G"; the panes render from the buffer on
// their tick, and a tick that finds the generation unmoved skips the render
// entirely.
func (w logWriter) Write(p []byte) (int, error) {
	n, err := w.buf.Write(p)
	w.r.emit(Event{ID: w.job.ID, Gen: w.buf.Gen(), Output: true})
	return n, err
}
