package ui

import (
	"errors"
	"os"
	"time"

	"github.com/dns/ggraphify/internal/applog"
	"github.com/dns/ggraphify/internal/gfy"
	"github.com/dns/ggraphify/internal/jobs"
	"github.com/dns/ggraphify/internal/ringbuf"
	"github.com/dns/ggraphify/internal/store"
)

// The job list across a restart.
//
// A board that forgot its queue every time it closed would be a board you
// could not close: a sweep over 128 checkouts is hours of work, and the one
// thing somebody does in the middle of it is quit the application, reboot, or
// have the session end for them. So the queue, the jobs that were in flight
// and the run log are written to the sidecar on every transition and read back
// on the next launch.
//
// Two rules shape what comes back:
//
//   - A job that was RUNNING is not restored as running. Its process group
//     died with the last session, and its output directory is in whatever
//     state graphify left it. It returns as a held queued job, so the board
//     shows that the work is outstanding without silently repeating it.
//   - A job that costs something returns held. Free work — AST updates,
//     clustering, exports — starts on its own, because that is what the
//     previous session had already decided and it costs nothing to be wrong
//     about. Metered work is an LLM bill and local work is hours of this
//     machine; both wait for the click that the confirm dialog stands for.

// persistJobs writes the runner's whole view of the world to the sidecar.
//
// It is a full rewrite rather than an incremental edit because the runner owns
// the queue and the board does not: the only copy of it that cannot drift is
// the one taken from Snapshot at the moment it changed.
func (a *App) persistJobs() {
	if a.opts.Store == nil || a.runner == nil {
		return
	}
	snaps := a.runner.Snapshot() // newest first
	list := make([]store.JobEntry, 0, len(snaps))
	for _, s := range snaps {
		e := store.JobEntry{
			Kind: s.Kind, Repo: s.Repo, Label: s.Label, Argv: s.Argv,
			Cost: s.Cost.String(), Local: s.Local, Dir: s.Dir, Out: s.Out,
			Status: s.Status.String(), Held: s.Held, Exit: s.Exit,
			Queued: s.Queued, Started: s.Started, Ended: s.Ended, Error: s.Err,
		}
		if s.Log != nil {
			e.Log = s.Log.TailBytes(store.MaxLogTail)
		}
		list = append(list, e)
	}
	a.opts.Store.SetJobs(list)
}

// restoreJobs puts the previous session's jobs back into the runner. It runs
// once, from activate, before the views are built and before anything is
// draining the event channel — which is why Restore emits nothing and the
// caller reloads from Snapshot instead.
//
// It returns how many jobs came back and how many of those are waiting for a
// click, so the board can say so rather than leaving somebody to discover a
// held queue by opening the Jobs dialog.
func (a *App) restoreJobs() (restored, held int) {
	if a.opts.Store == nil || a.runner == nil {
		return 0, 0
	}
	entries := a.opts.Store.Jobs() // newest first
	set := a.opts.Store.Settings()

	// Oldest first, so the ids the runner hands out ascend in the order the
	// previous session created them.
	out := make([]*jobs.Job, 0, len(entries))
	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		j := restoredJob(e, a.opts.Store, set.ClaudeAccount)
		if j == nil {
			continue
		}
		if !j.Status.Done() && j.Held {
			held++
		}
		out = append(out, j)
	}
	restored = a.runner.Restore(out)

	// Seed the per-row job cache by hand. Every other job the board shows got
	// there through an event, and Restore emits none — without this the Job
	// column would be blank on a row whose work is sitting in the queue, which
	// is the one place somebody would look for it.
	a.mu.Lock()
	for _, s := range a.runner.Snapshot() { // newest first
		if s.Repo == "" || s.ID <= a.jobIDs[s.Repo] {
			continue
		}
		snap := s
		a.jobIDs[s.Repo] = s.ID
		a.jobByRepo[s.Repo] = &snap
	}
	a.mu.Unlock()

	return restored, held
}

// restoredJob rebuilds one job from its sidecar entry, or nil when it should
// not come back at all.
func restoredJob(e store.JobEntry, st *store.Store, account string) *jobs.Job {
	if len(e.Argv) == 0 {
		return nil
	}
	status, known := jobs.ParseStatus(e.Status)
	log := ringbuf.New(st.Settings().LogBytes)
	if e.Log != "" {
		log.WriteString(e.Log)
	}

	// A sidecar written before labels were recorded, or by a path that never
	// set one, would restore a row the Jobs dialog and the dock both draw as a
	// blank line. The name is rebuilt from the kind and the checkout.
	lbl := e.Label
	if lbl == "" {
		lbl = gfy.JobLabel(e.Kind, e.Repo)
	}

	j := &jobs.Job{
		Kind: e.Kind, Repo: e.Repo, Label: lbl, Local: e.Local,
		Argv: e.Argv, Dir: e.Dir, Out: e.Out,
		Status: status, Exit: e.Exit,
		Queued: e.Queued, Started: e.Started, Ended: e.Ended,
		Log: log,
	}
	// The cost is re-derived from the kind, which is what SubmitCmd does, and
	// then widened by what the file says. The two can only disagree if a kind
	// changed its price between releases, and the safe direction to resolve
	// that is the expensive one.
	j.Cost = gfy.CostOf(e.Kind)
	if e.Cost == gfy.Metered.String() {
		j.Cost = gfy.Metered
	}
	if e.Error != "" {
		j.Err = errors.New(e.Error)
	}

	if e.Done() {
		// A status this version does not recognise is kept out of the queue
		// rather than re-run; it lands in the history as what it is.
		if !known || status == jobs.Running {
			j.Status = jobs.Failed
		}
		return j
	}

	// From here on it is outstanding work. A checkout that has since been
	// deleted or moved cannot be worked on, and queueing a job that can only
	// fail would be noise at every launch.
	if e.Dir != "" {
		if fi, err := os.Stat(e.Dir); err != nil || !fi.IsDir() {
			applog.Infof("restore: dropping %s — %s is gone", e.Kind, e.Dir)
			return nil
		}
	}
	j.Status = jobs.Queued
	j.Started = time.Time{}
	j.Ended = time.Time{}

	// The environment is rebuilt rather than restored, the same way Retry
	// rebuilds it: the sidecar holds variable names, never a credential's
	// value, and the backend the argv was built for is readable from the argv.
	backend := gfy.ArgvBackend(e.Argv)
	j.Env = gfy.ClaudeAccountEnv(
		gfy.ClaudeCLIEnv(gfy.LocalEnv(gfy.JobEnv(st.Overlay(e.Repo)), backend), backend),
		account)

	j.Held = e.Held || j.Cost == gfy.Metered || j.Local
	if status == jobs.Running {
		// It was in flight when the session ended. Say so in its own log,
		// where somebody looking at the restored row will find it, and hold it
		// whatever it costs — re-running a half-finished mutating job is a
		// decision, not a default.
		j.Log.WriteString("\nggraphify: the board closed while this job was running; " +
			"its process group was terminated. Restored as held — start it to run it again.\n")
		j.Held = true
	}
	return j
}

// announceRestored says what came back, once, when the window appears.
//
// A held queue that announced itself nowhere would be a queue somebody only
// finds by opening the Jobs dialog on a hunch — and the whole point of holding
// it is that it is waiting for a person.
func (a *App) announceRestored(restored, held int) {
	if restored == 0 {
		return
	}
	applog.Infof("restored %d job(s) from the previous session, %d held", restored, held)
	if held == 0 {
		return
	}
	a.toastf("%s waiting to be started — open Jobs", plural(held, "job", "jobs"))
}
