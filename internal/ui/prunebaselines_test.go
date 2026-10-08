package ui

import (
	"path/filepath"
	"testing"

	"github.com/dns/ggraphify/internal/board"
	"github.com/dns/ggraphify/internal/graphstate"
	"github.com/dns/ggraphify/internal/store"
)

func TestPruneBaselinesDropsOnlyDeletedCheckouts(t *testing.T) {
	dir := t.TempDir()
	st := store.Open(filepath.Join(dir, "state.json"))
	onBoard := t.TempDir()
	hidden := t.TempDir() // exists on disk, not a row (e.g. worktrees hidden)
	gone := filepath.Join(dir, "deleted-worktree")
	b := graphstate.Baseline{Added: map[string]float64{"a.go": 1}}
	for _, r := range []string{onBoard, hidden, gone} {
		st.SetDriftBaseline(r, b)
	}

	pruneBaselines(st, []board.Row{{Path: onBoard}})

	for r, want := range map[string]bool{onBoard: true, hidden: true, gone: false} {
		got := len(st.DriftBaseline(r).Added) > 0
		if got != want {
			t.Errorf("baseline of %s kept=%v, want %v", r, got, want)
		}
	}
}
