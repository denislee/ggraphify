package jobs

import (
	"testing"
	"time"

	"github.com/dns/ggraphify/internal/gfy"
	"github.com/dns/ggraphify/internal/ringbuf"
)

// A restored queue runs. This is the whole point: a sweep interrupted by a
// restart continues where it was rather than having to be started again.
func TestRestoredQueuedJobRuns(t *testing.T) {
	bin := fakeGraphify(t, "echo restored")
	r := New(Options{})
	defer r.Close()

	n := r.Restore([]*Job{{
		Kind: "update", Repo: repoDir(t), Label: "Update", Argv: []string{bin, "update"},
		Dir: repoDir(t), Status: Queued,
	}})
	if n != 1 {
		t.Fatalf("Restore reported %d jobs, want 1", n)
	}
	s := wait(t, r, 1)
	if s.Status != Succeeded {
		t.Fatalf("restored job ended %s, want ok", s.Status)
	}
}

// A held job is not dispatched, and it does not block the queue behind it.
func TestHeldJobWaitsAndDoesNotBlockTheQueue(t *testing.T) {
	bin := fakeGraphify(t, "echo hi")
	r := New(Options{})
	defer r.Close()

	r.Restore([]*Job{
		{Kind: "extract", Label: "Extract", Cost: gfy.Metered, Argv: []string{bin, "extract"},
			Dir: repoDir(t), Status: Queued, Held: true},
		{Kind: "update", Label: "Update", Argv: []string{bin, "update"},
			Dir: repoDir(t), Status: Queued},
	})

	// The free job behind the held one finishes...
	if s := wait(t, r, 2); s.Status != Succeeded {
		t.Fatalf("the job behind the held one ended %s, want ok", s.Status)
	}
	// ...while the held one has not started at all.
	if s := snapOf(t, r, 1); s.Status != Queued || !s.Held {
		t.Fatalf("the held job is %s (held=%v), want a held queued job", s.Status, s.Held)
	}
	if n := r.HeldCount(); n != 1 {
		t.Fatalf("HeldCount is %d, want 1", n)
	}
}

// Release is the click the held job was waiting for.
func TestReleaseStartsAHeldJob(t *testing.T) {
	bin := fakeGraphify(t, "echo go")
	r := New(Options{})
	defer r.Close()

	r.Restore([]*Job{{
		Kind: "extract", Label: "Extract", Cost: gfy.Metered, Argv: []string{bin, "extract"},
		Dir: repoDir(t), Status: Queued, Held: true,
	}})
	if r.Release(999) {
		t.Fatal("Release claimed to have started a job that does not exist")
	}
	if !r.Release(1) {
		t.Fatal("Release did not start the held job")
	}
	if r.Release(1) {
		t.Fatal("Release started the same job twice")
	}
	if s := wait(t, r, 1); s.Status != Succeeded {
		t.Fatalf("the released job ended %s, want ok", s.Status)
	}
}

func TestReleaseAllStartsEveryHeldJob(t *testing.T) {
	bin := fakeGraphify(t, "echo go")
	r := New(Options{})
	defer r.Close()

	r.Restore([]*Job{
		{Kind: "extract", Cost: gfy.Metered, Argv: []string{bin, "extract"},
			Dir: repoDir(t), Status: Queued, Held: true},
		{Kind: "label", Cost: gfy.Metered, Argv: []string{bin, "label"},
			Dir: repoDir(t), Status: Queued, Held: true},
	})
	if n := r.ReleaseAll(); n != 2 {
		t.Fatalf("ReleaseAll released %d, want 2", n)
	}
	if n := r.ReleaseAll(); n != 0 {
		t.Fatalf("ReleaseAll released %d on a queue with nothing held, want 0", n)
	}
	for _, id := range []uint64{1, 2} {
		if s := wait(t, r, id); s.Status != Succeeded {
			t.Fatalf("job %d ended %s, want ok", id, s.Status)
		}
	}
}

// A finished job comes back as history, not as work. Restoring it must not
// re-run it, and the list it lands in stays newest-first.
func TestRestoredFinishedJobsAreHistoryNotWork(t *testing.T) {
	r := New(Options{})
	defer r.Close()

	log := ringbuf.New(0)
	log.WriteString("traceback\n")
	// Oldest first, which is the order Restore documents.
	r.Restore([]*Job{
		{Kind: "update", Status: Succeeded, Argv: []string{"graphify", "update"},
			Started: time.Now().Add(-time.Hour), Ended: time.Now().Add(-time.Hour)},
		{Kind: "extract", Status: Failed, Exit: 2, Log: log,
			Argv:    []string{"graphify", "extract"},
			Started: time.Now().Add(-time.Minute), Ended: time.Now()},
	})
	if q, run := r.Active(); q != 0 || run != 0 {
		t.Fatalf("a finished job was queued: %d queued, %d running", q, run)
	}
	snaps := r.Snapshot()
	if len(snaps) != 2 {
		t.Fatalf("history has %d entries, want 2", len(snaps))
	}
	if snaps[0].Kind != "extract" {
		t.Fatalf("history is not newest-first: %s came first", snaps[0].Kind)
	}
	if snaps[0].Log.String() != "traceback\n" {
		t.Fatalf("the restored log was lost: %q", snaps[0].Log.String())
	}
}

// A job that was RUNNING when the last session ended is outstanding work, not
// a running process: its process group died with that session.
func TestRestoredRunningJobComesBackQueued(t *testing.T) {
	bin := fakeGraphify(t, "echo again")
	r := New(Options{})
	defer r.Close()

	r.Restore([]*Job{{
		Kind: "update", Argv: []string{bin, "update"}, Dir: repoDir(t),
		Status: Running, Started: time.Now().Add(-time.Hour), Held: true,
	}})
	s := snapOf(t, r, 1)
	if s.Status != Queued {
		t.Fatalf("a restored running job is %s, want queued", s.Status)
	}
	if !s.Started.IsZero() {
		t.Fatal("a restored running job kept the start time of a process that is gone")
	}
	if !s.Held {
		t.Fatal("a restored running job was not held")
	}
}

func TestParseStatusRoundTrips(t *testing.T) {
	for _, st := range []Status{Queued, Running, Succeeded, Failed, Canceled} {
		got, ok := ParseStatus(st.String())
		if !ok || got != st {
			t.Fatalf("%s did not round-trip: got %s, ok=%v", st, got, ok)
		}
	}
	if _, ok := ParseStatus("something-a-later-version-wrote"); ok {
		t.Fatal("an unknown status was accepted")
	}
}
