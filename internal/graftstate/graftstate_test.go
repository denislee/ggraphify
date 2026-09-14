package graftstate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// repo builds a checkout with a graft index in it. body is the source file's
// contents, and the fingerprint is written to match unless a test moves one of
// them afterwards.
type fixture struct {
	root string
	dir  string
}

func newFixture(t *testing.T, deep bool, files map[string]string) fixture {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, DirName)
	mkdir(t, filepath.Join(dir, graphDir))
	mkdir(t, filepath.Join(dir, cacheDir))

	write(t, filepath.Join(dir, indexFile), "# graft — repo map\n")
	write(t, filepath.Join(dir, graphDir, wiringFile), `{
  "meta": {"version": 1, "nodeCount": 951, "edgeCount": 2392, "languages": ["go"]},
  "nodes": [{"id": "a.go"}, {"id": "a.go#F"}],
  "edges": [{"from": "a.go#F", "to": "a.go"}]
}`)
	if deep {
		write(t, filepath.Join(dir, manifestFile), `{"version":1,"files":[],"nodes":[]}`)
	}

	prints := map[string][]any{}
	for rel, body := range files {
		abs := filepath.Join(root, filepath.FromSlash(rel))
		mkdir(t, filepath.Dir(abs))
		write(t, abs, body)
		fi, err := os.Stat(abs)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256([]byte(body))
		prints[rel] = []any{
			fi.Size(),
			float64(fi.ModTime().UnixNano()) / 1e6,
			hex.EncodeToString(sum[:]),
		}
	}
	fp, err := json.Marshal(map[string]any{"version": 1, "extractor": "test", "files": prints})
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, cacheDir, fingerprintPr+"test.json"), string(fp))

	return fixture{root: root, dir: dir}
}

func mkdir(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
}

func write(t *testing.T, p, body string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestNoGraftDirectoryIsNoneNotAnError(t *testing.T) {
	i, err := Read(Options{Repo: t.TempDir()})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if i.State != StateNone {
		t.Errorf("state = %v, want none", i.State)
	}
	if i.NeedsBuild() != true {
		t.Error("a repository with no index needs a build")
	}
}

func TestNoRepoIsAnError(t *testing.T) {
	if _, err := Read(Options{}); err == nil {
		t.Error("Read with no repo should be an error")
	}
}

// The counters come out of wiring.json's meta and nothing else is parsed.
func TestFreshDeepIndex(t *testing.T) {
	f := newFixture(t, true, map[string]string{"a.go": "package a\n"})
	i, err := Read(Options{Repo: f.root})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if i.State != StateFresh {
		t.Errorf("state = %v (%s), want fresh", i.State, i.Err)
	}
	if i.Nodes != 951 || i.Edges != 2392 {
		t.Errorf("counters = %dn/%de, want 951n/2392e", i.Nodes, i.Edges)
	}
	if len(i.Languages) != 1 || i.Languages[0] != "go" {
		t.Errorf("languages = %v, want [go]", i.Languages)
	}
	if !i.Deep || !i.HasIndex {
		t.Errorf("deep = %v, hasIndex = %v, want both true", i.Deep, i.HasIndex)
	}
	if i.Files != 1 {
		t.Errorf("files = %d, want 1", i.Files)
	}
	if i.Drift() {
		t.Errorf("unexpected drift %q", i.DriftString())
	}
	if i.NeedsBuild() {
		t.Error("a fresh index needs no build")
	}
}

// Without manifest.json the index is wiring-only. That is graft's free
// default, not a defect — but it is not the same thing as fresh either.
func TestWiringOnlyIsRaw(t *testing.T) {
	f := newFixture(t, false, map[string]string{"a.go": "package a\n"})
	i, _ := Read(Options{Repo: f.root})
	if i.State != StateRaw {
		t.Errorf("state = %v, want wiring", i.State)
	}
	if i.Deep {
		t.Error("deep should be false with no manifest.json")
	}
	if i.NeedsBuild() {
		t.Error("a wiring-only index is not work `graft build` can clear")
	}
}

func TestMissingWiringIsBroken(t *testing.T) {
	f := newFixture(t, true, map[string]string{"a.go": "package a\n"})
	if err := os.Remove(filepath.Join(f.dir, graphDir, wiringFile)); err != nil {
		t.Fatal(err)
	}
	i, _ := Read(Options{Repo: f.root})
	if i.State != StateBroken {
		t.Errorf("state = %v, want broken", i.State)
	}
	if i.Err == "" {
		t.Error("a broken index must say why")
	}
}

func TestChangedBytesAreDrift(t *testing.T) {
	f := newFixture(t, true, map[string]string{"a.go": "package a\n"})
	write(t, filepath.Join(f.root, "a.go"), "package a\n\nfunc F() {}\n")
	touch(t, filepath.Join(f.root, "a.go"))

	i, _ := Read(Options{Repo: f.root})
	if i.State != StateStale {
		t.Fatalf("state = %v, want stale", i.State)
	}
	if i.DriftChanged != 1 || i.DriftRemoved != 0 {
		t.Errorf("drift = ~%d −%d, want ~1 −0", i.DriftChanged, i.DriftRemoved)
	}
	if i.DriftString() != "~1" {
		t.Errorf("DriftString = %q, want %q", i.DriftString(), "~1")
	}
	if !i.NeedsBuild() {
		t.Error("drift is work `graft build` clears")
	}
}

func TestRemovedFileIsDrift(t *testing.T) {
	f := newFixture(t, true, map[string]string{"a.go": "package a\n"})
	if err := os.Remove(filepath.Join(f.root, "a.go")); err != nil {
		t.Fatal(err)
	}
	i, _ := Read(Options{Repo: f.root})
	if i.DriftRemoved != 1 {
		t.Errorf("removed = %d, want 1", i.DriftRemoved)
	}
	if i.State != StateStale {
		t.Errorf("state = %v, want stale", i.State)
	}
}

// The whole reason the drift pass re-hashes instead of trusting a stat: a
// `touch`, or a branch switch that restores identical bytes, must not read as
// work to do. This is graft's own probe rule.
func TestTouchedButIdenticalIsNotDrift(t *testing.T) {
	f := newFixture(t, true, map[string]string{"a.go": "package a\n"})
	touch(t, filepath.Join(f.root, "a.go"))

	i, _ := Read(Options{Repo: f.root})
	if i.Drift() {
		t.Errorf("a touched but unchanged file drifted: %q", i.DriftString())
	}
	if i.State != StateFresh {
		t.Errorf("state = %v, want fresh", i.State)
	}
}

func TestSkipDriftOmitsTheStatPass(t *testing.T) {
	f := newFixture(t, true, map[string]string{"a.go": "package a\n"})
	if err := os.Remove(filepath.Join(f.root, "a.go")); err != nil {
		t.Fatal(err)
	}
	i, _ := Read(Options{Repo: f.root, SkipDrift: true})
	if i.Drift() {
		t.Error("SkipDrift still walked the files")
	}
	if i.Files != 1 {
		t.Errorf("files = %d, want the fingerprint's roster even with SkipDrift", i.Files)
	}
}

// A directory whose fingerprint is gone cannot be checked for drift, and must
// not therefore claim to be in step with a tree it has not looked at. It is
// still an index, so it is not broken — it is just unverified.
func TestNoFingerprintIsNotDrift(t *testing.T) {
	f := newFixture(t, true, map[string]string{"a.go": "package a\n"})
	if err := os.Remove(filepath.Join(f.dir, cacheDir, fingerprintPr+"test.json")); err != nil {
		t.Fatal(err)
	}
	i, _ := Read(Options{Repo: f.root})
	if i.Drift() {
		t.Error("no fingerprint should mean no drift claim")
	}
	if i.Files != 0 {
		t.Errorf("files = %d, want 0", i.Files)
	}
}

// The cache must notice a rebuild. The key is the graft directory's own
// identity, so writing a new wiring.json invalidates it even inside the TTL.
func TestCacheSeesARebuild(t *testing.T) {
	f := newFixture(t, false, map[string]string{"a.go": "package a\n"})
	var c Cache
	first, _ := c.Read(Options{Repo: f.root})
	if first.State != StateRaw {
		t.Fatalf("state = %v, want wiring", first.State)
	}

	write(t, filepath.Join(f.dir, manifestFile), `{"version":1}`)
	touch(t, filepath.Join(f.dir, graphDir, wiringFile))

	second, _ := c.Read(Options{Repo: f.root})
	if second.State != StateFresh {
		t.Errorf("state after the deep build = %v, want fresh", second.State)
	}
}

func TestCacheInvalidateForcesARederive(t *testing.T) {
	f := newFixture(t, true, map[string]string{"a.go": "package a\n"})
	var c Cache
	if _, err := c.Read(Options{Repo: f.root}); err != nil {
		t.Fatal(err)
	}
	// An edit changes nothing inside graft/, so only an explicit invalidation
	// (or the TTL) can surface it before the next build.
	write(t, filepath.Join(f.root, "a.go"), "package a\n\nfunc F() {}\n")
	touch(t, filepath.Join(f.root, "a.go"))

	if i, _ := c.Read(Options{Repo: f.root}); i.Drift() {
		t.Error("the cached answer should still be the pre-edit one")
	}
	c.Invalidate(f.root)
	if i, _ := c.Read(Options{Repo: f.root}); !i.Drift() {
		t.Error("after Invalidate the edit should show as drift")
	}
	c.Clear()
	if i, _ := c.Read(Options{Repo: f.root}); !i.Drift() {
		t.Error("after Clear the edit should still show as drift")
	}
}

// touch moves a file's mtime far enough past the recorded one to clear the
// slack, without waiting for a real clock.
func touch(t *testing.T, path string) {
	t.Helper()
	when := time.Now().Add(time.Hour)
	if err := os.Chtimes(path, when, when); err != nil {
		t.Fatal(err)
	}
}

// A directory named `graft` that holds none of graft's files is not an index
// at all, and must not be reported as a broken one: it is somebody's source
// directory, and a sweep that "repaired" it would build an index nobody asked
// for.
func TestForeignGraftDirectoryIsNone(t *testing.T) {
	root := t.TempDir()
	mkdir(t, filepath.Join(root, DirName, "src"))
	write(t, filepath.Join(root, DirName, "src", "main.go"), "package main\n")

	i, _ := Read(Options{Repo: root})
	if i.State != StateNone {
		t.Errorf("state = %v, want none", i.State)
	}
}

// An interrupted build leaves the cache and no graph. That IS broken — and
// `graft build` is what clears it.
func TestHalfBuiltIndexIsBroken(t *testing.T) {
	root := t.TempDir()
	mkdir(t, filepath.Join(root, DirName, cacheDir))
	write(t, filepath.Join(root, DirName, cacheDir, fingerprintPr+"test.json"),
		`{"version":1,"extractor":"test","files":{}}`)

	i, _ := Read(Options{Repo: root})
	if i.State != StateBroken {
		t.Errorf("state = %v, want broken", i.State)
	}
	if !i.NeedsBuild() {
		t.Error("a half-built index is work `graft build` clears")
	}
}

// A checkout whose .gitignore does not cover graft/ leaves its cards in the
// working tree, where graphify's gitignore-aware extraction will index them as
// source. The row says so; it is not a state, and nothing else changes.
func TestExposedWhenGraftIsNotGitignored(t *testing.T) {
	f := newFixture(t, true, map[string]string{"a.go": "package a\n"})

	for _, tc := range []struct {
		name        string
		gitignore   string
		wantExposed bool
	}{
		{"no .gitignore at all", "", true},
		{"graft's own entry", "/graft/\n", false},
		{"bare directory name", "graft\n", false},
		{"trailing slash only", "graft/\n", false},
		{"commented out", "# /graft/\n*.out\n", true},
		{"unrelated rules", "node_modules/\n*.log\n", true},
		{"re-admitted by a later negation", "/graft/\n!graft/\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gi := filepath.Join(f.root, ".gitignore")
			if tc.gitignore == "" {
				os.Remove(gi)
			} else {
				write(t, gi, tc.gitignore)
			}
			i, err := Read(Options{Repo: f.root})
			if err != nil {
				t.Fatal(err)
			}
			if i.Exposed != tc.wantExposed {
				t.Errorf("Exposed = %v, want %v", i.Exposed, tc.wantExposed)
			}
			if i.State != StateFresh {
				t.Errorf("State = %v — the note must not move the verdict", i.State)
			}
		})
	}
}

// A graft directory somewhere else was put there with --dir, and this package
// does not judge a path it did not derive.
func TestExposedIsNotGuessedForADirectoryElsewhere(t *testing.T) {
	f := newFixture(t, true, map[string]string{"a.go": "package a\n"})
	elsewhere := t.TempDir()
	if err := os.Rename(f.dir, filepath.Join(elsewhere, DirName)); err != nil {
		t.Fatal(err)
	}
	i, err := Read(Options{Repo: f.root, Dir: filepath.Join(elsewhere, DirName)})
	if err != nil {
		t.Fatal(err)
	}
	if i.Exposed {
		t.Error("Exposed = true for a --dir index outside the checkout")
	}
}
