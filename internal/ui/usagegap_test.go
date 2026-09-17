package ui

import (
	"strings"
	"testing"

	"github.com/dns/ggraphify/internal/board"
	"github.com/dns/ggraphify/internal/discover"
	"github.com/dns/ggraphify/internal/graphstate"
	"github.com/dns/ggraphify/internal/usage"
)

// Both halves of "used, and no graph to answer with" were already on the
// board — the sparkline in Used, the dot in the state column — and reading
// them together meant tracking two columns across ninety rows. These tests
// pin the mark that states the conjunction in one place.

func gapRow(path string, st graphstate.State) *board.Row {
	r := &board.Row{Repo: discover.Repo{Path: path, Name: path}}
	r.Graph.State = st
	return r
}

func gapApp(path string, uses int) *App {
	a := &App{}
	a.usageUses = map[string]usage.RepoUse{
		path: {Graphify: uses, Series: []int{uses}},
	}
	return a
}

func TestUsageCellMarksTheGap(t *testing.T) {
	u := usage.RepoUse{Graphify: 26, Series: []int{4, 9, 13}}

	cell, class := usageCell(u, true)
	if !strings.HasSuffix(cell, "✕") {
		t.Errorf("a gap row is unmarked: %q", cell)
	}
	if class != "st-broken" {
		t.Errorf("class = %q, want st-broken so the mark carries the error colour", class)
	}

	cell, class = usageCell(u, false)
	if strings.Contains(cell, "✕") {
		t.Errorf("a graphed row was marked: %q", cell)
	}
	if class != "" {
		t.Errorf("class = %q, want none", class)
	}

	// An unused row is dim whether or not it has a graph: there is no gap to
	// report where there was no usage to leave unanswered.
	cell, class = usageCell(usage.RepoUse{}, true)
	if cell != "" || class != "st-none" {
		t.Errorf("unused row rendered %q/%q, want empty and dim", cell, class)
	}
}

func TestUsageGapFollowsTheSharedPredicate(t *testing.T) {
	cases := []struct {
		name  string
		state graphstate.State
		uses  int
		want  bool
	}{
		{"used, no graph", graphstate.StateNone, 26, true},
		{"used, broken graph", graphstate.StateBroken, 3, true},
		{"used, fresh graph", graphstate.StateFresh, 26, false},
		{"never used, no graph", graphstate.StateNone, 0, false},
	}
	for _, c := range cases {
		a := gapApp("/r", c.uses)
		if got := a.usageGap(gapRow("/r", c.state)); got != c.want {
			t.Errorf("%s: usageGap = %v, want %v", c.name, got, c.want)
		}
	}
}

// The chip is not a graph state, so it has to be answered before ParseState is
// consulted — a lookup of "used-no-graph" fails there, and the board would
// silently show every row instead of the handful worth acting on.
func TestGapFilterIsNotParsedAsAState(t *testing.T) {
	if _, ok := graphstate.ParseState(filterGap); ok {
		t.Fatalf("%q parses as a graph state; the filter's whole premise is that it does not", filterGap)
	}

	a := gapApp("/r", 26)
	a.filterID = filterGap
	if !a.matches(gapRow("/r", graphstate.StateNone)) {
		t.Error("a used, ungraphed row was filtered out by its own chip")
	}
	if a.matches(gapRow("/r", graphstate.StateFresh)) {
		t.Error("a graphed row survived the gap filter")
	}

	b := gapApp("/quiet", 0)
	b.filterID = filterGap
	if b.matches(gapRow("/quiet", graphstate.StateNone)) {
		t.Error("an unused row survived the gap filter")
	}
}

// publishUsage republishes on a two-second tick while the live watch is on,
// and each republish that claims a change costs a full re-filter and re-sort
// of every row. sameUses is what keeps a quiet tick quiet.
func TestSameUsesDetectsWhatTheBoardRenders(t *testing.T) {
	base := map[string]usage.RepoUse{
		"/a": {Graphify: 3, Graft: 1, Series: []int{1, 2}},
		"/b": {Graft: 7, Series: []int{7}},
	}
	same := map[string]usage.RepoUse{
		"/a": {Graphify: 3, Graft: 1, Series: []int{1, 2}},
		"/b": {Graft: 7, Series: []int{7}},
	}
	if !sameUses(base, same) {
		t.Error("identical windows reported as changed — the board would re-sort on every tick")
	}

	// The series moving without a total moving is a day boundary passing. No
	// cell renders that differently, so it must not trigger a re-filter.
	shifted := map[string]usage.RepoUse{
		"/a": {Graphify: 3, Graft: 1, Series: []int{2, 1}},
		"/b": {Graft: 7, Series: []int{0, 7}},
	}
	if !sameUses(base, shifted) {
		t.Error("a reshuffled series counted as a change")
	}

	for name, next := range map[string]map[string]usage.RepoUse{
		"a count moved":   {"/a": {Graphify: 4, Graft: 1}, "/b": {Graft: 7}},
		"a tool split":    {"/a": {Graphify: 2, Graft: 2}, "/b": {Graft: 7}},
		"a repo appeared": {"/a": {Graphify: 3, Graft: 1}, "/b": {Graft: 7}, "/c": {Graft: 1}},
		"a repo vanished": {"/a": {Graphify: 3, Graft: 1}},
	} {
		if sameUses(base, next) {
			t.Errorf("%s: reported as unchanged, so the column would keep stale text", name)
		}
	}

	// The cold start this whole fix is about: the first publish is an empty
	// map before any scan has produced rows, and the real one must be seen as
	// a change or the Used column never rebinds.
	if sameUses(map[string]usage.RepoUse{}, base) {
		t.Error("the first real rollup after the empty one counted as unchanged")
	}
}
