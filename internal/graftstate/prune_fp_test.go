package graftstate

import (
	"path/filepath"
	"testing"
)

// Prune drops every repository the board no longer tracks — both the derived
// entry and the invalidation counter that guards an in-flight read. The kept
// one must survive in both maps.
func TestCachePrune(t *testing.T) {
	c := &Cache{
		m: map[string]entry{
			"/a": {},
			"/b": {},
			"/c": {},
		},
		gen: map[string]uint64{"/a": 1, "/c": 2},
	}

	n := c.Prune(map[string]bool{"/a": true})
	if n != 2 {
		t.Errorf("Prune dropped %d entries, want 2", n)
	}
	if len(c.m) != 1 || c.m == nil {
		t.Fatalf("c.m = %v, want only /a", c.m)
	}
	if _, ok := c.m["/a"]; !ok {
		t.Errorf("Prune dropped the kept entry: %v", c.m)
	}
	if len(c.gen) != 1 {
		t.Fatalf("c.gen = %v, want only /a", c.gen)
	}
	if _, ok := c.gen["/a"]; !ok {
		t.Errorf("Prune dropped the kept invalidation counter: %v", c.gen)
	}

	// A zero Cache has nothing to prune and must not panic on its nil maps.
	var zero Cache
	if n := zero.Prune(map[string]bool{"x": true}); n != 0 {
		t.Errorf("Prune on a zero Cache dropped %d, want 0", n)
	}
}

// The fingerprint reader decodes graft's [size, mtime, digest] records into a
// typed print, dropping anything that is not a whole three-field record.
func TestReadFingerprintTyped(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fingerprint.test.json")
	write(t, path, `{"version":1,"files":{`+
		`"a.go":[120, 1700000000123.5, "abc"],`+
		`"short.go":[1,2],`+
		`"nul.go":null,`+
		`"typed.go":["x", "y", 7],`+
		`"long.go":[5,6,"d","extra"]}}`)

	fp, ok := readFingerprint(path)
	if !ok {
		t.Fatalf("readFingerprint ok = false, want true")
	}
	if len(fp) != 3 {
		t.Fatalf("len(fp) = %d, want 3 (%v)", len(fp), fp)
	}
	if rec, ok := fp["a.go"]; !ok {
		t.Error(`fp["a.go"] missing`)
	} else if rec.size != 120 || rec.mtime != 1700000000123.5 || rec.digest != "abc" {
		t.Errorf("a.go = %+v, want size 120 mtime 1700000000123.5 digest abc", rec)
	}
	for _, rel := range []string{"short.go", "nul.go"} {
		if rec, ok := fp[rel]; ok {
			t.Errorf("%s present with %+v, want absent", rel, rec)
		}
	}
	if rec, ok := fp["typed.go"]; !ok {
		t.Error(`fp["typed.go"] missing`)
	} else if rec.size != 0 || rec.mtime != 0 || rec.digest != "" {
		t.Errorf("typed.go = %+v, want zero size/mtime/digest", rec)
	}
	if rec, ok := fp["long.go"]; !ok {
		t.Error(`fp["long.go"] missing`)
	} else if rec.size != 5 || rec.mtime != 6 || rec.digest != "d" {
		t.Errorf("long.go = %+v, want size 5 mtime 6 digest d", rec)
	}

	// An object where a record should be fails the whole decode.
	objPath := filepath.Join(t.TempDir(), "fp.json")
	write(t, objPath, `{"files":{"a":{"x":1}}}`)
	if _, ok := readFingerprint(objPath); ok {
		t.Error("readFingerprint accepted an object record, want ok false")
	}

	// A missing path is no fingerprint.
	if _, ok := readFingerprint(filepath.Join(t.TempDir(), "nope.json")); ok {
		t.Error("readFingerprint of a missing path returned ok true")
	}

	// No files member at all is an empty fingerprint, not a failure.
	emptyPath := filepath.Join(t.TempDir(), "empty.json")
	write(t, emptyPath, `{"version":1}`)
	fp, ok = readFingerprint(emptyPath)
	if !ok {
		t.Error(`readFingerprint of {"version":1} returned ok false`)
	}
	if len(fp) != 0 {
		t.Errorf(`readFingerprint of {"version":1} = %v, want empty`, fp)
	}
}
