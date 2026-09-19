package gfy

import (
	"os"
	"strings"
)

// graft's second tier — the one that costs something.
//
// `graft build` is tree-sitter only: a wiring graph and per-file cards, no key
// and no network. `graft build --deep` adds the tier an agent actually reads
// as prose — a concept map in graft/*.md plus a summary and an 8-line crux per
// symbol — and every line of it comes out of an LLM. That is why the board
// shipped without it: a GUI that dispatches metered requests has to describe
// the bill first, and graft's deep pass has no --no-label half to offer.
//
// A model already running on this machine removes the bill rather than
// describing it. graft speaks the OpenAI wire format against any base URL, and
// ollama serves that format on /v1 — so the deep pass against the local server
// is the same free-lane job the rest of the board's local work is, and it is
// wired here on exactly that condition: ggraphify runs `--deep` against a
// local model, and against nothing else. A vendor key is still reachable from
// a terminal, where the person typing it has said what they meant.
//
// The whole coupling is this file, the `graft-deep` arm of Argv, and the env
// line in jobs.SubmitCmd.

const (
	// GraftDeepKind is the job kind. It is a constant rather than a literal
	// because three packages test for it.
	GraftDeepKind = "graft-deep"

	// GraftProviderVar and friends are graft's own environment variables, the
	// flags' equivalents. Only the key travels this way: a provider, a base
	// URL and a model belong in the argv the confirm dialog prints, and a
	// credential — even a placeholder one — does not.
	GraftProviderVar = "GRAFT_PROVIDER"
	GraftBaseURLVar  = "GRAFT_BASE_URL"
	GraftModelVar    = "GRAFT_MODEL"
	GraftKeyVar      = "GRAFT_API_KEY"

	// GraftOpenAIProvider is graft's name for the OpenAI wire format, which is
	// what ollama's /v1 endpoint and llama-server, vLLM and LM Studio all
	// speak. graft has no "ollama" provider of its own; this is how a local
	// server is reached.
	GraftOpenAIProvider = "openai"

	// GraftLocalKey is the placeholder credential. A local server authenticates
	// nothing, but graft refuses to dispatch with no key at all, and a refusal
	// that reads "no API key" on a machine that needs none is a bad half-hour
	// for whoever hits it. OLLAMA_API_KEY wins when the server is one of the
	// setups that does check (a proxied or tailnet-fronted ollama).
	GraftLocalKey = "local"
)

// GraftProvider is graft's wire-format name for one of graphify's backends, or
// "" for a backend graft has no way to talk to.
func GraftProvider(backend string) string {
	switch strings.TrimSpace(backend) {
	case OllamaBackend, OpenAIBackend:
		return GraftOpenAIProvider
	}
	return ""
}

// LocalRun resolves the backend and model an unattended local run should use,
// given whatever the board's settings say.
//
// It is the rule autoFixPolicy already applies, lifted out so the graft deep
// pass applies the same one: when the board's backend is itself local, its
// model setting is the preference; when it is claude-cli or a vendor API, that
// setting names a model the local server has never heard of, so the run asks
// for the server's own default instead. It never starts a server and never
// pulls a model — a machine with neither gets a refusal with the fix in it.
func LocalRun(backend, model string) (b, m string, ok bool, why string) {
	eff := EffectiveBackend(backend)
	b, preferred := eff, model
	if !IsLocalBackend(eff) {
		b, preferred = OllamaBackend, ""
	}
	m, ok, why = AutoLocalModel(b, preferred)
	if !ok {
		return "", "", false, why
	}
	return b, m, true, ""
}

// GraftDeepReady answers the one question the deep sweep's dialog asks before
// it queues anything: can this machine run graft's LLM pass for free, and on
// what. Every failure comes back as a sentence with the fix in it, because a
// fan-out of forty jobs that all die on the same missing model is a worse way
// to learn any of them.
func GraftDeepReady(backend, model string) (b, m string, ok bool, why string) {
	if ok, why := GraftReady(); !ok {
		return "", "", false, why
	}
	b, m, ok, why = LocalRun(backend, model)
	if !ok {
		return "", "", false, why
	}
	if GraftProvider(b) == "" {
		return "", "", false, b + " is not a server graft can be pointed at; " +
			"the deep pass needs an OpenAI-compatible endpoint."
	}
	if LocalBaseURL(b) == "" {
		return "", "", false, "no local endpoint is configured for " + b +
			" — export " + OllamaBaseURLVar + " or " + OllamaHostVar + "."
	}
	return b, m, true, ""
}

// GraftDeepParams fits a Params for the deep pass: a local backend, a model
// that server actually serves, and the concurrency its slots can hold.
//
// It refuses rather than falling back. A deep build submitted with a metered
// backend still in p.Backend would spend real money from a button labelled
// free, which is the one outcome this whole file exists to prevent.
func GraftDeepParams(p *Params) (ok bool, why string) {
	if p == nil {
		return false, "no parameters"
	}
	b, m, ok, why := GraftDeepReady(p.Backend, p.Model)
	if !ok {
		return false, why
	}
	p.Backend, p.Model, p.Deep = b, m, true
	// graft summarizes files in parallel with -j, and the ceiling is the same
	// one graphify's chunks have: the server's own slot count. Asking for more
	// than that does not make it faster — the surplus queues inside ollama,
	// each queued request holding its share of the K/V cache. Zero leaves
	// graft's default of 5 alone, which is right for a server this board could
	// not measure.
	p.MaxConcurrency = LocalConcurrency(b)
	// And accept a partial meaning tier, because against a local model partial
	// is the ordinary outcome rather than the broken one.
	//
	// graft's deep pass has two halves. The concept map is plain JSON and a
	// 7B coder model produces it happily. The per-symbol summary and crux go
	// through a tool call with tool_choice pinned — and ollama's OpenAI
	// endpoint does not honor tool_choice: it hands the model's tool call back
	// as message content, graft cannot parse it, and the whole run exits 1
	// having thrown away the concept map it did build. Which half a given
	// model manages is a property of that model's template and of ollama's
	// parser, not of anything this board can fix.
	//
	// So the flag is set here rather than offered as a checkbox: without it
	// the board's deep sweep is a row of red failures on the one machine it
	// was written for. Nothing is lost by it — graft caches what it computed,
	// and the next run resumes from there, so a better model later fills the
	// tier in rather than starting over.
	p.AllowPartial = true
	return true, ""
}

// GraftDeepEnv puts the placeholder credential in the job's environment when
// the job is a local deep build, and leaves every other job untouched.
//
// Composed in jobs.SubmitCmd rather than at the call site, for the reason the
// other overlays are: ggraphify-job submits through that function too, and an
// overlay applied in the GUI only is an overlay a headless sweep runs without.
// An explicit value in the user's own overlay wins outright — a proxied server
// that does check the key is theirs to describe, not this board's to guess at.
func GraftDeepEnv(e Env, kind, backend string) Env {
	if kind != GraftDeepKind || !IsLocalBackend(backend) {
		return e
	}
	if _, ok := e[GraftKeyVar]; ok {
		return e
	}
	c := e.Clone()
	if c == nil {
		c = Env{}
	}
	key := strings.TrimSpace(os.Getenv(OllamaKeyVar))
	if key == "" {
		key = GraftLocalKey
	}
	c[GraftKeyVar] = key
	return c
}

// ArgvOllama reports whether an argv will send its requests to this machine's
// ollama server — either as graphify's `--backend ollama`, or as a graft build
// pointed with `--base-url` at the endpoint ollama serves.
//
// The lease that limits how many jobs share one local server is taken on this
// answer, so the two shapes have to give the same one: a graft deep build and
// a graphify extraction queue at the same slots, and a board that leased only
// the second would run them both at once against a K/V cache sized for one.
func ArgvOllama(argv []string) bool {
	if ArgvBackend(argv) == OllamaBackend {
		return true
	}
	base := argvValue(argv, "--base-url")
	return base != "" && sameEndpoint(base, OllamaBaseURL())
}

// argvValue reads a `--name value` or `--name=value` pair out of an argv.
func argvValue(argv []string, name string) string {
	for i, a := range argv {
		if a == name && i+1 < len(argv) {
			return argv[i+1]
		}
		if v, ok := strings.CutPrefix(a, name+"="); ok {
			return v
		}
	}
	return ""
}

// sameEndpoint compares two base URLs the way a server would: trailing slashes
// and a /v1 suffix are not what distinguishes one endpoint from another.
func sameEndpoint(a, b string) bool {
	norm := func(s string) string {
		s = strings.TrimSpace(strings.ToLower(s))
		s = strings.TrimSuffix(s, "/")
		s = strings.TrimSuffix(s, "/v1")
		return strings.TrimSuffix(s, "/")
	}
	a, b = norm(a), norm(b)
	return a != "" && a == b
}
