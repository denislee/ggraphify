package jobs

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
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
	for {
		if strings.Contains(job.Log.String(), "started") {
			break
		}
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
	if _, m := r.Lanes(); m != 1 {
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
