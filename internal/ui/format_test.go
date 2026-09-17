package ui

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/dns/ggraphify/internal/board"
	"github.com/dns/ggraphify/internal/discover"
	"github.com/dns/ggraphify/internal/graftstate"
	"github.com/dns/ggraphify/internal/graphstate"
	"github.com/dns/ggraphify/internal/jobs"
)

// readSource reads a file from this package's own directory, for the tests
// that assert on the source rather than on behaviour that needs a display.
func readSource(t *testing.T, name string) string {
	t.Helper()
	_, self, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate the package source")
	}
	b, err := os.ReadFile(filepath.Join(filepath.Dir(self), name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Every state must have a distinct glyph. Shape carries the meaning as well as
// colour does, so the board stays readable without colour vision — two states
// sharing a glyph would defeat that.
func TestStateGlyphsAreDistinct(t *testing.T) {
	seen := map[string]graphstate.State{}
	for _, s := range []graphstate.State{
		graphstate.StateNone, graphstate.StateRaw, graphstate.StateStale,
		graphstate.StateFresh, graphstate.StateBroken, graphstate.StateRunning,
	} {
		g := stateGlyph(s)
		if prev, ok := seen[g]; ok {
			t.Errorf("%v and %v share the glyph %q", prev, s, g)
		}
		seen[g] = s
		if stateWord(s) == "?" {
			t.Errorf("%v has no word", s)
		}
		if stateClass(s) == "" {
			t.Errorf("%v has no CSS class", s)
		}
	}
}

func TestGraphAndCommCells(t *testing.T) {
	if got := graphCell(graphstate.Graph{}); got != "" {
		t.Errorf("an empty graph renders %q, want nothing", got)
	}
	if got := graphCell(graphstate.Graph{Nodes: 2047, Links: 4835}); got != "2047n / 4835e" {
		t.Errorf("graphCell = %q", got)
	}
	// Placeholder community names are the difference between a graph that has
	// had the expensive labelling pass and one that has not, so the cell says
	// so rather than showing a bare count.
	if got := commCell(graphstate.Graph{Communities: 83, Labeled: false}); got != "83 unnamed" {
		t.Errorf("commCell unlabeled = %q", got)
	}
	if got := commCell(graphstate.Graph{Communities: 83, Labeled: true}); got != "83" {
		t.Errorf("commCell labeled = %q", got)
	}
}

func TestJobCell(t *testing.T) {
	if text, _ := jobCell(nil); text != "" {
		t.Errorf("no job renders %q", text)
	}
	now := time.Now()
	cases := []struct {
		s        jobs.Snapshot
		contains string
		class    string
	}{
		{jobs.Snapshot{Kind: "update", Status: jobs.Queued}, "queued", "st-none"},
		{jobs.Snapshot{Kind: "update", Status: jobs.Running, Started: now}, "update", "st-running"},
		{jobs.Snapshot{Kind: "update", Status: jobs.Succeeded, Started: now, Ended: now}, "ok", "st-fresh"},
		{jobs.Snapshot{Kind: "extract", Status: jobs.Failed, Exit: 3, Started: now, Ended: now}, "failed", "st-broken"},
		{jobs.Snapshot{Kind: "watch", Status: jobs.Canceled, Started: now, Ended: now}, "cancelled", "st-none"},
	}
	for _, c := range cases {
		s := c.s
		text, class := jobCell(&s)
		if !strings.Contains(text, c.contains) {
			t.Errorf("jobCell(%v) = %q, want it to mention %q", s.Status, text, c.contains)
		}
		if class != c.class {
			t.Errorf("jobCell(%v) class = %q, want %q", s.Status, class, c.class)
		}
	}
}

// The elapsed cell is repainted every second, so a widening string would make
// the column jitter.
func TestShortDurIsCompact(t *testing.T) {
	cases := map[time.Duration]string{
		3 * time.Second:             "3s",
		90 * time.Second:            "1m30s",
		2*time.Hour + 5*time.Minute: "2h05m",
	}
	for in, want := range cases {
		if got := shortDur(in); got != want {
			t.Errorf("shortDur(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestBranchCellMarksBehind(t *testing.T) {
	r := board.Row{Repo: discover.Repo{Branch: "main", HeadSHA: "1234567890"}, Behind: true}
	text, behind := branchCell(r)
	if !behind {
		t.Error("a behind row was not reported as behind")
	}
	if !strings.Contains(text, "main") || !strings.Contains(text, "1234567") {
		t.Errorf("branchCell = %q", text)
	}
}

func TestTooltipNamesTheImportantFacts(t *testing.T) {
	r := board.Row{
		Repo: discover.Repo{Name: "svc", Path: "/g/svc", Branch: "main", HeadSHA: "aaaaaaaaaa"},
		Graph: graphstate.Graph{
			State: graphstate.StateStale, Out: "/g/svc/graphify-out",
			Nodes: 10, Links: 20, Communities: 3, Labeled: false,
			DriftChanged: 4, BuiltCommit: "bbbbbbbbbb", BuiltAt: time.Now(),
			SizeBytes: 1 << 20, HasReport: true,
		},
		Behind: true,
	}
	tip := tooltip(r)
	for _, want := range []string{"svc", "/g/svc", "stale", "graphify-out", "placeholder names", "HEAD is now", "Drift"} {
		if !strings.Contains(tip, want) {
			t.Errorf("tooltip does not mention %q:\n%s", want, tip)
		}
	}
}

func TestPluralIsGrammatical(t *testing.T) {
	if got := plural(1, "repository", "repositories"); got != "1 repository" {
		t.Errorf("plural(1) = %q", got)
	}
	if got := plural(0, "repository", "repositories"); got != "0 repositories" {
		t.Errorf("plural(0) = %q", got)
	}
	if got := plural(7, "repository", "repositories"); got != "7 repositories" {
		t.Errorf("plural(7) = %q", got)
	}
}

func TestEllipsize(t *testing.T) {
	if got := ellipsize("short", 10); got != "short" {
		t.Errorf("ellipsize = %q", got)
	}
	if got := ellipsize("a-very-long-string", 8); got != "a-very-…" {
		t.Errorf("ellipsize = %q", got)
	}
}

// The drift sort key must order by weight, not by the rendered string: "+12
// ~30" and "~3" compare the wrong way round alphabetically, and Drift is the
// column people sort by to find what to update next.
func TestDriftWeightOrdersByMagnitude(t *testing.T) {
	small := &board.Row{Graph: graphstate.Graph{DriftChanged: 3}}
	large := &board.Row{Graph: graphstate.Graph{DriftAdded: 12, DriftChanged: 30}}
	pending := &board.Row{Graph: graphstate.Graph{NeedsUpdate: true}}

	if driftWeight(small) >= driftWeight(large) {
		t.Error("a larger drift must sort heavier")
	}
	// A pending semantic re-extraction outranks any amount of file drift: it
	// needs the expensive command rather than the free one.
	if driftWeight(pending) <= driftWeight(large) {
		t.Error("needs_update must outrank file drift")
	}
}

// Sorting the state column by the raw ordinal put "no graph" — 140 rows of it
// on this machine — above everything worth looking at. The default has to
// float what wants attention: running, broken, stale, unlabeled, fresh, none.
func TestStateWeightOrdersByAttention(t *testing.T) {
	a := &App{jobByRepo: map[string]*jobs.Snapshot{}}
	row := func(s graphstate.State) *board.Row {
		return &board.Row{Graph: graphstate.Graph{State: s}}
	}
	order := []graphstate.State{
		graphstate.StateBroken, graphstate.StateStale, graphstate.StateRaw,
		graphstate.StateFresh, graphstate.StateNone,
	}
	for i := 1; i < len(order); i++ {
		prev, cur := stateWeight(a, row(order[i-1])), stateWeight(a, row(order[i]))
		if prev >= cur {
			t.Errorf("%v (%d) must sort above %v (%d)", order[i-1], prev, order[i], cur)
		}
	}
	// And a row with a job in flight outranks all of them.
	busy := row(graphstate.StateNone)
	busy.Path = "/busy"
	a.jobByRepo["/busy"] = &jobs.Snapshot{Status: jobs.Running}
	if stateWeight(a, busy) >= stateWeight(a, row(graphstate.StateBroken)) {
		t.Error("a running job must sort to the very top")
	}
}

// A check detail is built from paths and error text and is rendered into an
// AdwActionRow subtitle, which parses Pango markup. A bare angle bracket there
// does not merely look wrong — GTK drops the whole string.
func TestEscapeMarkup(t *testing.T) {
	got := escapeMarkup(`Rebuild <repo>/graft & "friends"`)
	for _, bad := range []string{"<repo>", ` & `, `"friends"`} {
		if strings.Contains(got, bad) {
			t.Errorf("escapeMarkup left %q unescaped: %s", bad, got)
		}
	}
	if !strings.Contains(got, "&lt;repo&gt;") {
		t.Errorf("escapeMarkup = %q", got)
	}
}

func TestGraftFact(t *testing.T) {
	if got := graftFact(graftstate.Index{State: graftstate.StateNone}); got != "" {
		t.Errorf("a checkout graft never ran on says %q, want nothing", got)
	}
	got := graftFact(graftstate.Index{
		State: graftstate.StateStale, Nodes: 951, Files: 79, DriftChanged: 12, DriftRemoved: 3,
	})
	for _, want := range []string{"stale", "951 nodes", "79 files", "~12", "−3"} {
		if !strings.Contains(got, want) {
			t.Errorf("graftFact = %q, missing %q", got, want)
		}
	}
}

// The exposure note is about what graphify will do to graft's cards, so it is
// silent when there are no cards — and silent when git is already ignoring them.
func TestGraftExposureNote(t *testing.T) {
	cases := []struct {
		name string
		i    graftstate.Index
		want bool
	}{
		{"exposed index", graftstate.Index{State: graftstate.StateFresh, Exposed: true}, true},
		{"gitignored index", graftstate.Index{State: graftstate.StateFresh}, false},
		{"no index at all", graftstate.Index{State: graftstate.StateNone, Exposed: true}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := graftExposureNote(tc.i) != ""; got != tc.want {
				t.Errorf("note present = %v, want %v", got, tc.want)
			}
		})
	}
}

// The Job column sorts by how much attention a row wants. The bug it guards
// against: a row with NO job weighed less than a finished one, so a board
// sorted by this column put its blank cells in the middle — between the queued
// jobs and the finished ones — which reads as no sort at all on a board where
// most rows have never run anything.
func TestJobWeightOrdersByAttention(t *testing.T) {
	a := &App{jobByRepo: map[string]*jobs.Snapshot{}}
	row := func(path string, s *jobs.Snapshot) *board.Row {
		if s != nil {
			a.jobByRepo[path] = s
		}
		return &board.Row{Path: path}
	}
	order := []*board.Row{
		row("/failed", &jobs.Snapshot{Status: jobs.Failed}),
		row("/running", &jobs.Snapshot{Status: jobs.Running}),
		row("/held", &jobs.Snapshot{Status: jobs.Queued, Held: true}),
		row("/queued", &jobs.Snapshot{Status: jobs.Queued}),
		row("/ok", &jobs.Snapshot{Status: jobs.Succeeded}),
		row("/none", nil),
	}
	for i := 1; i < len(order); i++ {
		prev, cur := jobWeight(a, order[i-1]), jobWeight(a, order[i])
		if prev >= cur {
			t.Errorf("%s (%d) must sort above %s (%d)",
				order[i-1].Path, prev, order[i].Path, cur)
		}
	}
	// A cancelled job is finished, not outstanding: it belongs with the ones
	// that ran, never above the queue.
	cancelled := row("/cancelled", &jobs.Snapshot{Status: jobs.Canceled})
	if jobWeight(a, cancelled) <= jobWeight(a, order[3]) {
		t.Error("a cancelled job must not sort above a queued one")
	}
	// And still above a row that has no job at all.
	if jobWeight(a, cancelled) >= jobWeight(a, order[5]) {
		t.Error("a cancelled job must sort above a row with no job")
	}
}
