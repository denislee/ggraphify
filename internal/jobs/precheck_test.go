package jobs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dns/ggraphify/internal/gfy"
)

// withGraph builds an output directory holding a plausible graph.json.
func withGraph(t *testing.T) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "graphify-out")
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(out, "graph.json"), []byte(`{"nodes":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	return out
}

// The regression this whole gate exists for: `label` on a checkout that was
// never extracted. graphify answers "no graph found at …/graph.json — run
// /graphify first" only after the job has been queued, and for a metered
// command only after a confirm dialog has already named the bill.
func TestPrecheckRefusesLabelWithoutAGraph(t *testing.T) {
	err := RequireGraph(&Job{Kind: "label", Repo: "/repo", Out: filepath.Join(t.TempDir(), "never-built")})
	if err == nil {
		t.Fatal("label was allowed against an output directory with no graph.json")
	}
	if !strings.Contains(err.Error(), "graph.json") || !strings.Contains(err.Error(), "Extract") {
		t.Errorf("the refusal names neither the file nor the remedy: %v", err)
	}
}

// The gate is about the graph, not about the money: it must not block the two
// commands that build a graph from nothing, or the repository would have no
// way out of the state.
func TestPrecheckAllowsTheCommandsThatBuildAGraph(t *testing.T) {
	none := filepath.Join(t.TempDir(), "never-built")
	for _, kind := range []string{"extract", "update", "install", "hook-install", "global-list"} {
		if err := RequireGraph(&Job{Kind: kind, Out: none}); err != nil {
			t.Errorf("%s was refused on a graphless repository: %v", kind, err)
		}
	}
}

// Every graph-reading kind is covered, and all of them pass once a graph is
// actually there.
func TestPrecheckCoversEveryGraphReadingKind(t *testing.T) {
	none := filepath.Join(t.TempDir(), "never-built")
	has := withGraph(t)
	n := 0
	for kind, spec := range gfy.Known {
		if !spec.NeedsGraph {
			continue
		}
		n++
		if err := RequireGraph(&Job{Kind: kind, Out: none}); err == nil {
			t.Errorf("%s was allowed with no graph", kind)
		}
		if err := RequireGraph(&Job{Kind: kind, Out: has}); err != nil {
			t.Errorf("%s was refused with a graph present: %v", kind, err)
		}
	}
	if n == 0 {
		t.Fatal("no kind is marked NeedsGraph; the gate would be inert")
	}
}

// An unknown kind is never gated: the hook exists to give a clearer message
// than graphify's own, not to become a second allowlist that a new command has
// to be added to before it can run.
func TestPrecheckIgnoresUnknownKinds(t *testing.T) {
	if err := RequireGraph(&Job{Kind: "no-such-kind", Out: filepath.Join(t.TempDir(), "nope")}); err != nil {
		t.Errorf("an unknown kind was gated: %v", err)
	}
}
