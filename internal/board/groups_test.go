package board

import (
	"path/filepath"
	"testing"

	"github.com/dns/ggraphify/internal/discover"
)

func row(root, path, group string) Row {
	return Row{Repo: discover.Repo{Path: path, Root: root, Group: group, Name: filepath.Base(path)}}
}

// The folder list is derived from the rows, and a root is followed by the
// directories directly below it that hold checkouts of their own.
func TestGroupsListsRootsThenSubdirectories(t *testing.T) {
	rows := []Row{
		row("/home/u/git", "/home/u/git/api", ""),
		row("/home/u/git", "/home/u/git/nova/charge", "nova"),
		row("/home/u/git", "/home/u/git/nova/payout", "nova"),
		row("/home/u/tmp", "/home/u/tmp/scratch", ""),
	}
	got := Groups(rows)

	want := []struct {
		path  string
		depth int
		count int
	}{
		{"/home/u/git", 0, 3},
		{"/home/u/git/nova", 1, 2},
		{"/home/u/tmp", 0, 1},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d groups %v, want %d", len(got), got, len(want))
	}
	for i, w := range want {
		if got[i].Path != w.path || got[i].Depth != w.depth || got[i].Count != w.count {
			t.Errorf("group %d = %+v, want %s depth %d count %d", i, got[i], w.path, w.depth, w.count)
		}
	}
}

// One level only: a repository's own parent directory is not a folder to
// filter by, because filtering to it would be the same as selecting the row.
func TestGroupsStopsAtOneLevel(t *testing.T) {
	rows := []Row{
		row("/home/u/git", "/home/u/git/nova/services/api", "nova/services"),
		row("/home/u/git", "/home/u/git/nova/services/web", "nova/services"),
	}
	got := Groups(rows)
	if len(got) != 2 {
		t.Fatalf("got %d groups %+v, want the root and one subdirectory", len(got), got)
	}
	if got[1].Path != "/home/u/git/nova" {
		t.Errorf("subgroup = %q, want /home/u/git/nova", got[1].Path)
	}
}

// Two roots ending in the same name are two folders, which is the reason a
// group is a path and not a label.
func TestGroupsDoNotCollideOnName(t *testing.T) {
	rows := []Row{
		row("/home/u/git/work", "/home/u/git/work/api", ""),
		row("/home/u/oss/work", "/home/u/oss/work/api", ""),
	}
	if got := Groups(rows); len(got) != 2 {
		t.Fatalf("got %d groups %+v, want 2", len(got), got)
	}
}

// A directory holding one checkout is not offered as a folder: filtering to it
// would show exactly the row that selecting it shows.
func TestGroupsSkipsSingletonSubdirectories(t *testing.T) {
	rows := []Row{
		row("/home/u/tmp", "/home/u/tmp/scratch", ""),
		row("/home/u/tmp", "/home/u/tmp/oneoff/thing", "oneoff"),
		row("/home/u/tmp", "/home/u/tmp/sweep/a", "sweep"),
		row("/home/u/tmp", "/home/u/tmp/sweep/b", "sweep"),
	}
	got := Groups(rows)
	if len(got) != 2 {
		t.Fatalf("got %d groups %+v, want the root and ~/tmp/sweep", len(got), got)
	}
	if got[0].Count != 4 {
		t.Errorf("root count = %d, want every row under it", got[0].Count)
	}
	if got[1].Path != "/home/u/tmp/sweep" {
		t.Errorf("subgroup = %q, want /home/u/tmp/sweep", got[1].Path)
	}
	// The singleton is still reachable — it is just not its own folder.
	if !InGroup(rows[1], "/home/u/tmp") {
		t.Error("a row in a singleton subdirectory fell out of its root")
	}
}

func TestInGroup(t *testing.T) {
	r := row("/home/u/git", "/home/u/git/nova/charge", "nova")
	cases := []struct {
		group string
		want  bool
	}{
		{"", true},
		{"/home/u/git", true},
		{"/home/u/git/", true},
		{"/home/u/git/nova", true},
		{"/home/u/git/nova/charge", true},
		{"/home/u/tmp", false},
		// A prefix that is not a path boundary is not a parent directory.
		{"/home/u/gi", false},
		{"/home/u/git/nov", false},
	}
	for _, c := range cases {
		if got := InGroup(r, c.group); got != c.want {
			t.Errorf("InGroup(%q) = %v, want %v", c.group, got, c.want)
		}
	}
}

func TestGroupOfIsTheMostSpecific(t *testing.T) {
	if got := GroupOf(row("/home/u/git", "/home/u/git/api", "")); got != "/home/u/git" {
		t.Errorf("flat row: %q", got)
	}
	if got := GroupOf(row("/home/u/git", "/home/u/git/nova/charge", "nova")); got != "/home/u/git/nova" {
		t.Errorf("grouped row: %q", got)
	}
}

func TestTilde(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if got, want := Tilde(filepath.Join(home, "git")), "~/git"; got != want {
		t.Errorf("Tilde = %q, want %q", got, want)
	}
	if got := Tilde("/srv/checkouts"); got != "/srv/checkouts" {
		t.Errorf("a path outside home was rewritten: %q", got)
	}
	// A sibling of the home directory only shares a prefix; it is not under it.
	if got := Tilde(home + "-backup/git"); got != home+"-backup/git" {
		t.Errorf("prefix mistaken for a parent: %q", got)
	}
}
