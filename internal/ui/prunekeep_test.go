package ui

import (
	"errors"
	"testing"

	"github.com/dns/ggraphify/internal/autofix"
	"github.com/dns/ggraphify/internal/globalgraph"
)

func TestRepoKeepKeepsCandidatesAndEveryFleetKey(t *testing.T) {
	keep := repoKeep([]autofix.Candidate{{Path: "/git/a"}, {Path: "/wt/a@x"}})
	cases := map[string]bool{
		"/git/a":               true,
		"/wt/a@x":              true,
		"/git/gone":            false,
		"fleet:workspace:/git": true,
		"fleet:prune:anything": true,
	}
	for k, want := range cases {
		if got := keep(k); got != want {
			t.Errorf("repoKeep(%q) = %v, want %v", k, got, want)
		}
	}
}

func TestFleetKeepFollowsRootsAndManifestTags(t *testing.T) {
	roots := []autofix.Root{{Path: "/git"}}
	m := &globalgraph.Manifest{Exists: true, Entries: []globalgraph.Entry{{Tag: "still"}}}
	keep := fleetKeep(roots, m)
	cases := map[string]bool{
		"fleet:workspace:/git":     true,
		"fleet:workspace:/removed": false,
		"fleet:prune:still":        true,
		"fleet:prune:gone":         false,
		"/git/a":                   true, // the repository pass's business
	}
	for k, want := range cases {
		if got := keep(k); got != want {
			t.Errorf("fleetKeep(%q) = %v, want %v", k, got, want)
		}
	}

	// An unreadable manifest says nothing about which members are gone.
	bad := fleetKeep(roots, &globalgraph.Manifest{Exists: true, Err: errors.New("parse")})
	if !bad("fleet:prune:gone") {
		t.Error("an unreadable manifest must keep every prune record")
	}
}
