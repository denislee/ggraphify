package autofix

import (
	"testing"
	"time"

	"github.com/dns/ggraphify/internal/graphstate"
)

// The README's cost-safety rule is that metered work is always a decision,
// never a side effect. These tests pin the ways the unattended loop used to
// turn one decision into many.

func brokenGraph(path, name string) Candidate {
	return Candidate{Path: path, Name: name, Graph: graphstate.Graph{State: graphstate.StateBroken}}
}

func metered() Policy { p := on(); p.Metered = true; return p }

// W3: a graph flipping between no graph and a broken one changes signature on
// every tick. The signature change reset the attempts AND skipped the
// cooldown, so with Metered on every tick queued another billed extraction —
// ten back-to-back in ten ticks, each attempt 1.
func TestAGraphFlippingBetweenNoGraphAndBrokenIsNotExtractedEveryTick(t *testing.T) {
	e, now := fixed(t)
	pol := metered()

	billed := 0
	for i := 0; i < 10; i++ {
		c := unbuilt("/a", "a")
		if i%2 == 1 {
			c = brokenGraph("/a", "a")
		}
		acts, _ := e.Plan([]Candidate{c}, pol)
		for _, a := range acts {
			if a.Plan.Metered() {
				billed++
			}
			e.Done(a, "killed")
		}
		*now = now.Add(30 * time.Second)
	}
	if billed != 1 {
		t.Fatalf("%d billed plans in 10 ticks of a flipping graph, want 1", billed)
	}
}

// W3: a healthy read inside the cooldown is not a fresh start. Forgetting the
// record at once gave the next defect a record with no last attempt, and so
// no cooldown at all.
func TestAHealthyReadInsideTheCooldownIsNotAFreshStart(t *testing.T) {
	e, now := fixed(t)
	pol := metered()

	acts, _ := e.Plan([]Candidate{unbuilt("/a", "a")}, pol)
	if len(acts) != 1 {
		t.Fatalf("first tick planned %d actions, want 1", len(acts))
	}
	e.Done(acts[0], "")

	*now = now.Add(30 * time.Second)
	if acts, _ := e.Plan([]Candidate{healthy("/a", "a")}, pol); len(acts) != 0 {
		t.Fatalf("a healthy repository planned %d actions", len(acts))
	}
	*now = now.Add(30 * time.Second)
	acts, skips := e.Plan([]Candidate{unbuilt("/a", "a")}, pol)
	if len(acts) != 0 {
		t.Fatalf("re-planned a minute after the last chain: %+v", acts)
	}
	if len(skips) != 1 || skips[0].Why != "cooling down" {
		t.Fatalf("skips = %+v, want one cooling down", skips)
	}

	// Past the cooldown the healthy read does forget it.
	*now = now.Add(DefaultCooldown)
	e.Plan([]Candidate{healthy("/a", "a")}, pol)
	acts, _ = e.Plan([]Candidate{unbuilt("/a", "a")}, pol)
	if len(acts) != 1 || acts[0].Attempt != 1 {
		t.Fatalf("after the cooldown got %+v, want a fresh attempt 1", acts)
	}
}

// W3: a repository whose defects keep changing, slowly enough to clear the
// cooldown each time, still cannot spend without bound — the billed runs per
// repository are capped over a rolling day, and the free part of the plan
// still runs once the cap is hit.
func TestBilledRunsAreCappedPerRepositoryPerDay(t *testing.T) {
	e, now := fixed(t)
	pol := metered()

	billed, free := 0, 0
	for i := 0; i < 20; i++ {
		c := unbuilt("/a", "a")
		if i%2 == 1 {
			c = brokenGraph("/a", "a")
		}
		acts, _ := e.Plan([]Candidate{c}, pol)
		for _, a := range acts {
			if a.Plan.Metered() {
				billed++
			} else {
				free++
			}
			e.Done(a, "")
		}
		*now = now.Add(DefaultCooldown + time.Second)
	}
	if billed != DefaultMeteredPerDay {
		t.Fatalf("%d billed runs inside a day, want the cap of %d", billed, DefaultMeteredPerDay)
	}
	if free == 0 {
		t.Fatal("the cap stopped the free fallback as well")
	}

	// The window rolls.
	*now = now.Add(MeteredWindow)
	acts, _ := e.Plan([]Candidate{unbuilt("/a", "a")}, pol)
	if len(acts) != 1 || !acts[0].Plan.Metered() {
		t.Fatalf("after the window rolled got %+v, want a billed plan again", acts)
	}

	// Settings changes and Retry do not refill it.
	acts2 := acts
	e.Done(acts2[0], "")
	for i := 1; i < DefaultMeteredPerDay; i++ {
		e.Retry("/a")
		a, _ := e.Plan([]Candidate{unbuilt("/a", "a")}, pol)
		e.Done(a[0], "")
	}
	e.Reset()
	e.Retry("/a")
	acts, _ = e.Plan([]Candidate{unbuilt("/a", "a")}, pol)
	if len(acts) == 1 && acts[0].Plan.Metered() {
		t.Fatal("Retry/Reset refilled the billed budget")
	}
}

// The cap is on spending: a local model bills nobody and is not capped.
func TestALocalModelIsNotMeteredCapped(t *testing.T) {
	e, _ := fixed(t)
	pol := on()
	pol.Local, pol.LocalBackend, pol.LocalModel = true, "ollama", "qwen2.5-coder:7b"
	for i := 0; i < DefaultMeteredPerDay+2; i++ {
		e.Retry("/a")
		acts, _ := e.Plan([]Candidate{unbuilt("/a", "a")}, pol)
		if len(acts) != 1 || !acts[0].Plan.Metered() {
			t.Fatalf("local run %d got %+v, want the full plan", i+1, acts)
		}
		e.Done(acts[0], "")
	}
}

// W3, fleet half: the workspace signature carries the drift's Summary, so a
// root whose children keep changing got a whole-root `graft build` per tick.
func TestAFlippingWorkspaceIsNotRebuiltEveryTick(t *testing.T) {
	e, now := fixed(t)
	pol := graftOnFleet(enrollOn())

	runs := 0
	for i := 0; i < 10; i++ {
		missing := []string{"a", "b"}
		if i%2 == 1 {
			missing = []string{"b"}
		}
		acts, _ := e.PlanFleet([]Root{federated("/git", missing, nil)}, nil, pol)
		for _, a := range acts {
			runs++
			e.FleetDone(a, "")
		}
		*now = now.Add(30 * time.Second)
	}
	if runs != 1 {
		t.Fatalf("%d whole-root rebuilds in 10 ticks, want 1", runs)
	}
}

// W36: Reset runs on every settings change. Dropping a running record freed
// a slot that was still taken — Done for it became a no-op — so the next tick
// could queue Max more chains on top of the ones still running.
func TestResetKeepsARunningChainsSlot(t *testing.T) {
	e, _ := fixed(t)
	pol := on()
	pol.Max = 1

	acts, _ := e.Plan([]Candidate{drifted("/a", "a")}, pol)
	if len(acts) != 1 {
		t.Fatalf("first tick planned %d actions, want 1", len(acts))
	}
	e.Reset()
	if got := e.Running(); got != 1 {
		t.Fatalf("Running = %d after Reset, want the in-flight chain still counted", got)
	}
	if more, _ := e.Plan([]Candidate{drifted("/b", "b")}, pol); len(more) != 0 {
		t.Fatalf("Reset let a second chain past Max=1: %+v", more)
	}
	e.Done(acts[0], "")
	if got := e.Running(); got != 0 {
		t.Fatalf("Running = %d after Done, want 0", got)
	}
}

// W36: the same for Retry on a repository whose chain is still running.
func TestRetryKeepsARunningChainsSlot(t *testing.T) {
	e, _ := fixed(t)
	pol := on()
	pol.Max = 1

	acts, _ := e.Plan([]Candidate{drifted("/a", "a")}, pol)
	e.Retry("/a")
	if got := e.Running(); got != 1 {
		t.Fatalf("Running = %d after Retry, want the in-flight chain still counted", got)
	}
	if more, _ := e.Plan([]Candidate{drifted("/a", "a"), drifted("/b", "b")}, pol); len(more) != 0 {
		t.Fatalf("Retry let a second chain past Max=1: %+v", more)
	}
	e.Done(acts[0], "")
	if got := e.Running(); got != 0 {
		t.Fatalf("Running = %d after Done, want 0", got)
	}
}
