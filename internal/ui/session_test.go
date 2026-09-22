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
		// The deep graft pass has no --backend in its argv, so where it ran
		// is read off the sidecar's own Local flag. A local one is free; the
		// same kind pointed at a vendor is not, and a restart must not be the
		// moment that distinction is lost.
		{"a local deep graft index restores free",
			func() store.JobEntry {
				e := base
				e.Kind = gfy.GraftDeepKind
				e.Cost = "free"
				e.Local = true
				return e
			}(), true},
		{"a metered deep graft index restores metered",
			func() store.JobEntry {
				e := base
				e.Kind = gfy.GraftDeepKind
				e.Cost = "metered"
				return e
			}(), true},
		{"a job that was already held stays held",
			func() store.JobEntry { e := base; e.Kind = "update"; e.Cost = "free"; e.Held = true; return e }(), true},
		// The interrupted ones. A window that closed over a running job is not
		// a decision about that job, so the board picks it up again — unless
		// picking it up means spending money, or unless the job had been
		// stopped by hand before the window closed.
		{"a job that was mid-flight resumes on its own",
			func() store.JobEntry { e := base; e.Kind = "update"; e.Cost = "free"; e.Status = "running"; return e }(), false},
		{"a local extraction that was mid-flight resumes — it costs hours, not money",
			func() store.JobEntry {
				e := base
				e.Kind = "extract"
				e.Cost = "metered"
				e.Local = true
				e.Status = "running"
				return e
			}(), false},
		{"a billed extraction that was mid-flight waits for a click",
			func() store.JobEntry {
				e := base
				e.Kind = "extract"
				e.Cost = "metered"
				e.Status = "running"
				return e
			}(), true},
		{"a job paused by hand stays stopped across a restart",
			func() store.JobEntry {
				e := base
				e.Kind = "update"
				e.Cost = "free"
				e.Status = "running"
				e.Paused = true
				return e
			}(), true},
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
			if c.e.Kind == gfy.GraftDeepKind {
				want := gfy.Metered
				if c.e.Local {
					want = gfy.Free
				}
				if j.Cost != want {
					t.Fatalf("cost=%v, want %v", j.Cost, want)
				}
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

// What a finished job invalidates. The cases that matter are the two the board
// got wrong: a failure is scoped exactly like a success (a killed extract
// leaves a half-written graph.json behind, which is a row that has to start
// saying `broken`), and a job with no repository of its own has no row to drop
// but still deserves a rescan.
func TestDeriveScopeOf(t *testing.T) {
	cases := []struct {
		kind, repo string
		want       deriveScope
	}{
		{"update", "/r", scopeRepo},
		{"extract", "/r", scopeRepo},
		{"cluster-only", "/r", scopeRepo},
		{"export-html", "/r", scopeRepo},
		{"graft-init", "/r", scopeGraft},
		{"install", "", scopeBoard},
		{"global-list", "", scopeNone}, // reads, writes nothing
		// Membership changes write ~/.graphify and nothing a row is derived
		// from, so they drop the membership memo and nothing else.
		{"global-add", "/r", scopeGlobal},
		{"global-remove", "/r", scopeGlobal},
		{"merge-graphs", "", scopeNone}, // writes a file of its own, not an output dir
		{"query", "/r", scopeNone},      // reads the graph
		{"nonesuch", "/r", scopeNone},   // unknown kinds are not assumed to mutate
	}
	for _, c := range cases {
		if got := deriveScopeOf(c.kind, c.repo); got != c.want {
			t.Errorf("deriveScopeOf(%q, %q) = %d, want %d", c.kind, c.repo, got, c.want)
		}
	}
}

// A resumed job says in its own log where the interrupted run had got to, and
// that the command starts again rather than picking up there. Both halves
// matter: "4/19" is the only trace of the work that was lost, and a row that
// said "resumed" without the second sentence would promise a checkpoint
// graphify does not have.
func TestAResumedJobSaysWhereItWasAndThatItStartsOver(t *testing.T) {
	st := testStore(t)
	dir := t.TempDir()
	e := store.JobEntry{
		Kind: "extract", Repo: dir, Dir: dir, Status: "running", Local: true,
		Cost: "metered", Argv: []string{"graphify", "extract", dir},
		Log: "extract: chunk [4/19] internal/ui/dock.go\n",
	}

	j := restoredJob(e, st, "")
	if j == nil {
		t.Fatal("the job was dropped")
	}
	if j.Held {
		t.Fatal("a local extraction interrupted by the last session came back held")
	}
	log := j.Log.Tail(40)
	for _, want := range []string{"4/19", "runs again from the start"} {
		if !strings.Contains(log, want) {
			t.Fatalf("the restored log does not mention %q:\n%s", want, log)
		}
	}
}

// The same entry, paused: no restart, and the log says which of the two
// reasons it is being held for.
func TestAPausedJobIsHeldRatherThanResumed(t *testing.T) {
	st := testStore(t)
	dir := t.TempDir()
	e := store.JobEntry{
		Kind: "extract", Repo: dir, Dir: dir, Status: "running", Local: true,
		Cost: "metered", Paused: true, Argv: []string{"graphify", "extract", dir},
		Log: "extract: chunk [4/19] internal/ui/dock.go\n",
	}

	j := restoredJob(e, st, "")
	if j == nil {
		t.Fatal("the job was dropped")
	}
	if !j.Held {
		t.Fatal("a job paused by hand restarted itself after a reopen")
	}
	if log := j.Log.Tail(40); !strings.Contains(log, "stopped by hand") {
		t.Fatalf("the restored log does not say why it is held:\n%s", log)
	}
}
