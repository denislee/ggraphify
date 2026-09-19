package ui

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dns/ggraphify/internal/graphstate"
)

const (
	testBuiltSHA = "1111111111111111111111111111111111111111"
	testHeadSHA  = "2222222222222222222222222222222222222222"
)

// out builds an output directory holding a graph stamped at testBuiltSHA.
func stampedOut(t *testing.T) string {
	t.Helper()
	out := t.TempDir()
	body := `{"directed":false,"graph":0,"nodes":[{"id":"n0","label":"N"}],"links":[],` +
		`"built_at_commit":"` + testBuiltSHA + `"}`
	if err := os.WriteFile(filepath.Join(out, "graph.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return out
}

func stampOf(t *testing.T, repo, out string) string {
	t.Helper()
	g, err := graphstate.Read(graphstate.Options{Repo: repo, Out: out, SkipDrift: true})
	if err != nil {
		t.Fatal(err)
	}
	return g.BuiltCommit
}

// The case it exists for: the rescan walked the tree, found nothing changed,
// and graphify therefore rewrote nothing — stamp included.
func TestRestampBehindAdvancesACleanRescan(t *testing.T) {
	out := stampedOut(t)
	if !restampBehind(t.TempDir(), out, testHeadSHA, graphstate.Drift{}) {
		t.Fatal("a clean rescan did not advance the stamp")
	}
	if got := stampOf(t, t.TempDir(), out); got != testHeadSHA {
		t.Fatalf("BuiltCommit = %q, want %q", got, testHeadSHA)
	}
}

// Everything that would make the advance a lie. A stamp moved under any of
// these would turn a graph that genuinely lags its tree into a row the board
// calls fresh — the precise failure the behind check exists to catch.
func TestRestampBehindRefusesWhatItCannotVouchFor(t *testing.T) {
	cases := []struct {
		name  string
		head  string
		drift graphstate.Drift
		flag  bool
	}{
		{name: "no head to stamp with", head: ""},
		{name: "files the rescan left changed", head: testHeadSHA,
			drift: graphstate.Drift{Changed: []string{"a.go"}}},
		{name: "flagged for re-extraction", head: testHeadSHA, flag: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := stampedOut(t)
			if c.flag {
				if err := os.WriteFile(filepath.Join(out, "needs_update"), nil, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if restampBehind(t.TempDir(), out, c.head, c.drift) {
				t.Error("reported a stamp advance it had no grounds for")
			}
			if got := stampOf(t, t.TempDir(), out); got != testBuiltSHA {
				t.Fatalf("BuiltCommit = %q — the stamp moved anyway", got)
			}
		})
	}
}

// An output directory with no graph in it is the ordinary state of a
// repository that has never been built, and must not produce an error path.
func TestRestampBehindSurvivesAnEmptyOutputDirectory(t *testing.T) {
	if restampBehind(t.TempDir(), t.TempDir(), testHeadSHA, graphstate.Drift{}) {
		t.Error("claimed to restamp a directory with no graph.json")
	}
	if restampBehind(t.TempDir(), "", testHeadSHA, graphstate.Drift{}) {
		t.Error("claimed to restamp an empty path")
	}
}
