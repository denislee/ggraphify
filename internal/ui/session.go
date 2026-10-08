package ui

import (
	"errors"
	"os"
	"strings"
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
// Three rules shape what comes back:
//
//   - A job that was RUNNING is not restored as running. Its process group
//     died with the last session, and its output directory is in whatever
//     state graphify left it. It returns to the queue, and — unless it was
//     paused or it spends money — it starts again on its own: see
//     resumeInterrupted for why that is the right reading of a window that
//     closed over a half-finished extraction.
//   - A job that was merely QUEUED and costs something returns held. Free work
//     — AST updates, clustering, exports — starts on its own, because that is
//     what the previous session had already decided and it costs nothing to be
//     wrong about. Metered work is an LLM bill and local work is hours of this
//     machine; both wait for the click that the confirm dialog stands for.
//   - Nothing resumes mid-way. graphify has no checkpoint, so a resumed job
//     runs its command again from the top; what survives the restart is the
//     decision to run it, not the work it had done.

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
	// A finished job's entry cannot change any more, so it is built once and
	// reused: this runs several times per job, and rebuilding every retired
	// job's entry each time was a TailBytes copy of up to 8 KB apiece. The
	// cache is rebuilt from what is still listed, so it never outlives the
	// runner's own history.
	cache := make(map[uint64]store.JobEntry, len(snaps))
	for _, s := range snaps {
		if s.Status.Done() {
			if e, ok := a.jobEntries[s.ID]; ok {
				cache[s.ID] = e
				list = append(list, e)
				continue
			}
		}
		e := store.JobEntry{
			Kind: s.Kind, Repo: s.Repo, Label: s.Label, Argv: s.Argv,
			Cost: s.Cost.String(), Local: s.Local, Dir: s.Dir, Out: s.Out,
			Status: s.Status.String(), Held: s.Held, Paused: s.Paused, Exit: s.Exit,
			Queued: s.Queued, Started: s.Started, Ended: s.Ended, Error: s.Err,
		}
		if s.Log != nil {
			e.Log = s.Log.TailBytes(store.MaxLogTail)
		}
		if s.Status.Done() {
			cache[s.ID] = e
		}
		list = append(list, e)
	}
	a.jobEntries = cache
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
func (a *App) restoreJobs() (restored, held, resumed int) {
	if a.opts.Store == nil || a.runner == nil {
		return 0, 0, 0
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
		// What the board is about to restart by itself, which is the one thing
		// about a restored queue somebody would want told rather than
		// discovered: a job that was in flight when the window closed and is
		// not waiting for a click.
		if was, _ := jobs.ParseStatus(e.Status); was == jobs.Running && !j.Status.Done() && !j.Held {
			if err := localServerCheck(a.opts.Store)(j); err != nil {
				// Resuming it now would be twelve connection errors and a
				// partial graph. Hold it instead, with the refusal in its own
				// log — the same verdict the submit path gives, arriving here
				// because Restore deliberately bypasses the prechecks.
				j.Held = true
				j.Log.WriteString("ggraphify: not resumed yet — " + err.Error() + "\n")
			} else {
				resumed++
			}
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

	return restored, held, resumed
}

// localServerCheck is the one precheck a resumed job still has to pass: the
// model server it needs has to be answering, or be something this board can
// start. It is the same check activate installs on the runner, asked here
// because Restore bypasses the prechecks by design — every restored job
// already passed them once, and re-running them against a repository that has
// changed would silently drop work. A local server that has since been turned
// off is the exception: it is not a fact about the job, it is a fact about
// this minute, and it is the difference between resuming and failing red.
func localServerCheck(st *store.Store) func(*jobs.Job) error {
	return jobs.RequireLocalServer(func() bool { return st.Settings().AutoOllama() })
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
	// The cost is re-derived from the kind and where it ran, which is what
	// SubmitCmd does, and then widened by what the file says. The two can only disagree if a kind
	// changed its price between releases, and the safe direction to resolve
	// that is the expensive one.
	j.Cost = gfy.CostForLocal(e.Kind, e.Local)
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
		gfy.ClaudeCLIModelEnv(
			gfy.ClaudeCLIEnv(gfy.LocalEnv(gfy.JobEnv(st.Overlay(e.Repo)), backend), backend),
			backend, gfy.ArgvModel(e.Argv)),
		account)

	j.Held = e.Held || j.Cost == gfy.Metered || j.Local
	if status == jobs.Running {
		resumeInterrupted(j, e)
	}
	return j
}

// resumeInterrupted decides what happens to a job the last session ended in
// the middle of, and writes the reason into the job's own log.
//
// A job that was RUNNING is work somebody started and never got an answer
// about. The board used to bring every one of them back held, which is the
// safe reading of "we do not know how far it got" — but it is the wrong one
// for the case that actually happens: a nineteen-chunk local extraction closed
// at chunk four, reopened, and sitting in the queue waiting for a click that
// the person who started it already gave. So an interrupted job resumes on its
// own, and the exceptions are the two where restarting it is not obviously
// what was wanted:
//
//   - A PAUSED job stays held. It was stopped by hand, and a restart is not
//     consent to start it again — it is the one interrupted job whose owner
//     told us, in as many words, that they did not want it running.
//   - A job that spends MONEY stays held. A metered extraction against a
//     vendor is a bill, and a bill is not something a board may re-incur
//     because a window closed. A local run is metered too (see
//     gfy.CostForLocal) but costs nothing but this machine's hours, which the
//     first click already bought — those resume.
//
// There is no checkpoint underneath any of this. graphify has no --resume, so
// "resume" means the same command runs again from the top; what carries over
// is the decision to run it, not the chunks already done. The log line says so
// rather than letting a row that reads "resumed" imply otherwise.
func resumeInterrupted(j *jobs.Job, e store.JobEntry) {
	where := ""
	if _, text, ok := parseProgress(lastProgressLine(e.Log)); ok {
		where = " It had reached " + text + "."
	}

	switch {
	case e.Paused:
		j.Held = true
		j.Log.WriteString("\nggraphify: the board closed while this job was paused; " +
			"its process group was terminated." + where +
			" Restored as held — it was stopped by hand, so start it to run it again.\n")
	case j.Cost == gfy.Metered && !j.Local:
		j.Held = true
		j.Log.WriteString("\nggraphify: the board closed while this job was running; " +
			"its process group was terminated." + where +
			" Restored as held — re-running it would dispatch billed requests again, " +
			"so start it to run it again.\n")
	default:
		j.Held = false
		j.Log.WriteString("\nggraphify: the board closed while this job was running; " +
			"its process group was terminated." + where +
			" Resuming it — the command runs again from the start, " +
			"graphify having no checkpoint to pick up from.\n")
	}
}

// lastProgressLine is the newest line of a restored log tail that carries a
// figure, or "" — the same read jobProgress does on a live job, against the
// text that survived in the sidecar.
func lastProgressLine(log string) string {
	lines := strings.Split(log, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if _, _, ok := parseProgress(lines[i]); ok {
			return lines[i]
		}
	}
	return ""
}

// announceRestored says what came back, once, when the window appears.
//
// A held queue that announced itself nowhere would be a queue somebody only
// finds by opening the Jobs dialog on a hunch — and the whole point of holding
// it is that it is waiting for a person.
func (a *App) announceRestored(restored, held, resumed int) {
	if restored == 0 {
		return
	}
	applog.Infof("restored %d job(s) from the previous session, %d held, %d resumed",
		restored, held, resumed)
	// The resumed jobs are the louder news: those are already running again,
	// on their own, without anybody asking twice. A board that restarted an
	// hours-long extraction in silence would be a board doing the right thing
	// invisibly, which is how it gets cancelled by somebody who thought it was
	// stuck.
	if resumed > 0 {
		a.toastf("resumed %s interrupted by the last session",
			plural(resumed, "job", "jobs"))
	}
	if held == 0 {
		return
	}
	a.toastf("%s waiting to be started — open Jobs", plural(held, "job", "jobs"))
}
