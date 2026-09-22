package heal

import (
	"strings"
	"testing"

	"github.com/dns/ggraphify/internal/gfy"
	"github.com/dns/ggraphify/internal/globalgraph"
	"github.com/dns/ggraphify/internal/graphstate"
	"github.com/dns/ggraphify/internal/workspace"
)

func federation(missing, orphans []string) workspace.State {
	return workspace.State{
		Root:    "/git",
		File:    &workspace.File{Path: "/git/graft/workspace.json", Root: "/git", Exists: true},
		Missing: missing,
		Orphans: orphans,
	}
}

func TestWorkspaceStepCoversDriftOnly(t *testing.T) {
	if _, ok := WorkspaceStep(federation(nil, nil)); ok {
		t.Error("a federation that matches disk was given a rebuild")
	}
	// No workspace.json at all: graft was never pointed at this directory,
	// and federating it for the first time is a decision, not a repair.
	if _, ok := WorkspaceStep(workspace.State{Root: "/git", File: &workspace.File{}}); ok {
		t.Error("the loop federated a directory graft has never federated")
	}
	step, ok := WorkspaceStep(federation([]string{"new"}, nil))
	if !ok {
		t.Fatal("an unfederated checkout got no rebuild")
	}
	if step.Kind != "graft-build" {
		t.Fatalf("kind = %q, want graft-build", step.Kind)
	}
	if step.Cost() != gfy.Free {
		t.Fatalf("cost = %v, want free — this is tree-sitter and nothing else", step.Cost())
	}
}

// The reason is what the log line and the dialog say, and the two directions
// of drift are different problems. Naming the wrong one sends a reader to
// look for a checkout that is not the one at fault.
func TestWorkspaceStepNamesWhichWayItDrifted(t *testing.T) {
	missing, _ := WorkspaceStep(federation([]string{"a", "b"}, nil))
	if !strings.Contains(missing.Why, "misses 2") {
		t.Errorf("Why = %q, want it to count the unfederated checkouts", missing.Why)
	}
	orphans, _ := WorkspaceStep(federation(nil, []string{"gone"}))
	if !strings.Contains(orphans.Why, "no longer on disk") {
		t.Errorf("Why = %q, want it to name the children that are gone", orphans.Why)
	}
	both, _ := WorkspaceStep(federation([]string{"a"}, []string{"gone"}))
	if !strings.Contains(both.Why, "gone") || !strings.Contains(both.Why, "misses 1") {
		t.Errorf("Why = %q, want both directions named", both.Why)
	}
}

// Joining is answerable only with a graph to join. `global add` copies what
// is in graph.json when it runs, so a repository with none would either fail
// the step or merge nothing and record a member that answers nothing.
func TestJoinStepNeedsAGraphToMerge(t *testing.T) {
	for _, st := range []graphstate.State{
		graphstate.StateNone, graphstate.StateBroken, graphstate.StateRunning,
	} {
		if _, ok := JoinStep(graphstate.Graph{State: st}); ok {
			t.Errorf("state %v was merged into the global graph", st)
		}
	}
	for _, st := range []graphstate.State{
		graphstate.StateRaw, graphstate.StateStale, graphstate.StateFresh,
	} {
		step, ok := JoinStep(graphstate.Graph{State: st})
		if !ok {
			t.Fatalf("state %v was refused a merge", st)
		}
		if step.Kind != "global-add" {
			t.Fatalf("kind = %q, want global-add", step.Kind)
		}
		if step.Cost() != gfy.Free {
			t.Fatalf("cost = %v, want free", step.Cost())
		}
	}
}

// GlobalStep's refusal to join is unchanged. It answers about one membership
// and nothing else, and from there absence is still a choice — the fleet root
// is the second fact, and it arrives at JoinStep, not here.
func TestGlobalStepStillRefusesToJoin(t *testing.T) {
	if _, ok := GlobalStep(globalgraph.Member{}); ok {
		t.Error("GlobalStep joined a non-member")
	}
	if _, ok := GlobalStep(globalgraph.Member{In: true}); ok {
		t.Error("GlobalStep re-merged a member that is not stale")
	}
}

func TestPruneStepNamesTheTagItDrops(t *testing.T) {
	step := PruneStep("gone")
	if step.Kind != "global-remove" {
		t.Fatalf("kind = %q, want global-remove", step.Kind)
	}
	if !strings.Contains(step.Why, "gone") {
		t.Errorf("Why = %q, want it to name the member being dropped", step.Why)
	}
	if step.Cost() != gfy.Free {
		t.Fatalf("cost = %v, want free", step.Cost())
	}
}
