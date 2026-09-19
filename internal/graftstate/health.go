package graftstate

// What the board's unattended loop is allowed to repair about a graft index,
// in the same shape graphstate's issue codes take: a stable machine name per
// defect, matched on by the remediation planner rather than by a State
// comparison scattered across three packages.
//
// The list is deliberately shorter than State is. `graft build` can also build
// an index where there has never been one, and `graft build --deep` can add
// the concept layer a wiring-only index lacks — and neither is something to do
// to a checkout while nobody is watching. Building the first index writes a
// graft/ directory INSIDE the repository and appends to its .gitignore, which
// is a decision about somebody's working tree; the deep pass is an LLM run
// whose scheduling belongs to the deep sweep and its local-model gate. What is
// left is the honest unattended case: an index this machine already has, which
// has stopped describing the tree it was built from.
const (
	// IssueStale is an index whose recorded files have changed or gone.
	IssueStale = "graft-stale"
	// IssueBroken is a graft/ directory with no readable wiring.json.
	IssueBroken = "graft-broken"
)

// Issue is the code for what an unattended `graft build` would repair here, or
// "" when there is nothing of that kind to do — including the two states that
// are jobs for a human or for the deep sweep (no index at all, wiring with no
// concept layer).
func (i Index) Issue() string {
	switch i.State {
	case StateStale:
		return IssueStale
	case StateBroken:
		return IssueBroken
	}
	return ""
}

// Repairable reports whether this index has a defect the board may fix on its
// own. It is NeedsBuild minus StateNone, for the reason above.
func (i Index) Repairable() bool { return i.Issue() != "" }
