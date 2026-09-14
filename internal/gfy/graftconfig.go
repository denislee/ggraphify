package gfy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// This file answers, for graft, the question claudeconfig.go answers for
// graphify: is the Claude Code on this machine actually wired up to use it?
//
// It is the same kind of question and it has the same failure mode — silence.
// A missing SessionStart hook, a shim whose baked package directory was
// removed by a node upgrade, an `mcpServers.graft` entry that a hand-edited
// ~/.claude.json lost: every one of them presents as an agent that simply
// never mentions graft, greps the source it was supposed to query, and burns
// the tokens the index exists to save. Nothing errors.
//
// So this is again a pure filesystem inspection — no subprocess, no network —
// of the artefacts `graft init` writes at USER level, the ones that apply to
// every project the CLI opens:
//
//	<config dir>/helpers/graft-hooks.cjs   the shim every hook command runs
//	<config dir>/settings.json             SessionStart / UserPromptSubmit / PostToolUse / Stop
//	~/.claude.json                         mcpServers.graft
//
// The repo-level half of graft's wiring (`.claude/settings.json`, `.mcp.json`
// and the skill inside a checkout) is deliberately not inspected here: it is a
// per-repository fact, it belongs to the board rather than to a machine-wide
// settings page, and graft's own hooks repair it whenever they run.

// GraftShimName is the file every graft hook command invokes, under
// <config dir>/helpers/.
const GraftShimName = "graft-hooks.cjs"

// GraftMCPFile is where Claude Code keeps the user-scope MCP registration, and
// where `graft init` writes `mcpServers.graft`. It sits beside the
// configuration directory rather than inside it.
const GraftMCPFile = ".claude.json"

// GraftMCPKey is the server name graft registers itself under.
const GraftMCPKey = "graft"

// GraftInitDir is where `graft init` writes the user-level wiring, and it is
// NOT the directory the rest of this file inspects.
//
// graft resolves it from the home directory alone — `installClaudeGlobal(home
// ?? homedir())` in its init, `join(home, '.claude', …)` in every target it
// derives — and reads CLAUDE_CONFIG_DIR only inside the hooks at runtime,
// never in the installer. graphify's own installer honours the variable, so
// the board's per-job CLAUDE_CONFIG_DIR reaches one of the two integrations
// and silently misses the other.
//
// Which matters because the board lets an account be selected: with
// ~/.claude-work in force, this inspection reads that directory, `graft init`
// writes ~/.claude, and a Fix button that ran it would leave the very checks
// that enabled it red. GraftSetup.InitWrites is how the report says so.
func GraftInitDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".claude"
	}
	return filepath.Join(home, ".claude")
}

// GraftInitTargets is the three user-level files `graft init` writes, which is
// what a dialog about running it must name — not the paths of the account
// being inspected, and not the shim some other account's hooks point at.
func GraftInitTargets() (shim, settings, mcp string) {
	dir := GraftInitDir()
	mcp = GraftMCPFile
	if home, err := os.UserHomeDir(); err == nil {
		mcp = filepath.Join(home, GraftMCPFile)
	}
	return filepath.Join(dir, "helpers", GraftShimName),
		filepath.Join(dir, "settings.json"),
		mcp
}

// graftEvents are the four hook events a user-level `graft init` wires, and
// what each one is for. All four are written by the same install, so a
// settings file carrying some of them and not others is a partial or
// hand-edited wiring rather than a choice.
var graftEvents = []struct{ Event, What string }{
	{"SessionStart", "tells the agent this repo is indexed, at the top of the session"},
	{"UserPromptSubmit", "puts the graph's starting points under the prompt"},
	{"PostToolUse", "refreshes the index after an edit, and counts the tokens saved"},
	{"Stop", "flushes the session's savings record"},
}

// GraftSetup is the whole inspection.
type GraftSetup struct {
	// Dir is the Claude Code configuration directory that was inspected.
	Dir string
	// Account is the login that directory belongs to, so the report says which
	// of several ~/.claude* it is about.
	Account ClaudeAccount

	Shim     string // <Dir>/helpers/graft-hooks.cjs
	Settings string // <Dir>/settings.json
	MCP      string // the .claude.json that was read

	// InitDir is where `graft init` would write, and InitWrites whether that
	// is the directory inspected here. When it is not, nothing `graft init`
	// does can repair this account and no check is reported as fixable.
	InitDir    string
	InitWrites bool

	// Bin and Version are the graft the board found on this machine.
	Bin     string
	Version string
	// ShimVersion is the graft whose dist/ directory the installed shim bakes
	// in, when that directory is still there to be read.
	ShimVersion string
	// Events is the hook events that actually call the shim.
	Events []string

	Checks []Check
}

// InspectGraft reads the Claude Code side of the graft integration. v is the
// board's graft probe; pass the one it already has rather than shelling out
// again. sel is the selected account directory, blank for the environment's.
func InspectGraft(v GraftVersion, sel string) GraftSetup {
	dir := ClaudeDirFor(sel)
	s := GraftSetup{
		Dir:      dir,
		Account:  ClaudeAccountFor(sel),
		Shim:     filepath.Join(dir, "helpers", GraftShimName),
		Settings: filepath.Join(dir, "settings.json"),
		Bin:      v.Bin,
		Version:  v.Number,
		InitDir:  GraftInitDir(),
	}
	s.InitWrites = sameDirSpelling(s.InitDir, dir)

	// 1. graft itself. Everything below is written by `graft init` and run by
	//    graft, so a machine without it has nothing this group can repair.
	switch {
	case !v.Found:
		detail := "Not found or would not run"
		if v.Err != "" {
			detail += ": " + v.Err
		}
		s.add("graft", CheckBad, detail+". Install it with `npm i -g @nanonets/graft`, "+
			"or point "+GraftBinVar+" at it.", false)
	default:
		s.add("graft", CheckOK, v.Number+" at "+v.Bin, false)
	}

	// The hook entries are read first, because they decide which shim file is
	// the one that matters. A config directory of its own is the common case,
	// but a settings.json that names another account's shim by absolute path
	// is wired correctly — and a board that insisted on the canonical location
	// would call a working install broken.
	events, named, sErr := graftHookEvents(s.Settings)
	s.Events = events
	if p := firstExisting(named); p != "" {
		s.Shim = p
	}

	// 2. The shim. Every hook command is `node "<shim>" <verb>`, so a missing
	//    or non-executable one silently disables all four at once.
	fi, err := os.Stat(s.Shim)
	switch {
	case err != nil || fi.IsDir():
		detail := "Not installed — nothing at " + s.Shim + ". Claude Code has no graft hook to run."
		if len(named) > 0 {
			detail = s.Settings + " runs " + strings.Join(named, ", ") +
				", and no such file exists — every graft hook is a no-op."
		}
		s.add("Hooks shim", CheckBad, detail, true)
	case fi.Size() == 0:
		s.add("Hooks shim", CheckBad, "Empty file at "+s.Shim+".", true)
	default:
		s.add("Hooks shim", CheckOK, s.Shim+" ("+humanSize(fi.Size())+")", false)
	}

	// 3. What the shim points at. It bakes the absolute dist/claude of the
	//    graft that wrote it; a node upgrade, an nvm version bump or an
	//    `npm uninstall` moves that directory out from under it. The shim does
	//    have fallbacks, but the last of them shells out to `npm root -g` on
	//    every hook — so a stale bake is a real cost, not a cosmetic one.
	if err == nil {
		switch baked := graftBakedDir(s.Shim); {
		case baked == "":
			s.add("Shim target", CheckWarn, "Could not read the graft directory baked into "+
				s.Shim+" — it was written by a graft too old to bake one, or edited by hand.", true)
		case !dirExists(baked):
			s.add("Shim target", CheckWarn, baked+" no longer exists — the shim falls back to "+
				"`npm root -g` on every hook. Re-running `graft init` re-bakes it.", true)
		default:
			s.ShimVersion = graftPackageVersion(baked)
			detail := baked
			if s.ShimVersion != "" {
				detail = "graft " + s.ShimVersion + " at " + baked
			}
			if s.ShimVersion != "" && v.Found && s.ShimVersion != v.Number {
				s.add("Shim target", CheckWarn, detail+", but the graft on this machine is "+
					v.Number+" — the hooks run the older one.", true)
			} else {
				s.add("Shim target", CheckOK, detail, false)
			}
		}
	}

	// 4. The hook entries themselves. This is the check that actually decides
	//    whether an agent hears about graft: the shim can be perfectly
	//    installed and never invoked.
	switch {
	case sErr != nil && os.IsNotExist(sErr):
		s.add("Hook entries", CheckBad, "No "+s.Settings+
			" — nothing tells Claude Code to run graft's hooks.", true)
	case sErr != nil:
		s.add("Hook entries", CheckBad, s.Settings+" could not be read as JSON: "+sErr.Error()+
			". graft will not touch a file it cannot parse; fix the JSON first.", false)
	case len(events) == 0:
		s.add("Hook entries", CheckBad, s.Settings+" has no hook that runs "+GraftShimName+
			" — the shim is installed and never invoked.", true)
	case len(events) < len(graftEvents):
		s.add("Hook entries", CheckWarn, strings.Join(events, ", ")+" wired in "+s.Settings+
			"; missing "+strings.Join(graftMissingEvents(events), ", ")+".", true)
	default:
		s.add("Hook entries", CheckOK, strings.Join(events, ", "), false)
	}

	// 5. The MCP registration, which is how the agent calls graft as tools
	//    rather than as a shell command. Its absence is a warning and not a
	//    failure: the CLI still works and the hooks still fire, the agent just
	//    has to spend a Bash call to reach it.
	mcp, found := graftMCPRegistered(dir)
	s.MCP = mcp
	switch {
	case found:
		s.add("MCP server", CheckOK, "mcpServers."+GraftMCPKey+" in "+mcp, false)
	case mcp == "":
		s.add("MCP server", CheckWarn, "No "+GraftMCPFile+" next to "+dir+
			" — graft's tools are not registered, so the agent can only reach it through Bash.", true)
	default:
		s.add("MCP server", CheckWarn, mcp+" has no mcpServers."+GraftMCPKey+
			" — graft's tools are not registered, so the agent can only reach it through Bash.", true)
	}

	// 6. The other indexer's hooks in the same settings file. Both installers
	//    tell the agent to reach for their own index first, and they compose
	//    silently — nothing errors, the two hints simply arrive together.
	//    Advisory, that is merely noise. Strict is not: see graftGuardCheck.
	if len(events) > 0 {
		if name, st, detail := graftGuardCheck(s.Settings); name != "" {
			s.add(name, st, detail, false)
		}
	}

	// 7. Whether the Fix button can reach this account at all. Done last,
	//    because it withdraws the fixability every check above claimed.
	s.applyInitScope()

	return s
}

// applyInitScope withdraws Fixable from every check when `graft init` writes
// somewhere other than the directory inspected, and says why.
//
// Without it the graft group is a trap: on a board with ~/.claude-work
// selected, the shim and hook checks go red, both are marked fixable, the Fix
// button runs `graft init`, graft writes ~/.claude, and the next Check shows
// exactly the same two red rows. A button that cannot change its own verdict
// is worse than no button.
//
// The row is added only when something was fixable — an account that is
// healthy, or broken in a way `graft init` never repaired anyway, has nothing
// to explain and must not gain a warning it did not have.
func (s *GraftSetup) applyInitScope() {
	if s.InitWrites || !checksFixable(s.Checks) {
		return
	}
	for i := range s.Checks {
		s.Checks[i].Fixable = false
	}
	s.add("Fix scope", CheckWarn, "`graft init` resolves the user-level wiring from the home "+
		"directory and ignores "+ClaudeConfigDirVar+", so it would write "+s.InitDir+
		" and not "+s.Dir+" — running it cannot repair the account inspected here. "+
		"Select the default account in settings, or copy graft's hook entries from "+
		filepath.Join(s.InitDir, "settings.json")+" into "+s.Settings+" by hand "+
		"(the shim may be named by absolute path from either directory).", false)
}

// graftGuardCheck reports how graphify's PreToolUse guard sits alongside
// graft's hooks in one settings file.
//
// The two integrations are wired independently and merge cleanly — graphify
// owns PreToolUse, graft owns SessionStart / UserPromptSubmit / PostToolUse /
// Stop — so nothing here is broken. What this row is about is the one case
// where they actually contradict each other: in strict mode the guard blocks
// the session's first raw read until `graphify query` has run, and it decides
// that from graphify-out/cache/last_query_stamp, which a session oriented
// through `graft ask` never touches. The agent is told to reach for graft
// first and then blocked for having done so.
//
// An empty name means there is nothing to say: no guard in this file.
func graftGuardCheck(settings string) (name string, st CheckState, detail string) {
	present, strict := graphifyGuard(settings)
	if !present {
		return "", CheckOK, ""
	}
	if !hookStrictEnabled(strict) {
		return "graphify hook-guard", CheckOK, "advisory in " + settings +
			" — both indexers' hints reach the agent and neither blocks a tool call."
	}
	return "graphify hook-guard", CheckWarn, "strict in " + settings +
		" — it blocks the session's first raw read until `graphify query` has run, and a session " +
		"oriented with `graft ask` does not refresh graphify-out/cache/last_query_stamp, so " +
		"following graft's own SessionStart hint is what trips it. Set " + HookStrictVar +
		"=0 for the agent's environment, or re-install the guard without --strict."
}

// HookStrictVar is graphify's runtime override for the guard's strict mode. It
// beats the flag baked into the installed hook command, in both directions.
const HookStrictVar = "GRAPHIFY_HOOK_STRICT"

// graphifyGuard reports whether a settings file runs graphify's hook-guard,
// and whether the installed command carries --strict.
//
// The match is on `hook-guard`, the verb, rather than on the executable path:
// graphify writes the absolute path of whichever interpreter installed it, and
// on a machine with several that path is routinely not the graphify the board
// found.
func graphifyGuard(settings string) (present, strict bool) {
	b, err := os.ReadFile(settings)
	if err != nil {
		return false, false
	}
	var doc struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if json.Unmarshal(b, &doc) != nil {
		return false, false
	}
	for _, group := range doc.Hooks["PreToolUse"] {
		for _, h := range group.Hooks {
			if !strings.Contains(h.Command, "hook-guard") {
				continue
			}
			present = true
			if strings.Contains(h.Command, "--strict") {
				strict = true
			}
		}
	}
	return present, strict
}

// hookStrictEnabled resolves strict mode the way graphify's own guard does:
// the environment variable overrides the baked-in flag in both directions, and
// an unset or unparseable value defers to the flag.
func hookStrictEnabled(flag bool) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(HookStrictVar))) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}
	return flag
}

func (s *GraftSetup) add(name string, st CheckState, detail string, fixable bool) {
	s.Checks = append(s.Checks, Check{Name: name, State: st, Detail: detail, Fixable: fixable})
}

// State is the worst outcome across every check.
func (s GraftSetup) State() CheckState { return worstCheck(s.Checks) }

// OK reports whether nothing needs attention.
func (s GraftSetup) OK() bool { return s.State() == CheckOK }

// Fixable reports whether re-running `graft init` would change anything.
func (s GraftSetup) Fixable() bool { return checksFixable(s.Checks) }

// Summary is the one-line verdict.
func (s GraftSetup) Summary() string {
	return summarizeChecks(s.Checks, "Claude Code is wired up to graft on this machine.")
}

// Report is the whole inspection as text, for the application log.
func (s GraftSetup) Report() string {
	var b strings.Builder
	b.WriteString("Claude Code ↔ graft: ")
	b.WriteString(s.Summary())
	b.WriteString("\n  account: ")
	b.WriteString(s.Account.Name)
	b.WriteString("\n  config dir: ")
	b.WriteString(s.Dir)
	b.WriteByte('\n')
	if !s.InitWrites {
		b.WriteString("  `graft init` writes: ")
		b.WriteString(s.InitDir)
		b.WriteString(" (it ignores " + ClaudeConfigDirVar + ")\n")
	}
	for _, c := range s.Checks {
		b.WriteString("  ")
		b.WriteString(c.State.Glyph())
		b.WriteByte(' ')
		b.WriteString(c.Name)
		b.WriteString(": ")
		b.WriteString(c.Detail)
		b.WriteByte('\n')
	}
	return b.String()
}

// GraftFixArgv is the command that repairs the wiring, for a tooltip and for
// the confirm dialog. It needs a repository because `graft init` has no
// user-level-only mode: the writes into ~/.claude are made alongside the
// writes into whichever repo it is pointed at.
func GraftFixArgv(repo string) string { return Quote(Argv("graft-init", Params{Repo: repo})) }

// graftBakedDir pulls the dist/claude directory the shim was written with out
// of its `const BAKED = "…"` line. Reading the shim rather than guessing is
// the point: the path is absolute and machine-specific, and the whole question
// is whether *that* path is still there.
func graftBakedDir(shim string) string {
	b, err := os.ReadFile(shim)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "const BAKED") {
			continue
		}
		i := strings.Index(line, "\"")
		j := strings.LastIndex(line, "\"")
		if i < 0 || j <= i {
			return ""
		}
		return line[i+1 : j]
	}
	return ""
}

// graftPackageVersion reads the version out of the package.json two levels
// above a dist/claude directory, which is where the shim's own resolver looks
// for it.
func graftPackageVersion(distClaude string) string {
	b, err := os.ReadFile(filepath.Join(distClaude, "..", "..", "package.json"))
	if err != nil {
		return ""
	}
	var pkg struct {
		Version string `json:"version"`
	}
	if json.Unmarshal(b, &pkg) != nil {
		return ""
	}
	return pkg.Version
}

// graftHookEvents reports which hook events in a settings.json run the graft
// shim, in graft's own order, and which shim files they name.
//
// The match is on the shim's file NAME, never on a path: the command carries
// whatever absolute path the install baked into it, which is routinely another
// account's directory or a machine the file was copied from. Whether the named
// file exists is a separate question, and a separate check answers it.
func graftHookEvents(settings string) (events, named []string, err error) {
	b, readErr := os.ReadFile(settings)
	if readErr != nil {
		return nil, nil, readErr
	}
	var doc struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, nil, err
	}
	seen := map[string]bool{}
	for _, e := range graftEvents {
		wired := false
		for _, group := range doc.Hooks[e.Event] {
			for _, h := range group.Hooks {
				if !strings.Contains(h.Command, GraftShimName) {
					continue
				}
				wired = true
				if p := shimPathIn(h.Command); p != "" && !seen[p] {
					seen[p] = true
					named = append(named, p)
				}
			}
		}
		if wired {
			events = append(events, e.Event)
		}
	}
	return events, named, nil
}

// shimPathIn pulls the shim's path out of a hook command such as
//
//	node "/home/dns/.claude/helpers/graft-hooks.cjs" session-start
//
// Commands that resolve the shim through a variable — the repo-level form is
// ${CLAUDE_PROJECT_DIR}/.claude/helpers/graft-hooks.cjs — name no file this
// process can stat, and are deliberately skipped rather than half-expanded.
func shimPathIn(command string) string {
	i := strings.Index(command, GraftShimName)
	if i < 0 {
		return ""
	}
	end := i + len(GraftShimName)
	start := i
	for start > 0 {
		c := command[start-1]
		if c == '"' || c == '\'' || c == ' ' || c == '\t' {
			break
		}
		start--
	}
	p := command[start:end]
	if strings.Contains(p, "$") {
		return ""
	}
	return expandHome(p)
}

// expandHome resolves a leading ~/ the way a shell would, because that is how
// a hand-written hook command spells a path in the home directory.
func expandHome(p string) string {
	if !strings.HasPrefix(p, "~/") {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	return filepath.Join(home, p[2:])
}

// firstExisting is the first of these paths that is a readable file.
func firstExisting(paths []string) string {
	for _, p := range paths {
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() {
			return p
		}
	}
	return ""
}

// graftMissingEvents is the complement of what was found, so a partial wiring
// says what is missing rather than what is present.
func graftMissingEvents(have []string) []string {
	seen := map[string]bool{}
	for _, e := range have {
		seen[e] = true
	}
	var out []string
	for _, e := range graftEvents {
		if !seen[e.Event] {
			out = append(out, e.Event)
		}
	}
	return out
}

// graftMCPRegistered looks for the user-scope MCP entry. It returns the file
// it read and whether graft is registered in it.
//
// Two locations, in order: beside the configuration directory (which is where
// Claude Code keeps it when CLAUDE_CONFIG_DIR points somewhere unusual) and
// ~/.claude.json (which is the one `graft init` writes, unconditionally). A
// board that looked only where graft writes would report a healthy custom
// install as broken, and one that looked only beside the directory would miss
// the file graft actually created.
func graftMCPRegistered(dir string) (string, bool) {
	var candidates []string
	if dir != "" {
		candidates = append(candidates,
			filepath.Join(dir, GraftMCPFile),
			filepath.Join(filepath.Dir(dir), GraftMCPFile))
	}
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates, filepath.Join(home, GraftMCPFile))
	}

	first := ""
	seen := map[string]bool{}
	for _, path := range candidates {
		if seen[path] {
			continue
		}
		seen[path] = true
		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if first == "" {
			first = path
		}
		// Only the mcpServers key is decoded. ~/.claude.json also holds the
		// login's own state, including credentials, and this has no business
		// materialising any of it.
		var doc struct {
			MCPServers map[string]json.RawMessage `json:"mcpServers"`
		}
		if json.Unmarshal(b, &doc) != nil {
			continue
		}
		if _, ok := doc.MCPServers[GraftMCPKey]; ok {
			return path, true
		}
	}
	return first, false
}

func dirExists(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}
