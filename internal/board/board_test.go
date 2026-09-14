package board

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dns/ggraphify/internal/discover"
	"github.com/dns/ggraphify/internal/graphstate"
)

func mkrepo(t *testing.T, root, name, sha string) string {
	t.Helper()
	dir := filepath.Join(root, name)
	git := filepath.Join(dir, ".git", "refs", "heads")
	if err := os.MkdirAll(git, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0o644)
	os.WriteFile(filepath.Join(git, "main"), []byte(sha+"\n"), 0o644)
	return dir
}

func withGraph(t *testing.T, dir, builtCommit string) {
	t.Helper()
	out := filepath.Join(dir, "graphify-out")
	os.MkdirAll(out, 0o755)
	os.WriteFile(filepath.Join(out, "graph.json"),
		[]byte(`{"graph":0,"nodes":[{"id":"a"}],"links":[],"built_at_commit":"`+builtCommit+`"}`), 0o644)
	os.WriteFile(filepath.Join(out, ".graphify_labels.json"), []byte(`{"0":"A Real Name"}`), 0o644)
	os.WriteFile(filepath.Join(out, "manifest.json"), []byte(`{}`), 0o644)
}

func TestScanJoinsDiscoveryAndGraphState(t *testing.T) {
	root := t.TempDir()
	head := "1111111111111111111111111111111111111111"
	a := mkrepo(t, root, "with-graph", head)
	withGraph(t, a, head)
	mkrepo(t, root, "no-graph", "2222222222222222222222222222222222222222")

	rows, err := Scan(Options{Roots: []string{root}})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}
	byName := map[string]Row{}
	for _, r := range rows {
		byName[r.Name] = r
	}
	if byName["with-graph"].Graph.State != graphstate.StateFresh {
		t.Errorf("with-graph state = %v", byName["with-graph"].Graph.State)
	}
	if byName["no-graph"].Graph.State != graphstate.StateNone {
		t.Errorf("no-graph state = %v", byName["no-graph"].Graph.State)
	}
	if byName["with-graph"].Behind {
		t.Error("a graph built at HEAD is not behind")
	}
}

// Behind-HEAD is a different question from drift: a branch switch moves it
// without touching a single file's mtime, and the board shows both.
func TestBehindHeadIsIndependentOfDrift(t *testing.T) {
	root := t.TempDir()
	dir := mkrepo(t, root, "moved", "1111111111111111111111111111111111111111")
	withGraph(t, dir, "9999999999999999999999999999999999999999")

	rows, _ := Scan(Options{Roots: []string{root}})
	if len(rows) != 1 {
		t.Fatalf("got %d rows", len(rows))
	}
	if !rows[0].Behind {
		t.Fatal("a graph built at another commit must be marked behind")
	}
	if rows[0].Graph.Drift() {
		t.Fatal("no file moved, so there is no drift")
	}
}

// The override hook is how the per-repository settings reach the scan: an
// excluded repository is still a row, it is just kept out of fan-out actions.
func TestOverrideHook(t *testing.T) {
	root := t.TempDir()
	dir := mkrepo(t, root, "one", "1111111111111111111111111111111111111111")
	elsewhere := t.TempDir()
	os.MkdirAll(filepath.Join(elsewhere, "sub"), 0o755)

	rows, _ := Scan(Options{
		Roots: []string{root},
		Override: func(path string) (string, bool, bool) {
			if path == dir {
				return elsewhere, true, true
			}
			return "", false, false
		},
	})
	if len(rows) != 1 {
		t.Fatalf("got %d rows", len(rows))
	}
	if !rows[0].Excluded || !rows[0].Pinned {
		t.Errorf("flags did not reach the row: %+v", rows[0])
	}
	if rows[0].Graph.Out != elsewhere {
		t.Errorf("Out = %q, want the override %q", rows[0].Graph.Out, elsewhere)
	}
}

// Pinned rows float to the top, and the order is otherwise deterministic — a
// refresh that found nothing new must produce an identical slice, or the UI's
// "did anything move?" comparison is a lie.
func TestOrderIsDeterministicAndPinnedFirst(t *testing.T) {
	root := t.TempDir()
	for _, n := range []string{"zeta", "alpha", "mid"} {
		mkrepo(t, root, n, "1111111111111111111111111111111111111111")
	}
	pin := func(path string) (string, bool, bool) {
		return "", false, filepath.Base(path) == "zeta"
	}
	first, _ := Scan(Options{Roots: []string{root}, Override: pin})
	second, _ := Scan(Options{Roots: []string{root}, Override: pin})

	if len(first) != 3 {
		t.Fatalf("got %d rows", len(first))
	}
	if first[0].Name != "zeta" {
		t.Errorf("order = %s…, want the pinned row first", first[0].Name)
	}
	if first[1].Name != "alpha" || first[2].Name != "mid" {
		t.Errorf("unpinned rows are not alphabetical: %s, %s", first[1].Name, first[2].Name)
	}
	for i := range first {
		if first[i].Path != second[i].Path {
			t.Fatal("two scans of an unchanged tree produced different orders")
		}
	}
}

func TestSummarize(t *testing.T) {
	rows := []Row{
		{Graph: graphstate.Graph{State: graphstate.StateFresh}},
		{Graph: graphstate.Graph{State: graphstate.StateStale}, Behind: true},
		{Graph: graphstate.Graph{State: graphstate.StateRaw}},
		{Graph: graphstate.Graph{State: graphstate.StateBroken}},
		{Graph: graphstate.Graph{State: graphstate.StateNone}},
	}
	c := Summarize(rows)
	if c.Repos != 5 || c.Graphed != 3 || c.Fresh != 1 || c.Stale != 1 ||
		c.Raw != 1 || c.Broken != 1 || c.None != 1 || c.Behind != 1 {
		t.Fatalf("counts = %+v", c)
	}
}

func TestAge(t *testing.T) {
	now := time.Now()
	cases := []struct {
		t    time.Time
		want string
	}{
		{time.Time{}, ""},
		{now, "now"},
		{now.Add(-30 * time.Minute), "30m"},
		{now.Add(-5 * time.Hour), "5h"},
		{now.Add(-3 * 24 * time.Hour), "3d"},
		{now.Add(-800 * 24 * time.Hour), "2y"},
	}
	for _, c := range cases {
		if got := Age(c.t); got != c.want {
			t.Errorf("Age(%v) = %q, want %q", c.t, got, c.want)
		}
	}
}

func TestBytes(t *testing.T) {
	cases := map[int64]string{
		0:           "",
		512:         "512 B",
		2048:        "2.0 KB",
		1024 * 1024: "1.0 MB",
		11534336:    "11 MB",
	}
	for in, want := range cases {
		if got := Bytes(in); got != want {
			t.Errorf("Bytes(%d) = %q, want %q", in, got, want)
		}
	}
}

// A graph kept outside the checkout is still the row's graph: the whole point
// of a central directory is that the board reads it as if it were in-tree.
func TestScanReadsGraphFromCentralDirectory(t *testing.T) {
	t.Setenv("GRAPHIFY_OUT", "")
	t.Setenv("GRAPHIFY_OUT_NAME", "")
	root, base := t.TempDir(), t.TempDir()
	head := "1111111111111111111111111111111111111111"
	repo := mkrepo(t, root, "central", head)

	out := filepath.Join(base, discover.Slug(repo))
	os.MkdirAll(out, 0o755)
	os.WriteFile(filepath.Join(out, "graph.json"),
		[]byte(`{"graph":0,"nodes":[{"id":"a"}],"links":[],"built_at_commit":"`+head+`"}`), 0o644)
	os.WriteFile(filepath.Join(out, ".graphify_labels.json"), []byte(`{"0":"A Real Name"}`), 0o644)
	os.WriteFile(filepath.Join(out, "manifest.json"), []byte(`{}`), 0o644)

	// Without the setting there is no graph to find; with it, the same
	// checkout is fresh.
	plain, err := Scan(Options{Roots: []string{root}})
	if err != nil {
		t.Fatal(err)
	}
	if plain[0].Graph.State != graphstate.StateNone {
		t.Errorf("in-tree scan state = %v, want none", plain[0].Graph.State)
	}

	rows, err := Scan(Options{Roots: []string{root}, OutBase: base})
	if err != nil {
		t.Fatal(err)
	}
	if rows[0].Graph.Out != out {
		t.Errorf("Out = %q, want %q", rows[0].Graph.Out, out)
	}
	if rows[0].Graph.State != graphstate.StateFresh {
		t.Errorf("state = %v, want fresh", rows[0].Graph.State)
	}
}

// A per-repository override outranks the board-wide location and is resolved
// against the checkout when it is relative, the same way a job resolves it.
func TestScanOverrideOutIsResolved(t *testing.T) {
	root := t.TempDir()
	head := "1111111111111111111111111111111111111111"
	repo := mkrepo(t, root, "one", head)

	rows, err := Scan(Options{
		Roots:    []string{root},
		OutBase:  t.TempDir(),
		Override: func(string) (string, bool, bool) { return "sub/graph", false, false },
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(repo, "sub", "graph"); rows[0].Graph.Out != want {
		t.Errorf("Out = %q, want %q", rows[0].Graph.Out, want)
	}
}

// NeverExtracted is what the board-wide local sweep selects on, so the states
// it excludes matter as much as the ones it includes: a false positive is
// hours of CPU rebuilding a graph that was already there.
func TestNeverExtracted(t *testing.T) {
	cases := []struct {
		state graphstate.State
		want  bool
		why   string
	}{
		{graphstate.StateNone, true, "no output directory — graphify never ran here"},
		{graphstate.StateRaw, true, "a graph, but the LLM half never named its communities"},
		{graphstate.StateFresh, false, "a full run already completed"},
		{graphstate.StateStale, false, "a full run completed; drift is repaired free by update"},
		{graphstate.StateBroken, false, "an unreadable graph is a repair, not a missing run"},
		{graphstate.StateRunning, false, "a job is in flight; the sweep must not queue a second"},
	}
	for _, c := range cases {
		r := Row{Graph: graphstate.Graph{State: c.state}}
		if got := r.NeverExtracted(); got != c.want {
			t.Errorf("NeverExtracted(%v) = %v, want %v — %s", c.state, got, c.want, c.why)
		}
	}
}
