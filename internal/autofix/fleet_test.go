package autofix

import (
	"testing"
	"time"

	"github.com/dns/ggraphify/internal/globalgraph"
	"github.com/dns/ggraphify/internal/workspace"
)

// Enrolment: the half of the third index that changes the MEMBERSHIP rather
// than the members. Every test here is about the one hinge that makes it
// safe — a repository is only ever joined when the user nominated the root it
// sits under.

func enrollOn() Policy { p := globalOn(); p.Enroll = true; return p }

// The rule TestNonMembersAreNeverJoined still states, restated for the switch
// that lifts it: with enrolment on but no fleet root behind this row, nothing
// joins. Enrollable is the judgement, and it is not this package's to make.
func TestEnrolmentStillRefusesARepositoryNoRootNominated(t *testing.T) {
	e, _ := fixed(t)
	c := healthy("/scratch/a", "a") // Enrollable is false: not under a fleet root
	acts, _ := e.Plan([]Candidate{c}, enrollOn())
	if len(acts) != 0 {
		t.Fatalf("joined a repository under no nominated root: %v", kinds(enrollOn(), acts[0]))
	}
}

// Under a nominated root, absence IS the defect, and a healthy graph is
// merged with one free step.
func TestAHealthyRepositoryUnderAFleetRootIsJoined(t *testing.T) {
	e, _ := fixed(t)
	c := healthy("/git/a", "a")
	c.Enrollable = true
	acts, _ := e.Plan([]Candidate{c}, enrollOn())
	if len(acts) != 1 {
		t.Fatalf("got %d actions, want 1 — an unmerged repository under a fleet root", len(acts))
	}
	if got := kinds(enrollOn(), acts[0]); len(got) != 1 || got[0] != "global-add" {
		t.Fatalf("plan = %v, want [global-add]", got)
	}
	if acts[0].Sig != globalgraph.IssueOut {
		t.Fatalf("Sig = %q, want %q", acts[0].Sig, globalgraph.IssueOut)
	}
	if acts[0].Plan.Metered() {
		t.Error("a merge of two files on disk is free; the plan says it is metered")
	}
}

// The switch is a switch, here too.
func TestEnrolmentOffLeavesAnUnmergedRepositoryAlone(t *testing.T) {
	e, _ := fixed(t)
	c := healthy("/git/a", "a")
	c.Enrollable = true
	acts, skips := e.Plan([]Candidate{c}, globalOn())
	if len(acts) != 0 {
		t.Fatalf("joined a repository with enrolment off: %v", kinds(globalOn(), acts[0]))
	}
	if len(skips) != 0 {
		t.Fatalf("a repository with nothing to do produced skips: %v", skips)
	}
}

// The gate that stops the loop giving every unextracted checkout a permanent
// defect: there is nothing to merge from a repository with no graph, so its
// absence from the global graph is not a defect and never enters the
// signature. Getting this wrong burns an attempt budget every cooldown,
// forever, on every repository the board has never extracted.
func TestARepositoryWithNoGraphIsNotJoined(t *testing.T) {
	e, _ := fixed(t)
	c := unbuilt("/git/a", "a")
	c.Enrollable = true
	acts, _ := e.Plan([]Candidate{c}, enrollOn())
	if len(acts) != 1 {
		t.Fatalf("got %d actions, want 1 — the extraction itself", len(acts))
	}
	for _, k := range kinds(enrollOn(), acts[0]) {
		if k == "global-add" {
			t.Fatalf("merged a repository that has no graph to merge: %v",
				kinds(enrollOn(), acts[0]))
		}
	}
}

// Ordering, for the join exactly as for the re-merge: the merge copies the
// nodes that are in graph.json when it runs, so a join queued before the
// rebuild that is part of the same chain would record a member that is stale
// before the chain finishes.
func TestAJoinRunsAfterTheStepsThatRewriteTheGraph(t *testing.T) {
	e, _ := fixed(t)
	c := drifted("/git/a", "a")
	c.Enrollable = true
	acts, _ := e.Plan([]Candidate{c}, enrollOn())
	if len(acts) != 1 {
		t.Fatalf("got %d actions, want 1", len(acts))
	}
	got := kinds(enrollOn(), acts[0])
	if len(got) < 2 || got[len(got)-1] != "global-add" {
		t.Fatalf("plan = %v, want it to end with global-add", got)
	}
}

// The fleet pass: repairs whose subject is a directory, or a member with no
// checkout left to have a row.

func federated(root string, missing, orphans []string) Root {
	return Root{Path: root, Workspace: workspace.State{
		Root:    root,
		File:    &workspace.File{Path: root + "/graft/workspace.json", Root: root, Exists: true},
		Missing: missing,
		Orphans: orphans,
	}}
}

func fleetKinds(acts []FleetAction) []string {
	out := make([]string, 0, len(acts))
	for _, a := range acts {
		out = append(out, a.Step.Kind)
	}
	return out
}

func TestFleetPassIsOffWithEnrolmentOff(t *testing.T) {
	e, _ := fixed(t)
	acts, _ := e.PlanFleet(
		[]Root{federated("/git", []string{"new"}, nil)},
		[]Stranded{{Tag: "gone", Source: "/knowledge/gone/graph.json"}},
		graftOnFleet(globalOn()))
	if len(acts) != 0 {
		t.Fatalf("the fleet pass ran with enrolment off: %v", fleetKinds(acts))
	}
}

func graftOnFleet(p Policy) Policy { p.Graft = true; return p }

func TestAnUnfederatedCheckoutRebuildsTheWorkspace(t *testing.T) {
	e, _ := fixed(t)
	acts, _ := e.PlanFleet(
		[]Root{federated("/git", []string{"new"}, nil)}, nil, graftOnFleet(enrollOn()))
	if len(acts) != 1 {
		t.Fatalf("got %d actions, want 1", len(acts))
	}
	if acts[0].Step.Kind != "graft-build" {
		t.Fatalf("kind = %q, want graft-build", acts[0].Step.Kind)
	}
	if acts[0].Path != "/git" {
		t.Fatalf("Path = %q, want the root — the rebuild is pointed at the directory, "+
			"not at any one checkout", acts[0].Path)
	}
	if acts[0].Tag != "" {
		t.Fatalf("Tag = %q on a workspace rebuild", acts[0].Tag)
	}
}

// `graft build` at the root rewrites every child's graft/ directory. Doing
// that while a per-repository build is writing one of them is two writers in
// one tree.
func TestABusyRootIsLeftAlone(t *testing.T) {
	e, _ := fixed(t)
	r := federated("/git", []string{"new"}, nil)
	r.Busy = true
	acts, skips := e.PlanFleet([]Root{r}, nil, graftOnFleet(enrollOn()))
	if len(acts) != 0 {
		t.Fatalf("rebuilt a root with a job already running in it: %v", fleetKinds(acts))
	}
	if len(skips) != 1 {
		t.Fatalf("got %d skips, want one saying why", len(skips))
	}
}

// Without graft on this machine the rebuild is a command that cannot run.
// Queueing it would fail once per root per cooldown for a reason that has
// nothing to do with the root.
func TestNoGraftMeansNoWorkspaceRebuild(t *testing.T) {
	e, _ := fixed(t)
	acts, _ := e.PlanFleet(
		[]Root{federated("/git", []string{"new"}, nil)}, nil, enrollOn())
	if len(acts) != 0 {
		t.Fatalf("queued a graft build on a machine without graft: %v", fleetKinds(acts))
	}
}

func TestAFederatedRootNeedsNothing(t *testing.T) {
	e, _ := fixed(t)
	acts, skips := e.PlanFleet(
		[]Root{federated("/git", nil, nil)}, nil, graftOnFleet(enrollOn()))
	if len(acts) != 0 || len(skips) != 0 {
		t.Fatalf("a root in step produced %d actions and %d skips", len(acts), len(skips))
	}
}

func TestAStrandedMemberIsDropped(t *testing.T) {
	e, _ := fixed(t)
	acts, _ := e.PlanFleet(nil,
		[]Stranded{{Tag: "gone", Source: "/knowledge/gone/graph.json"}}, enrollOn())
	if len(acts) != 1 {
		t.Fatalf("got %d actions, want 1", len(acts))
	}
	if acts[0].Step.Kind != "global-remove" {
		t.Fatalf("kind = %q, want global-remove", acts[0].Step.Kind)
	}
	if acts[0].Tag != "gone" {
		t.Fatalf("Tag = %q, want the tag — it is the only handle `global remove` takes",
			acts[0].Tag)
	}
	if acts[0].Sig != globalgraph.IssueStranded {
		t.Fatalf("Sig = %q, want %q", acts[0].Sig, globalgraph.IssueStranded)
	}
}

// A prune is not gated on Global: Global governs re-merging members that are
// still there, and a member whose source is gone can never be re-merged.
func TestAStrandedMemberIsDroppedEvenWithTheRemergeSwitchOff(t *testing.T) {
	e, _ := fixed(t)
	pol := on()
	pol.Enroll = true // Global stays off
	acts, _ := e.PlanFleet(nil, []Stranded{{Tag: "gone", Source: "/k/gone/graph.json"}}, pol)
	if len(acts) != 1 {
		t.Fatalf("got %d actions, want the prune", len(acts))
	}
}

// One repair per subject per tick, and the slot stays reserved until it is
// released — the property that stops a 30-second tick queueing the same
// whole-root rebuild over and over while the first is still running.
func TestAReservedFleetSlotIsNotQueuedTwice(t *testing.T) {
	e, _ := fixed(t)
	pol := graftOnFleet(enrollOn())
	roots := []Root{federated("/git", []string{"new"}, nil)}
	first, _ := e.PlanFleet(roots, nil, pol)
	if len(first) != 1 {
		t.Fatalf("first tick planned %d actions, want 1", len(first))
	}
	again, _ := e.PlanFleet(roots, nil, pol)
	if len(again) != 0 {
		t.Fatalf("queued the same root while its rebuild was still in flight: %v",
			fleetKinds(again))
	}
	e.FleetDone(first[0], "")
	if e.Running() != 0 {
		t.Fatalf("Running = %d after the only action finished", e.Running())
	}
}

// A rebuild that made partial progress gets a fresh budget rather than
// reading as the identical complaint coming back: four of five new checkouts
// federated is progress, and Max is the only thing that should pace it.
func TestPartialProgressIsNotTheSameDefect(t *testing.T) {
	e, now := fixed(t)
	pol := graftOnFleet(enrollOn())
	first, _ := e.PlanFleet([]Root{federated("/git", []string{"a", "b"}, nil)}, nil, pol)
	if len(first) != 1 {
		t.Fatalf("first tick planned %d actions, want 1", len(first))
	}
	e.FleetDone(first[0], "still short")

	second, _ := e.PlanFleet([]Root{federated("/git", []string{"b"}, nil)}, nil, pol)
	if len(second) != 1 {
		t.Fatalf("a root that went from 2 unfederated to 1 was not re-planned")
	}
	if second[0].Attempt != 1 {
		t.Fatalf("Attempt = %d, want a fresh budget on the changed drift", second[0].Attempt)
	}
	_ = now
}

// The cap is on what this loop is doing at once, across both passes: a
// whole-root `graft build` is the most expensive free thing it can queue.
func TestTheFleetPassRespectsMax(t *testing.T) {
	e, _ := fixed(t)
	pol := graftOnFleet(enrollOn())
	pol.Max = 1
	acts, _ := e.PlanFleet(
		[]Root{federated("/git", []string{"new"}, nil)},
		[]Stranded{{Tag: "gone", Source: "/k/gone/graph.json"}}, pol)
	if len(acts) != 1 {
		t.Fatalf("got %d actions with Max=1", len(acts))
	}
}

// A `graft build` that exits 0 and leaves the same drift behind is not a
// repair. Forgetting the record on a clean exit reset the attempt count and
// the cooldown together, so the next tick re-queued the whole-root rebuild at
// once — 178 back-to-back rebuilds of ~/git in one afternoon.
func TestACleanRebuildThatLeavesTheDriftIsNotRequeuedAtOnce(t *testing.T) {
	e, now := fixed(t)
	pol := graftOnFleet(enrollOn())
	roots := []Root{federated("/git", nil, []string{"pulumi-wt-a"})}

	first, _ := e.PlanFleet(roots, nil, pol)
	if len(first) != 1 {
		t.Fatalf("first tick planned %d actions, want 1", len(first))
	}
	e.FleetDone(first[0], "")

	again, skips := e.PlanFleet(roots, nil, pol)
	if len(again) != 0 {
		t.Fatalf("re-queued the rebuild the tick after a clean run that changed nothing: %v",
			fleetKinds(again))
	}
	if len(skips) != 1 || skips[0].Why != "cooling down" {
		t.Fatalf("skips = %v, want one cooling down", skips)
	}

	*now = now.Add(DefaultCooldown)
	second, _ := e.PlanFleet(roots, nil, pol)
	if len(second) != 1 || second[0].Attempt != 2 {
		t.Fatalf("after the cooldown got %v, want attempt 2", second)
	}
	e.FleetDone(second[0], "")

	*now = now.Add(24 * time.Hour)
	third, _ := e.PlanFleet(roots, nil, pol)
	if len(third) != 0 {
		t.Fatalf("attacked the same drift a third time: %v", fleetKinds(third))
	}
}

// The record a clean run keeps is dropped the moment the root reads healthy,
// so drift that comes back later starts from a fresh budget.
func TestADriftThatClearsForgetsItsAttempts(t *testing.T) {
	e, now := fixed(t)
	pol := graftOnFleet(enrollOn())
	drift := []Root{federated("/git", []string{"new"}, nil)}

	first, _ := e.PlanFleet(drift, nil, pol)
	e.FleetDone(first[0], "")
	if acts, _ := e.PlanFleet([]Root{federated("/git", nil, nil)}, nil, pol); len(acts) != 0 {
		t.Fatalf("a healthy root planned %v", fleetKinds(acts))
	}

	*now = now.Add(time.Second)
	back, _ := e.PlanFleet(drift, nil, pol)
	if len(back) != 1 || back[0].Attempt != 1 {
		t.Fatalf("returning drift got %v, want attempt 1", back)
	}
}
