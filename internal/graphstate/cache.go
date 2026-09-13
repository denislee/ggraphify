package graphstate

import (
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Cache memoises derived graph state on the identity of the files it was
// derived from.
//
// Reading one graph costs about 5 ms for a 2.4 MB graph.json plus a tree walk
// for drift. Over a hundred repositories on a thirty-second tick that is real
// work to repeat for an answer that has not changed — and almost none of them
// change between two ticks. The key is the output directory's own identity
// (graph.json's size and mtime, plus manifest.json's and the needs_update
// flag's), so any graphify run — from this board or from a terminal — moves it
// and invalidates the entry.
//
// Drift is deliberately NOT covered by the key: editing a source file changes
// nothing in graphify-out/, and a cached "no drift" answer would be exactly
// wrong. Entries therefore also expire on a short TTL, which is what lets the
// board notice an edit without re-reading every graph.json every tick.
type Cache struct {
	mu sync.Mutex
	m  map[string]entry
	// TTL bounds how stale a cached derivation may be. Zero means DefaultTTL.
	TTL time.Duration
}

// DefaultTTL is short enough that a file edited in an editor shows up as drift
// within a tick or two, and long enough that the expensive half is skipped on
// most of them.
const DefaultTTL = 45 * time.Second

type entry struct {
	key string
	at  time.Time
	g   Graph
}

// Read returns the cached derivation when the output directory is unchanged
// and the entry is fresh, and derives it otherwise.
func (c *Cache) Read(opts Options) (Graph, error) {
	out := opts.Out
	if out == "" {
		out = filepath.Join(opts.Repo, "graphify-out")
	}
	key := outKey(out)
	ttl := c.TTL
	if ttl <= 0 {
		ttl = DefaultTTL
	}

	c.mu.Lock()
	e, ok := c.m[opts.Repo]
	c.mu.Unlock()
	if ok && e.key == key && time.Since(e.at) < ttl {
		return e.g, nil
	}

	g, err := Read(opts)
	if err != nil {
		return g, err
	}
	c.mu.Lock()
	if c.m == nil {
		c.m = map[string]entry{}
	}
	c.m[opts.Repo] = entry{key: key, at: time.Now(), g: g}
	c.mu.Unlock()
	return g, nil
}

// Invalidate drops one repository's entry. The board calls it the moment a
// job for that repository finishes, so the row is re-derived immediately
// rather than at the next tick.
func (c *Cache) Invalidate(repo string) {
	c.mu.Lock()
	delete(c.m, repo)
	c.mu.Unlock()
}

// Clear drops everything, for an explicit rescan.
func (c *Cache) Clear() {
	c.mu.Lock()
	c.m = nil
	c.mu.Unlock()
}

// outKey fingerprints an output directory cheaply: three stats, no reads.
func outKey(out string) string {
	var b []byte
	for _, name := range []string{"graph.json", "manifest.json", ".graphify_labels.json", "needs_update"} {
		b = append(b, name...)
		b = append(b, ':')
		if fi, err := os.Stat(filepath.Join(out, name)); err == nil {
			b = appendInt(b, fi.Size())
			b = append(b, '@')
			b = appendInt(b, fi.ModTime().UnixNano())
		} else {
			b = append(b, '-')
		}
		b = append(b, '|')
	}
	return string(b)
}

func appendInt(b []byte, n int64) []byte {
	if n == 0 {
		return append(b, '0')
	}
	if n < 0 {
		b = append(b, '-')
		n = -n
	}
	var d [20]byte
	i := len(d)
	for n > 0 {
		i--
		d[i] = byte('0' + n%10)
		n /= 10
	}
	return append(b, d[i:]...)
}
