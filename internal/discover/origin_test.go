package discover

import (
	"os"
	"path/filepath"
	"testing"
)

// writeConfig writes a git config file into dir (a .git directory).
func writeConfig(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The remote URL has half a dozen spellings and they all name one repository.
func TestNormalizeOrigin(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"git@github.com:nova-pay/Acquiring.git", "github.com/nova-pay/acquiring"},
		{"https://github.com/nova-pay/acquiring.git", "github.com/nova-pay/acquiring"},
		{"ssh://git@github.com/nova-pay/acquiring", "github.com/nova-pay/acquiring"},
		{"https://github.com/nova-pay/acquiring/", "github.com/nova-pay/acquiring"},
	}
	for _, c := range cases {
		if got := NormalizeOrigin(c.in); got != c.want {
			t.Errorf("NormalizeOrigin(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// A normal checkout reads its origin straight out of its config.
func TestReadOrigin(t *testing.T) {
	dir := t.TempDir()
	gitDir := filepath.Join(dir, ".git")
	writeConfig(t, gitDir, "[core]\n\trepositoryformatversion = 0\n"+
		"[remote \"origin\"]\n\turl = git@github.com:nova-pay/Acquiring.git\n\tfetch = +refs/heads/*:refs/remotes/origin/*\n")

	if got := readOrigin(gitDir); got != "github.com/nova-pay/acquiring" {
		t.Fatalf("readOrigin = %q, want the normalized remote", got)
	}
}

// A local clone's origin is the path of the checkout it was cloned from; the
// remote that matters is the one THAT checkout points at.
func TestReadOriginFollowsALocalClone(t *testing.T) {
	upstream := t.TempDir()
	writeConfig(t, filepath.Join(upstream, ".git"),
		"[remote \"origin\"]\n\turl = git@github.com:x/y.git\n")

	clone := t.TempDir()
	writeConfig(t, filepath.Join(clone, ".git"),
		"[remote \"origin\"]\n\turl = "+upstream+"\n")

	if got := readOrigin(filepath.Join(clone, ".git")); got != "github.com/x/y" {
		t.Fatalf("readOrigin = %q, want the origin of the sibling checkout", got)
	}
}

// No origin section is no origin, not an error.
func TestReadOriginWithoutOrigin(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, filepath.Join(dir, ".git"),
		"[core]\n\trepositoryformatversion = 0\n"+
			"[remote \"upstream\"]\n\turl = git@github.com:x/y.git\n")

	if got := readOrigin(filepath.Join(dir, ".git")); got != "" {
		t.Fatalf("readOrigin = %q, want empty", got)
	}
}
