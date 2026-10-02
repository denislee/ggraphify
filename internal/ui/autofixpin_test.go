package ui

import (
	"strings"
	"testing"

	"github.com/dns/ggraphify/internal/autofix"
	"github.com/dns/ggraphify/internal/gfy"
	"github.com/dns/ggraphify/internal/heal"
	"github.com/dns/ggraphify/internal/store"
)

// The local fallback follows Heavy work, not the general Backend. A board whose
// Backend is an OpenCode plan with an ollama tag left in Model, and whose Heavy
// pin is a local server, used to fall back to ollama with the stale tag; the
// Heavy pin and its own model are what the user chose for extractions.
func TestAutoFixLocalPinFollowsHeavyWork(t *testing.T) {
	set := store.Defaults()
	set.Backend, set.Model = gfy.OpenCodeBackend, "qwen2.5-coder:7b"
	set.HeavyBackend, set.HeavyModel = gfy.OllamaBackend, "qwen3:14b"
	if b, m := autoFixLocalPin(set); b != gfy.OllamaBackend || m != "qwen3:14b" {
		t.Fatalf("local Heavy pin: got %s/%s, want ollama/qwen3:14b", b, m)
	}

	// A billed Heavy pin: its model is not an ollama id, so the fallback asks
	// the server for its own default.
	set.HeavyBackend, set.HeavyModel = gfy.ClaudeCLIBackend, "opus"
	if b, m := autoFixLocalPin(set); b != gfy.OllamaBackend || m != "" {
		t.Fatalf("billed Heavy pin: got %s/%s, want ollama with no model", b, m)
	}
}

// The reroute table. The row that was the bug: Heavy work on claude-cli,
// metered fixes on, a local server up — the extraction went to ollama anyway.
func TestAutoFixToLocalHonoursThePin(t *testing.T) {
	local := autofix.Action{Local: true}
	cases := []struct {
		name    string
		pinned  string
		act     autofix.Action
		metered bool
		want    bool
	}{
		{"billed pin, metered on", gfy.ClaudeCLIBackend, local, true, false},
		{"billed pin, metered off", gfy.ClaudeCLIBackend, local, false, true},
		{"local pin, metered off", gfy.OllamaBackend, local, false, false},
		{"billed pin, no local model", gfy.ClaudeCLIBackend, autofix.Action{}, true, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pol := autofix.Policy{Metered: c.metered, Local: c.act.Local}
			if got := autoFixToLocal(c.pinned, c.act, pol); got != c.want {
				t.Fatalf("autoFixToLocal(%s) = %v, want %v", c.pinned, got, c.want)
			}
		})
	}
}

// The log line names where the LLM steps actually went, and says METERED when
// one of them bills — it used to print the local model for every local policy.
func TestAutoFixPlanLineNamesTheRealBackend(t *testing.T) {
	act := autofix.Action{Local: true, Plan: heal.Plan{Steps: []heal.Step{{Kind: "extract"}}}}
	if !act.Plan.Metered() {
		t.Skip("extract is not metered in this build")
	}
	line := autoFixPlanLine(act, []string{gfy.ClaudeCLIBackend}, true)
	if !strings.Contains(line, gfy.ClaudeCLIBackend) || !strings.Contains(line, "METERED") {
		t.Fatalf("plan line %q does not name claude-cli as metered", line)
	}
	line = autoFixPlanLine(act, []string{"ollama/qwen3:14b"}, false)
	if !strings.Contains(line, "ollama/qwen3:14b") || !strings.Contains(line, "free") {
		t.Fatalf("plan line %q does not name the local model as free", line)
	}
}

// Following the work pins never invents a fallback: a billed pin with metered
// fixes off leaves the loop on the free plan, not on ollama.
func TestAutoFixFollowPinsHasNoFallback(t *testing.T) {
	set := store.Defaults()
	set.AutoFixFollowPins = true
	set.HeavyBackend, set.HeavyModel = gfy.ClaudeCLIBackend, "opus"
	set.LightBackend, set.LightModel = gfy.ClaudeCLIBackend, "haiku"

	if _, _, ok, why := autoFixPinsLocal(set); ok || !strings.Contains(why, "Heavy work") {
		t.Fatalf("billed Heavy pin: ok=%v why=%q, want not free and naming Heavy work", ok, why)
	}
	pol := autoFixPolicy(set)
	if !pol.Pins || pol.Local || pol.AllowMetered() {
		t.Fatalf("policy %+v: want Pins, no local fallback, no LLM steps", pol)
	}

	// Metered on: the pins themselves run, still with no reroute.
	set.AutoFixMetered = true
	pol = autoFixPolicy(set)
	if !pol.AllowMetered() || pol.Local {
		t.Fatalf("policy %+v: want the billed pins allowed and no local model", pol)
	}
	if autoFixToLocal(gfy.ClaudeCLIBackend, autofix.Action{Local: pol.Local}, pol) {
		t.Fatal("a followed billed pin was rerouted")
	}
}
