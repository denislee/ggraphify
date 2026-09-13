package gfy

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// A machine can hold more than one Claude Code login. Claude Code keeps
// everything about one login — its credential, its settings, its skills, its
// user-level CLAUDE.md — inside a single configuration directory, and reads
// $CLAUDE_CONFIG_DIR to decide which one that is. So "which account" is not a
// concept the CLI exposes as a flag: it is which directory the process was
// pointed at. A second login is an ordinary sibling directory, ~/.claude-work
// beside ~/.claude, switched by exporting one variable.
//
// That makes the choice ggraphify's to offer, because the board launches the
// CLI itself. Two things follow the selection:
//
//   - the claude-cli backend runs against that login's plan, so picking the
//     account is picking whose subscription pays for an extraction;
//   - `graphify install --platform claude` writes the skill and the CLAUDE.md
//     pointer into that directory, so the integration check and its Fix button
//     have to agree with the backend about which install they mean.
//
// Both go through the same setting, and both put CLAUDE_CONFIG_DIR in the
// job's environment overlay, where the confirm dialog prints it.

// ClaudeCredentialFiles are the names Claude Code writes its stored login to
// inside a configuration directory. Both spellings are accepted: the dotted
// one is what current versions write, the undotted one exists on machines
// carried forward from older ones. Only their *presence* is ever read — never
// their contents, which are a credential.
var ClaudeCredentialFiles = []string{".credentials.json", "credentials.json"}

// ClaudeAccount is one Claude Code configuration directory found on this
// machine: one login, as far as the CLI is concerned.
type ClaudeAccount struct {
	// Dir is the directory itself, the value CLAUDE_CONFIG_DIR would take.
	Dir string
	// Name is the short handle shown in the UI: "default" for ~/.claude,
	// otherwise the part that distinguishes it — "nova" for ~/.claude-nova.
	Name string
	// Credential is true when a stored login is present in the directory. A
	// directory without one is still offered: `claude` may be authenticated
	// through the system keyring instead, and the honest report is "no
	// credential file here", not "this account does not exist".
	Credential bool
	// Default marks ~/.claude, the directory Claude Code uses when nothing
	// points it elsewhere.
	Default bool
	// Inherited marks the directory $CLAUDE_CONFIG_DIR already names in the
	// environment ggraphify was launched with.
	Inherited bool
	// Missing marks a directory that is selected in settings but is not on
	// disk. It is kept in the list rather than dropped, so a selection that
	// stopped resolving shows up as a problem instead of silently becoming
	// "default".
	Missing bool
}

// Label is the one-line description for a combo row or a report.
func (a ClaudeAccount) Label() string {
	s := a.Name
	switch {
	case a.Missing:
		s += " — missing: " + Tilde(a.Dir)
		return s
	case a.Default:
		s += " (" + Tilde(a.Dir) + ")"
	default:
		s += " (" + Tilde(a.Dir) + ")"
	}
	if !a.Credential {
		s += " · no stored login"
	}
	if a.Inherited {
		s += " · $" + ClaudeConfigDirVar
	}
	return s
}

// Tilde shortens a path under the home directory for display. The board shows
// these paths in rows that are already long; the full path stays available in
// the tooltip and in the copied report.
func Tilde(p string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" || !strings.HasPrefix(p, home) {
		return p
	}
	rest := strings.TrimPrefix(p, home)
	if rest == "" {
		return "~"
	}
	if rest[0] != filepath.Separator {
		return p
	}
	return "~" + rest
}

// ClaudeAccounts lists the Claude Code configuration directories on this
// machine: ~/.claude and every ~/.claude-* sibling that looks like one, plus
// whatever $CLAUDE_CONFIG_DIR names if it is somewhere else entirely.
//
// "Looks like one" is deliberately loose — a credential, a settings.json, a
// CLAUDE.md, a projects/ or skills/ directory is enough. A stricter test would
// hide a freshly created account that has been logged into but not yet used,
// and the cost of an extra entry in a list is far lower than the cost of a
// login the board refuses to see.
//
// sel is the directory currently chosen in settings; it is included even when
// it no longer exists, marked Missing.
func ClaudeAccounts(sel string) []ClaudeAccount {
	seen := map[string]bool{}
	var out []ClaudeAccount
	add := func(dir string, missingOK bool) {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			return
		}
		if abs, err := filepath.Abs(dir); err == nil {
			dir = abs
		}
		if seen[dir] {
			return
		}
		fi, err := os.Stat(dir)
		exists := err == nil && fi.IsDir()
		if !exists && !missingOK {
			return
		}
		seen[dir] = true
		out = append(out, ClaudeAccount{
			Dir:        dir,
			Name:       claudeAccountName(dir),
			Credential: exists && ClaudeCredential(dir) != "",
			Default:    dir == defaultClaudeDir(),
			Inherited:  sameDirSpelling(dir, os.Getenv(ClaudeConfigDirVar)),
			Missing:    !exists,
		})
	}

	if home, err := os.UserHomeDir(); err == nil {
		add(filepath.Join(home, ".claude"), false)
		ents, err := os.ReadDir(home)
		if err == nil {
			for _, e := range ents {
				n := e.Name()
				if !e.IsDir() || !strings.HasPrefix(n, ".claude-") {
					continue
				}
				dir := filepath.Join(home, n)
				if looksLikeClaudeDir(dir) {
					add(dir, false)
				}
			}
		}
	}
	// An account kept outside the home directory is reachable only through
	// these two, so neither is filtered by the shape test above: the user
	// named it, which is evidence enough.
	add(os.Getenv(ClaudeConfigDirVar), false)
	add(sel, true)

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Default != out[j].Default {
			return out[i].Default
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// ClaudeCredential returns the stored-login file inside a configuration
// directory, or "" when there is none. The file is never opened.
func ClaudeCredential(dir string) string {
	for _, n := range ClaudeCredentialFiles {
		p := filepath.Join(dir, n)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p
		}
	}
	return ""
}

// ClaudeAccountFor resolves a settings selection to the account it names,
// falling back to the directory the environment would have used anyway.
func ClaudeAccountFor(sel string) ClaudeAccount {
	dir := ClaudeDirFor(sel)
	for _, a := range ClaudeAccounts(sel) {
		if sameDirSpelling(a.Dir, dir) {
			return a
		}
	}
	return ClaudeAccount{Dir: dir, Name: claudeAccountName(dir), Missing: true}
}

// ClaudeDirFor is the configuration directory a job should run against: the
// explicit selection when there is one, else whatever ClaudeDir resolves to
// from the environment.
func ClaudeDirFor(sel string) string {
	if s := strings.TrimSpace(sel); s != "" {
		return s
	}
	return ClaudeDir()
}

// ClaudeAccountEnv points a job at a chosen Claude Code account by setting
// CLAUDE_CONFIG_DIR in its overlay.
//
// It is applied to every job, not only to claude-cli ones, because the
// installer job writes into this same directory — an install that landed in
// ~/.claude while the backend ran out of ~/.claude-work would leave the board
// checking one account and billing another.
//
// A blank selection is left alone rather than pinned to the resolved default:
// the inherited environment is then still in charge, which is what a user who
// exports CLAUDE_CONFIG_DIR in their shell profile expects.
func ClaudeAccountEnv(e Env, sel string) Env {
	s := strings.TrimSpace(sel)
	if s == "" {
		return e
	}
	if abs, err := filepath.Abs(s); err == nil {
		s = abs
	}
	c := e.Clone()
	if c == nil {
		c = Env{}
	}
	c[ClaudeConfigDirVar] = s
	return c
}

// claudeAccountName is the short handle for a configuration directory.
func claudeAccountName(dir string) string {
	if dir == defaultClaudeDir() {
		return "default"
	}
	base := strings.TrimPrefix(filepath.Base(dir), ".")
	if n := strings.TrimPrefix(base, "claude-"); n != "" && n != base {
		return n
	}
	if base == "" {
		return dir
	}
	return base
}

// defaultClaudeDir is ~/.claude regardless of what the environment says, which
// is what makes "default" a stable label rather than one that moves with
// $CLAUDE_CONFIG_DIR.
func defaultClaudeDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude")
}

func looksLikeClaudeDir(dir string) bool {
	if ClaudeCredential(dir) != "" {
		return true
	}
	for _, n := range []string{"settings.json", "CLAUDE.md", "projects", "skills", "statsig", ".claude.json"} {
		if _, err := os.Stat(filepath.Join(dir, n)); err == nil {
			return true
		}
	}
	return false
}

// sameDirSpelling compares two directory paths tolerantly: absolute where it
// can, by identity when both exist, and by string otherwise — enough for two
// spellings of one account to agree without turning a missing directory into
// a mismatch.
func sameDirSpelling(a, b string) bool {
	a, b = strings.TrimSpace(a), strings.TrimSpace(b)
	if a == "" || b == "" {
		return false
	}
	if pa, err := filepath.Abs(a); err == nil {
		a = pa
	}
	if pb, err := filepath.Abs(b); err == nil {
		b = pb
	}
	if a == b {
		return true
	}
	return sameFile(a, b)
}
