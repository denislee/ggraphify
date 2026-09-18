package graphstate

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func future() float64 { return float64(time.Now().Add(time.Hour).Unix()) }

// The bug this is the regression test for: a manifest entry the walk cannot
// reach — because its directory is gitignored, vendored or a nested checkout —
// was reported as removed on every tick, forever. The file is on disk and in
// the graph; nothing is missing. One real checkout showed −31 permanently.
func TestRemovedNeedsTheFileToBeGone(t *testing.T) {
	f := newFixture(t)
	f.graphJSON(1, 0, "")
	f.writeOut(".graphify_labels.json", `{"0":"Real"}`)
	f.write(".gitignore", ".pulumi-local/\n")
	f.write("a.go", "package a")
	f.write("stack/.pulumi-local/.pulumi/meta.yaml", "v: 1")
	f.write("vendor/dep/dep.go", "package dep")
	f.manifest(map[string]float64{
		"a.go":                                  future(),
		"stack/.pulumi-local/.pulumi/meta.yaml": future(),
		"vendor/dep/dep.go":                     future(),
	})

	g, _ := Read(Options{Repo: f.repo})
	if g.DriftRemoved != 0 {
		t.Fatalf("DriftRemoved = %d (%v), want 0 — the files are on disk",
			g.DriftRemoved, g.DriftFiles.Removed)
	}
	if g.State != StateFresh {
		t.Fatalf("State = %v, want fresh", g.State)
	}

	// A file that is genuinely deleted still counts, and is named.
	if err := os.Remove(filepath.Join(f.repo, "a.go")); err != nil {
		t.Fatal(err)
	}
	g, _ = Read(Options{Repo: f.repo})
	if g.DriftRemoved != 1 || len(g.DriftFiles.Removed) != 1 || g.DriftFiles.Removed[0] != "a.go" {
		t.Fatalf("DriftRemoved = %d %v, want exactly [a.go]", g.DriftRemoved, g.DriftFiles.Removed)
	}
}

// A .gitignore in a subdirectory is the ordinary way a monorepo ignores the
// artefacts next to the code that generates them. Reading only the root file
// counted every one of them as a file graphify forgot to extract.
func TestDriftHonoursNestedGitignore(t *testing.T) {
	f := newFixture(t)
	f.graphJSON(1, 0, "")
	f.writeOut(".graphify_labels.json", `{"0":"Real"}`)
	f.write(".gitignore", "*.tmp\n")
	f.write("scripts/.gitignore", "/import.json\ngenerated-*.go\n")
	f.write("scripts/real.go", "package s")
	f.write("scripts/import.json", "{}")
	f.write("scripts/generated-records.go", "package s")
	f.write("tools/.gitignore", "out/\n")
	f.write("tools/out/thing.go", "package out")
	f.manifest(map[string]float64{"scripts/real.go": future(), "a.json": future()})
	f.write("a.json", "{}")

	g, _ := Read(Options{Repo: f.repo})
	if g.DriftAdded != 0 {
		t.Fatalf("DriftAdded = %d (%v), want 0 — all nested-gitignored",
			g.DriftAdded, g.DriftFiles.Added)
	}
}

// A deeper .gitignore re-admitting something the root ignored is the rule that
// makes a nested file an override rather than an addition.
func TestNestedGitignoreNegationOverridesTheRoot(t *testing.T) {
	f := newFixture(t)
	f.graphJSON(1, 0, "")
	f.writeOut(".graphify_labels.json", `{"0":"Real"}`)
	f.write(".gitignore", "*.json\n")
	f.write("keep/.gitignore", "!wanted.json\n")
	f.write("keep/wanted.json", "{}")
	f.write("drop/other.json", "{}")
	f.manifest(map[string]float64{"a.json": future()})
	f.write("a.json", "{}")

	g, _ := Read(Options{Repo: f.repo})
	if g.DriftAdded != 1 || g.DriftFiles.Added[0] != "keep/wanted.json" {
		t.Fatalf("DriftAdded = %d %v, want exactly [keep/wanted.json]",
			g.DriftAdded, g.DriftFiles.Added)
	}
}

// graphify never extracts a lock file or a non-source file under a credential
// directory, so neither is a file it forgot.
func TestDriftSkipsWhatGraphifyRefuses(t *testing.T) {
	f := newFixture(t)
	f.graphJSON(1, 0, "")
	f.writeOut(".graphify_labels.json", `{"0":"Real"}`)
	f.write("a.go", "package a")
	f.write("app/package-lock.json", "{}")
	f.write("app/go.sum", "x")
	f.write("secrets/Pulumi.yaml", "name: x")
	f.write("secrets/main.go", "package main") // real source: still drift
	f.write("certs/server.pem", "-----BEGIN")
	f.manifest(map[string]float64{"a.go": future(), "x.yaml": future(), "x.sum": future(), "x.pem": future()})
	f.write("x.yaml", "a: 1")
	f.write("x.sum", "x")
	f.write("x.pem", "x")

	g, _ := Read(Options{Repo: f.repo})
	if g.DriftAdded != 1 || g.DriftFiles.Added[0] != "secrets/main.go" {
		t.Fatalf("DriftAdded = %d %v, want exactly [secrets/main.go]",
			g.DriftAdded, g.DriftFiles.Added)
	}
}

// The baseline is the answer to drift that is real, permanent and not
// graphify's fault: a file it read and got nothing from is absent from the
// manifest forever. Once a successful run has settled it, it stops counting —
// until it is edited, when it is a real change again.
func TestBaselineSettlesUnexplainedDriftUntilItChanges(t *testing.T) {
	f := newFixture(t)
	f.graphJSON(1, 0, "")
	f.writeOut(".graphify_labels.json", `{"0":"Real"}`)
	f.write("a.go", "package a")
	f.write("data/huge.json", "{}") // graphify read it, got no nodes
	f.write("cfg.json", "{}")       // a .json it did graph, so .json is in scope
	f.manifest(map[string]float64{"a.go": future(), "cfg.json": future()})

	g, _ := Read(Options{Repo: f.repo})
	if g.DriftAdded != 1 {
		t.Fatalf("DriftAdded = %d %v, want 1 before any baseline", g.DriftAdded, g.DriftFiles.Added)
	}

	base := MakeBaseline(f.repo, g.DriftFiles, time.Now().Add(time.Minute))
	g, _ = Read(Options{Repo: f.repo, Baseline: base})
	if g.DriftAdded != 0 || g.DriftFiles.Acked != 1 {
		t.Fatalf("after the baseline: added %d, acked %d — want 0 and 1",
			g.DriftAdded, g.DriftFiles.Acked)
	}
	if g.State != StateFresh {
		t.Fatalf("State = %v, want fresh — a settled path must not pin the row to stale", g.State)
	}

	// Editing it makes it drift again: the baseline remembers an mtime, not a
	// permanent exemption.
	later := time.Now().Add(2 * time.Hour)
	if err := os.Chtimes(filepath.Join(f.repo, "data/huge.json"), later, later); err != nil {
		t.Fatal(err)
	}
	g, _ = Read(Options{Repo: f.repo, Baseline: base})
	if g.DriftAdded != 1 {
		t.Fatalf("DriftAdded = %d, want 1 — an edited path counts again", g.DriftAdded)
	}
}

// A baseline must not swallow an edit made while the run was in flight: that
// file may never have been seen, so it stays drift until the next run.
func TestBaselineSkipsPathsTouchedDuringTheRun(t *testing.T) {
	f := newFixture(t)
	f.write("late.json", "{}")
	d := Drift{Added: []string{"late.json"}}
	b := MakeBaseline(f.repo, d, time.Now().Add(-time.Hour))
	if len(b.Added) != 0 {
		t.Fatalf("baseline acked %v, want nothing — the file is newer than the run", b.Added)
	}
}

// A removed entry is settled the same way: graphify only rewrites its manifest
// when the graph's topology moves, so a deletion it does not care about would
// otherwise show forever.
func TestBaselineSettlesRemovals(t *testing.T) {
	f := newFixture(t)
	f.graphJSON(1, 0, "")
	f.writeOut(".graphify_labels.json", `{"0":"Real"}`)
	f.write("a.go", "package a")
	f.manifest(map[string]float64{"a.go": future(), "gone.go": future()})

	g, _ := Read(Options{Repo: f.repo})
	if g.DriftRemoved != 1 {
		t.Fatalf("DriftRemoved = %d, want 1", g.DriftRemoved)
	}
	base := MakeBaseline(f.repo, g.DriftFiles, time.Now())
	g, _ = Read(Options{Repo: f.repo, Baseline: base})
	if g.DriftRemoved != 0 || g.DriftFiles.Acked != 1 {
		t.Fatalf("after the baseline: removed %d, acked %d — want 0 and 1",
			g.DriftRemoved, g.DriftFiles.Acked)
	}
}

// The names are the point of the report; an edit has to be named too.
func TestDriftNamesChangedFiles(t *testing.T) {
	f := newFixture(t)
	f.graphJSON(1, 0, "")
	f.writeOut(".graphify_labels.json", `{"0":"Real"}`)
	f.write("a.go", "package a")
	f.manifest(map[string]float64{"a.go": float64(time.Now().Add(-time.Hour).Unix())})

	g, _ := Read(Options{Repo: f.repo})
	if g.DriftChanged != 1 || len(g.DriftFiles.Changed) != 1 || g.DriftFiles.Changed[0] != "a.go" {
		t.Fatalf("changed = %d %v, want [a.go]", g.DriftChanged, g.DriftFiles.Changed)
	}
}

// MaxBaselinePaths bounds the sidecar: a checkout with thousands of unexplained
// paths has something else wrong and must not grow the state file without limit.
func TestBaselineIsBounded(t *testing.T) {
	f := newFixture(t)
	var added []string
	for i := 0; i < MaxBaselinePaths+50; i++ {
		rel := "d/f" + itoa(i) + ".json"
		f.write(rel, "{}")
		added = append(added, rel)
	}
	b := MakeBaseline(f.repo, Drift{Added: added}, time.Time{})
	if len(b.Added) != MaxBaselinePaths {
		t.Fatalf("baseline holds %d paths, want the cap of %d", len(b.Added), MaxBaselinePaths)
	}
}

// `**/coverage/` is git's "in every directory, the root included" — and the
// root is the case that matters, because that is where a Flutter checkout keeps
// its `.pub-cache/` and its `coverage/`. The matcher used to read the leading
// `**/` as an ordinary anchored path component, which never matched at depth
// zero: one real repository reported 26 091 added files, every one of them
// gitignored, which pinned the row to `stale` and made the auto-fix loop give
// up on drift it could never clear.
func TestDriftHonoursLeadingDoubleStar(t *testing.T) {
	f := newFixture(t)
	f.graphJSON(1, 0, "")
	f.writeOut(".graphify_labels.json", `{"0":"Real"}`)
	f.write(".gitignore", "**/coverage/\n**/.pub-cache/\n**/android/app/gen/\n")
	f.write("a.go", "package a")
	f.write("coverage/html/index.go", "package h")           // root: the regressed case
	f.write("modules/x/coverage/html/index.go", "package h") // and still at depth
	f.write(".pub-cache/hosted/pkg/lib.go", "package lib")
	f.write("android/app/gen/g.go", "package p")     // multi-segment, at the root
	f.write("x/y/android/app/gen/g.go", "package p") // and the same at depth
	f.manifest(map[string]float64{"a.go": future()})

	g, _ := Read(Options{Repo: f.repo})
	if g.DriftAdded != 0 {
		t.Fatalf("DriftAdded = %d (%v), want 0 — every one of them is gitignored",
			g.DriftAdded, g.DriftFiles.Added)
	}

	// The rule still has to be a rule and not a blanket: a file that matches
	// nothing counts exactly as before.
	f.write("real.go", "package r")
	g, _ = Read(Options{Repo: f.repo})
	if g.DriftAdded != 1 || g.DriftFiles.Added[0] != "real.go" {
		t.Fatalf("DriftAdded = %d %v, want [real.go]", g.DriftAdded, g.DriftFiles.Added)
	}
}
