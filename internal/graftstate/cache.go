package graftstate

import (
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Cache memoises derived graft state on the identity of the files it was
// derived from, exactly as graphstate.Cache does for graphify's output.
//
// The key is the graft directory's own identity — wiring.json's size and
// mtime, the newest fingerprint's, and the concept manifest's — so any graft
// run, from this board or from a terminal or from an agent's own MCP call,
// moves it and invalidates the entry.
//
// Drift is deliberately NOT part of the key: editing a source file changes
// nothing inside graft/, and a cached "no drift" answer would be exactly
// wrong. Entries therefore also expire on a short TTL.
type Cache struct {
	mu sync.Mutex
	m  map[string]entry
	// gen counts invalidations, per repository and board-wide, so a
	// derivation that was already in flight when its repository was
	// invalidated does not store a result computed from superseded inputs.
	gen    map[string]uint64
	clears uint64
	// TTL bounds how stale a cached derivation may be. Zero means DefaultTTL.
	TTL time.Duration
}

// DefaultTTL matches graphstate's, for the same reason: short enough that an
// edit shows up as drift within a tick or two, long enough that most ticks
// skip the stat pass entirely.
const DefaultTTL = 45 * time.Second

type entry struct {
	key string
	at  time.Time
	i   Index
}

type stamp struct {
	clears uint64
	repo   uint64
}

// Read returns the cached derivation when the graft directory is unchanged and
// the entry is fresh, and derives it otherwise.
func (c *Cache) Read(opts Options) (Index, error) {
	dir := opts.Dir
	if dir == "" {
		dir = DirFor(opts.Repo)
	}
	key := dirKey(dir)
	ttl := c.TTL
	if ttl <= 0 {
		ttl = DefaultTTL
	}

	c.mu.Lock()
	e, ok := c.m[opts.Repo]
	at := stamp{clears: c.clears, repo: c.gen[opts.Repo]}
	c.mu.Unlock()
	if ok && e.key == key && time.Since(e.at) < ttl {
		return e.i, nil
	}

	i, err := Read(opts)
	if err != nil {
		return i, err
	}
	c.mu.Lock()
	if (stamp{clears: c.clears, repo: c.gen[opts.Repo]}) == at {
		if c.m == nil {
			c.m = map[string]entry{}
		}
		c.m[opts.Repo] = entry{key: key, at: time.Now(), i: i}
	}
	c.mu.Unlock()
	return i, nil
}

// Invalidate drops one repository's entry. The board calls it the moment a
// graft job for that repository finishes.
func (c *Cache) Invalidate(repo string) {
	c.mu.Lock()
	delete(c.m, repo)
	if c.gen == nil {
		c.gen = map[string]uint64{}
	}
	c.gen[repo]++
	c.mu.Unlock()
}

// Clear drops everything, for an explicit rescan.
func (c *Cache) Clear() {
	c.mu.Lock()
	c.m = nil
	c.gen = nil
	c.clears++
	c.mu.Unlock()
}

// dirKey fingerprints a graft directory cheaply: three stats and one small
// directory read, no file contents.
func dirKey(dir string) string {
	var b []byte
	for _, rel := range []string{
		filepath.Join(graphDir, wiringFile),
		manifestFile,
		indexFile,
	} {
		b = appendStat(b, rel, filepath.Join(dir, rel))
	}
	if fp := newestFingerprint(filepath.Join(dir, cacheDir)); fp != "" {
		b = appendStat(b, fingerprintPr, fp)
	} else {
		b = append(b, fingerprintPr...)
		b = append(b, "-|"...)
	}
	return string(b)
}

func appendStat(b []byte, name, path string) []byte {
	b = append(b, name...)
	b = append(b, ':')
	if fi, err := os.Stat(path); err == nil {
		b = appendInt(b, fi.Size())
		b = append(b, '@')
		b = appendInt(b, fi.ModTime().UnixNano())
	} else {
		b = append(b, '-')
	}
	return append(b, '|')
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
