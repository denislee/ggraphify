package graphstate

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Coverage is how far the semantic half of an extraction got in one output
// directory: how many files the manifest carries, and how many of those have
// a semantic_hash.
//
// It is the difference between the two tiers graphify builds. The AST tier is
// free and fills ast_hash for every file it parses; the semantic tier costs an
// LLM call per chunk and is what fills semantic_hash — and what turns a
// community called `testing.T` into one called "the HTTP test harness". A
// graph whose Semantic is zero is a graph no metered pass has ever completed
// on, which is invisible from the board's counters: it looks exactly like a
// healthy AST graph, because it is one.
type Coverage struct {
	Files    int // manifest entries
	Semantic int // of those, entries carrying a semantic_hash
}

// Tier names the coverage in one word, the way the index and `doctor` report
// it: "ast" for a graph the semantic pass never touched, "semantic" for one it
// finished, "mixed" for a partial pass — an extraction that was interrupted,
// or one run with --code-only over part of the tree.
func (c Coverage) Tier() string {
	switch {
	case c.Files == 0:
		return ""
	case c.Semantic == 0:
		return "ast"
	case c.Semantic >= c.Files:
		return "semantic"
	default:
		return "mixed"
	}
}

// Percent is the semantic share, rounded, for a report line.
func (c Coverage) Percent() int {
	if c.Files == 0 {
		return 0
	}
	return c.Semantic * 100 / c.Files
}

// CoverageOf reads one output directory's manifest and counts the two tiers.
//
// It parses the manifest with a streaming decoder for the same reason
// readGraph does: a manifest for a large repository is megabytes of per-file
// records, and the answer here is two integers. ok is false when there is no
// readable manifest — a repository with no graph at all, which is not the same
// fact as "a graph with no semantic tier" and must not be reported as one.
func CoverageOf(out string) (Coverage, bool) {
	f, err := os.Open(filepath.Join(out, "manifest.json"))
	if err != nil {
		return Coverage{}, false
	}
	defer f.Close()

	dec := json.NewDecoder(f)
	tok, err := dec.Token()
	if err != nil {
		return Coverage{}, false
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return Coverage{}, false
	}
	var c Coverage
	for dec.More() {
		if _, err := dec.Token(); err != nil { // the path, which is not the question
			return Coverage{}, false
		}
		var e manifestEntry
		if err := dec.Decode(&e); err != nil {
			// One unreadable record does not invalidate the count, but a
			// decoder that has lost its place cannot be walked further.
			return c, c.Files > 0
		}
		c.Files++
		if e.SemanticHash != "" {
			c.Semantic++
		}
	}
	return c, true
}
