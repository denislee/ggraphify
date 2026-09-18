package usage

import (
	"sort"
	"time"
)

// SessionRoll is one agent session: which checkout it ran in, how long it ran,
// how much it did, and how much of that went through either index.
//
// It exists because the per-day rollup answers "how much was graft used here"
// and cannot answer "by whom, in which sitting". RepoDay keeps sessions as a
// deduplicated list of ids — set membership, no counters — so a repository
// with four hundred graft calls across two sessions and one across forty read
// identically. The dimension is in every Event already (Event.Session); this
// is where it stops being thrown away.
//
// Tools is the denominator, and the reason the transcript half registers a
// session even when it never touched either tool: a sitting that ran graft
// twice and a sitting that ran graft twice out of four hundred tool calls are
// the same number and opposite facts.
type SessionRoll struct {
	Session string `json:"session"`
	Account string `json:"account"`
	// Repo is the working directory the session reported, and Branch the git
	// branch it was on, both as of the last line read.
	Repo   string `json:"repo"`
	Branch string `json:"branch,omitempty"`
	// Start is the first timestamped line in the transcript, Last the newest.
	Start time.Time `json:"start"`
	Last  time.Time `json:"last"`

	// Tools is every tool call the session made, of any kind — see
	// countToolUses for what makes that affordable, and what it costs in
	// precision.
	Tools int `json:"tools"`

	// Counts is keyed by tool/kind/verb, exactly as RepoDay.Counts, and Fails
	// by tool/reason/verb as RepoDay.Fails.
	Counts map[string]int `json:"counts,omitempty"`
	Fails  map[string]int `json:"fails,omitempty"`

	// The graft counter half, from graft's own per-session files. They are
	// keyed by the same session id the transcripts carry, which is what lets
	// the two halves join without a heuristic.
	GraftReads   int   `json:"graft_reads,omitempty"`
	SourceReads  int   `json:"source_reads,omitempty"`
	Nudges       int   `json:"nudges,omitempty"`
	SavedTokens  int64 `json:"saved_tokens,omitempty"`
	CostMicros   int64 `json:"cost_micros,omitempty"`
	BilledTokens int64 `json:"billed_tokens,omitempty"`
}

// ByTool is how many real invocations of one tool the session made, hooks
// excluded — the same rule Summary.ByTool follows, for the same reason.
func (r SessionRoll) ByTool(t Tool) int {
	n := 0
	for ck, c := range r.Counts {
		if tool, kind, _ := splitCounter(ck); tool == t && kind != Hook {
			n += c
		}
	}
	return n
}

// HooksByTool is how often one integration injected context into the session.
// A session with forty injections and no calls is the "installed and ignored"
// case the dashboard exists to surface.
func (r SessionRoll) HooksByTool(t Tool) int {
	n := 0
	for ck, c := range r.Counts {
		if tool, kind, _ := splitCounter(ck); tool == t && kind == Hook {
			n += c
		}
	}
	return n
}

// Uses is both tools' real invocations.
func (r SessionRoll) Uses() int { return r.ByTool(Graphify) + r.ByTool(Graft) }

// Hooks is both integrations' injections.
func (r SessionRoll) Hooks() int { return r.HooksByTool(Graphify) + r.HooksByTool(Graft) }

// Failed is how many of this session's calls came back unusable.
func (r SessionRoll) Failed() int {
	n := 0
	for fk, c := range r.Fails {
		if _, reason, _ := splitFail(fk); reason != FailNone {
			n += c
		}
	}
	return n
}

// Share is what fraction of the session's tool calls went to either index, in
// percent, and whether there were any tool calls to divide. It is the one
// number that says whether an agent reached for these tools or worked around
// them.
func (r SessionRoll) Share() (int, bool) {
	if r.Tools <= 0 {
		return 0, false
	}
	uses := r.Uses()
	if uses > r.Tools {
		// countToolUses is a byte scan, not a decode, so the denominator can
		// come in slightly under a count assembled from decoded blocks. Report
		// the ceiling rather than a percentage above a hundred.
		return 100, true
	}
	return uses * 100 / r.Tools, true
}

// Mix is the share of this session's reads that went through graft rather than
// straight to the source, in percent, from graft's own counters.
func (r SessionRoll) Mix() (int, bool) {
	total := r.GraftReads + r.SourceReads
	if total == 0 {
		return 0, false
	}
	return r.GraftReads * 100 / total, true
}

// CostUSD is what this session was billed for input, in dollars.
func (r SessionRoll) CostUSD() float64 { return float64(r.CostMicros) / 1e6 }

// Idle reports whether the session touched neither tool. These are the rows
// that make the list an adoption view rather than a usage view: without them
// the denominator is invisible and every session looks like a user.
func (r SessionRoll) Idle() bool { return r.Uses() == 0 }

// SessionRolls returns the sessions alive in the window, newest activity
// first, at most n of them (n <= 0 means all).
//
// A session counts as in-window if its last line falls inside it: a sitting
// that began before the window and ran into it is this window's usage, and one
// that ended before it began is not.
func (x *Index) SessionRolls(w Window, n int) []SessionRoll {
	from, _, _ := w.resolve()

	x.mu.RLock()
	defer x.mu.RUnlock()

	// Selected and sorted as pointers, and copied only once the list has been
	// trimmed. The live watch re-reads this every couple of seconds with every
	// session of the retention window in hand, and cloning a few hundred maps
	// to then show twelve of them is the one way this page could cost anything.
	hits := make([]*SessionRoll, 0, len(x.Sessions))
	for _, r := range x.Sessions {
		if r == nil || r.Last.Before(from) {
			continue
		}
		if w.Repo != "" && !RepoMatch(w.Repo, r.Repo) {
			continue
		}
		hits = append(hits, r)
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if !hits[i].Last.Equal(hits[j].Last) {
			return hits[i].Last.After(hits[j].Last)
		}
		return hits[i].Session < hits[j].Session
	})
	if n > 0 && len(hits) > n {
		hits = hits[:n]
	}
	out := make([]SessionRoll, 0, len(hits))
	for _, r := range hits {
		out = append(out, r.clone())
	}
	return out
}

// SessionCount is how many sessions the window holds, and how many of them
// used either tool. It is the pair the tiles need and is far cheaper than
// rolling every session up to count two of them.
func (x *Index) SessionCount(w Window) (total, used int) {
	from, _, _ := w.resolve()
	x.mu.RLock()
	defer x.mu.RUnlock()
	for _, r := range x.Sessions {
		if r == nil || r.Last.Before(from) {
			continue
		}
		if w.Repo != "" && !RepoMatch(w.Repo, r.Repo) {
			continue
		}
		total++
		if !r.Idle() {
			used++
		}
	}
	return total, used
}

// clone copies a roll and its maps, so a caller can read it after the lock is
// dropped while the next update keeps folding into the original.
func (r *SessionRoll) clone() SessionRoll {
	out := *r
	out.Counts = make(map[string]int, len(r.Counts))
	for k, v := range r.Counts {
		out.Counts[k] = v
	}
	if r.Fails != nil {
		out.Fails = make(map[string]int, len(r.Fails))
		for k, v := range r.Fails {
			out.Fails[k] = v
		}
	}
	return out
}
