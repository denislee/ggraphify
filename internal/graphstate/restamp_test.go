package graphstate

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	builtSHA = "1111111111111111111111111111111111111111"
	headSHA  = "2222222222222222222222222222222222222222"
)

// The case the whole file exists for: a commit landed, `graphify update` ran,
// found nothing to rebuild and left the stamp on the old commit. Restamping it
// is what takes the row out of `behind`, and it must not disturb anything else
// in the file while doing so.
func TestRestampAdvancesTheCommitAndLeavesTheGraphAlone(t *testing.T) {
	f := newFixture(t)
	f.graphJSON(12, 30, builtSHA)
	before, err := os.ReadFile(filepath.Join(f.out, "graph.json"))
	if err != nil {
		t.Fatal(err)
	}

	ok, err := Restamp(f.out, headSHA)
	if err != nil || !ok {
		t.Fatalf("Restamp = %v, %v; want true, nil", ok, err)
	}

	g, err := Read(Options{Repo: f.repo, Head: headSHA, SkipDrift: true})
	if err != nil {
		t.Fatal(err)
	}
	if g.BuiltCommit != headSHA {
		t.Fatalf("BuiltCommit = %q, want %q", g.BuiltCommit, headSHA)
	}
	if g.Behind() {
		t.Error("the graph is still Behind after a restamp — the fix did not take")
	}
	if g.Nodes != 12 || g.Links != 30 {
		t.Errorf("counters moved: %d nodes, %d links, want 12 and 30", g.Nodes, g.Links)
	}

	// Byte-for-byte, the only difference is the stamp. A splice that reflowed
	// the file would make every restamp look like a rebuild to anything
	// diffing graph.json.
	after, err := os.ReadFile(filepath.Join(f.out, "graph.json"))
	if err != nil {
		t.Fatal(err)
	}
	if want := strings.Replace(string(before), builtSHA, headSHA, 1); string(after) != want {
		t.Errorf("restamp rewrote more than the stamp:\n got %s\nwant %s", after, want)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(after, &doc); err != nil {
		t.Fatalf("restamped graph.json does not parse: %v", err)
	}
}

// The stamp's value is spliced by offset, so a graph.json whose stamp is NOT
// the last member has to come out just as valid.
func TestRestampHandlesAStampThatIsNotLast(t *testing.T) {
	f := newFixture(t)
	f.writeOut("graph.json", `{"built_at_commit":"`+builtSHA+`","directed":false,"graph":0,`+
		`"nodes":[{"id":"n0","label":"N"}],"links":[]}`)

	if ok, err := Restamp(f.out, headSHA); err != nil || !ok {
		t.Fatalf("Restamp = %v, %v; want true, nil", ok, err)
	}
	g, _ := Read(Options{Repo: f.repo, Head: headSHA, SkipDrift: true})
	if g.BuiltCommit != headSHA {
		t.Fatalf("BuiltCommit = %q, want %q", g.BuiltCommit, headSHA)
	}
	if g.Nodes != 1 {
		t.Errorf("Nodes = %d, want 1", g.Nodes)
	}
}

// A graph already at HEAD is not rewritten at all — including when the two are
// the same commit written to different lengths, which is the exact case
// Behind() takes care not to misread.
func TestRestampIsANoOpWhenAlreadyAtHead(t *testing.T) {
	for _, stamp := range []string{headSHA, headSHA[:7]} {
		f := newFixture(t)
		f.graphJSON(2, 1, stamp)
		fi, err := os.Stat(filepath.Join(f.out, "graph.json"))
		if err != nil {
			t.Fatal(err)
		}
		ok, err := Restamp(f.out, headSHA)
		if err != nil {
			t.Fatalf("stamp %q: %v", stamp, err)
		}
		if ok {
			t.Errorf("stamp %q: reported a rewrite of a graph already at HEAD", stamp)
		}
		fi2, err := os.Stat(filepath.Join(f.out, "graph.json"))
		if err != nil {
			t.Fatal(err)
		}
		if !fi2.ModTime().Equal(fi.ModTime()) {
			t.Errorf("stamp %q: graph.json was touched anyway", stamp)
		}
	}
}

// A graph with no stamp is never Behind, so there is nothing to advance — and
// the caller is told which of the two "nothing happened" answers it got.
func TestRestampReportsAGraphWithNoStamp(t *testing.T) {
	f := newFixture(t)
	f.writeOut("graph.json", `{"directed":false,"nodes":[],"links":[]}`)
	ok, err := Restamp(f.out, headSHA)
	if ok || !errors.Is(err, ErrNoStamp) {
		t.Fatalf("Restamp = %v, %v; want false, ErrNoStamp", ok, err)
	}
}

// The head value is written straight into a JSON string. Anything that is not
// a commit id is refused rather than interpolated.
func TestRestampRefusesAHeadThatIsNotACommit(t *testing.T) {
	f := newFixture(t)
	f.graphJSON(2, 1, builtSHA)
	for _, bad := range []string{`x", "nodes": [], "x":"`, "not-hex", "abc", strings.Repeat("a", 65)} {
		if ok, err := Restamp(f.out, bad); ok || err == nil {
			t.Errorf("Restamp(%q) = %v, %v; want false and an error", bad, ok, err)
		}
	}
	g, _ := Read(Options{Repo: f.repo, Head: headSHA, SkipDrift: true})
	if g.BuiltCommit != builtSHA {
		t.Fatalf("BuiltCommit = %q — a refused head reached the file", g.BuiltCommit)
	}
}

// The report is what agents are pointed at for architecture questions; a
// provenance line contradicting the graph beside it is worth the one write.
func TestRestampFixesTheReportProvenanceLine(t *testing.T) {
	f := newFixture(t)
	f.graphJSON(2, 1, builtSHA)
	f.writeOut("GRAPH_REPORT.md", "# Graph\n\n- Nodes: 2\n"+
		reportCommitPrefix+"`"+builtSHA[:8]+"`\n- Built at: whenever\n")

	if ok, err := Restamp(f.out, headSHA); err != nil || !ok {
		t.Fatalf("Restamp = %v, %v", ok, err)
	}
	b, err := os.ReadFile(filepath.Join(f.out, "GRAPH_REPORT.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), reportCommitPrefix+"`"+headSHA[:8]+"`") {
		t.Errorf("report still names the old commit:\n%s", b)
	}
	if !strings.Contains(string(b), "- Built at: whenever") {
		t.Errorf("report lost lines around the one that was rewritten:\n%s", b)
	}
}

// A missing report is the ordinary case for a graph that has only ever been
// extracted, and must not fail the write that already landed.
func TestRestampSurvivesAMissingReport(t *testing.T) {
	f := newFixture(t)
	f.graphJSON(2, 1, builtSHA)
	if ok, err := Restamp(f.out, headSHA); err != nil || !ok {
		t.Fatalf("Restamp = %v, %v", ok, err)
	}
}

func TestNeedsUpdateFlag(t *testing.T) {
	f := newFixture(t)
	if NeedsUpdateFlag(f.out) {
		t.Error("flag reported on a directory that has none")
	}
	f.writeOut("needs_update", "")
	if !NeedsUpdateFlag(f.out) {
		t.Error("flag not reported when the file is there")
	}
	if NeedsUpdateFlag("") {
		t.Error("an empty path is not a directory with a flag in it")
	}
}
