package graphstate

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

// readLabelState folds what used to be three reads of .graphify_labels.json into
// one. It must return exactly what the three separate readers would, for every
// shape the file comes in, and the flat case's numbers are pinned here.
func TestLabelStateMatchesSeparateReads(t *testing.T) {
	cases := []struct {
		name   string
		labels string
		// extra are sibling files to write, e.g. the two signature files.
		extra map[string]string
		// want* are asserted only when check is set: the other shapes exist to
		// prove the folded reader agrees with the separate ones, not to pin a
		// value nobody has an opinion about.
		wantN       int
		wantLabeled bool
		wantHub     int
		check       bool
	}{
		{
			name:        "flat names",
			labels:      `{"0":"HTTP test harness","1":"testing.T","2":"community_3"}`,
			wantN:       3,
			wantLabeled: true,
			wantHub:     1,
			check:       true,
		},
		{
			name:   "nested under labels",
			labels: `{"labels":{"0":{"name":"Router setup"},"1":"x.Y"}}`,
		},
		{
			name:   "objects without a name",
			labels: `{"0":{"size":3}}`,
		},
		{
			name:   "a number value",
			labels: `{"0":5}`,
		},
		{
			name:   "empty object",
			labels: `{}`,
		},
		{
			name:   "invalid JSON",
			labels: `{"0":`,
		},
		{
			name:   "snapshot moves one signature",
			labels: `{"0":"HTTP test harness","1":"Router setup"}`,
			extra: map[string]string{
				".graphify_labels.json.sig": `{"0":"s0","1":"s1"}`,
				LLMLabelSigFile:             `{"0":"s0","1":"OLD"}`,
			},
			wantN:       2,
			wantLabeled: true,
			wantHub:     1,
			check:       true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeFile(t, filepath.Join(dir, ".graphify_labels.json"), tc.labels)
			for name, body := range tc.extra {
				writeFile(t, filepath.Join(dir, name), body)
			}

			n, labeled, hubNamed := readLabelState(dir)
			wantN, wantLabeled := readLabels(dir)
			wantHub := readHubNamed(dir)
			t.Logf("readLabelState = (%d, %v, %d); readLabels = (%d, %v); readHubNamed = %d",
				n, labeled, hubNamed, wantN, wantLabeled, wantHub)

			if n != wantN || labeled != wantLabeled || hubNamed != wantHub {
				t.Errorf("readLabelState = (%d, %v, %d), separate reads = (%d, %v, %d)",
					n, labeled, hubNamed, wantN, wantLabeled, wantHub)
			}
			if tc.check && (n != tc.wantN || labeled != tc.wantLabeled || hubNamed != tc.wantHub) {
				t.Errorf("readLabelState = (%d, %v, %d), want (%d, %v, %d)",
					n, labeled, hubNamed, tc.wantN, tc.wantLabeled, tc.wantHub)
			}
		})
	}
}
