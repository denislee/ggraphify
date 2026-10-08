package usage

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

// TestGoldenCorpus dumps everything Update derives from a frozen transcript
// corpus, so a change that is meant to be result-neutral can be diffed against
// the output of the code before it. It only runs when USAGE_GOLDEN_OUT is set:
//
//	USAGE_GOLDEN_ACCT  an account dir holding projects/ (a frozen copy)
//	USAGE_GOLDEN_REPOS a dir of mirror repositories carrying graft/.cache/session
//	USAGE_GOLDEN_OUT   where to write the dump
func TestGoldenCorpus(t *testing.T) {
	out := os.Getenv("USAGE_GOLDEN_OUT")
	if out == "" {
		t.Skip("USAGE_GOLDEN_OUT not set")
	}
	acct := os.Getenv("USAGE_GOLDEN_ACCT")
	mirror := os.Getenv("USAGE_GOLDEN_REPOS")

	var frozen, repos []string
	if ents, err := os.ReadDir(mirror); err == nil {
		for _, e := range ents {
			frozen = append(frozen, filepath.Join(mirror, e.Name()))
		}
	}
	repos = append(repos, frozen...)
	// Real checkouts are only used to attribute working directories; their
	// live graft counters are never read, so the corpus stays frozen.
	for _, glob := range []string{"/home/dns/git/*", "/home/dns/tmp/*", "/home/dns/git"} {
		m, _ := filepath.Glob(glob)
		repos = append(repos, m...)
	}
	sort.Strings(repos)

	now := time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)
	opts := Options{Accounts: []Account{{Name: "golden", Dir: acct}}, Repos: frozen, Now: now}

	snap := func(x *Index) map[string]any {
		b, err := json.Marshal(x)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatal(err)
		}
		m["scanned"] = x.Scanned
		return m
	}

	x := New()
	if err := x.Update(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	cold := snap(x)
	if err := x.Update(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	warm := snap(x)

	path := filepath.Join(t.TempDir(), "usage.json")
	if err := x.Save(path); err != nil {
		t.Fatal(err)
	}
	y := Load(path)
	if err := y.Update(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	reload := snap(y)

	// Incremental: every transcript is first seen cut at a line boundary near
	// its middle, then whole — so calls and their results routinely land in
	// different scans, the way the live watch sees them.
	incr := incremental(t, acct, now)

	derived := map[string]string{}
	for _, days := range []int{1, 7, 30, 90} {
		w := Window{Days: days, Now: now}
		derived[fmt.Sprintf("repouses_%d", days)] = fmt.Sprintf("%+v", x.RepoUses(repos, w))
		derived[fmt.Sprintf("summary_%d", days)] = fmt.Sprintf("%+v", x.Summarize(w))
		tot, used := x.SessionCount(w)
		derived[fmt.Sprintf("sessioncount_%d", days)] = fmt.Sprint(tot, used)
		derived[fmt.Sprintf("sessionrolls_%d", days)] = fmt.Sprintf("%+v", x.SessionRolls(w, 0))
	}
	for _, r := range repos {
		if ev := x.Recents(r, 20); len(ev) > 0 {
			derived["recents_"+r] = fmt.Sprintf("%+v", ev)
		}
	}

	b, err := json.MarshalIndent(map[string]any{
		"cold": cold, "warm": warm, "reload": reload, "derived": derived,
		"incremental": incr,
	}, "", " ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(out, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func incremental(t *testing.T, acct string, now time.Time) map[string]any {
	src := filepath.Join(acct, "projects")
	dst := t.TempDir()
	type half struct {
		path string
		rest []byte
	}
	var halves []half
	projects, _ := os.ReadDir(src)
	for _, p := range projects {
		if !p.IsDir() {
			continue
		}
		files, _ := os.ReadDir(filepath.Join(src, p.Name()))
		for _, f := range files {
			if f.IsDir() || filepath.Ext(f.Name()) != ".jsonl" {
				continue
			}
			b, err := os.ReadFile(filepath.Join(src, p.Name(), f.Name()))
			if err != nil {
				t.Fatal(err)
			}
			cut := len(b) / 2
			for cut < len(b) && b[cut] != '\n' {
				cut++
			}
			if cut < len(b) {
				cut++
			}
			dir := filepath.Join(dst, "projects", p.Name())
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, f.Name())
			if err := os.WriteFile(path, b[:cut], 0o600); err != nil {
				t.Fatal(err)
			}
			halves = append(halves, half{path, b[cut:]})
		}
	}
	opts := Options{Accounts: []Account{{Name: "golden", Dir: dst}}, Now: now}
	x := New()
	if err := x.Update(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	first := x.Scanned
	for _, h := range halves {
		f, err := os.OpenFile(h.path, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write(h.rest); err != nil {
			t.Fatal(err)
		}
		f.Close()
	}
	if err := x.Update(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(x)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	// The copies live under a fresh temp dir, so their paths and stat data
	// differ run to run; everything else is keyed by session and cwd.
	delete(m, "files")
	m["scanned"] = []int{first, x.Scanned}
	return m
}
