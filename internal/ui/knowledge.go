package ui

import (
	coreglib "github.com/diamondburned/gotk4/pkg/core/glib"

	"github.com/dns/ggraphify/internal/applog"
	"github.com/dns/ggraphify/internal/board"
	"github.com/dns/ggraphify/internal/discover"
	"github.com/dns/ggraphify/internal/knowledge"
)

// syncIndex republishes <out-base>/index.json from the scan that just landed.
//
// The board is the only process on this machine that holds a complete, current
// picture of where every graph is, so it is the right publisher — and the scan
// it has already paid for is the whole input. Nothing here re-reads a graph.
//
// It runs off the main thread because the write takes a lock file and may
// count semantic hashes in a manifest for any repository whose graph moved,
// and it writes only when something actually changed, so the ninety per cent
// of ticks that find the world unmoved touch no disk at all.
//
// A board with no out-base publishes nothing: graphs living in-tree are
// already discoverable from the checkout they belong to, and inventing a
// location for an index of them would be the split this file exists to close.
func (a *App) syncIndex(rows []board.Row) {
	_, base := a.outLocation()
	base = discover.Expand(base)
	if base == "" || a.indexing || len(rows) == 0 {
		return
	}
	a.indexing = true
	snap := append([]board.Row(nil), rows...)
	go func() {
		n, wrote, err := knowledge.Sync(base, snap)
		coreglib.IdleAdd(func() {
			a.indexing = false
			a.indexed = n
			switch {
			case err != nil:
				applog.Errorf("knowledge index at %s: %v", knowledge.Path(base), err)
			case wrote:
				applog.Infof("knowledge index rewritten: %d repositories in %s",
					n, knowledge.Path(base))
			}
		})
	}()
}
