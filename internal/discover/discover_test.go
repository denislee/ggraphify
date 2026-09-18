package discover

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// mkrepo creates a checkout with a HEAD pointing at branch and a loose ref.
func mkrepo(t *testing.T, dir, branch, sha string) {
	t.Helper()
	git := filepath.Join(dir, ".git")
	if err := os.MkdirAll(filepath.Join(git, "refs", "heads"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(git, "HEAD"), []byte("ref: refs/heads/"+branch+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ref := filepath.Join(git, "refs", "heads", filepath.FromSlash(branch))
	if err := os.MkdirAll(filepath.Dir(ref), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ref, []byte(sha+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func names(repos []Repo) []string {
	out := make([]string, len(repos))
	for i, r := range repos {
		out[i] = r.Name
	}
	return out
}

func has(repos []Repo, name string) *Repo {
	for i := range repos {
		if repos[i].Name == name {
			return &repos[i]
		}
	}
	return nil
}

func TestWalkFindsCheckoutsAndReadsHead(t *testing.T) {
	root := t.TempDir()
	mkrepo(t, mkdir(t, root, "alpha"), "main", "1111111111111111111111111111111111111111")
	mkrepo(t, mkdir(t, root, "group/beta"), "feature/x", "2222222222222222222222222222222222222222")

	repos, err := Walk(Options{Roots: []string{root}})
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 2 {
		t.Fatalf("found %v, want alpha and beta", names(repos))
	}
	a := has(repos, "alpha")
	if a == nil || a.Branch != "main" || a.ShortSHA() != "1111111" {
		t.Fatalf("alpha = %+v", a)
	}
	b := has(repos, "beta")
	if b == nil || b.Branch != "feature/x" {
		t.Fatalf("beta = %+v", b)
	}
	if b.Group != "group" {
		t.Errorf("beta.Group = %q, want group", b.Group)
	}
}

// A checkout below the root is a leaf: descending into one would count
// submodules as repositories of their own against one graph.
func TestNestedCheckoutIsNotDescendedInto(t *testing.T) {
	root := t.TempDir()
	outer := mkdir(t, root, "outer")
	mkrepo(t, outer, "main", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	inner := mkdir(t, outer, "vendored")
	mkrepo(t, inner, "main", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")

	repos, _ := Walk(Options{Roots: []string{root}})
	if len(repos) != 1 || repos[0].Name != "outer" {
		t.Fatalf("found %v, want just outer", names(repos))
	}
}

// The root itself is the exception, and it has to be: ~/tmp on this machine is
// a checkout holding a hundred unrelated ones, and stopping at it would find
// exactly one repository and call that the answer.
func TestRootItselfIsDescendedInto(t *testing.T) {
	root := t.TempDir()
	mkrepo(t, root, "master", "cccccccccccccccccccccccccccccccccccccccc")
	mkrepo(t, mkdir(t, root, "child"), "main", "dddddddddddddddddddddddddddddddddddddddd")

	repos, _ := Walk(Options{Roots: []string{root}})
	if len(repos) != 2 {
		t.Fatalf("found %v, want the root and its child", names(repos))
	}
}

// A linked worktree's .git is a file, and the worktree is a first-class row:
// it has its own working tree and can have its own graphify-out.
func TestLinkedWorktreeIsAFirstClassRow(t *testing.T) {
	root := t.TempDir()
	main := mkdir(t, root, "repo")
	mkrepo(t, main, "main", "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee")

	// The worktree's own git dir, with a commondir pointing back at the main
	// one and its HEAD on a different branch.
	wtGit := filepath.Join(main, ".git", "worktrees", "wt")
	os.MkdirAll(wtGit, 0o755)
	os.WriteFile(filepath.Join(wtGit, "HEAD"), []byte("ref: refs/heads/side\n"), 0o644)
	os.WriteFile(filepath.Join(wtGit, "commondir"), []byte("../..\n"), 0o644)
	os.WriteFile(filepath.Join(main, ".git", "refs", "heads", "side"),
		[]byte("ffffffffffffffffffffffffffffffffffffffff\n"), 0o644)

	wt := mkdir(t, root, ".wt-tech17302")
	os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: "+wtGit+"\n"), 0o644)

	// A `.wt-` park is a hidden directory, so the walk only reaches it with
	// ShowHidden on; see TestHiddenDirectoriesAreOffByDefault.
	repos, _ := Walk(Options{Roots: []string{root}, ShowHidden: true})
	r := has(repos, ".wt-tech17302")
	if r == nil {
		t.Fatalf("the worktree was not found; got %v", names(repos))
	}
	if !r.IsWorktree {
		t.Error("the worktree was not marked as one")
	}
	if r.Branch != "side" {
		t.Errorf("Branch = %q, want side", r.Branch)
	}
	// Resolved through commondir, which is where a worktree's refs actually
	// live.
	if r.ShortSHA() != "fffffff" {
		t.Errorf("HeadSHA = %q — commondir was not followed", r.HeadSHA)
	}
}

func TestPackedRefsHead(t *testing.T) {
	root := t.TempDir()
	dir := mkdir(t, root, "packed")
	git := filepath.Join(dir, ".git")
	os.MkdirAll(git, 0o755)
	os.WriteFile(filepath.Join(git, "HEAD"), []byte("ref: refs/heads/main\n"), 0o644)
	os.WriteFile(filepath.Join(git, "packed-refs"), []byte(
		"# pack-refs with: peeled fully-peeled sorted \n"+
			"1234567890123456789012345678901234567890 refs/heads/main\n"+
			"^abcdef1234567890123456789012345678901234\n"), 0o644)

	repos, _ := Walk(Options{Roots: []string{root}})
	r := has(repos, "packed")
	if r == nil || r.ShortSHA() != "1234567" {
		t.Fatalf("packed-refs was not read: %+v", r)
	}
}

func TestDetachedHead(t *testing.T) {
	root := t.TempDir()
	dir := mkdir(t, root, "detached")
	git := filepath.Join(dir, ".git")
	os.MkdirAll(git, 0o755)
	os.WriteFile(filepath.Join(git, "HEAD"), []byte("9999999999999999999999999999999999999999\n"), 0o644)

	repos, _ := Walk(Options{Roots: []string{root}})
	r := has(repos, "detached")
	if r == nil || r.Branch != "" {
		t.Fatalf("detached = %+v", r)
	}
	if r.Ref() != "(9999999)" {
		t.Errorf("Ref() = %q, want the short SHA in parentheses", r.Ref())
	}
}

// A half-written HEAD during a checkout must be reported as "no SHA" rather
// than rendered as garbage in the Branch column.
func TestNonHexHeadIsRejected(t *testing.T) {
	root := t.TempDir()
	dir := mkdir(t, root, "weird")
	git := filepath.Join(dir, ".git")
	os.MkdirAll(git, 0o755)
	os.WriteFile(filepath.Join(git, "HEAD"), []byte("this is not an object id\n"), 0o644)

	repos, _ := Walk(Options{Roots: []string{root}})
	r := has(repos, "weird")
	if r == nil || r.HeadSHA != "" {
		t.Fatalf("weird = %+v, want an empty SHA", r)
	}
}

func TestSkipsHeavyDirectories(t *testing.T) {
	root := t.TempDir()
	mkrepo(t, mkdir(t, root, "node_modules/pkg"), "main", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	mkrepo(t, mkdir(t, root, "real"), "main", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")

	repos, _ := Walk(Options{Roots: []string{root}})
	if len(repos) != 1 || repos[0].Name != "real" {
		t.Fatalf("found %v, want just real", names(repos))
	}
}

// Two checkouts with the same base name get enough of their parent path to be
// told apart, because the board renders names and two identical rows are
// worse than useless.
func TestCollidingNamesAreDisambiguated(t *testing.T) {
	root := t.TempDir()
	mkrepo(t, mkdir(t, root, "a/api"), "main", "1111111111111111111111111111111111111111")
	mkrepo(t, mkdir(t, root, "b/api"), "main", "2222222222222222222222222222222222222222")

	repos, _ := Walk(Options{Roots: []string{root}})
	got := names(repos)
	if len(got) != 2 || got[0] == got[1] {
		t.Fatalf("names = %v, want two distinct", got)
	}
	for _, n := range got {
		if n == "api" {
			t.Fatalf("names = %v — a colliding name was left ambiguous", got)
		}
	}
}

func TestMissingRootIsSkippedNotAnError(t *testing.T) {
	root := t.TempDir()
	mkrepo(t, mkdir(t, root, "real"), "main", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")

	// The default root set names three plausible locations and a given machine
	// will rarely have all three.
	repos, err := Walk(Options{Roots: []string{root, filepath.Join(root, "nope")}})
	if err != nil {
		t.Fatalf("a missing root must not be an error: %v", err)
	}
	if len(repos) != 1 {
		t.Fatalf("found %v", names(repos))
	}
}

func TestDepthCap(t *testing.T) {
	root := t.TempDir()
	mkrepo(t, mkdir(t, root, "a/b/c/d/deep"), "main", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")

	if repos, _ := Walk(Options{Roots: []string{root}, Depth: 2}); len(repos) != 0 {
		t.Fatalf("depth 2 found %v, want nothing", names(repos))
	}
	if repos, _ := Walk(Options{Roots: []string{root}, Depth: 6}); len(repos) != 1 {
		t.Fatalf("depth 6 found %v, want deep", names(repos))
	}
}

// GRAPHIFY_OUT redirects the output directory, and the board has to agree with
// what a terminal in the same environment would do.
func TestOutHonoursGraphifyOutEnv(t *testing.T) {
	root := t.TempDir()
	dir := mkdir(t, root, "repo")
	mkrepo(t, dir, "main", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")

	t.Setenv("GRAPHIFY_OUT", "")
	repos, _ := Walk(Options{Roots: []string{root}})
	if got := repos[0].Out; got != filepath.Join(dir, "graphify-out") {
		t.Fatalf("Out = %q", got)
	}

	t.Setenv("GRAPHIFY_OUT", "somewhere-else")
	repos, _ = Walk(Options{Roots: []string{root}})
	if got := repos[0].Out; got != filepath.Join(dir, "somewhere-else") {
		t.Fatalf("relative GRAPHIFY_OUT: Out = %q", got)
	}

	abs := t.TempDir()
	t.Setenv("GRAPHIFY_OUT", abs)
	repos, _ = Walk(Options{Roots: []string{root}})
	if got := repos[0].Out; got != abs {
		t.Fatalf("absolute GRAPHIFY_OUT: Out = %q, want %q", got, abs)
	}
}

func TestOutNameHonoursEnv(t *testing.T) {
	t.Setenv("GRAPHIFY_OUT_NAME", "")
	if OutName() != "graphify-out" {
		t.Fatal("default out name is wrong")
	}
	t.Setenv("GRAPHIFY_OUT_NAME", "gout")
	if OutName() != "gout" {
		t.Fatal("GRAPHIFY_OUT_NAME was ignored")
	}
}

// The cache must return the same slice when nothing moved, and re-walk after
// Invalidate — which is what the explicit rescan button relies on.
func TestCacheHitAndInvalidate(t *testing.T) {
	root := t.TempDir()
	mkrepo(t, mkdir(t, root, "one"), "main", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")

	var c Cache
	opts := Options{Roots: []string{root}}
	first, _ := c.Walk(opts)
	mkrepo(t, mkdir(t, root, "two"), "main", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")

	// Creating "two" moved the root's mtime, so this is a legitimate miss; the
	// point being asserted is that Invalidate always forces a fresh walk.
	c.Invalidate()
	second, _ := c.Walk(opts)
	if len(second) <= len(first) {
		t.Fatalf("after Invalidate: %v, want more than %v", names(second), names(first))
	}
	third, _ := c.Walk(opts)
	if len(third) != len(second) {
		t.Fatalf("a cache hit changed the answer: %v vs %v", names(third), names(second))
	}
}

func mkdir(t *testing.T, root, rel string) string {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// OutSpec is the one resolver the board and the jobs share, so the precedence
// between a per-repository override, a central directory, a configured name
// and the inherited environment is worth pinning down exactly.
func TestOutSpecPrecedence(t *testing.T) {
	t.Setenv("GRAPHIFY_OUT", "")
	t.Setenv("GRAPHIFY_OUT_NAME", "")
	const repo = "/home/u/git/api"

	cases := []struct {
		name string
		spec OutSpec
		want string
	}{
		{"default", OutSpec{}, "/home/u/git/api/graphify-out"},
		{"name", OutSpec{Name: ".graph"}, "/home/u/git/api/.graph"},
		{"base", OutSpec{Base: "/var/graphs"}, "/var/graphs/" + Slug(repo)},
		{"base outranks name", OutSpec{Name: ".graph", Base: "/var/graphs"}, "/var/graphs/" + Slug(repo)},
		{"repo absolute", OutSpec{Name: ".graph", Base: "/var/graphs", Repo: "/tmp/g"}, "/tmp/g"},
		{"repo relative", OutSpec{Repo: "out/graph"}, "/home/u/git/api/out/graph"},
	}
	for _, c := range cases {
		if got := c.spec.For(repo); got != c.want {
			t.Errorf("%s: For(%q) = %q, want %q", c.name, repo, got, c.want)
		}
	}
}

// The environment is the fallback, not the override: a configured location has
// to win over an inherited one, or a setting typed in the board would do
// nothing on a machine whose shell profile exports GRAPHIFY_OUT.
func TestOutSpecEnvIsAFallback(t *testing.T) {
	t.Setenv("GRAPHIFY_OUT", "/env/out")
	t.Setenv("GRAPHIFY_OUT_NAME", "env-out")
	const repo = "/home/u/git/api"

	if got, want := (OutSpec{}).For(repo), "/env/out"; got != want {
		t.Errorf("unconfigured: %q, want %q", got, want)
	}
	if got, want := (OutSpec{Base: "/var/graphs"}).For(repo), "/var/graphs/"+Slug(repo); got != want {
		t.Errorf("configured base lost to the environment: %q, want %q", got, want)
	}
	// With GRAPHIFY_OUT unset, the name still comes from the environment.
	t.Setenv("GRAPHIFY_OUT", "")
	if got, want := (OutSpec{}).For(repo), "/home/u/git/api/env-out"; got != want {
		t.Errorf("$GRAPHIFY_OUT_NAME ignored: %q, want %q", got, want)
	}
}

// Two checkouts with the same base name must not share one graph under a
// central directory, and a slug must not move between runs.
func TestSlugDistinguishesSameName(t *testing.T) {
	a, b := Slug("/home/u/git/api"), Slug("/home/u/tmp/api")
	if a == b {
		t.Fatalf("two repositories collided on %q", a)
	}
	if !strings.HasPrefix(a, "api-") {
		t.Errorf("slug %q does not name the repository", a)
	}
	if a != Slug("/home/u/git/api/") {
		t.Error("slug is not stable across an equivalent path")
	}
}

// Walk has to hand back the configured location, because everything the board
// derives — state, drift, size — is read from the directory it names.
func TestWalkHonoursOutBase(t *testing.T) {
	t.Setenv("GRAPHIFY_OUT", "")
	t.Setenv("GRAPHIFY_OUT_NAME", "")
	root := t.TempDir()
	repo := mkdir(t, root, "one")
	mkrepo(t, repo, "main", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	base := t.TempDir()

	repos, err := Walk(Options{Roots: []string{root}, OutBase: base})
	if err != nil || len(repos) != 1 {
		t.Fatalf("Walk: %v, %d repos", err, len(repos))
	}
	want := filepath.Join(base, Slug(repo))
	if repos[0].Out != want {
		t.Errorf("Out = %q, want %q", repos[0].Out, want)
	}

	named, err := Walk(Options{Roots: []string{root}, OutName: ".graph"})
	if err != nil || len(named) != 1 {
		t.Fatalf("Walk: %v, %d repos", err, len(named))
	}
	if want := filepath.Join(repo, ".graph"); named[0].Out != want {
		t.Errorf("Out = %q, want %q", named[0].Out, want)
	}
}

// The discovery cache keys on the location too: changing where graphs live
// while a cached walk is still warm must not keep the old directories.
func TestCacheKeyIncludesOutLocation(t *testing.T) {
	root := t.TempDir()
	mkrepo(t, mkdir(t, root, "one"), "main", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")

	var c Cache
	first, _ := c.Walk(Options{Roots: []string{root}})
	second, _ := c.Walk(Options{Roots: []string{root}, OutBase: "/var/graphs"})
	if len(first) != 1 || len(second) != 1 {
		t.Fatalf("walks returned %d and %d rows", len(first), len(second))
	}
	if first[0].Out == second[0].Out {
		t.Errorf("the cache served %q for a different output location", second[0].Out)
	}
}

// Hidden directories are off by default and on with ShowHidden. A root that is
// itself hidden is scanned either way: naming it is asking for what is in it.
func TestHiddenDirectoriesAreOffByDefault(t *testing.T) {
	root := t.TempDir()
	mkrepo(t, mkdir(t, root, "visible"), "main", "1111111111111111111111111111111111111111")
	mkrepo(t, mkdir(t, root, ".hidden"), "main", "2222222222222222222222222222222222222222")
	buried := mkdir(t, filepath.Join(root, ".park"), "inside")
	mkrepo(t, buried, "main", "3333333333333333333333333333333333333333")

	repos, _ := Walk(Options{Roots: []string{root}})
	if has(repos, ".hidden") != nil || has(repos, "inside") != nil {
		t.Errorf("hidden checkouts were boarded by default; got %v", names(repos))
	}
	if has(repos, "visible") == nil {
		t.Errorf("the visible checkout was lost; got %v", names(repos))
	}

	repos, _ = Walk(Options{Roots: []string{root}, ShowHidden: true})
	for _, want := range []string{"visible", ".hidden", "inside"} {
		if has(repos, want) == nil {
			t.Errorf("ShowHidden did not board %s; got %v", want, names(repos))
		}
	}

	// The hidden root itself, named directly.
	repos, _ = Walk(Options{Roots: []string{filepath.Join(root, ".park")}})
	if has(repos, "inside") == nil {
		t.Errorf("a hidden root was not scanned; got %v", names(repos))
	}
}

// A commit made in a terminal moves .git/HEAD and nothing else — not the scan
// root's mtime, which is this cache's key. The listing may still be served
// from the memo, but the commit it reports must not be: the board's branch
// cell, its short SHA and its behind-HEAD verdict all read it.
func TestCacheRefreshesHeadOnAHit(t *testing.T) {
	root := t.TempDir()
	one, two := filepath.Join(root, "one"), filepath.Join(root, "two")
	for _, d := range []string{one, two} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	mkrepo(t, one, "main", "1111111111111111111111111111111111111111")
	mkrepo(t, two, "main", "9999999999999999999999999999999999999999")

	var c Cache
	first, err := c.Walk(Options{Roots: []string{root}})
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 2 {
		t.Fatalf("first walk = %v, want both checkouts", names(first))
	}

	// The commit under test, plus a second checkout stripped of its .git. A
	// fresh walk would list only one repository; a served memo lists two —
	// which is how this asserts the refresh happened on the CACHED path and
	// not because the walk simply ran again.
	if err := os.WriteFile(filepath.Join(one, ".git", "refs", "heads", "main"),
		[]byte("2222222222222222222222222222222222222222\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(two, ".git")); err != nil {
		t.Fatal(err)
	}

	got, err := c.Walk(Options{Roots: []string{root}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %v — the listing should still be the cached one", names(got))
	}
	r := has(got, "one")
	if r == nil || r.HeadSHA != "2222222222222222222222222222222222222222" {
		t.Errorf("got %+v, want the commit that just landed", r)
	}
	// The cached slice itself must not have been written through: it has
	// already been handed to a caller that may be reading it right now.
	if first[0].HeadSHA == "2222222222222222222222222222222222222222" {
		t.Error("refreshHeads wrote through the slice it was given")
	}
}

// A checkout whose HEAD cannot be read keeps what it had. Blanking the Branch
// column for a repository being deleted underneath the board would report it
// as detached, which it is not.
func TestCacheKeepsTheLastHeadWhenItCannotBeRead(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "one")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	mkrepo(t, dir, "main", "1111111111111111111111111111111111111111")

	var c Cache
	if _, err := c.Walk(Options{Roots: []string{root}}); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(dir, ".git")); err != nil {
		t.Fatal(err)
	}
	got, err := c.Walk(Options{Roots: []string{root}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Branch != "main" ||
		got[0].HeadSHA != "1111111111111111111111111111111111111111" {
		t.Errorf("got %+v, want the previous branch and commit kept", got)
	}
}
