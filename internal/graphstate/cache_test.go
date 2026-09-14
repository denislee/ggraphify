package graphstate

import (
	"os"
	"testing"
	"time"
)

// A derivation that was already running when its repository was invalidated
// must not be stored. This is the "Fix cleared the drift, the row stayed
// yellow" bug: the board records the baseline and invalidates while the
// refresh it raced is still walking the tree with the old one, and the loser's
// answer used to land in the cache and be served for a whole TTL.
func TestInvalidateDuringAReadDropsTheInFlightResult(t *testing.T) {
	f := newFixture(t)
	f.graphJSON(1, 0, "abc")
	f.writeOut(".graphify_labels.json", `{"0":"colspec_test.go","1":"Settings"}`)
	f.manifest(map[string]float64{"a.go": float64(time.Now().Unix())})
	f.write("a.go", "package a\n")
	added := f.write("new.go", "package a\n")

	var c Cache
	opts := Options{Repo: f.repo, Out: f.out}

	// Prime the cache the way a refresh tick does, with no baseline.
	stale, err := c.Read(opts)
	if err != nil {
		t.Fatal(err)
	}
	if stale.State != StateStale {
		t.Fatalf("setup: want stale, got %s", stale.State)
	}

	// The baseline lands: the added file is one graphify declined to graph.
	fi, err := os.Stat(added)
	if err != nil {
		t.Fatal(err)
	}
	base := Baseline{Added: map[string]float64{
		"new.go": float64(fi.ModTime().UnixNano()) / 1e9,
	}}

	// The losing read: it starts before the invalidation and stores after it.
	c.Invalidate(f.repo)
	derived = func(string) {
		derived = nil
		c.Invalidate(f.repo)
	}
	t.Cleanup(func() { derived = nil })
	if _, err := c.Read(Options{Repo: f.repo, Out: f.out}); err != nil {
		t.Fatal(err)
	}

	opts.Baseline = base
	g, err := c.Read(opts)
	if err != nil {
		t.Fatal(err)
	}
	if g.State != StateFresh {
		t.Fatalf("after the baseline the row should be fresh, got %s (drift +%d ~%d -%d)",
			g.State, g.DriftAdded, g.DriftChanged, g.DriftRemoved)
	}
}

// Clear does the same for the whole board.
func TestClearDropsInFlightResults(t *testing.T) {
	f := newFixture(t)
	f.graphJSON(1, 0, "abc")
	f.manifest(map[string]float64{"a.go": float64(time.Now().Unix())})
	f.write("a.go", "package a\n")

	var c Cache
	if _, err := c.Read(Options{Repo: f.repo, Out: f.out}); err != nil {
		t.Fatal(err)
	}
	c.Clear()
	c.mu.Lock()
	n := len(c.m)
	c.mu.Unlock()
	if n != 0 {
		t.Fatalf("Clear left %d entries", n)
	}
}
