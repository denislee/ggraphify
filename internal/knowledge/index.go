// Package knowledge publishes the lookup table that says where every
// repository's graphify graph lives.
//
// The problem it closes: with a central out-base — ~/knowledge here — a graph
// lands in <base>/<repo-basename>-<hash>/, and nothing inside the repository
// being graphed points back at it. Every external consumer (a Claude Code
// hook, a script, another agent) therefore has to glob the base directory by
// basename and read .graphify_root in each candidate to disambiguate, which
// works only for as long as no two scanned checkouts share a basename. That is
// coincidence, not design, and it breaks silently: the glob finds *a* graph,
// just not this repository's.
//
// So the board writes one file, <base>/index.json, keyed by the ABSOLUTE
// source path — the same value already written to .graphify_root, so this is a
// reshaping of data the app has rather than new bookkeeping. A consumer
// resolves in one read, with no globbing and no basename assumption:
//
//	jq -r '.entries["'"$PWD"'"].graph' ~/knowledge/index.json
//
// Two rules make it safe to write from several places at once — the GUI on
// every scan, `ggraphify-job` at the end of a build, `ggraphify-scan -index`:
// the read-modify-write is taken under a lock file in the same directory, and
// the result is written to a temporary file and renamed over the old one, so a
// reader never sees a half-written index.
package knowledge

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/dns/ggraphify/internal/board"
	"github.com/dns/ggraphify/internal/graphstate"
)

// Name is the index's filename inside the out-base directory.
const Name = "index.json"

// LockName is the file the read-modify-write is serialised on. It is a
// separate file rather than a lock on the index itself, because the index is
// replaced by rename: a lock held on the old inode would guard nothing.
const LockName = ".index.lock"

// Version is the schema version carried in the file. A consumer that does not
// recognise it should fall back to globbing rather than misread the entries.
const Version = 1

// Entry is one repository's knowledge, as an external consumer needs it.
//
// Every field is something the board already derived; nothing here is
// computed for the index alone except Tier, which is one manifest read and is
// carried forward unchanged while the graph's build time has not moved.
type Entry struct {
	Out         string    `json:"out"`                    // the output directory
	Graph       string    `json:"graph"`                  // out/graph.json, the file consumers open
	Report      string    `json:"report,omitempty"`       // out/GRAPH_REPORT.md when there is one
	BuiltAt     time.Time `json:"built_at,omitzero"`      // graph.json's mtime
	BuiltCommit string    `json:"built_commit,omitempty"` // the commit the graph was built from
	HeadCommit  string    `json:"head_commit,omitempty"`  // the checkout's HEAD when this was written
	Stale       bool      `json:"stale,omitempty"`        // built_commit is not head_commit
	State       string    `json:"state,omitempty"`        // the board's one-word verdict
	Tier        string    `json:"tier,omitempty"`         // ast | mixed | semantic
	Nodes       int       `json:"nodes,omitempty"`
	Edges       int       `json:"edges,omitempty"`
	Communities int       `json:"communities,omitempty"`
	Labeled     bool      `json:"labeled,omitempty"`  // communities carry real names
	Files       int       `json:"files,omitempty"`    // manifest entries
	Semantic    int       `json:"semantic,omitempty"` // of those, with a semantic_hash
}

// Same compares two entries the way the index has to: every field, but with
// the build time compared by instant rather than by struct. A time that has
// been through JSON comes back with a different location pointer for the same
// moment, and == on it would report a change on every single tick and rewrite
// the file forever.
func (e Entry) Same(o Entry) bool {
	a, b := e, o
	a.BuiltAt, b.BuiltAt = time.Time{}, time.Time{}
	return a == b && e.BuiltAt.Equal(o.BuiltAt)
}

// Index is the whole file.
type Index struct {
	Version   int              `json:"version"`
	Generated time.Time        `json:"generated"`
	Base      string           `json:"base,omitempty"` // the out-base these entries live under
	Entries   map[string]Entry `json:"entries"`
}

// Path is the index's location under an out-base. A blank base has no index:
// graphs that live in-tree are already discoverable from the checkout they
// belong to, which is the whole thing this file exists to restore.
func Path(base string) string {
	if base == "" {
		return ""
	}
	return filepath.Join(base, Name)
}

// Load reads an index. A missing file is an empty index and not an error: the
// first write is the one that creates it.
func Load(path string) (Index, error) {
	ix := Index{Version: Version, Entries: map[string]Entry{}}
	b, err := os.ReadFile(path) // #nosec G304 -- a path this application composed
	if errors.Is(err, os.ErrNotExist) {
		return ix, nil
	}
	if err != nil {
		return ix, err
	}
	if err := json.Unmarshal(b, &ix); err != nil {
		return Index{Version: Version, Entries: map[string]Entry{}}, err
	}
	if ix.Entries == nil {
		ix.Entries = map[string]Entry{}
	}
	return ix, nil
}

// Save writes the index atomically: a temporary file in the same directory,
// then a rename over whatever is there. Several ggraphify processes can finish
// a build at the same moment, and a reader must never catch a truncated file.
func (ix Index) Save(path string) error {
	ix.Version = Version
	ix.Generated = time.Now()
	if ix.Entries == nil {
		ix.Entries = map[string]Entry{}
	}
	b, err := json.MarshalIndent(ix, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, Name+".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }() // a no-op once the rename has happened
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	// 0644 rather than the 0600 the temp file is created with: this file is
	// published for other tools to read, and it holds paths and counters, not
	// a credential.
	if err := os.Chmod(name, 0o644); err != nil { // #nosec G302 -- deliberately world-readable
		return err
	}
	return os.Rename(name, path)
}

// FromRow builds the entry for one board row, or reports false when the row
// has no graph to point at. prev is the entry the index already held for this
// repository, consulted only to carry the tier forward: counting semantic
// hashes is a manifest parse, and re-doing it every thirty seconds for every
// repository on the board would cost more than everything else the refresh
// does put together. A build that moved graph.json's mtime re-counts.
func FromRow(r board.Row, prev Entry, ok bool) (Entry, bool) {
	g := r.Graph
	if g.Out == "" || g.State == graphstate.StateNone {
		return Entry{}, false
	}
	e := Entry{
		Out:         g.Out,
		Graph:       filepath.Join(g.Out, "graph.json"),
		BuiltAt:     g.BuiltAt,
		BuiltCommit: g.BuiltCommit,
		HeadCommit:  r.HeadSHA,
		Stale:       r.Behind,
		State:       g.State.String(),
		Nodes:       g.Nodes,
		Edges:       g.Links,
		Communities: g.Communities,
		Labeled:     g.Labeled,
	}
	if g.HasReport {
		e.Report = filepath.Join(g.Out, "GRAPH_REPORT.md")
	}
	if ok && prev.Out == e.Out && prev.BuiltAt.Equal(e.BuiltAt) && prev.Tier != "" {
		e.Tier, e.Files, e.Semantic = prev.Tier, prev.Files, prev.Semantic
		return e, true
	}
	if c, found := graphstate.CoverageOf(g.Out); found {
		e.Tier, e.Files, e.Semantic = c.Tier(), c.Files, c.Semantic
	}
	return e, true
}

// Sync folds a scan's rows into the index at base and writes it when anything
// moved. It returns how many entries the file now holds and whether it was
// rewritten, so a caller can log the write and stay silent about the ninety
// per cent of ticks that change nothing.
//
// Rows are authoritative for the repositories they cover. An entry for a
// repository this scan did not see is kept — a scan narrowed to one root must
// not delete the rest of the machine's index — unless its graph has gone from
// disk, which is the one case where keeping it would point a consumer at
// nothing.
func Sync(base string, rows []board.Row) (n int, wrote bool, err error) {
	path := Path(base)
	if path == "" {
		return 0, false, nil
	}
	unlock, err := lock(base)
	if err != nil {
		return 0, false, err
	}
	defer unlock()

	ix, err := Load(path)
	if err != nil {
		// A corrupt index is rebuilt rather than refused: it is derived data,
		// and the scan in hand is a complete answer for everything it covers.
		ix = Index{Version: Version, Entries: map[string]Entry{}}
	}
	before := ix.Entries
	next := make(map[string]Entry, len(before)+len(rows))
	for repo, e := range before {
		if graphstate.HasGraph(e.Out) {
			next[repo] = e
		}
	}
	for _, r := range rows {
		prev, had := before[r.Path]
		e, ok := FromRow(r, prev, had)
		if !ok {
			delete(next, r.Path)
			continue
		}
		next[r.Path] = e
	}
	ix.Entries = next
	ix.Base = base
	if same(before, next) {
		return len(next), false, nil
	}
	if err := ix.Save(path); err != nil {
		return len(next), false, err
	}
	return len(next), true, nil
}

// Put records one repository's entry, for the single-repository callers: a
// finished job knows exactly which row it changed and has no scan in hand.
func Put(base, repo string, e Entry) error {
	path := Path(base)
	if path == "" || repo == "" {
		return nil
	}
	unlock, err := lock(base)
	if err != nil {
		return err
	}
	defer unlock()

	ix, err := Load(path)
	if err != nil {
		ix = Index{Version: Version, Entries: map[string]Entry{}}
	}
	if cur, ok := ix.Entries[repo]; ok && cur.Same(e) {
		return nil
	}
	ix.Entries[repo] = e
	return ix.Save(path)
}

// Lookup answers the one question a consumer asks: where is this checkout's
// graph? It is here so that ggraphify's own commands resolve through the same
// file they publish, rather than keeping a second path of their own.
func Lookup(base, repo string) (Entry, bool) {
	path := Path(base)
	if path == "" {
		return Entry{}, false
	}
	ix, err := Load(path)
	if err != nil {
		return Entry{}, false
	}
	if abs, err := filepath.Abs(repo); err == nil {
		repo = abs
	}
	e, ok := ix.Entries[filepath.Clean(repo)]
	return e, ok
}

func same(a, b map[string]Entry) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if w, ok := b[k]; !ok || !w.Same(v) {
			return false
		}
	}
	return true
}

// lock takes the directory's index lock, creating the directory if the first
// write of a fresh machine got here before any graph did. The returned
// function releases it; a lock that cannot be taken is reported rather than
// waited on forever — flock without LOCK_NB would hang a refresh tick behind
// another process's write.
func lock(base string) (func(), error) {
	if err := os.MkdirAll(base, 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(base, LockName), os.O_CREATE|os.O_RDWR, 0o644) // #nosec G302,G304 -- a lock file beside a published index
	if err != nil {
		return nil, err
	}
	// Blocking, but bounded by the work under it: the critical section is one
	// small read and one rename. LOCK_NB and a retry loop would be the same
	// wait spelled less honestly.
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, err
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}
