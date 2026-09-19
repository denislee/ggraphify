package gfy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeClaudeAccounts builds a home directory holding several Claude Code
// logins — the shape of a machine with a personal ~/.claude and a work
// ~/.claude-nova beside it — and points $HOME at it. The returned paths are
// default, then each named account in the order given.
func fakeClaudeAccounts(t *testing.T, named ...string) (home string, dirs map[string]string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(ClaudeConfigDirVar, "")

	dirs = map[string]string{}
	mk := func(name, dir string, cred bool) {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if cred {
			if err := os.WriteFile(filepath.Join(dir, ".credentials.json"), []byte("{}"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		dirs[name] = dir
	}
	mk("default", filepath.Join(home, ".claude"), true)
	for _, n := range named {
		mk(n, filepath.Join(home, ".claude-"+n), true)
	}
	return home, dirs
}

func accountNames(accs []ClaudeAccount) []string {
	out := make([]string, len(accs))
	for i, a := range accs {
		out[i] = a.Name
	}
	return out
}

func TestClaudeAccountsFindsSiblings(t *testing.T) {
	home, dirs := fakeClaudeAccounts(t, "nova")
	// A dot-directory that is not a Claude config dir must not be offered as
	// a login: the list is a picker, and a wrong entry there sends a metered
	// job at a directory with no credential in it.
	if err := os.MkdirAll(filepath.Join(home, ".claude-cache-junk"), 0o755); err != nil {
		t.Fatal(err)
	}

	accs := ClaudeAccounts("")
	got := accountNames(accs)
	if len(got) != 2 || got[0] != "default" || got[1] != "nova" {
		t.Fatalf("ClaudeAccounts = %v, want [default nova]", got)
	}
	if accs[0].Dir != dirs["default"] || !accs[0].Default {
		t.Errorf("first account = %+v, want ~/.claude marked default", accs[0])
	}
	if !accs[1].Credential {
		t.Errorf("nova has a .credentials.json but Credential is false: %+v", accs[1])
	}
}

func TestClaudeAccountsKeepsMissingSelection(t *testing.T) {
	home, _ := fakeClaudeAccounts(t)
	gone := filepath.Join(home, ".claude-deleted")

	accs := ClaudeAccounts(gone)
	var found *ClaudeAccount
	for i := range accs {
		if accs[i].Dir == gone {
			found = &accs[i]
		}
	}
	if found == nil {
		t.Fatalf("a selected-but-absent account vanished from the list: %v", accountNames(accs))
	}
	if !found.Missing {
		t.Errorf("account %+v should be marked Missing", *found)
	}
}

func TestClaudeAccountsIncludesEnvDirOutsideHome(t *testing.T) {
	fakeClaudeAccounts(t)
	elsewhere := t.TempDir()
	t.Setenv(ClaudeConfigDirVar, elsewhere)

	accs := ClaudeAccounts("")
	var inherited bool
	for _, a := range accs {
		if a.Dir == elsewhere {
			inherited = a.Inherited
		}
	}
	if !inherited {
		t.Fatalf("$%s outside the home directory was not offered: %v", ClaudeConfigDirVar, accountNames(accs))
	}
}

func TestClaudeDirForAndAccountFor(t *testing.T) {
	_, dirs := fakeClaudeAccounts(t, "nova")

	if got := ClaudeDirFor(""); got != dirs["default"] {
		t.Errorf("ClaudeDirFor(\"\") = %q, want the default %q", got, dirs["default"])
	}
	if got := ClaudeDirFor(dirs["nova"]); got != dirs["nova"] {
		t.Errorf("ClaudeDirFor(nova) = %q, want %q", got, dirs["nova"])
	}
	if a := ClaudeAccountFor(dirs["nova"]); a.Name != "nova" || !a.Credential || a.Missing {
		t.Errorf("ClaudeAccountFor(nova) = %+v", a)
	}
	if a := ClaudeAccountFor(""); a.Name != "default" {
		t.Errorf("ClaudeAccountFor(\"\") = %+v, want the default account", a)
	}
}

func TestClaudeAccountEnv(t *testing.T) {
	_, dirs := fakeClaudeAccounts(t, "nova")

	// A blank selection must leave the environment alone: a user who exports
	// CLAUDE_CONFIG_DIR in their shell profile is not overridden by a setting
	// they never made.
	if got := ClaudeAccountEnv(Env{"GRAPHIFY_NO_TIPS": "1"}, ""); got[ClaudeConfigDirVar] != "" {
		t.Errorf("blank selection set %s=%q", ClaudeConfigDirVar, got[ClaudeConfigDirVar])
	}

	base := Env{"GRAPHIFY_NO_TIPS": "1"}
	got := ClaudeAccountEnv(base, dirs["nova"])
	if got[ClaudeConfigDirVar] != dirs["nova"] {
		t.Errorf("%s = %q, want %q", ClaudeConfigDirVar, got[ClaudeConfigDirVar], dirs["nova"])
	}
	if got["GRAPHIFY_NO_TIPS"] != "1" {
		t.Error("the rest of the overlay was dropped")
	}
	if _, ok := base[ClaudeConfigDirVar]; ok {
		t.Error("the caller's overlay was mutated; a running job would have its account changed underneath it")
	}
}

func TestInspectClaudeInUsesSelectedAccount(t *testing.T) {
	_, dirs := fakeClaudeAccounts(t, "nova")

	s := InspectClaudeIn(Version{Found: true, Number: BuiltAgainst}, dirs["nova"])
	if s.Dir != dirs["nova"] {
		t.Fatalf("inspected %q, want the selected %q", s.Dir, dirs["nova"])
	}
	if s.Account.Name != "nova" {
		t.Errorf("Account = %+v, want nova", s.Account)
	}
	if c := check(t, s, "Account"); c.State != CheckOK {
		t.Errorf("Account check = %s — %s", c.State, c.Detail)
	}
	// The skill is not installed in that account, so the report must say so
	// rather than reporting the default account's healthy install.
	if c := check(t, s, "Agent skill"); c.State != CheckBad {
		t.Errorf("Agent skill = %s — %s", c.State, c.Detail)
	}
}

func TestInspectClaudeInAccountWithoutCredential(t *testing.T) {
	home, _ := fakeClaudeAccounts(t)
	empty := filepath.Join(home, ".claude-empty")
	if err := os.MkdirAll(filepath.Join(empty, "projects"), 0o755); err != nil {
		t.Fatal(err)
	}

	s := InspectClaudeIn(Version{Found: true, Number: BuiltAgainst}, empty)
	// A warning, not a failure: Claude Code can keep the login in the system
	// keyring instead of in a file.
	if c := check(t, s, "Account"); c.State != CheckWarn {
		t.Errorf("Account = %s — %s", c.State, c.Detail)
	}

	gone := filepath.Join(home, ".claude-gone")
	s = InspectClaudeIn(Version{Found: true, Number: BuiltAgainst}, gone)
	if c := check(t, s, "Account"); c.State != CheckBad {
		t.Errorf("a selected directory that does not exist reported %s — %s", c.State, c.Detail)
	}
}

func TestTilde(t *testing.T) {
	home, _ := fakeClaudeAccounts(t)
	if got := Tilde(filepath.Join(home, ".claude-nova")); got != filepath.Join("~", ".claude-nova") {
		t.Errorf("Tilde = %q", got)
	}
	if got := Tilde("/etc/claude"); got != "/etc/claude" {
		t.Errorf("Tilde outside home rewrote %q", got)
	}
}

// Which Claude Code install a job runs as is a setting, and on this machine it
// is not the default one. The risk is purely that it is invisible: a reader of
// a job log assumes their own configuration directory, and therefore their own
// hooks, skills and login. So the note has to name the directory AND say when
// it is not the default.
func TestAccountNoteNamesTheResolvedDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	alt := filepath.Join(home, ".claude-nova")
	if err := os.MkdirAll(alt, 0o755); err != nil {
		t.Fatal(err)
	}

	note := AccountNote("extract", ClaudeCLIBackend, alt)
	if !strings.Contains(note, alt) {
		t.Errorf("the note does not name the directory: %q", note)
	}
	if !strings.Contains(note, "not the default") {
		t.Errorf("the note does not say it is not ~/.claude: %q", note)
	}

	// `install` writes into that same directory, so it is the one free
	// command the account bears on.
	if AccountNote("install", "", alt) == "" {
		t.Error("the installer carries no account note")
	}
	// An AST update never talks to Claude Code, and a line about it there
	// would be noise in every log on the board.
	if got := AccountNote("update", "", alt); got != "" {
		t.Errorf("AccountNote on a free AST job = %q, want empty", got)
	}
}
