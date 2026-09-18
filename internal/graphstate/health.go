package graphstate

import "strings"

// Issue is one concrete thing wrong with a repository's graph, in the terms a
// remedy can be chosen from.
//
// The board's State is a headline — one word for a row. An issue is the
// detail behind it, and a graph can have several at once: a stale manifest AND
// unnamed communities AND no report is three separate commands, not one.
type Issue struct {
	// Code is the stable machine name, matched on by the remediation planner.
	Code string
	// What is the one-line statement of the defect, as the UI shows it.
	What string
	// Why is what it costs you to leave it — the reason it is on the list at
	// all rather than a matter of taste.
	Why string
}

// Issue codes. A repository with none of these is healthy.
const (
	IssueNoGraph     = "no-graph"      // never extracted
	IssueBroken      = "broken"        // graph.json missing or unreadable
	IssueNeedsUpdate = "needs-extract" // graphify's own needs_update flag
	IssueDrift       = "drift"         // the tree has moved since the manifest
	IssueBehind      = "behind"        // built from a commit that is no longer HEAD
	IssueNoCommunity = "no-community"  // clustering has not run
	IssueUnnamed     = "unnamed"       // communities exist with placeholder names
	IssueNoReport    = "no-report"     // no GRAPH_REPORT.md
)

// Healthy is the definition the Fix button drives towards: a graph that
// exists and parses, a manifest that matches the tree, communities that have
// been detected AND named, and a GRAPH_REPORT.md to read them from.
//
// Deliberately NOT included: graph.html, the wiki, the tree and call-flow
// exports. Those are renderings somebody asks for, and a repository without
// them is not unhealthy — it is just not exported. Adding them here would make
// "fix" mean "regenerate 60 MB of HTML" on every click.
func (g Graph) Healthy() bool { return len(g.Issues()) == 0 }

// Issues lists everything wrong with this graph, worst first.
//
// A graph that does not exist has exactly one issue: it does not exist.
// Listing "no communities" and "no report" underneath would be three ways of
// saying the same thing and would make the fix plan look three times longer
// than the single command it is.
func (g Graph) Issues() []Issue {
	switch g.State {
	case StateNone:
		return []Issue{{
			Code: IssueNoGraph,
			What: "no graph has ever been built here",
			Why:  "every query, export and report reads graph.json, and there is none",
		}}
	case StateBroken:
		what := "the output directory has no readable graph.json"
		if g.Err != "" {
			what = g.Err
		}
		return []Issue{{
			Code: IssueBroken,
			What: what,
			Why:  "this is what a killed or out-of-disk build leaves behind; nothing can read it",
		}}
	}

	var out []Issue
	if g.NeedsUpdate {
		out = append(out, Issue{
			Code: IssueNeedsUpdate,
			What: "graphify has flagged this graph for re-extraction (needs_update)",
			Why:  "the flag is set when files changed in a way the AST pass alone cannot reconcile",
		})
	}
	if n := g.DriftAdded + g.DriftChanged + g.DriftRemoved; n > 0 {
		out = append(out, Issue{
			Code: IssueDrift,
			What: "the tree has moved since the graph was built (" + g.DriftString() + ")",
			Why:  "queries answer from the old code — the worst failure mode, because it looks like an answer",
		})
	}
	if g.Behind() {
		out = append(out, Issue{
			Code: IssueBehind,
			What: "built at " + shortSHA(g.BuiltCommit) + ", HEAD is now " + shortSHA(g.HeadCommit),
			Why:  "commits have landed since the build; the graph answers for code this checkout has moved past",
		})
	}
	if g.Communities == 0 {
		out = append(out, Issue{
			Code: IssueNoCommunity,
			What: "clustering has not run — the graph has no communities",
			Why:  "community structure is what makes the graph navigable instead of a wall of nodes",
		})
	} else if !g.Labeled {
		out = append(out, Issue{
			Code: IssueUnnamed,
			What: itoa(g.Communities) + " communities carry placeholder names",
			Why:  "\"Community 214\" tells a reader nothing; the names are what the report is for",
		})
	}
	if !g.HasReport {
		out = append(out, Issue{
			Code: IssueNoReport,
			What: "no GRAPH_REPORT.md",
			Why:  "it is the file agents are pointed at for architecture questions",
		})
	}
	return out
}

// shortSHA is the seven-character form the UI shows commits in. A commit that
// is already shorter than that is left alone rather than padded.
func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// IssueSummary is the compact form for a tooltip or a row: the issue codes,
// comma-separated, or "healthy".
func (g Graph) IssueSummary() string {
	issues := g.Issues()
	if len(issues) == 0 {
		return "healthy"
	}
	codes := make([]string, 0, len(issues))
	for _, i := range issues {
		codes = append(codes, i.Code)
	}
	return strings.Join(codes, ", ")
}
