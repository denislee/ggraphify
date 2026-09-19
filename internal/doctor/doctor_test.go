package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dns/ggraphify/internal/board"
	"github.com/dns/ggraphify/internal/discover"
	"github.com/dns/ggraphify/internal/graphstate"
	"github.com/dns/ggraphify/internal/knowledge"
)

func graphed(t *testing.T, base, repo, built, head string) board.Row {
	t.Helper()
	out := filepath.Join(base, discover.Slug(repo))
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(out, "graph.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	g := graphstate.Graph{
		Out: out, State: graphstate.StateFresh, Nodes: 1,
		BuiltCommit: built, HeadCommit: head, BuiltAt: time.Now(),
	}
	return board.Row{
		Repo:   discover.Repo{Path: repo, Name: filepath.Base(repo), HeadSHA: head, Out: out},
		Graph:  g,
		Behind: g.Behind(),
	}
}

// The check that would have caught the original problem: 212 graphs in a
// central directory that nothing pointed at from the checkouts they describe.
func TestStorageCheckFailsWhenNothingPublishesTheIndex(t *testing.T) {
	base := t.TempDir()
	rows := []board.Row{graphed(t, base, filepath.Join(t.TempDir(), "acquiring"), "1111111", "1111111")}

	c := storageCheck(Options{Rows: rows, OutBase: base})
	if c.Level != Bad {
		t.Fatalf("level = %v, want Bad — an unpublished index is the invariant being asserted", c.Level)
	}
	if !strings.Contains(c.Do, "-index") {
		t.Errorf("the remedy does not name the command that writes it: %q", c.Do)
	}

	if _, _, err := knowledge.Sync(base, rows); err != nil {
		t.Fatal(err)
	}
	if c := storageCheck(Options{Rows: rows, OutBase: base}); c.Level != OK {
		t.Fatalf("level = %v after publishing, want OK (%s)", c.Level, c.Detail)
	}
}

// A graphed checkout the index does not mention is the basename-collision
// failure in its observable form: the consumer resolves nothing for it.
func TestStorageCheckWarnsWhenARepoIsMissingFromTheIndex(t *testing.T) {
	base := t.TempDir()
	one := graphed(t, base, filepath.Join(t.TempDir(), "one"), "1111111", "1111111")
	two := graphed(t, base, filepath.Join(t.TempDir(), "two"), "2222222", "2222222")
	if _, _, err := knowledge.Sync(base, []board.Row{one}); err != nil {
		t.Fatal(err)
	}
	c := storageCheck(Options{Rows: []board.Row{one, two}, OutBase: base})
	if c.Level != Warn {
		t.Fatalf("level = %v, want Warn (%s)", c.Level, c.Detail)
	}
}

// In-tree graphs are discoverable from the repository they describe, so the
// absence of an index is not a finding.
func TestStorageCheckIsSilentWithoutAnOutBase(t *testing.T) {
	if c := storageCheck(Options{}); c.Level != OK {
		t.Fatalf("level = %v, want OK", c.Level)
	}
}

func TestStalenessCheckCountsGraphsBehindHead(t *testing.T) {
	base := t.TempDir()
	rows := []board.Row{
		graphed(t, base, filepath.Join(t.TempDir(), "fresh"), "1111111", "1111111"),
		graphed(t, base, filepath.Join(t.TempDir(), "moved"), "1111111", "2222222"),
	}
	c := stalenessCheck(Options{Rows: rows})
	if c.Level != Warn {
		t.Fatalf("level = %v, want Warn", c.Level)
	}
	if !strings.HasPrefix(c.Value, "1 of 2") {
		t.Errorf("value = %q, want it to count 1 of 2", c.Value)
	}
	// Behind HEAD is a warning, never a failure: the graph is still a true
	// description of a commit, and clearing it costs nothing but is not
	// urgent. A doctor that exited non-zero over it could never gate a cron.
	if c.Level == Bad {
		t.Error("behind-HEAD was reported as a broken invariant")
	}
}

// The symptom that hides a backend which has never worked.
func TestTierCheckNamesAnASTOnlyEstate(t *testing.T) {
	base := t.TempDir()
	r := graphed(t, base, filepath.Join(t.TempDir(), "ast"), "1111111", "1111111")
	if err := os.WriteFile(filepath.Join(r.Graph.Out, "manifest.json"),
		[]byte(`{"a.go":{"mtime":1,"ast_hash":"h","semantic_hash":""}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	c := tierCheck(Options{Rows: []board.Row{r}})
	if c.Level != Warn {
		t.Fatalf("level = %v, want Warn", c.Level)
	}
	if !strings.Contains(c.Detail, "no metered pass") {
		t.Errorf("detail does not say the semantic pass never ran: %q", c.Detail)
	}
}

// Every check renders, and the text puts the remedy where a reader looks for
// it. This is the report an agent is pointed at, so an empty line or a missing
// arrow is a real defect.
func TestTextRendersEveryCheckWithItsRemedy(t *testing.T) {
	r := Report{Checks: []Check{
		{Name: "backend", Value: "ollama — NOT READY", Detail: "no server", Do: "start one", Level: Bad},
		{Name: "out_base", Value: "~/knowledge", Level: OK},
	}}
	out := r.Text()
	for _, want := range []string{"backend", "NOT READY", "[BAD]", "→ start one", "out_base"} {
		if !strings.Contains(out, want) {
			t.Errorf("the report does not contain %q:\n%s", want, out)
		}
	}
	if r.Worst() != Bad {
		t.Errorf("Worst() = %v, want Bad", r.Worst())
	}
}
