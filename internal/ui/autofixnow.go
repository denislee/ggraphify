package ui

import (
	"strings"

	"github.com/dns/ggraphify/internal/autofix"
	"github.com/dns/ggraphify/internal/usage"
)

// The auto-fix loop, on demand — the same loop, started by a click.
//
// "Worth doing next" already puts a button on every line, and each one runs
// the single command that line names. What it did not have is the obvious
// second answer: fix the whole list, the way the board fixes it when nobody
// is watching. That is not a new repair path — it is autoFixTick without the
// wait, so a person who trusts the loop can have its result now instead of on
// the next tick, and a person who has auto-fix switched off can still borrow
// it once without turning it on.
//
// Two things differ from the tick, both because a click is a person:
//
//   - The give-up memory and the cooldown are cleared for the repositories in
//     scope. Those bounds exist so an unattended loop does not spend a night
//     re-running a fix that cannot work; they are not what a button owes
//     somebody who just pressed it and is watching.
//   - The in-flight cap is lifted to the size of the list. The cap keeps a
//     hundred-checkout board from queueing a hundred chains the moment it
//     starts; an explicit click over eight named repositories is the opposite
//     of that, and the runner's lanes are the real limit anyway.
//
// What does NOT differ is money. Metered still comes from the settings — the
// click is consent to run the loop, not consent to bill a card — so with the
// metered switch off this does the free half and says what it left undone.

// autoFixNow runs one auto-fix pass over repos, right now.
//
// An empty repos means the whole board. It is the main-thread half: gather
// rows, clear the memory for what is in scope, then decide on a worker,
// because resolving whether a local model is up is an HTTP call.
func (a *App) autoFixNow(repos []string) {
	if a.opts.Store == nil || a.runner == nil || a.autofix == nil {
		return
	}
	if a.autofixBusy {
		// A tick is already probing. Two passes planning off the same rows
		// would each reserve attempts for the same repositories.
		a.toastf("auto-fix: already deciding what to do — a moment")
		return
	}

	cands := autoFixScope(a.autoFixCandidates(), repos)
	if len(cands) == 0 {
		a.toastf("auto-fix: nothing on this list it can repair on its own")
		return
	}
	for _, c := range cands {
		a.autofix.Retry(c.Path)
	}

	set := a.opts.Store.Settings()
	a.autofixBusy = true
	go func() {
		pol := autoFixPolicy(set)
		// The click is the switch: the loop may be off as a standing
		// preference and still be what this person is asking for once.
		pol.Enabled = true
		pol.Max = len(cands)

		broken, busy := 0, 0
		for _, c := range cands {
			if autofix.Defect(c, pol) == "" {
				continue
			}
			broken++
			if c.Busy {
				busy++
			}
		}

		var (
			actions []autofix.Action
			skips   []autofix.Skip
		)
		msg, run := autoFixNowVerdict(len(cands), broken, busy)
		if run {
			actions, skips = a.autofix.Plan(cands, pol)
		}
		idle(func() {
			a.autofixBusy = false
			logAutoFixSkips(skips)
			switch {
			case !run:
				a.toastf("auto-fix: %s", msg)
			case len(actions) == 0:
				a.toastf("auto-fix: nothing started — %s", autoFixSkipLine(skips))
			default:
				a.runAutoFix(actions, pol)
			}
		})
	}()
}

// autoFixScope narrows the board's candidates to the repositories named, in
// the order they were named — the order the list that produced them was
// ranked in. An empty list means the whole board.
func autoFixScope(cands []autofix.Candidate, repos []string) []autofix.Candidate {
	if len(repos) == 0 {
		return cands
	}
	by := make(map[string]autofix.Candidate, len(cands))
	for _, c := range cands {
		by[c.Path] = c
	}
	out := make([]autofix.Candidate, 0, len(repos))
	for _, p := range repos {
		if c, ok := by[p]; ok {
			out = append(out, c)
		}
	}
	return out
}

// autoFixNowVerdict is what the click found before anything ran: whether to
// plan at all, and what to say when the answer is no.
//
// The interesting case is the middle one. A list can be worth acting on and
// have nothing left to repair — the recommendations are computed from the
// last scan, and between that scan and this click the fix may have been made
// by a tick of the loop, by the per-line button, or by hand in a terminal.
// Reporting that as "already done" is the honest answer; planning it would
// reserve attempts for repositories with no defect and report nothing
// happened, which reads like a broken button.
func autoFixNowVerdict(targets, broken, busy int) (msg string, run bool) {
	switch {
	case targets == 0:
		return "nothing on this list it can repair on its own", false
	case broken == 0:
		return "already done — every repository on this list has a current index", false
	case busy >= broken:
		return "every repository on this list already has a job running", false
	}
	return "", true
}

// autoFixSkipLine is the skips in one toast: the first two reasons, and a
// count for the rest. The full list goes to the log either way.
func autoFixSkipLine(skips []autofix.Skip) string {
	if len(skips) == 0 {
		return "nothing left that can be fixed without an LLM"
	}
	parts := make([]string, 0, 3)
	for i, s := range skips {
		if i == 2 {
			parts = append(parts, "and "+plural(len(skips)-2, "other", "others"))
			break
		}
		parts = append(parts, s.Name+": "+s.Why)
	}
	return strings.Join(parts, "; ")
}

// autoFixTargets is the repositories on a recommendation list that the loop
// can actually act on.
//
// AddRoot is dropped, and that is the whole filter: adding a scan root is a
// decision about which checkouts this board watches, not a repair, and it is
// the one recommendation whose button opens settings rather than running a
// command. A list of nothing but scan roots produces no targets, which is
// what leaves the button insensitive.
func autoFixTargets(recs []usage.Rec) []string {
	out := make([]string, 0, len(recs))
	for _, r := range recs {
		if r.Action == usage.AddRoot {
			continue
		}
		out = append(out, r.Repo)
	}
	return out
}
