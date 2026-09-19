package ui

import (
	"strings"
	"time"

	"github.com/dns/ggraphify/internal/applog"
	"github.com/dns/ggraphify/internal/autofix"
	"github.com/dns/ggraphify/internal/board"
	"github.com/dns/ggraphify/internal/gfy"
	"github.com/dns/ggraphify/internal/jobs"
	"github.com/dns/ggraphify/internal/store"
)

// The auto-fix loop: the board repairs its own repositories.
//
// It is the Fix button with the click taken out, and nothing else. The plan
// still comes from internal/heal, the decision of which repositories to touch
// from internal/autofix, and every command it queues is one the Fix dialog
// would have printed before running. What this file does is the join: gather
// the board's rows, ask the engine, and turn its answers into chains.
//
// Three rules make it safe to have on by default, and they are the only three
// worth remembering:
//
//   - It never spends money on its own. The LLM steps — a full extraction,
//     naming communities — run against a model on THIS machine, which costs
//     time and nothing else. If no local server answers, the loop falls back
//     to the free plan (AST rebuild, clustering, report) and leaves the
//     semantic half undone rather than reaching for a billed backend.
//   - It repairs both indexes, but only where repairing is unattended work.
//     graft's own index under <repo>/graft goes stale exactly the way
//     graphify's graph does, and `graft build` fixes it for free — so a stale
//     or unreadable one is rebuilt in the same chain. What the loop will not
//     do is BUILD a graft index where there has never been one (that writes a
//     directory into somebody's checkout and appends to its .gitignore) or
//     run graft's --deep pass (that is the deep sweep, with its own gate).
//   - It gives up. A repository whose defects survive the fix is tried a
//     bounded number of times and then left alone, with one line in the log
//     saying so. A loop that retried forever would be a machine that is busy
//     every night and a graph that is wrong every morning.
//
// Spending IS reachable — one switch in settings, off by default — because a
// board whose only backend is a billed one is otherwise a board where this
// feature does the free half and stops.

// autoFixTick is called from setRows, on the main thread, with the freshest
// scan the board has.
//
// The work is split across the thread boundary on purpose. Gathering rows is a
// main-thread read of the model; deciding is a pure function; but resolving
// whether a local model server is up is an HTTP call, and doing that on the
// GTK thread would freeze the window for as long as a hung server takes to
// time out. So: gather here, decide on a worker, submit back here.
func (a *App) autoFixTick() {
	if a.opts.Store == nil || a.runner == nil || a.autofix == nil {
		return
	}
	set := a.opts.Store.Settings()
	if !set.AutoFix() {
		return
	}
	if a.autofixBusy {
		// The previous tick's probe has not come back. Skipping is right: the
		// next scan is 30 seconds away and nothing is lost by waiting for it.
		return
	}

	cands := a.autoFixCandidates()
	if len(cands) == 0 {
		return
	}

	a.autofixBusy = true
	go func() {
		pol := autoFixPolicy(set)
		actions, skips := a.autofix.Plan(cands, pol)
		stuck := a.autofix.Declare(cands, pol)
		idle(func() {
			a.autofixBusy = false
			for _, s := range stuck {
				a.onAutoFixStuck(s)
			}
			if len(actions) == 0 {
				logAutoFixSkips(skips)
				return
			}
			a.runAutoFix(actions, pol)
		})
	}()
}

// autoFixCandidates is every repository on the board as the engine needs to
// see it. Main thread: it reads the row model and the job table.
//
// It is the whole board rather than the visible listing, for the same reason
// the board-wide free sweep is: a loop that quietly skipped whatever the
// search box happened to hide would repair a different set of repositories
// depending on what was typed in a text field.
func (a *App) autoFixCandidates() []autofix.Candidate {
	rows := a.allRows()
	out := make([]autofix.Candidate, 0, len(rows))
	for _, r := range rows {
		job := a.jobFor(r.Path)
		busy := job != nil && !job.Status.Done()
		if !busy {
			_, busy = a.runner.Busy(r.Path)
		}
		if !busy {
			// A rescan whose job is finished but whose settlement is not: the
			// drift walk and the commit restamp run off the main thread after
			// the job ends, and until they land this row still describes the
			// repository as it was before the fix. Planning or giving up from
			// that row is deciding on stale evidence.
			busy = a.settling[r.Path]
		}
		out = append(out, autofix.Candidate{
			Path:     r.Path,
			Name:     r.Name,
			Graph:    r.Graph,
			Graft:    r.Graft,
			Excluded: r.Excluded,
			Busy:     busy,
		})
	}
	return out
}

// autoFixPolicy turns the settings into a policy, resolving the one part that
// is not a preference: whether this machine can actually serve a model right
// now, and which one it would serve.
//
// Called off the main thread — ProbeLocal inside gfy.AutoLocalModel does
// network I/O.
func autoFixPolicy(set store.Settings) autofix.Policy {
	pol := autofix.Policy{
		Enabled:  set.AutoFix(),
		Metered:  set.AutoFixMetered,
		Max:      set.AutoFixMax,
		Cooldown: time.Duration(set.AutoFixCooldown) * time.Second,
		Attempts: set.AutoFixAttempts,
	}
	// The other index. Whether graft can be run at all is a fact about this
	// machine — a PATH lookup, not a preference — and it has to be resolved
	// before the engine plans, because a loop that queued `graft build` on a
	// machine without graft would fail once per stale checkout per cooldown
	// and give up on each of them for a reason that has nothing to do with the
	// checkout.
	if ok, why := gfy.GraftReady(); ok {
		pol.Graft = true
	} else {
		applog.Debugf("auto-fix: graft indexes left alone — %s", why)
	}

	if !set.AutoFixLocal() {
		return pol
	}

	// Which local backend, and with which model. When the board's backend is
	// already a local one, its own model setting is the preference; when it is
	// claude-cli or an API, the model setting names a model that server has
	// never heard of, so the loop asks for the server's own default instead.
	// gfy.AutoLocalModel resolves both cases and refuses rather than guessing
	// when there is nothing usable.
	eff := gfy.EffectiveBackend(set.Backend)
	backend, preferred := eff, set.Model
	if !gfy.IsLocalBackend(eff) {
		backend, preferred = gfy.OllamaBackend, ""
	}
	model, ok, why := gfy.AutoLocalModel(backend, preferred)
	if !ok {
		// Not an error and not a toast: on a machine that has never run a
		// local model this is the ordinary state of the world, every tick,
		// forever. The loop carries on with the free plan.
		applog.Debugf("auto-fix: no local model available (%s) — free steps only", why)
		return pol
	}
	pol.Local = true
	pol.LocalBackend = backend
	pol.LocalModel = model
	return pol
}

// runAutoFix queues one chain per repository, exactly as the Fix button does —
// one chain each rather than one across all of them, so a failure on one
// repository does not stop the others.
func (a *App) runAutoFix(actions []autofix.Action, pol autofix.Policy) {
	for _, act := range actions {
		act := act
		row := a.row(act.Path)
		if row == nil {
			// It left the board between the scan and here. Release the slot
			// the engine reserved for it, or the loop leaks one per vanished
			// checkout until the board restarts.
			a.autofix.Done(act, "repository is no longer on the board")
			continue
		}

		total := len(act.Plan.Steps)
		steps := make([]jobs.ChainStep, 0, total)
		for n, s := range act.Plan.Steps {
			p := a.params(*row)
			s.Apply(&p)
			if act.Local && s.Cost() == gfy.Metered {
				// The whole point of the loop: the step that would have been
				// a bill is a local run instead. The sizing has to be redone
				// after the backend moves — a.params sized the chunk and the
				// timeout for whatever backend was configured, and against a
				// local server those two numbers are the difference between a
				// graph and six hours of truncated prose.
				p.Backend = pol.LocalBackend
				p.Model = pol.LocalModel
				gfy.ApplyLocalSizing(&p)
			}
			steps = append(steps, jobs.ChainStep{
				Kind: s.Kind,
				Repo: row.Path,
				Label: "Auto-fix " + gfy.Itoa(n+1) + "/" + gfy.Itoa(total) + " · " +
					gfy.Title(s.Kind) + " · " + row.Name,
				Params: p,
				Env:    a.opts.Store.Overlay(row.Path),
			})
		}

		applog.Infof("auto-fix %s (attempt %d): %s — %s",
			row.Name, act.Attempt, act.Sig, autoFixPlanLine(act, pol))
		name := row.Name
		a.runner.SubmitChain(steps, func(res jobs.ChainResult) {
			// The runner calls this on a worker goroutine. The engine is safe
			// there; everything else here is GTK and is not.
			errText := ""
			if res.Err != nil {
				errText = res.Err.Error()
			}
			a.autofix.Done(act, errText)
			idle(func() {
				if res.Err != nil {
					applog.Errorf("auto-fix %s: stopped after %d/%d — %v",
						name, res.Ran, res.Total, res.Err)
					a.toastf("auto-fix %s: stopped after %d/%d — %v",
						name, res.Ran, res.Total, res.Err)
				} else {
					applog.Infof("auto-fix %s: %d/%d done", name, res.Ran, res.Total)
				}
				a.refresh(false)
			})
		})
	}

	// One toast for the tick, not one per repository: this is background work
	// and the board should say so once.
	names := make([]string, 0, len(actions))
	for _, act := range actions {
		names = append(names, act.Name)
	}
	how := "free steps"
	switch {
	case pol.Spends():
		how = "metered — billed backend"
	case pol.Local:
		how = "local model " + pol.LocalModel
	}
	a.toastf("auto-fix: %s (%s)", strings.Join(names, ", "), how)
	a.refreshStatus()
}

// autoFixPlanLine is the plan in one log line: the commands, in order, and
// where the LLM steps are going.
func autoFixPlanLine(act autofix.Action, pol autofix.Policy) string {
	kinds := make([]string, 0, len(act.Plan.Steps))
	for _, s := range act.Plan.Steps {
		kinds = append(kinds, s.Kind)
	}
	line := strings.Join(kinds, " → ")
	switch {
	case !act.Plan.Metered():
		return line + " (free)"
	case act.Local:
		return line + " (LLM steps on " + pol.LocalBackend + "/" + pol.LocalModel + ", free)"
	default:
		return line + " (METERED)"
	}
}

// onAutoFixStuck reports a repository the loop has given up on — once, in the
// log, and as a toast, because the alternative is a board that silently stops
// repairing one checkout and never mentions it.
func (a *App) onAutoFixStuck(s autofix.Stuck) {
	// The engine's own sentence, not one composed here: for behind-HEAD "the
	// same defects came back" named the wrong obstacle, and "1 attempts" read
	// as a loop that had barely tried.
	why := s.Why
	if s.Err != "" {
		why = s.Err
	}
	applog.Errorf("auto-fix %s: giving up on %s — %s. "+
		"Fix it by hand, or select it and press Fix; the loop starts over for it "+
		"as soon as its defects change.", s.Name, s.Sig, why)
	a.toastf("auto-fix gave up on %s — %s", s.Name, why)
}

// logAutoFixSkips says why a tick did nothing, at debug level. It is the
// difference between a loop that is off and a loop that is waiting, and that
// question is asked of the log pane rather than of a dialog.
func logAutoFixSkips(skips []autofix.Skip) {
	for _, s := range skips {
		applog.Debugf("auto-fix skip %s: %s", board.Tilde(s.Path), s.Why)
	}
}
