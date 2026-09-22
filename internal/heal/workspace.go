package heal

import "github.com/dns/ggraphify/internal/workspace"

// The fourth index — and the one that is not about a repository at all.
//
// graft federates a directory that holds checkouts: `graft build <root>`
// writes <root>/graft/workspace.json listing the children, and from then on a
// query at the root fans out across all of them and labels each hit with the
// child it came from. That file drifts for one reason, and it is the reason
// nobody notices: a repository was cloned, or deleted, and the federation
// still describes the set as it was. Queries keep working and keep being
// wrong by omission, which is the worst failure mode an index has.
//
// The remedy is the same free command that built it, pointed at the root.
// That is what makes it fit an unattended loop, and it is also what makes it
// self-pruning: graft lists the children it finds, so the rebuild that adds a
// new checkout drops a deleted one in the same pass. There is no "remove
// child" command to plan, because there does not need to be.
//
// Like GraftStep and GlobalStep it is kept out of For: For answers a question
// about one repository's graph.json, and a step that rebuilds a hundred
// sibling checkouts arriving in that dialog would be a plan about a subject
// it never named.

// WorkspaceStep is the free `graft build` at the root that brings a
// federation back in line with what is on disk, and whether this one needs
// it. Pure, like the rest of this package: the state was read elsewhere.
//
// It never federates a root for the first time. workspace.Inspect already
// declines to report drift for a directory with no workspace.json, so this
// can only ever be reached for a root graft has federated before — which
// matters, because the first build is the decision that a directory is a
// fleet, and a loop that made that decision would be writing a graft/
// directory into a parent the user never pointed graft at.
func WorkspaceStep(s workspace.State) (Step, bool) {
	if s.Issue() != workspace.IssueDrift {
		return Step{}, false
	}
	why := "re-federate graft's workspace at this root — "
	switch {
	case len(s.Missing) > 0 && len(s.Orphans) > 0:
		why += "it lists " + itoa(len(s.Orphans)) + " checkout(s) that are gone and " +
			"misses " + itoa(len(s.Missing)) + " that are there"
	case len(s.Missing) > 0:
		why += "it misses " + itoa(len(s.Missing)) + " checkout(s), so a query at the " +
			"root answers as if they did not exist"
	default:
		why += "it lists " + itoa(len(s.Orphans)) + " checkout(s) that are no longer " +
			"on disk, so it resolves children that are not there"
	}
	return Step{
		Kind: "graft-build",
		Why:  why + ". Free: tree-sitter only, and unchanged children replay from cache",
	}, true
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
