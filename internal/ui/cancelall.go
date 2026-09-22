package ui

import (
	"github.com/diamondburned/gotk4-adwaita/pkg/adw"

	"github.com/dns/ggraphify/internal/applog"
)

// Stopping everything, and the one case where that deserves a question.
//
// "Stop all" is instant on purpose, and the reasoning in jobsview.go is right
// as far as it goes: a RUNNING job that is killed can be run again from its
// row, so friction in front of the button would be friction in the moment
// somebody needs it most — a sweep going wrong across the whole board.
//
// What that reasoning misses is the QUEUE. A queue is not a job; it is the
// record of a decision somebody made once, over a set of repositories they
// picked, and "can simply be re-run" is false for it in the way that matters:
// re-running means finding those repositories again and re-selecting them.
// A sweep queued over a hundred checkouts and cancelled by one unconfirmed
// click leaves a sidecar full of `canceled` entries with no start time — work
// that was never done, and no longer recorded anywhere as outstanding.
//
// So the question is asked about the queue and only about the queue, and only
// once it is big enough that rebuilding it is real work. Stopping what is
// running stays one click, whatever else is going on.

// cancelAllConfirmAt is how many queued jobs make "Stop all" worth a
// question. Small on purpose: a handful is a queue somebody can retype from
// memory, and anything past that is a selection they made deliberately.
const cancelAllConfirmAt = 5

// cancelAllNeedsConfirm decides whether the click is asked about. Running jobs
// deliberately do not count — killing those is the recoverable direction, and
// it is what the button is for.
func cancelAllNeedsConfirm(queued, _ int) bool {
	return queued > cancelAllConfirmAt
}

// cancelAll is the one path behind every "Stop all" / "Cancel all" control, so
// that the policy above cannot be true in the jobs dialog and false in the
// dock. after runs only when something was actually cancelled, on the main
// thread, and is where a caller repaints a view the runner's own events would
// not reach in time.
func (a *App) cancelAll(after func()) {
	if a.runner == nil {
		return
	}
	queued, running := a.runner.Active()
	if queued+running == 0 {
		return
	}
	if !cancelAllNeedsConfirm(queued, running) {
		a.doCancelAll(queued, running, after)
		return
	}

	body := "This stops " + plural(running, "job that is running", "jobs that are running") +
		" and drops " + plural(queued, "job that has not started yet",
		"jobs that have not started yet") + ".\n\n" +
		"The running ones can be started again from their rows. The queued ones cannot: " +
		"cancelling them is not a pause, and nothing remembers which repositories they " +
		"were queued over — rebuilding that queue means selecting those checkouts again.\n\n" +
		"Nothing already written to any graph is touched."

	dlg := adw.NewAlertDialog("Stop "+plural(queued+running, "job", "jobs")+"?", body)
	dlg.SetPreferWideLayout(true)
	dlg.AddResponse("keep", "Keep the queue")
	dlg.AddResponse("stop", "Stop everything")
	dlg.SetResponseAppearance("stop", adw.ResponseDestructive)
	dlg.SetDefaultResponse("keep")
	dlg.SetCloseResponse("keep")
	dlg.ConnectResponse(func(resp string) {
		if resp != "stop" {
			return
		}
		// Re-read rather than trusting the counts the dialog was built from:
		// jobs finish and lanes free up while a dialog is open, and the log
		// line below should say what was actually stopped.
		q, r := a.runner.Active()
		a.doCancelAll(q, r, after)
	})
	dlg.Present(a.win)
}

func (a *App) doCancelAll(queued, running int, after func()) {
	a.runner.CancelAll()
	applog.Infof("cancel all: %d queued, %d running", queued, running)
	a.toastf("stopped %s", plural(queued+running, "job", "jobs"))
	if after != nil {
		after()
	}
}
