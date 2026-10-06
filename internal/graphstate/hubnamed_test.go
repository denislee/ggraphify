package graphstate

import (
	"os"
	"path/filepath"
	"testing"
)

// writeFile is a tiny helper so the hub-named tests do not need a full fixture.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// With a snapshot of the last successful LLM labeling, only the communities
// whose signature has moved since are hub-named — even when the name itself is
// still an LLM-style phrase (the hub may have chosen "Mocks" for a community
// the LLM once called "Mocks").
func TestReadHubNamedFromSnapshot(t *testing.T) {
	out := t.TempDir()
	writeFile(t, filepath.Join(out, ".graphify_labels.json"),
		`{"0":"HTTP Routes","1":"Mocks","2":"testing.T"}`)
	writeFile(t, filepath.Join(out, ".graphify_labels.json.sig"),
		`{"0":"a","1":"b","2":"c"}`)
	writeFile(t, filepath.Join(out, LLMLabelSigFile),
		`{"0":"a","1":"b","2":"x"}`)

	if got := readHubNamed(out); got != 1 {
		t.Fatalf("readHubNamed = %d, want 1", got)
	}
}

// Without a snapshot ggraphify falls back to the shape of the name: the hub
// fallback is one symbol or path ("testing.T", ".Return") and LLM names are
// phrases, while placeholders are skipped entirely.
func TestReadHubNamedHeuristicWithoutSnapshot(t *testing.T) {
	out := t.TempDir()
	writeFile(t, filepath.Join(out, ".graphify_labels.json"),
		`{"0":"HTTP Routes","1":"testing.T","2":".Return","3":"Community 3"}`)

	if got := readHubNamed(out); got != 2 {
		t.Fatalf("readHubNamed = %d, want 2", got)
	}
}

// MarkLLMLabeled copies graphify's current signature, so the communities the
// LLM just named stop reading as hub-named.
func TestMarkLLMLabeledClearsHubNamed(t *testing.T) {
	out := t.TempDir()
	writeFile(t, filepath.Join(out, ".graphify_labels.json"),
		`{"0":"HTTP Routes","1":"Mocks","2":"testing.T"}`)
	writeFile(t, filepath.Join(out, ".graphify_labels.json.sig"),
		`{"0":"a","1":"b","2":"c"}`)
	writeFile(t, filepath.Join(out, LLMLabelSigFile),
		`{"0":"a","1":"b","2":"x"}`)

	if got := readHubNamed(out); got != 1 {
		t.Fatalf("before marking: readHubNamed = %d, want 1", got)
	}
	if err := MarkLLMLabeled(out); err != nil {
		t.Fatalf("MarkLLMLabeled: %v", err)
	}
	if got := readHubNamed(out); got != 0 {
		t.Fatalf("after marking: readHubNamed = %d, want 0", got)
	}
}

// A label job that graphify left no signature for is not a failure: nothing to
// record, nothing written.
func TestMarkLLMLabeledWithoutASignature(t *testing.T) {
	out := t.TempDir()
	if err := MarkLLMLabeled(out); err != nil {
		t.Fatalf("MarkLLMLabeled with no signature = %v, want nil", err)
	}
	if _, err := os.Stat(filepath.Join(out, LLMLabelSigFile)); !os.IsNotExist(err) {
		t.Fatalf("MarkLLMLabeled created %s with no signature to copy", LLMLabelSigFile)
	}
}

// hubThreshold is at least three and at least a tenth, so one changed community
// cannot buy a metered relabel by itself.
func TestHubThreshold(t *testing.T) {
	cases := []struct{ n, want int }{
		{0, 3}, {1, 3}, {20, 3}, {29, 3}, {30, 3}, {31, 4}, {100, 10},
	}
	for _, c := range cases {
		if got := hubThreshold(c.n); got != c.want {
			t.Errorf("hubThreshold(%d) = %d, want %d", c.n, got, c.want)
		}
	}
}

func hasHubIssue(g Graph) bool {
	for _, i := range g.Issues() {
		if i.Code == IssueHubNamed {
			return true
		}
	}
	return false
}

func TestIssueHubNamedThreshold(t *testing.T) {
	cases := []struct {
		name                  string
		communities, hubNamed int
		labeled               bool
		want                  bool
	}{
		{"three of twenty", 20, 3, true, true},
		{"two of twenty", 20, 2, true, false},
		{"nine of a hundred", 100, 9, true, false},
		{"ten of a hundred", 100, 10, true, true},
		{"unlabeled never", 100, 100, false, false},
	}
	for _, c := range cases {
		g := Graph{
			State:       StateFresh,
			Communities: c.communities,
			Labeled:     c.labeled,
			HubNamed:    c.hubNamed,
		}
		if got := hasHubIssue(g); got != c.want {
			t.Errorf("%s: IssueHubNamed present = %v, want %v (issues=%v)",
				c.name, got, c.want, g.IssueSummary())
		}
	}
}
