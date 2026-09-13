// Package heal turns "this repository's graph is not healthy" into the exact
// ordered list of graphify commands that would make it healthy.
//
// It is a pure function of a graphstate.Graph: no filesystem, no subprocess,
// no GTK. That is what makes the Fix button auditable — the plan it is about to
// run can be printed, tested and argued with before a single process starts,
// and the same plan is what the confirm dialog shows.
package heal

import (
	"github.com/dns/ggraphify/internal/gfy"
	"github.com/dns/ggraphify/internal/graphstate"
)

// Step is one command in a fix plan.
type Step struct {
	Kind string // a key of gfy.Known
	// Why is the issue this step answers, in one line, for the dialog.
	Why string
	// Flags are the parameter overrides this step needs on top of the row's
	// ordinary ones. The UI applies them to gfy.Params; keeping them as data
	// rather than a closure is what lets the golden argv test assert on a plan.
	Force       bool
	NoLabel     bool
	MissingOnly bool
}

// Cost is the step's cost, read from the one table that owns that question.
func (s Step) Cost() gfy.Cost { return gfy.CostOf(s.Kind) }

// Apply writes the step's overrides onto a params struct.
func (s Step) Apply(p *gfy.Params) {
	p.Force = p.Force || s.Force
	p.NoLabel = p.NoLabel || s.NoLabel
	p.MissingOnly = p.MissingOnly || s.MissingOnly
}

// Plan is an ordered remediation for one repository.
type Plan struct {
	Issues []graphstate.Issue
	Steps  []Step
	// Unreachable is set when the plan cannot finish the job — the free-only
	// plan for a repository that has never been extracted, say. It names what
	// will still be wrong afterwards, so the dialog can say so rather than
	// promising a "healthy" it will not deliver.
	Unreachable []graphstate.Issue
}

// Metered reports whether any step spends money.
func (p Plan) Metered() bool {
	for _, s := range p.Steps {
		if s.Cost() == gfy.Metered {
			return true
		}
	}
	return false
}

// Empty reports that there is nothing to do.
func (p Plan) Empty() bool { return len(p.Steps) == 0 }

// For builds the plan for one graph.
//
// allowMetered=false asks for the free-only plan: AST work, clustering and
// reporting, but no LLM call. It is a genuinely useful answer — an AST rebuild
// plus clustering fixes drift and produces a report for nothing — and it is
// honest about what it cannot reach, which is community names and the semantic
// half of an extraction.
//
// The ordering is the whole point and it is not negotiable: rebuild the graph
// before clustering it, cluster before naming, name before expecting a report
// with names in it. Each step is also individually idempotent, so a plan that
// is interrupted halfway can simply be run again.
func For(g graphstate.Graph, allowMetered bool) Plan {
	p := Plan{Issues: g.Issues()}
	if len(p.Issues) == 0 {
		return p
	}

	has := func(code string) bool {
		for _, i := range p.Issues {
			if i.Code == code {
				return true
			}
		}
		return false
	}
	unreachable := func(code string) {
		for _, i := range p.Issues {
			if i.Code == code {
				p.Unreachable = append(p.Unreachable, i)
			}
		}
	}

	// clustered tracks whether a step already ran clustering, so the plan does
	// not queue `cluster-only` behind an `update` that just did it. `extract`
	// deliberately stops at graph.json — it prints "next: run cluster-only" —
	// so it does NOT set this.
	clustered := false
	// fromScratch means the graph is being built rather than refreshed, so
	// every community it ends up with is brand new and unnamed — even though
	// the CURRENT state has no "unnamed communities" issue to read that off,
	// because it has no communities at all yet.
	fromScratch := false

	switch {
	case has(graphstate.IssueNoGraph), has(graphstate.IssueBroken):
		fromScratch = true
		// A broken output directory is rebuilt in place: graphify's own
		// shrink guard refuses to replace a larger graph with a smaller one,
		// and a half-written graph.json is exactly the case where that guard
		// would block the fix.
		force := has(graphstate.IssueBroken)
		if allowMetered {
			p.Steps = append(p.Steps, Step{
				Kind:  "extract",
				Why:   "build the graph — AST plus the semantic pass over docs and images",
				Force: force,
			})
		} else {
			// `update` is `extract`'s free half: it rebuilds every code file
			// from the AST and clusters, and it leaves docs, images and their
			// concept nodes out because those are the part that needs an LLM.
			p.Steps = append(p.Steps, Step{
				Kind:  "update",
				Why:   "rebuild the graph from the AST — free, but without the semantic pass over docs and images",
				Force: force,
			})
			clustered = true
		}

	case has(graphstate.IssueNeedsUpdate) && allowMetered:
		// graphify sets needs_update when the change is one the AST pass alone
		// cannot reconcile, which is precisely when `update` is the wrong tool.
		p.Steps = append(p.Steps, Step{
			Kind: "extract",
			Why:  "graphify flagged this graph for re-extraction, which is the semantic pass, not the AST one",
		})

	case has(graphstate.IssueDrift), has(graphstate.IssueNeedsUpdate):
		p.Steps = append(p.Steps, Step{
			Kind: "update",
			Why:  "re-extract the changed files from the AST and re-cluster — free",
		})
		clustered = true
		if has(graphstate.IssueNeedsUpdate) {
			// Free-only: `update` clears the drift but graphify's flag is
			// about the semantic half, which nothing free can supply.
			unreachable(graphstate.IssueNeedsUpdate)
		}
	}

	if !clustered && (has(graphstate.IssueNoCommunity) || has(graphstate.IssueNoReport) ||
		len(p.Steps) > 0) {
		// Always after an extract: `extract` writes graph.json and stops, so
		// the communities it detected have no names and there is no report
		// until this runs. --no-label is what keeps it free; naming is the
		// next step and is priced separately on purpose.
		p.Steps = append(p.Steps, Step{
			Kind:    "cluster-only",
			Why:     "detect communities and write GRAPH_REPORT.md — free",
			NoLabel: true,
		})
	}

	if fromScratch || has(graphstate.IssueUnnamed) || has(graphstate.IssueNoCommunity) {
		if allowMetered {
			p.Steps = append(p.Steps, Step{
				Kind:        "label",
				Why:         "name the communities with the LLM — METERED",
				MissingOnly: true,
			})
		} else if fromScratch {
			p.Unreachable = append(p.Unreachable, graphstate.Issue{
				Code: graphstate.IssueUnnamed,
				What: "the communities this build finds will keep placeholder names",
				Why:  "naming them is an LLM pass, and this plan spends nothing",
			})
		} else {
			unreachable(graphstate.IssueUnnamed)
			unreachable(graphstate.IssueNoCommunity)
		}
	}

	return p
}
