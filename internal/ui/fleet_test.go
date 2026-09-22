package ui

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/dns/ggraphify/internal/board"
	"github.com/dns/ggraphify/internal/discover"
	"github.com/dns/ggraphify/internal/globalgraph"
)

func row(path string, noGit bool) board.Row {
	return board.Row{Repo: discover.Repo{Path: path, Name: filepath.Base(path), NoGit: noGit}}
}

// Each exclusion here is drift that no rebuild could ever clear — a child the
// board offers that graft will never write into workspace.json — which is the
// shape that burns an attempt budget every cooldown and then declares the
// root stuck.
func TestFleetChildExcludesWhatGraftWillNeverFederate(t *testing.T) {
	cases := []struct {
		name string
		row  board.Row
		want string
	}{
		{"a direct-child checkout", row("/git/acquiring", false), "acquiring"},
		{"a trailing slash on the root is not a different root", row("/git/nexus", false), "nexus"},
		{"nested deeper than one level", row("/git/group/deep", false), ""},
		{"a graph-only row with no git", row("/git/docs", true), ""},
		{"a dot-directory graft always skips", row("/git/.github", false), ""},
		{"a checkout under a different root", row("/tmp/other", false), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := fleetChild("/git/", tc.row)
			if tc.want == "" {
				if ok {
					t.Fatalf("federated %q, want it excluded", got)
				}
				return
			}
			if !ok || got != tc.want {
				t.Fatalf("got (%q, %v), want (%q, true)", got, ok, tc.want)
			}
		})
	}
}

// The guard that makes pruning survivable. Under a central output layout
// every member's source is <base>/<slug>/graph.json, so re-pointing the
// output base makes every recorded path miss at once — and a prune cannot be
// undone without re-extracting the repository it dropped.
func TestAMissingGraphInADirectoryThatStillExistsIsNotStranded(t *testing.T) {
	base := t.TempDir()
	live := filepath.Join(base, "still-here")
	if err := os.MkdirAll(live, 0o755); err != nil {
		t.Fatal(err)
	}
	m := &globalgraph.Manifest{Exists: true, Entries: []globalgraph.Entry{
		{Tag: "still-here", Source: filepath.Join(live, "graph.json")},
	}}
	if got := strandedMembers(m); len(got) != 0 {
		t.Fatalf("pruned %v — the directory is still there, so this is a moved output "+
			"base rather than a deleted repository", got)
	}
}

func TestAMemberWhoseDirectoryIsGoneIsStranded(t *testing.T) {
	base := t.TempDir()
	m := &globalgraph.Manifest{Exists: true, Entries: []globalgraph.Entry{
		{Tag: "deleted", Source: filepath.Join(base, "deleted", "graph.json")},
	}}
	got := strandedMembers(m)
	if len(got) != 1 || got[0].Tag != "deleted" {
		t.Fatalf("strandedMembers = %v, want the one member whose directory is gone", got)
	}
}

func TestAMemberWhoseGraphIsPresentIsNeverStranded(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "alive")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(dir, "graph.json")
	if err := os.WriteFile(src, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := strandedMembers(&globalgraph.Manifest{
		Exists:  true,
		Entries: []globalgraph.Entry{{Tag: "alive", Source: src}},
	}); len(got) != 0 {
		t.Fatalf("pruned a member whose graph is right there: %v", got)
	}
}

// An unreadable manifest is not an empty one. Deriving "these members are
// gone" from a parse failure would prune the entire global graph.
func TestAnUnreadableManifestPrunesNothing(t *testing.T) {
	base := t.TempDir()
	entries := []globalgraph.Entry{{Tag: "x", Source: filepath.Join(base, "x", "graph.json")}}
	if got := strandedMembers(&globalgraph.Manifest{
		Exists: true, Err: errors.New("unexpected end of JSON input"), Entries: entries,
	}); len(got) != 0 {
		t.Fatalf("pruned %v from a manifest that could not be parsed", got)
	}
	if got := strandedMembers(nil); len(got) != 0 {
		t.Fatalf("pruned %v from a nil manifest", got)
	}
	if got := strandedMembers(&globalgraph.Manifest{Entries: entries}); len(got) != 0 {
		t.Fatalf("pruned %v from a manifest that does not exist — a machine that has "+
			"never run `global add` has nothing to prune", got)
	}
}
