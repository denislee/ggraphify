package gfy

import (
	"os"
	"sort"
	"strconv"
	"strings"
)

// Env is a GRAPHIFY_* overlay composed per job rather than exported globally,
// so two concurrent jobs can target different backends without one changing
// the other's environment underneath it.
//
// API keys are never members of this map. They are read from the inherited
// environment and passed through untouched; ggraphify's own state file holds
// the *name* of a backend, never the value of a credential, and nothing in
// here is ever rendered into the UI's command preview beyond the keys the user
// typed themselves.
type Env map[string]string

// SecretSuffixes name the variables whose values must never be displayed. The
// overlay editor in settings still lets a user set one — it is their machine —
// but the value is masked wherever the board prints the composed environment.
var SecretSuffixes = []string{"_API_KEY", "_TOKEN", "_SECRET", "_PASSWORD", "KEY"}

// IsSecret reports whether a variable's value should be masked on screen.
func IsSecret(name string) bool {
	n := strings.ToUpper(name)
	for _, suf := range SecretSuffixes {
		if strings.HasSuffix(n, suf) {
			return true
		}
	}
	return false
}

// Mask renders a value safe to display.
func Mask(name, value string) string {
	if !IsSecret(name) || value == "" {
		return value
	}
	return "••••••"
}

// Compose merges the process environment with the overlay and returns it in
// exec.Cmd's []string form, sorted so that a command preview is stable between
// runs rather than reordered by Go's map iteration.
func (e Env) Compose(base []string) []string {
	if base == nil {
		base = os.Environ()
	}
	if len(e) == 0 {
		return base
	}
	keys := make([]string, 0, len(e))
	for k := range e {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	out := make([]string, 0, len(base)+len(keys))
	skip := make(map[string]bool, len(keys))
	for _, k := range keys {
		skip[k] = true
	}
	for _, kv := range base {
		if name, _, ok := strings.Cut(kv, "="); ok && skip[name] {
			continue
		}
		out = append(out, kv)
	}
	for _, k := range keys {
		out = append(out, k+"="+e[k])
	}
	return out
}

// Display is the overlay as sorted `NAME=value` lines with secrets masked —
// what the confirm dialog shows under the command line.
func (e Env) Display() []string {
	keys := make([]string, 0, len(e))
	for k := range e {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k+"="+Mask(k, e[k]))
	}
	return out
}

// Clone is a defensive copy, so a job holds the overlay it was launched with
// even if settings change while it runs.
func (e Env) Clone() Env {
	if e == nil {
		return nil
	}
	c := make(Env, len(e))
	for k, v := range e {
		c[k] = v
	}
	return c
}

// JobEnv is the overlay every job gets on top of the user's settings.
//
// GRAPHIFY_NO_TIPS silences the CLI's interactive tips, which are written for
// a human at a terminal and are noise in a job log. Everything else is left
// alone: graphify's own defaults are better tested than any value this board
// could invent.
func JobEnv(user Env) Env {
	e := user.Clone()
	if e == nil {
		e = Env{}
	}
	if _, ok := e["GRAPHIFY_NO_TIPS"]; !ok {
		e["GRAPHIFY_NO_TIPS"] = "1"
	}
	return e
}

// OutEnv points graphify at a non-default output directory for one job.
// graphify takes either a bare directory name or an absolute path.
func OutEnv(e Env, out string) Env {
	if out == "" {
		return e
	}
	c := e.Clone()
	if c == nil {
		c = Env{}
	}
	c["GRAPHIFY_OUT"] = out
	return c
}

// KnownVars are the GRAPHIFY_* variables the settings overlay editor offers by
// name, so the common ones are a pick rather than a thing to remember. The
// editor still accepts any name: this list is a convenience, not a whitelist.
var KnownVars = []string{
	"GRAPHIFY_OUT", "GRAPHIFY_OUT_NAME", "GRAPHIFY_FORCE",
	"GRAPHIFY_MAX_WORKERS", "GRAPHIFY_MAX_CONTEXTS", "GRAPHIFY_MAX_OUTPUT_TOKENS",
	"GRAPHIFY_MAX_RETRIES", "GRAPHIFY_API_TIMEOUT", "GRAPHIFY_LLM_TEMPERATURE",
	"GRAPHIFY_DISABLE_THINKING", "GRAPHIFY_NO_INCREMENTAL_CACHE", "GRAPHIFY_NO_BACKUP",
	"GRAPHIFY_NO_TIPS", "GRAPHIFY_DEBUG", "GRAPHIFY_LOG", "GRAPHIFY_QUERY_LOG",
	"GRAPHIFY_BIN", "GRAPHIFY_PYTHON", "GRAPHIFY_CHANGED",
	"GRAPHIFY_MTIME_GRANULARITY_MS", "GRAPHIFY_MAX_GRAPH_BYTES", "GRAPHIFY_BUILD_PERF",
	"GRAPHIFY_ALLOW_LOCAL_PROVIDERS", "GRAPHIFY_HOOK_STRICT", "GRAPHIFY_HOOK_STRICT_TTL",
	"GRAPHIFY_GEMINI_MODEL", "GRAPHIFY_OPENAI_MODEL", "GRAPHIFY_DEEPSEEK_MODEL",
	ClaudeCLIModelVar, "GRAPHIFY_CLAUDE_CLI_PARALLEL",
	"GRAPHIFY_OLLAMA_MODEL", "GRAPHIFY_OLLAMA_HOST",
}

// ValidVar rejects a name that cannot be an environment variable, which is the
// only validation the overlay editor needs to do.
func ValidVar(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		ok := c == '_' || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (i > 0 && c >= '0' && c <= '9')
		if !ok {
			return false
		}
	}
	return true
}

// HasAPIKey reports whether the inherited environment carries a credential a
// metered job could actually use. The confirm dialog says so: a fan-out
// extraction that is going to fail on every repository for want of a key is
// worth catching before it starts, not after.
func HasAPIKey() bool {
	for _, k := range []string{
		"GRAPHIFY_API_KEY", "ANTHROPIC_API_KEY", "OPENAI_API_KEY",
		"GEMINI_API_KEY", "GOOGLE_API_KEY", "DEEPSEEK_API_KEY", "MOONSHOT_API_KEY",
	} {
		if strings.TrimSpace(os.Getenv(k)) != "" {
			return true
		}
	}
	// ollama needs no key of its own, only a host to talk to. claude-cli
	// needs neither — ask HasClaudeCLI about that one, not this.
	return strings.TrimSpace(os.Getenv("OLLAMA_HOST")) != ""
}

// Itoa is strconv.Itoa, re-exported so the ui package can format overlay
// numbers without importing strconv for one call.
func Itoa(n int) string { return strconv.Itoa(n) }
