// Package globalgraph reads graphify's cross-repository graph membership —
// which checkouts have been merged into ~/.graphify/global-graph.json — without
// running graphify at all.
//
// The graph itself is a single file that can run to hundreds of megabytes, and
// answering "is this repository in it" by parsing it would be unaffordable on
// a 30-second tick over ~130 rows. It does not have to be parsed: `graphify
// global add` records every member in a manifest beside it, and that manifest
// is a few kilobytes of exactly the facts the board wants — the tag the repo
// was added under, the graph.json it was built from, how many nodes and edges
// it contributed, and when.
//
// The manifest is graphify's file, not this board's. Nothing here writes to
// it: membership changes go through `graphify global add` / `global remove`
// like every other mutation the board causes.
package globalgraph

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// The file names are graphify's, mirrored from graphify/global_graph.py.
const (
	DirName      = ".graphify"
	ManifestName = "global-manifest.json"
	GraphName    = "global-graph.json"
)

// Dir is ~/.graphify, or "" when there is no home directory to speak of.
func Dir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, DirName)
}

// ManifestPath is where the membership record lives.
func ManifestPath() string {
	d := Dir()
	if d == "" {
		return ""
	}
	return filepath.Join(d, ManifestName)
}

// GraphPath is where the merged graph itself lives.
func GraphPath() string {
	d := Dir()
	if d == "" {
		return ""
	}
	return filepath.Join(d, GraphName)
}

// Entry is one repository's membership record, as graphify wrote it.
type Entry struct {
	// Tag is the name the repository was added under — the key the manifest
	// is keyed on, and the only handle `graphify global remove` accepts.
	Tag string `json:"tag"`
	// Source is the graph.json the nodes came from, absolute and resolved.
	Source string `json:"source_path"`
	// Nodes and Edges are what this repository contributed at the time it was
	// added, not what it has now.
	Nodes int `json:"node_count"`
	Edges int `json:"edge_count"`
	// AddedAt is when the add ran. It is what makes staleness answerable
	// without hashing a multi-megabyte file: a graph.json built after this is
	// a graph whose nodes are not the ones in the global graph.
	AddedAt time.Time `json:"added_at"`
	// Hash is graphify's own fingerprint of the source graph — the first 16
	// hex digits of its SHA-256. It is the authority on whether an add would
	// change anything (graphify skips one whose hash still matches), and it is
	// carried here so a caller that can afford the read can say so exactly.
	Hash string `json:"source_hash"`
}

// rawManifest is the on-disk shape.
type rawManifest struct {
	Version int                 `json:"version"`
	Repos   map[string]rawEntry `json:"repos"`
}

type rawEntry struct {
	AddedAt    string `json:"added_at"`
	SourcePath string `json:"source_path"`
	NodeCount  int    `json:"node_count"`
	EdgeCount  int    `json:"edge_count"`
	SourceHash string `json:"source_hash"`
}

// Manifest is the membership record: every repository in the global graph,
// indexed both by tag and by the graph.json it was built from.
//
// The zero value, and a nil *Manifest, are both usable and mean "no global
// graph here" — every method is nil-safe, so a board that could not find a
// home directory renders the column as "out" rather than crashing.
type Manifest struct {
	// Path is the manifest file this was read from, for the UI to name.
	Path string
	// Entries is every member, sorted by tag.
	Entries []Entry
	// Exists is false when there is no manifest file at all, which is the
	// ordinary state of a machine that has never run `graphify global add`.
	// It is distinct from a manifest that exists and is empty.
	Exists bool
	// Err is why a manifest that exists could not be read. A membership
	// answer derived from an unreadable manifest is "not a member", which is
	// wrong in exactly the direction that invites a duplicate add — so the
	// error is carried rather than swallowed, and the UI says so.
	Err error

	byTag  map[string]Entry
	byPath map[string]Entry
}

// Load reads the manifest at path. A missing file is not an error: it is the
// answer "nothing has been added to the global graph".
func Load(path string) *Manifest {
	m := &Manifest{Path: path, byTag: map[string]Entry{}, byPath: map[string]Entry{}}
	if path == "" {
		return m
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			m.Exists, m.Err = true, err
		}
		return m
	}
	m.Exists = true

	var raw rawManifest
	if err := json.Unmarshal(b, &raw); err != nil {
		m.Err = err
		return m
	}
	for tag, r := range raw.Repos {
		e := Entry{
			Tag:    tag,
			Source: normalize(r.SourcePath),
			Nodes:  r.NodeCount,
			Edges:  r.EdgeCount,
			Hash:   r.SourceHash,
		}
		if t, err := time.Parse(time.RFC3339, r.AddedAt); err == nil {
			e.AddedAt = t
		}
		m.Entries = append(m.Entries, e)
		m.byTag[tag] = e
		if e.Source != "" {
			m.byPath[e.Source] = e
		}
	}
	sort.Slice(m.Entries, func(i, j int) bool { return m.Entries[i].Tag < m.Entries[j].Tag })
	return m
}

// Len is how many repositories are in the global graph.
func (m *Manifest) Len() int {
	if m == nil {
		return 0
	}
	return len(m.Entries)
}

// Totals sums what the members contributed when they were added.
func (m *Manifest) Totals() (nodes, edges int) {
	if m == nil {
		return 0, 0
	}
	for _, e := range m.Entries {
		nodes += e.Nodes
		edges += e.Edges
	}
	return nodes, edges
}

// ByTag looks a member up by the name it was added under.
func (m *Manifest) ByTag(tag string) (Entry, bool) {
	if m == nil {
		return Entry{}, false
	}
	e, ok := m.byTag[tag]
	return e, ok
}

// ByGraph looks a member up by the graph.json it was built from, which is the
// question a board row asks: paths are the stable identity here, because two
// checkouts can have the same basename and a tag is only a label.
func (m *Manifest) ByGraph(graphPath string) (Entry, bool) {
	if m == nil || graphPath == "" {
		return Entry{}, false
	}
	e, ok := m.byPath[normalize(graphPath)]
	return e, ok
}

// Member is one row's verdict: is this repository's graph in the global graph,
// and is what is in there still what this repository has.
type Member struct {
	In      bool      `json:"in"`
	Tag     string    `json:"tag,omitempty"`
	Nodes   int       `json:"nodes,omitempty"`
	Edges   int       `json:"edges,omitempty"`
	AddedAt time.Time `json:"added_at,omitzero"`
	// Stale is true when the repository's graph was rebuilt after it was
	// merged, so the global graph is carrying the previous extraction's nodes.
	Stale bool `json:"stale,omitempty"`
}

// IssueStale is the one defect a membership can have that anything can act
// on: the repository's own graph was rebuilt after it was merged, so the
// global graph is answering from the previous extraction's nodes.
//
// It is a string for the same reason graphstate's and graftstate's codes are:
// it goes into the auto-fix loop's signature, where "what is wrong here" has
// to compare as a value rather than as a struct.
const IssueStale = "global-stale"

// IssueOut is the defect a NON-member has, and only ever in the one context
// where absence is answerable: a repository under a root the user nominated
// as a fleet, which is to say a directory they have already said they want to
// query across.
//
// It is defined here, beside IssueStale, so the two membership codes live in
// one place and the loop's signature can carry either. But nothing in this
// package ever returns it — Member.Issue() below deliberately does not,
// because a Member on its own cannot know whether its absence was a choice.
// The caller that knows about roots is the one that raises it; see
// autofix.globalCode.
const IssueOut = "global-out"

// IssueStranded is the defect of a member whose source graph is no longer on
// disk: not stale, which is a copy that can be refreshed, but orphaned — the
// nodes in the global graph cite a checkout that is gone and there is nothing
// left to re-merge them from.
//
// Like IssueOut it is never returned by Member.Issue(), for a plainer reason:
// a Member is derived from a board row, and a repository with no checkout has
// no row. Only a pass that walks the manifest itself can find one — see
// autofix.PlanFleet.
const IssueStranded = "global-stranded"

// Issue is this membership's defect code, or "" when there is nothing to do.
// A repository that was never merged is not a defect — it is a choice — so
// only a member can have one.
func (m Member) Issue() string {
	if m.In && m.Stale {
		return IssueStale
	}
	return ""
}

// MemberOf is the verdict for one graph, given when that graph was last built.
// A zero builtAt — no graph at all — is never stale: there is nothing newer to
// be stale against.
func (m *Manifest) MemberOf(graphPath string, builtAt time.Time) Member {
	e, ok := m.ByGraph(graphPath)
	if !ok {
		return Member{}
	}
	return Member{
		In:      true,
		Tag:     e.Tag,
		Nodes:   e.Nodes,
		Edges:   e.Edges,
		AddedAt: e.AddedAt,
		// Truncated to the second: the manifest records an RFC3339 timestamp
		// and a file mtime has nanoseconds, so an add and a build within the
		// same second must not read as drift.
		Stale: !builtAt.IsZero() && !e.AddedAt.IsZero() &&
			builtAt.Truncate(time.Second).After(e.AddedAt.Truncate(time.Second)),
	}
}

// GraphFor is the graph.json an output directory holds — the path the manifest
// records and the path `graphify global add` is handed. It is here rather than
// in each caller so the board and the UI cannot disagree about it.
func GraphFor(out string) string {
	if out == "" {
		return ""
	}
	return filepath.Join(out, "graph.json")
}

// normalize resolves a path far enough that two spellings of the same file
// compare equal. graphify records Path.resolve(), which follows symlinks, and
// a board root reached through one — /home/x/git when /home is a link — would
// otherwise never match its own manifest entry.
func normalize(p string) string {
	if p == "" {
		return ""
	}
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	p = filepath.Clean(p)
	// EvalSymlinks fails on a path that no longer exists — a removed checkout,
	// a graph that was deleted — and that is a case the manifest is expected
	// to contain. The cleaned path is the honest answer there.
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return resolved
	}
	return p
}
