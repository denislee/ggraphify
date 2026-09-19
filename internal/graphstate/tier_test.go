package graphstate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// semanticManifest writes a manifest where the first n entries carry a
// semantic hash and the rest carry only an AST one — the shape a partially
// completed metered pass leaves behind.
func (f *fixture) semanticManifest(files, semantic int) {
	f.t.Helper()
	m := map[string]manifestEntry{}
	for i := 0; i < files; i++ {
		e := manifestEntry{MTime: 1, ASTHash: "ast"}
		if i < semantic {
			e.SemanticHash = "sem"
		}
		m["f"+itoa(i)+".go"] = e
	}
	b, err := json.Marshal(m)
	if err != nil {
		f.t.Fatal(err)
	}
	f.writeOut("manifest.json", string(b))
}

// The whole point of the counter: a graph nothing metered has ever touched is
// indistinguishable from a healthy one by every other measure the board has.
func TestCoverageSeparatesTheTiers(t *testing.T) {
	cases := []struct {
		name           string
		files, sem     int
		wantTier       string
		wantPctAtLeast int
	}{
		{"never extracted semantically", 10, 0, "ast", 0},
		{"interrupted pass", 10, 3, "mixed", 30},
		{"complete pass", 10, 10, "semantic", 100},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			f.semanticManifest(c.files, c.sem)
			cov, ok := CoverageOf(f.out)
			if !ok {
				t.Fatal("CoverageOf reported no manifest")
			}
			if cov.Files != c.files || cov.Semantic != c.sem {
				t.Fatalf("Coverage = %d/%d, want %d/%d", cov.Semantic, cov.Files, c.sem, c.files)
			}
			if cov.Tier() != c.wantTier {
				t.Errorf("Tier() = %q, want %q", cov.Tier(), c.wantTier)
			}
			if cov.Percent() != c.wantPctAtLeast {
				t.Errorf("Percent() = %d, want %d", cov.Percent(), c.wantPctAtLeast)
			}
		})
	}
}

// "No manifest" and "a manifest with no semantic hashes" are different facts,
// and reporting the first as the second would tell a reader that a repository
// with no graph at all has an AST-tier one.
func TestCoverageOfMissingManifestIsNotATier(t *testing.T) {
	cov, ok := CoverageOf(t.TempDir())
	if ok {
		t.Fatalf("CoverageOf on a directory with no manifest reported ok (%+v)", cov)
	}
	if cov.Tier() != "" {
		t.Errorf("Tier() = %q, want empty", cov.Tier())
	}
}

// A manifest that is not JSON must not be read as an empty one: an unreadable
// file is not evidence that the semantic pass never ran.
func TestCoverageOfGarbageManifest(t *testing.T) {
	f := newFixture(t)
	if err := os.WriteFile(filepath.Join(f.out, "manifest.json"), []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := CoverageOf(f.out); ok {
		t.Error("CoverageOf accepted a manifest that is not JSON")
	}
}
