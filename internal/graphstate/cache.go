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
// wrong. So an entry has two layers. The static layer — counters, labels,
// communities, Has* flags, size — is keyed on the directory's stamp alone and
// never expires: it is re-read only when a graphify run moves the stamp, and
// those numbers cannot change any other way. The drift layer expires on a
// short TTL, which is what lets the board notice an edit without re-reading
// any graph.json at all.
type Cache struct {
	mu sync.Mutex
	m  map[string]entry
	// gen counts invalidations, per repository and board-wide. A derivation
	// that was already in flight when its repository was invalidated must not
	// store its result: it was computed from inputs the caller has since
	// declared out of date, and storing it puts the superseded answer back in
	// the cache for a whole TTL. That is exactly the "Fix cleared the drift,
	// the row stayed stale" case — ackDrift records the baseline and
	// invalidates while the refresh it raced is still walking the tree with
	// the old one.
	gen    map[string]uint64
	clears uint64
	// TTL bounds how stale a cached derivation may be. Zero means DefaultTTL.
	TTL time.Duration
}

// stamp is the invalidation state one derivation was started under. Read only
// stores its result when the stamp has not moved since.
type stamp struct {
	clears uint64
	repo   uint64
}

// derived is called between a derivation and its store, by tests only.
var derived func(repo string)

// DefaultTTL is short enough that a file edited in an editor shows up as drift
// within a tick or two, and long enough that the expensive half is skipped on
// most of them.
const DefaultTTL = 45 * time.Second

type entry struct {
	key string
	// static is readStatic's result for key; final says it is already the
	// whole answer (no graph, or a broken one) and has no drift layer.
	static Graph
	final  bool
	// at is when the drift layer was last derived, and g is static plus
	// drift plus the verdict.
	at time.Time
	g  Graph
}

// Read returns the cached derivation when the output directory is unchanged
// and the entry is fresh, and derives it otherwise.
func (c *Cache) Read(opts Options) (Graph, error) {
	out := opts.Out
	if out == "" {
		out = filepath.Join(opts.Repo, "graphify-out")
	}
	// HEAD is part of the key rather than of the TTL: a commit changes the
	// Behind verdict without touching a byte in graphify-out/, and a row that
	// went stale on commit should say so on the next tick, not up to a TTL
	// later.
	key := outKey(out) + "@" + opts.Head
	if opts.CommitOnly {
		key += "|commit-only"
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
		return e.g, nil
	}

	var (
		static Graph
		final  bool
	)
	if ok && e.key == key {
		// Only the drift layer has expired: the directory has not moved, so
		// nothing readStatic would read has either.
		static, final = e.static, e.final
	} else {
		var err error
		static, final, err = readStatic(opts)
		if err != nil {
			return static, err
		}
	}
	g := static
	if !final {
		g = finishRead(static, opts)
	}
	if derived != nil {
		// Test seam: the whole question this function answers is what happens
		// when an Invalidate lands *here*, between the derivation and the
		// store, and there is no other way to put it there deterministically.
		derived(opts.Repo)
	}
	c.mu.Lock()
	if (stamp{clears: c.clears, repo: c.gen[opts.Repo]}) == at {
		if c.m == nil {
			c.m = map[string]entry{}
		}
		c.m[opts.Repo] = entry{key: key, static: static, final: final, at: time.Now(), g: g}
	}
	c.mu.Unlock()
	return g, nil
}

// Invalidate drops one repository's entry. The board calls it the moment a
// job for that repository finishes, so the row is re-derived immediately
// rather than at the next tick.
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
// invalidation counter. Checkouts come and go — agent worktrees above all —
// and a cache that only ever adds keys holds every one it has ever seen. It
// returns how many entries it dropped.
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

// outKeyFiles are what readStatic reads, or whose existence it reports. "."
// is the directory itself: creating or removing anything in it — a report, an
// HTML view, the cache — moves its mtime, which is what keeps the Has* flags
// and the size honest without a TTL.
var outKeyFiles = []string{
	".", "graph.json", "manifest.json", ".graphify_labels.json", "needs_update",
	".graphify_labels.json.sig", LLMLabelSigFile, ".graphify_analysis.json",
	filepath.Join("wiki", "index.md"),
}

// outKey fingerprints an output directory cheaply: a handful of stats, no
// reads.
func outKey(out string) string {
	var b []byte
	for _, name := range outKeyFiles {
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
