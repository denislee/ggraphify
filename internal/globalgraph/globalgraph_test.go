package globalgraph

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeManifest lays down a manifest in graphify's own shape — the one
// graphify/global_graph.py writes — so a change to that shape fails here
// rather than silently reporting every repository as "not a member".
func writeManifest(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, ManifestName)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadReadsGraphifysShape(t *testing.T) {
	src := filepath.Join(t.TempDir(), "graph.json")
	if err := os.WriteFile(src, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	path := writeManifest(t, `{
	  "version": 1,
	  "repos": {
	    "nexus": {
	      "added_at": "2026-09-18T11:04:00+00:00",
	      "source_path": "`+src+`",
	      "node_count": 2305,
	      "edge_count": 6309,
	      "source_hash": "abc123"
	    }
	  }
	}`)

	m := Load(path)
	if m.Err != nil {
		t.Fatalf("Load: %v", m.Err)
	}
	if !m.Exists || m.Len() != 1 {
		t.Fatalf("Exists=%v Len=%d, want true/1", m.Exists, m.Len())
	}
	e, ok := m.ByTag("nexus")
	if !ok {
		t.Fatal("ByTag(nexus) missed")
	}
	if e.Nodes != 2305 || e.Edges != 6309 || e.Hash != "abc123" {
		t.Errorf("entry = %+v", e)
	}
	if e.AddedAt.IsZero() {
		t.Error("added_at did not parse")
	}
	if _, ok := m.ByGraph(src); !ok {
		t.Errorf("ByGraph(%s) missed; byPath holds %q", src, e.Source)
	}
	if n, ed := m.Totals(); n != 2305 || ed != 6309 {
		t.Errorf("Totals = %d/%d", n, ed)
	}
}

// A machine that has never run `graphify global add` has no manifest, and
// that is an answer rather than a failure: every row is simply out.
func TestMissingManifestIsNotAnError(t *testing.T) {
	m := Load(filepath.Join(t.TempDir(), "nope.json"))
	if m.Exists || m.Err != nil || m.Len() != 0 {
		t.Fatalf("Exists=%v Err=%v Len=%d", m.Exists, m.Err, m.Len())
	}
	if got := m.MemberOf("/anywhere/graph.json", time.Now()); got.In {
		t.Errorf("MemberOf on an empty manifest = %+v, want out", got)
	}
}

// A manifest that exists and does not parse must not read as "nothing is in
// the global graph" — that is the answer that invites a duplicate add.
func TestCorruptManifestCarriesItsError(t *testing.T) {
	m := Load(writeManifest(t, `{"version": 1, "repos": {`))
	if !m.Exists {
		t.Error("Exists = false for a file that is there")
	}
	if m.Err == nil {
		t.Error("a truncated manifest parsed without error")
	}
}

// The nil manifest is the "no home directory" case, and every accessor has to
// survive it: the board renders a column from these on every repaint.
func TestNilManifestIsUsable(t *testing.T) {
	var m *Manifest
	if m.Len() != 0 {
		t.Error("Len")
	}
	if _, ok := m.ByTag("x"); ok {
		t.Error("ByTag")
	}
	if _, ok := m.ByGraph("/x/graph.json"); ok {
		t.Error("ByGraph")
	}
	if m.MemberOf("/x/graph.json", time.Now()).In {
		t.Error("MemberOf")
	}
}

func TestMemberOfStaleness(t *testing.T) {
	added := time.Date(2026, 9, 18, 11, 4, 0, 0, time.UTC)
	src := filepath.Join(t.TempDir(), "graph.json")
	path := writeManifest(t, `{"version":1,"repos":{"gg":{
	  "added_at":"`+added.Format(time.RFC3339)+`","source_path":"`+src+`",
	  "node_count":10,"edge_count":20,"source_hash":"h"}}}`)
	m := Load(path)

	cases := []struct {
		name      string
		builtAt   time.Time
		wantIn    bool
		wantStale bool
	}{
		{"built before the add", added.Add(-time.Hour), true, false},
		{"built in the same second", added.Add(300 * time.Millisecond), true, false},
		{"rebuilt after the add", added.Add(time.Hour), true, true},
		{"no graph at all", time.Time{}, true, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := m.MemberOf(src, c.builtAt)
			if got.In != c.wantIn || got.Stale != c.wantStale {
				t.Errorf("MemberOf = %+v, want in=%v stale=%v", got, c.wantIn, c.wantStale)
			}
			if got.Tag != "gg" {
				t.Errorf("tag = %q", got.Tag)
			}
		})
	}

	if got := m.MemberOf(filepath.Join(t.TempDir(), "other.json"), added); got.In {
		t.Errorf("an unrelated graph reported as a member: %+v", got)
	}
}

// The board asks by path, and the path it has is built from the row's output
// directory. Two spellings of the same file must not read as two repositories.
func TestByGraphNormalizesSpelling(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "graph.json")
	if err := os.WriteFile(src, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	m := Load(writeManifest(t, `{"version":1,"repos":{"t":{"added_at":"2026-01-01T00:00:00+00:00",
	  "source_path":"`+src+`","node_count":1,"edge_count":1,"source_hash":"h"}}}`))

	for _, spelling := range []string{
		src,
		filepath.Join(dir, ".", "graph.json"),
		filepath.Join(dir, "sub", "..", "graph.json"),
		GraphFor(dir),
	} {
		if _, ok := m.ByGraph(spelling); !ok {
			t.Errorf("ByGraph(%q) missed", spelling)
		}
	}
}

// The cache is what makes a 130-row scan affordable; it also has to notice a
// `graphify global add` that ran outside this process.
func TestCacheRereadsWhenTheFileMoves(t *testing.T) {
	path := writeManifest(t, `{"version":1,"repos":{}}`)
	var c Cache

	if got := c.LoadFrom(path).Len(); got != 0 {
		t.Fatalf("Len = %d, want 0", got)
	}
	body := `{"version":1,"repos":{"new":{"added_at":"2026-01-01T00:00:00+00:00",
	  "source_path":"/tmp/x/graph.json","node_count":1,"edge_count":1,"source_hash":"h"}}}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	// Same-second writes can leave mtime unmoved on a coarse clock, so the
	// size change is what this leans on — it is part of the key for exactly
	// this reason.
	if got := c.LoadFrom(path).Len(); got != 1 {
		t.Fatalf("Len after rewrite = %d, want 1", got)
	}

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	c.Invalidate()
	if got := c.LoadFrom(path); got.Exists || got.Len() != 0 {
		t.Fatalf("after removal: Exists=%v Len=%d", got.Exists, got.Len())
	}
}
