package graphstate

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// This file holds the byte scanner in scan.go against the implementation it
// replaced. scan.go's own doc comment makes the claim — "the counts on any
// well-formed graph.json are identical to the decoder's" — and these tests are
// what makes it a fact rather than a hope: the old reader is copied here
// verbatim (renamed legacy*) and every document is run through both.

// legacyReadGraph is the pre-scanner readGraph: it opens path and hands the
// stream to legacyScan. It exists only so the tests can drive the old reader
// from a file, the way the package does.
func legacyReadGraph(path string, g *Graph) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return legacyScan(f, g)
}

// legacyScan is the old streaming decoder read, factored out of the old
// readGraph so the allocation comparison can hand it a bytes.Reader.
func legacyScan(r io.Reader, g *Graph) error {
	dec := json.NewDecoder(r)
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
		switch key {
		case "nodes":
			n, err := legacyCountArray(dec)
			if err != nil {
				return err
			}
			g.Nodes = n
		case "links", "edges":
			n, err := legacyCountArray(dec)
			if err != nil {
				return err
			}
			g.Links += n
		case "hyperedges":
			n, err := legacyCountArray(dec)
			if err != nil {
				return err
			}
			g.Hyperedges = n
		case "built_at_commit":
			var v string
			if err := dec.Decode(&v); err != nil {
				return err
			}
			g.BuiltCommit = v
		case "graph":
			// Some versions carry built_at_commit inside a "graph" object;
			// 0.9.58 writes a bare 0 here and puts the commit at top level.
			// Decode into a RawMessage so either shape is survivable — a
			// struct decode against `0` would fail the whole read and show
			// the repository as broken.
			var raw json.RawMessage
			if err := dec.Decode(&raw); err != nil {
				return err
			}
			var meta struct {
				BuiltAtCommit string `json:"built_at_commit"`
			}
			if json.Unmarshal(raw, &meta) == nil && g.BuiltCommit == "" {
				g.BuiltCommit = meta.BuiltAtCommit
			}
		default:
			if err := skipValue(dec); err != nil {
				return err
			}
		}
	}
	return nil
}

// legacyCountArray is the old countArray, verbatim.
func legacyCountArray(dec *json.Decoder) (int, error) {
	tok, err := dec.Token()
	if err != nil {
		return 0, err
	}
	d, ok := tok.(json.Delim)
	if !ok || d != '[' {
		// Not an array after all — skip whatever it is and report none.
		if ok && (d == '{') {
			if err := skipRest(dec); err != nil {
				return 0, err
			}
		}
		return 0, nil
	}
	n := 0
	for dec.More() {
		if err := skipValue(dec); err != nil {
			return n, err
		}
		n++
	}
	_, err = dec.Token() // the closing ]
	return n, err
}

// legacyLocateStamp is the old locateStamp, which took an io.Reader (the new
// one takes an *os.File so it can do the ReadAt tail read first). Its returned
// range spans the colon and value, exactly as the new one's does.
func legacyLocateStamp(r io.Reader) (start, end int64, cur string, err error) {
	dec := json.NewDecoder(r)
	tok, err := dec.Token()
	if err != nil {
		return 0, 0, "", err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return 0, 0, "", errors.New("graphstate: graph.json is not a JSON object")
	}
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return 0, 0, "", err
		}
		key, _ := keyTok.(string)
		if key == "built_at_commit" {
			start = dec.InputOffset()
			var v string
			if err := dec.Decode(&v); err != nil {
				return 0, 0, "", err
			}
			return start, dec.InputOffset(), v, nil
		}
		if err := skipValue(dec); err != nil {
			return 0, 0, "", err
		}
	}
	return 0, 0, "", ErrNoStamp
}

// legacyFromBytes writes b to a temp file and reads it with the old reader,
// which is the only way to exercise a path-based API on an in-memory document.
func legacyFromBytes(t *testing.T, b []byte) (Graph, error) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "graph.json")
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	var g Graph
	err := legacyReadGraph(p, &g)
	return g, err
}

// newFromBytes is the scanner's answer to the same document.
func newFromBytes(b []byte) (Graph, error) {
	var g Graph
	err := scanGraph(bytes.NewReader(b), &g)
	return g, err
}

// sameCounters is the equality the whole file rests on.
func sameCounters(a, b Graph) bool {
	return a.Nodes == b.Nodes && a.Links == b.Links &&
		a.Hyperedges == b.Hyperedges && a.BuiltCommit == b.BuiltCommit
}

// checkAgainstLegacy runs one document through both readers and requires that
// they both succeed or both fail, and that their counters agree either way.
func checkAgainstLegacy(t *testing.T, b []byte) {
	t.Helper()
	lg, lerr := legacyFromBytes(t, b)
	ng, nerr := newFromBytes(b)
	if (lerr == nil) != (nerr == nil) {
		t.Fatalf("error disagreement: legacy = %v, scanner = %v", lerr, nerr)
	}
	if !sameCounters(lg, ng) {
		t.Errorf("counters differ:\n legacy  nodes=%d links=%d hyperedges=%d commit=%q\n scanner nodes=%d links=%d hyperedges=%d commit=%q",
			lg.Nodes, lg.Links, lg.Hyperedges, lg.BuiltCommit,
			ng.Nodes, ng.Links, ng.Hyperedges, ng.BuiltCommit)
	}
}

// TestScanGraphMatchesLegacy is the equivalence table: every shape the scanner
// claims to accept or refuse, next to the decoder's answer on the same bytes.
func TestScanGraphMatchesLegacy(t *testing.T) {
	longKey := strings.Repeat("k", 200)
	cases := []struct {
		name string
		doc  string
	}{
		{"empty object", `{}`},
		{"arrays of objects", `{"nodes":[{"id":"a"},{"id":"b"}],"links":[{"source":"a","target":"b"}],"hyperedges":[{"id":"h"}]}`},
		{"edges and links are both added", `{"links":[{"a":1}],"edges":[{"b":2},{"c":3}]}`},
		{"edges without links", `{"edges":[{"b":2},{"c":3}]}`},
		{"escaped quote and trailing backslash", `{"nodes":[{"id":"a\"b","label":"x\\"}],"links":[]}`},
		{"structural characters inside strings", `{"nodes":[{"id":"a[b]{c},d:e"}],"links":[]}`},
		{"nested arrays inside elements", `{"nodes":[[1,[2,3]],[4]],"links":[]}`},
		{"empty arrays", `{"nodes":[],"links":[],"hyperedges":[]}`},
		{"arrays of scalars", `{"nodes":[1,true,false,null,-1.5e3,2.0,0.5e-2,0],"links":[]}`},
		{"unicode escapes", `{"nodes":["\u005b","\u007b","\u002c"],"links":[]}`},
		{"whitespace everywhere", "  \n\t{ \"nodes\"\n:\n[ ] ,\n\"links\" : [ { } ]\n,\"hyperedges\"\n:\n[]\n}\n  "},
		{"nodes is an object", `{"nodes":{"a":1}}`},
		{"nodes is a string", `{"nodes":"x"}`},
		{"nodes is a number", `{"nodes":5}`},
		{"nodes is null", `{"nodes":null}`},
		{"built_at_commit at top level last", `{"nodes":[],"links":[],"built_at_commit":"deadbeef"}`},
		{"built_at_commit inside graph object only", `{"graph":{"built_at_commit":"abc"}}`},
		{"graph is bare zero", `{"graph":0,"nodes":[1,2]}`},
		{"graph is an empty object", `{"graph":{},"nodes":[1,2]}`},
		{"top level stamp before graph stamp", `{"built_at_commit":"top","graph":{"built_at_commit":"inner"}}`},
		{"graph stamp before top level stamp", `{"graph":{"built_at_commit":"inner"},"built_at_commit":"top"}`},
		{"built_at_commit null", `{"built_at_commit":null,"nodes":[1]}`},
		{"built_at_commit is a number", `{"nodes":[1],"built_at_commit":123}`},
		{"built_at_commit is a bool", `{"built_at_commit":true}`},
		{"long unknown key", `{"` + longKey + `":1,"nodes":[1]}`},
		{"unknown key with deep nested value", `{"unknown":{"a":{"b":[1,[2,{"c":3}]]}},"nodes":[{}]}`},
		{"truncated inside the nodes array", `{"nodes":[{"id":"a"},{"id":"b"`},
		{"truncated inside a string", `{"nodes":["abc`},
		{"truncated after the colon", `{"nodes":`},
		{"empty input", ``},
		{"top level array", `[1,2,3]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			checkAgainstLegacy(t, []byte(tc.doc))
		})
	}

	// A document large enough that the scanner's 64 KB buffer fills and is
	// refilled mid-array and mid-string, which is where a fill bug would hide.
	t.Run("large document crosses the buffer boundary", func(t *testing.T) {
		checkAgainstLegacy(t, scanBlob(5000, 9000))
	})
}

// scanBlob builds a graph.json of the given size with a label that exercises
// the string state machine across buffer refills: each label contains escaped
// quotes, a trailing backslash and structural characters.
//
// It deliberately carries only nodes, links and built_at_commit. The "graph"
// key is tested in the table above; here it would add a fixed handful of
// allocations (the RawMessage capture) on top of the per-element zero the pool
// exists to keep, and this document is also the allocation test's input.
func scanBlob(nodes, links int) []byte {
	var b strings.Builder
	b.WriteString(`{"directed":false,"multigraph":false,"nodes":[`)
	for i := 0; i < nodes; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`{"id":"n`)
		b.WriteString(itoa(i))
		b.WriteString(`","label":"a\"b [x] {y} \\ `)
		b.WriteString(itoa(i))
		b.WriteString(`"}`)
	}
	b.WriteString(`],"links":[`)
	for i := 0; i < links; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`{"source":"n0","target":"n1","relation":"CALLS"}`)
	}
	b.WriteString(`],"hyperedges":[],"built_at_commit":"abc123"}`)
	return []byte(b.String())
}

// realGraphPaths returns the largest three and smallest three graph.json files
// under ~/knowledge that are within MaxGraphBytes, deduped. It is shared by the
// equivalence test and the stamp test so both see the same real documents.
func realGraphPaths(t *testing.T) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(os.Getenv("HOME"), "knowledge", "*", "graph.json"))
	if err != nil {
		t.Fatal(err)
	}
	type entry struct {
		path string
		size int64
	}
	var within []entry
	for _, p := range matches {
		fi, err := os.Stat(p)
		if err != nil || !fi.Mode().IsRegular() || fi.Size() > MaxGraphBytes {
			continue
		}
		within = append(within, entry{p, fi.Size()})
	}
	if len(within) == 0 {
		return nil
	}
	sort.Slice(within, func(i, j int) bool { return within[i].size > within[j].size })
	seen := map[string]bool{}
	out := []string{}
	add := func(p string) {
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	for i := 0; i < 3 && i < len(within); i++ {
		add(within[i].path)
	}
	for i := len(within) - 1; i >= 0 && i >= len(within)-3; i-- {
		add(within[i].path)
	}
	return out
}

// TestScanGraphMatchesLegacyOnRealGraphs is the same equivalence claim on the
// actual files the board reads, where the shapes are whatever graphify wrote
// rather than whatever the table thought of.
func TestScanGraphMatchesLegacyOnRealGraphs(t *testing.T) {
	paths := realGraphPaths(t)
	if len(paths) == 0 {
		t.Skip("no graph.json under $HOME/knowledge")
	}
	for _, p := range paths {
		p := p
		t.Run(filepath.Base(filepath.Dir(p)), func(t *testing.T) {
			var lg, ng Graph
			lerr := legacyReadGraph(p, &lg)
			nerr := readGraph(p, &ng)
			if (lerr == nil) != (nerr == nil) {
				t.Fatalf("error disagreement on %s: legacy = %v, scanner = %v", p, lerr, nerr)
			}
			if !sameCounters(lg, ng) {
				t.Errorf("%s counters differ:\n legacy  nodes=%d links=%d hyperedges=%d commit=%q\n scanner nodes=%d links=%d hyperedges=%d commit=%q",
					p, lg.Nodes, lg.Links, lg.Hyperedges, lg.BuiltCommit,
					ng.Nodes, ng.Links, ng.Hyperedges, ng.BuiltCommit)
			}
			t.Logf("%s: nodes=%d links=%d hyperedges=%d commit=%q", p, ng.Nodes, ng.Links, ng.Hyperedges, ng.BuiltCommit)
		})
	}
}

// openTempGraph writes doc to a temp file and returns the open file and its
// size, for the stamp lookups that need an io.ReaderAt plus a size.
func openTempGraph(t *testing.T, doc string) (*os.File, int64) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "graph.json")
	if err := os.WriteFile(p, []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	fi, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	return f, fi.Size()
}

// checkStampPair requires that tailStamp's verdict, and locateStamp's full
// answer, agree with the old locateStamp on the same file.
func checkStampPair(t *testing.T, doc string, wantTailOK bool) {
	t.Helper()
	f, size := openTempGraph(t, doc)

	at, ok := tailStamp(f, size)
	if ok != wantTailOK {
		t.Fatalf("tailStamp ok = %v, want %v (doc %q)", ok, wantTailOK, doc)
	}

	if _, err := f.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	lstart, lend, lcur, lerr := legacyLocateStamp(f)
	sstart, send, scur, serr := locateStamp(f, size)
	if sstart != lstart || send != lend || scur != lcur || (serr == nil) != (lerr == nil) {
		t.Fatalf("locateStamp = (%d, %d, %q, %v), legacy = (%d, %d, %q, %v)",
			sstart, send, scur, serr, lstart, lend, lcur, lerr)
	}

	if ok && (at.start != lstart || at.end != lend || at.cur != lcur) {
		t.Fatalf("tailStamp = (%d, %d, %q), legacy = (%d, %d, %q)", at.start, at.end, at.cur, lstart, lend, lcur)
	}
}

// TestTailStampMatchesLegacyLocate covers the shapes tailStamp is meant to
// accept: graphify's own layout, with and without the whitespace a hand edit
// or a formatter might leave behind.
func TestTailStampMatchesLegacyLocate(t *testing.T) {
	cases := []struct {
		name string
		doc  string
	}{
		{"graphify layout with trailing newline", `{"directed":false,"graph":0,"nodes":[],"links":[],"hyperedges":[],"built_at_commit":"abcdef123456"}` + "\n"},
		{"spaces around the colon", `{"nodes":[],"built_at_commit" : "abc123" }`},
		{"spaces and newlines around the colon", "{\n  \"nodes\": [],\n  \"built_at_commit\"\n  :\n  \"abc123\"\n}\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			checkStampPair(t, tc.doc, true)
		})
	}

	for _, p := range realGraphPaths(t) {
		f, err := os.Open(p)
		if err != nil {
			t.Fatal(err)
		}
		fi, err := f.Stat()
		if err != nil {
			f.Close()
			t.Fatal(err)
		}
		at, ok := tailStamp(f, fi.Size())
		if !ok {
			f.Close()
			continue
		}
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			f.Close()
			t.Fatal(err)
		}
		lstart, lend, lcur, lerr := legacyLocateStamp(f)
		f.Close()
		if lerr != nil {
			t.Errorf("%s: tailStamp accepted a file the legacy reader rejected: %v", p, lerr)
			continue
		}
		if at.start != lstart || at.end != lend || at.cur != lcur {
			t.Errorf("%s: tailStamp = (%d, %d, %q), legacy = (%d, %d, %q)", p, at.start, at.end, at.cur, lstart, lend, lcur)
		}
	}
}

// TestTailStampRefusesOtherShapes pins the refusal: any layout that is not
// graphify's own falls back to the full scan rather than guessing, because a
// tail answer is a splice offset and a wrong one corrupts the file.
func TestTailStampRefusesOtherShapes(t *testing.T) {
	cases := []struct {
		name string
		doc  string
	}{
		{"stamp inside a nested graph object last", `{"nodes":[],"graph":{"built_at_commit":"abc"}}`},
		{"stamp value is null", `{"nodes":[],"built_at_commit":null}`},
		{"stamp value has an escaped quote", `{"nodes":[],"built_at_commit":"a\"b"}`},
		{"file does not end with a brace", `{"nodes":[],"built_at_commit":"abc"`},
		{"stamp text embedded in another string", `{"nodes":[],"x":"say \"built_at_commit\": \"abc\"}"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			checkStampPair(t, tc.doc, false)
		})
	}
}

// TestReadBuiltCommit exercises the cheap read on all three shapes it has to
// answer: the tail layout, the nested fallback, and no file at all.
func TestReadBuiltCommit(t *testing.T) {
	out := t.TempDir()

	write := func(doc string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(out, "graph.json"), []byte(doc), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	write(`{"nodes":[],"links":[],"built_at_commit":"cafebabe"}`)
	got, err := ReadBuiltCommit(out)
	if err != nil || got != "cafebabe" {
		t.Fatalf("ReadBuiltCommit (tail) = %q, %v; want cafebabe, nil", got, err)
	}

	write(`{"nodes":[],"graph":{"built_at_commit":"deadc0de"}}`)
	got, err = ReadBuiltCommit(out)
	if err != nil || got != "deadc0de" {
		t.Fatalf("ReadBuiltCommit (nested fallback) = %q, %v; want deadc0de, nil", got, err)
	}

	if _, err := ReadBuiltCommit(t.TempDir()); err == nil {
		t.Fatal("ReadBuiltCommit on a directory with no graph.json must error")
	}
}

// TestScanGraphAllocs is scan.go's whole reason for existing: counting a graph
// must not allocate per element the way json.Decoder.Token does. The scanner
// sits in a pool, so after the warmup run it allocates little beyond the
// bytes.Reader wrapper.
func TestScanGraphAllocs(t *testing.T) {
	b := scanBlob(5000, 9000)

	newAllocs := testing.AllocsPerRun(20, func() {
		var g Graph
		_ = scanGraph(bytes.NewReader(b), &g)
	})
	legacyAllocs := testing.AllocsPerRun(20, func() {
		var g Graph
		_ = legacyScan(bytes.NewReader(b), &g)
	})
	t.Logf("scanGraph: %.1f allocs/op; legacyScan: %.1f allocs/op", newAllocs, legacyAllocs)

	if newAllocs > 4 {
		t.Errorf("scanGraph allocated %.1f times per run, want at most 4", newAllocs)
	}
	if newAllocs >= legacyAllocs {
		t.Errorf("scanGraph (%.1f) should allocate less than legacyScan (%.1f)", newAllocs, legacyAllocs)
	}
}
