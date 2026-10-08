// Package graphstate derives a repository's graph freshness from the files
// graphify leaves in graphify-out/, without running graphify at all.
//
// This is the one thing the GUI adds over the CLI: an honest freshness number
// for every checkout on one screen. It has to be cheap enough to run for ~130
// repositories on a 30-second tick, which rules out subprocesses and rules out
// parsing graph.json into memory — a mid-size repository's is 2.4 MB and a
// monorepo's can be far larger.
package graphstate

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// State is the row's headline verdict.
type State int

const (
	StateNone    State = iota // no output dir — never initialized
	StateRaw                  // a graph, but no community labels yet
	StateStale                // manifest drift, or the needs_update flag
	StateFresh                // manifest matches the tree, labels present
	StateBroken               // an output dir whose graph.json is missing or unreadable
	StateRunning              // a job for this repo is in flight (set by the UI, not here)
)

// String is the machine-readable name, used by the -print output, the filter
// chips and the settings file. Display strings live in the ui package.
func (s State) String() string {
	switch s {
	case StateNone:
		return "none"
	case StateRaw:
		return "raw"
	case StateStale:
		return "stale"
	case StateFresh:
		return "fresh"
	case StateBroken:
		return "broken"
	case StateRunning:
		return "running"
	}
	return "unknown"
}

// MarshalJSON renders the state as its name rather than its ordinal, so
// `ggraphify-scan` output stays readable when the constants are reordered.
func (s State) MarshalJSON() ([]byte, error) { return json.Marshal(s.String()) }

// ParseState is the inverse, for filter chips restored from the sidecar.
func ParseState(s string) (State, bool) {
	for _, v := range []State{StateNone, StateRaw, StateStale, StateFresh, StateBroken, StateRunning} {
		if v.String() == s {
			return v, true
		}
	}
	return StateNone, false
}

// Graph is everything the board knows about one repository's graph.
type Graph struct {
	Out   string `json:"out"` // the output directory this was read from
	State State  `json:"state"`

	Nodes       int       `json:"nodes"`
	Links       int       `json:"links"`
	Hyperedges  int       `json:"hyperedges"`
	Communities int       `json:"communities"`
	Labeled     bool      `json:"labeled"`      // .graphify_labels.json present and non-empty
	HubNamed    int       `json:"hub_named"`    // communities named after their hub symbol by graphify rather than by the LLM
	BuiltAt     time.Time `json:"built_at"`     // graph.json mtime
	BuiltCommit string    `json:"built_commit"` // graph.json's built_at_commit
	// HeadCommit is the checkout's resolved HEAD at the time of the read,
	// supplied by the caller — this package runs no git. It is what turns
	// BuiltCommit from a fact into a verdict: see Behind.
	HeadCommit string `json:"head_commit,omitempty"`
	// LLMLabeledAt is the mtime of LLMLabelSigFile — when ggraphify last
	// recorded a successful LLM labeling — and zero when there is none. The
	// planner reads it to keep a busy repository from buying a full relabel on
	// every rebuild.
	LLMLabeledAt time.Time `json:"llm_labeled_at"`

	DriftAdded   int  `json:"drift_added"`   // in the tree, absent from the manifest
	DriftChanged int  `json:"drift_changed"` // newer than the manifest's mtime
	DriftRemoved int  `json:"drift_removed"` // in the manifest, really gone from disk
	NeedsUpdate  bool `json:"needs_update"`  // the needs_update flag file exists

	// DriftFiles names what drifted, capped at driftPathsMax per category. A
	// count on its own is not actionable — "+9 −31, and every update leaves it
	// there" is a bug report nobody can act on without the names.
	DriftFiles Drift `json:"drift_files,omitempty"`

	SizeBytes  int64 `json:"size_bytes"` // the whole output dir
	GraphBytes int64 `json:"graph_bytes"`

	HasHTML     bool `json:"has_html"`
	HasTreeHTML bool `json:"has_tree_html"`
	HasCallflow bool `json:"has_callflow"`
	HasWiki     bool `json:"has_wiki"`
	HasReport   bool `json:"has_report"`
	HasCache    bool `json:"has_cache"`
	Watched     bool `json:"-"` // a `graphify watch` job is running (set by the UI)

	// Err is why the state is Broken, when it is.
	Err string `json:"err,omitempty"`
}

// Behind reports that the graph was built from a commit that is no longer
// HEAD — history moved under it, whether or not a single working file did.
//
// It is a separate question from drift and neither implies the other: a
// `git checkout` of another branch rewrites the tree and moves HEAD (both), a
// `git commit` of already-extracted files moves only HEAD, and an unsaved
// editor buffer moves only the tree. Answering it needs the caller's Head,
// so a Graph read without one is never Behind.
//
// The comparison is by prefix because the two strings do not have to be the
// same length: graphify has written both a short and a full built_at_commit
// across versions, and a full-vs-short mismatch must not read as "behind" for
// every repository on the board.
func (g Graph) Behind() bool {
	if g.BuiltCommit == "" || g.HeadCommit == "" {
		return false
	}
	return !sameCommit(g.BuiltCommit, g.HeadCommit)
}

// sameCommit is that prefix comparison on its own, because Restamp has to ask
// exactly the same question before it rewrites anything — a stamp this says is
// already current must never be "advanced" to a different spelling of itself.
func sameCommit(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	if len(a) > len(b) {
		a, b = b, a
	}
	return strings.HasPrefix(b, a)
}

// Drift reports whether anything has moved since the graph was built.
func (g Graph) Drift() bool {
	return g.DriftAdded+g.DriftChanged+g.DriftRemoved > 0 || g.NeedsUpdate
}

// DriftString is the compact `+12 ~30 −3` the Drift column renders. It is
// empty when the graph is in step with the tree.
func (g Graph) DriftString() string {
	if g.State == StateNone {
		return ""
	}
	var parts []string
	if g.DriftAdded > 0 {
		parts = append(parts, "+"+itoa(g.DriftAdded))
	}
	if g.DriftChanged > 0 {
		parts = append(parts, "~"+itoa(g.DriftChanged))
	}
	if g.DriftRemoved > 0 {
		parts = append(parts, "−"+itoa(g.DriftRemoved))
	}
	if g.NeedsUpdate {
		parts = append(parts, "needs-extract")
	}
	return strings.Join(parts, " ")
}

// HasGraph reports whether an output directory holds a graph a command could
// read. It is two stats, deliberately: this is asked on the click path, where
// a full Read of a 2.4 MB graph.json would be absurd, and it has to reflect the
// filesystem *now* rather than whatever the last board scan saw — a graph can
// be deleted, or built, between two 30-second ticks.
//
// An empty graph.json is treated as absent: that is what a killed build leaves
// behind, and every command that reads it fails the same way on both.
func HasGraph(out string) bool {
	if out == "" {
		return false
	}
	fi, err := os.Stat(filepath.Join(out, "graph.json"))
	return err == nil && fi.Mode().IsRegular() && fi.Size() > 0
}

// DefaultOutName is graphify's own output directory name, which is what a
// repository uses when nothing has been configured. discover.DefaultOutName is
// the same string; this package does not import that one, because a board-side
// resolver and a filesystem reader that never runs a command have no business
// depending on each other.
const DefaultOutName = "graphify-out"

// MaxGraphBytes is the ceiling above which graph.json is not opened at all.
// Above it the row shows size only and the detail pane offers an external
// open — a 200 MB monorepo graph must not be able to stall a refresh tick even
// through a streaming decoder.
const MaxGraphBytes = 128 << 20

// LLMLabelSigFile is ggraphify's copy of graphify's .graphify_labels.json.sig
// taken when a label job succeeds; a community whose current signature differs
// from it has been renamed by hub since the LLM named it.
const LLMLabelSigFile = ".ggraphify_llm_labels.sig"

// Options configures a read.
type Options struct {
	// Repo is the checkout root; drift is computed against its tree.
	Repo string
	// Out is the output directory. Empty means Repo/graphify-out.
	Out string
	// Head is the checkout's resolved HEAD commit, when the caller knows it.
	// Empty is not an error — it only means the read cannot judge whether the
	// graph is behind history, and Behind stays false.
	Head string
	// SkipDrift omits the tree walk. The board wants drift; a one-shot that
	// only needs counters does not.
	SkipDrift bool
	// Ignore are directory names the drift walk does not descend into, in
	// addition to the built-in set.
	Ignore map[string]bool
	// Baseline is drift a previous successful graphify run already adjudicated,
	// and which therefore does not count until the files move again. The board
	// supplies it from its sidecar; a one-shot scan can leave it zero and see
	// the raw walk.
	Baseline Baseline
	// CommitOnly reads graph.json's built_at_commit (a tail read) and nothing
	// else of it: no element counts, no labels, no communities, no directory
	// size. It is for callers that ask only where a graph is, whether it is
	// behind HEAD and what tier it reached — `ggraphify doctor`. Counters stay
	// zero and an unlabelled-looking graph reads as Raw rather than Fresh,
	// because a read that never opened the labels must not vouch for them.
	CommitOnly bool
}

// Read derives the graph state for one repository.
//
// It never returns an error for "this repository has no graph" — that is
// StateNone, an ordinary and very common answer. An error comes back only
// when the arguments themselves are unusable.
func Read(opts Options) (Graph, error) {
	g, final, err := readStatic(opts)
	if err != nil || final {
		return g, err
	}
	return finishRead(g, opts), nil
}

// readStatic is the half of Read that depends only on the output directory —
// everything but drift and the verdict that drift decides. Cache keeps it
// across ticks for as long as the directory's stamp holds still. final reports
// a Graph that is already its own answer (no graph, or a broken one), which
// finishRead must not touch.
func readStatic(opts Options) (g Graph, final bool, err error) {
	if opts.Repo == "" {
		return Graph{}, true, errors.New("graphstate: no repo path")
	}
	out := opts.Out
	if out == "" {
		out = filepath.Join(opts.Repo, DefaultOutName)
	}
	g = Graph{Out: out, HeadCommit: opts.Head}

	fi, err := os.Stat(out)
	if err != nil || !fi.IsDir() {
		g.State = StateNone
		return g, true, nil
	}

	g.HasReport = exists(filepath.Join(out, "GRAPH_REPORT.md"))
	g.HasHTML = exists(filepath.Join(out, "graph.html"))
	g.HasTreeHTML = exists(filepath.Join(out, "GRAPH_TREE.html"))
	g.HasCallflow = exists(filepath.Join(out, "callflow.html"))
	g.HasWiki = exists(filepath.Join(out, "wiki", "index.md"))
	g.HasCache = exists(filepath.Join(out, "cache"))
	g.NeedsUpdate = exists(filepath.Join(out, "needs_update"))
	if !opts.CommitOnly {
		g.SizeBytes = dirSize(out)
	}

	gj := filepath.Join(out, "graph.json")
	gfi, err := os.Stat(gj)
	if err != nil {
		g.State = StateBroken
		g.Err = "graph.json is missing"
		return g, true, nil
	}
	g.BuiltAt = gfi.ModTime()
	g.GraphBytes = gfi.Size()

	if opts.CommitOnly {
		if gfi.Size() > MaxGraphBytes {
			// The same verdict the full read gives, so the two never disagree
			// about whether a huge graph is behind.
			g.State = StateRaw
			g.Err = "graph.json is larger than the parse ceiling; counters not read"
		} else if c, err := ReadBuiltCommit(out); err != nil {
			g.State = StateBroken
			g.Err = "graph.json: " + err.Error()
			return g, true, nil
		} else {
			g.BuiltCommit = c
		}
		return g, false, nil
	}

	if gfi.Size() > MaxGraphBytes {
		// Deliberately not parsed. The row still carries size, age and drift,
		// which is enough to decide what to do about it.
		g.State = StateRaw
		g.Err = "graph.json is larger than the parse ceiling; counters not read"
	} else if err := readGraph(gj, &g); err != nil {
		g.State = StateBroken
		g.Err = "graph.json: " + err.Error()
		return g, true, nil
	}

	g.Communities, g.Labeled, g.HubNamed = readLabelState(out)
	if fi, err := os.Stat(filepath.Join(out, LLMLabelSigFile)); err == nil {
		g.LLMLabeledAt = fi.ModTime()
	}
	if g.Communities == 0 {
		// `extract` writes .graphify_analysis.json with every community it
		// detected; .graphify_labels.json only appears once `cluster-only` or
		// `label` has run. Without this fallback a freshly extracted graph
		// reports no communities at all, when what is true is that it has 355
		// of them and none has a name yet — which is the difference between
		// "nothing to see" and "one free command away from a report".
		g.Communities = readAnalysisCommunities(out)
	}
	return g, false, nil
}

// finishRead is the other half: the drift walk, which no stamp on the output
// directory can vouch for, and the verdict.
func finishRead(g Graph, opts Options) Graph {
	out := g.Out
	if !opts.SkipDrift {
		d := DriftOf(opts.Repo, out, opts.Ignore, opts.Baseline)
		g.DriftAdded, g.DriftChanged, g.DriftRemoved = len(d.Added), len(d.Changed), len(d.Removed)
		g.DriftFiles = Drift{
			Added:   capPaths(d.Added),
			Changed: capPaths(d.Changed),
			Removed: capPaths(d.Removed),
			Acked:   d.Acked,
		}
	}

	switch {
	case g.Err != "" && (g.State == StateBroken || g.State == StateRaw):
		// Already decided, and it stays decided. Broken is obvious; Raw is the
		// graph that was too large to parse, whose counters were never read —
		// calling that one fresh would vouch for numbers nobody has seen. The
		// row still carries size, age and drift, which is what you act on.
	case g.NeedsUpdate || g.DriftAdded+g.DriftChanged+g.DriftRemoved > 0 || g.Behind():
		// Behind counts as stale even with a clean tree. The graph answers
		// queries about the commit it was built from, and once that is not
		// HEAD any more the answer is about code this checkout no longer has —
		// which is the failure that looks like a success.
		g.State = StateStale
	case !g.Labeled:
		g.State = StateRaw
	default:
		g.State = StateFresh
	}
	return g
}

// skipValue consumes exactly one JSON value, however nested.
func skipValue(dec *json.Decoder) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if d, ok := tok.(json.Delim); ok && (d == '{' || d == '[') {
		return skipRest(dec)
	}
	return nil
}

// skipRest consumes the remainder of the composite value whose opening
// delimiter has just been read.
func skipRest(dec *json.Decoder) error {
	for dec.More() {
		if err := skipValue(dec); err != nil {
			return err
		}
	}
	_, err := dec.Token() // the closing delimiter
	return err
}

// labelName is one community's entry in .graphify_labels.json: a bare name,
// or an object carrying one under "name". Anything else is a nameless entry —
// still a community, never a label.
type labelName string

func (l *labelName) UnmarshalJSON(b []byte) error {
	*l = ""
	switch {
	case len(b) > 0 && b[0] == '"':
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*l = labelName(s)
	case len(b) > 0 && b[0] == '{':
		var o struct {
			Name any `json:"name"`
		}
		if err := json.Unmarshal(b, &o); err != nil {
			return err
		}
		s, _ := o.Name.(string)
		*l = labelName(s)
	}
	return nil
}

// readLabelFile reads .graphify_labels.json once, into a typed map. Some
// versions nest the mapping under a "labels" key; that layout costs a second
// decode of the same bytes, the flat one does not.
func readLabelFile(out string) (map[string]labelName, bool) {
	b, err := os.ReadFile(filepath.Join(out, ".graphify_labels.json"))
	if err != nil {
		return nil, false
	}
	var m map[string]labelName
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, false
	}
	if _, nested := m["labels"]; nested {
		var doc struct {
			Labels map[string]labelName `json:"labels"`
		}
		if json.Unmarshal(b, &doc) == nil && doc.Labels != nil {
			m = doc.Labels
		}
	}
	return m, true
}

// readLabelState is readLabels and readHubNamed over a single read of the
// labels file.
func readLabelState(out string) (n int, labeled bool, hubNamed int) {
	m, ok := readLabelFile(out)
	if !ok {
		return 0, false, 0
	}
	n, labeled = countLabels(m)
	return n, labeled, countHubNamed(out, m)
}

// readLabels counts communities from .graphify_labels.json.
//
// "Labeled" means the file exists and names at least one community with a
// non-placeholder name: graphify writes `community_0`-style placeholders
// before the LLM labelling pass has run, and a row that claimed to be labelled
// on the strength of those would be lying about the expensive step.
func readLabels(out string) (n int, labeled bool) {
	m, ok := readLabelFile(out)
	if !ok {
		return 0, false
	}
	return countLabels(m)
}

func countLabels(m map[string]labelName) (n int, labeled bool) {
	for k, v := range m {
		n++
		if s := string(v); s != "" && !isPlaceholder(s) && !isPlaceholder(k) {
			labeled = true
		}
	}
	return n, labeled && n > 0
}

// readHubNamed counts the communities whose current name was chosen by
// graphify's hub fallback rather than by the LLM.
//
// When ggraphify has a record of a previous successful LLM labeling, the
// signature files are the authority: graphify's .graphify_labels.json.sig
// carries one signature per community, and a community whose current signature
// differs from (or is missing from) the snapshot has been re-clustered and
// renamed by hub since that labeling. Without a snapshot we fall back to the
// shape of the name itself: graphify's hub fallback is a single symbol or path
// ("testing.T", ".Return"), so any name with no whitespace in it is one.
func readHubNamed(out string) int {
	m, ok := readLabelFile(out)
	if !ok {
		return 0
	}
	return countHubNamed(out, m)
}

func countHubNamed(out string, m map[string]labelName) int {
	// The signatures are decoded only when both exist: one without the other
	// is heuristic mode, and decoding it would be work thrown away.
	var (
		cur, snap map[string]string
		sigs      bool
	)
	curPath := filepath.Join(out, ".graphify_labels.json.sig")
	snapPath := filepath.Join(out, LLMLabelSigFile)
	if exists(curPath) && exists(snapPath) {
		var curOK, snapOK bool
		cur, curOK = readSigMap(curPath)
		if curOK {
			snap, snapOK = readSigMap(snapPath)
		}
		sigs = curOK && snapOK
	}

	n := 0
	for k, v := range m {
		s := string(v)
		if s == "" || isPlaceholder(s) {
			continue
		}
		if sigs {
			// Snapshot mode: the signature moved (or is new) since the LLM
			// named it.
			if snapV, ok := snap[k]; !ok || snapV != cur[k] {
				n++
			}
			continue
		}
		// Heuristic mode, no snapshot yet: the hub fallback is a single symbol
		// or path with no spaces, whereas LLM names are phrases.
		if !strings.ContainsAny(strings.TrimSpace(s), " \t\n\r\f\v") {
			n++
		}
	}
	return n
}

// readSigMap loads a signature file as community-id to signature. The second
// result is false when the file is missing or unreadable, which is what tells
// readHubNamed to fall back to the name-shape heuristic rather than treat every
// community as unchanged.
func readSigMap(path string) (map[string]string, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	var m map[string]string
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, false
	}
	return m, true
}

// MarkLLMLabeled records that the LLM has just named the communities of out, by
// copying graphify's current .graphify_labels.json.sig to ggraphify's own
// LLMLabelSigFile.
//
// If graphify wrote no signature file there is nothing to record and nothing is
// written: the label job is not the thing that failed. Any other error is
// returned. The write is atomic so a crash mid-copy cannot leave a partial
// snapshot that would read as "every community was renamed".
func MarkLLMLabeled(out string) error {
	b, err := os.ReadFile(filepath.Join(out, ".graphify_labels.json.sig"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	dir := out
	tmp, err := os.CreateTemp(dir, ".ggraphify_llm_labels.sig.*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, filepath.Join(dir, LLMLabelSigFile)); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return nil
}

// readAnalysisCommunities counts the communities graphify recorded in
// .graphify_analysis.json, whose "communities" member maps a community id to
// its member node ids.
//
// Counted with the streaming decoder rather than unmarshalled: the member
// lists are the whole node set again — 400 KB on the reference repository —
// and the board wants one integer from it on every tick.
func readAnalysisCommunities(out string) int {
	f, err := os.Open(filepath.Join(out, ".graphify_analysis.json"))
	if err != nil {
		return 0
	}
	defer f.Close()

	dec := json.NewDecoder(f)
	tok, err := dec.Token()
	if err != nil {
		return 0
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return 0
	}
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return 0
		}
		key, _ := keyTok.(string)
		if key != "communities" {
			if skipValue(dec) != nil {
				return 0
			}
			continue
		}
		tok, err := dec.Token()
		if err != nil {
			return 0
		}
		d, ok := tok.(json.Delim)
		if !ok {
			return 0
		}
		n := 0
		switch d {
		case '{':
			for dec.More() {
				if _, err := dec.Token(); err != nil { // the community id
					return n
				}
				if skipValue(dec) != nil {
					return n
				}
				n++
			}
		case '[':
			for dec.More() {
				if skipValue(dec) != nil {
					return n
				}
				n++
			}
		default:
			return 0
		}
		return n
	}
	return 0
}

func isPlaceholder(s string) bool {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" || s == "unlabeled" || s == "unknown" {
		return true
	}
	if !strings.HasPrefix(s, "community") && !strings.HasPrefix(s, "cluster") {
		return false
	}
	// "community_7" is a placeholder; "community detection internals" is not.
	rest := strings.TrimLeft(strings.TrimPrefix(strings.TrimPrefix(s, "community"), "cluster"), "_- ")
	for i := 0; i < len(rest); i++ {
		if rest[i] < '0' || rest[i] > '9' {
			return false
		}
	}
	return true
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

// dirSize sums the output directory. It is a plain walk, bounded in practice
// by graphify's own output, and it is what the Size column shows.
func dirSize(dir string) int64 {
	var n int64
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if fi, err := d.Info(); err == nil {
			n += fi.Size()
		}
		return nil
	})
	return n
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var d [20]byte
	i := len(d)
	for n > 0 {
		i--
		d[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		d[i] = '-'
	}
	return string(d[i:])
}
