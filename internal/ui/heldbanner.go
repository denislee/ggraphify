package ui

import "github.com/dns/ggraphify/internal/gfy"

// A held queue is work that will never start on its own.
//
// Three things put a job in that state: it costs money, it costs hours of this
// machine, or it was running when the last session ended and came back as
// outstanding work rather than as a silent re-run (see session.go). All three
// are deliberate, and all three have the same failure mode — a board where
// every row looks idle, a status bar clause most eyes skip, and a launch toast
// that is gone in five seconds. That is how a restored queue waits a whole
// afternoon for a click nobody knew was owed.
//
// So the held count gets a banner of its own, for as long as it is non-zero,
// with the one button that answers it. It is not an error and is not styled
// like one: it is a question the board is holding open.

// refreshHeldBanner keeps the banner on the actual held count. It rides the
// same refresh as the status bar, and rewrites its wording only when the
// number moves — a banner re-titled every second flickers under a screen
// reader even though nothing changed.
func (a *App) refreshHeldBanner() {
	if a.heldBanner == nil || a.runner == nil {
		return
	}
	n := a.runner.HeldCount()
	if n == 0 {
		a.heldShown = 0
		a.heldBanner.SetRevealed(false)
		return
	}
	if n != a.heldShown {
		a.heldShown = n
		a.heldBanner.SetTitle(plural(n, "job is", "jobs are") + " waiting to be started — " +
			"held because it costs something, or because it was running when this board " +
			"last closed")
		a.heldBanner.SetButtonLabel("Start all held (" + gfy.Itoa(n) + ")")
	}
	a.heldBanner.SetRevealed(true)
}

// startAllHeld releases the whole held queue. It is the banner's button and
// the jobs dialog's "Start all held" doing the same thing, so that answering
// the question from the board and answering it from the dialog cannot differ.
func (a *App) startAllHeld() {
	if a.runner == nil {
		return
	}
	n := a.runner.ReleaseAll()
	if n == 0 {
		// The count moved between the paint and the click — another view
		// released them, or they were cancelled. Say nothing and let the
		// refresh below take the banner down.
		a.refreshHeldBanner()
		return
	}
	a.toastf("started %s", plural(n, "held job", "held jobs"))
	// ReleaseAll emits a transition per job, so the rows, the dock and the
	// dialog all repaint on their own. The banner does not listen to those,
	// and this is the moment it stops being true.
	a.refreshHeldBanner()
	if a.jobsPage != nil {
		a.jobsPage.reload()
	}
}
