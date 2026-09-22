package ui

import (
	"strings"
	"testing"

	"github.com/dns/ggraphify/internal/autofix"
	"github.com/dns/ggraphify/internal/usage"
)

// The verdict, stated as a table. The middle row is the one the button exists
// to get right: a list that still recommends work, whose work is already done.
func TestAutoFixNowVerdict(t *testing.T) {
	cases := []struct {
		name                  string
		targets, broken, busy int
		wantRun               bool
		wantMsg               string
	}{
		{"nothing it can act on", 0, 0, 0, false, "repair on its own"},
		{"the fix already exists", 6, 0, 0, false, "already done"},
		{"every one of them is busy", 3, 3, 3, false, "already has a job running"},
		{"some busy, some not", 3, 3, 2, true, ""},
		{"work to do", 4, 4, 0, true, ""},
		{"stale list: fewer broken than listed", 8, 1, 0, true, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			msg, run := autoFixNowVerdict(c.targets, c.broken, c.busy)
			if run != c.wantRun {
				t.Fatalf("autoFixNowVerdict(%d,%d,%d) run = %v, want %v",
					c.targets, c.broken, c.busy, run, c.wantRun)
			}
			if run && msg != "" {
				t.Fatalf("a verdict that runs must not carry a message, got %q", msg)
			}
			if !run && !strings.Contains(msg, c.wantMsg) {
				t.Fatalf("message %q does not mention %q", msg, c.wantMsg)
			}
		})
	}
}

// Scope keeps the caller's order — the ranking of the list that produced it —
// and silently drops a repository that has left the board between the paint
// and the click.
func TestAutoFixScope(t *testing.T) {
	cands := []autofix.Candidate{{Path: "/a"}, {Path: "/b"}, {Path: "/c"}}

	all := autoFixScope(cands, nil)
	if len(all) != 3 {
		t.Fatalf("an empty scope is the whole board, got %d", len(all))
	}

	got := autoFixScope(cands, []string{"/c", "/gone", "/a"})
	if len(got) != 2 || got[0].Path != "/c" || got[1].Path != "/a" {
		t.Fatalf("got %v, want /c then /a", got)
	}
}

// AddRoot is the one recommendation the loop cannot act on, so a list of
// nothing but scan roots yields no targets at all.
func TestAutoFixTargets(t *testing.T) {
	recs := []usage.Rec{
		{Repo: "/one", Action: usage.Extract},
		{Repo: "/two", Action: usage.AddRoot},
		{Repo: "/three", Action: usage.Update},
	}
	got := autoFixTargets(recs)
	if len(got) != 2 || got[0] != "/one" || got[1] != "/three" {
		t.Fatalf("got %v, want /one and /three", got)
	}
	if n := len(autoFixTargets([]usage.Rec{{Repo: "/x", Action: usage.AddRoot}})); n != 0 {
		t.Fatalf("a list of scan roots has no targets, got %d", n)
	}
}

// The skip line names the first two and counts the rest, so one toast cannot
// grow with the size of the board.
func TestAutoFixSkipLine(t *testing.T) {
	if got := autoFixSkipLine(nil); !strings.Contains(got, "LLM") {
		t.Fatalf("no skips should still say why nothing ran, got %q", got)
	}
	skips := []autofix.Skip{
		{Name: "one", Why: "cooling down"},
		{Name: "two", Why: "a job is already running here"},
		{Name: "three", Why: "cooling down"},
		{Name: "four", Why: "cooling down"},
	}
	got := autoFixSkipLine(skips)
	for _, want := range []string{"one: cooling down", "two:", "2 others"} {
		if !strings.Contains(got, want) {
			t.Fatalf("skip line %q does not contain %q", got, want)
		}
	}
	if strings.Contains(got, "three") {
		t.Fatalf("skip line %q should not name past the second", got)
	}
}
