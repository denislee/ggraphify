// Package graftart describes the files graft writes into every checkout it
// wires for its agent hosts that graphify must not index.
//
// Three of them sit at the checkout root — AGENTS.md, .mcp.json and
// opencode.json — and the fourth is graft's copied skill under
// .claude/skills/graft/. Untracked and usually not gitignored, they put the
// same ~10 nodes into ~120 graphs and make ggq lookups of "graft" ambiguous. A
// root file the repository itself tracks (pulumi tracks AGENTS.md) is real
// content and stays in.
package graftart

import (
	"os"
	"os/exec"
	"path/filepath"
)

// SkillDir is where graft copies its agent skill, relative to the checkout
// root. The whole directory is graft's, so it is excluded as one path rather
// than file by file.
const SkillDir = ".claude/skills/graft"

// RootFiles are the files graft writes at the checkout root. Only the ones the
// repository does not track are graft's to exclude.
var RootFiles = []string{"AGENTS.md", ".mcp.json", "opencode.json"}

// Untracked reports whether rel under repo exists on disk and is not tracked by
// git. A file that is not there at all is not untracked, so a repository that
// has never been graft-wired contributes no exclusions.
//
// A checkout that is not a git repository makes `git ls-files` fail, which
// reads as untracked — the conservative answer for a file that is present but
// whose ownership cannot be established.
func Untracked(repo, rel string) bool {
	if _, err := os.Stat(filepath.Join(repo, rel)); err != nil {
		return false
	}
	return exec.Command("git", "-C", repo, "ls-files", "--error-unmatch", "--", rel).Run() != nil
}

// Excludes are the --exclude patterns that keep graft's agent-host files out of
// graphify's extraction. The skill directory always comes first; the root files
// follow in RootFiles order, and only those Untracked in this checkout.
//
// A leading slash anchors a pattern at the checkout root, so `/.mcp.json` is
// the root file and a nested .mcp.json is untouched.
func Excludes(repo string) []string {
	out := []string{SkillDir + "/"}
	for _, f := range RootFiles {
		if Untracked(repo, f) {
			out = append(out, "/"+f)
		}
	}
	return out
}
