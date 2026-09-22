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
// describing it, and that is where this started: for a long time the board
// would build `--deep` for a local server and for nothing else.
//
// It now follows the Heavy work pin instead — the same pin `extract` follows,
// because these are the same job in two indexers: one request per file of the
// whole checkout. A pin that named a metered backend used to be refused; it is
// now honored, and the price is carried rather than hidden — CostFor reports
// the kind as Metered the moment it is not local, which is what puts it in the
// metered lane, behind the confirm that names the bill, and held on restore.
// Free is a claim about an endpoint, so it is made about the endpoint and not
// about the kind.
//
// The whole coupling is this file, the `graft-deep` arm of Argv, the weight
// table in argv.go, and the env line in jobs.SubmitCmd.

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

	// GraftRetriesVar is how many times graft re-sends a request its transport
	// gave up on; its own default is 4, so five attempts in all.
	GraftRetriesVar = "GRAFT_LLM_RETRIES"

	// GraftLocalRetries is what that budget is worth against a server on this
	// machine, and it is one.
	//
	// graft gives each request the OpenAI SDK's default ten-minute timeout and
	// exposes no knob to change it. A synthesis batch a local model cannot
	// finish inside ten minutes cannot finish inside the next ten either —
	// nothing about the machine changed — so the default budget spends forty
	// minutes arriving at the answer the first attempt already had. One retry
	// keeps the case retries exist for, an ollama that was reloading a model
	// or briefly wedged, and caps the rest.
	GraftLocalRetries = "1"

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

// GraftAnthropicProvider is graft's name for the Anthropic wire format, which
// is how the `claude` backend — the API one, not the CLI — is reached. graft
// speaks four formats in all (openai, anthropic, litellm, orcarouter); these
// two are the ones a graphify backend maps onto.
const GraftAnthropicProvider = "anthropic"

// GraftTarget is one of graphify's backends restated in graft's own terms: the
// wire format, the endpoint it is served on (blank = the provider's default),
// the variable its credential is read from, and whether the whole exchange
// stays on this machine.
//
// It exists because the deep pass now follows the Heavy pin wherever it
// points, and "where does that backend live" is then four questions rather
// than one — asked in Argv, in the readiness check, in the env overlay and in
// the confirm dialog, which must all get the same answer.
type GraftTarget struct {
	Backend  string
	Provider string
	BaseURL  string
	// KeyVar is the variable holding the credential. Its VALUE is copied into
	// GRAFT_API_KEY when the job is built; nothing in this board stores it.
	KeyVar string
	Local  bool
}

// GraftTargetFor resolves a backend for graft, or says in a sentence why graft
// cannot be pointed at it. Every refusal names the fix, because a fan-out of
// forty jobs that all die on the same missing key is a worse way to learn it.
//
// The OpenCode plans go through this board's own loopback proxy, for the
// reason opencodeproxy.go documents: the gateway requires a session header
// that neither graphify nor graft sends, and the proxy is what adds it.
func GraftTargetFor(backend string) (GraftTarget, string) {
	b := EffectiveBackend(backend)
	if b == "" {
		// Nothing detected and nothing pinned. The old behaviour, and still
		// the right one: a board with no credential anywhere means the local
		// server, or a sentence about the local server.
		b = OllamaBackend
	}
	switch {
	case IsLocalBackend(b):
		base := LocalBaseURL(b)
		if base == "" {
			return GraftTarget{}, "no local endpoint is configured for " + b +
				" — export " + OllamaBaseURLVar + " or " + OllamaHostVar + "."
		}
		return GraftTarget{Backend: b, Provider: GraftOpenAIProvider, BaseURL: base,
			KeyVar: OllamaKeyVar, Local: true}, ""

	case IsOpenCodeBackend(b):
		if !HasOpenCodeKey() {
			return GraftTarget{}, OpenCodeKeyVar + " is not set in this environment, so " +
				b + " has no credential to run graft's deep pass on."
		}
		base, err := StartOpenCodeProxy(b)
		if err != nil {
			return GraftTarget{}, err.Error()
		}
		return GraftTarget{Backend: b, Provider: GraftOpenAIProvider, BaseURL: base,
			KeyVar: OpenCodeKeyVar}, ""

	case b == ClaudeAPIBackend:
		if strings.TrimSpace(os.Getenv(AnthropicKeyVar)) == "" {
			return GraftTarget{}, AnthropicKeyVar + " is not set in this environment, so " +
				"the deep pass has no credential for " + b + "."
		}
		return GraftTarget{Backend: b, Provider: GraftAnthropicProvider,
			KeyVar: AnthropicKeyVar}, ""

	case b == OpenAIBackend:
		// Not local: IsLocalBackend took that arm above, so OPENAI_BASE_URL is
		// either unset or points at the vendor. graft's own default endpoint
		// is the right one, so no --base-url is passed.
		if strings.TrimSpace(os.Getenv(OpenAIKeyVar)) == "" {
			return GraftTarget{}, OpenAIKeyVar + " is not set in this environment, so " +
				"the deep pass has no credential for " + b + "."
		}
		return GraftTarget{Backend: b, Provider: GraftOpenAIProvider, KeyVar: OpenAIKeyVar}, ""
	}

	// gemini, kimi, deepseek, claude-cli. Three of them are behind wire
	// formats graft does not speak or endpoints this board does not know the
	// URL of, and claude-cli is a subscription reached by launching a binary,
	// which graft has no way to do at all. Guessing a base URL for any of them
	// would send the corpus somewhere nobody named.
	return GraftTarget{}, b + " is not a backend graft can be pointed at. Its deep pass " +
		"speaks the OpenAI and Anthropic wire formats, so Heavy work has to name " +
		OllamaBackend + ", a local OpenAI-compatible server, " + ClaudeAPIBackend + ", " +
		OpenAIBackend + ", or one of the two OpenCode plans."
}

// GraftProvider is graft's wire-format name for a backend, or "" for one graft
// has no way to talk to.
func GraftProvider(backend string) string {
	t, why := GraftTargetFor(backend)
	if why != "" {
		return ""
	}
	return t.Provider
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
	t, why := GraftTargetFor(backend)
	if why != "" {
		return "", "", false, why
	}
	if t.Local {
		// The local half keeps the answer LocalRun gives: the board's model
		// setting is a preference when the backend it was typed under is this
		// one, and the server's own default otherwise — a claude model id is
		// not a name ollama has ever heard.
		return LocalRun(t.Backend, model)
	}
	m = strings.TrimSpace(model)
	if m == "" {
		// Each OpenCode plan has a default worth naming; a vendor API does
		// not, and graft's own fallback is an id for some other catalogue.
		// Better to ask for one than to dispatch a run that 404s per file.
		m = DefaultModelFor(t.Backend)
	}
	if m == "" {
		return "", "", false, "Heavy work has no model: " + t.Backend + " needs one by name, " +
			"and graft's own default belongs to a different catalogue. Type one in the " +
			"Heavy work model field."
	}
	return t.Backend, m, true, ""
}

// GraftDeepParams fits a Params for the deep pass: the backend Heavy work
// names, a model that backend actually serves, and — on a local server — the
// concurrency its slots can hold.
//
// It refuses rather than falling back. A backend graft cannot be pointed at
// leaves the argv unbuildable, which SubmitCmd reports per repository, rather
// than being silently swapped for one that would run: a corpus sent somewhere
// nobody named is the one outcome this file exists to prevent.
func GraftDeepParams(p *Params) (ok bool, why string) {
	if p == nil {
		return false, "no parameters"
	}
	b, m, ok, why := GraftDeepReady(p.Backend, p.Model)
	if !ok {
		return false, why
	}
	p.Backend, p.Model, p.Deep = b, m, true
	if !IsLocalBackend(b) {
		// A metered gateway's ceiling is its own rate limit, not this
		// machine's slots, and LocalConcurrency would be measuring the wrong
		// server. Zero leaves graft's default of 5, and the partial-result
		// concession below is local-only for the same reason: it is there to
		// survive ollama's tool_choice gap, and a vendor endpoint that fails
		// has failed at something worth exiting 1 over.
		return true, ""
	}
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
	//
	// What it does NOT cover is the concept pass's second half. graft
	// summarizes every file, then synthesizes those summaries into the
	// concept map in batches of 48 KB — and that synthesis call is not behind
	// --allow-partial at all: it throws out of buildContext, and the flag is
	// only consulted afterwards, on the degraded-tier path it never reaches.
	// A batch that big is one ~12k-token prompt with a forced tool call, and
	// a 7B model on this machine does not finish one inside the OpenAI SDK's
	// fixed ten-minute request timeout, which graft exposes no knob for.
	// pulumi — 444 files, seven batches — therefore fails on batch 1 with
	// "Request timed out." after the summaries it just computed were all
	// cached successfully. So a local deep build of a large checkout is a
	// capacity limit rather than a misconfiguration, and this flag cannot
	// turn it green.
	p.AllowPartial = true
	return true, ""
}

// GraftDeepEnv puts the deep build's credential in the job's environment —
// the placeholder one against a local server, and the value of the backend's
// own key variable against a metered one — and leaves every other job
// untouched. The value is read here and handed to the child process; it is
// never written to the board's state file, which holds variable names only.
//
// Composed in jobs.SubmitCmd rather than at the call site, for the reason the
// other overlays are: ggraphify-job submits through that function too, and an
// overlay applied in the GUI only is an overlay a headless sweep runs without.
// An explicit value in the user's own overlay wins outright — a proxied server
// that does check the key is theirs to describe, not this board's to guess at.
func GraftDeepEnv(e Env, kind, backend string) Env {
	if kind != GraftDeepKind {
		return e
	}
	t, why := GraftTargetFor(backend)
	if why != "" {
		return e
	}
	c := e

	// The retry budget comes first, and independently of the credential: an
	// overlay that carries its own key is a proxied local server, which is
	// still a server on this machine and still has nothing to gain from four
	// ten-minute attempts at the same request. An explicit value — in the
	// overlay or exported into the session — is the user describing their own
	// endpoint, and wins.
	if t.Local {
		_, pinned := c[GraftRetriesVar]
		if !pinned && strings.TrimSpace(os.Getenv(GraftRetriesVar)) == "" {
			c = setVar(c, GraftRetriesVar, GraftLocalRetries)
		}
	}

	if _, ok := c[GraftKeyVar]; ok {
		return c
	}
	key := strings.TrimSpace(os.Getenv(t.KeyVar))
	if key == "" {
		if !t.Local {
			// GraftTargetFor already refused a metered backend with no
			// credential; reaching here means it was unset between that check
			// and this one. Leave the variable out and let graft say so.
			return c
		}
		key = GraftLocalKey
	}
	return setVar(c, GraftKeyVar, key)
}

// setVar returns e with name set to value, copied rather than mutated: the
// overlay handed in belongs to the caller, and a job's environment composed
// for one repository must not follow the next one.
func setVar(e Env, name, value string) Env {
	c := e.Clone()
	if c == nil {
		c = Env{}
	}
	c[name] = value
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
