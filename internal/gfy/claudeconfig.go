package gfy

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// This file answers one question the board could not answer before: is the
// Claude Code on this machine actually wired up to graphify?
//
// The wiring is not something ggraphify owns. `graphify install --platform
// claude` writes all of it — the skill, its reference pages, and the pointer
// line in the user-level CLAUDE.md that makes an agent reach for the skill at
// all. What ggraphify can do is *read* those artefacts, say which of them are
// missing or stale, and offer the one command that puts them right.
//
// So this is a pure filesystem inspection: no subprocess, no network, cheap
// enough to re-run on a button. The fix is a normal supervised job like every
// other mutation the board performs.

// ClaudeConfigDirVar is Claude Code's own override for where ~/.claude lives.
// Honouring it matters: a board that checked ~/.claude on a machine whose
// agent reads somewhere else would report a broken install that is fine, or
// worse, a healthy one that is not.
const ClaudeConfigDirVar = "CLAUDE_CONFIG_DIR"

// ClaudeSkillName is the directory `graphify install` writes the skill into,
// under <config dir>/skills/.
const ClaudeSkillName = "graphify"

// ClaudeSkillVersionFile is the stamp graphify leaves beside the skill. It is
// the authoritative answer to "which graphify wrote this skill", and comparing
// it against the installed package is how skew is detected without having to
// parse a warning line out of a subprocess's stderr.
const ClaudeSkillVersionFile = ".graphify_version"

// ClaudePointer is the substring the user-level CLAUDE.md carries once the
// skill is installed. Without it an agent has the skill on disk and no reason
// to look at it.
const ClaudePointer = "skills/graphify/SKILL.md"

// CheckState is how one check came out.
type CheckState int

// The three outcomes. Warn is deliberately distinct from Bad: a stale skill
// still works, a missing one does not, and collapsing the two would make the
// summary useless for deciding whether to act now.
const (
	CheckOK CheckState = iota
	CheckWarn
	CheckBad
)

func (s CheckState) String() string {
	switch s {
	case CheckWarn:
		return "warning"
	case CheckBad:
		return "problem"
	default:
		return "ok"
	}
}

// Glyph is the leading mark in the settings row and in the copied report.
// Shape carries the meaning as well as colour does, the same rule the board's
// state dots follow.
func (s CheckState) Glyph() string {
	switch s {
	case CheckWarn:
		return "△"
	case CheckBad:
		return "✘"
	default:
		return "✔"
	}
}

// Check is one thing that was looked at.
type Check struct {
	Name   string
	State  CheckState
	Detail string
	// Fixable is true when `graphify install --platform claude` is what
	// repairs this. A missing graphify or a missing Claude Code is not
	// fixable from here, and a button that pretended otherwise would run a
	// command that cannot help.
	Fixable bool
}

// ClaudeSetup is the whole inspection.
type ClaudeSetup struct {
	// Dir is the Claude Code configuration directory that was inspected.
	Dir string
	// SkillDir is where the graphify skill lives, or would.
	SkillDir string
	// SkillVersion is the graphify that wrote the installed skill, "" when
	// none is installed or the stamp is missing.
	SkillVersion string
	// Version is the graphify the board found on this machine.
	Version string
	// Account is the Claude Code login whose directory was inspected: which
	// of this machine's several ~/.claude* directories this report is about.
	Account ClaudeAccount

	Checks []Check
}

// ClaudeDir is the Claude Code configuration directory: $CLAUDE_CONFIG_DIR,
// else ~/.claude.
func ClaudeDir() string {
	if v := strings.TrimSpace(os.Getenv(ClaudeConfigDirVar)); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".claude"
	}
	return filepath.Join(home, ".claude")
}

// InspectClaude reads the Claude Code side of the integration and reports what
// it found. v is the board's graphify version probe; pass the one it already
// has rather than probing again.
//
// It inspects the directory the environment resolves to. Use InspectClaudeIn
// when a specific account is selected in settings — a board that checked
// ~/.claude while its jobs ran against ~/.claude-work would be answering about
// an install nobody is using.
func InspectClaude(v Version) ClaudeSetup { return InspectClaudeIn(v, "") }

// InspectClaudeIn is InspectClaude against one chosen account directory. A
// blank sel means the environment decides, exactly as before.
func InspectClaudeIn(v Version, sel string) ClaudeSetup {
	dir := ClaudeDirFor(sel)
	s := ClaudeSetup{
		Dir:      dir,
		SkillDir: filepath.Join(dir, "skills", ClaudeSkillName),
		Version:  v.Number,
		Account:  ClaudeAccountFor(sel),
	}

	// 1. graphify itself. Everything below is written by it, so a machine
	//    without it has nothing to check and nothing this dialog can fix.
	switch {
	case !v.Found:
		s.add("graphify", CheckBad, "Not found or would not run: "+v.Err+
			". Install it first — the skill is written by `graphify install`.", false)
	case !v.Supported():
		s.add("graphify", CheckWarn, v.Number+" at "+Bin()+
			" — ggraphify's command builders target "+BuiltAgainst+".", false)
	default:
		s.add("graphify", CheckOK, v.Number+" at "+Bin(), false)
	}

	// 2. The Claude Code CLI. Not required for the skill to be installed —
	//    the skill is read by whatever agent opens the directory — but it is
	//    required for the claude-cli backend, and it is what the user means
	//    by "the local Claude Code".
	if p := ClaudeCLI(); p == "" {
		s.add("Claude Code CLI", CheckBad,
			"Not found on this machine. Install it from https://claude.ai/code and run "+
				"`claude` once to authenticate.", false)
	} else if !claudeOnPath(p) {
		// graphify launches the CLI by the bare name, so this is a real
		// difference and not a cosmetic one — the board compensates by
		// prepending the directory to each job's PATH.
		s.add("Claude Code CLI", CheckWarn, p+
			" — found off $PATH. Jobs get its directory prepended to PATH; a shell will not.", false)
	} else {
		s.add("Claude Code CLI", CheckOK, p, false)
	}

	// 2b. The selected account's stored login. Claude Code can also hold its
	//     credential in the system keyring, so a directory without the file is
	//     a warning and not a failure — but on the machines where the file is
	//     how it works, an account directory without one is exactly why an
	//     extraction dies at the first request.
	switch cred := ClaudeCredential(dir); {
	case cred != "":
		s.add("Account", CheckOK, s.Account.Name+" — stored login at "+cred, false)
	case s.Account.Missing:
		s.add("Account", CheckBad, s.Account.Name+" — no directory at "+dir+
			". Pick another account in settings, or run `claude` once with "+
			ClaudeConfigDirVar+"="+dir+" to create it.", false)
	default:
		s.add("Account", CheckWarn, s.Account.Name+" at "+dir+
			" — no stored login file. Claude Code may be using the system keyring; if it "+
			"is not, run `"+ClaudeConfigDirVar+"="+dir+" claude` once to authenticate.", false)
	}

	// 3. The skill itself.
	skill := filepath.Join(s.SkillDir, "SKILL.md")
	fi, err := os.Stat(skill)
	switch {
	case err != nil || fi.IsDir():
		s.add("Agent skill", CheckBad,
			"Not installed — nothing at "+skill+". Claude Code has no graphify skill to invoke.", true)
	case fi.Size() == 0:
		s.add("Agent skill", CheckBad, "Empty file at "+skill+".", true)
	default:
		s.add("Agent skill", CheckOK, skill+" ("+humanSize(fi.Size())+")", false)
	}

	// 4. Its version stamp against the installed package. graphify prints the
	//    same complaint on every invocation when these disagree, so a stale
	//    skill is also noise in every job log until it is re-installed.
	s.SkillVersion = readVersionStamp(filepath.Join(s.SkillDir, ClaudeSkillVersionFile))
	switch {
	case err != nil:
		// No skill at all: check 3 already said so, and a second row saying
		// the version is unknown would be noise.
	case s.SkillVersion == "":
		s.add("Skill version", CheckWarn,
			"No "+ClaudeSkillVersionFile+" beside the skill — it was written by a graphify too old "+
				"to stamp it, or edited by hand.", true)
	case v.Found && s.SkillVersion != v.Number:
		s.add("Skill version", CheckWarn,
			"Skill is from graphify "+s.SkillVersion+"; the installed package is "+v.Number+".", true)
	default:
		s.add("Skill version", CheckOK, s.SkillVersion, false)
	}
	// graphify's own skew warning, when the probe caught one that the stamp
	// comparison above did not — an install for a *different* platform can be
	// the stale one.
	if v.SkillSkew != "" && !s.has("Skill version", CheckWarn) {
		s.add("Skill skew reported by graphify", CheckWarn, v.SkillSkew, true)
	}

	// 5. The reference pages the skill links to. A skill whose references are
	//    missing degrades quietly: the agent follows a link to nothing.
	if err == nil {
		refs := countMarkdown(filepath.Join(s.SkillDir, "references"))
		if refs == 0 {
			s.add("Skill references", CheckWarn,
				"No reference pages under "+filepath.Join(s.SkillDir, "references")+
					" — the skill links to pages that are not there.", true)
		} else {
			s.add("Skill references", CheckOK, Itoa(refs)+" pages", false)
		}
	}

	// 6. The pointer in the user-level CLAUDE.md. This is the one that
	//    actually decides whether an agent reaches for the skill, and it is
	//    the one most likely to be lost — a hand-edited CLAUDE.md drops it
	//    without anything else breaking.
	md := filepath.Join(dir, "CLAUDE.md")
	switch b, mdErr := os.ReadFile(md); {
	case mdErr != nil:
		s.add("CLAUDE.md pointer", CheckWarn,
			"No "+md+" — nothing tells Claude Code the graphify skill exists.", true)
	case !strings.Contains(string(b), ClaudePointer):
		s.add("CLAUDE.md pointer", CheckWarn,
			md+" does not mention "+ClaudePointer+" — the skill is installed but nothing points at it.", true)
	default:
		s.add("CLAUDE.md pointer", CheckOK, md, false)
	}

	return s
}

func (s *ClaudeSetup) add(name string, st CheckState, detail string, fixable bool) {
	s.Checks = append(s.Checks, Check{Name: name, State: st, Detail: detail, Fixable: fixable})
}

func (s ClaudeSetup) has(name string, st CheckState) bool {
	for _, c := range s.Checks {
		if c.Name == name && c.State == st {
			return true
		}
	}
	return false
}

// State is the worst outcome across every check, which is what the summary
// row shows.
func (s ClaudeSetup) State() CheckState { return worstCheck(s.Checks) }

// OK reports whether nothing needs attention.
func (s ClaudeSetup) OK() bool { return s.State() == CheckOK }

// Fixable reports whether re-running the installer would change anything.
func (s ClaudeSetup) Fixable() bool { return checksFixable(s.Checks) }

// Summary is the one-line verdict.
func (s ClaudeSetup) Summary() string {
	return summarizeChecks(s.Checks, "Claude Code is wired up to graphify on this machine.")
}

// FixArgv is the command that repairs what is repairable, for the button's
// tooltip and for the copied report.
func FixArgv() string { return Quote([]string{Bin(), "install", "--platform", "claude"}) }

// Report is the whole inspection as text, for the application log and for the
// clipboard — the artefact worth pasting into an issue or handing to an agent.
func (s ClaudeSetup) Report() string {
	var b strings.Builder
	b.WriteString("Claude Code ↔ graphify: ")
	b.WriteString(s.Summary())
	b.WriteString("\n  account: ")
	b.WriteString(s.Account.Name)
	b.WriteString("\n  config dir: ")
	b.WriteString(s.Dir)
	b.WriteByte('\n')
	for _, c := range s.Checks {
		b.WriteString("  ")
		b.WriteString(c.State.Glyph())
		b.WriteByte(' ')
		b.WriteString(c.Name)
		b.WriteString(": ")
		b.WriteString(c.Detail)
		b.WriteByte('\n')
	}
	if s.Fixable() {
		b.WriteString("  fix: ")
		b.WriteString(FixArgv())
		b.WriteByte('\n')
	}
	return b.String()
}

// claudeOnPath reports whether a bare `claude` lookup lands on this same
// binary.
func claudeOnPath(p string) bool {
	found, err := exec.LookPath(filepath.Base(p))
	return err == nil && sameFile(found, p)
}

func readVersionStamp(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func countMarkdown(dir string) int {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range ents {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
			n++
		}
	}
	return n
}

// count is "1 problem" / "2 problems", kept here because package gfy has no
// UI helpers and this is the only place it needs one.
func count(n int, noun string) string {
	if n == 1 {
		return Itoa(n) + " " + noun
	}
	return Itoa(n) + " " + noun + "s"
}

// humanSize is a compact byte count for the skill file's row. internal/board
// has the same helper, but board imports this package and not the other way
// round.
func humanSize(n int64) string {
	switch {
	case n >= 1<<20:
		return Itoa(int(n>>20)) + " MB"
	case n >= 1<<10:
		return Itoa(int(n>>10)) + " kB"
	default:
		return Itoa(int(n)) + " B"
	}
}
