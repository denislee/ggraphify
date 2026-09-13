package gfy

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

// ClaudeCLIBackend is graphify's name for the backend that shells out to the
// Claude Code CLI installed on this machine (`claude -p`) instead of calling a
// vendor API with a key of its own. It is the one backend with no credential
// to find: authentication is whatever `claude` itself is already logged in as,
// which for a Pro/Max subscriber is the subscription and not a metered key.
//
// Two consequences the board has to respect:
//
//   - graphify's own backend auto-detection is key-based and therefore can
//     never select it (see graphify's llm.detect_backend). A machine with a
//     Claude Code install and no API key must be told `--backend claude-cli`
//     explicitly, which is what EffectiveBackend does.
//   - graphify forces this backend to a single in-flight request unless
//     GRAPHIFY_CLAUDE_CLI_PARALLEL=1, because parallel subprocesses fight over
//     one Claude Code session. A fan-out over many repositories is therefore
//     slower here than against an API key, not broken.
const ClaudeCLIBackend = "claude-cli"

// ClaudeCLIModelVar is the env var graphify reads to pick the model the CLI
// backend runs with. Left unset, Claude Code's own default applies.
const ClaudeCLIModelVar = "GRAPHIFY_CLAUDE_CLI_MODEL"

// ClaudeCLIBinVar names the override for where the Claude Code CLI lives, or
// "off" to make the board behave as though it were not installed.
const ClaudeCLIBinVar = "GGRAPHIFY_CLAUDE_BIN"

var claudeOnce struct {
	sync.Once
	path string
}

// ClaudeCLI resolves the Claude Code executable, or "" when this machine has
// none. Like Bin, it looks past $PATH: the board is often launched from a
// .desktop entry, whose PATH is the session's minimal one and not the shell's,
// so a `claude` in ~/.local/bin is invisible to a bare lookup.
//
// $GGRAPHIFY_CLAUDE_BIN overrides everything: a path, for an install in a
// place this list does not know about, or "off" to hide an install the board
// should not use. It is read on every call, while the search below — which
// touches the filesystem — is done once per process.
func ClaudeCLI() string {
	switch v := strings.TrimSpace(os.Getenv(ClaudeCLIBinVar)); strings.ToLower(v) {
	case "":
	case "off", "none", "0":
		return ""
	default:
		return v
	}
	claudeOnce.Do(func() { claudeOnce.path = resolveClaudeCLI() })
	return claudeOnce.path
}

func resolveClaudeCLI() string {
	names := []string{"claude"}
	if runtime.GOOS == "windows" {
		// npm installs claude.ps1 next to claude.cmd, and CreateProcess can
		// only run the latter — graphify prefers .cmd for the same reason.
		names = []string{"claude.cmd", "claude"}
	}
	for _, n := range names {
		if p, err := exec.LookPath(n); err == nil {
			return p
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		for _, c := range []string{
			filepath.Join(home, ".local", "bin", "claude"),
			filepath.Join(home, ".claude", "local", "claude"),
			filepath.Join(home, ".npm-global", "bin", "claude"),
			filepath.Join(home, "node_modules", ".bin", "claude"),
			filepath.Join(home, ".bun", "bin", "claude"),
		} {
			if executableFile(c) {
				return c
			}
		}
	}
	for _, c := range []string{"/usr/local/bin/claude", "/usr/bin/claude", "/opt/homebrew/bin/claude"} {
		if executableFile(c) {
			return c
		}
	}
	return ""
}

func executableFile(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir() && (runtime.GOOS == "windows" || fi.Mode()&0o111 != 0)
}

// HasClaudeCLI reports whether the Claude Code CLI backend is usable here.
func HasClaudeCLI() bool { return ClaudeCLI() != "" }

// EffectiveBackend is the backend a job will actually run against, given what
// the user chose in settings.
//
// An explicit choice is always honoured. A blank choice means "auto-detect",
// which is graphify's own default and works whenever an API key is exported —
// but graphify's detection is key-based, so on a machine whose only credential
// is a logged-in Claude Code install it would find nothing and the extraction
// would refuse to run. There, blank resolves to claude-cli: the one backend
// that is demonstrably available. The resolved name is what goes into the
// argv the confirm dialog shows, so this is a visible substitution rather
// than a silent one.
func EffectiveBackend(backend string) string {
	if b := strings.TrimSpace(backend); b != "" {
		return b
	}
	if HasAPIKey() {
		return "" // let graphify detect from the key that is set
	}
	if HasClaudeCLI() {
		return ClaudeCLIBackend
	}
	return ""
}

// BackendReady reports whether a metered run against this backend has the
// credential it needs, and why not when it does not. The reason is written to
// be shown to a human in the confirm dialog.
//
// It answers for the *effective* backend, so pass what EffectiveBackend
// returned rather than the raw setting.
func BackendReady(backend string) (bool, string) {
	switch strings.TrimSpace(backend) {
	case ClaudeCLIBackend:
		if HasClaudeCLI() {
			return true, "Claude Code CLI at " + ClaudeCLI() + " — billed to that " +
				"login's plan, not to an API key. Requests run one at a time."
		}
		return false, "The claude-cli backend needs the Claude Code CLI on this machine, " +
			"and none was found. Install it from https://claude.ai/code and run " +
			"`claude` once to authenticate."
	case "ollama":
		return true, "A local ollama backend needs no credential."
	case "bedrock":
		return true, "Bedrock authenticates through the AWS credential chain."
	case "":
		if HasAPIKey() {
			return true, "An API key is visible in this environment; graphify will auto-detect the backend."
		}
		return false, "No API key is visible in this environment and no Claude Code CLI was found, " +
			"so there is nothing for graphify to auto-detect."
	default:
		if HasAPIKey() {
			return true, "An API key is visible in this environment."
		}
		return false, "No API key is visible in this environment for the " + backend + " backend."
	}
}

// ClaudeCLIEnv makes a resolved-but-off-PATH Claude Code install reachable by
// the graphify subprocess. graphify launches the CLI by the bare name
// `claude`, so a binary found in ~/.local/bin by ClaudeCLI is of no use to it
// unless that directory is on the PATH the job inherits — the exact gap a
// .desktop launch opens. Prepending the directory costs nothing when the
// lookup already succeeded on PATH, which is the common case.
func ClaudeCLIEnv(e Env, backend string) Env {
	if strings.TrimSpace(backend) != ClaudeCLIBackend {
		return e
	}
	p := ClaudeCLI()
	if p == "" {
		return e
	}
	// Nothing to do when a bare lookup already lands on this exact binary. A
	// lookup that lands on a *different* one is the interesting case — the
	// board would report one path and graphify would run another — so that
	// falls through and the resolved directory wins.
	if found, err := exec.LookPath(filepath.Base(p)); err == nil && sameFile(found, p) {
		return e
	}
	dir := filepath.Dir(p)
	c := e.Clone()
	if c == nil {
		c = Env{}
	}
	path := c["PATH"]
	if path == "" {
		path = os.Getenv("PATH")
	}
	if path == "" {
		c["PATH"] = dir
	} else {
		c["PATH"] = dir + string(os.PathListSeparator) + path
	}
	return c
}

// ArgvBackend reads the backend back out of a built command line, for the
// paths that have an argv but no longer the Params it came from — a retry of a
// finished job, say. Returns "" when the command carries no --backend.
func ArgvBackend(argv []string) string {
	for i, a := range argv {
		if a == "--backend" && i+1 < len(argv) {
			return argv[i+1]
		}
		if v, ok := strings.CutPrefix(a, "--backend="); ok {
			return v
		}
	}
	return ""
}

// sameFile compares two paths by identity rather than by spelling, so a
// symlink, a relative path and an absolute one all agree.
func sameFile(a, b string) bool {
	fa, err := os.Stat(a)
	if err != nil {
		return false
	}
	fb, err := os.Stat(b)
	if err != nil {
		return false
	}
	return os.SameFile(fa, fb)
}
