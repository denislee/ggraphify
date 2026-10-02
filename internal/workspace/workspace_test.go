package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// write lays down a workspace.json at root with the given children.
func write(t *testing.T, root string, children ...string) {
	t.Helper()
	dir := filepath.Join(root, DirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"version":1,"children":[`
	for i, c := range children {
		if i > 0 {
			body += ","
		}
		body += `"` + c + `"`
	}
	body += `]}`
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestADirectoryThatIsNotAWorkspaceHasNoDefect(t *testing.T) {
	root := t.TempDir()
	s := Inspect(root, []string{"alpha", "beta"})
	if s.File.Exists {
		t.Fatalf("Exists = true for a directory with no graft/")
	}
	if got := s.Issue(); got != "" {
		t.Fatalf("Issue = %q, want none — a directory graft was never pointed at "+
			"is a directory, not a broken workspace", got)
	}
	if len(s.Missing) != 0 {
		t.Fatalf("Missing = %v, want none: federating for the first time is not a repair", s.Missing)
	}
}

func TestAFederationThatMatchesDiskNeedsNothing(t *testing.T) {
	root := t.TempDir()
	write(t, root, "alpha", "beta")
	s := Inspect(root, []string{"beta", "alpha"})
	if got := s.Issue(); got != "" {
		t.Fatalf("Issue = %q, want none — order is not drift", got)
	}
	if got := s.Summary(); got != "federated" {
		t.Fatalf("Summary = %q", got)
	}
}

func TestANewCheckoutIsMissingFromTheFederation(t *testing.T) {
	root := t.TempDir()
	write(t, root, "alpha")
	s := Inspect(root, []string{"alpha", "beta"})
	if got := s.Issue(); got != IssueDrift {
		t.Fatalf("Issue = %q, want %q", got, IssueDrift)
	}
	if len(s.Missing) != 1 || s.Missing[0] != "beta" {
		t.Fatalf("Missing = %v, want [beta]", s.Missing)
	}
	if len(s.Orphans) != 0 {
		t.Fatalf("Orphans = %v, want none", s.Orphans)
	}
	if !strings.Contains(s.Summary(), "unfederated") {
		t.Fatalf("Summary = %q, want it to name the unfederated checkout", s.Summary())
	}
}

func TestADeletedCheckoutLingersAsAnOrphan(t *testing.T) {
	root := t.TempDir()
	write(t, root, "alpha", "gone")
	s := Inspect(root, []string{"alpha"})
	if got := s.Issue(); got != IssueDrift {
		t.Fatalf("Issue = %q, want %q", got, IssueDrift)
	}
	if len(s.Orphans) != 1 || s.Orphans[0] != "gone" {
		t.Fatalf("Orphans = %v, want [gone]", s.Orphans)
	}
}

// An unreadable federation must not read as an empty one. The difference
// decides whether the loop rebuilds a hundred checkouts every cooldown for a
// file only a person can fix.
func TestAnUnreadableFederationIsNotDrift(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, DirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := Inspect(root, []string{"alpha"})
	if s.File.Err == nil {
		t.Fatal("Err = nil for a workspace.json that does not parse")
	}
	if got := s.Issue(); got != "" {
		t.Fatalf("Issue = %q, want none — a parse failure is not a repair this loop can make", got)
	}
}

func TestAnEmptyFederationWithCheckoutsIsDrift(t *testing.T) {
	root := t.TempDir()
	write(t, root)
	s := Inspect(root, []string{"alpha"})
	if !s.File.Exists {
		t.Fatal("Exists = false for a workspace.json holding an empty list")
	}
	if got := s.Issue(); got != IssueDrift {
		t.Fatalf("Issue = %q, want %q: an empty federation beside a checkout is drift, "+
			"not an absent workspace", got, IssueDrift)
	}
}

// The board hides linked worktrees and dot-directories by default; graft
// federates them. A federated child the board never listed but that is still
// a checkout on disk is not gone — calling it an orphan is drift that no
// rebuild can clear, because the rebuild writes it straight back.
func TestAHiddenCheckoutStillOnDiskIsNotAnOrphan(t *testing.T) {
	root := t.TempDir()
	write(t, root, "alpha", "alpha-wt", "gone")
	wt := filepath.Join(root, "alpha-wt")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	// A linked worktree: .git is a file, not a directory.
	if err := os.WriteFile(filepath.Join(wt, ".git"),
		[]byte("gitdir: /elsewhere/.git/worktrees/alpha-wt\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := Inspect(root, []string{"alpha"})
	if len(s.Orphans) != 1 || s.Orphans[0] != "gone" {
		t.Fatalf("Orphans = %v, want [gone] — the worktree is still on disk", s.Orphans)
	}
}
