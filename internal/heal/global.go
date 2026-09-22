package heal

import (
	"github.com/dns/ggraphify/internal/globalgraph"
	"github.com/dns/ggraphify/internal/graphstate"
)

// The third index. The global graph under ~/.graphify holds a copy of every
// merged repository's nodes, and that copy goes stale for the one reason the
// other two do: the repository was extracted again and nothing told the global
// graph about it.
//
// The remedy is `graphify global add` on the same tag — free, no LLM, no
// network, and idempotent, since adding a member that is already there
// replaces its nodes rather than duplicating them. That is what makes it fit
// an unattended loop, the same test `graft build` had to pass.
//
// Like GraftStep it is kept out of For: For answers a question about one
// repository's graph.json and is asked it by the Fix dialog, which shows a
// plan about that repository. A step that edits a file in the home directory
// arriving in that dialog would be a second subject appearing in a plan that
// never named it. The loop composes the two.

// GlobalStep is the free re-add that brings a stale membership up to date, and
// whether this membership needs one. Pure, like the rest of this package: the
// state was read elsewhere.
//
// It deliberately never ADDS a repository that is not already a member. Which
// repositories belong in the global graph is a judgement about what the user
// wants to query across, not a defect — a loop that merged every checkout on
// the board would be answering a question nobody asked, and undoing it is a
// removal per repository.
func GlobalStep(m globalgraph.Member) (Step, bool) {
	if m.Issue() != globalgraph.IssueStale {
		return Step{}, false
	}
	return Step{
		Kind: "global-add",
		Why: "re-merge this repository into the global graph — its own graph has been " +
			"rebuilt since it was added, so cross-repo queries are answering from the " +
			"previous extraction. Free: a merge of two files already on disk",
	}, true
}

// JoinStep is the first merge that brings a repository INTO the global graph,
// and whether this repository is in a state to be merged.
//
// GlobalStep above refuses to join, and that refusal is still right where it
// stands: asked about one membership and nothing else, "this repository is
// not a member" is a choice rather than a defect, and a loop that merged
// every checkout it could see would be answering a question nobody asked.
//
// What makes joining answerable is the second fact the caller brings: the
// repository sits under a root the user nominated as a fleet — a directory
// they have already said they want to query across. Against that, an
// unmerged checkout under it IS a defect: the cross-repo graph is missing a
// repository the user asked for, and it will stay missing until somebody
// notices by hand. The judgement moved to the root; it did not disappear.
//
// The state gate is what keeps the merge honest rather than merely eager.
// `graphify global add` copies the nodes that are in graph.json at the moment
// it runs, so a repository with no graph, or one whose graph cannot be read,
// has nothing to contribute and would either fail the step or — worse —
// merge an empty set and record a member that answers nothing.
func JoinStep(g graphstate.Graph) (Step, bool) {
	switch g.State {
	case graphstate.StateNone, graphstate.StateBroken, graphstate.StateRunning:
		return Step{}, false
	}
	return Step{
		Kind: "global-add",
		Why: "merge this repository into the global graph — it is under a fleet root " +
			"but none of its nodes are in the cross-repo graph, so a query across the " +
			"fleet answers as if it were not there. Free: a merge of two files already " +
			"on disk",
	}, true
}

// PruneStep is the removal that drops a member whose graph is gone.
//
// It is the one membership change that needs no judgement at all, which is
// what separates it from joining. Every other reason a member might not
// belong is a preference; this one is arithmetic. `global add` recorded an
// absolute path to a graph.json, that file is no longer on disk, and the
// nodes merged from it therefore describe a checkout nobody can look at.
// They cannot be refreshed — there is nothing to refresh them from — so the
// member is not stale, it is stranded, and it will answer cross-repo queries
// with file:line citations into a directory that does not exist.
//
// The caller decides WHEN a source counts as gone, and should be strict about
// it: a graph missing because a disk is not mounted is not a repository that
// was deleted, and this step is not reversible without re-extracting.
func PruneStep(tag string) Step {
	return Step{
		Kind: "global-remove",
		Why: "drop " + tag + " from the global graph — the graph.json it was merged " +
			"from is gone, so its nodes cite a checkout that is no longer on this " +
			"machine and cannot be refreshed. Free: one file in the home directory",
	}
}
