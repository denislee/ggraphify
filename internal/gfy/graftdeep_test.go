package gfy

import (
	"strings"
	"testing"
)

// The deep pass is the one graft command that dispatches LLM requests. It
// follows the Heavy work pin wherever it points, so its safety is no longer
// "local or nothing" but "named or nothing": a backend graft cannot be pointed
// at builds no argv, and a backend that is not this machine's is priced as
// what it is. These tests pin both, and the two places a mistake would be
// invisible: an argv with no --base-url, and a lease not taken.

func TestArgvGraftDeepNamesTheLocalServer(t *testing.T) {
	t.Setenv(OllamaBaseURLVar, "http://127.0.0.1:11434/v1")

	argv := Argv(GraftDeepKind, Params{
		Repo: "/tmp/repo", Backend: OllamaBackend, Model: "qwen2.5-coder:14b",
		MaxConcurrency: 3, AllowPartial: true,
	})
	if argv == nil {
		t.Fatal("no argv for a local deep build")
	}
	got := strings.Join(argv[1:], " ")
	want := "--provider openai --base-url http://127.0.0.1:11434/v1 --model qwen2.5-coder:14b " +
		"build --deep /tmp/repo -j 3 --allow-partial"
	if got != want {
		t.Fatalf("argv:\n got %q\nwant %q", got, want)
	}
	// graft's global flags are global: they must precede the subcommand, or
	// commander rejects them outright.
	if i, j := indexOf(argv, "--provider"), indexOf(argv, "build"); i > j {
		t.Fatalf("--provider at %d comes after `build` at %d", i, j)
	}
}

// The backends graft has no way to talk to. Three are behind wire formats it
// does not speak or endpoints this board does not know the URL of; claude-cli
// is a subscription reached by launching a binary. Guessing for any of them
// would send the corpus somewhere nobody named.
func TestArgvGraftDeepRefusesABackendGraftCannotReach(t *testing.T) {
	for _, backend := range []string{"gemini", "kimi", "deepseek", ClaudeCLIBackend} {
		if argv := Argv(GraftDeepKind, Params{Repo: "/tmp/repo", Backend: backend, Model: "m"}); argv != nil {
			t.Fatalf("backend %q built a deep argv: %v", backend, argv)
		}
	}
}

// A metered pin is honored rather than refused — and named as itself: the
// Anthropic wire format, no --base-url, because the vendor's own endpoint is
// graft's default and restating a default is what breaks on the release that
// moves it.
func TestArgvGraftDeepFollowsAMeteredPin(t *testing.T) {
	t.Setenv(AnthropicKeyVar, "sk-test")

	argv := Argv(GraftDeepKind, Params{
		Repo: "/tmp/repo", Backend: ClaudeAPIBackend, Model: "claude-opus-5",
	})
	if argv == nil {
		t.Fatal("no argv for a metered deep build")
	}
	got := strings.Join(argv[1:], " ")
	want := "--provider anthropic --model claude-opus-5 build --deep /tmp/repo"
	if got != want {
		t.Fatalf("argv:\n got %q\nwant %q", got, want)
	}
}

// No key is not a refusal to spend money — it is a run that would fail on
// every file for the same reason, which is worth saying once and before.
func TestGraftDeepRefusesAMeteredPinWithNoKey(t *testing.T) {
	t.Setenv(AnthropicKeyVar, "")
	if _, why := GraftTargetFor(ClaudeAPIBackend); why == "" {
		t.Fatal("a keyless claude pin resolved")
	}
	if argv := Argv(GraftDeepKind, Params{Repo: "/tmp/repo", Backend: ClaudeAPIBackend, Model: "m"}); argv != nil {
		t.Fatalf("a keyless claude pin built %v", argv)
	}
}

func TestGraftDeepEnvSuppliesThePlaceholderKey(t *testing.T) {
	t.Setenv(OllamaKeyVar, "") // an exported key is used ahead of the placeholder
	e := GraftDeepEnv(Env{}, GraftDeepKind, OllamaBackend)
	if e[GraftKeyVar] != GraftLocalKey {
		t.Fatalf("key = %q, want %q", e[GraftKeyVar], GraftLocalKey)
	}
	// A key the user set themselves is theirs — a proxied ollama that does
	// check one would otherwise be overwritten with a placeholder.
	e = GraftDeepEnv(Env{GraftKeyVar: "mine"}, GraftDeepKind, OllamaBackend)
	if e[GraftKeyVar] != "mine" {
		t.Fatalf("overlay key overwritten: %q", e[GraftKeyVar])
	}
	// Every other job is untouched, including the free graft build.
	if e := GraftDeepEnv(Env{}, "graft-build", OllamaBackend); len(e) != 0 {
		t.Fatalf("graft-build got %v", e)
	}
	if e := GraftDeepEnv(Env{}, GraftDeepKind, "anthropic"); len(e) != 0 {
		t.Fatalf("a metered backend got %v", e)
	}
}

func TestArgvOllamaSeesBothShapes(t *testing.T) {
	t.Setenv(OllamaBaseURLVar, "http://127.0.0.1:11434/v1")

	if !ArgvOllama([]string{"graphify", "extract", "/r", "--backend", "ollama"}) {
		t.Error("graphify --backend ollama not recognized")
	}
	if !ArgvOllama([]string{"graft", "--base-url", "http://127.0.0.1:11434", "build", "--deep", "/r"}) {
		t.Error("graft pointed at the ollama endpoint not recognized")
	}
	if ArgvOllama([]string{"graft", "--base-url", "http://10.0.0.9:8000/v1", "build", "--deep", "/r"}) {
		t.Error("another server's endpoint claimed the ollama lease")
	}
	if ArgvOllama([]string{"graft", "build", "/r"}) {
		t.Error("the free wiring build took the lease")
	}
}

// A local deep run must never exit 1 over a meaning tier the local model could
// not fill — the concept map it DID build is the point of the run.
func TestGraftDeepParamsAcceptsAPartialMeaningTier(t *testing.T) {
	t.Setenv(OllamaBaseURLVar, "http://127.0.0.1:11434/v1")
	if !ProbeLocal(OllamaBackend).Reach {
		t.Skip("no local model server on this machine")
	}
	p := Params{Repo: "/tmp/repo"}
	if ok, why := GraftDeepParams(&p); !ok {
		t.Skip("no local model to resolve: " + why)
	}
	if !p.AllowPartial {
		t.Error("AllowPartial not set — a local deep run fails on an unfillable crux tier")
	}
	if !IsLocalBackend(p.Backend) {
		t.Errorf("backend %q is not local", p.Backend)
	}
}

// Free is a claim about an endpoint, so the kind alone cannot make it. Known
// says Metered — the safe answer for a backend nobody has resolved yet — and
// only CostFor, which has the backend in hand, is allowed to say otherwise.
func TestGraftDeepIsFreeOnlyWhereItIsLocal(t *testing.T) {
	t.Setenv(OllamaBaseURLVar, "http://127.0.0.1:11434/v1")

	if CostOf(GraftDeepKind) != Metered {
		t.Fatal("the deep pass must default to Metered for an unresolved backend")
	}
	if got := CostFor(GraftDeepKind, OllamaBackend); got != Free {
		t.Fatalf("local deep pass costs %v, want free", got)
	}
	if got := CostFor(GraftDeepKind, ClaudeAPIBackend); got != Metered {
		t.Fatalf("claude deep pass costs %v, want metered", got)
	}
	if got := CostForLocal(GraftDeepKind, true); got != Free {
		t.Fatalf("restored local deep pass costs %v, want free", got)
	}
	// Nothing else changes price with its backend.
	if got := CostFor("extract", OllamaBackend); got != Metered {
		t.Fatalf("extract on ollama costs %v, want metered — its price is not the board's to discount", got)
	}
	if !Graft(GraftDeepKind) {
		t.Fatal("graft-deep must resolve the graft binary, not graphify's")
	}
}

func indexOf(ss []string, want string) int {
	for i, s := range ss {
		if s == want {
			return i
		}
	}
	return -1
}

// Job 203: `graft build --deep` on pulumi spent 42 minutes re-sending a
// synthesis batch a 7B local model cannot finish inside graft's ten-minute
// request timeout, and failed with the answer its first attempt already had.
// Against a server on this machine the retry budget is capped.
func TestGraftDeepEnvCapsRetriesOnALocalServer(t *testing.T) {
	t.Setenv(GraftRetriesVar, "")
	t.Setenv(OllamaKeyVar, "")

	e := GraftDeepEnv(Env{}, GraftDeepKind, OllamaBackend)
	if e[GraftRetriesVar] != GraftLocalRetries {
		t.Errorf("%s = %q, want %q", GraftRetriesVar, e[GraftRetriesVar], GraftLocalRetries)
	}
	// The credential still arrives; the cap is not applied instead of it.
	if e[GraftKeyVar] != GraftLocalKey {
		t.Errorf("key = %q, want %q", e[GraftKeyVar], GraftLocalKey)
	}
	// A proxied local server carries its own key, and used to return early
	// on it — which skipped the cap for the one setup most likely to stall.
	e = GraftDeepEnv(Env{GraftKeyVar: "mine"}, GraftDeepKind, OllamaBackend)
	if e[GraftRetriesVar] != GraftLocalRetries {
		t.Errorf("overlay with a key skipped the cap: %v", e)
	}
	if e[GraftKeyVar] != "mine" {
		t.Errorf("overlay key overwritten: %q", e[GraftKeyVar])
	}
	// A budget the user named is theirs, from the overlay or the session.
	e = GraftDeepEnv(Env{GraftRetriesVar: "9"}, GraftDeepKind, OllamaBackend)
	if e[GraftRetriesVar] != "9" {
		t.Errorf("overlay budget overwritten: %q", e[GraftRetriesVar])
	}
	t.Setenv(GraftRetriesVar, "7")
	e = GraftDeepEnv(Env{}, GraftDeepKind, OllamaBackend)
	if _, ok := e[GraftRetriesVar]; ok {
		t.Errorf("an exported budget was overridden: %q", e[GraftRetriesVar])
	}
}

// The cap is about this machine's own server. A metered gateway's failures
// are the transport's, which is what a retry budget is for.
func TestGraftDeepEnvLeavesAMeteredBudgetAlone(t *testing.T) {
	t.Setenv(GraftRetriesVar, "")
	t.Setenv(AnthropicKeyVar, "sk-test")

	e := GraftDeepEnv(Env{}, GraftDeepKind, ClaudeAPIBackend)
	if _, ok := e[GraftRetriesVar]; ok {
		t.Errorf("a metered backend got a capped budget: %v", e)
	}
	if e[GraftKeyVar] != "sk-test" {
		t.Errorf("key = %q, want the backend's own", e[GraftKeyVar])
	}
}
