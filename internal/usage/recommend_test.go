package usage

import (
	"testing"
	"time"

	"github.com/dns/ggraphify/internal/board"
	"github.com/dns/ggraphify/internal/discover"
	"github.com/dns/ggraphify/internal/graftstate"
	"github.com/dns/ggraphify/internal/graphstate"
)

func row(path, name string, g graphstate.Graph, gi graftstate.Index) board.Row {
	return board.Row{Repo: discover.Repo{Path: path, Name: name}, Graph: g, Graft: gi}
}

// summaryWith builds a Summary carrying only the per-directory usage counts,
// which is all Recommend reads.
func summaryWith(counts ...Count) Summary { return Summary{Repos: counts} }

func TestRecommendRanksUsageWithoutAnIndex(t *testing.T) {
	rows := []board.Row{
		row("/r/none", "none", graphstate.Graph{State: graphstate.StateNone}, graftstate.Index{}),
		row("/r/drift", "drift", graphstate.Graph{State: graphstate.StateStale, DriftChanged: 3}, graftstate.Index{State: graftstate.StateFresh}),
		row("/r/fine", "fine", graphstate.Graph{State: graphstate.StateFresh, Communities: 4, Labeled: true}, graftstate.Index{State: graftstate.StateFresh}),
	}
	recs := Recommend(rows, summaryWith(
		Count{Name: "/r/drift", Count: 40},
		Count{Name: "/r/none", Count: 9},
		Count{Name: "/r/fine", Count: 100},
		Count{Name: "/elsewhere/scratch", Count: 12},
	), 0)

	if len(recs) != 3 {
		t.Fatalf("recs = %+v, want three: the healthy repository must produce none", recs)
	}
	if recs[0].Repo != "/r/drift" || recs[0].Action != Update || recs[0].Metered() {
		t.Fatalf("first = %+v, want the busiest one, a free update", recs[0])
	}
	if recs[1].Repo != "/elsewhere/scratch" || recs[1].Action != AddRoot {
		t.Fatalf("second = %+v, want the unboarded directory", recs[1])
	}
	if recs[2].Repo != "/r/none" || recs[2].Action != Extract || !recs[2].Metered() {
		t.Fatalf("third = %+v, want the ungraphed repository, metered", recs[2])
	}
}

// Usage is recorded per working directory: a subdirectory's calls belong to
// the checkout above it, and must be added to it rather than counted as a
// separate, unboarded place.
func TestRecommendFoldsSubdirectoriesIntoTheCheckout(t *testing.T) {
	rows := []board.Row{row("/r/svc", "svc", graphstate.Graph{State: graphstate.StateNone}, graftstate.Index{})}
	recs := Recommend(rows, summaryWith(
		Count{Name: "/r/svc", Count: 2},
		Count{Name: "/r/svc/internal/ui", Count: 5},
	), 0)
	if len(recs) != 1 || recs[0].Uses != 7 {
		t.Fatalf("recs = %+v, want one recommendation counting 7 uses", recs)
	}
}

func TestRecommendPrefersTheWorstDefectAndSkipsHealthy(t *testing.T) {
	for _, tc := range []struct {
		name string
		row  board.Row
		want Action
		none bool
	}{
		{"no graph at all", row("/a", "a", graphstate.Graph{State: graphstate.StateNone}, graftstate.Index{State: graftstate.StateFresh}), Extract, false},
		{"pending re-extraction", row("/b", "b", graphstate.Graph{State: graphstate.StateStale, NeedsUpdate: true}, graftstate.Index{State: graftstate.StateFresh}), Extract, false},
		{"drift outranks graft", row("/c", "c", graphstate.Graph{State: graphstate.StateStale, DriftAdded: 2}, graftstate.Index{State: graftstate.StateNone}), Update, false},
		{"graft missing", row("/d", "d", graphstate.Graph{State: graphstate.StateFresh, Communities: 2, Labeled: true}, graftstate.Index{State: graftstate.StateNone}), GraftBuild, false},
		{"placeholders last", row("/e", "e", graphstate.Graph{State: graphstate.StateRaw, Communities: 9}, graftstate.Index{State: graftstate.StateFresh}), Label, false},
		{"healthy", row("/f", "f", graphstate.Graph{State: graphstate.StateFresh, Communities: 3, Labeled: true, BuiltAt: time.Now()}, graftstate.Index{State: graftstate.StateFresh}), "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recs := Recommend([]board.Row{tc.row}, summaryWith(Count{Name: tc.row.Path, Count: 3}), 0)
			if tc.none {
				if len(recs) != 0 {
					t.Fatalf("recs = %+v, want none", recs)
				}
				return
			}
			if len(recs) != 1 || recs[0].Action != tc.want {
				t.Fatalf("recs = %+v, want %s", recs, tc.want)
			}
			if recs[0].Why == "" {
				t.Fatal("a recommendation with no reason is not a recommendation")
			}
		})
	}
}

// ~/git is not an unindexed repository; it is the directory the indexed ones
// live in, and telling somebody to add a scan root for it is advice to add a
// root they already have.
func TestRecommendIgnoresContainerDirectories(t *testing.T) {
	rows := []board.Row{row("/home/me/git/svc", "svc", graphstate.Graph{State: graphstate.StateFresh, Communities: 1, Labeled: true}, graftstate.Index{State: graftstate.StateFresh})}
	recs := Recommend(rows, summaryWith(
		Count{Name: "/home/me/git", Count: 4},
		Count{Name: "/somewhere/else", Count: 1},
	), 0)
	if len(recs) != 1 || recs[0].Repo != "/somewhere/else" {
		t.Fatalf("recs = %+v, want only the genuinely unboarded directory", recs)
	}
}

// A repository nobody has used is not a recommendation, however unbuilt it is:
// the whole point is that usage is the argument.
func TestRecommendIgnoresUnusedRepositories(t *testing.T) {
	rows := []board.Row{row("/r/idle", "idle", graphstate.Graph{State: graphstate.StateNone}, graftstate.Index{})}
	if recs := Recommend(rows, summaryWith(), 0); len(recs) != 0 {
		t.Fatalf("recs = %+v, want none", recs)
	}
}

func TestRecommendLimits(t *testing.T) {
	var rows []board.Row
	var counts []Count
	for _, n := range []string{"a", "b", "c"} {
		rows = append(rows, row("/r/"+n, n, graphstate.Graph{State: graphstate.StateNone}, graftstate.Index{}))
		counts = append(counts, Count{Name: "/r/" + n, Count: 5})
	}
	if recs := Recommend(rows, summaryWith(counts...), 2); len(recs) != 2 {
		t.Fatalf("recs = %d, want 2", len(recs))
	}
}
