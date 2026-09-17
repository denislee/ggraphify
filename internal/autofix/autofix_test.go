package autofix

import (
	"testing"
	"time"

	"github.com/dns/ggraphify/internal/graphstate"
)

// fixed makes an engine whose clock can be moved by hand, because every rule
// worth testing here is about time passing.
func fixed(t *testing.T) (*Engine, *time.Time) {
	t.Helper()
	now := time.Date(2026, 9, 15, 9, 0, 0, 0, time.UTC)
	e := New()
	e.now = func() time.Time { return now }
	return e, &now
}

func unbuilt(path, name string) Candidate {
	return Candidate{Path: path, Name: name, Graph: graphstate.Graph{State: graphstate.StateNone}}
}

func drifted(path, name string) Candidate {
	return Candidate{Path: path, Name: name, Graph: graphstate.Graph{
		State: graphstate.StateStale, Nodes: 100, Communities: 4, Labeled: true,
		HasReport: true, DriftChanged: 7,
	}}
}

func healthy(path, name string) Candidate {
	return Candidate{Path: path, Name: name, Graph: graphstate.Graph{
		State: graphstate.StateFresh, Nodes: 100, Communities: 4, Labeled: true, HasReport: true,
	}}
}

func on() Policy { return Policy{Enabled: true, Max: 8} }

func kinds(p Policy, a Action) []string {
	out := make([]string, 0, len(a.Plan.Steps))
	for _, s := range a.Plan.Steps {
		out = append(out, s.Kind)
	}
	return out
}

// Off is off. The one property this feature cannot be allowed to get wrong.
func TestDisabledDoesNothing(t *testing.T) {
	e, _ := fixed(t)
	acts, _ := e.Plan([]Candidate{unbuilt("/a", "a")}, Policy{})
	if len(acts) != 0 {
		t.Fatalf("a disabled policy planned %d actions", len(acts))
	}
}

// A healthy board is a quiet board.
func TestHealthyRepositoriesAreNotTouched(t *testing.T) {
	e, _ := fixed(t)
	acts, skips := e.Plan([]Candidate{healthy("/a", "a")}, on())
	if len(acts) != 0 || len(skips) != 0 {
		t.Fatalf("healthy repo produced %d actions, %d skips", len(acts), len(skips))
	}
}

// Without a local model and without permission to spend, the loop runs the
// free plan: an AST rebuild rather than an extraction. This is the default
// state of a machine with no ollama, and it must still do useful work.
func TestWithoutAnLLMTheLoopRunsTheFreePlan(t *testing.T) {
	e, _ := fixed(t)
	acts, _ := e.Plan([]Candidate{unbuilt("/a", "a")}, on())
	if len(acts) != 1 {
		t.Fatalf("got %d actions, want 1", len(acts))
	}
	if got := kinds(on(), acts[0]); got[0] != "update" {
		t.Fatalf("first step = %q, want the free rebuild", got[0])
	}
	if acts[0].Plan.Metered() {
		t.Fatal("a policy that may not spend planned a metered step")
	}
	if acts[0].Local {
		t.Fatal("action claims a local run with no local model configured")
	}
}

// With a local model the loop plans the full sequence — extraction and
// community naming — and marks it local, which is what tells the UI to point
// those steps at this machine instead of a billed backend.
func TestALocalModelUnlocksTheFullPlanForFree(t *testing.T) {
	e, _ := fixed(t)
	pol := on()
	pol.Local, pol.LocalBackend, pol.LocalModel = true, "ollama", "qwen2.5-coder:7b"

	acts, _ := e.Plan([]Candidate{unbuilt("/a", "a")}, pol)
	if len(acts) != 1 {
		t.Fatalf("got %d actions, want 1", len(acts))
	}
	got := kinds(pol, acts[0])
	if got[0] != "extract" {
		t.Fatalf("first step = %q, want the full extraction", got[0])
	}
	if !acts[0].Local {
		t.Fatal("the LLM steps must be marked as local runs")
	}
	if pol.Spends() {
		t.Fatal("a local policy must never report that it spends money")
	}
}

// The billed backend is reachable, but only through the switch, and turning it
// on is the only way Spends becomes true.
func TestOnlyTheMeteredSwitchSpends(t *testing.T) {
	pol := on()
	if pol.AllowMetered() || pol.Spends() {
		t.Fatal("the default policy must not allow or spend metered work")
	}
	pol.Metered = true
	if !pol.AllowMetered() || !pol.Spends() {
		t.Fatal("the metered switch must both allow LLM steps and admit it spends")
	}
	pol.Local = true
	if pol.Spends() {
		t.Fatal("with a local model in play the run is free, whatever the switch says")
	}
}

// Excluded and busy repositories are left alone — the X flag and a running job
// mean exactly this.
func TestExcludedAndBusyAreSkipped(t *testing.T) {
	e, _ := fixed(t)
	ex := unbuilt("/a", "a")
	ex.Excluded = true
	busy := unbuilt("/b", "b")
	busy.Busy = true

	acts, skips := e.Plan([]Candidate{ex, busy}, on())
	if len(acts) != 0 {
		t.Fatalf("planned %d actions over excluded/busy repositories", len(acts))
	}
	if len(skips) != 1 || skips[0].Path != "/b" {
		t.Fatalf("skips = %+v, want one line about the busy repository", skips)
	}
}

// Max bounds what goes out at once, and the repositories it did not reach are
// still there next tick.
func TestMaxInFlightIsRespectedAcrossTicks(t *testing.T) {
	e, _ := fixed(t)
	pol := on()
	pol.Max = 2
	cands := []Candidate{unbuilt("/a", "a"), unbuilt("/b", "b"), unbuilt("/c", "c")}

	acts, _ := e.Plan(cands, pol)
	if len(acts) != 2 {
		t.Fatalf("first tick planned %d, want 2", len(acts))
	}
	// Nothing has finished, so the second tick adds nothing.
	if more, _ := e.Plan(cands, pol); len(more) != 0 {
		t.Fatalf("second tick planned %d while two were in flight", len(more))
	}
	if e.Running() != 2 {
		t.Fatalf("Running = %d, want 2", e.Running())
	}
	// One finishes and its repository is now healthy; the third gets the slot.
	e.Done(acts[0], "")
	cands[0] = healthy("/a", "a")
	third, _ := e.Plan(cands, pol)
	if len(third) != 1 || third[0].Path != "/c" {
		t.Fatalf("third tick = %+v, want /c", third)
	}
}

// The same repository is not hammered: after an attempt it cools down.
func TestCooldownHoldsTheSameRepositoryBack(t *testing.T) {
	e, now := fixed(t)
	pol := on()
	pol.Cooldown = 10 * time.Minute
	c := []Candidate{unbuilt("/a", "a")}

	acts, _ := e.Plan(c, pol)
	e.Done(acts[0], "graphify failed")

	*now = now.Add(time.Minute)
	if again, skips := e.Plan(c, pol); len(again) != 0 {
		t.Fatalf("retried after a minute: %+v (%+v)", again, skips)
	}
	// The backoff grows with attempts, so the plain cooldown is not enough.
	*now = now.Add(30 * time.Minute)
	if again, _ := e.Plan(c, pol); len(again) != 1 {
		t.Fatal("never retried after the backoff elapsed")
	}
}

// The rule that keeps an unattended loop from running all night: the same
// defects, surviving the same fix, are not attacked forever.
func TestTheLoopGivesUpOnADefectItCannotClear(t *testing.T) {
	e, now := fixed(t)
	pol := on()
	pol.Attempts = 2
	pol.Cooldown = time.Minute
	c := []Candidate{drifted("/a", "a")}

	for i := 0; i < pol.Attempts; i++ {
		acts, _ := e.Plan(c, pol)
		if len(acts) != 1 {
			t.Fatalf("attempt %d planned %d actions", i+1, len(acts))
		}
		e.Done(acts[0], "")
		*now = now.Add(time.Hour)
	}

	acts, skips := e.Plan(c, pol)
	if len(acts) != 0 {
		t.Fatalf("kept trying after %d attempts", pol.Attempts)
	}
	if len(skips) != 1 || skips[0].Why == "" {
		t.Fatalf("skips = %+v, want one that says it gave up", skips)
	}

	// And it says so exactly once, however many ticks go by.
	if stuck := e.Declare(c, pol); len(stuck) != 1 || stuck[0].Name != "a" {
		t.Fatalf("Declare = %+v, want one report", stuck)
	}
	if stuck := e.Declare(c, pol); len(stuck) != 0 {
		t.Fatalf("Declare repeated itself: %+v", stuck)
	}
}

// A repository that is making progress is not mistaken for a stuck one: each
// new set of defects gets its own budget of attempts. Without this, any repair
// that takes more stages than Attempts would be abandoned halfway.
func TestProgressResetsTheAttemptBudget(t *testing.T) {
	e, now := fixed(t)
	pol := on()
	pol.Attempts = 1
	pol.Cooldown = time.Minute

	c := []Candidate{unbuilt("/a", "a")}
	acts, _ := e.Plan(c, pol)
	e.Done(acts[0], "")
	*now = now.Add(time.Hour)

	// It built a graph; now the communities have no names. Different defect.
	c[0] = Candidate{Path: "/a", Name: "a", Graph: graphstate.Graph{
		State: graphstate.StateRaw, Nodes: 100, Communities: 4,
	}}
	again, skips := e.Plan(c, pol)
	if len(again) != 1 {
		t.Fatalf("a repository that moved on was refused: %+v", skips)
	}
	if again[0].Attempt != 1 {
		t.Fatalf("attempt = %d, want a fresh count", again[0].Attempt)
	}
}

// Retry and Reset are the two escape hatches: one repository, or the whole
// memory, after the user has changed something the loop could not.
func TestRetryAndResetClearTheGiveUp(t *testing.T) {
	e, now := fixed(t)
	pol := on()
	pol.Attempts = 1
	pol.Cooldown = time.Minute
	c := []Candidate{drifted("/a", "a")}

	acts, _ := e.Plan(c, pol)
	e.Done(acts[0], "")
	*now = now.Add(time.Hour)
	if a2, _ := e.Plan(c, pol); len(a2) != 0 {
		t.Fatal("should have given up")
	}

	e.Retry("/a")
	if a3, _ := e.Plan(c, pol); len(a3) != 1 {
		t.Fatal("Retry did not clear the give-up")
	}
	e.Reset()
	if e.Running() != 0 {
		t.Fatalf("Running = %d after Reset", e.Running())
	}
	if a4, _ := e.Plan(c, pol); len(a4) != 1 {
		t.Fatal("Reset did not clear the in-flight marker")
	}
}

// The signature is the issue codes, and it is what "the same defect" means.
func TestSigTracksTheIssueCodes(t *testing.T) {
	if s := Sig(healthy("/a", "a").Graph); s != "" {
		t.Fatalf("a healthy graph has signature %q, want empty", s)
	}
	if s := Sig(unbuilt("/a", "a").Graph); s != graphstate.IssueNoGraph {
		t.Fatalf("Sig = %q, want %q", s, graphstate.IssueNoGraph)
	}
}

// A zero policy from a state file that predates these settings still runs with
// sane bounds rather than with zeroes.
func TestZeroPolicyGetsTheDefaults(t *testing.T) {
	p := Policy{Enabled: true}.withDefaults()
	if p.Max != DefaultMax || p.Cooldown != DefaultCooldown || p.Attempts != DefaultAttempts {
		t.Fatalf("withDefaults = %+v", p)
	}
}
