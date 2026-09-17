package gfy

import (
	"sync"
	"testing"
	"time"
)

// resetOllamaStart puts the package's "did we start it?" record back to no,
// which every test that touches it must do: it is process-wide state, and a
// test that left it set would make the next one believe it owned a server.
func resetOllamaStart(t *testing.T) {
	t.Helper()
	t.Cleanup(func() { noteOllamaStart(startedNot, 0) })
	noteOllamaStart(startedNot, 0)
}

// fakeServer stands in for ollama: it counts starts and stops, and records the
// start the way the real StartOllama's detached route does, so the lease
// counter's "is it ours?" checks see what they would see in production.
type fakeServer struct {
	mu            sync.Mutex
	up            bool
	starts, stops int
	startErr      error
}

// start mirrors the real StartOllama, INCLUDING its already-running
// short-circuit: a server that is up is not started again, and that call
// claims no ownership. Counting spawns rather than calls is what makes the
// tests below assertions about the server rather than about the code.
func (f *fakeServer) start() (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.startErr != nil {
		return "fake", f.startErr
	}
	if f.up {
		return "already running", nil
	}
	f.up = true
	f.starts++
	noteOllamaStart(startedDetached, 4242)
	return "fake start", nil
}

func (f *fakeServer) stop() (string, error) {
	f.mu.Lock()
	f.up = false
	f.stops++
	f.mu.Unlock()
	noteOllamaStart(startedNot, 0)
	return "fake stop", nil
}

func (f *fakeServer) counts() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.starts, f.stops
}

func newAuto(f *fakeServer, idle time.Duration) *AutoOllama {
	return &AutoOllama{
		Idle:  func() time.Duration { return idle },
		start: f.start,
		stop:  f.stop,
	}
}

// eventually polls because the stop is a timer callback on another goroutine;
// a bare sleep would either be flaky or slow, and this is both fast and
// robust.
func eventually(t *testing.T, want string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestAutoOllamaStartsOnceAndStopsWhenIdle(t *testing.T) {
	resetOllamaStart(t)
	f := &fakeServer{}
	a := newAuto(f, 10*time.Millisecond)

	// Two overlapping jobs are one server, not two starts.
	l1 := a.Acquire()
	l2 := a.Acquire()
	if starts, stops := f.counts(); starts != 1 || stops != 0 {
		t.Fatalf("two leases: starts=%d stops=%d, want 1/0", starts, stops)
	}
	if n := a.Leases(); n != 2 {
		t.Fatalf("Leases() = %d, want 2", n)
	}

	// The first release is not the last one, so nothing may be stopped: this
	// is the case that a naive "stop when a job ends" would get wrong.
	l1.Release()
	time.Sleep(30 * time.Millisecond)
	if _, stops := f.counts(); stops != 0 {
		t.Fatalf("stopped while a lease was still out (stops=%d)", stops)
	}

	l2.Release()
	eventually(t, "the idle stop", func() bool { _, stops := f.counts(); return stops == 1 })
	if n := a.Leases(); n != 0 {
		t.Fatalf("Leases() = %d after both released, want 0", n)
	}
}

func TestAutoOllamaIdleStopCancelledByANewJob(t *testing.T) {
	resetOllamaStart(t)
	f := &fakeServer{}
	a := newAuto(f, 50*time.Millisecond)

	a.Acquire().Release()
	// A job arriving inside the grace period must cancel the stop outright —
	// the whole point of the grace period is that the gaps within a sweep are
	// free — and must not start a second server either.
	time.Sleep(10 * time.Millisecond)
	lease := a.Acquire()
	time.Sleep(80 * time.Millisecond)
	if starts, stops := f.counts(); starts != 1 || stops != 0 {
		t.Fatalf("job inside the grace period: spawns=%d stops=%d, want 1/0", starts, stops)
	}

	lease.Release()
	eventually(t, "the stop after the second job", func() bool { _, stops := f.counts(); return stops == 1 })
}

// The rule the whole design rests on: a server the board did not start is
// never one the board stops.
func TestAutoOllamaNeverStopsAServerItDidNotStart(t *testing.T) {
	resetOllamaStart(t)
	f := &fakeServer{}
	f.up = true // somebody else's ollama, answering before the board asked
	a := newAuto(f, 10*time.Millisecond)

	a.Acquire().Release()
	time.Sleep(60 * time.Millisecond)
	if _, stops := f.counts(); stops != 0 {
		t.Fatalf("stopped a server it did not start (stops=%d)", stops)
	}
	// And the same at exit, which is the other path that could reach a stop.
	a.Shutdown()
	if _, stops := f.counts(); stops != 0 {
		t.Fatalf("Shutdown stopped a server it did not start (stops=%d)", stops)
	}
}

func TestAutoOllamaDisabledDoesNothing(t *testing.T) {
	resetOllamaStart(t)
	f := &fakeServer{}
	a := newAuto(f, time.Millisecond)
	a.Enabled = func() bool { return false }

	lease := a.Acquire()
	if lease == nil {
		t.Fatal("Acquire returned nil; a job would panic on it")
	}
	lease.Release()
	time.Sleep(20 * time.Millisecond)
	if starts, stops := f.counts(); starts != 0 || stops != 0 {
		t.Fatalf("switched off: starts=%d stops=%d, want 0/0", starts, stops)
	}
}

// A nil supervisor is what a caller that never built one has, and every method
// has to survive it rather than making each call site check.
func TestAutoOllamaNilIsInert(t *testing.T) {
	var a *AutoOllama
	l := a.Acquire()
	l.Suspend()
	l.Resume()
	l.Release()
	a.Shutdown()

	// And a nil lease, which is what the runner holds for a job the hook
	// declined.
	var nl *OllamaLease
	nl.Suspend()
	nl.Resume()
	nl.Release()
	if nl.Suspended() {
		t.Fatal("a nil lease reports itself suspended")
	}
	if n := a.Leases(); n != 0 {
		t.Fatalf("Leases() on nil = %d, want 0", n)
	}
}

func TestAutoOllamaReleaseIsIdempotent(t *testing.T) {
	resetOllamaStart(t)
	f := &fakeServer{}
	a := newAuto(f, time.Hour)

	lease := a.Acquire()
	other := a.Acquire()
	lease.Release()
	lease.Release() // a deferred release next to an early return, called twice
	if n := a.Leases(); n != 1 {
		t.Fatalf("Leases() = %d after a double release, want 1", n)
	}
	other.Release()
}

func TestAutoOllamaShutdownStopsWhatItStarted(t *testing.T) {
	resetOllamaStart(t)
	f := &fakeServer{}
	a := newAuto(f, time.Hour) // long enough that only Shutdown can stop it

	a.Acquire().Release()
	a.Shutdown()
	if starts, stops := f.counts(); starts != 1 || stops != 1 {
		t.Fatalf("Shutdown: starts=%d stops=%d, want 1/1", starts, stops)
	}
}

// A failed start is logged and the job runs anyway: graphify's own error names
// the endpoint, and a second diagnosis of the same fact is worse than none.
func TestAutoOllamaStartFailureStillLeases(t *testing.T) {
	resetOllamaStart(t)
	f := &fakeServer{startErr: errNoOllama}
	a := newAuto(f, 10*time.Millisecond)
	var logged int
	a.Logf = func(string, ...any) { logged++ }

	lease := a.Acquire()
	if n := a.Leases(); n != 1 {
		t.Fatalf("Leases() = %d after a failed start, want 1", n)
	}
	lease.Release()
	time.Sleep(40 * time.Millisecond)
	if _, stops := f.counts(); stops != 0 {
		t.Fatalf("stopped a server that never started (stops=%d)", stops)
	}
	if logged == 0 {
		t.Fatal("a failed start was not logged")
	}
}

func TestStopOllamaRefusesWhenNothingWasStarted(t *testing.T) {
	resetOllamaStart(t)
	if _, err := StopOllama(); !NotOurs(err) {
		t.Fatalf("StopOllama with no recorded start: err = %v, want NotOurs", err)
	}
	if StartedOllama() {
		t.Fatal("StartedOllama() is true with no recorded start")
	}
}

func TestStartedOllamaTracksTheRecord(t *testing.T) {
	resetOllamaStart(t)
	noteOllamaStart(startedUserUnit, 0)
	if !StartedOllama() {
		t.Fatal("StartedOllama() is false after a recorded user-unit start")
	}
	noteOllamaStart(startedNot, 0)
	if StartedOllama() {
		t.Fatal("StartedOllama() is true after the record was cleared")
	}
}

func TestAutoOllamaIdleFallsBackToTheDefault(t *testing.T) {
	a := &AutoOllama{}
	if got := a.idle(); got != DefaultAutoOllamaIdle {
		t.Fatalf("idle() with no Idle func = %v, want %v", got, DefaultAutoOllamaIdle)
	}
	// A zero or negative stored value is a value nobody meant, not an
	// instruction to stop instantly.
	a.Idle = func() time.Duration { return 0 }
	if got := a.idle(); got != DefaultAutoOllamaIdle {
		t.Fatalf("idle() with a zero Idle = %v, want %v", got, DefaultAutoOllamaIdle)
	}
}

// A server started by hand is exempt from both automatic stops. The Start
// button is a person saying they want ollama running; a timer they never saw
// must not overrule that, and neither must quitting the board.
func TestAutoOllamaLeavesAHandStartedServerAlone(t *testing.T) {
	resetOllamaStart(t)
	f := &fakeServer{}
	a := newAuto(f, 10*time.Millisecond)

	// The Start button's sequence: start, then pin.
	if _, err := f.start(); err != nil {
		t.Fatal(err)
	}
	PinOllama()
	if !OllamaPinned() {
		t.Fatal("OllamaPinned() is false after PinOllama()")
	}

	// A job comes and goes against it. The lease must not take the server
	// down when it ends.
	a.Acquire().Release()
	time.Sleep(60 * time.Millisecond)
	if _, stops := f.counts(); stops != 0 {
		t.Fatalf("the idle timer stopped a hand-started server (stops=%d)", stops)
	}
	a.Shutdown()
	if _, stops := f.counts(); stops != 0 {
		t.Fatalf("exit stopped a hand-started server (stops=%d)", stops)
	}
}

// Pinning during the grace period is the same statement, made later.
func TestAutoOllamaPinDuringTheGracePeriod(t *testing.T) {
	resetOllamaStart(t)
	f := &fakeServer{}
	a := newAuto(f, 60*time.Millisecond)

	a.Acquire().Release()
	PinOllama()
	time.Sleep(120 * time.Millisecond)
	if _, stops := f.counts(); stops != 0 {
		t.Fatalf("stopped a server pinned during the grace period (stops=%d)", stops)
	}
}

// And a stop clears the pin, so the next auto-started server is managed again
// rather than inheriting an exemption from a server that no longer exists.
func TestStopClearsThePin(t *testing.T) {
	resetOllamaStart(t)
	f := &fakeServer{}
	if _, err := f.start(); err != nil {
		t.Fatal(err)
	}
	PinOllama()
	if _, err := f.stop(); err != nil {
		t.Fatal(err)
	}
	if OllamaPinned() {
		t.Fatal("the pin survived the server it belonged to")
	}
}

// Pausing a job gives the server up AT ONCE, not after the grace period. A
// pause is somebody asking for their machine back; answering "in five
// minutes" would be answering a different question.
func TestSuspendStopsTheServerImmediately(t *testing.T) {
	resetOllamaStart(t)
	f := &fakeServer{}
	a := newAuto(f, time.Hour) // long enough that only an immediate stop can pass

	lease := a.Acquire()
	lease.Suspend()
	if starts, stops := f.counts(); starts != 1 || stops != 1 {
		t.Fatalf("after Suspend: spawns=%d stops=%d, want 1/1", starts, stops)
	}
	if n := a.Leases(); n != 0 {
		t.Fatalf("Leases() = %d while suspended, want 0", n)
	}
	if !lease.Suspended() {
		t.Fatal("Suspended() is false after Suspend()")
	}

	// And resuming brings it back, which is the half the job depends on.
	lease.Resume()
	if starts, stops := f.counts(); starts != 2 || stops != 1 {
		t.Fatalf("after Resume: spawns=%d stops=%d, want 2/1", starts, stops)
	}
	if n := a.Leases(); n != 1 {
		t.Fatalf("Leases() = %d after Resume, want 1", n)
	}
	if lease.Suspended() {
		t.Fatal("Suspended() is true after Resume()")
	}
	lease.Release()
}

// One paused job among several must not take the server away from the others.
// This is the case a bare "pause stops ollama" would get wrong.
func TestSuspendKeepsTheServerForOtherJobs(t *testing.T) {
	resetOllamaStart(t)
	f := &fakeServer{}
	a := newAuto(f, time.Hour)

	paused := a.Acquire()
	running := a.Acquire()
	paused.Suspend()
	if starts, stops := f.counts(); starts != 1 || stops != 0 {
		t.Fatalf("one of two paused: spawns=%d stops=%d, want 1/0", starts, stops)
	}
	if n := a.Leases(); n != 1 {
		t.Fatalf("Leases() = %d, want 1 (the job still running)", n)
	}

	// Resuming it must not start a second server either.
	paused.Resume()
	if starts, _ := f.counts(); starts != 1 {
		t.Fatalf("resume spawned a second server (spawns=%d)", starts)
	}
	paused.Release()
	running.Release()
}

// Cancelling a paused job reaches Release with the lease already given back.
// Taking a second count off would stop a server another job is using, which
// is why Release is a state machine and not a decrement.
func TestReleaseWhileSuspendedGivesNothingBackTwice(t *testing.T) {
	resetOllamaStart(t)
	f := &fakeServer{}
	a := newAuto(f, time.Hour)

	other := a.Acquire()
	cancelled := a.Acquire()
	cancelled.Suspend()
	cancelled.Release()
	if n := a.Leases(); n != 1 {
		t.Fatalf("Leases() = %d after a suspended lease was released, want 1", n)
	}
	if _, stops := f.counts(); stops != 0 {
		t.Fatalf("stopped a server the other job was using (stops=%d)", stops)
	}
	// And the released lease is inert: a stray resume must not resurrect it.
	cancelled.Resume()
	if n := a.Leases(); n != 1 {
		t.Fatalf("a released lease was resumed (Leases() = %d, want 1)", n)
	}
	other.Release()
}

// Suspend and Resume repeated, and in the wrong order, change nothing: the
// runner pairs them correctly, but nothing downstream should depend on that.
func TestSuspendResumeAreIdempotent(t *testing.T) {
	resetOllamaStart(t)
	f := &fakeServer{}
	a := newAuto(f, time.Hour)

	lease := a.Acquire()
	lease.Resume() // not suspended: nothing to do
	lease.Suspend()
	lease.Suspend() // already given back
	if n := a.Leases(); n != 0 {
		t.Fatalf("Leases() = %d after a double suspend, want 0", n)
	}
	lease.Resume()
	lease.Resume() // already held
	if n := a.Leases(); n != 1 {
		t.Fatalf("Leases() = %d after a double resume, want 1", n)
	}
	if starts, stops := f.counts(); starts != 2 || stops != 1 {
		t.Fatalf("spawns=%d stops=%d, want 2/1 — one stop and one restart", starts, stops)
	}
	lease.Release()
}

// With the lifecycle switched off, a pause must not reach for the server
// either — the whole feature is one switch, not two.
func TestSuspendDoesNothingWhenDisabled(t *testing.T) {
	resetOllamaStart(t)
	f := &fakeServer{}
	a := newAuto(f, time.Millisecond)
	a.Enabled = func() bool { return false }

	lease := a.Acquire()
	lease.Suspend()
	lease.Resume()
	lease.Release()
	if starts, stops := f.counts(); starts != 0 || stops != 0 {
		t.Fatalf("switched off: spawns=%d stops=%d, want 0/0", starts, stops)
	}
}

// A hand-started server is exempt from the pause stop as well. The Start
// button is a person saying they want ollama running, and pausing a job is
// not a retraction of that.
func TestSuspendLeavesAHandStartedServerAlone(t *testing.T) {
	resetOllamaStart(t)
	f := &fakeServer{}
	a := newAuto(f, time.Hour)
	if _, err := f.start(); err != nil {
		t.Fatal(err)
	}
	PinOllama()

	lease := a.Acquire()
	lease.Suspend()
	if _, stops := f.counts(); stops != 0 {
		t.Fatalf("a pause stopped a hand-started server (stops=%d)", stops)
	}
	lease.Resume()
	lease.Release()
}
