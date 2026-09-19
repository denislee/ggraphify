package heal

import "github.com/dns/ggraphify/internal/graftstate"

// The other index. graft keeps its own graph under <repo>/graft, built by
// tree-sitter and read by the agent rather than by this board, and it goes
// stale for exactly the reason graphify's does: the tree moved and the
// recorded files no longer describe it.
//
// The remedy is one free command — `graft build`, no key, no network, no LLM —
// which is what makes it fit an unattended loop at all. It is kept out of For
// because For answers a question about graph.json and is asked that question
// by the Fix dialog, which shows a plan about graphify; a graft step arriving
// in that plan would be a second tool appearing in a dialog that never named
// it. The loop composes the two.

// GraftStep is the free `graft build` that repairs a graft index, and whether
// this index needs one. It is pure, like everything else in this package: the
// state was read elsewhere.
func GraftStep(i graftstate.Index) (Step, bool) {
	switch i.Issue() {
	case graftstate.IssueStale:
		return Step{
			Kind: "graft-build",
			Why: "rebuild graft's index — the files it recorded have changed or gone, " +
				"so its cards point at lines the checkout has moved past. Free: tree-sitter only",
		}, true
	case graftstate.IssueBroken:
		return Step{
			Kind: "graft-build",
			Why: "rebuild graft's index — the graft directory has no readable wiring.json, " +
				"so every graft query in this checkout answers from nothing. Free: tree-sitter only",
		}, true
	}
	return Step{}, false
}
