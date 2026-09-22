package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/dns/ggraphify/internal/board"
	"github.com/dns/ggraphify/internal/discover"
	"github.com/dns/ggraphify/internal/globalgraph"
)

func memberRow(name, path string, m globalgraph.Member) board.Row {
	return board.Row{
		Repo:   discover.Repo{Name: name, Path: path},
		Global: m,
	}
}

// The three membership verdicts must be three shapes, for the same reason the
// state dot is: the board has to be readable without colour vision.
func TestGlobalCellIsThreeDistinctShapes(t *testing.T) {
	cases := []struct {
		name  string
		m     globalgraph.Member
		class string
	}{
		{"out", globalgraph.Member{}, "st-none"},
		{"in", globalgraph.Member{In: true, Tag: "gg"}, "st-fresh"},
		{"stale", globalgraph.Member{In: true, Tag: "gg", Stale: true}, "st-stale"},
	}
	seen := map[string]string{}
	for _, c := range cases {
		text, class := globalCell(c.m, "gg")
		glyph := strings.Fields(text)[0]
		if other, dup := seen[glyph]; dup {
			t.Errorf("%s and %s share the glyph %q", c.name, other, glyph)
		}
		seen[glyph] = c.name
		if class != c.class {
			t.Errorf("%s: class = %q, want %q", c.name, class, c.class)
		}
		if !strings.Contains(boardCSS, "."+class) {
			t.Errorf("%s: class %q is not defined in the stylesheet", c.name, class)
		}
	}
}

// The tag is the only handle `graphify global remove` accepts, so a row added
// under a name that is not its directory's has to show it — and one added
// under its own name must not waste the column repeating it.
func TestGlobalCellShowsOnlyASurprisingTag(t *testing.T) {
	text, _ := globalCell(globalgraph.Member{In: true, Tag: "nexus"}, "nexus")
	if strings.Contains(text, "nexus") {
		t.Errorf("cell = %q, want the tag omitted when it matches the repo name", text)
	}
	text, _ = globalCell(globalgraph.Member{In: true, Tag: "nexus-old"}, "nexus")
	if !strings.Contains(text, "nexus-old") {
		t.Errorf("cell = %q, want the tag named when it differs", text)
	}
}

func TestGlobalWeightOrdersByAttention(t *testing.T) {
	stale := memberRow("a", "/a", globalgraph.Member{In: true, Stale: true})
	in := memberRow("b", "/b", globalgraph.Member{In: true})
	out := memberRow("c", "/c", globalgraph.Member{})
	if !(globalWeight(&stale) < globalWeight(&in) && globalWeight(&in) < globalWeight(&out)) {
		t.Errorf("weights = stale %d, in %d, out %d; want ascending",
			globalWeight(&stale), globalWeight(&in), globalWeight(&out))
	}
}

// The manifest's tag wins over the directory name. Guessing a tag from the
// path is how a remove drops the wrong repository's nodes.
func TestGlobalTagPrefersTheManifest(t *testing.T) {
	r := memberRow("nexus", "/home/dns/git/nexus", globalgraph.Member{In: true, Tag: "nexus-2024"})
	if got := globalTag(r); got != "nexus-2024" {
		t.Errorf("globalTag = %q, want the manifest's tag", got)
	}
	r = memberRow("nexus", "/home/dns/git/nexus", globalgraph.Member{})
	if got := globalTag(r); got != "nexus" {
		t.Errorf("globalTag = %q, want the directory name", got)
	}
}

// The tooltip is where the stale state is actually explained; the glyph alone
// cannot say "this is carrying the previous extraction".
func TestGlobalTooltipExplainsEachVerdict(t *testing.T) {
	out := memberRow("gg", "/gg", globalgraph.Member{})
	out.Graph.Nodes = 10
	if tip := globalTooltip(out); !strings.Contains(tip, "never been run") {
		t.Errorf("out-of-graph tooltip = %q", tip)
	}

	none := memberRow("gg", "/gg", globalgraph.Member{})
	if tip := globalTooltip(none); !strings.Contains(tip, "no graph here") {
		t.Errorf("ungraphed tooltip = %q, want it to name the missing graph", tip)
	}

	stale := memberRow("gg", "/gg", globalgraph.Member{
		In: true, Tag: "gg", Nodes: 10, Edges: 20,
		AddedAt: time.Now().Add(-time.Hour), Stale: true,
	})
	tip := globalTooltip(stale)
	for _, want := range []string{"tag gg", "10 nodes", "rebuilt since"} {
		if !strings.Contains(tip, want) {
			t.Errorf("stale tooltip = %q, want it to mention %q", tip, want)
		}
	}
}

func TestGlobalFactIsEmptyForANonMember(t *testing.T) {
	if got := globalFact(globalgraph.Member{}); got != "" {
		t.Errorf("globalFact for a non-member = %q, want empty", got)
	}
	got := globalFact(globalgraph.Member{In: true, Tag: "gg", Nodes: 7})
	for _, want := range []string{"tag gg", "7 nodes"} {
		if !strings.Contains(got, want) {
			t.Errorf("globalFact = %q, want it to mention %q", got, want)
		}
	}
}

// The detail pane's membership row is one control for two commands, so its
// wording has to change with the row — a button reading "Add" on a repository
// that is already in the global graph is the one that silently replaces it.
func TestGlobalRowSubFollowsMembership(t *testing.T) {
	cases := []struct {
		name string
		m    globalgraph.Member
		want string
	}{
		{"out", globalgraph.Member{}, "Merge this graph"},
		{"in", globalgraph.Member{In: true, Tag: "gg"}, "Removing drops"},
		{"stale", globalgraph.Member{In: true, Tag: "gg", Stale: true}, "re-extracted since"},
	}
	for _, c := range cases {
		got := globalRowSub(board.Row{Global: c.m})
		if !strings.Contains(got, c.want) {
			t.Errorf("%s: subtitle = %q, want it to mention %q", c.name, got, c.want)
		}
	}
}

// A confirm that names forty repositories is a confirm nobody reads.
func TestNamesUpTo(t *testing.T) {
	got := namesUpTo([]string{"a", "b", "c"}, 8)
	if strings.Join(got, ",") != "a,b,c" {
		t.Errorf("short list was rewritten: %v", got)
	}
	got = namesUpTo([]string{"a", "b", "c", "d"}, 2)
	if len(got) != 3 || got[2] != "and 2 more" {
		t.Errorf("namesUpTo = %v, want the first two and a count", got)
	}
}

// Every class the Global screen paints with has to exist, or a chip that
// should be amber is silently plain.
func TestGlobalPaneClassesAreDefined(t *testing.T) {
	for _, c := range []string{staleTileClass(3), "st-none", "st-fresh", "st-stale", "dim", "joblog"} {
		if c == "" {
			continue
		}
		if !strings.Contains(boardCSS, "."+c) {
			t.Errorf("class %q is not defined in the stylesheet", c)
		}
	}
	if staleTileClass(0) != "" {
		t.Error("a nought stale count is painted as a warning")
	}
}

// The board's Global column and the screen behind it are one feature: the
// column must exist under the id the sidecar and the settings switch use.
func TestGlobalColumnIsRegistered(t *testing.T) {
	for _, c := range columns {
		if c.ID == "global" {
			if c.cmp == nil {
				t.Error("the Global column is not sortable")
			}
			return
		}
	}
	t.Error("no column with the id \"global\"")
}

// boardRow is a row as the Global screen sees one: a checkout under a root,
// with a graph somewhere the output location decides.
func boardRow(root, sub, name string, nodes int) board.Row {
	path := root + "/" + name
	if sub != "" {
		path = root + "/" + sub + "/" + name
	}
	r := board.Row{Repo: discover.Repo{Name: name, Path: path, Root: root, Group: sub}}
	r.Graph.Nodes = nodes
	r.Graph.Out = path + "/graphify-out"
	return r
}

// The folder dropdown narrows both lists. A repository outside the selected
// folder is not a choice on that screen, and the heading says so rather than
// quietly reporting a smaller total.
func TestGlobalPaneFolderFilterNarrowsRows(t *testing.T) {
	p := &globalPane{checkedTags: map[string]bool{}, checkedPaths: map[string]bool{}}
	nova := boardRow("/home/dns/git", "nova", "charge", 10)
	other := boardRow("/home/dns/tmp", "", "scratch", 10)

	if !p.showRow(nova) || !p.showRow(other) {
		t.Fatal("no filter should list everything")
	}
	p.folder = "/home/dns/git/nova"
	if !p.showRow(nova) {
		t.Error("a repository inside the selected folder was filtered out")
	}
	if p.showRow(other) {
		t.Error("a repository outside the selected folder was listed")
	}

	// The text box still applies on top of it.
	p.query = "nothing-like-this"
	if p.showRow(nova) {
		t.Error("the text filter stopped applying once a folder was chosen")
	}
}

// A member whose repository is not on the board belongs to no folder — it
// cannot be placed under one — so it is hidden while a folder is selected and
// listed under "All folders". Those are the entries most worth removing, so
// hiding them silently would be the wrong half to lose.
func TestGlobalPaneFolderFilterAndOffBoardMembers(t *testing.T) {
	p := &globalPane{checkedTags: map[string]bool{}, checkedPaths: map[string]bool{}}
	row := boardRow("/home/dns/git", "nova", "charge", 10)
	boarded := globalMember{entry: globalgraph.Entry{Tag: "charge"}, row: &row}
	orphan := globalMember{entry: globalgraph.Entry{Tag: "long-gone", Source: "/nowhere/graph.json"}}

	if !p.showMember(boarded) || !p.showMember(orphan) {
		t.Fatal("with no folder selected, every member is listed")
	}
	p.folder = "/home/dns/git/nova"
	if !p.showMember(boarded) {
		t.Error("a member inside the selected folder was filtered out")
	}
	if p.showMember(orphan) {
		t.Error("a member belonging to no checkout was claimed by a folder")
	}
}

// A member is findable by its tag and by the graph it was built from, not
// only by the repository behind it — an entry with no repository has nothing
// else to match on.
func TestGlobalPaneTextFilterMatchesTagAndSource(t *testing.T) {
	p := &globalPane{checkedTags: map[string]bool{}, checkedPaths: map[string]bool{}}
	m := globalMember{entry: globalgraph.Entry{Tag: "nexus-2024", Source: "/home/dns/knowledge/nexus-ab12/graph.json"}}

	for _, q := range []string{"nexus", "2024", "knowledge", "ab12"} {
		p.query = q
		if !p.showMember(m) {
			t.Errorf("query %q did not match tag or source", q)
		}
	}
	p.query = "unrelated"
	if p.showMember(m) {
		t.Error("an unrelated query matched")
	}
}

// The ticked set survives a filter change — assembling a batch across two
// folders is a thing people do — so the buttons have to say how many are in
// it, or a narrowed screen would act on repositories nobody can see.
func TestGlobalPaneTicksSurviveTheFilters(t *testing.T) {
	p := &globalPane{
		checkedTags:  map[string]bool{"charge": true},
		checkedPaths: map[string]bool{"/home/dns/tmp/scratch": true},
	}
	row := boardRow("/home/dns/git", "nova", "charge", 10)
	members := []globalMember{{entry: globalgraph.Entry{Tag: "charge"}, row: &row}}
	candidates := []board.Row{boardRow("/home/dns/tmp", "", "scratch", 10)}

	p.folder = "/home/dns/git/nova" // hides the ticked candidate
	if got := len(p.ticked(members)); got != 1 {
		t.Errorf("ticked members = %d, want 1", got)
	}
	if got := len(p.tickedRows(candidates)); got != 1 {
		t.Errorf("ticked candidates = %d, want 1 — a filter must not untick", got)
	}
}

func TestCountOfNamesTheFilteredTotal(t *testing.T) {
	if got := countOf(12, 12); got != "12" {
		t.Errorf("countOf(12,12) = %q, want a bare total", got)
	}
	if got := countOf(5, 12); got != "5 of 12" {
		t.Errorf("countOf(5,12) = %q", got)
	}
}
