package jobs

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/dns/ggraphify/internal/gfy"
)

// lockedBuf is a tee target that is safe to read while a job writes to it.
type lockedBuf struct {
	mu sync.Mutex
	sb strings.Builder
}

func (b *lockedBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.sb.Write(p)
}

func (b *lockedBuf) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.sb.String()
}

// readPid reads a pid a fake graphify wrote, waiting for it to appear.
func readPid(t *testing.T, path string) int {
	t.Helper()
	var pid int
	if !waitFor(3*time.Second, func() bool {
		b, err := os.ReadFile(path)
		if err != nil {
			return false
		}
		pid, err = strconv.Atoi(strings.TrimSpace(string(b)))
		return err == nil && pid > 0
	}) {
		t.Fatalf("no pid in %s", path)
	}
	return pid
}

// W5: a log far past its buffer's cap still reaches the tee whole, header to
// last line — the buffer evicts and is compacted at retirement, the tee is not.
func TestTeeCarriesTheWholeLogPastTheCap(t *testing.T) {
	fakeGraphify(t, `i=1; while [ $i -le 3000 ]; do echo "line $i"; i=$((i+1)); done; printf 'progress 1\rprogress 2\r\n'`)
	var tee lockedBuf
	r := New(Options{LogBytes: 4096, Tee: &tee})
	defer r.Close()

	repo := repoDir(t)
	job, err := r.SubmitCmd("update", repo, "Update · repo", gfy.Params{Repo: repo}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if s := wait(t, r, job.ID); s.Status != Succeeded {
		t.Fatalf("Status = %v, want ok", s.Status)
	}
	got := tee.String()
	if !strings.HasPrefix(got, "$ ") {
		t.Errorf("tee does not open with the command header: %.60q", got)
	}
	for _, want := range []string{"\nline 1\n", "\nline 1500\n", "\nline 3000\n", "progress 1\rprogress 2\r\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("tee is missing %q", want)
		}
	}
	if !strings.Contains(job.Log.String(), "elided") {
		t.Error("the test did not push the buffer past its cap")
	}
}

// W7: a grandchild in its own session holding the stdout pipe must not keep
// the job Running after the job's own process has exited.
func TestALingeringGrandchildDoesNotHangTheJob(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pid")
	fakeGraphify(t, `setsid sh -c 'echo $$ > `+pidFile+`; exec sleep 15' &
echo hi; exit 0`)
	r := New(Options{WaitDelay: 200 * time.Millisecond})
	defer r.Close()

	repo := repoDir(t)
	start := time.Now()
	job, err := r.SubmitCmd("update", repo, "Update · repo", gfy.Params{Repo: repo}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(readPid(t, pidFile), syscall.SIGKILL) })

	done := make(chan Snapshot, 1)
	go func() {
		for ev := range r.Events() {
			if ev.Job.ID == job.ID && ev.Job.Status.Done() {
				done <- ev.Job
				return
			}
		}
	}()
	select {
	case s := <-done:
		if s.Status != Succeeded {
			t.Errorf("Status = %v, want ok — the job's own process exited 0", s.Status)
		}
		if el := time.Since(start); el > 5*time.Second {
			t.Errorf("job took %v to finish", el)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("the job is still Running while a setsid grandchild holds its pipe")
	}
	if !strings.Contains(job.Log.String(), "hi") {
		t.Errorf("output lost:\n%s", job.Log.String())
	}
}

// W7: a straggler left in the job's process group is terminated once the
// job's own process exits, rather than outliving the job it belongs to.
func TestStragglersInTheGroupAreReapedAtExit(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pid")
	fakeGraphify(t, `sleep 30 >/dev/null 2>&1 &
echo $! > `+pidFile+`
exit 0`)
	r := New(Options{KillGrace: time.Second, WaitDelay: 200 * time.Millisecond})
	defer r.Close()

	repo := repoDir(t)
	job, err := r.SubmitCmd("update", repo, "Update · repo", gfy.Params{Repo: repo}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if s := wait(t, r, job.ID); s.Status != Succeeded {
		t.Fatalf("Status = %v, want ok", s.Status)
	}
	pid := readPid(t, pidFile)
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	if !waitFor(3*time.Second, func() bool { return syscall.Kill(pid, 0) != nil }) {
		t.Fatalf("straggler %d is still alive after the job finished", pid)
	}
}

// W27: a pause whose signal lands just as the job exits must not be recorded:
// the job is finished, and what the SIGSTOP reached is a straggler that has to
// be continued again rather than left stopped.
func TestPauseRacingANaturalExitIsNotRecorded(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pid")
	fakeGraphify(t, `sh -c "trap '' TERM HUP; exec sleep 30" >/dev/null 2>&1 &
echo $! > `+pidFile+`
sleep 0.3
exit 0`)
	r := New(Options{KillGrace: 1500 * time.Millisecond, WaitDelay: 200 * time.Millisecond})
	defer r.Close()

	// Between the SIGSTOP and the second lock: wake everything but the
	// straggler, let the job's own process exit, and wait until run has seen
	// it go — the exit landing in exactly the window the lock is let go for.
	var pid int
	pauseSignalled = func(j *Job) {
		r.mu.Lock()
		pgid := j.pgid
		r.mu.Unlock()
		_ = syscall.Kill(pid, syscall.SIGSTOP)
		_ = syscall.Kill(-pgid, syscall.SIGCONT)
		_ = syscall.Kill(pid, syscall.SIGSTOP)
		waitFor(5*time.Second, func() bool {
			r.mu.Lock()
			defer r.mu.Unlock()
			return j.exited
		})
	}
	t.Cleanup(func() { pauseSignalled = nil })

	repo := repoDir(t)
	job, err := r.SubmitCmd("update", repo, "Update · repo", gfy.Params{Repo: repo}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !waitFor(2*time.Second, func() bool { return r.pausable(job.ID) }) {
		t.Fatal("job never reached a pausable state")
	}
	pid = readPid(t, pidFile)
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })

	if r.Pause(job.ID) {
		t.Error("Pause reported a change on a job whose process had exited")
	}
	s := wait(t, r, job.ID)
	if s.Paused {
		t.Error("the finished job is recorded as Paused")
	}
	if s.Status != Succeeded {
		t.Errorf("Status = %v, want ok", s.Status)
	}
	// The straggler ignores SIGTERM and SIGHUP, so only the group reap's
	// SIGKILL ends it — which a stopped process still answers.
	if !waitFor(3*time.Second, func() bool { return syscall.Kill(pid, 0) != nil }) {
		t.Errorf("straggler %d outlived its job", pid)
	}
	if !strings.Contains(job.Log.String(), "SIGKILL") {
		t.Errorf("the group reap did not escalate:\n%s", job.Log.String())
	}
}
