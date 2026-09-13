// Package discover finds local checkouts under a set of roots and reads the
// little git metadata a board row needs.
//
// It imports no GTK and shells out to nothing. In particular it never runs
// `git`: 128 repositories × one `git status` per refresh tick is not a cost a
// 30-second timer can carry, so branch and HEAD are read straight out of
// .git/, and dirtiness is left to the caller to compute for one selected repo
// at a time (see Dirty).
package discover

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// DefaultDepth is how far below a root a checkout may sit before the walk
// stops looking. ~/git here is flat at depth 1 with a handful of grouped
// repositories at depth 2; 3 leaves headroom without turning the walk into a
// full-disk crawl.
const DefaultDepth = 3

// SkipDirs are never descended into. They are large, they never contain a
// checkout worth boarding, and node_modules alone would dominate the walk.
var SkipDirs = map[string]bool{
	"node_modules": true, ".venv": true, "venv": true, "target": true,
	"vendor": true, "dist": true, "build": true, ".cache": true,
	"graphify-out": true, "__pycache__": true, ".mypy_cache": true,
	".pytest_cache": true, ".tox": true, ".next": true, ".gradle": true,
}

// Repo is one checkout, as the board renders it.
type Repo struct {
	Path       string `json:"path"`        // absolute checkout root
	Name       string `json:"name"`        // display name; disambiguated on collision
	Group      string `json:"group"`       // path from the root down to the parent dir
	Root       string `json:"root"`        // the scan root it was found under
	IsWorktree bool   `json:"is_worktree"` // .git is a file → linked worktree
	Branch     string `json:"branch"`      // short branch name, or "" when detached
	HeadSHA    string `json:"head_sha"`    // resolved HEAD, full hex
	GitDir     string `json:"git_dir"`     // the real .git directory
	Out        string `json:"out"`         // absolute graphify output dir in effect
}

// ShortSHA is the seven-character form the board shows next to the branch.
func (r Repo) ShortSHA() string {
	if len(r.HeadSHA) > 7 {
		return r.HeadSHA[:7]
	}
	return r.HeadSHA
}

// Ref is what the Branch column renders: the branch when there is one, the
// short SHA when HEAD is detached.
func (r Repo) Ref() string {
	if r.Branch != "" {
		return r.Branch
	}
	if r.HeadSHA != "" {
		return "(" + r.ShortSHA() + ")"
	}
	return "—"
}

// Options configures a walk.
type Options struct {
	Roots []string // directories to scan; each is walked independently
	Depth int      // how deep below a root to look; <=0 means DefaultDepth
	// OutName is the directory graphify writes into, normally "graphify-out".
	// Honours $GRAPHIFY_OUT_NAME the way graphify itself does, so a board and
	// a terminal in the same environment agree on where a graph lives.
	OutName string
	// OutBase holds every repository's graph outside the checkouts, one
	// subdirectory each. Blank keeps them in-tree under OutName.
	OutBase string
	// Skip is consulted in addition to SkipDirs, so a caller can exclude a
	// noisy tree without editing the package.
	Skip map[string]bool
	// ShowHidden descends into dot-named directories below a root, boarding
	// the checkouts inside them. The default — false — is the quieter one: a
	// hidden directory is hidden because its owner did not want it in a
	// listing, and that includes this one.
	//
	// A root that is itself hidden (~/.graphify/repos) is unaffected either
	// way: naming a root is asking for what is in it.
	ShowHidden bool
}

// DefaultRoots are the three places checkouts live on a machine set up like
// this one: the working tree of record, the scratch area, and wherever
// `graphify clone` has put things.
func DefaultRoots() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return []string{"."}
	}
	return []string{
		filepath.Join(home, "git"),
		filepath.Join(home, "tmp"),
		filepath.Join(home, ".graphify", "repos"),
	}
}

// OutName resolves the output directory name graphify would use when nothing
// has been configured: $GRAPHIFY_OUT_NAME, else "graphify-out".
func OutName() string {
	if v := strings.TrimSpace(os.Getenv("GRAPHIFY_OUT_NAME")); v != "" {
		return v
	}
	return DefaultOutName
}

// DefaultOutName is graphify's own default output directory name.
const DefaultOutName = "graphify-out"

// OutSpec says where a repository's graphify knowledge lives. It is the single
// resolver both halves of ggraphify go through: the board reads state from the
// directory it names, and every job is launched with GRAPHIFY_OUT pointing at
// the same one. A board that read one directory while its jobs wrote another
// would report drift forever.
//
// The three fields are tried in order of how specific they are.
type OutSpec struct {
	// Name is the directory name inside each checkout — "graphify-out" unless
	// configured. Blank falls back to $GRAPHIFY_OUT_NAME, then the default.
	Name string
	// Base moves every graph out of the checkouts entirely, into one
	// directory holding a subdirectory per repository (see Slug). Blank keeps
	// graphs in-tree. A leading ~ is expanded.
	Base string
	// Repo is one repository's own override, absolute or relative to the
	// checkout. It outranks both of the above.
	Repo string
}

// OutName is the in-tree directory name this spec resolves to.
func (s OutSpec) OutName() string {
	if n := strings.TrimSpace(s.Name); n != "" {
		return n
	}
	return OutName()
}

// For resolves the output directory for one checkout.
//
// $GRAPHIFY_OUT is honoured only when nothing has been configured here, so a
// board and a terminal in the same environment agree by default while an
// explicit setting still wins over an inherited one.
func (s OutSpec) For(repo string) string {
	if v := strings.TrimSpace(s.Repo); v != "" {
		return underRepo(repo, v)
	}
	if v := strings.TrimSpace(s.Base); v != "" {
		return filepath.Join(Expand(v), Slug(repo))
	}
	if v := strings.TrimSpace(os.Getenv("GRAPHIFY_OUT")); v != "" {
		return underRepo(repo, v)
	}
	return filepath.Join(repo, s.OutName())
}

// Central reports whether graphs live outside the checkouts, which is the one
// thing the UI phrases differently.
func (s OutSpec) Central() bool {
	return strings.TrimSpace(s.Repo) == "" && strings.TrimSpace(s.Base) != ""
}

func underRepo(repo, out string) string {
	out = Expand(out)
	if filepath.IsAbs(out) {
		return filepath.Clean(out)
	}
	return filepath.Join(repo, out)
}

// Expand resolves a leading ~ against the home directory, because a path typed
// into a settings entry is typed the way it is typed in a shell.
func Expand(p string) string {
	p = strings.TrimSpace(p)
	if p != "~" && !strings.HasPrefix(p, "~/") {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	return filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(p, "~"), "/"))
}

// Slug is the subdirectory one checkout gets under a central Base: its name
// plus a short digest of its absolute path.
//
// The digest is what makes a shared base directory safe — ~/git/api and
// ~/tmp/api are two repositories and must not collide on one graph — and it is
// stable across runs, so a graph built yesterday is still found today.
func Slug(repo string) string {
	clean := filepath.Clean(repo)
	sum := sha256.Sum256([]byte(clean))
	name := filepath.Base(clean)
	if name == "." || name == string(filepath.Separator) || name == "" {
		name = "repo"
	}
	return name + "-" + hex.EncodeToString(sum[:4])
}

// Walk finds every checkout under opts.Roots. The result is sorted by name
// then path, which is a stable order regardless of readdir ordering.
//
// Roots that do not exist are skipped silently: the default root set names
// three plausible locations and a given machine will rarely have all three.
func Walk(opts Options) ([]Repo, error) {
	if opts.Depth <= 0 {
		opts.Depth = DefaultDepth
	}
	if opts.OutName == "" {
		opts.OutName = OutName()
	}
	spec := OutSpec{Name: opts.OutName, Base: opts.OutBase}
	seen := map[string]bool{}
	var out []Repo
	for _, root := range opts.Roots {
		abs, err := filepath.Abs(root)
		if err != nil {
			continue
		}
		if fi, err := os.Stat(abs); err != nil || !fi.IsDir() {
			continue
		}
		walkRoot(abs, abs, 0, opts, seen, &out)
	}
	resolve(out, spec)
	disambiguate(out)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].Path < out[j].Path
	})
	return out, nil
}

func walkRoot(root, dir string, depth int, opts Options, seen map[string]bool, out *[]Repo) {
	if depth > opts.Depth {
		return
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	// A directory holding .git is a checkout, and below the root a checkout is
	// a leaf: we do not descend into one looking for submodules, because a
	// submodule shares its parent's working tree and would double-count rows
	// against one graph.
	//
	// The root itself is the exception, and it has to be: a scratch directory
	// like ~/tmp can be a checkout of its own while holding a hundred
	// unrelated ones, and stopping at it would find exactly one repository on
	// this machine and call that the answer.
	for _, e := range ents {
		if e.Name() != ".git" {
			continue
		}
		if r, ok := repoAt(root, dir, e); ok && !seen[r.Path] {
			seen[r.Path] = true
			*out = append(*out, r)
		}
		if depth > 0 {
			return
		}
		break
	}
	for _, e := range ents {
		if !isDirEntry(e, dir) {
			continue
		}
		name := e.Name()
		if SkipDirs[name] || (opts.Skip != nil && opts.Skip[name]) {
			continue
		}
		// Dotted directories are hidden directories, and by default they stay
		// off the board. With ShowHidden on they are walked like any other,
		// which is how `.wt-tech17302` style worktree parks — a real pattern
		// on this machine — get boarded.
		if strings.HasPrefix(name, ".") && !opts.ShowHidden {
			continue
		}
		walkRoot(root, filepath.Join(dir, name), depth+1, opts, seen, out)
	}
}

// isDirEntry reports whether e is a directory, following a symlink when the
// entry is one. Symlinked checkouts are common enough to matter and a
// DirEntry reports the link itself, not its target.
func isDirEntry(e os.DirEntry, parent string) bool {
	if e.IsDir() {
		return true
	}
	if e.Type()&os.ModeSymlink == 0 {
		return false
	}
	fi, err := os.Stat(filepath.Join(parent, e.Name()))
	return err == nil && fi.IsDir()
}

func repoAt(root, dir string, git os.DirEntry) (Repo, bool) {
	r := Repo{Path: dir, Root: root, Name: filepath.Base(dir)}
	gitPath := filepath.Join(dir, ".git")
	switch {
	case git.IsDir():
		r.GitDir = gitPath
	default:
		// A linked worktree's .git is a file holding "gitdir: <path>". The
		// worktree is a first-class row: it has its own working tree and can
		// have its own graphify-out.
		b, err := os.ReadFile(gitPath)
		if err != nil {
			return Repo{}, false
		}
		line := strings.TrimSpace(string(b))
		if !strings.HasPrefix(line, "gitdir:") {
			return Repo{}, false
		}
		r.IsWorktree = true
		gd := strings.TrimSpace(strings.TrimPrefix(line, "gitdir:"))
		if !filepath.IsAbs(gd) {
			gd = filepath.Join(dir, gd)
		}
		r.GitDir = filepath.Clean(gd)
	}
	if rel, err := filepath.Rel(root, filepath.Dir(dir)); err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
		r.Group = rel
	}
	r.Branch, r.HeadSHA = head(r.GitDir)
	return r, true
}

// head reads the branch and resolved HEAD out of a git directory without
// running git. It understands the three shapes that occur in practice: a
// symbolic ref into refs/heads, a loose ref file, and packed-refs.
func head(gitDir string) (branch, sha string) {
	b, err := os.ReadFile(filepath.Join(gitDir, "HEAD"))
	if err != nil {
		return "", ""
	}
	line := strings.TrimSpace(string(b))
	if !strings.HasPrefix(line, "ref:") {
		// Detached HEAD: the file is the object id itself.
		return "", hexOnly(line)
	}
	ref := strings.TrimSpace(strings.TrimPrefix(line, "ref:"))
	branch = strings.TrimPrefix(ref, "refs/heads/")
	if v, err := os.ReadFile(filepath.Join(gitDir, filepath.FromSlash(ref))); err == nil {
		return branch, hexOnly(strings.TrimSpace(string(v)))
	}
	// A worktree's own refs live in the common dir, and a repacked repository
	// has no loose ref file at all. commonDir resolves the first; packed-refs
	// answers the second.
	if cd := commonDir(gitDir); cd != "" && cd != gitDir {
		if v, err := os.ReadFile(filepath.Join(cd, filepath.FromSlash(ref))); err == nil {
			return branch, hexOnly(strings.TrimSpace(string(v)))
		}
		if s := packed(cd, ref); s != "" {
			return branch, s
		}
	}
	return branch, packed(gitDir, ref)
}

// commonDir is the shared .git of a linked worktree, or "" when this is an
// ordinary repository.
func commonDir(gitDir string) string {
	b, err := os.ReadFile(filepath.Join(gitDir, "commondir"))
	if err != nil {
		return ""
	}
	p := strings.TrimSpace(string(b))
	if !filepath.IsAbs(p) {
		p = filepath.Join(gitDir, p)
	}
	return filepath.Clean(p)
}

func packed(gitDir, ref string) string {
	f, err := os.Open(filepath.Join(gitDir, "packed-refs"))
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if line == "" || line[0] == '#' || line[0] == '^' {
			continue
		}
		sp, name, ok := strings.Cut(line, " ")
		if ok && name == ref {
			return hexOnly(sp)
		}
	}
	return ""
}

// hexOnly guards against a HEAD file holding something that is not an object
// id — a half-written ref during a checkout, say. A non-hex value is reported
// as no SHA rather than rendered as garbage in the Branch column.
func hexOnly(s string) string {
	if len(s) < 7 {
		return ""
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return ""
		}
	}
	return s
}

// resolve fills in each repo's effective output directory from the spec, which
// is where the configured name, the configured central base and $GRAPHIFY_OUT
// are reconciled. The symlink is resolved so two worktrees sharing one output
// can be recognised as such.
//
// A per-repository override is not applied here: discover knows nothing about
// the sidecar, so the board layers that on afterwards (see board.derive).
func resolve(repos []Repo, spec OutSpec) {
	for i := range repos {
		out := spec.For(repos[i].Path)
		if real, err := filepath.EvalSymlinks(out); err == nil {
			out = real
		}
		repos[i].Out = out
	}
}

// disambiguate gives colliding names enough of their parent path to tell them
// apart. Two checkouts called "api" under different groups are common.
func disambiguate(repos []Repo) {
	count := map[string]int{}
	for _, r := range repos {
		count[r.Name]++
	}
	for i := range repos {
		if count[repos[i].Name] < 2 {
			continue
		}
		parent := filepath.Base(filepath.Dir(repos[i].Path))
		if parent != "" && parent != "." && parent != string(filepath.Separator) {
			repos[i].Name = parent + "/" + repos[i].Name
		}
	}
}

// Dirty reports whether the working tree has uncommitted changes. It is the
// one answer this package cannot compute cheaply, so it is deliberately not
// part of Walk: the board calls it for the selected row only, off the main
// thread, and caches the result.
//
// The implementation is intentionally an approximation — it compares the
// index's mtime against the newest tracked-looking file — because the exact
// answer needs git's index format and a full stat of the tree, and the column
// it feeds is advisory.
func Dirty(repo Repo) (bool, error) {
	idx, err := os.Stat(filepath.Join(repo.GitDir, "index"))
	if err != nil {
		return false, err
	}
	newest, err := newestMod(repo.Path, 2)
	if err != nil {
		return false, err
	}
	return newest.After(idx.ModTime().Add(time.Second)), nil
}

func newestMod(dir string, depth int) (time.Time, error) {
	var newest time.Time
	if depth < 0 {
		return newest, nil
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return newest, err
	}
	for _, e := range ents {
		name := e.Name()
		if name == ".git" || SkipDirs[name] || strings.HasPrefix(name, ".") {
			continue
		}
		if e.IsDir() {
			t, err := newestMod(filepath.Join(dir, name), depth-1)
			if err == nil && t.After(newest) {
				newest = t
			}
			continue
		}
		fi, err := e.Info()
		if err == nil && fi.ModTime().After(newest) {
			newest = fi.ModTime()
		}
	}
	return newest, nil
}

// Cache memoises a walk keyed on the roots' modification times. A rescan tick
// that finds no root has been touched returns the previous slice without
// touching the filesystem beyond one Stat per root.
//
// The key is only the roots' own mtimes, which a repository appearing two
// levels down does not move — so the cache also expires on TTL. That keeps
// the steady-state tick free without letting a new checkout stay invisible
// until the next explicit rescan.
type Cache struct {
	mu    sync.Mutex
	key   string
	at    time.Time
	repos []Repo
	// TTL bounds how stale the cached listing may be. Zero means DefaultTTL.
	TTL time.Duration
}

// DefaultTTL is how long a cached walk is trusted when no root has been
// touched. The walk itself is tens of milliseconds over ~130 repositories, so
// this is about sparing the refresh tick, not about the walk being expensive.
const DefaultTTL = 2 * time.Minute

// Walk returns the cached result when the roots are unchanged, and a fresh
// walk otherwise.
func (c *Cache) Walk(opts Options) ([]Repo, error) {
	k := rootKey(opts)
	ttl := c.TTL
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	c.mu.Lock()
	if c.key == k && c.repos != nil && time.Since(c.at) < ttl {
		repos := c.repos
		c.mu.Unlock()
		return repos, nil
	}
	c.mu.Unlock()

	repos, err := Walk(opts)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.key, c.repos, c.at = k, repos, time.Now()
	c.mu.Unlock()
	return repos, nil
}

// Invalidate forces the next Walk to re-read the filesystem. The board calls
// it when the root set changes in settings.
func (c *Cache) Invalidate() {
	c.mu.Lock()
	c.key, c.repos, c.at = "", nil, time.Time{}
	c.mu.Unlock()
}

func rootKey(opts Options) string {
	var sb strings.Builder
	sb.WriteString(OutSpec{Name: opts.OutName, Base: opts.OutBase}.OutName())
	sb.WriteByte('|')
	sb.WriteString(opts.OutBase)
	sb.WriteByte('|')
	if opts.ShowHidden {
		sb.WriteString("hidden|")
	}
	for _, r := range opts.Roots {
		sb.WriteString(r)
		sb.WriteByte(':')
		if fi, err := os.Stat(r); err == nil {
			sb.WriteString(fi.ModTime().UTC().Format(time.RFC3339Nano))
		}
		sb.WriteByte('|')
	}
	return sb.String()
}
