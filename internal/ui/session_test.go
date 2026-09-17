package ui

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/dns/ggraphify/internal/gfy"
	"github.com/dns/ggraphify/internal/jobs"
	"github.com/dns/ggraphify/internal/store"
)

func testStore(t *testing.T) *store.Store {
	t.Helper()
	return store.Open(filepath.Join(t.TempDir(), "state.json"))
}

// What survives a restart, and what has to be asked for again.
func TestRestoredJobHoldPolicy(t *testing.T) {
	st := testStore(t)
	dir := t.TempDir()
	base := store.JobEntry{Repo: dir, Dir: dir, Status: "queued", Argv: []string{"graphify", "update"}}

	cases := []struct {
		name string
		e    store.JobEntry
		held bool
	}{
		{"free work starts on its own",
			func() store.JobEntry { e := base; e.Kind = "update"; e.Cost = "free"; return e }(), false},
		{"metered work waits for a click",
			func() store.JobEntry { e := base; e.Kind = "extract"; e.Cost = "metered"; return e }(), true},
		{"local work waits too — it is hours of this machine",
			func() store.JobEntry {
				e := base
				e.Kind = "update"
				e.Cost = "free"
				e.Local = true
				return e
			}(), true},
		{"a job that was already held stays held",
			func() store.JobEntry { e := base; e.Kind = "update"; e.Cost = "free"; e.Held = true; return e }(), true},
		{"a job that was mid-flight comes back held",
			func() store.JobEntry { e := base; e.Kind = "update"; e.Cost = "free"; e.Status = "running"; return e }(), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			j := restoredJob(c.e, st, "")
			if j == nil {
				t.Fatal("the job was dropped")
			}
			if j.Status != jobs.Queued {
				t.Fatalf("status is %s, want queued", j.Status)
			}
			if j.Held != c.held {
				t.Fatalf("held=%v, want %v", j.Held, c.held)
			}
		})
	}
}

// The cost is re-derived from the kind and widened by what the file says, so a
// kind that became metered between releases cannot come back as free work that
// starts itself.
func TestRestoredJobNeverLosesItsPrice(t *testing.T) {
	st := testStore(t)
	dir := t.TempDir()
	j := restoredJob(store.JobEntry{
		Kind: "a-kind-this-version-does-not-know", Cost: "metered", Status: "queued",
		Repo: dir, Dir: dir, Argv: []string{"graphify", "whatever"},
	}, st, "")
	if j == nil {
		t.Fatal("the job was dropped")
	}
	if j.Cost != gfy.Metered || !j.Held {
		t.Fatalf("cost=%s held=%v, want a held metered job", j.Cost, j.Held)
	}
}

// A checkout that is gone cannot be worked on, and a job that can only fail is
// noise at every launch.
func TestRestoreDropsAJobWhoseCheckoutIsGone(t *testing.T) {
	st := testStore(t)
	gone := filepath.Join(t.TempDir(), "deleted")
	if j := restoredJob(store.JobEntry{
		Kind: "update", Status: "queued", Repo: gone, Dir: gone,
		Argv: []string{"graphify", "update"},
	}, st, ""); j != nil {
		t.Fatal("a job for a checkout that no longer exists was queued")
	}
	// A FINISHED job for the same vanished checkout is history, not work, and
	// history about a deleted repository is still the truth about what ran.
	if j := restoredJob(store.JobEntry{
		Kind: "update", Status: "ok", Repo: gone, Dir: gone,
		Argv: []string{"graphify", "update"},
	}, st, ""); j == nil {
		t.Fatal("history was dropped along with the checkout")
	}
}

// The log tail is the reason a restored failure is worth looking at.
func TestRestoredFailureKeepsItsLog(t *testing.T) {
	st := testStore(t)
	j := restoredJob(store.JobEntry{
		Kind: "extract", Status: "failed", Exit: 2, Error: "exit status 2",
		Log: "Traceback (most recent call last)\n", Argv: []string{"graphify", "extract"},
	}, st, "")
	if j == nil {
		t.Fatal("the entry was dropped")
	}
	if j.Status != jobs.Failed || j.Exit != 2 {
		t.Fatalf("came back as %s (%d), want failed (2)", j.Status, j.Exit)
	}
	if j.Log.String() != "Traceback (most recent call last)\n" {
		t.Fatalf("the log tail was lost: %q", j.Log.String())
	}
	if j.Err == nil || j.Err.Error() != "exit status 2" {
		t.Fatalf("the error was lost: %v", j.Err)
	}
}

// An entry with no argv cannot be re-run and cannot be shown as a command.
func TestRestoreSkipsAnEntryWithNoCommand(t *testing.T) {
	if j := restoredJob(store.JobEntry{Kind: "update", Status: "queued"}, testStore(t), ""); j != nil {
		t.Fatal("an entry with no argv was restored")
	}
}

// A held job says so where somebody reads it.
func TestHeldJobReadsAsHeld(t *testing.T) {
	s := &jobs.Snapshot{Kind: "extract", Status: jobs.Queued, Held: true}
	text, _ := jobCell(s)
	if text != "held extract" {
		t.Fatalf("the Job column says %q for a held job", text)
	}
	s.Held = false
	if text, _ := jobCell(s); text != "queued extract" {
		t.Fatalf("the Job column says %q for a queued job", text)
	}
}

// A job restored without a label draws as a blank row in both the dock and the
// Jobs dialog, which both render s.Label and nothing else.
func TestRestoredJobWithoutALabelGetsOne(t *testing.T) {
	j := restoredJob(store.JobEntry{
		Kind: "update", Repo: "/src/nexus", Status: "ok",
		Argv: []string{"graphify", "update"},
	}, testStore(t), "")
	if j == nil {
		t.Fatal("a finished job was dropped")
	}
	if j.Label == "" {
		t.Fatal("restored with no label — the row has no name")
	}
	if !strings.Contains(j.Label, "nexus") {
		t.Fatalf("label %q does not name the checkout it ran against", j.Label)
	}
}

// The negative control: a recorded label is what comes back, untouched.
func TestRestoredJobKeepsItsRecordedLabel(t *testing.T) {
	j := restoredJob(store.JobEntry{
		Kind: "update", Repo: "/src/nexus", Label: "Auto-fix 3/9 · nexus", Status: "ok",
		Argv: []string{"graphify", "update"},
	}, testStore(t), "")
	if j == nil || j.Label != "Auto-fix 3/9 · nexus" {
		t.Fatalf("a recorded label was rewritten: %+v", j)
	}
}
