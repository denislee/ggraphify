package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/dns/ggraphify/internal/gfy"
	"github.com/dns/ggraphify/internal/jobs"
)

// dockKey is what decides whether the strip is rebuilt. It must move when a
// job's status moves and stay put when only its elapsed time does, or the
// strip either freezes or rebuilds sixty times a minute.
func TestDockKey(t *testing.T) {
	started := time.Now().Add(-time.Minute)
	entries := func(snaps ...jobs.Snapshot) []dockEntry {
		out := []dockEntry{{header: "Running", count: len(snaps)}}
		for _, s := range snaps {
			out = append(out, dockEntry{snap: s})
		}
		return out
	}
	running := entries(jobs.Snapshot{ID: 1, Status: jobs.Running, Started: started})
	later := entries(jobs.Snapshot{ID: 1, Status: jobs.Running, Started: started})
	if dockKey(running) != dockKey(later) {
		t.Fatal("key moved without the queue moving")
	}
	done := entries(jobs.Snapshot{ID: 1, Status: jobs.Succeeded, Started: started})
	if dockKey(running) == dockKey(done) {
		t.Fatal("key did not move when a job finished")
	}
	two := entries(
		jobs.Snapshot{ID: 1, Status: jobs.Running, Started: started},
		jobs.Snapshot{ID: 2, Status: jobs.Queued},
	)
	if dockKey(running) == dockKey(two) {
		t.Fatal("key did not move when a job was queued")
	}
	// A header whose count or detail moved is a visible change too: the group
	// heading is what says "nothing is queued", and a stale one lies.
	lanes := []dockEntry{{header: "Running", count: 1, note: "free 1/4 · metered 0/1"}}
	busier := []dockEntry{{header: "Running", count: 1, note: "free 1/2 · metered 0/1"}}
	if dockKey(lanes) == dockKey(busier) {
		t.Fatal("key did not move when the lane usage changed")
	}
}

// The strip's status text is the whole point of the pane: every status has to
// name itself, and a failure has to carry its exit code.
func TestDockStatus(t *testing.T) {
	cases := []struct {
		name     string
		entry    dockEntry
		contains []string
		class    string
	}{
		{
			"queued",
			dockEntry{snap: jobs.Snapshot{ID: 7, Status: jobs.Queued}, pos: 2, wait: "waiting for a metered lane"},
			[]string{"queued", "#2", "waiting for a metered lane"},
			"st-none",
		},
		{
			"running",
			dockEntry{snap: jobs.Snapshot{ID: 7, Status: jobs.Running, Started: time.Now().Add(-90 * time.Second)}},
			[]string{"running", "1m30s"},
			"st-running",
		},
		{
			"ok",
			dockEntry{snap: jobs.Snapshot{ID: 7, Status: jobs.Succeeded, Started: time.Now().Add(-time.Minute), Ended: time.Now()}},
			[]string{"finished ok", "took"},
			"st-fresh",
		},
		{
			"failed",
			dockEntry{snap: jobs.Snapshot{ID: 7, Status: jobs.Failed, Exit: 2, Ended: time.Now()}},
			[]string{"FAILED", "exit 2"},
			"st-broken",
		},
		{
			"cancelled",
			dockEntry{snap: jobs.Snapshot{ID: 7, Status: jobs.Canceled, Ended: time.Now()}},
			[]string{"cancelled"},
			"st-none",
		},
	}
	for _, c := range cases {
		text, class := dockStatus(c.entry)
		for _, want := range c.contains {
			if !strings.Contains(text, want) {
				t.Errorf("%s status = %q, want it to mention %q", c.name, text, want)
			}
		}
		if class != c.class {
			t.Errorf("%s class = %q, want %q", c.name, class, c.class)
		}
		if !strings.Contains(boardCSS, "."+class) {
			t.Errorf("%s uses class %q, which the stylesheet does not define", c.name, class)
		}
	}
}

// outcomeNote is what the Finished heading says. It must break the group down
// rather than just counting it, because "5 finished" hides a failure.
func TestOutcomeNote(t *testing.T) {
	note := outcomeNote([]jobs.Snapshot{
		{Status: jobs.Succeeded}, {Status: jobs.Succeeded},
		{Status: jobs.Failed}, {Status: jobs.Canceled},
	})
	for _, want := range []string{"2 ok", "1 failed", "1 cancelled"} {
		if !strings.Contains(note, want) {
			t.Errorf("outcomeNote = %q, want it to mention %q", note, want)
		}
	}
	if got := outcomeNote(nil); got != "" {
		t.Errorf("outcomeNote(nil) = %q, want empty", got)
	}
}

// An empty group has to say so in words; a bare zero reads as a strip that
// failed to draw.
func TestEmptyGroupNote(t *testing.T) {
	for _, h := range []string{"Running", "Queued", "Finished"} {
		if emptyGroupNote(h) == "" {
			t.Errorf("no empty note for the %s group", h)
		}
	}
}

// The header classes are styled, not decorative: an undefined class is an
// invisible rendering bug.
func TestDockClassesDefined(t *testing.T) {
	for _, c := range []string{"dockhead", "dockheadrow", "docknote", "docklist", "dockbar"} {
		if !strings.Contains(boardCSS, "."+c) {
			t.Errorf("class %q is not defined in the stylesheet", c)
		}
	}
}

// Every status needs a glyph: a row whose mark is blank reads as a rendering
// bug rather than as a state.
func TestStateGlyphForJob(t *testing.T) {
	for _, st := range []jobs.Status{jobs.Queued, jobs.Running, jobs.Succeeded, jobs.Failed, jobs.Canceled} {
		if g := stateGlyphForJob(st); g == "" {
			t.Errorf("no glyph for %s", st)
		}
	}
}

func TestCheckClass(t *testing.T) {
	seen := map[string]bool{}
	for _, st := range []gfy.CheckState{gfy.CheckOK, gfy.CheckWarn, gfy.CheckBad} {
		c := checkClass(st)
		if c == "" {
			t.Fatalf("no CSS class for %s", st)
		}
		if seen[c] {
			t.Fatalf("%s reuses the class %q, so two outcomes look identical", st, c)
		}
		seen[c] = true
		if !strings.Contains(boardCSS, "."+c) {
			t.Errorf("class %q is not defined in the stylesheet", c)
		}
	}
}

func TestFirstNonEmpty(t *testing.T) {
	if got := firstNonEmpty("", "  ", "x", "y"); got != "x" {
		t.Fatalf("firstNonEmpty = %q", got)
	}
	if got := firstNonEmpty("", " "); got != "" {
		t.Fatalf("firstNonEmpty = %q, want empty", got)
	}
}

func TestDescribeOut(t *testing.T) {
	cases := []struct{ name, base, want string }{
		{"", "", "graphify's default inside each checkout"},
		{"graphify-out", "", "graphify-out/ inside each checkout"},
		{"", "/tmp/graphs", "one directory: /tmp/graphs"},
	}
	for _, c := range cases {
		if got := describeOut(c.name, c.base); got != c.want {
			t.Errorf("describeOut(%q, %q) = %q, want %q", c.name, c.base, got, c.want)
		}
	}
}
