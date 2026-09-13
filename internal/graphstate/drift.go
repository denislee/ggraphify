package graphstate

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Drift is the per-file answer behind the board's `+12 ~30 −3`.
//
// The counters alone were a dead end: a row that says `+9 −31` and does not say
// which files, while every `update` leaves the number exactly where it was, is
// unactionable — and that is the common case, because graphify's manifest is
// not "the files graphify looked at" but "the files that contributed a node to
// the graph". A file it read and got nothing from is absent from the manifest
// forever, and a walk that infers "graphify forgot this" from that absence is
// simply wrong. So the walk names what it counts, and the caller can adjudicate
// it (see Baseline) instead of arguing with it.
type Drift struct {
	Added   []string `json:"added,omitempty"`   // in the tree, absent from the manifest
	Changed []string `json:"changed,omitempty"` // newer than the manifest's mtime
	Removed []string `json:"removed,omitempty"` // in the manifest, really gone from disk
	// Acked is how many paths a Baseline suppressed: drift that a completed
	// graphify run has already looked at and declined to graph.
	Acked int `json:"acked,omitempty"`
}

// Any reports whether anything drifted.
func (d Drift) Any() bool { return len(d.Added)+len(d.Changed)+len(d.Removed) > 0 }

// Baseline is drift a successful graphify run has already adjudicated.
//
// This is the part that cannot be derived from the filesystem. graphify skips
// files for reasons the board cannot see from outside — a 24 MB JSON it parsed
// and found no symbols in, a language whose tree-sitter grammar is not
// installed, an internal heuristic added in a later release — and every one of
// them reads as a file graphify forgot to extract. So the board stops guessing:
// when `update` finishes successfully, whatever it left behind is by definition
// what graphify chose not to graph, and the board records that set. Those paths
// stop counting until their mtime moves, at which point they are a real edit
// again and count exactly once more.
//
// The alternative — mirroring graphify's skip rules in this package — was tried
// and cannot work: the rules are a moving target in another project, and the
// "read it and got nothing" case has no rule at all.
type Baseline struct {
	At time.Time `json:"at"`
	// Added maps an acknowledged path to the mtime it had when acknowledged.
	// An mtime that has moved since is a real edit and counts again.
	Added map[string]float64 `json:"added,omitempty"`
	// Removed is the set of manifest entries known to be gone for good: the
	// file is deleted and graphify has since run without dropping it from the
	// manifest, which it only rewrites when the graph's topology moves.
	Removed map[string]bool `json:"removed,omitempty"`
}

// MaxBaselinePaths bounds what is remembered per repository. A checkout whose
// drift runs to thousands of files has something else wrong with it, and the
// board's sidecar is not the place to record all of it.
const MaxBaselinePaths = 2000

// acks reports whether the baseline already covers this added path at this
// mtime.
func (b Baseline) acks(rel string, mtime float64) bool {
	was, ok := b.Added[rel]
	if !ok {
		return false
	}
	// The same slack the manifest comparison uses: graphify rounds mtimes and
	// filesystems disagree about sub-second precision.
	return mtime <= was+mtimeSlack.Seconds()
}

// MakeBaseline turns a drift report into the baseline that acknowledges it.
// The caller does this after a successful graphify run: at that point every
// path still in the report is a path graphify has seen and not graphed.
//
// before is when that run started. A file touched while it was running was not
// necessarily seen by it, so it is left counting as drift rather than quietly
// acknowledged — the next run settles it. A zero time acknowledges everything.
func MakeBaseline(repo string, d Drift, before time.Time) Baseline {
	b := Baseline{At: time.Now(), Added: map[string]float64{}, Removed: map[string]bool{}}
	for _, rel := range d.Added {
		if len(b.Added) >= MaxBaselinePaths {
			break
		}
		fi, err := os.Stat(filepath.Join(repo, filepath.FromSlash(rel)))
		if err != nil {
			continue
		}
		if !before.IsZero() && fi.ModTime().After(before) {
			continue
		}
		b.Added[rel] = float64(fi.ModTime().UnixNano()) / 1e9
	}
	for _, rel := range d.Removed {
		if len(b.Removed) >= MaxBaselinePaths {
			break
		}
		b.Removed[rel] = true
	}
	return b
}

// driftSkip are directories the drift walk never descends. They mirror what
// graphify's own extraction prunes; a mismatch here would show permanent
// phantom drift on every repository with a node_modules.
var driftSkip = map[string]bool{
	".git": true, "node_modules": true, ".venv": true, "venv": true,
	"target": true, "vendor": true, "dist": true, "build": true,
	".cache": true, "__pycache__": true, ".mypy_cache": true,
	".pytest_cache": true, ".tox": true, ".next": true, ".gradle": true,
	".idea": true, ".vscode": true,
	// The rest of graphify's own prune list (detect.py). Each of these is a
	// generated or tool-private directory graphify never indexes, so anything
	// inside one is drift the board would report forever.
	".nuxt": true, ".turbo": true, ".angular": true, ".parcel-cache": true,
	".svelte-kit": true, ".terraform": true, ".serverless": true,
	".graphify": true, ".obsidian": true, ".smart-env": true,
	".worktrees": true, "__snapshots__": true, "storybook-static": true,
	"dist-protected": true,
}

// driftSkipFiles are graphify's _SKIP_FILES: generated files it never extracts
// whatever directory they sit in. A repository with a package-lock.json showed
// `+1` forever before this.
var driftSkipFiles = map[string]bool{
	"package-lock.json": true, "yarn.lock": true, "pnpm-lock.yaml": true,
	"Cargo.lock": true, "poetry.lock": true, "Gemfile.lock": true,
	"composer.lock": true, "go.sum": true, "go.work.sum": true,
	".graphifyinclude": true,
}

// sensitiveDirs are the directory names graphify treats as credential stores.
// Inside one it indexes genuine programming-language source and nothing else,
// so `secrets/Pulumi.yaml` is not a file it forgot — it is a file it refuses.
var sensitiveDirs = map[string]bool{
	"secrets": true, ".secrets": true, "credentials": true,
	".aws": true, ".ssh": true, ".gnupg": true,
}

// sourceExts are the extensions graphify counts as real source for the purpose
// above: everything else inside a secrets directory is dropped. It is
// deliberately a short list of unambiguous programming languages rather than a
// copy of graphify's whole CODE_EXTENSIONS, because the only thing it decides
// is whether to forgive a file the manifest does not have.
var sourceExts = map[string]bool{
	".go": true, ".py": true, ".ts": true, ".tsx": true, ".js": true,
	".jsx": true, ".mjs": true, ".cjs": true, ".rs": true, ".java": true,
	".kt": true, ".rb": true, ".php": true, ".cs": true, ".swift": true,
	".c": true, ".h": true, ".cc": true, ".cpp": true, ".hpp": true,
	".sh": true, ".bash": true, ".lua": true, ".scala": true, ".ex": true,
	".exs": true, ".dart": true, ".zig": true, ".sql": true, ".tf": true,
	".hcl": true, ".vue": true, ".svelte": true,
}

// sensitiveExts are graphify's unconditional credential extensions.
var sensitiveExts = map[string]bool{
	".pem": true, ".key": true, ".p12": true, ".pfx": true, ".cert": true,
	".crt": true, ".der": true, ".p8": true, ".tfvars": true,
}

// manifestEntry is one file's record in manifest.json.
type manifestEntry struct {
	MTime        float64 `json:"mtime"`
	ASTHash      string  `json:"ast_hash"`
	SemanticHash string  `json:"semantic_hash"`
}

// mtimeSlack is how far a file's mtime may exceed the manifest's before it
// counts as changed. graphify records mtimes as floats and rounds them, and
// filesystems disagree about sub-second precision; without slack every
// repository would show phantom drift on every file.
const mtimeSlack = 2 * time.Second

// driftPathsMax bounds how many names are carried per category. The counts are
// exact; the lists are for a human to read, and nobody reads the 300th.
const driftPathsMax = 100

// DriftOf walks the tree and names everything that has moved relative to the
// manifest.
//
// Two rules are what make the answer honest, and both were bugs:
//
//   - A manifest entry is "removed" only when the file is really gone. The walk
//     skips directories — .gitignored ones, generated ones, nested checkouts —
//     and a file inside one is still on disk and still in the graph. Inferring
//     removal from "the walk did not reach it" reported 31 permanently missing
//     files on a repository that had lost none.
//   - The walk honours nested .gitignore files, not only the root one. A
//     subdirectory ignoring its own generated artefacts is the ordinary shape
//     of a monorepo, and every one of those files read as drift.
func DriftOf(repo, out string, ignore map[string]bool, base Baseline) Drift {
	var d Drift
	b, err := os.ReadFile(filepath.Join(out, "manifest.json"))
	if err != nil {
		return d
	}
	var man map[string]manifestEntry
	if err := json.Unmarshal(b, &man); err != nil || len(man) == 0 {
		return d
	}
	// Only file types the manifest already contains are considered. graphify
	// extracts a specific set of languages, and counting a README or a PNG as
	// an added file would report drift on every repository forever.
	exts := map[string]bool{}
	// graphify classifies an extensionless file by its shebang, so a manifest
	// can legitimately contain `.githooks/pre-push`. Without this the walk
	// never visits one, never marks it seen, and reports it as removed on
	// every single tick for the life of the graph.
	noExt := false
	for rel := range man {
		if e := filepath.Ext(rel); e != "" {
			exts[e] = true
		} else {
			noExt = true
		}
	}

	seen := make(map[string]bool, len(man))
	outBase := filepath.Base(out)
	gi := newIgnoreTree(repo)
	_ = filepath.WalkDir(repo, func(path string, de fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		name := de.Name()
		rel, relErr := filepath.Rel(repo, path)
		if relErr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if de.IsDir() {
			if path == repo {
				return nil
			}
			// DefaultOutName as well as this repository's configured one: a
			// central --out-base does not delete the in-tree graphify-out a
			// previous run left behind, and its 800-odd cached files are not
			// drift in the checkout.
			if driftSkip[name] || name == outBase || name == DefaultOutName ||
				(ignore != nil && ignore[name]) {
				return fs.SkipDir
			}
			if gi.Match(rel, true) || isNestedCheckout(path) {
				return fs.SkipDir
			}
			return nil
		}
		ext := filepath.Ext(name)
		if e, ok := man[rel]; ok {
			// A manifest hit is checked for an edit whatever else is true of
			// the file: it is in the graph, so its mtime is the question.
			seen[rel] = true
			if fi, err := de.Info(); err == nil &&
				fi.ModTime().After(floatTime(e.MTime).Add(mtimeSlack)) {
				d.Changed = append(d.Changed, rel)
			}
			return nil
		}
		if ext != "" && !exts[ext] {
			return nil
		}
		if ext == "" && !noExt {
			return nil
		}
		if gi.Match(rel, false) {
			return nil
		}
		// An extensionless file is only ever counted as *seen*, never as
		// added: whether graphify would extract one depends on its shebang,
		// which is exactly the thing this package refuses to open files to
		// find out. A Makefile is not drift.
		if ext == "" || notGraphable(rel, name, ext) {
			return nil
		}
		fi, err := de.Info()
		if err != nil {
			return nil
		}
		if base.acks(rel, float64(fi.ModTime().UnixNano())/1e9) {
			d.Acked++
			return nil
		}
		d.Added = append(d.Added, rel)
		return nil
	})

	for rel := range man {
		if seen[rel] {
			continue
		}
		// The file was not reached by the walk, which is not the same as gone:
		// it may sit under a skipped or ignored directory and still be in the
		// graph. Only a failed stat is removal.
		if _, err := os.Lstat(filepath.Join(repo, filepath.FromSlash(rel))); err == nil {
			continue
		}
		if base.Removed[rel] {
			d.Acked++
			continue
		}
		d.Removed = append(d.Removed, rel)
	}

	sort.Strings(d.Added)
	sort.Strings(d.Changed)
	sort.Strings(d.Removed)
	return d
}

// notGraphable reports whether graphify would refuse this file for one of its
// own documented reasons, so the board does not report it as a file graphify
// forgot. It mirrors graphify's generated-file and credential rules; anything
// subtler than that is what Baseline is for.
func notGraphable(rel, name, ext string) bool {
	if driftSkipFiles[name] {
		return true
	}
	if sensitiveExts[strings.ToLower(ext)] {
		return true
	}
	lower := strings.ToLower(name)
	if strings.HasPrefix(lower, ".env") || strings.HasPrefix(lower, ".envrc") {
		return true
	}
	// A credential-store directory anywhere above the file drops everything
	// that is not genuine source.
	parts := strings.Split(rel, "/")
	for _, p := range parts[:len(parts)-1] {
		if sensitiveDirs[strings.ToLower(p)] && !sourceExts[strings.ToLower(ext)] {
			return true
		}
	}
	return false
}

func floatTime(f float64) time.Time {
	if f <= 0 {
		return time.Time{}
	}
	sec := int64(f)
	return time.Unix(sec, int64((f-float64(sec))*1e9))
}

// capPaths trims a name list to what a human will actually read.
func capPaths(xs []string) []string {
	if len(xs) <= driftPathsMax {
		return xs
	}
	return xs[:driftPathsMax]
}
