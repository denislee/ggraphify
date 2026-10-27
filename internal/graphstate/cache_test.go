package graphstate

import (
	"os"
	"path/filepath"
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

// W32: the Size column is the one static field the output directory's stamp
// does not cover — graphify's cache grows inside cache/ without moving any
// stamped file — so it must be re-walked on its own TTL rather than kept for
// as long as the stamp holds.
func TestCacheSizeFollowsGrowthInsideSubdirs(t *testing.T) {
	f := newFixture(t)
	f.graphJSON(1, 0, "abc")
	f.manifest(map[string]float64{"a.go": float64(time.Now().Unix())})
	f.write("a.go", "package a\n")
	if err := os.MkdirAll(filepath.Join(f.out, "cache", "ast"), 0o755); err != nil {
		t.Fatal(err)
	}

	c := Cache{TTL: time.Nanosecond, SizeTTL: time.Nanosecond}
	opts := Options{Repo: f.repo, Out: f.out, SkipDrift: true}
	g, err := c.Read(opts)
	if err != nil {
		t.Fatal(err)
	}
	before := g.SizeBytes
	key := outKey(f.out)

	if err := os.WriteFile(filepath.Join(f.out, "cache", "ast", "x.json"), make([]byte, 4096), 0o644); err != nil {
		t.Fatal(err)
	}
	if outKey(f.out) != key {
		t.Fatal("setup: the write moved the stamp; the test no longer exercises the static layer")
	}
	g, err = c.Read(opts)
	if err != nil {
		t.Fatal(err)
	}
	if g.SizeBytes != before+4096 {
		t.Fatalf("SizeBytes = %d, want %d — the static layer kept a stale size", g.SizeBytes, before+4096)
	}
}

// W31: a read stamped before a Prune must not store its result after it —
// neither re-adding the pruned key nor, once a later Invalidate brings the
// reset counter back to the read's value, caching a superseded answer.
func TestPruneDropsInFlightResults(t *testing.T) {
	f := newFixture(t)
	f.graphJSON(1, 0, "abc")
	f.manifest(map[string]float64{"a.go": float64(time.Now().Unix())})
	f.write("a.go", "package a\n")

	var c Cache
	c.Invalidate(f.repo) // gen = 1: the read below is stamped with it
	derived = func(string) {
		derived = nil
		c.Prune(map[string]bool{}) // gen deleted → 0
		c.Invalidate(f.repo)       // gen back to 1: the stamps used to match
	}
	t.Cleanup(func() { derived = nil })
	if _, err := c.Read(Options{Repo: f.repo, Out: f.out}); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	_, stored := c.m[f.repo]
	c.mu.Unlock()
	if stored {
		t.Fatal("a read that raced Prune and Invalidate stored its superseded result")
	}

	// Plain Prune: the in-flight read must not put the pruned key back.
	derived = func(string) {
		derived = nil
		c.Prune(map[string]bool{})
	}
	if _, err := c.Read(Options{Repo: f.repo, Out: f.out}); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	_, stored = c.m[f.repo]
	c.mu.Unlock()
	if stored {
		t.Fatal("a read finishing after Prune re-added the pruned key")
	}
}
