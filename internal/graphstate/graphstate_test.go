package graphstate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fixture builds a synthetic repository with a graphify-out in it.
type fixture struct {
	t    *testing.T
	repo string
	out  string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	repo := t.TempDir()
	out := filepath.Join(repo, "graphify-out")
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	return &fixture{t: t, repo: repo, out: out}
}

func (f *fixture) write(rel, content string) string {
	f.t.Helper()
	p := filepath.Join(f.repo, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
	return p
}

func (f *fixture) writeOut(name, content string) {
	f.t.Helper()
	if err := os.WriteFile(filepath.Join(f.out, name), []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

// graphJSON writes a graph.json with the shape graphify 0.9.58 actually
// produces — note that "graph" is a bare 0 there, not an object, which a
// struct decode would choke on.
func (f *fixture) graphJSON(nodes, links int, commit string) {
	f.t.Helper()
	var b strings.Builder
	b.WriteString(`{"directed": false, "multigraph": false, "graph": 0, "nodes": [`)
	for i := 0; i < nodes; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`{"id":"n`)
		b.WriteString(itoa(i))
		b.WriteString(`","label":"N","community":1,"source_file":"a.go"}`)
	}
	b.WriteString(`], "links": [`)
	for i := 0; i < links; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`{"source":"n0","target":"n0","relation":"CALLS"}`)
	}
	b.WriteString(`], "hyperedges": [], "built_at_commit": "` + commit + `"}`)
	f.writeOut("graph.json", b.String())
}

func (f *fixture) manifest(entries map[string]float64) {
	f.t.Helper()
	m := map[string]manifestEntry{}
	for rel, mt := range entries {
		m[rel] = manifestEntry{MTime: mt, ASTHash: "h"}
	}
	b, err := json.Marshal(m)
	if err != nil {
		f.t.Fatal(err)
	}
	f.writeOut("manifest.json", string(b))
}

func TestNoOutputDirIsStateNone(t *testing.T) {
	repo := t.TempDir()
	g, err := Read(Options{Repo: repo})
	if err != nil {
		t.Fatal(err)
	}
	// "No graph here" is an ordinary, very common answer, not an error.
	if g.State != StateNone {
		t.Fatalf("State = %v, want none", g.State)
	}
}

func TestMissingGraphJSONIsBroken(t *testing.T) {
	f := newFixture(t)
	g, _ := Read(Options{Repo: f.repo})
	if g.State != StateBroken {
		t.Fatalf("State = %v, want broken", g.State)
	}
	if g.Err == "" {
		t.Error("a broken state must say why")
	}
}

func TestCorruptGraphJSONIsBroken(t *testing.T) {
	f := newFixture(t)
	f.writeOut("graph.json", "{not json at all")
	g, _ := Read(Options{Repo: f.repo})
	if g.State != StateBroken {
		t.Fatalf("State = %v, want broken", g.State)
	}
}

// The counters must come out of a graph.json shaped like the real one —
// including the bare `"graph": 0` that a struct decode would fail on.
func TestCountersFromRealShape(t *testing.T) {
	f := newFixture(t)
	f.graphJSON(7, 11, "deadbeef")
	g, _ := Read(Options{Repo: f.repo, SkipDrift: true})
	if g.Nodes != 7 || g.Links != 11 {
		t.Fatalf("nodes/links = %d/%d, want 7/11", g.Nodes, g.Links)
	}
	if g.BuiltCommit != "deadbeef" {
		t.Fatalf("BuiltCommit = %q", g.BuiltCommit)
	}
	if g.Err != "" {
		t.Fatalf("unexpected error: %s", g.Err)
	}
}

// Placeholder community names mean the expensive labelling pass has not run.
// A row that claimed to be "fresh" on the strength of `community_7` would be
// lying about exactly the step that costs money.
func TestPlaceholderLabelsAreNotLabeled(t *testing.T) {
	f := newFixture(t)
	f.graphJSON(1, 0, "")
	f.writeOut(".graphify_labels.json", `{"0":"community_0","1":"Community 1","2":"cluster-3"}`)
	g, _ := Read(Options{Repo: f.repo, SkipDrift: true})
	if g.Communities != 3 {
		t.Fatalf("Communities = %d, want 3", g.Communities)
	}
	if g.Labeled {
		t.Fatal("placeholder names must not count as labelled")
	}
	if g.State != StateRaw {
		t.Fatalf("State = %v, want raw", g.State)
	}
}

func TestRealLabelsAreLabeled(t *testing.T) {
	f := newFixture(t)
	f.graphJSON(1, 0, "")
	// The real file on this machine maps community index to a name like a
	// file or a symbol, which must not be mistaken for a placeholder.
	f.writeOut(".graphify_labels.json", `{"0":"colspec_test.go","1":"Settings","2":"community detection"}`)
	g, _ := Read(Options{Repo: f.repo, SkipDrift: true})
	if !g.Labeled {
		t.Fatal("real names must count as labelled")
	}
	if g.State != StateFresh {
		t.Fatalf("State = %v, want fresh", g.State)
	}
}

func TestDriftCounts(t *testing.T) {
	f := newFixture(t)
	f.graphJSON(1, 0, "")
	f.writeOut(".graphify_labels.json", `{"0":"Real Name"}`)

	old := float64(time.Now().Add(-time.Hour).Unix())
	f.write("kept.go", "package a")
	f.write("changed.go", "package a")
	f.write("added.go", "package a")
	// "gone.go" is in the manifest but never written.
	f.manifest(map[string]float64{
		"kept.go":    float64(time.Now().Add(time.Hour).Unix()), // manifest newer than the file
		"changed.go": old,                                       // file newer than the manifest
		"gone.go":    old,
	})

	g, _ := Read(Options{Repo: f.repo})
	if g.DriftAdded != 1 {
		t.Errorf("DriftAdded = %d, want 1 (added.go)", g.DriftAdded)
	}
	if g.DriftChanged != 1 {
		t.Errorf("DriftChanged = %d, want 1 (changed.go)", g.DriftChanged)
	}
	if g.DriftRemoved != 1 {
		t.Errorf("DriftRemoved = %d, want 1 (gone.go)", g.DriftRemoved)
	}
	if g.State != StateStale {
		t.Errorf("State = %v, want stale", g.State)
	}
	if s := g.DriftString(); s != "+1 ~1 −1" {
		t.Errorf("DriftString = %q", s)
	}
}

// Only file types the manifest already contains are considered. Counting a
// README or a PNG as an added file would show permanent phantom drift on every
// repository graphify has ever touched.
func TestDriftIgnoresFileTypesTheManifestNeverHad(t *testing.T) {
	f := newFixture(t)
	f.graphJSON(1, 0, "")
	f.writeOut(".graphify_labels.json", `{"0":"Real"}`)
	f.write("a.go", "package a")
	f.write("logo.png", "\x89PNG")
	f.write("notes.txt", "hello")
	f.manifest(map[string]float64{"a.go": float64(time.Now().Add(time.Hour).Unix())})

	g, _ := Read(Options{Repo: f.repo})
	if g.DriftAdded != 0 {
		t.Fatalf("DriftAdded = %d, want 0 — png/txt are not graphify's file types here", g.DriftAdded)
	}
	if g.State != StateFresh {
		t.Fatalf("State = %v, want fresh", g.State)
	}
}

// node_modules and friends are never descended into. Without this, one
// npm install would report tens of thousands of added files.
func TestDriftSkipsVendorTrees(t *testing.T) {
	f := newFixture(t)
	f.graphJSON(1, 0, "")
	f.writeOut(".graphify_labels.json", `{"0":"Real"}`)
	f.write("a.go", "package a")
	f.write("node_modules/pkg/index.go", "package pkg")
	f.write(".venv/lib/thing.go", "package thing")
	f.manifest(map[string]float64{"a.go": float64(time.Now().Add(time.Hour).Unix())})

	g, _ := Read(Options{Repo: f.repo})
	if g.DriftAdded != 0 {
		t.Fatalf("DriftAdded = %d, want 0 — vendor trees must be skipped", g.DriftAdded)
	}
}

// The needs_update flag outranks a clean manifest: it means a semantic
// re-extraction is pending, which is the expensive command rather than the
// free one, and a "fresh" row would hide that.
func TestNeedsUpdateWinsOverACleanManifest(t *testing.T) {
	f := newFixture(t)
	f.graphJSON(1, 0, "")
	f.writeOut(".graphify_labels.json", `{"0":"Real"}`)
	f.write("a.go", "package a")
	f.manifest(map[string]float64{"a.go": float64(time.Now().Add(time.Hour).Unix())})
	f.writeOut("needs_update", "")

	g, _ := Read(Options{Repo: f.repo})
	if !g.NeedsUpdate {
		t.Fatal("needs_update was not read")
	}
	if g.State != StateStale {
		t.Fatalf("State = %v, want stale", g.State)
	}
	if !strings.Contains(g.DriftString(), "needs-extract") {
		t.Errorf("DriftString = %q, want it to name the pending re-extraction", g.DriftString())
	}
}

// A graph.json above the ceiling is not opened at all. A monorepo's must not
// be able to stall a refresh tick even through a streaming decoder.
func TestOversizeGraphIsNotParsed(t *testing.T) {
	f := newFixture(t)
	f.graphJSON(3, 3, "abc")
	p := filepath.Join(f.out, "graph.json")
	if err := os.Truncate(p, MaxGraphBytes+1); err != nil {
		t.Skipf("cannot create a sparse oversize file here: %v", err)
	}
	g, _ := Read(Options{Repo: f.repo, SkipDrift: true})
	if g.Nodes != 0 {
		t.Errorf("Nodes = %d, want 0 — the ceiling must prevent the parse", g.Nodes)
	}
	if g.GraphBytes <= MaxGraphBytes {
		t.Errorf("GraphBytes = %d, want it reported even when unparsed", g.GraphBytes)
	}
	if g.Err == "" {
		t.Error("the row must say why the counters are missing")
	}
}

func TestStateRoundTrip(t *testing.T) {
	for _, s := range []State{StateNone, StateRaw, StateStale, StateFresh, StateBroken, StateRunning} {
		got, ok := ParseState(s.String())
		if !ok || got != s {
			t.Errorf("ParseState(%q) = %v, %v", s.String(), got, ok)
		}
	}
	if _, ok := ParseState("nonsense"); ok {
		t.Error("ParseState accepted nonsense")
	}
	// The JSON form is the name, not the ordinal, so reordering the constants
	// cannot silently change ggraphify-scan's output.
	b, _ := json.Marshal(StateStale)
	if string(b) != `"stale"` {
		t.Errorf("MarshalJSON = %s", b)
	}
}

// Reading a 2.4 MB graph.json — the size of the real reference graph — must
// stay cheap enough for a refresh tick over a hundred repositories.
func BenchmarkReadBigGraph(b *testing.B) {
	repo := b.TempDir()
	out := filepath.Join(repo, "graphify-out")
	os.MkdirAll(out, 0o755)

	var sb strings.Builder
	sb.WriteString(`{"directed": false, "graph": 0, "nodes": [`)
	for i := 0; i < 2047; i++ {
		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString(`{"id":"node_` + itoa(i) + `","label":"Label","file_type":"go",` +
			`"source_file":"internal/pkg/file.go","source_location":"1:1","community":3,` +
			`"community_name":"a community","norm_label":"label"}`)
	}
	sb.WriteString(`], "links": [`)
	for i := 0; i < 4835; i++ {
		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString(`{"source":"node_1","target":"node_2","relation":"CALLS","context":"x"}`)
	}
	sb.WriteString(`], "hyperedges": [], "built_at_commit": "abc"}`)
	os.WriteFile(filepath.Join(out, "graph.json"), []byte(sb.String()), 0o644)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		g, _ := Read(Options{Repo: repo, SkipDrift: true})
		if g.Nodes != 2047 {
			b.Fatalf("Nodes = %d", g.Nodes)
		}
	}
}

// HasGraph is asked on the click path, before a job is queued, so it has to
// agree with what the commands themselves do: a missing directory, a missing
// graph.json and the empty graph.json a killed build leaves behind are all
// "no graph", and only a real one is not.
func TestHasGraph(t *testing.T) {
	dir := t.TempDir()
	if HasGraph("") {
		t.Error("an unresolved output directory reported a graph")
	}
	if HasGraph(filepath.Join(dir, "never-made")) {
		t.Error("a missing directory reported a graph")
	}
	out := filepath.Join(dir, "graphify-out")
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	if HasGraph(out) {
		t.Error("an empty output directory reported a graph")
	}
	gj := filepath.Join(out, "graph.json")
	if err := os.WriteFile(gj, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if HasGraph(out) {
		t.Error("a zero-byte graph.json reported a graph; every reader fails on it")
	}
	if err := os.WriteFile(gj, []byte(`{"nodes":[],"links":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if !HasGraph(out) {
		t.Error("a real graph.json was not recognised")
	}
	if err := os.Remove(gj); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(gj, 0o755); err != nil {
		t.Fatal(err)
	}
	if HasGraph(out) {
		t.Error("a directory named graph.json reported a graph")
	}
}

// graphify's extraction is .gitignore-aware — that is what its --no-gitignore
// flag turns off — so a drift walk that is not reports every ignored file as
// one graphify failed to extract. On the reference checkout that was a
// gitignored graft/ cache and a leftover in-tree graphify-out/: 1 568 phantom
// added files, and a row that could never reach "fresh" no matter what ran.
func TestDriftHonoursGitignore(t *testing.T) {
	f := newFixture(t)
	f.graphJSON(1, 0, "")
	f.writeOut(".graphify_labels.json", `{"0":"Real"}`)
	f.write(".gitignore", "/graft/\n*.gen.go\n!keep.gen.go\n")
	f.write("a.go", "package a")
	f.write("graft/cards/thing.go", "package thing")
	f.write("zz.gen.go", "package a")
	f.write("keep.gen.go", "package a")
	f.manifest(map[string]float64{
		"a.go":        float64(time.Now().Add(time.Hour).Unix()),
		"keep.gen.go": float64(time.Now().Add(time.Hour).Unix()),
	})

	g, _ := Read(Options{Repo: f.repo})
	if g.DriftAdded != 0 {
		t.Fatalf("DriftAdded = %d, want 0 — gitignored paths are not drift", g.DriftAdded)
	}
	if g.State != StateFresh {
		t.Fatalf("State = %v, want fresh", g.State)
	}
}

// A .claude/worktrees/agent-*/ left behind by an agent session is a second
// copy of the whole repository. graphify does not descend into a nested
// checkout and neither may the drift walk: the reference repository carried
// 3 189 files in two such worktrees, every one of them counted as added.
func TestDriftSkipsNestedCheckouts(t *testing.T) {
	f := newFixture(t)
	f.graphJSON(1, 0, "")
	f.writeOut(".graphify_labels.json", `{"0":"Real"}`)
	f.write("a.go", "package a")
	f.write(".claude/worktrees/agent-1/.git", "gitdir: /elsewhere")
	f.write(".claude/worktrees/agent-1/a.go", "package a")
	f.write(".claude/worktrees/agent-1/deep/b.go", "package b")
	f.manifest(map[string]float64{"a.go": float64(time.Now().Add(time.Hour).Unix())})

	g, _ := Read(Options{Repo: f.repo})
	if g.DriftAdded != 0 {
		t.Fatalf("DriftAdded = %d, want 0 — a nested checkout is not this repository", g.DriftAdded)
	}
}

// A central --out-base does not remove the in-tree graphify-out/ an earlier
// run left behind, and its cache is ~800 files of nothing to do with the
// checkout.
func TestDriftSkipsAnInTreeGraphifyOutWhenTheGraphIsCentral(t *testing.T) {
	repo := t.TempDir()
	central := filepath.Join(t.TempDir(), "repo-abcd1234")
	if err := os.MkdirAll(central, 0o755); err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, repo: repo, out: central}
	f.graphJSON(1, 0, "")
	f.writeOut(".graphify_labels.json", `{"0":"Real"}`)
	f.write("a.go", "package a")
	f.write("graphify-out/cache/semantic/x.go", "package x")
	f.manifest(map[string]float64{"a.go": float64(time.Now().Add(time.Hour).Unix())})

	g, _ := Read(Options{Repo: repo, Out: central})
	if g.DriftAdded != 0 {
		t.Fatalf("DriftAdded = %d, want 0 — a stale in-tree output dir is not drift", g.DriftAdded)
	}
}

// graphify classifies an extensionless file by its shebang, so `.githooks/
// pre-push` is a legitimate manifest entry. The walk filters by the extensions
// the manifest carries, and an extensionless entry has none — so it was never
// visited, never seen, and reported as removed forever. The converse must not
// happen either: a Makefile that graphify skipped is not an added file.
func TestDriftHandlesExtensionlessFiles(t *testing.T) {
	f := newFixture(t)
	f.graphJSON(1, 0, "")
	f.writeOut(".graphify_labels.json", `{"0":"Real"}`)
	f.write("a.go", "package a")
	f.write(".githooks/pre-push", "#!/bin/sh\n")
	f.write("Makefile", "all:\n")
	f.manifest(map[string]float64{
		"a.go":               float64(time.Now().Add(time.Hour).Unix()),
		".githooks/pre-push": float64(time.Now().Add(time.Hour).Unix()),
	})

	g, _ := Read(Options{Repo: f.repo})
	if g.DriftRemoved != 0 {
		t.Errorf("DriftRemoved = %d, want 0 — the hook is right there", g.DriftRemoved)
	}
	if g.DriftAdded != 0 {
		t.Errorf("DriftAdded = %d, want 0 — a Makefile graphify skipped is not drift", g.DriftAdded)
	}
}

// `extract` writes .graphify_analysis.json; .graphify_labels.json only appears
// once clustering has run. A freshly extracted graph has communities and no
// names for them, and the board has to be able to say so — "355 unnamed" is
// what tells you the next command is free clustering, not another extraction.
func TestCommunitiesFallBackToTheAnalysisFile(t *testing.T) {
	f := newFixture(t)
	f.graphJSON(1, 0, "")
	f.writeOut(".graphify_analysis.json",
		`{"communities": {"0": ["a","b"], "1": ["c"], "2": []}, "gods": []}`)

	g, _ := Read(Options{Repo: f.repo})
	if g.Communities != 3 {
		t.Errorf("Communities = %d, want 3 from .graphify_analysis.json", g.Communities)
	}
	if g.Labeled {
		t.Error("Labeled must stay false — the analysis file names nothing")
	}
	if g.State != StateRaw {
		t.Errorf("State = %v, want raw", g.State)
	}
}

// The labels file is the authority when it exists: it is the one that knows
// whether the expensive naming pass has run.
func TestLabelsFileOutranksTheAnalysisFile(t *testing.T) {
	f := newFixture(t)
	f.graphJSON(1, 0, "")
	f.writeOut(".graphify_analysis.json", `{"communities": {"0": [], "1": []}}`)
	f.writeOut(".graphify_labels.json", `{"0":"Payments ingest"}`)

	g, _ := Read(Options{Repo: f.repo})
	if g.Communities != 1 || !g.Labeled {
		t.Fatalf("Communities = %d, Labeled = %v; want 1, true", g.Communities, g.Labeled)
	}
}
