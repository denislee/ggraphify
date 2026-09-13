package heal

import (
	"strings"
	"testing"

	"github.com/dns/ggraphify/internal/gfy"
	"github.com/dns/ggraphify/internal/graphstate"
)

func kinds(p Plan) []string {
	var out []string
	for _, s := range p.Steps {
		out = append(out, s.Kind)
	}
	return out
}

func want(t *testing.T, p Plan, kk ...string) {
	t.Helper()
	got := strings.Join(kinds(p), " → ")
	expect := strings.Join(kk, " → ")
	if got != expect {
		t.Fatalf("plan = %q, want %q", got, expect)
	}
}

// A healthy graph produces no plan at all. The button has to be able to say
// "nothing to do" rather than inventing work.
func TestHealthyGraphNeedsNothing(t *testing.T) {
	g := graphstate.Graph{
		State: graphstate.StateFresh, Communities: 12, Labeled: true, HasReport: true,
	}
	if !g.Healthy() {
		t.Fatalf("graph should be healthy: %v", g.IssueSummary())
	}
	p := For(g, true)
	if !p.Empty() {
		t.Fatalf("plan = %v, want empty", kinds(p))
	}
}

// The state the reference repository was actually left in by a full
// `extract --backend claude-cli`: a complete graph, 355 communities, no names
// and no report, because extract stops at graph.json on purpose.
func TestFreshlyExtractedGraphClustersThenLabels(t *testing.T) {
	g := graphstate.Graph{
		State: graphstate.StateRaw, Nodes: 7169, Communities: 355, Labeled: false,
	}
	want(t, For(g, true), "cluster-only", "label")
	if !For(g, true).Metered() {
		t.Error("naming communities is metered and the plan must say so")
	}
}

// Free-only gets you the report and stops, and says which issue it left behind
// rather than claiming the repository is fixed.
func TestFreeOnlyPlanNamesWhatItCannotReach(t *testing.T) {
	g := graphstate.Graph{
		State: graphstate.StateRaw, Nodes: 7169, Communities: 355, Labeled: false,
	}
	p := For(g, false)
	want(t, p, "cluster-only")
	if p.Metered() {
		t.Error("a free-only plan must not contain a metered step")
	}
	if len(p.Unreachable) != 1 || p.Unreachable[0].Code != graphstate.IssueUnnamed {
		t.Fatalf("Unreachable = %+v, want the unnamed-communities issue", p.Unreachable)
	}
}

func TestNoGraphExtractsClustersLabels(t *testing.T) {
	g := graphstate.Graph{State: graphstate.StateNone}
	want(t, For(g, true), "extract", "cluster-only", "label")
}

// Without an API key or a Claude Code login, `update` is the free half of an
// extraction and still produces a clustered, reported graph — just one with no
// doc/image nodes and no community names.
func TestNoGraphFreeOnlyUpdatesInstead(t *testing.T) {
	g := graphstate.Graph{State: graphstate.StateNone}
	p := For(g, false)
	want(t, p, "update")
	if len(p.Unreachable) == 0 {
		t.Error("the free plan cannot name communities and must say so")
	}
}

// A broken output directory is rebuilt with --force: graphify refuses to
// replace a larger graph with a smaller one, and a half-written graph.json is
// exactly where that guard would otherwise block the fix.
func TestBrokenGraphIsForced(t *testing.T) {
	g := graphstate.Graph{State: graphstate.StateBroken, Err: "graph.json is missing"}
	p := For(g, true)
	want(t, p, "extract", "cluster-only", "label")
	if !p.Steps[0].Force {
		t.Error("the rebuild of a broken graph must pass --force")
	}
	var params gfy.Params
	p.Steps[0].Apply(&params)
	argv := gfy.Argv(p.Steps[0].Kind, params)
	if !containsArg(argv, "--force") {
		t.Fatalf("argv = %v, want --force", argv)
	}
}

// Drift is the free case, and it must not become an extraction by accident:
// that is the difference between a rebuild that costs nothing and one that
// bills an LLM for every doc in the repository.
func TestDriftIsFreeEvenWhenMeteredIsAllowed(t *testing.T) {
	g := graphstate.Graph{
		State: graphstate.StateStale, Communities: 40, Labeled: true, HasReport: true,
		DriftAdded: 3, DriftChanged: 1,
	}
	p := For(g, true)
	want(t, p, "update")
	if p.Metered() {
		t.Fatal("drift is an AST rebuild; nothing here should be metered")
	}
}

// graphify's own needs_update flag is set for changes the AST pass cannot
// reconcile, so it is the one drift-shaped issue whose remedy is the expensive
// command.
func TestNeedsUpdateEscalatesToExtract(t *testing.T) {
	g := graphstate.Graph{
		State: graphstate.StateStale, Communities: 40, Labeled: true, HasReport: true,
		NeedsUpdate: true,
	}
	want(t, For(g, true), "extract", "cluster-only")

	free := For(g, false)
	want(t, free, "update")
	if len(free.Unreachable) != 1 || free.Unreachable[0].Code != graphstate.IssueNeedsUpdate {
		t.Fatalf("Unreachable = %+v, want needs-extract", free.Unreachable)
	}
}

// Clustering runs once. An `update` already clusters, so queueing
// `cluster-only` behind it would be a second pass over the same graph for
// nothing.
func TestUpdateIsNotFollowedByRedundantClustering(t *testing.T) {
	g := graphstate.Graph{
		State: graphstate.StateStale, Communities: 40, Labeled: true, HasReport: true,
		DriftAdded: 1,
	}
	want(t, For(g, true), "update")
}

// Every kind a plan can emit has to exist in the one table the runner and the
// argv builder read, or the Fix button queues a job that cannot be built.
func TestEveryPlannedKindIsKnown(t *testing.T) {
	graphs := []graphstate.Graph{
		{State: graphstate.StateNone},
		{State: graphstate.StateBroken},
		{State: graphstate.StateRaw, Communities: 3},
		{State: graphstate.StateStale, DriftAdded: 1},
		{State: graphstate.StateStale, NeedsUpdate: true},
		{State: graphstate.StateFresh, Communities: 3, Labeled: true},
	}
	for _, g := range graphs {
		for _, allow := range []bool{true, false} {
			for _, s := range For(g, allow).Steps {
				if _, ok := gfy.Known[s.Kind]; !ok {
					t.Errorf("plan for %v emits unknown kind %q", g.State, s.Kind)
				}
				var p gfy.Params
				p.Repo = "/tmp/repo"
				s.Apply(&p)
				if gfy.Argv(s.Kind, p) == nil {
					t.Errorf("no argv builder for planned kind %q", s.Kind)
				}
			}
		}
	}
}

func containsArg(argv []string, want string) bool {
	for _, a := range argv {
		if a == want {
			return true
		}
	}
	return false
}
