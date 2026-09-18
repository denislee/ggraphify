package gfy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// noCredentials clears every variable HasAPIKey looks at, so a test asserts
// about the machine it describes rather than about the developer's shell.
func noCredentials(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"GRAPHIFY_API_KEY", "ANTHROPIC_API_KEY", "OPENAI_API_KEY",
		"GEMINI_API_KEY", "GOOGLE_API_KEY", "DEEPSEEK_API_KEY", "MOONSHOT_API_KEY",
		"OLLAMA_HOST",
	} {
		t.Setenv(k, "")
	}
	// EffectiveBackend's last resort PROBES the default ollama port rather
	// than reading a variable, so on a machine actually running ollama — the
	// machine most likely to be working on this file — clearing the
	// environment still leaves it finding a server. Pointing OLLAMA_BASE_URL
	// or OLLAMA_HOST at a dead port does not help: exporting either is
	// precisely the signal HasAPIKey reads as "ollama is configured", which
	// turns "no credential" into "a credential". So the probe itself is
	// stubbed, and restored when the test ends.
	orig := probeLocal
	probeLocal = func(backend string) LocalProbe {
		return LocalProbe{Backend: backend}
	}
	t.Cleanup(func() { probeLocal = orig })
}

// The Claude Code CLI is the one backend graphify cannot auto-detect, because
// its detection is key-based and this backend has no key. A machine with a
// Claude Code install and nothing else must therefore be told the backend by
// name, or every metered run refuses for want of a credential it does not need.
func TestEffectiveBackendPrefersClaudeCLIWithoutAKey(t *testing.T) {
	noCredentials(t)
	t.Setenv(ClaudeCLIBinVar, "/usr/bin/claude")

	if got := EffectiveBackend(""); got != ClaudeCLIBackend {
		t.Fatalf("blank backend with a Claude Code install resolved to %q, want %q", got, ClaudeCLIBackend)
	}
	if got := EffectiveBackend("gemini"); got != "gemini" {
		t.Fatalf("an explicit backend was overridden: got %q", got)
	}
}

// With a key set, auto-detect stays auto-detect: graphify's own resolution is
// better informed than this board's, and a user who exported a key meant it.
func TestEffectiveBackendLeavesAutoDetectAloneWithAKey(t *testing.T) {
	noCredentials(t)
	t.Setenv(ClaudeCLIBinVar, "/usr/bin/claude")
	t.Setenv("OPENAI_API_KEY", "sk-test")

	if got := EffectiveBackend(""); got != "" {
		t.Fatalf("auto-detect was overridden despite an API key: got %q", got)
	}
}

func TestEffectiveBackendWithNothingInstalled(t *testing.T) {
	noCredentials(t)
	t.Setenv(ClaudeCLIBinVar, "off")

	if got := EffectiveBackend(""); got != "" {
		t.Fatalf("blank backend invented %q on a machine with no credential at all", got)
	}
	if ok, why := BackendReady(""); ok || why == "" {
		t.Fatalf("BackendReady(%q) = %v, %q; want a refusal with a reason", "", ok, why)
	}
}

func TestBackendReadyClaudeCLI(t *testing.T) {
	noCredentials(t)

	t.Setenv(ClaudeCLIBinVar, "/usr/bin/claude")
	ok, why := BackendReady(ClaudeCLIBackend)
	if !ok {
		t.Fatalf("claude-cli reported unusable with the CLI present: %s", why)
	}
	if !strings.Contains(why, "/usr/bin/claude") {
		t.Errorf("the reason does not say where the CLI is: %q", why)
	}

	t.Setenv(ClaudeCLIBinVar, "off")
	if ok, why := BackendReady(ClaudeCLIBackend); ok {
		t.Fatalf("claude-cli reported usable with no CLI installed: %q", why)
	}
}

// graphify launches the CLI by the bare name `claude`, so an install that only
// this board can find has to be put on the PATH the job inherits — otherwise a
// .desktop launch, whose PATH is the session's and not the shell's, fails with
// "Claude Code CLI not found on $PATH" while settings cheerfully shows a path.
func TestClaudeCLIEnvPutsAnOffPathInstallOnPath(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "claude")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(ClaudeCLIBinVar, bin)
	t.Setenv("PATH", "/usr/bin")

	got := ClaudeCLIEnv(Env{"GRAPHIFY_NO_TIPS": "1"}, ClaudeCLIBackend)
	path := got["PATH"]
	if !strings.HasPrefix(path, dir+string(os.PathListSeparator)) {
		t.Fatalf("PATH = %q, want it to start with %q", path, dir)
	}
	if !strings.Contains(path, "/usr/bin") {
		t.Errorf("the inherited PATH was dropped: %q", path)
	}
	if got["GRAPHIFY_NO_TIPS"] != "1" {
		t.Error("the caller's overlay was lost")
	}
}

// Every other backend is left untouched — no board should be rewriting PATH
// for a job that does not shell out to anything.
func TestClaudeCLIEnvLeavesOtherBackendsAlone(t *testing.T) {
	t.Setenv(ClaudeCLIBinVar, "/nowhere/claude")
	in := Env{"GRAPHIFY_NO_TIPS": "1"}
	if got := ClaudeCLIEnv(in, "gemini"); got["PATH"] != "" {
		t.Fatalf("PATH was set for the gemini backend: %q", got["PATH"])
	}
}

// An install already on PATH needs no help, and adding a duplicate entry would
// only make the composed environment noisier to read in the confirm dialog.
func TestClaudeCLIEnvLeavesAnOnPathInstallAlone(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "claude")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(ClaudeCLIBinVar, bin)
	t.Setenv("PATH", dir)

	if got := ClaudeCLIEnv(nil, ClaudeCLIBackend); got["PATH"] != "" {
		t.Fatalf("PATH was rewritten for an install already on it: %q", got["PATH"])
	}
}

func TestArgvBackend(t *testing.T) {
	cases := []struct {
		argv []string
		want string
	}{
		{[]string{"graphify", "extract", ".", "--backend", "claude-cli"}, "claude-cli"},
		{[]string{"graphify", "label", ".", "--backend=gemini"}, "gemini"},
		{[]string{"graphify", "update", "."}, ""},
		{[]string{"graphify", "extract", ".", "--backend"}, ""},
	}
	for _, c := range cases {
		if got := ArgvBackend(c.argv); got != c.want {
			t.Errorf("ArgvBackend(%q) = %q, want %q", c.argv, got, c.want)
		}
	}
}

// claude-cli is offered in the picker, because a backend the board can resolve
// to but not let a user choose is a backend that cannot be un-chosen either.
func TestBackendsOffersClaudeCLI(t *testing.T) {
	for _, b := range Backends {
		if b == ClaudeCLIBackend {
			return
		}
	}
	t.Fatalf("Backends does not offer %q: %q", ClaudeCLIBackend, Backends)
}

// An overlay entry the user typed themselves outranks the model setting: the
// settings page offers GRAPHIFY_CLAUDE_CLI_MODEL by name, so a board that
// overwrote it would make that editor a lie.
func TestClaudeCLIModelEnvPrecedence(t *testing.T) {
	e := Env{ClaudeCLIModelVar: "sonnet"}
	got := ClaudeCLIModelEnv(e, ClaudeCLIBackend, "haiku")
	if got[ClaudeCLIModelVar] != "sonnet" {
		t.Errorf("the overlay lost to the setting: %q", got[ClaudeCLIModelVar])
	}

	// A blank model is left alone rather than pinned to a guess — the
	// inherited environment, then Claude Code's own default, decide.
	if got := ClaudeCLIModelEnv(Env{}, ClaudeCLIBackend, "  "); len(got) != 0 {
		t.Errorf("a blank model invented a variable: %v", got)
	}
}

// A job rebuilt from its saved argv — a retry, a session restored after a
// restart — has to recover the model the same way it recovers the backend.
func TestArgvModelRoundTripsThroughAnExtractArgv(t *testing.T) {
	argv := Argv("extract", Params{
		Repo: ".", Backend: ClaudeCLIBackend, Model: "haiku",
	})
	if got := ArgvModel(argv); got != "haiku" {
		t.Errorf("ArgvModel(%v) = %q, want haiku", argv, got)
	}
	if got := ArgvModel(Argv("update", Params{Repo: "."})); got != "" {
		t.Errorf("a modelless argv reported %q", got)
	}
}
