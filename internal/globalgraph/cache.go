package globalgraph

import (
	"os"
	"strconv"
	"sync"
	"time"
)

// Cache memoises the manifest on the identity of the file it was read from.
//
// It is far smaller than graphstate's or graftstate's caches because there is
// only ever one file: a scan of 130 rows asks for membership 130 times and
// must not read, parse and path-resolve the manifest 130 times. The key is the
// file's size and mtime, so an add or a remove run from a terminal — or by a
// job this board queued — is picked up on the next tick without anything
// having to invalidate it.
type Cache struct {
	mu  sync.Mutex
	m   *Manifest
	key string
	at  time.Time
	// TTL bounds how long a manifest is trusted even when its stat has not
	// moved. Zero means DefaultTTL.
	TTL time.Duration
}

// DefaultTTL is short: the file is a few kilobytes, so re-reading it costs
// almost nothing, and the cache exists to collapse one scan's worth of
// lookups rather than to avoid the read across ticks.
const DefaultTTL = 5 * time.Second

// Load returns the manifest, re-reading it when the file has changed or the
// entry has expired. It never returns nil.
func (c *Cache) Load() *Manifest {
	return c.LoadFrom(ManifestPath())
}

// LoadFrom is Load against an explicit path, for tests and for a caller that
// knows better than $HOME.
func (c *Cache) LoadFrom(path string) *Manifest {
	ttl := c.TTL
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	key := statKey(path)

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m != nil && c.key == key && time.Since(c.at) < ttl {
		return c.m
	}
	m := Load(path)
	c.m, c.key, c.at = m, key, time.Now()
	return m
}

// Invalidate drops the memo, so the next Load re-reads. A `graphify global
// add` that finishes within the TTL would otherwise leave the board showing
// the membership it had before the job ran.
func (c *Cache) Invalidate() {
	c.mu.Lock()
	c.m, c.key = nil, ""
	c.mu.Unlock()
}

func statKey(path string) string {
	if path == "" {
		return "-"
	}
	fi, err := os.Stat(path)
	if err != nil {
		return "missing"
	}
	return fi.ModTime().UTC().Format(time.RFC3339Nano) + ":" + strconv.FormatInt(fi.Size(), 10)
}
