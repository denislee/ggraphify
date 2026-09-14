package gfy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeGraftHome writes what a user-level `graft init` writes: the shim, the
// four hook entries that run it, and the MCP registration. Each piece can be
// left out, because the whole point of the inspection is which piece is gone.
type graftFixture struct {
	dir   string // the Claude Code configuration directory
	home  string // its parent, where .claude.json lives
	shim  string
	baked string // the dist/claude directory the shim bakes in
}

func fakeGraftHome(t *testing.T, opts struct {
	shim    bool
	baked   bool
	events  []string
	mcp     bool
	version string
}) graftFixture {
	t.Helper()
	home := t.TempDir()
	dir := filepath.Join(home, ".claude")
	mustMkdir(t, filepath.Join(dir, "helpers"))
	f := graftFixture{
		dir:   dir,
		home:  home,
		shim:  filepath.Join(dir, "helpers", GraftShimName),
		baked: filepath.Join(home, "node_modules", "@nanonets", "graft", "dist", "claude"),
	}

	if opts.baked {
		mustMkdir(t, f.baked)
		v := opts.version
		if v == "" {
			v = "0.18.0"
		}
		mustWrite(t, filepath.Join(f.baked, "..", "..", "package.json"),
			`{"name":"@nanonets/graft","version":"`+v+`"}`)
	}
	if opts.shim {
		mustWrite(t, f.shim, "#!/usr/bin/env node\nconst BAKED = \""+f.baked+"\";\n")
	}

	settings := map[string]any{}
	if len(opts.events) > 0 {
		hooks := map[string]any{}
		for _, e := range opts.events {
			hooks[e] = []any{map[string]any{"hooks": []any{map[string]any{
				"type":    "command",
				"command": `node "` + f.shim + `" ` + strings.ToLower(e),
			}}}}
		}
		settings["hooks"] = hooks
	}
	b, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(dir, "settings.json"), string(b))

	mcp := map[string]any{"mcpServers": map[string]any{"other": map[string]any{"command": "x"}}}
	if opts.mcp {
		mcp["mcpServers"].(map[string]any)[GraftMCPKey] = map[string]any{
			"command": "graft", "args": []string{"mcp"},
		}
	}
	mb, err := json.Marshal(mcp)
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(home, GraftMCPFile), string(mb))

	// Both are needed: the inspection resolves the config directory through
	// CLAUDE_CONFIG_DIR, and falls back to $HOME for ~/.claude.json.
	t.Setenv(ClaudeConfigDirVar, dir)
	t.Setenv("HOME", home)
	return f
}

func allEvents() []string {
	out := make([]string, 0, len(graftEvents))
	for _, e := range graftEvents {
		out = append(out, e.Event)
	}
	return out
}

func mustMkdir(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustWrite(t *testing.T, p, body string) {
	t.Helper()
	mustMkdir(t, filepath.Dir(p))
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func graftCheck(t *testing.T, s GraftSetup, name string) Check {
	t.Helper()
	for _, c := range s.Checks {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("no check named %q in %v", name, s.Checks)
	return Check{}
}

var installed = GraftVersion{Found: true, Number: "0.18.0", Bin: "/usr/bin/graft"}

func TestInspectGraftHealthy(t *testing.T) {
	fakeGraftHome(t, struct {
		shim    bool
		baked   bool
		events  []string
		mcp     bool
		version string
	}{shim: true, baked: true, events: allEvents(), mcp: true})

	s := InspectGraft(installed, "")
	for _, name := range []string{"graft", "Hooks shim", "Shim target", "Hook entries", "MCP server"} {
		if c := graftCheck(t, s, name); c.State != CheckOK {
			t.Errorf("%s: %s — %s", name, c.State, c.Detail)
		}
	}
	if !s.OK() {
		t.Errorf("a complete wiring did not report OK: %s", s.Summary())
	}
	if s.Fixable() {
		t.Error("a healthy wiring reported as fixable, so the Fix button would be live for nothing")
	}
}

// The case this machine is actually in: the config directory has no helpers/
// of its own, and the hook entries name another directory's shim by absolute
// path. That is a working install and must not read as broken.
func TestShimNamedByHooksElsewhereIsOK(t *testing.T) {
	f := fakeGraftHome(t, struct {
		shim    bool
		baked   bool
		events  []string
		mcp     bool
		version string
	}{shim: true, baked: true, events: allEvents(), mcp: true})

	// A second config directory whose settings point at the first one's shim.
	other := filepath.Join(f.home, ".claude-work")
	mustWrite(t, filepath.Join(other, "settings.json"),
		`{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"node \"`+f.shim+`\" session-start"}]}]}}`)

	s := InspectGraft(installed, other)
	if c := graftCheck(t, s, "Hooks shim"); c.State != CheckOK {
		t.Errorf("Hooks shim: %s — %s", c.State, c.Detail)
	}
	if s.Shim != f.shim {
		t.Errorf("shim = %q, want the one the hooks name (%q)", s.Shim, f.shim)
	}
}

// A hook command naming a shim that does not exist is the worst case: every
// hook is a silent no-op, and nothing anywhere says so.
func TestHooksNamingAMissingShimAreBad(t *testing.T) {
	f := fakeGraftHome(t, struct {
		shim    bool
		baked   bool
		events  []string
		mcp     bool
		version string
	}{shim: false, baked: true, events: allEvents(), mcp: true})

	s := InspectGraft(installed, "")
	c := graftCheck(t, s, "Hooks shim")
	if c.State != CheckBad {
		t.Errorf("state = %s, want problem", c.State)
	}
	if !c.Fixable {
		t.Error("a missing shim is exactly what `graft init` writes")
	}
	if !strings.Contains(c.Detail, f.shim) {
		t.Errorf("the detail does not name the missing file: %s", c.Detail)
	}
}

func TestNoHookEntriesIsBad(t *testing.T) {
	fakeGraftHome(t, struct {
		shim    bool
		baked   bool
		events  []string
		mcp     bool
		version string
	}{shim: true, baked: true, mcp: true})

	s := InspectGraft(installed, "")
	if c := graftCheck(t, s, "Hook entries"); c.State != CheckBad {
		t.Errorf("state = %s, want problem — an installed shim nothing invokes", c.State)
	}
	if !s.Fixable() {
		t.Error("missing hook entries are fixable by `graft init`")
	}
}

func TestPartialHookEntriesNameWhatIsMissing(t *testing.T) {
	fakeGraftHome(t, struct {
		shim    bool
		baked   bool
		events  []string
		mcp     bool
		version string
	}{shim: true, baked: true, events: []string{"SessionStart"}, mcp: true})

	s := InspectGraft(installed, "")
	c := graftCheck(t, s, "Hook entries")
	if c.State != CheckWarn {
		t.Fatalf("state = %s, want warning", c.State)
	}
	for _, want := range []string{"UserPromptSubmit", "PostToolUse", "Stop"} {
		if !strings.Contains(c.Detail, want) {
			t.Errorf("the detail does not name the missing %s: %s", want, c.Detail)
		}
	}
}

// A shim whose baked package directory was removed — an nvm version bump, an
// npm uninstall — still works through its fallbacks, at the cost of an `npm
// root -g` per hook. That is a warning, not a failure.
func TestVanishedShimTargetIsAWarning(t *testing.T) {
	fakeGraftHome(t, struct {
		shim    bool
		baked   bool
		events  []string
		mcp     bool
		version string
	}{shim: true, baked: false, events: allEvents(), mcp: true})

	s := InspectGraft(installed, "")
	if c := graftCheck(t, s, "Shim target"); c.State != CheckWarn {
		t.Errorf("state = %s, want warning", c.State)
	}
	if s.OK() {
		t.Error("a vanished shim target should not report as fully wired")
	}
}

// A shim baked against an older graft than the one installed: the hooks run
// the old code, silently.
func TestShimFromAnOlderGraftIsAWarning(t *testing.T) {
	fakeGraftHome(t, struct {
		shim    bool
		baked   bool
		events  []string
		mcp     bool
		version string
	}{shim: true, baked: true, events: allEvents(), mcp: true, version: "0.14.0"})

	s := InspectGraft(installed, "")
	c := graftCheck(t, s, "Shim target")
	if c.State != CheckWarn {
		t.Fatalf("state = %s, want warning", c.State)
	}
	if !strings.Contains(c.Detail, "0.14.0") || !strings.Contains(c.Detail, "0.18.0") {
		t.Errorf("the detail does not contrast the two versions: %s", c.Detail)
	}
	if s.ShimVersion != "0.14.0" {
		t.Errorf("ShimVersion = %q, want 0.14.0", s.ShimVersion)
	}
}

func TestMissingMCPRegistrationIsAWarning(t *testing.T) {
	fakeGraftHome(t, struct {
		shim    bool
		baked   bool
		events  []string
		mcp     bool
		version string
	}{shim: true, baked: true, events: allEvents()})

	s := InspectGraft(installed, "")
	c := graftCheck(t, s, "MCP server")
	if c.State != CheckWarn {
		t.Errorf("state = %s, want warning — the hooks still fire without it", c.State)
	}
	if !c.Fixable {
		t.Error("`graft init` writes mcpServers.graft, so this is fixable")
	}
}

// No graft on the machine is not something this dialog can repair, and the
// button must not offer to.
func TestNoGraftIsNotFixable(t *testing.T) {
	fakeGraftHome(t, struct {
		shim    bool
		baked   bool
		events  []string
		mcp     bool
		version string
	}{shim: true, baked: true, events: allEvents(), mcp: true})

	s := InspectGraft(GraftVersion{Found: false, Err: "executable file not found in $PATH"}, "")
	c := graftCheck(t, s, "graft")
	if c.State != CheckBad {
		t.Errorf("state = %s, want problem", c.State)
	}
	if c.Fixable {
		t.Error("`graft init` cannot install graft itself")
	}
	if s.Fixable() {
		t.Error("nothing else is wrong, so the Fix button must stay dark")
	}
}

// An unparseable settings.json is a refusal, not a repair: graft will not
// touch a file it cannot parse, so a Fix button that promised to would lie.
func TestUnparseableSettingsIsNotFixable(t *testing.T) {
	f := fakeGraftHome(t, struct {
		shim    bool
		baked   bool
		events  []string
		mcp     bool
		version string
	}{shim: true, baked: true, events: allEvents(), mcp: true})
	mustWrite(t, filepath.Join(f.dir, "settings.json"), "{ not json")

	s := InspectGraft(installed, "")
	c := graftCheck(t, s, "Hook entries")
	if c.State != CheckBad {
		t.Errorf("state = %s, want problem", c.State)
	}
	if c.Fixable {
		t.Error("graft skips an unparseable settings file, so this is not fixable from here")
	}
}

func TestGraftFixArgvNamesTheRepo(t *testing.T) {
	got := GraftFixArgv("/home/me/git/nexus")
	for _, want := range []string{"init", "/home/me/git/nexus", "--no-build", "--no-agents", "--yes"} {
		if !strings.Contains(got, want) {
			t.Errorf("GraftFixArgv = %q, missing %q", got, want)
		}
	}
}

func TestGraftReportListsEveryCheck(t *testing.T) {
	fakeGraftHome(t, struct {
		shim    bool
		baked   bool
		events  []string
		mcp     bool
		version string
	}{shim: true, baked: true, events: allEvents(), mcp: true})

	s := InspectGraft(installed, "")
	r := s.Report()
	for _, c := range s.Checks {
		if !strings.Contains(r, c.Name) {
			t.Errorf("the report does not mention %q:\n%s", c.Name, r)
		}
	}
}

// A hook command that resolves the shim through a variable names no file this
// process can stat, and must not be reported as a broken absolute path.
func TestProjectDirShimPathIsSkipped(t *testing.T) {
	if p := shimPathIn(`node "${CLAUDE_PROJECT_DIR}/.claude/helpers/` + GraftShimName + `" prompt`); p != "" {
		t.Errorf("shimPathIn returned %q for a variable path, want none", p)
	}
	if p := shimPathIn(`node "/a/b/` + GraftShimName + `" prompt`); p != "/a/b/"+GraftShimName {
		t.Errorf("shimPathIn = %q", p)
	}
}

// addPreToolUse merges a PreToolUse entry into a settings.json, the way
// `graphify install --platform claude` does alongside graft's own hooks.
func addPreToolUse(t *testing.T, settings, command string) {
	t.Helper()
	b, err := os.ReadFile(settings)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	hooks, _ := doc["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
		doc["hooks"] = hooks
	}
	hooks["PreToolUse"] = []any{map[string]any{
		"matcher": "Read|Glob",
		"hooks":   []any{map[string]any{"type": "command", "command": command}},
	}}
	out, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, settings, string(out))
}

// `graft init` resolves the user-level wiring from $HOME and ignores
// CLAUDE_CONFIG_DIR, so on a board with another account selected it cannot
// repair what this report read. Nothing may be offered as fixable.
func TestGraftInitCannotFixAnotherAccount(t *testing.T) {
	f := fakeGraftHome(t, struct {
		shim    bool
		baked   bool
		events  []string
		mcp     bool
		version string
	}{shim: true, baked: true, events: allEvents(), mcp: true})

	work := filepath.Join(f.home, ".claude-work")
	mustWrite(t, filepath.Join(work, "settings.json"), `{}`)

	s := InspectGraft(installed, work)
	if s.InitWrites {
		t.Fatalf("InitWrites = true for %s; graft init writes %s", work, s.InitDir)
	}
	if s.Fixable() {
		t.Error("Fixable() = true — the Fix button would run a command that writes another directory")
	}
	for _, c := range s.Checks {
		if c.Fixable {
			t.Errorf("check %q is still marked fixable", c.Name)
		}
	}
	scope := graftCheck(t, s, "Fix scope")
	if scope.State != CheckWarn || !strings.Contains(scope.Detail, f.dir) {
		t.Errorf("Fix scope = %+v, want a warning naming %s", scope, f.dir)
	}
}

// The same mismatch on a healthy account says nothing: there is nothing to
// fix, so there is no button to explain and no warning to add.
func TestGraftInitScopeIsSilentWhenNothingIsFixable(t *testing.T) {
	f := fakeGraftHome(t, struct {
		shim    bool
		baked   bool
		events  []string
		mcp     bool
		version string
	}{shim: true, baked: true, events: allEvents(), mcp: true})

	// A second account wired by hand to the same (existing) shim: everything
	// the inspection asks about is in place, from another directory.
	work := filepath.Join(f.home, ".claude-work")
	mustWrite(t, filepath.Join(work, "settings.json"), graftSettingsNaming(t, f.shim))

	s := InspectGraft(installed, work)
	if s.InitWrites {
		t.Fatal("InitWrites = true for a non-default account")
	}
	if !s.OK() {
		t.Fatalf("account is wired but reads unhealthy: %s", s.Report())
	}
	for _, c := range s.Checks {
		if c.Name == "Fix scope" {
			t.Error("a healthy account gained a Fix scope warning")
		}
	}
}

// graftSettingsNaming is a settings.json whose four graft hooks name one shim
// by absolute path — the wiring a second account gets by copying the first.
func graftSettingsNaming(t *testing.T, shim string) string {
	t.Helper()
	hooks := map[string]any{}
	for _, e := range graftEvents {
		hooks[e.Event] = []any{map[string]any{"hooks": []any{map[string]any{
			"type": "command", "command": `node "` + shim + `" x`,
		}}}}
	}
	b, err := json.Marshal(map[string]any{"hooks": hooks})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Strict mode is the one place the two integrations contradict each other: the
// guard blocks the first raw read of a session that graft's own SessionStart
// hint told the agent to start with `graft ask`.
func TestStrictHookGuardAlongsideGraftIsAWarning(t *testing.T) {
	f := fakeGraftHome(t, struct {
		shim    bool
		baked   bool
		events  []string
		mcp     bool
		version string
	}{shim: true, baked: true, events: allEvents(), mcp: true})
	addPreToolUse(t, filepath.Join(f.dir, "settings.json"), "/usr/bin/graphify hook-guard read --strict")

	c := graftCheck(t, InspectGraft(installed, ""), "graphify hook-guard")
	if c.State != CheckWarn {
		t.Errorf("state = %v, want a warning: %s", c.State, c.Detail)
	}
	if !strings.Contains(c.Detail, HookStrictVar) {
		t.Errorf("the warning does not name %s: %s", HookStrictVar, c.Detail)
	}
	if c.Fixable {
		t.Error("marked fixable — `graft init` does not write graphify's guard")
	}
}

// The advisory guard is the common case and is not a problem: both indexers'
// hints reach the agent and neither blocks a call.
func TestAdvisoryHookGuardAlongsideGraftIsOK(t *testing.T) {
	f := fakeGraftHome(t, struct {
		shim    bool
		baked   bool
		events  []string
		mcp     bool
		version string
	}{shim: true, baked: true, events: allEvents(), mcp: true})
	addPreToolUse(t, filepath.Join(f.dir, "settings.json"), "/usr/bin/graphify hook-guard read")

	s := InspectGraft(installed, "")
	if c := graftCheck(t, s, "graphify hook-guard"); c.State != CheckOK {
		t.Errorf("state = %v, want ok: %s", c.State, c.Detail)
	}
	if !s.OK() {
		t.Errorf("the group is no longer healthy: %s", s.Report())
	}
}

// The environment variable beats the flag baked into the installed command,
// in both directions — the same resolution graphify's own guard performs.
func TestHookGuardStrictEnvOverridesTheBakedFlag(t *testing.T) {
	f := fakeGraftHome(t, struct {
		shim    bool
		baked   bool
		events  []string
		mcp     bool
		version string
	}{shim: true, baked: true, events: allEvents(), mcp: true})
	addPreToolUse(t, filepath.Join(f.dir, "settings.json"), "/usr/bin/graphify hook-guard read --strict")

	t.Setenv(HookStrictVar, "0")
	if c := graftCheck(t, InspectGraft(installed, ""), "graphify hook-guard"); c.State != CheckOK {
		t.Errorf("%s=0 did not turn the warning off: %v %s", HookStrictVar, c.State, c.Detail)
	}
	t.Setenv(HookStrictVar, "1")
	addPreToolUse(t, filepath.Join(f.dir, "settings.json"), "/usr/bin/graphify hook-guard read")
	if c := graftCheck(t, InspectGraft(installed, ""), "graphify hook-guard"); c.State != CheckWarn {
		t.Errorf("%s=1 did not turn the warning on: %v %s", HookStrictVar, c.State, c.Detail)
	}
}

// A settings file with no graphify guard gets no row about one.
func TestNoHookGuardMeansNoRow(t *testing.T) {
	fakeGraftHome(t, struct {
		shim    bool
		baked   bool
		events  []string
		mcp     bool
		version string
	}{shim: true, baked: true, events: allEvents(), mcp: true})

	for _, c := range InspectGraft(installed, "").Checks {
		if c.Name == "graphify hook-guard" {
			t.Errorf("unexpected row: %+v", c)
		}
	}
}
