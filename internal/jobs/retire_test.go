package jobs

import (
	"strings"
	"sync/atomic"
	"testing"

	"github.com/dns/ggraphify/internal/ringbuf"
)

// newRetireRunner builds the smallest Runner that can retire a job: the
// history ring's bound, an empty running map, and nothing else. The retirement
// path takes no goroutines and emits no events, so no New and no Close are
// needed.
func newRetireRunner(history int) *Runner {
	return &Runner{opts: Options{History: history}, running: map[uint64]*Job{}}
}

// retireOne retires a hand-built job and returns it, so a test can inspect the
// log compaction the runner applied to it.
func retireOne(r *Runner, id uint64, status Status, log *ringbuf.Buf) *Job {
	j := &Job{ID: id, Status: status, Log: log}
	r.retire(j)
	return j
}

// retireTestLog is a log of n 100-byte lines, comfortably under the default
// ring-buffer cap so nothing is ever evicted before retirement.
func retireTestLog(lines int) *ringbuf.Buf {
	b := ringbuf.New(0)
	line := strings.Repeat("a", 99) + "\n"
	for i := 0; i < lines; i++ {
		b.WriteString(line)
	}
	return b
}

// The history is a ring: once full, each retirement overwrites the oldest slot
// in place, and Snapshot reports what is left newest-first.
func TestRetireRingKeepsNewestHistory(t *testing.T) {
	r := newRetireRunner(5)
	for id := uint64(1); id <= 12; id++ {
		retireOne(r, id, Succeeded, ringbuf.New(0))
	}

	snap := r.Snapshot()
	got := make([]uint64, len(snap))
	for i, s := range snap {
		got[i] = s.ID
	}
	want := []uint64{12, 11, 10, 9, 8}
	if len(got) != len(want) {
		t.Fatalf("Snapshot returned %d jobs (%v), want %d (%v)", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Snapshot order = %v, want %v", got, want)
		}
	}
	if n := len(r.history); n != 5 {
		t.Fatalf("len(history) = %d, want 5", n)
	}
}

// A succeeded job's log is read, not written, and two hundred of them at full
// size is the board's largest idle allocation — so retirement shrinks it.
func TestRetireCompactsSucceededLogs(t *testing.T) {
	r := newRetireRunner(10)
	j := retireOne(r, 1, Succeeded, retireTestLog(1024))
	if got := j.Log.Len(); got > RetiredLogBytes {
		t.Fatalf("a retired succeeded log kept %d bytes, want <= %d", got, RetiredLogBytes)
	}
}

// A failure is the one finished job somebody reads all of, so the most recent
// KeepFullFailures of them keep their whole log and older ones are compacted
// only when they fall out of that window.
func TestRetireKeepsLastFailuresWhole(t *testing.T) {
	r := newRetireRunner(50)
	var jobs []*Job
	for id := uint64(1); id <= 12; id++ {
		jobs = append(jobs, retireOne(r, id, Failed, retireTestLog(1024)))
	}

	for i, j := range jobs {
		got := j.Log.Len()
		if i < 2 {
			if got > RetiredLogBytes {
				t.Errorf("failure %d fell out of the keep window but kept %d bytes, want <= %d",
					i+1, got, RetiredLogBytes)
			}
			continue
		}
		if got <= RetiredLogBytes {
			t.Errorf("failure %d is in the keep window but was compacted to %d bytes", i+1, got)
		}
	}
	if n := len(r.fullLogs); n != KeepFullFailures {
		t.Fatalf("len(fullLogs) = %d, want %d", n, KeepFullFailures)
	}
}

// fullLogs must not keep a job alive after the history has forgotten it: the
// two are pruned together as the ring overwrites old slots.
func TestRetireForgetsEvictedFailures(t *testing.T) {
	r := newRetireRunner(3)
	for id := uint64(1); id <= 5; id++ {
		retireOne(r, id, Failed, retireTestLog(1024))
	}

	if n := len(r.fullLogs); n > 3 {
		t.Fatalf("len(fullLogs) = %d, want <= 3 (the history bound)", n)
	}
	inHistory := make(map[*Job]bool, len(r.history))
	for _, j := range r.history {
		inHistory[j] = true
	}
	for _, j := range r.fullLogs {
		if !inHistory[j] {
			t.Fatalf("job %d is in fullLogs but no longer in history", j.ID)
		}
	}
}

// Live is Snapshot without the history: the per-tick callers never render a
// finished job, so they must not copy one.
func TestLiveReturnsOnlyQueuedAndRunning(t *testing.T) {
	r := newRetireRunner(10)
	r.queue = []*Job{
		{ID: 3, Status: Queued, Log: ringbuf.New(0)},
		{ID: 5, Status: Queued, Log: ringbuf.New(0)},
	}
	r.running = map[uint64]*Job{
		4: {ID: 4, Status: Running, Log: ringbuf.New(0)},
	}
	retireOne(r, 1, Succeeded, ringbuf.New(0))

	live := r.Live()
	got := make([]uint64, len(live))
	for i, s := range live {
		got[i] = s.ID
	}
	want := []uint64{5, 4, 3}
	if len(got) != len(want) {
		t.Fatalf("Live returned %d jobs (%v), want %d (%v)", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Live order = %v, want %v", got, want)
		}
	}
	if n := len(r.Snapshot()); n != 4 {
		t.Fatalf("Snapshot returned %d entries, want 4 (history still present)", n)
	}
}

// countingError counts how often its message is rendered. A finished job's
// error text does not change, so Snapshot must compute it once and reuse it —
// Snapshot runs many times a second across every retained job.
type countingError struct {
	msg string
	n   *int32
}

func (e countingError) Error() string {
	atomic.AddInt32(e.n, 1)
	return e.msg
}

func TestSnapshotErrStringComputedOnce(t *testing.T) {
	r := newRetireRunner(10)
	var calls int32
	const msg = "boom"
	j := &Job{
		ID:     1,
		Status: Failed,
		Log:    ringbuf.New(0),
		Err:    countingError{msg: msg, n: &calls},
	}
	r.retire(j)

	for i := 0; i < 5; i++ {
		snap := r.Snapshot()
		if len(snap) != 1 {
			t.Fatalf("snapshot %d: got %d jobs, want 1", i, len(snap))
		}
		if snap[0].Err != msg {
			t.Fatalf("snapshot %d: Err = %q, want %q", i, snap[0].Err, msg)
		}
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("Error() was called %d times, want 1", got)
	}
}
