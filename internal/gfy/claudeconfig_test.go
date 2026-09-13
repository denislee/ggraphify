package gfy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeClaudeHome writes what `graphify install --platform claude` writes, so
// the inspection has something healthy to find. skillVersion is the stamp;
// pass "" to leave the file out entirely.
func fakeClaudeHome(t *testing.T, skillVersion string, pointer bool) string {
	t.Helper()
	dir := t.TempDir()
	skill := filepath.Join(dir, "skills", ClaudeSkillName)
	if err := os.MkdirAll(filepath.Join(skill, "references"), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(p, body string) {
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(skill, "SKILL.md"), "# graphify skill\n")
	write(filepath.Join(skill, "references", "query.md"), "# query\n")
	if skillVersion != "" {
		write(filepath.Join(skill, ClaudeSkillVersionFile), skillVersion+"\n")
	}
	if pointer {
		write(filepath.Join(dir, "CLAUDE.md"),
			"# graphify\n- **graphify** (`~/.claude/"+ClaudePointer+"`) - any input to knowledge graph.\n")
	}
	t.Setenv(ClaudeConfigDirVar, dir)
	return dir
}

func check(t *testing.T, s ClaudeSetup, name string) Check {
	t.Helper()
	for _, c := range s.Checks {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("no check named %q in %v", name, s.Checks)
	return Check{}
}

func TestInspectClaudeHealthy(t *testing.T) {
	fakeClaudeHome(t, BuiltAgainst, true)
	s := InspectClaude(Version{Found: true, Number: BuiltAgainst})

	for _, name := range []string{"Agent skill", "Skill version", "Skill references", "CLAUDE.md pointer"} {
		if c := check(t, s, name); c.State != CheckOK {
			t.Errorf("%s: %s — %s", name, c.State, c.Detail)
		}
	}
	if s.Fixable() {
		t.Error("a healthy install reported as fixable, so the Fix button would be live for nothing")
	}
	if s.SkillVersion != BuiltAgainst {
		t.Errorf("SkillVersion = %q, want %q", s.SkillVersion, BuiltAgainst)
	}
}

// The case the group exists for: graphify was upgraded and nobody re-ran the
// installer, so the skill on disk is from an older release.
func TestInspectClaudeStaleSkill(t *testing.T) {
	fakeClaudeHome(t, "0.9.35", true)
	s := InspectClaude(Version{Found: true, Number: "0.9.58"})

	c := check(t, s, "Skill version")
	if c.State != CheckWarn {
		t.Fatalf("stale skill reported as %s", c.State)
	}
	if !c.Fixable || !s.Fixable() {
		t.Fatal("a stale skill is exactly what re-running the installer fixes")
	}
	if !strings.Contains(c.Detail, "0.9.35") || !strings.Contains(c.Detail, "0.9.58") {
		t.Errorf("detail names neither version: %q", c.Detail)
	}
}

func TestInspectClaudeNoSkill(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(ClaudeConfigDirVar, dir)
	s := InspectClaude(Version{Found: true, Number: BuiltAgainst})

	if c := check(t, s, "Agent skill"); c.State != CheckBad || !c.Fixable {
		t.Fatalf("missing skill reported as %s (fixable=%v)", c.State, c.Fixable)
	}
	// With no skill at all, the version row would be noise on top of a row
	// that already said the whole thing is missing.
	for _, c := range s.Checks {
		if c.Name == "Skill version" {
			t.Error("a second row about the version of a skill that is not installed")
		}
	}
	if s.OK() {
		t.Error("OK() true with no skill installed")
	}
}

// A hand-edited CLAUDE.md that lost its pointer line is the quiet failure:
// the skill is on disk and no agent ever reaches for it.
func TestInspectClaudeMissingPointer(t *testing.T) {
	fakeClaudeHome(t, BuiltAgainst, false)
	s := InspectClaude(Version{Found: true, Number: BuiltAgainst})

	c := check(t, s, "CLAUDE.md pointer")
	if c.State != CheckWarn || !c.Fixable {
		t.Fatalf("missing pointer reported as %s (fixable=%v)", c.State, c.Fixable)
	}
	if check(t, s, "Agent skill").State != CheckOK {
		t.Error("the skill itself is fine and should not be reported otherwise")
	}
}

// Nothing the installer writes can help when graphify itself is missing, and
// a Fix button that ran it anyway would be a lie.
func TestInspectClaudeNoGraphify(t *testing.T) {
	fakeClaudeHome(t, BuiltAgainst, true)
	s := InspectClaude(Version{Found: false, Err: "exec: \"graphify\": not found"})

	c := check(t, s, "graphify")
	if c.State != CheckBad {
		t.Fatalf("missing graphify reported as %s", c.State)
	}
	if c.Fixable {
		t.Fatal("a missing graphify must not be offered as installer-fixable")
	}
}

func TestClaudeSetupReport(t *testing.T) {
	fakeClaudeHome(t, "0.9.35", true)
	s := InspectClaude(Version{Found: true, Number: "0.9.58"})
	r := s.Report()
	if !strings.Contains(r, "Claude Code ↔ graphify") || !strings.Contains(r, s.Dir) {
		t.Fatalf("report is missing its header:\n%s", r)
	}
	if !strings.Contains(r, "install --platform claude") {
		t.Fatalf("a fixable report must name the fix:\n%s", r)
	}
}

func TestClaudeDirHonoursOverride(t *testing.T) {
	t.Setenv(ClaudeConfigDirVar, "/somewhere/else")
	if got := ClaudeDir(); got != "/somewhere/else" {
		t.Fatalf("ClaudeDir = %q", got)
	}
}
