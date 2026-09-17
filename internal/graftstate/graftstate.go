// Package graftstate derives a repository's graft index freshness from the
// files graft leaves in graft/, without running graft at all.
//
// It is the sibling of internal/graphstate, and it exists for the same reason:
// the board shows ~130 checkouts on a 30-second tick, so the only affordable
// answer is one computed from stats and one small sidecar. `graft check` is
// the authoritative answer and it re-extracts every source file through
// tree-sitter — seconds per repository — which rules it out entirely here.
//
// What graft leaves behind, and what this package reads:
//
//	graft/.graph/wiring.json          the graph; its `meta` carries the counts
//	graft/.cache/fingerprint.*.json   (size, mtimeMs, sha256) per source file
//	graft/manifest.json               the --deep concept layer's roster
//
// The drift answer is deliberately narrower than graphstate's: this package
// reports files graft recorded that have since CHANGED or been REMOVED, and
// says nothing about files that were added. Naming an added file would mean
// mirroring graft's own extension table, gitignore handling and `--only-dir`
// whitelist, which is a moving target in another project — and getting it
// wrong shows a permanently stale row that no rebuild can clear.
package graftstate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// State is the row's headline verdict about the graft index.
type State int

const (
	StateNone   State = iota // no graft/ directory — never built
	StateRaw                 // a wiring graph, but no --deep concept layer
	StateStale               // recorded files have changed or gone
	StateFresh               // in step with the tree, concept layer present
	StateBroken              // a graft/ directory with no readable wiring.json
)

// String is the machine-readable name, used by ggraphify-scan's JSON.
func (s State) String() string {
	switch s {
	case StateNone:
		return "none"
	case StateRaw:
		return "wiring"
	case StateStale:
		return "stale"
	case StateFresh:
		return "fresh"
	case StateBroken:
		return "broken"
	}
	return "unknown"
}

// MarshalJSON renders the state as its name rather than its ordinal.
func (s State) MarshalJSON() ([]byte, error) { return json.Marshal(s.String()) }

// DirName is graft's own output directory name: `<repo>/graft`, fixed in
// graft's contextDirFor. A `--dir` elsewhere is possible on the command line
// and is not something the board can discover, so it is not guessed at.
const DirName = "graft"

// The paths inside it, named once.
const (
	graphDir      = ".graph"
	wiringFile    = "wiring.json"
	cacheDir      = ".cache"
	fingerprintPr = "fingerprint."
	manifestFile  = "manifest.json"
	indexFile     = "INDEX.md"
)

// MaxWiringBytes is the ceiling above which wiring.json is not opened. The
// counters live in a `meta` object at the very top of the file, so the read is
// a few hundred bytes whatever the file's size — but a file this large is
// usually a truncated or foreign one, and the row says size only.
const MaxWiringBytes = 512 << 20

// maxHashed bounds how many files whose (size, mtime) moved are re-hashed to
// decide whether their bytes actually changed. This is the same rule graft's
// own probe uses — a `touch` or a branch switch that restores identical bytes
// must not read as drift — and the cap is what keeps a repository whose whole
// tree was just rewritten from costing a full content hash on every tick.
const maxHashed = 256

// driftPathsMax bounds how many names are carried per category, for the
// tooltip. The counts are exact; the names are a sample.
const driftPathsMax = 12

// mtimeSlack is how far a file's mtime may exceed the recorded one before the
// file is even considered for re-hashing.
const mtimeSlack = 2 * time.Second

// Index is everything the board knows about one repository's graft index.
type Index struct {
	Dir   string `json:"dir"` // the graft directory this was read from
	State State  `json:"state"`

	Nodes     int       `json:"nodes"`
	Edges     int       `json:"edges"`
	Files     int       `json:"files"`               // source files the fingerprint recorded
	Languages []string  `json:"languages,omitempty"` // as wiring.json's meta reports them
	Deep      bool      `json:"deep"`                // the LLM concept layer (manifest.json) is present
	HasIndex  bool      `json:"has_index"`           // INDEX.md, the repo map agents read
	BuiltAt   time.Time `json:"built_at"`            // wiring.json's mtime
	SizeBytes int64     `json:"size_bytes"`

	DriftChanged int      `json:"drift_changed"` // recorded, and the bytes have moved
	DriftRemoved int      `json:"drift_removed"` // recorded, and gone from disk
	DriftFiles   []string `json:"drift_files,omitempty"`
	// Unverified is how many files were assumed changed on their stat alone,
	// because the re-hash budget ran out. Without this number a big count
	// would look exact when it is an upper bound.
	Unverified int `json:"unverified,omitempty"`

	// Exposed reports that the index directory is visible to git: no rule in
	// the repository's .gitignore covers it.
	//
	// graft writes that rule itself on every build (`--no-gitignore` and
	// GRAFT_NO_GITIGNORE=1 are the opt-outs), and the whole separation between
	// the two indexes rests on it. An exposed graft/ is one markdown card per
	// source file sitting in the working tree, and graphify's extraction —
	// which is .gitignore-aware, and is the reason it has never seen them —
	// will index every one of those cards as source on the next run: a second
	// prose copy of the codebase in graph.json, counted as drift until it is,
	// and paid for at the metered rate when the run is an extract.
	Exposed bool `json:"exposed,omitempty"`

	// Err is why the state is Broken, when it is.
	Err string `json:"err,omitempty"`
}

// Drift reports whether anything the index recorded has moved.
func (i Index) Drift() bool { return i.DriftChanged+i.DriftRemoved > 0 }

// DriftString is the compact `~12 −3` the board renders, empty when the index
// is in step with the tree.
func (i Index) DriftString() string {
	var parts []string
	if i.DriftChanged > 0 {
		parts = append(parts, "~"+itoa(i.DriftChanged))
	}
	if i.DriftRemoved > 0 {
		parts = append(parts, "−"+itoa(i.DriftRemoved))
	}
	return strings.Join(parts, " ")
}

// NeedsBuild reports whether `graft build` has work to do here: no index at
// all, an unreadable one, or one the tree has moved under.
func (i Index) NeedsBuild() bool {
	switch i.State {
	case StateNone, StateBroken, StateStale:
		return true
	}
	return false
}

// DirFor is the graft directory of a checkout.
func DirFor(repo string) string { return filepath.Join(repo, DirName) }

// HasIndex reports whether a graft directory holds a wiring graph a query
// could read. Two stats, for the click path.
func HasIndex(dir string) bool {
	if dir == "" {
		return false
	}
	fi, err := os.Stat(filepath.Join(dir, graphDir, wiringFile))
	return err == nil && fi.Mode().IsRegular() && fi.Size() > 0
}

// Options configures a read.
type Options struct {
	// Repo is the checkout root; drift is measured against its tree.
	Repo string
	// Dir is the graft directory. Empty means Repo/graft.
	Dir string
	// SkipDrift omits the per-file stat pass.
	SkipDrift bool
}

// Read derives the graft state for one repository.
//
// As in graphstate, "this repository has no graft index" is not an error —
// it is StateNone, and it is the common answer.
func Read(opts Options) (Index, error) {
	if opts.Repo == "" {
		return Index{}, errors.New("graftstate: no repo path")
	}
	dir := opts.Dir
	if dir == "" {
		dir = DirFor(opts.Repo)
	}
	i := Index{Dir: dir}

	fi, err := os.Stat(dir)
	if err != nil || !fi.IsDir() {
		i.State = StateNone
		return i, nil
	}

	i.HasIndex = exists(filepath.Join(dir, indexFile))
	i.Deep = exists(filepath.Join(dir, manifestFile))

	wiring := filepath.Join(dir, graphDir, wiringFile)
	fpPath := newestFingerprint(filepath.Join(dir, cacheDir))
	if !exists(wiring) && fpPath == "" && !i.HasIndex && !i.Deep {
		// A directory called `graft` that carries none of graft's own files is
		// somebody's source directory that happens to share the name, not an
		// index this board may call broken. Reporting it as broken would put
		// a red row — and a `graft build` in the sweep — on a repository that
		// has nothing to do with graft.
		i.State = StateNone
		i.Dir = dir
		return i, nil
	}
	i.Exposed = exposedToGit(opts.Repo, dir)
	i.SizeBytes = dirSize(dir)

	wfi, err := os.Stat(wiring)
	if err != nil || wfi.Size() == 0 {
		i.State = StateBroken
		i.Err = filepath.Join(graphDir, wiringFile) + " is missing or empty — an interrupted build leaves this"
		return i, nil
	}
	i.BuiltAt = wfi.ModTime()
	if wfi.Size() > MaxWiringBytes {
		i.State = StateRaw
		i.Err = "wiring.json is larger than the parse ceiling; counters not read"
	} else if err := readMeta(wiring, &i); err != nil {
		i.State = StateBroken
		i.Err = wiringFile + ": " + err.Error()
		return i, nil
	}

	fp, ok := readFingerprint(fpPath)
	i.Files = len(fp)
	if ok && !opts.SkipDrift {
		driftOf(opts.Repo, fp, &i)
	}

	switch {
	case i.Err != "" && i.State == StateBroken:
		// already decided
	case i.Drift():
		i.State = StateStale
	case !i.Deep:
		// A wiring-only build is graft's free default and is genuinely useful;
		// it is "raw" in the same sense graphstate means it — the LLM layer
		// (concept nodes, per-symbol summaries) has not been run.
		i.State = StateRaw
	default:
		i.State = StateFresh
	}
	return i, nil
}

// readMeta pulls the counters out of wiring.json's `meta` object and stops
// there. `meta` is written first, so this reads a few hundred bytes of a file
// that is megabytes long — the nodes and edges arrays are never tokenised,
// let alone materialised.
func readMeta(path string, i *Index) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	dec := json.NewDecoder(f)
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return errors.New("not a JSON object")
	}
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return err
		}
		key, _ := keyTok.(string)
		if key != "meta" {
			if err := skipValue(dec); err != nil {
				return err
			}
			continue
		}
		var meta struct {
			NodeCount int      `json:"nodeCount"`
			EdgeCount int      `json:"edgeCount"`
			Languages []string `json:"languages"`
		}
		if err := dec.Decode(&meta); err != nil {
			return err
		}
		i.Nodes, i.Edges, i.Languages = meta.NodeCount, meta.EdgeCount, meta.Languages
		return nil // everything after meta is the graph itself
	}
	return nil
}

// skipValue consumes exactly one JSON value, however nested.
func skipValue(dec *json.Decoder) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if d, ok := tok.(json.Delim); ok && (d == '{' || d == '[') {
		for dec.More() {
			if err := skipValue(dec); err != nil {
				return err
			}
		}
		_, err = dec.Token() // the matching close
		return err
	}
	return nil
}

// print is one file's record in the fingerprint: size, mtime in milliseconds,
// and the sha256 of its bytes, exactly as graft writes them.
type print struct {
	size   int64
	mtime  float64
	digest string
}

// readFingerprint reads graft's probe sidecar, already resolved by
// newestFingerprint: the file is keyed by extractor identity — two grafts on
// one repo (an npx MCP server and a local install) each keep their own — so
// the newest is the one that describes this tree.
//
// The bool reports whether a fingerprint was found at all, which is not the
// same as an empty one: no fingerprint means "nothing recorded, so nothing can
// have drifted", and the row must not claim freshness it has not checked.
func readFingerprint(path string) (map[string]print, bool) {
	if path == "" {
		return nil, false
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer f.Close()

	var doc struct {
		Version int              `json:"version"`
		Files   map[string][]any `json:"files"`
	}
	if err := json.NewDecoder(f).Decode(&doc); err != nil {
		return nil, false
	}
	out := make(map[string]print, len(doc.Files))
	for rel, rec := range doc.Files {
		if len(rec) < 3 {
			continue
		}
		size, _ := rec[0].(float64)
		mtime, _ := rec[1].(float64)
		digest, _ := rec[2].(string)
		out[rel] = print{size: int64(size), mtime: mtime, digest: digest}
	}
	return out, true
}

// newestFingerprint picks the most recently written fingerprint.<stamp>.json.
func newestFingerprint(cache string) string {
	ents, err := os.ReadDir(cache)
	if err != nil {
		return ""
	}
	best, bestAt := "", time.Time{}
	for _, e := range ents {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, fingerprintPr) || !strings.HasSuffix(name, ".json") {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		if best == "" || fi.ModTime().After(bestAt) {
			best, bestAt = filepath.Join(cache, name), fi.ModTime()
		}
	}
	return best
}

// driftOf compares the recorded files against the tree: one stat each, and a
// content hash only for the ones whose stat disagrees. That is graft's own
// probe rule, and it is why a `touch` or a `git checkout` that restores
// identical bytes does not show up here as work to do.
func driftOf(repo string, fp map[string]print, i *Index) {
	rels := make([]string, 0, len(fp))
	for rel := range fp {
		rels = append(rels, rel)
	}
	sort.Strings(rels) // a deterministic sample, and a deterministic hash budget

	var changed, removed []string
	hashed := 0
	for _, rel := range rels {
		rec := fp[rel]
		abs := filepath.Join(repo, filepath.FromSlash(rel))
		fi, err := os.Stat(abs)
		if err != nil || fi.IsDir() {
			removed = append(removed, rel)
			continue
		}
		if fi.Size() == rec.size && !movedSince(fi.ModTime(), rec.mtime) {
			continue
		}
		if hashed < maxHashed && rec.digest != "" {
			hashed++
			if sum, err := hashFile(abs); err == nil && sum == rec.digest {
				continue // the stat moved, the bytes did not
			} else if err != nil {
				i.Unverified++
			}
		} else if rec.digest != "" {
			i.Unverified++
		}
		changed = append(changed, rel)
	}

	i.DriftChanged, i.DriftRemoved = len(changed), len(removed)
	i.DriftFiles = sample(changed, removed)
}

// movedSince reports whether a file's mtime is meaningfully past the recorded
// one. graft records milliseconds as a float; filesystems and clocks disagree
// about the last few of them.
func movedSince(mod time.Time, recorded float64) bool {
	return float64(mod.UnixNano())/1e6 > recorded+float64(mtimeSlack/time.Millisecond)
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// sample names a few of the drifted files for the tooltip, marked the same way
// the Drift column spells them.
func sample(changed, removed []string) []string {
	var out []string
	add := func(mark string, paths []string) {
		for _, p := range paths {
			if len(out) >= driftPathsMax {
				return
			}
			out = append(out, mark+" "+p)
		}
	}
	add("~", changed)
	add("−", removed)
	return out
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// dirSize totals a directory tree, ignoring what it cannot read.
func dirSize(dir string) int64 {
	var n int64
	// The walk's own error is dropped on purpose: every per-entry error is
	// already swallowed below, so the only thing left for WalkDir to report is
	// that the root does not exist — and "a tree that is not there totals
	// zero" is this function's documented answer, not a failure.
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
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

// exposedToGit reports whether the repository's root .gitignore leaves the
// graft directory in the working tree for every other tool to find.
//
// Only the root file, and only a rule that names graft's own directory:
// `graft/`, `/graft/`, `graft` and their negations, which is the entry graft
// writes (and the one its `retract` strips again, matched by the same shape).
// This is not a gitignore implementation and must not become one — a pattern
// this misses shows as a note beside a fact, never as a state, and the answer
// this package exists to give is unaffected either way.
//
// A graft directory somewhere other than <repo>/graft is nobody's business
// here: it was put there with `--dir`, this package never guesses at one, and
// judging a path the board did not derive would be guessing.
func exposedToGit(repo, dir string) bool {
	if dir != DirFor(repo) {
		return false
	}
	b, err := os.ReadFile(filepath.Join(repo, ".gitignore"))
	if err != nil {
		// No .gitignore at all is the plainest case of exposed there is.
		return true
	}
	exposed := true
	for _, line := range strings.Split(string(b), "\n") {
		s := strings.TrimSpace(line)
		if s == "" || strings.HasPrefix(s, "#") {
			continue
		}
		negate := strings.HasPrefix(s, "!")
		s = strings.TrimPrefix(s, "!")
		// git's last-match-wins: a later `!graft/` re-admits the directory.
		if strings.Trim(s, "/") == DirName {
			exposed = negate
		}
	}
	return exposed
}
