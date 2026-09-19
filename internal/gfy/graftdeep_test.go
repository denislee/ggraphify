package gfy

import (
	"strings"
	"testing"
)

// The deep pass is the one graft command that dispatches LLM requests, and the
// whole of its safety is that it is only ever built for a local endpoint.
// These tests pin that, and the two places a mistake would be invisible: an
// argv with no --base-url, and a lease not taken.

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

func TestArgvGraftDeepRefusesAMeteredBackend(t *testing.T) {
	for _, backend := range []string{"anthropic", ClaudeCLIBackend, OpenCodeBackend, ""} {
		if argv := Argv(GraftDeepKind, Params{Repo: "/tmp/repo", Backend: backend, Model: "m"}); argv != nil {
			t.Fatalf("backend %q built a deep argv: %v", backend, argv)
		}
	}
}

func TestGraftDeepEnvSuppliesThePlaceholderKey(t *testing.T) {
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

func TestGraftDeepIsFree(t *testing.T) {
	if CostOf(GraftDeepKind) != Free {
		t.Fatal("the deep pass is metered in Known; it only ever runs locally")
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
