package jobs

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dns/ggraphify/internal/gfy"
)

// fakeGraphify writes an executable script and points $GRAPHIFY_BIN at it, so
// the whole runner can be exercised without graphify installed and without
// spending a cent. This script is the contract test for internal/gfy: it
// echoes its argv, so a test can assert on exactly what was exec'd.
func fakeGraphify(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "graphify")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GRAPHIFY_BIN", p)
	return p
}

// repoDir is a real directory to use as a checkout path. The runner sets the
// subprocess's working directory to it, so a made-up path would fail exec
// before graphify ever ran — which is correct behaviour, and not what these
// tests are about.
func repoDir(t *testing.T) string { return t.TempDir() }

// wait drains events until the job reaches a terminal state, or fails the test.
func wait(t *testing.T, r *Runner, id uint64) Snapshot {
	t.Helper()
	deadline := time.After(20 * time.Second)
	for {
		select {
		case ev, ok := <-r.Events():
			if !ok {
				t.Fatal("event channel closed before the job finished")
			}
			if ev.Job.ID == id && ev.Job.Status.Done() {
				return ev.Job
			}
		case <-deadline:
			t.Fatal("timed out waiting for the job")
		}
	}
}

func TestRunsAndCapturesOutput(t *testing.T) {
	fakeGraphify(t, `echo "hello from $1"; exit 0`)
	r := New(Options{})
	defer r.Close()

	repo := repoDir(t)
	job, err := r.SubmitCmd("update", repo, "Update · repo", gfy.Params{Repo: repo}, nil)
	if err != nil {
		t.Fatal(err)
	}
	s := wait(t, r, job.ID)
	if s.Status != Succeeded {
		t.Fatalf("Status = %v, want ok", s.Status)
	}
	if s.Exit != 0 {
		t.Fatalf("Exit = %d", s.Exit)
	}
	if !strings.Contains(job.Log.String(), "hello from update") {
		t.Fatalf("output not captured:\n%s", job.Log.String())
	}
	// The argv is in the log above the output, which is what makes a job log
	// reproducible in a terminal.
	if !strings.HasPrefix(job.Log.String(), "$ ") {
		t.Error("the log must open with the command line")
	}
}

func TestFailureCarriesTheExitCode(t *testing.T) {
	fakeGraphify(t, `echo "RuntimeError: no API key" >&2; exit 3`)
	r := New(Options{})
	defer r.Close()

	repo := repoDir(t)
	job, _ := r.SubmitCmd("update", repo, "Update", gfy.Params{Repo: repo}, nil)
	s := wait(t, r, job.ID)
	if s.Status != Failed {
		t.Fatalf("Status = %v, want failed", s.Status)
	}
	if s.Exit != 3 {
		t.Fatalf("Exit = %d, want 3", s.Exit)
	}
	if got := gfy.FirstErrorLine(job.Log.String()); got != "RuntimeError: no API key" {
		t.Errorf("FirstErrorLine = %q", got)
	}
}

// Cancellation must reap the whole process group. graphify is a Python parent
// that spawns AST workers; killing only the parent leaves them holding CPU and
// the output directory. The fake here spawns a child that ignores SIGTERM,
// which is the case a naive Process.Kill gets wrong.
func TestCancelReapsTheProcessGroup(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "child-alive")
	fakeGraphify(t, `
trap '' TERM
( trap '' TERM; while true; do echo alive > `+marker+`; sleep 0.1; done ) &
child=$!
echo "started $child"
wait $child
`)
	r := New(Options{KillGrace: 500 * time.Millisecond})
	defer r.Close()

	repo := repoDir(t)
	job, _ := r.SubmitCmd("update", repo, "Update", gfy.Params{Repo: repo}, nil)

	// Wait until it is genuinely running before cancelling.
	deadline := time.After(10 * time.Second)
	for !strings.Contains(job.Log.String(), "started") {
		select {
		case <-deadline:
			t.Fatal("the fake never started")
		default:
			time.Sleep(20 * time.Millisecond)
		}
	}

	if !r.Cancel(job.ID) {
		t.Fatal("Cancel returned false for a running job")
	}
	s := wait(t, r, job.ID)
	if s.Status != Canceled {
		t.Fatalf("Status = %v, want cancelled", s.Status)
	}

	// The grandchild ignored SIGTERM, so only the SIGKILL to the group can
	// have stopped it. Give it a moment and then assert the marker stops
	// moving — a surviving orphan would keep rewriting it.
	time.Sleep(700 * time.Millisecond)
	before, err := os.Stat(marker)
	if err != nil {
		return // never got far enough to write; nothing survived either way
	}
	time.Sleep(600 * time.Millisecond)
	after, err := os.Stat(marker)
	if err != nil {
		return
	}
	if !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("an orphaned grandchild survived cancellation")
	}
}

func TestCancelDropsAQueuedJob(t *testing.T) {
	fakeGraphify(t, `sleep 5`)
	// One free lane, so the second job is definitely still queued.
	r := New(Options{FreeLanes: 1})
	defer r.Close()

	ra, rb := repoDir(t), repoDir(t)
	first, _ := r.SubmitCmd("update", ra, "a", gfy.Params{Repo: ra}, nil)
	second, _ := r.SubmitCmd("update", rb, "b", gfy.Params{Repo: rb}, nil)
	time.Sleep(200 * time.Millisecond)

	if !r.Cancel(second.ID) {
		t.Fatal("Cancel returned false for a queued job")
	}
	r.Cancel(first.ID)
}

// The metered lane is the one that spends money. It must default to exactly
// one and must not be raised because a caller passed a zero.
func TestMeteredLaneDefaultsToOne(t *testing.T) {
	r := New(Options{MeteredLanes: 0})
	defer r.Close()
	if _, m, _ := r.Lanes(); m != 1 {
		t.Fatalf("metered lanes = %d, want 1", m)
	}
}

// Two metered jobs must not run at once with the default lane count. This is
// the property that keeps a fan-out extraction from becoming a parallel bill.
func TestMeteredJobsAreSerialized(t *testing.T) {
	var concurrent, peak int32
	dir := t.TempDir()
	fakeGraphify(t, `
n=$(cat `+dir+`/n 2>/dev/null || echo 0)
echo $((n+1)) > `+dir+`/n
sleep 0.4
n=$(cat `+dir+`/n)
echo $((n-1)) > `+dir+`/n
`)
	_ = concurrent
	_ = peak

	r := New(Options{FreeLanes: 8})
	defer r.Close()

	var ids []uint64
	for _, repo := range []string{repoDir(t), repoDir(t), repoDir(t)} {
		j, err := r.SubmitCmd("extract", repo, "Extract "+repo, gfy.Params{Repo: repo}, nil)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, j.ID)
	}

	// Sample the counter while they run: it must never exceed 1.
	var maxSeen int32
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-done:
				return
			default:
			}
			// The counter file is briefly EMPTY while the shell truncates it
			// for the next write; sampling it there is not a reading of zero
			// in flight, it is no reading at all.
			if b, err := os.ReadFile(filepath.Join(dir, "n")); err == nil && len(b) > 0 {
				v := int32(b[0] - '0')
				for {
					old := atomic.LoadInt32(&maxSeen)
					if v <= old || atomic.CompareAndSwapInt32(&maxSeen, old, v) {
						break
					}
				}
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
	for _, id := range ids {
		wait(t, r, id)
	}
	close(done)

	if got := atomic.LoadInt32(&maxSeen); got > 1 {
		t.Fatalf("%d metered jobs ran at once; the metered lane must serialize them", got)
	}
}

// Two mutating jobs against one repository would race each other's graph.json.
// Read-only jobs against the same repository are unaffected.
func TestMutatingJobsForOneRepoAreSerialized(t *testing.T) {
	fakeGraphify(t, `sleep 0.3`)
	r := New(Options{FreeLanes: 8})
	defer r.Close()

	repo := repoDir(t)
	a, _ := r.SubmitCmd("update", repo, "a", gfy.Params{Repo: repo}, nil)
	b, _ := r.SubmitCmd("update", repo, "b", gfy.Params{Repo: repo}, nil)
	time.Sleep(150 * time.Millisecond)

	if _, held := r.Busy(repo); !held {
		t.Fatal("the repository should be held by the running job")
	}
	_, running := r.Active()
	if running != 1 {
		t.Fatalf("%d jobs running against one repository, want 1", running)
	}
	wait(t, r, a.ID)
	wait(t, r, b.ID)
}

// An unknown kind is a programming error and must not produce a half-built
// command that runs and does something unintended.
func TestSubmitCmdRejectsAnUnknownKind(t *testing.T) {
	r := New(Options{})
	defer r.Close()
	if _, err := r.SubmitCmd("no-such-kind", repoDir(t), "x", gfy.Params{Repo: repoDir(t)}, nil); err == nil {
		t.Fatal("want an error for an unknown kind")
	}
}

// A chatty subprocess must not be able to grow the heap.
func TestLogIsBounded(t *testing.T) {
	fakeGraphify(t, `i=0; while [ $i -lt 4000 ]; do echo "line $i padding padding padding padding"; i=$((i+1)); done`)
	r := New(Options{LogBytes: 8 << 10})
	defer r.Close()

	repo := repoDir(t)
	job, _ := r.SubmitCmd("update", repo, "Update", gfy.Params{Repo: repo}, nil)
	wait(t, r, job.ID)

	if n := job.Log.Len(); n > 8<<10 {
		t.Fatalf("log grew to %d bytes past its %d cap", n, 8<<10)
	}
	// The tail is what survives, because that is where a failure's message is.
	if !strings.Contains(job.Log.String(), "line 3999") {
		t.Error("the end of the output was dropped rather than the start")
	}
	if !strings.Contains(job.Log.String(), "elided") {
		t.Error("a truncated log must say it was truncated")
	}
}

// The composed environment reaches the subprocess, and the overlay wins over
// the inherited value.
func TestEnvOverlayReachesTheSubprocess(t *testing.T) {
	fakeGraphify(t, `echo "WORKERS=$GRAPHIFY_MAX_WORKERS TIPS=$GRAPHIFY_NO_TIPS"`)
	t.Setenv("GRAPHIFY_MAX_WORKERS", "1")

	r := New(Options{})
	defer r.Close()
	repo := repoDir(t)
	job, _ := r.SubmitCmd("update", repo, "Update", gfy.Params{Repo: repo},
		gfy.Env{"GRAPHIFY_MAX_WORKERS": "8"})
	wait(t, r, job.ID)

	out := job.Log.String()
	if !strings.Contains(out, "WORKERS=8") {
		t.Errorf("the overlay did not reach the subprocess:\n%s", out)
	}
	if !strings.Contains(out, "TIPS=1") {
		t.Errorf("JobEnv did not silence tips:\n%s", out)
	}
}

func TestCloseCancelsEverything(t *testing.T) {
	fakeGraphify(t, `sleep 30`)
	r := New(Options{})
	repo := repoDir(t)
	job, _ := r.SubmitCmd("watch", repo, "Watch", gfy.Params{Repo: repo}, nil)
	time.Sleep(200 * time.Millisecond)

	done := make(chan struct{})
	go func() { r.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("Close did not return — a watcher must not outlive the board")
	}
	if job.Status != Canceled && job.Status != Failed {
		t.Fatalf("Status = %v after Close", job.Status)
	}
}

// The precondition hook is the reason preconditions stay enforced: it sits on
// the one path every job takes, so a new button, a new keyboard shortcut or a
// new pane cannot submit around it. A refused job never reaches the queue and
// never reaches the events channel.
func TestSubmitCmdHonoursPrecheck(t *testing.T) {
	var seen []*Job
	r := New(Options{Precheck: func(j *Job) error {
		seen = append(seen, j)
		if j.Kind == "label" {
			return errors.New("no graph")
		}
		return nil
	}})
	defer r.Close()

	repo := repoDir(t)
	if _, err := r.SubmitCmd("label", repo, "Label", gfy.Params{Repo: repo, Out: "/nowhere"}, nil); err == nil {
		t.Fatal("want the precheck's error back from SubmitCmd")
	}
	if got := len(r.Snapshot()); got != 0 {
		t.Fatalf("a refused job was queued anyway: %d jobs", got)
	}
	if len(seen) != 1 || seen[0].Out != "/nowhere" {
		t.Fatalf("the precheck was not given the job's output directory: %+v", seen)
	}

	// A kind the hook allows still runs, and the hook is not consulted for
	// Submit — the caller there has already built the job deliberately.
	if _, err := r.SubmitCmd("check-update", repo, "Check", gfy.Params{Repo: repo, Out: "/nowhere"}, nil); err != nil {
		t.Fatalf("an allowed job was refused: %v", err)
	}
}

// A runner with no hook submits exactly as before.
func TestSubmitCmdWithoutPrecheck(t *testing.T) {
	r := New(Options{})
	defer r.Close()
	repo := repoDir(t)
	if _, err := r.SubmitCmd("check-update", repo, "Check", gfy.Params{Repo: repo}, nil); err != nil {
		t.Fatalf("SubmitCmd: %v", err)
	}
}

// TestPauseResumeStopsTheProcessGroup is the real test of the feature: not that
// a flag flips, but that the subprocess stops producing output while paused and
// picks up again when resumed. A script that appends a line every 100ms makes
// that observable without depending on timing being exact.
func TestPauseResumeStopsTheProcessGroup(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "ticks")
	fakeGraphify(t, `i=0
while [ $i -lt 200 ]; do echo tick >> `+out+`; i=$((i+1)); sleep 0.05; done`)

	r := New(Options{})
	defer r.Close()

	repo := repoDir(t)
	job, err := r.SubmitCmd("update", repo, "Update · repo", gfy.Params{Repo: repo}, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Wait for it to be genuinely running with a process group to signal;
	// Pause before exec.Start is a no-op by design.
	if !waitFor(2*time.Second, func() bool { return r.pausable(job.ID) }) {
		t.Fatal("job never reached a pausable state")
	}
	if !r.Pause(job.ID) {
		t.Fatal("Pause reported no change on a running job")
	}
	if !r.Paused(job.ID) {
		t.Fatal("Paused = false right after a successful Pause")
	}
	if r.Pause(job.ID) {
		t.Fatal("a second Pause reported a change")
	}

	// SIGSTOP is asynchronous: let the group settle, then measure.
	time.Sleep(250 * time.Millisecond)
	before := countLines(t, out)
	time.Sleep(400 * time.Millisecond)
	if after := countLines(t, out); after != before {
		t.Fatalf("a paused job kept writing: %d lines -> %d", before, after)
	}

	if !r.Resume(job.ID) {
		t.Fatal("Resume reported no change on a paused job")
	}
	if r.Paused(job.ID) {
		t.Fatal("still paused after Resume")
	}
	if !waitFor(2*time.Second, func() bool { return countLines(t, out) > before }) {
		t.Fatal("a resumed job never wrote again")
	}

	r.Cancel(job.ID)
	if s := wait(t, r, job.ID); s.Status != Canceled {
		t.Fatalf("Status = %v, want canceled", s.Status)
	}
}

// TestCancelWhilePausedTerminatesPromptly guards the reason reap sends SIGCONT
// after SIGTERM: a stopped process cannot act on a pending signal, so without
// it cancelling a paused job would sit out the whole kill grace and then be
// SIGKILLed mid-write — the exact outcome the grace period exists to avoid.
func TestCancelWhilePausedTerminatesPromptly(t *testing.T) {
	fakeGraphify(t, `i=0
while [ $i -lt 400 ]; do echo tick; i=$((i+1)); sleep 0.05; done`)

	r := New(Options{KillGrace: 30 * time.Second})
	defer r.Close()

	repo := repoDir(t)
	job, err := r.SubmitCmd("update", repo, "Update · repo", gfy.Params{Repo: repo}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !waitFor(2*time.Second, func() bool { return r.pausable(job.ID) }) {
		t.Fatal("job never reached a pausable state")
	}
	if !r.Pause(job.ID) {
		t.Fatal("Pause reported no change")
	}

	start := time.Now()
	r.Cancel(job.ID)
	s := wait(t, r, job.ID)
	if s.Status != Canceled {
		t.Fatalf("Status = %v, want canceled", s.Status)
	}
	// Far under the 30s grace: it died on the SIGTERM, not on the SIGKILL.
	if d := time.Since(start); d > 10*time.Second {
		t.Fatalf("cancelling a paused job took %s — it waited out the kill grace", d)
	}
}

// TestElapsedExcludesPausedTime pins the accounting: the run history records
// Elapsed as the job's duration, and time spent stopped is not time spent
// working.
func TestElapsedExcludesPausedTime(t *testing.T) {
	fakeGraphify(t, `sleep 30`)

	r := New(Options{})
	defer r.Close()

	repo := repoDir(t)
	job, err := r.SubmitCmd("update", repo, "Update · repo", gfy.Params{Repo: repo}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !waitFor(2*time.Second, func() bool { return r.pausable(job.ID) }) {
		t.Fatal("job never reached a pausable state")
	}
	if !r.Pause(job.ID) {
		t.Fatal("Pause reported no change")
	}
	s := snapOf(t, r, job.ID)
	at := s.Elapsed()
	time.Sleep(600 * time.Millisecond)
	// Same snapshot value, recomputed later: the UI holds one across ticks,
	// so the clock has to stand still in the value itself.
	if grew := s.Elapsed() - at; grew > 50*time.Millisecond {
		t.Fatalf("elapsed kept running while paused: grew by %s", grew)
	}
	if !r.Resume(job.ID) {
		t.Fatal("Resume reported no change")
	}
	if held := snapOf(t, r, job.ID).PausedFor; held < 500*time.Millisecond {
		t.Fatalf("Held = %s, want at least the time it was paused", held)
	}

	r.Cancel(job.ID)
	wait(t, r, job.ID)
}

// TestPauseRefusesWhatItCannotStop: a queued job has no process group, and a
// finished one has nothing left to signal. Both report no change rather than
// pretending.
func TestPauseRefusesWhatItCannotStop(t *testing.T) {
	fakeGraphify(t, `sleep 30`)

	// One free lane, two jobs: the second is queued behind the first.
	r := New(Options{FreeLanes: 1})
	defer r.Close()

	a, err := r.SubmitCmd("update", repoDir(t), "a", gfy.Params{Repo: repoDir(t)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	b, err := r.SubmitCmd("update", repoDir(t), "b", gfy.Params{Repo: repoDir(t)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !waitFor(2*time.Second, func() bool { return r.pausable(a.ID) }) {
		t.Fatal("the first job never started")
	}
	if r.Pause(b.ID) {
		t.Fatal("Pause reported a change on a queued job")
	}
	if r.Paused(b.ID) {
		t.Fatal("a queued job reports as paused")
	}
	if r.Pause(99999) {
		t.Fatal("Pause reported a change on an unknown id")
	}

	r.CancelAll()
	wait(t, r, a.ID)
}

// pausable reports whether the job is running with a process group to signal —
// the precondition Pause enforces, exposed for the tests that must wait for it.
func (r *Runner) pausable(id uint64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	j := r.running[id]
	return j != nil && j.Status == Running && j.pgid != 0
}

func snapOf(t *testing.T, r *Runner, id uint64) Snapshot {
	t.Helper()
	for _, s := range r.Snapshot() {
		if s.ID == id {
			return s
		}
	}
	t.Fatalf("no snapshot for job %d", id)
	return Snapshot{}
}

func waitFor(d time.Duration, ok func() bool) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if ok() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return ok()
}

func countLines(t *testing.T, path string) int {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0
		}
		t.Fatal(err)
	}
	return strings.Count(string(b), "\n")
}

// The ollama concurrency unlock has to be composed by SubmitCmd, not by the
// GUI, because ggraphify-job submits through here too. Without it graphify
// clamps itself back to one chunk and the --max-concurrency already in the
// argv is silently discarded — a run that looks parallel everywhere except in
// how long it takes.
func TestOllamaParallelUnlockReachesTheSubprocess(t *testing.T) {
	fakeGraphify(t, `echo "PARALLEL=$GRAPHIFY_OLLAMA_PARALLEL"`)
	t.Setenv(gfy.OllamaSlotsVar, "2")
	t.Setenv(gfy.OllamaHostVar, "")
	gfy.InvalidateLocalProbe()
	t.Cleanup(gfy.InvalidateLocalProbe)

	r := New(Options{})
	defer r.Close()
	repo := repoDir(t)
	job, _ := r.SubmitCmd("update", repo, "Update",
		gfy.Params{Repo: repo, Backend: gfy.OllamaBackend}, nil)
	wait(t, r, job.ID)

	if out := job.Log.String(); !strings.Contains(out, "PARALLEL=1") {
		t.Errorf("graphify would clamp itself back to one chunk:\n%s", out)
	}
}

// And a metered backend never sees it: the variable is meaningless there, and
// an environment that carries settings for a backend it is not using is how a
// confirm dialog comes to show something that will not happen.
func TestOllamaParallelUnlockIsNotAppliedToAMeteredBackend(t *testing.T) {
	fakeGraphify(t, `echo "PARALLEL=$GRAPHIFY_OLLAMA_PARALLEL"`)
	t.Setenv(gfy.OllamaSlotsVar, "2")
	gfy.InvalidateLocalProbe()
	t.Cleanup(gfy.InvalidateLocalProbe)

	r := New(Options{})
	defer r.Close()
	repo := repoDir(t)
	job, _ := r.SubmitCmd("update", repo, "Update",
		gfy.Params{Repo: repo, Backend: gfy.ClaudeCLIBackend}, nil)
	wait(t, r, job.ID)

	if out := job.Log.String(); !strings.Contains(out, "PARALLEL=\n") {
		t.Errorf("a claude-cli job carried an ollama variable:\n%s", out)
	}
}

// peakConcurrency runs three metered jobs of the given backend and reports how
// many were ever in flight at once, by the same shell counter
// TestMeteredJobsAreSerialized uses.
func peakConcurrency(t *testing.T, opts Options, backend string) int32 {
	t.Helper()
	dir := t.TempDir()
	// One file per running job, created on entry and removed on exit, so the
	// count is the number of entries in the directory. The obvious
	// alternative — a single counter file incremented and decremented by the
	// shell — is a read-modify-write with no locking, and the moment jobs
	// genuinely overlap it loses updates and goes negative. Which is the one
	// condition this helper exists to measure, so it cannot use that.
	fakeGraphify(t, `
: > `+dir+`/$$
sleep 0.4
rm -f `+dir+`/$$
`)
	r := New(opts)
	defer r.Close()

	for _, repo := range []string{repoDir(t), repoDir(t), repoDir(t)} {
		if _, err := r.SubmitCmd("extract", repo, "Extract "+repo,
			gfy.Params{Repo: repo, Backend: backend}, nil); err != nil {
			t.Fatal(err)
		}
	}

	// Drained by polling Active rather than by wait(), which is only correct
	// for jobs that finish in the order they were submitted: it discards every
	// event that is not the one id it was asked for, so the moment two jobs
	// really do run at once it throws away the completion of one of them and
	// then blocks forever waiting for it. Which is the whole subject of this
	// helper, so it cannot use that one.
	var maxSeen int32
	deadline := time.Now().Add(20 * time.Second)
	for {
		if ents, err := os.ReadDir(dir); err == nil {
			if v := int32(len(ents)); v > maxSeen {
				maxSeen = v
			}
		}
		if q, running := r.Active(); q == 0 && running == 0 {
			return maxSeen
		}
		if time.Now().After(deadline) {
			t.Fatal("jobs never drained")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// The whole point of the local lane: extraction against a model on this machine
// runs several jobs at once, because there is no bill to parallelize — while
// the identical command against a billed backend stays serialized on the very
// same runner. Asserting only the first half would pass just as well if the
// local lane had simply raised the metered one, which is the mistake this
// separation exists to make impossible.
func TestLocalJobsRunInParallelWhileMeteredStaySerialized(t *testing.T) {
	t.Setenv(gfy.OllamaHostVar, "")
	t.Setenv(gfy.OllamaBaseURLVar, "")

	opts := Options{FreeLanes: 8, MeteredLanes: 1, LocalLanes: 3}
	if got := peakConcurrency(t, opts, gfy.OllamaBackend); got < 2 {
		t.Errorf("local jobs peaked at %d in flight; the local lane is not being used", got)
	}
	if got := peakConcurrency(t, opts, gfy.ClaudeCLIBackend); got > 1 {
		t.Errorf("%d billed jobs ran at once — the local lane must not lift the metered one", got)
	}
}

// And the local lane is bounded by its own setting, not unbounded: a board set
// to two must not run three, however many local jobs are queued.
func TestLocalLaneIsBounded(t *testing.T) {
	t.Setenv(gfy.OllamaHostVar, "")
	t.Setenv(gfy.OllamaBaseURLVar, "")

	opts := Options{FreeLanes: 8, MeteredLanes: 1, LocalLanes: 2}
	if got := peakConcurrency(t, opts, gfy.OllamaBackend); got > 2 {
		t.Errorf("%d local jobs ran at once with LocalLanes=2", got)
	}
}

// A caller that passed no local lane count gets the default, never zero —
// which would be a lane nothing can ever enter.
func TestLocalLaneDefaults(t *testing.T) {
	r := New(Options{})
	defer r.Close()
	if _, _, l := r.Lanes(); l != DefaultLocalLanes {
		t.Fatalf("local lanes = %d, want %d", l, DefaultLocalLanes)
	}
}

// Close used to close the events channel while a finishing job was still
// inside emit: emit checked r.closed and then sent, and Close could land
// between the two. The wg covers loop() alone, so the per-job goroutines
// outlive it. In production that is a "send on closed channel" panic on quit
// — the whole GUI, taken down by closing the window at the instant a job
// finished.
func TestCloseDoesNotRaceAFinishingJobsEvent(t *testing.T) {
	fakeGraphify(t, "")
	for i := 0; i < 60; i++ {
		r := New(Options{FreeLanes: 8, MeteredLanes: 1, LocalLanes: 3, History: 16})
		for j := 0; j < 6; j++ {
			repo := repoDir(t)
			if _, err := r.SubmitCmd("extract", repo, "Extract "+repo,
				gfy.Params{Repo: repo}, nil); err != nil {
				t.Fatal(err)
			}
		}
		// Drain, so emit takes the send path rather than the full-buffer one.
		done := make(chan struct{})
		go func() {
			defer close(done)
			for range r.Events() {
			}
		}()
		// Close while the jobs are mid-flight: the window is exactly here.
		time.Sleep(time.Duration(i%4) * time.Millisecond)
		r.Close()
		<-done
	}
}

// The lease must bracket the job's process: taken before it starts, given
// back after it ends. Both halves matter — a lease taken late would let the
// job send to a server that is not up yet, and one released early would let
// the idle timer stop a server mid-run.
func TestLocalLeaseBracketsTheProcess(t *testing.T) {
	fakeGraphify(t, `sleep 0.2`)
	var (
		held     atomic.Int32
		peak     atomic.Int32
		released atomic.Int32
		duringMu sync.Mutex
		during   []int32
	)
	r := New(Options{
		FreeLanes: 4,
		LocalLease: func(j *Job) Lease {
			n := held.Add(1)
			for {
				p := peak.Load()
				if n <= p || peak.CompareAndSwap(p, n) {
					break
				}
			}
			return &countingLease{onRelease: func() {
				held.Add(-1)
				released.Add(1)
			}}
		},
	})
	defer r.Close()

	repo := repoDir(t)
	j, err := r.SubmitCmd("update", repo, "local", gfy.Params{Repo: repo, Backend: gfy.OllamaBackend}, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Sampled while the subprocess is still sleeping: the lease has to be out
	// for the whole of it, not merely acquired and dropped around the exec.
	time.Sleep(100 * time.Millisecond)
	duringMu.Lock()
	during = append(during, held.Load())
	duringMu.Unlock()

	snap := wait(t, r, j.ID)
	if snap.Status != Succeeded {
		t.Fatalf("job status = %v, want Succeeded", snap.Status)
	}
	if during[0] != 1 {
		t.Fatalf("%d leases held while the process ran, want 1", during[0])
	}
	if got := held.Load(); got != 0 {
		t.Fatalf("%d leases still out after the job ended, want 0", got)
	}
	if got := released.Load(); got != 1 {
		t.Fatalf("release called %d times, want 1", got)
	}
	if got := peak.Load(); got != 1 {
		t.Fatalf("peak leases = %d, want 1", got)
	}
}

// A job that dies is still a job whose lease has to come back, or the server
// stays up forever after one failure.
func TestLocalLeaseReleasedOnFailureAndCancellation(t *testing.T) {
	fakeGraphify(t, `sleep 0.3; exit 7`)
	var held atomic.Int32
	r := New(Options{
		FreeLanes: 4,
		LocalLease: func(*Job) Lease {
			held.Add(1)
			return &countingLease{onRelease: func() { held.Add(-1) }}
		},
	})
	defer r.Close()

	repo := repoDir(t)
	fail, _ := r.SubmitCmd("update", repo, "fails", gfy.Params{Repo: repo, Backend: gfy.OllamaBackend}, nil)
	if snap := wait(t, r, fail.ID); snap.Status != Failed {
		t.Fatalf("status = %v, want Failed", snap.Status)
	}
	if got := held.Load(); got != 0 {
		t.Fatalf("%d leases out after a failed job, want 0", got)
	}

	other := repoDir(t)
	cancelled, _ := r.SubmitCmd("update", other, "cancelled", gfy.Params{Repo: other, Backend: gfy.OllamaBackend}, nil)
	time.Sleep(100 * time.Millisecond)
	r.Cancel(cancelled.ID)
	if snap := wait(t, r, cancelled.ID); snap.Status != Canceled {
		t.Fatalf("status = %v, want Canceled", snap.Status)
	}
	if got := held.Load(); got != 0 {
		t.Fatalf("%d leases out after a cancelled job, want 0", got)
	}
}

// A nil hook, and a hook that declines a particular job by returning nil, are
// both ordinary: neither may stop the job from running.
func TestLocalLeaseOptional(t *testing.T) {
	fakeGraphify(t, `echo ok`)
	for _, tc := range []struct {
		name string
		hook func(*Job) Lease
	}{
		{"nil hook", nil},
		{"hook declines", func(*Job) Lease { return nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := New(Options{LocalLease: tc.hook})
			defer r.Close()
			repo := repoDir(t)
			j, err := r.SubmitCmd("update", repo, "x", gfy.Params{Repo: repo}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if snap := wait(t, r, j.ID); snap.Status != Succeeded {
				t.Fatalf("status = %v, want Succeeded", snap.Status)
			}
		})
	}
}

// countingLease records every transition the runner drives, in order, so a
// test can assert on the sequence rather than on a final count. onRelease is
// the hook the older lease tests use.
type countingLease struct {
	mu        sync.Mutex
	steps     []string
	resumedAt time.Time
	onRelease func()
}

func (l *countingLease) note(step string) {
	l.mu.Lock()
	l.steps = append(l.steps, step)
	l.mu.Unlock()
}

func (l *countingLease) Suspend() { l.note("suspend") }

// Resume deliberately takes a moment, the way a real cold start does. The
// point of the delay is that a runner which signalled first would have the
// process awake during it, and the test below can see that.
func (l *countingLease) Resume() {
	time.Sleep(150 * time.Millisecond)
	l.mu.Lock()
	l.resumedAt = time.Now()
	l.mu.Unlock()
	l.note("resume")
}

func (l *countingLease) Release() {
	l.note("release")
	if l.onRelease != nil {
		l.onRelease()
	}
}

func (l *countingLease) sequence() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.steps...)
}

func (l *countingLease) resumed() time.Time {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.resumedAt
}

// Pausing a job suspends its lease, and resuming takes it back — so a paused
// job that was using the local model server gives the server up, and gets it
// back before it runs again.
func TestPauseSuspendsTheLeaseAndResumeTakesItBack(t *testing.T) {
	fakeGraphify(t, `sleep 2`)
	lease := &countingLease{}
	r := New(Options{FreeLanes: 4, LocalLease: func(*Job) Lease { return lease }})
	defer r.Close()

	repo := repoDir(t)
	j, err := r.SubmitCmd("update", repo, "local", gfy.Params{Repo: repo, Backend: gfy.OllamaBackend}, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Wait for the process, because setPaused refuses a job whose pgid is
	// still zero and would otherwise report "nothing happened".
	waitRunning(t, r, j.ID)

	if !r.Pause(j.ID) {
		t.Fatal("Pause reported that it changed nothing")
	}
	if got := lease.sequence(); len(got) != 1 || got[0] != "suspend" {
		t.Fatalf("after Pause the lease saw %v, want [suspend]", got)
	}

	if !r.Resume(j.ID) {
		t.Fatal("Resume reported that it changed nothing")
	}
	if got := lease.sequence(); len(got) != 2 || got[1] != "resume" {
		t.Fatalf("after Resume the lease saw %v, want [suspend resume]", got)
	}

	r.Cancel(j.ID)
	wait(t, r, j.ID)
	if got := lease.sequence(); got[len(got)-1] != "release" {
		t.Fatalf("the lease was not released at the end: %v", got)
	}
}

// The ordering that makes resume correct: the lease is back BEFORE the process
// is woken. A SIGCONT sent first would wake graphify into a model server the
// pause had taken away, and it would find out as a connection error.
func TestResumeRestoresTheLeaseBeforeSignalling(t *testing.T) {
	// The script records when it wakes: it traps SIGCONT and stamps a file.
	dir := t.TempDir()
	stamp := filepath.Join(dir, "woke")
	fakeGraphify(t, `
trap 'date +%s.%N > `+stamp+`' CONT
i=0
while [ $i -lt 100 ]; do sleep 0.05; i=$((i+1)); done
`)
	lease := &countingLease{}
	r := New(Options{FreeLanes: 4, LocalLease: func(*Job) Lease { return lease }})
	defer r.Close()

	repo := repoDir(t)
	j, _ := r.SubmitCmd("update", repo, "local", gfy.Params{Repo: repo, Backend: gfy.OllamaBackend}, nil)
	waitRunning(t, r, j.ID)
	r.Pause(j.ID)
	r.Resume(j.ID)

	woke := readStamp(t, stamp)
	if woke.IsZero() {
		t.Skip("the shell did not report the SIGCONT; nothing to order against")
	}
	if resumed := lease.resumed(); !resumed.Before(woke) {
		t.Fatalf("the process woke at %v, before the lease was back at %v", woke, resumed)
	}
	r.Cancel(j.ID)
	wait(t, r, j.ID)
}

// A job cancelled while paused still gets exactly one Release, and the lease
// is told nothing else — no resume it did not ask for on the way out.
func TestCancelWhilePausedReleasesTheLeaseOnce(t *testing.T) {
	fakeGraphify(t, `sleep 5`)
	lease := &countingLease{}
	r := New(Options{FreeLanes: 4, LocalLease: func(*Job) Lease { return lease }})
	defer r.Close()

	repo := repoDir(t)
	j, _ := r.SubmitCmd("update", repo, "local", gfy.Params{Repo: repo, Backend: gfy.OllamaBackend}, nil)
	waitRunning(t, r, j.ID)
	r.Pause(j.ID)
	r.Cancel(j.ID)
	if snap := wait(t, r, j.ID); snap.Status != Canceled {
		t.Fatalf("status = %v, want Canceled", snap.Status)
	}

	got := lease.sequence()
	var releases int
	for _, s := range got {
		if s == "release" {
			releases++
		}
	}
	if releases != 1 {
		t.Fatalf("lease saw %d releases in %v, want exactly 1", releases, got)
	}
	if got[0] != "suspend" {
		t.Fatalf("lease sequence %v does not start with the pause", got)
	}
}

// A job with no lease — anything not against ollama — pauses and resumes
// exactly as it always did.
func TestPauseWithoutALeaseStillWorks(t *testing.T) {
	fakeGraphify(t, `sleep 1`)
	r := New(Options{FreeLanes: 4, LocalLease: func(*Job) Lease { return nil }})
	defer r.Close()

	repo := repoDir(t)
	j, _ := r.SubmitCmd("update", repo, "x", gfy.Params{Repo: repo}, nil)
	waitRunning(t, r, j.ID)
	if !r.Pause(j.ID) {
		t.Fatal("Pause on a job with no lease reported no change")
	}
	if !r.Paused(j.ID) {
		t.Fatal("the job is not paused")
	}
	if !r.Resume(j.ID) {
		t.Fatal("Resume on a job with no lease reported no change")
	}
	r.Cancel(j.ID)
	wait(t, r, j.ID)
}

// waitRunning blocks until the job's process actually exists, which is what
// Pause requires: a job dispatched but not yet exec'd has no process group to
// signal and is correctly refused.
func waitRunning(t *testing.T, r *Runner, id uint64) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		for _, s := range r.Snapshot() {
			if s.ID == id && s.Status == Running {
				// Status is set before exec.Start returns, so give the pgid a
				// moment to land rather than racing it.
				time.Sleep(50 * time.Millisecond)
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("the job never started running")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// readStamp reads the unix timestamp the fake graphify wrote when it woke.
func readStamp(t *testing.T, path string) time.Time {
	t.Helper()
	// The trap runs when the shell next gets control, which is after the
	// sleep it was in; poll rather than assume it is already there.
	deadline := time.Now().Add(3 * time.Second)
	for {
		b, err := os.ReadFile(path)
		if err == nil && len(strings.TrimSpace(string(b))) > 0 {
			var sec, nsec int64
			parts := strings.SplitN(strings.TrimSpace(string(b)), ".", 2)
			sec, _ = strconv.ParseInt(parts[0], 10, 64)
			if len(parts) == 2 {
				frac := (parts[1] + "000000000")[:9]
				nsec, _ = strconv.ParseInt(frac, 10, 64)
			}
			return time.Unix(sec, nsec)
		}
		if time.Now().After(deadline) {
			return time.Time{}
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A status change is the one event a consumer cannot reconstruct, so it must
// not be droppable. Output events fill the 256-slot buffer routinely — a
// chatty graphify under several parallel jobs does it in a second — and the
// completion event arriving behind them used to be thrown on the floor.
//
// For the GUI that is a row stuck at "running". For ggraphify-job, whose whole
// body is a range over this channel waiting for Status.Done(), it is a hang.
func TestTerminalEventSurvivesAFullEventChannel(t *testing.T) {
	// Comfortably more lines than the channel holds, with nothing draining it.
	fakeGraphify(t, `i=0; while [ $i -lt 600 ]; do echo "line $i"; i=$((i+1)); done`)

	r := New(Options{})
	defer r.Close()
	repo := repoDir(t)
	job, err := r.SubmitCmd("update", repo, "Update",
		gfy.Params{Repo: repo}, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Wait on the job itself rather than on the channel: done is closed before
	// the terminal event is emitted, so this arrives with the buffer still
	// full and the emit still to come.
	select {
	case <-job.Done():
	case <-time.After(20 * time.Second):
		t.Fatal("the job never finished")
	}
	// Long enough for the terminal emit to have been attempted against the
	// full buffer, which is the moment under test.
	time.Sleep(200 * time.Millisecond)

	deadline := time.After(20 * time.Second)
	for {
		select {
		case ev, ok := <-r.Events():
			if !ok {
				t.Fatal("event channel closed before the terminal event arrived")
			}
			if ev.Job.ID == job.ID && ev.Job.Status.Done() {
				return
			}
		case <-deadline:
			t.Fatal("the terminal event was dropped: a consumer waiting for " +
				"Status.Done() would wait forever")
		}
	}
}

// An output event is an ID and a generation, and nothing else. It used to be a
// full Snapshot built under the runner's global lock — the same lock that
// serializes dispatch, Cancel and completion — once per pipe read from every
// parallel job, to produce a struct copy the UI read two fields of.
//
// The ID lives on the event itself rather than on the snapshot precisely
// because there is no snapshot: a consumer filtering on ev.Job.ID would
// silently discard the entire log stream.
func TestOutputEventsCarryOnlyTheIDAndGeneration(t *testing.T) {
	fakeGraphify(t, `echo one; echo two; echo three`)

	r := New(Options{})
	defer r.Close()
	repo := repoDir(t)
	job, err := r.SubmitCmd("update", repo, "Update", gfy.Params{Repo: repo}, nil)
	if err != nil {
		t.Fatal(err)
	}

	seen := 0
	deadline := time.After(20 * time.Second)
	for done := false; !done; {
		select {
		case ev, ok := <-r.Events():
			if !ok {
				t.Fatal("event channel closed early")
			}
			if ev.ID != job.ID {
				continue
			}
			if ev.Output {
				seen++
				if ev.Gen == 0 {
					t.Error("output event carries no ring-buffer generation")
				}
				if ev.Job.Kind != "" || ev.Job.Repo != "" || ev.Job.Log != nil {
					t.Errorf("output event carries a snapshot: %+v", ev.Job)
				}
				continue
			}
			// Transitions still carry the whole thing, and their ID agrees.
			if ev.Job.ID != ev.ID {
				t.Errorf("transition ID mismatch: ev.ID=%d snapshot=%d", ev.ID, ev.Job.ID)
			}
			done = ev.Job.Status.Done()
		case <-deadline:
			t.Fatal("the job never finished")
		}
	}
	if seen == 0 {
		t.Fatal("no output events arrived at all")
	}
}

// Get answers about one job without building, copying and sorting every job
// the runner knows about — and it has to agree with Snapshot in every state a
// job can be in, since the callers it replaced were reading Snapshot's output.
func TestGetAgreesWithSnapshotAndMissesNothing(t *testing.T) {
	fakeGraphify(t, `exit 0`)

	r := New(Options{})
	defer r.Close()
	repo := repoDir(t)
	job, err := r.SubmitCmd("update", repo, "Update", gfy.Params{Repo: repo}, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Queued or running, depending on how fast dispatch was — either way Get
	// must find it, and agree with the whole-list answer.
	assertAgrees := func(when string) {
		t.Helper()
		got, ok := r.Get(job.ID)
		if !ok {
			t.Fatalf("%s: Get did not find job %d", when, job.ID)
		}
		var want Snapshot
		for _, s := range r.Snapshot() {
			if s.ID == job.ID {
				want = s
			}
		}
		if got.ID != want.ID || got.Kind != want.Kind || got.Repo != want.Repo {
			t.Errorf("%s: Get = %+v, Snapshot said %+v", when, got, want)
		}
	}
	assertAgrees("before it finished")

	wait(t, r, job.ID)

	// And in history, which is a different slice again.
	assertAgrees("after it finished")
	if s, _ := r.Get(job.ID); !s.Status.Done() {
		t.Errorf("Get returned a stale status %v for a finished job", s.Status)
	}
	if _, ok := r.Get(job.ID + 9999); ok {
		t.Error("Get invented a job that was never submitted")
	}
}
