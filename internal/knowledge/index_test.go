package knowledge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dns/ggraphify/internal/board"
	"github.com/dns/ggraphify/internal/discover"
	"github.com/dns/ggraphify/internal/graphstate"
)

// graphAt writes the two files a consumer resolves through, so the entry under
// test points at something real.
func graphAt(t *testing.T, base, repo string) string {
	t.Helper()
	out := filepath.Join(base, discover.Slug(repo))
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(out, "graph.json"), []byte(`{"nodes":[],"links":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	return out
}

func row(path, out, built, head string) board.Row {
	g := graphstate.Graph{
		Out: out, State: graphstate.StateFresh, Nodes: 10, Links: 20,
		Communities: 3, Labeled: true, BuiltCommit: built, HeadCommit: head,
		BuiltAt: time.Now().Truncate(time.Second),
	}
	return board.Row{
		Repo:   discover.Repo{Path: path, Name: filepath.Base(path), HeadSHA: head, Out: out},
		Graph:  g,
		Behind: g.Behind(),
	}
}

// The property the whole file exists for: a consumer resolves by the
// checkout's ABSOLUTE path, with no globbing and no assumption that basenames
// are unique. Two repositories called "api" must not collide.
func TestSyncKeysByAbsolutePathSoBasenamesMayCollide(t *testing.T) {
	base := t.TempDir()
	home := t.TempDir()
	a, b := filepath.Join(home, "git", "api"), filepath.Join(home, "tmp", "api")
	rows := []board.Row{
		row(a, graphAt(t, base, a), "aaaaaaa", "aaaaaaa"),
		row(b, graphAt(t, base, b), "bbbbbbb", "bbbbbbb"),
	}
	if _, wrote, err := Sync(base, rows); err != nil || !wrote {
		t.Fatalf("Sync = %v, %v", wrote, err)
	}
	ix, err := Load(Path(base))
	if err != nil {
		t.Fatal(err)
	}
	if len(ix.Entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(ix.Entries))
	}
	if ix.Entries[a].Graph == ix.Entries[b].Graph {
		t.Fatalf("both checkouts named %q resolved to one graph: %s",
			filepath.Base(a), ix.Entries[a].Graph)
	}
	for _, repo := range []string{a, b} {
		want := filepath.Join(ix.Entries[repo].Out, "graph.json")
		if ix.Entries[repo].Graph != want {
			t.Errorf("%s → %q, want %q", repo, ix.Entries[repo].Graph, want)
		}
	}
}

// The index is republished on every board refresh — every thirty seconds, for
// the life of the process. A write per tick would be a file rewritten three
// thousand times a day to say the same thing, and would defeat the "generated"
// timestamp a consumer reads to tell a live index from an abandoned one.
func TestSyncWritesOnlyWhenSomethingMoved(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(t.TempDir(), "acquiring")
	out := graphAt(t, base, repo)
	rows := []board.Row{row(repo, out, "1111111", "1111111")}

	if _, wrote, _ := Sync(base, rows); !wrote {
		t.Fatal("the first Sync wrote nothing")
	}
	if _, wrote, _ := Sync(base, rows); wrote {
		t.Fatal("an unchanged board rewrote the index")
	}

	// A commit lands: the graph is now behind HEAD, and that is exactly the
	// kind of change a consumer is reading this file to find out about.
	rows[0] = row(repo, out, "1111111", "2222222")
	if _, wrote, _ := Sync(base, rows); !wrote {
		t.Fatal("a graph that fell behind HEAD did not rewrite the index")
	}
	ix, _ := Load(Path(base))
	if !ix.Entries[repo].Stale {
		t.Error("the entry does not report the graph as behind HEAD")
	}
}

// A scan narrowed to one root must not delete the rest of the machine's index
// — but an entry whose graph is gone from disk points nowhere and has to go.
func TestSyncKeepsUnseenReposAndDropsVanishedGraphs(t *testing.T) {
	base := t.TempDir()
	kept := filepath.Join(t.TempDir(), "kept")
	gone := filepath.Join(t.TempDir(), "gone")
	keptOut, goneOut := graphAt(t, base, kept), graphAt(t, base, gone)

	if _, _, err := Sync(base, []board.Row{
		row(kept, keptOut, "1111111", "1111111"),
		row(gone, goneOut, "2222222", "2222222"),
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(goneOut); err != nil {
		t.Fatal(err)
	}
	// A scan that saw neither repository: one graph is still there, one is not.
	if _, _, err := Sync(base, []board.Row{row(kept, keptOut, "1111111", "1111111")}); err != nil {
		t.Fatal(err)
	}
	ix, _ := Load(Path(base))
	if _, ok := ix.Entries[kept]; !ok {
		t.Error("a repository outside this scan was dropped from the index")
	}
	if _, ok := ix.Entries[gone]; ok {
		t.Error("an entry whose graph is gone from disk was kept")
	}
}

// The tier is one manifest parse, so it is carried forward while the graph has
// not been rebuilt. If it were recomputed every tick the refresh would parse
// two hundred manifests a minute.
func TestFromRowCarriesTheTierForwardUntilTheGraphMoves(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(t.TempDir(), "carried")
	out := graphAt(t, base, repo)
	r := row(repo, out, "1111111", "1111111")

	prev := Entry{Out: out, BuiltAt: r.Graph.BuiltAt, Tier: "semantic", Files: 9, Semantic: 9}
	e, ok := FromRow(r, prev, true)
	if !ok {
		t.Fatal("FromRow refused a row with a graph")
	}
	if e.Tier != "semantic" || e.Semantic != 9 {
		t.Fatalf("tier = %q %d/%d, want the previous one carried forward", e.Tier, e.Semantic, e.Files)
	}

	// A rebuild moves graph.json's mtime, and the carried-forward answer stops
	// being evidence of anything.
	r.Graph.BuiltAt = r.Graph.BuiltAt.Add(time.Hour)
	e, _ = FromRow(r, prev, true)
	if e.Tier == "semantic" {
		t.Error("a rebuilt graph kept the old tier")
	}
}

// A row with no graph has nothing to publish, and an entry for it would point
// a consumer at a file that is not there.
func TestFromRowRefusesAnUngraphedRow(t *testing.T) {
	r := board.Row{Repo: discover.Repo{Path: "/x/y"}, Graph: graphstate.Graph{State: graphstate.StateNone}}
	if _, ok := FromRow(r, Entry{}, false); ok {
		t.Error("FromRow published an entry for a repository with no graph")
	}
}

// A time that has been through JSON comes back as the same instant with a
// different location pointer. Comparing with == would rewrite the file on
// every tick forever, which is the bug Same exists to prevent.
func TestSameIgnoresTheTimeRoundTrip(t *testing.T) {
	e := Entry{Out: "/o", Graph: "/o/graph.json", BuiltAt: time.Now()}
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	var back Entry
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if !e.Same(back) {
		t.Errorf("an entry stopped equalling itself across JSON:\n%+v\n%+v", e, back)
	}
	back.Nodes++
	if e.Same(back) {
		t.Error("Same ignored a field that actually changed")
	}
}

// An in-tree board has no index to write, and must not invent a place for one.
func TestNoOutBaseMeansNoIndex(t *testing.T) {
	if Path("") != "" {
		t.Errorf("Path(\"\") = %q", Path(""))
	}
	if n, wrote, err := Sync("", []board.Row{row("/a", "/a/graphify-out", "1", "1")}); err != nil || wrote || n != 0 {
		t.Errorf("Sync with no base = %d, %v, %v", n, wrote, err)
	}
}

// Put is the single-repository path a finished headless job takes, and it has
// to see what the board wrote and vice versa.
func TestPutAndLookupAgreeWithSync(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(t.TempDir(), "one")
	out := graphAt(t, base, repo)
	e, _ := FromRow(row(repo, out, "1111111", "1111111"), Entry{}, false)
	if err := Put(base, repo, e); err != nil {
		t.Fatal(err)
	}
	got, ok := Lookup(base, repo)
	if !ok {
		t.Fatal("Lookup found nothing after Put")
	}
	if got.Graph != filepath.Join(out, "graph.json") {
		t.Errorf("Lookup graph = %q", got.Graph)
	}
}
