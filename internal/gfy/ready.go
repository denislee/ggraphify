package gfy

import "strings"

// BackendChoice is one backend with this machine's verdict on it: can a
// metered job actually run against it right now, and why not when it cannot.
//
// It exists because "your pinned backend is dead" is only half an answer. The
// other half is what to do instead, and that half is knowable — the board
// already probes every one of these for the confirm dialog. Offering the
// alternatives is not the same as taking one: choosing a backend is choosing
// who pays for an extraction, so nothing here ever switches a setting. It
// hands a list to a human.
type BackendChoice struct {
	// Name is the value that goes into settings: "" for auto-detect,
	// otherwise a member of Backends.
	Name string
	// Label is the one-line description, the same phrasing the settings
	// dropdown uses.
	Label string
	// Ready is whether a job dispatched to it now would have what it needs.
	Ready bool
	// Why is the readiness verdict in full, ready or not.
	Why string
	// Local marks a backend that spends time rather than money.
	Local bool
}

// BackendChoices is every backend worth offering on this machine, with each
// one probed.
//
// Deliberately not all of gfy.Backends: a vendor backend whose API key is not
// exported is not an alternative, it is a second thing to go and set up, and a
// list of six of those buries the one that would actually work. What is
// offered is auto-detect (when something is detectable), the Claude Code CLI
// (when installed), OpenCode Go (when its key is exported) and a local model
// server (when one answers) — the four that need nothing bought.
//
// model is the board's model setting, which matters to two of the verdicts:
// OpenCode Go's model has to exist on the plan, and a local server has to have
// pulled the one being asked for.
//
// It probes ports and reads the filesystem, so it belongs off a UI thread.
func BackendChoices(model string) []BackendChoice {
	var out []BackendChoice
	add := func(name, label string) {
		c := BackendChoice{Name: name, Label: label, Local: IsLocalBackend(name)}
		switch {
		case name == OpenCodeBackend:
			c.Ready, c.Why = OpenCodeReady(model)
		case IsLocalBackend(name):
			c.Ready, c.Why = LocalReady(name, model)
		default:
			c.Ready, c.Why = BackendReady(name)
		}
		out = append(out, c)
	}

	if eff := EffectiveBackend(""); eff != "" {
		add("", "auto-detect (→ "+eff+")")
	}
	if HasClaudeCLI() {
		add(ClaudeCLIBackend, ClaudeCLIBackend+" — Claude Code on this machine, billed to that login's plan")
	}
	if HasOpenCodeKey() {
		add(OpenCodeBackend, OpenCodeBackend+" — the OpenCode Go subscription ("+OpenCodeKeyVar+")")
	}
	if p := ProbeLocal(OllamaBackend); p.Reach {
		add(OllamaBackend, OllamaBackend+" — a model on this machine: free, slower, no key")
	}
	return out
}

// ReadyBackends is BackendChoices narrowed to the ones that would work,
// without the one already chosen — the answer to "so what should I use
// instead".
func ReadyBackends(except, model string) []BackendChoice {
	except = strings.TrimSpace(except)
	var out []BackendChoice
	for _, c := range BackendChoices(model) {
		if !c.Ready || c.Name == except {
			continue
		}
		out = append(out, c)
	}
	return out
}

// BackendVerdict is the readiness of the backend a job would actually use,
// resolved and probed the same way the confirm dialog resolves it — including
// the two backends whose readiness depends on the model as well as on a
// credential.
//
// Returned alongside is the effective backend name, because a blank setting
// resolves to something and a verdict about "" would be unreadable.
func BackendVerdict(backend, model string) (eff string, ready bool, why string) {
	eff = EffectiveBackend(backend)
	switch {
	case eff == OpenCodeBackend:
		ready, why = OpenCodeReady(model)
	case IsLocalBackend(eff):
		ready, why = LocalReady(eff, model)
	default:
		ready, why = BackendReady(eff)
	}
	return eff, ready, why
}
