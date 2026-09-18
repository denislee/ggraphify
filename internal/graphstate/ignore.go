package graphstate

import (
	"bufio"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// gitIgnore is the subset of .gitignore the drift walk needs.
//
// graphify's extraction is .gitignore-aware — that is what `--no-gitignore`
// turns off — so a drift walk that is not reads every ignored file as a file
// graphify forgot to extract. On a checkout with a gitignored graft/ cache and
// a leftover in-tree graphify-out/ that was 1 568 phantom "added" files, which
// pins the row to `stale` forever and makes "bring this repository to healthy"
// an unreachable goal rather than a button.
//
// It is deliberately not a complete implementation of gitignore(5): no
// $GIT_DIR/info/exclude, no global core.excludesFile. Those cover cases this
// board does not see, and the walk still has its built-in skip list underneath.
// What is here is what actually appears in a repository's .gitignore files —
// anchored paths, directory-only patterns, basename patterns, negation, and the
// last-match-wins rule — in the root file and in the per-directory files below
// it (see ignoreTree).
type gitIgnore struct {
	rules []ignoreRule
}

type ignoreRule struct {
	pattern  string // normalized, slashes, no leading "/" and no trailing "/"
	negate   bool   // a "!" rule: a match re-admits the path
	dirOnly  bool   // pattern ended in "/"
	anchored bool   // pattern had an interior or leading "/": match the whole relative path
	// anyDepth is set for a pattern that began with "**/": git matches it in
	// every directory INCLUDING the root, so "**/coverage/" has to ignore
	// "coverage" as well as "a/b/coverage". Treating it as an ordinary
	// anchored pattern never matched at the root, and the whole of a
	// gitignored `.pub-cache/` — 23 000 files — read as drift graphify forgot.
	anyDepth bool
}

// loadGitIgnore reads repo/.gitignore. A missing or unreadable file yields an
// empty matcher, which ignores nothing — the same answer as no file at all.
func loadGitIgnore(repo string) *gitIgnore {
	f, err := os.Open(filepath.Join(repo, ".gitignore"))
	if err != nil {
		return &gitIgnore{}
	}
	defer f.Close()

	gi := &gitIgnore{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if r, ok := parseIgnoreLine(sc.Text()); ok {
			gi.rules = append(gi.rules, r)
		}
	}
	return gi
}

func parseIgnoreLine(line string) (ignoreRule, bool) {
	// A trailing space is only significant when escaped; nothing else about
	// escaping matters for the patterns that appear in practice.
	s := strings.TrimRight(line, " \t")
	if s == "" || strings.HasPrefix(s, "#") {
		return ignoreRule{}, false
	}
	var r ignoreRule
	if strings.HasPrefix(s, "!") {
		r.negate = true
		s = s[1:]
	}
	if strings.HasSuffix(s, "/") {
		r.dirOnly = true
		s = strings.TrimSuffix(s, "/")
	}
	if strings.HasPrefix(s, "**/") {
		// git: "**/foo" is "foo in any directory, the root included". Strip
		// the prefix and let anyDepth carry that meaning; what is left is
		// matched against every suffix of the path.
		r.anyDepth = true
		s = strings.TrimPrefix(s, "**/")
		for strings.HasPrefix(s, "**/") {
			s = strings.TrimPrefix(s, "**/")
		}
	}
	if strings.HasPrefix(s, "/") {
		r.anchored = true
		s = strings.TrimPrefix(s, "/")
	} else if strings.Contains(s, "/") {
		// "a/b" anchors at the root the same way "/a/b" does; only a pattern
		// with no slash at all matches by basename at any depth.
		r.anchored = true
	}
	if s == "" {
		return ignoreRule{}, false
	}
	r.pattern = s
	return r, true
}

// Match reports whether git would ignore rel — a slash-separated path relative
// to the repository root. Later rules win, which is how a "!" line re-admits
// something an earlier line ignored.
func (g *gitIgnore) Match(rel string, isDir bool) bool {
	if g == nil || len(g.rules) == 0 {
		return false
	}
	ignored := false
	for _, r := range g.rules {
		if r.dirOnly && !isDir {
			continue
		}
		if r.match(rel) {
			ignored = !r.negate
		}
	}
	return ignored
}

func (r ignoreRule) match(rel string) bool {
	if r.anyDepth {
		// Every suffix of the path, so "**/a/b" matches "a/b" and "x/a/b",
		// and the directory-swallowing prefix rule applies at each depth.
		segs := strings.Split(rel, "/")
		for i := range segs {
			rest := strings.Join(segs[i:], "/")
			if matchPath(r.pattern, rest) || strings.HasPrefix(rest, r.pattern+"/") {
				return true
			}
		}
		return false
	}
	if r.anchored {
		if matchPath(r.pattern, rel) {
			return true
		}
		// An ignored directory ignores everything under it, and the walk asks
		// about files too: "/graft/" has to swallow "graft/cards/x.md".
		return strings.HasPrefix(rel, r.pattern+"/")
	}
	// Unanchored: the pattern applies to every path component, so both
	// "node_modules" and "a/node_modules/b.js" are matched by "node_modules".
	for _, seg := range strings.Split(rel, "/") {
		if matchPath(r.pattern, seg) {
			return true
		}
	}
	return false
}

// matchPath is path.Match with "**" degraded to "*". path.Match's "*" does not
// cross a "/", which is the right behaviour for every other pattern; treating
// "**" as an ordinary "*" over-matches slightly less often than ignoring the
// rule entirely, and both are safe here — a wrong answer costs a phantom drift
// count, not a wrong command.
func matchPath(pattern, name string) bool {
	pattern = strings.ReplaceAll(pattern, "**", "*")
	ok, err := path.Match(pattern, name)
	return err == nil && ok
}

// isNestedCheckout reports whether dir is a git checkout in its own right — it
// holds a .git directory (a clone) or a .git file (a linked worktree).
//
// graphify does not descend into one and neither does the board's own scan: a
// worktree under .claude/worktrees/ is a whole second copy of the repository,
// and counting its files as drift against this repository's manifest is how a
// row reports +3 189 added files the moment an agent session leaves one behind.
func isNestedCheckout(dir string) bool {
	_, err := os.Lstat(filepath.Join(dir, ".git"))
	return err == nil
}

// ignoreTree is the repository's .gitignore files, root and nested, resolved
// lazily as the walk descends.
//
// Per-directory files are not a nicety: a monorepo ignores its generated
// artefacts next to the code that generates them, and a board that reads only
// the root file counts every one of those as a file graphify forgot to extract.
// On one checkout that was `godaddy/scripts/import.json`, `.beads/backup/…` and
// a permanent `+3` that no `update` could ever clear.
//
// git evaluates the files shallowest-first and lets a deeper one override, which
// is what the loop below does. Each file's patterns are relative to its own
// directory, so matching strips that prefix before asking.
type ignoreTree struct {
	repo string
	dirs map[string]*gitIgnore // keyed by directory, relative to repo ("" is the root)
}

func newIgnoreTree(repo string) *ignoreTree {
	return &ignoreTree{repo: repo, dirs: map[string]*gitIgnore{}}
}

// at returns the .gitignore for one directory, reading it at most once.
func (t *ignoreTree) at(dir string) *gitIgnore {
	if gi, ok := t.dirs[dir]; ok {
		return gi
	}
	base := t.repo
	if dir != "" {
		base = filepath.Join(t.repo, filepath.FromSlash(dir))
	}
	gi := loadGitIgnore(base)
	t.dirs[dir] = gi
	return gi
}

// Match reports whether git would ignore rel, a slash-separated path relative
// to the repository root.
func (t *ignoreTree) Match(rel string, isDir bool) bool {
	if t == nil {
		return false
	}
	ignored := false
	// "" (the root), then every ancestor directory of rel, shallowest first.
	if v, ok := t.at("").verdict(rel, isDir); ok {
		ignored = v
	}
	segs := strings.Split(rel, "/")
	for i := 1; i < len(segs); i++ {
		dir := strings.Join(segs[:i], "/")
		sub := strings.Join(segs[i:], "/")
		if v, ok := t.at(dir).verdict(sub, isDir); ok {
			ignored = v
		}
	}
	return ignored
}

// verdict is Match plus "did any rule have an opinion", which is what lets a
// deeper .gitignore override a shallower one instead of merely adding to it.
func (g *gitIgnore) verdict(rel string, isDir bool) (ignored, matched bool) {
	if g == nil || len(g.rules) == 0 {
		return false, false
	}
	for _, r := range g.rules {
		if r.dirOnly && !isDir {
			continue
		}
		if r.match(rel) {
			ignored, matched = !r.negate, true
		}
	}
	return ignored, matched
}
