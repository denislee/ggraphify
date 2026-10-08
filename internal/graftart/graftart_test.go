package graftart

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
)

func TestExcludes(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not on PATH")
	}

	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	write := func(rel, content string) {
		t.Helper()
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	git("init", "-q")
	write("AGENTS.md", "# agents") // tracked: the repository's own copy
	git("add", "AGENTS.md")
	write(".mcp.json", "{}") // untracked: graft's
	// opencode.json is deliberately never created.

	want := []string{".claude/skills/graft/", "/.mcp.json"}
	if got := Excludes(dir); !reflect.DeepEqual(got, want) {
		t.Fatalf("Excludes = %v, want %v", got, want)
	}

	if Untracked(dir, "opencode.json") {
		t.Error("a file that does not exist must not read as untracked")
	}
	if Untracked(dir, "AGENTS.md") {
		t.Error("a tracked root file must not be excluded")
	}
	if !Untracked(dir, ".mcp.json") {
		t.Error("an untracked root file must be excluded")
	}
}
