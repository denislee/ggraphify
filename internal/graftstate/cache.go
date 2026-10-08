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
// wrong. So an entry has two layers, as graphstate.Cache's does: the static
// layer (counters, size, Has*/Deep/Exposed, the file count) is keyed on the
// stamp alone and never expires, and only the drift layer — the fingerprint
// re-read and the per-file stat pass — runs on the TTL.
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
	// st is readStatic's result for key, without its fingerprint records:
	// those are re-read when the drift layer expires rather than kept.
	st static
	at time.Time // when the drift layer was last derived
	i  Index
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
	if dir == DirFor(opts.Repo) {
		// exposedToGit reads the checkout's root .gitignore, which is
		// outside the graft directory and moves on its own.
		key = string(appendStat([]byte(key), ".gitignore", filepath.Join(opts.Repo, ".gitignore")))
	}
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

	var st static
	if ok && e.key == key {
		// Only the drift layer has expired.
		st = e.st
		if !st.final && !opts.SkipDrift {
			st.fp, st.fpOK = readFingerprint(st.fpPath)
		}
	} else {
		var err error
		st, err = readStatic(opts)
		if err != nil {
			return st.i, err
		}
	}
	i := st.i
	if !st.final {
		i = finishRead(st.i, st.fp, st.fpOK, opts)
	}
	st.fp, st.fpOK = nil, false
	c.mu.Lock()
	if (stamp{clears: c.clears, repo: c.gen[opts.Repo]}) == at {
		if c.m == nil {
			c.m = map[string]entry{}
		}
		c.m[opts.Repo] = entry{key: key, st: st, at: time.Now(), i: i}
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

// Prune drops every repository not in keep, both its entry and its
// invalidation counter, and returns how many entries it dropped. Checkouts
// come and go — agent worktrees above all — and a cache that only adds keys
// holds every one it has ever seen.
func (c *Cache) Prune(keep map[string]bool) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for repo := range c.m {
		if !keep[repo] {
			delete(c.m, repo)
			n++
		}
	}
	for repo := range c.gen {
		if !keep[repo] {
			delete(c.gen, repo)
		}
	}
	return n
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
	b = appendStat(b, ".", dir) // a file created or removed at the top
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
