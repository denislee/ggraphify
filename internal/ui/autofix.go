package ui

import (
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/dns/ggraphify/internal/applog"
	"github.com/dns/ggraphify/internal/autofix"
	"github.com/dns/ggraphify/internal/board"
	"github.com/dns/ggraphify/internal/discover"
	"github.com/dns/ggraphify/internal/gfy"
	"github.com/dns/ggraphify/internal/graftstate"
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
//   - It repairs all three indexes, but only where repairing is unattended
//     work. graft's own index under <repo>/graft goes stale exactly the way
//     graphify's graph does, and `graft build` fixes it for free — so a stale
//     or unreadable one is rebuilt in the same chain. So does the global
//     graph's copy of a repository, which `graphify global add` re-merges for
//     free. It also builds a first graft index where there is none — in
//     board rows and in their linked worktrees, which the board hides but an
//     agent works in — unless switched off, since that writes graft/ into the
//     checkout and appends to its .gitignore. What the loop will not do is
//     run graft's --deep pass (that is the deep
//     sweep, with its own gate), or JOIN a repository to the global graph
//     (which repositories belong there is a judgement, not a defect).
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

	// The fleet pass runs alongside the repository one rather than inside it:
	// its subjects are directories and deleted members, neither of which is a
	// candidate, and it has its own in-flight accounting.
	a.fleetTick(set, a.allRows())

	cands := a.autoFixCandidates()
	owners := worktreeOwners(a.allRows())
	if len(cands) == 0 && len(owners) == 0 {
		return
	}

	a.autofixBusy = true
	go func() {
		pol := autoFixPolicy(set)
		if pol.Graft {
			cands = append(cands, a.worktreeCandidates(owners, cands)...)
		}
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
	// Which roots the user nominated as fleets, as a set — the fact that
	// turns "not in the global graph" from a choice into a defect. Resolved
	// once per tick rather than per row: Fleets cleans and expands paths.
	fleets := map[string]bool{}
	for _, root := range a.opts.Store.Settings().Fleets() {
		fleets[root] = true
	}
	// One canonical checkout per origin: extra full clones of the same
	// repository are the same graph, so only the canonical one may enroll.
	var cPaths, cOrigins []string
	for _, r := range rows {
		if fleets[filepath.Dir(r.Path)] && !r.NoGit {
			cPaths = append(cPaths, r.Path)
			cOrigins = append(cOrigins, r.Origin)
		}
	}
	canonical := autofix.CanonicalByOrigin(cPaths, cOrigins)
	nested := containers(rows)
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
			Path:       r.Path,
			Name:       r.Name,
			Graph:      r.Graph,
			Graft:      r.Graft,
			Global:     r.Global,
			Enrollable: fleets[filepath.Dir(r.Path)] && !r.NoGit && canonical[r.Path],
			Excluded:   r.Excluded || nested[r.Path],
			NoGit:      r.NoGit,
			Busy:       busy,
		})
	}
	return out
}

// containers is every row whose directory holds another row — a scratch root
// like ~/tmp that is a checkout of its own (see discover.walkRoot) while
// holding a hundred unrelated ones. The loop leaves such a row alone, as if
// it carried the X flag: its graph is every nested repository extracted over
// again, it drifts whenever any of them changes, and one rebuild of a scratch
// tree ran for most of an hour on 1.8 GB. The Fix button still works on it;
// what goes is only the unattended re-run on every edit underneath.
func containers(rows []board.Row) map[string]bool {
	// With a trailing separator on every path, a directory sorts directly
	// before its descendants — "/a/tmp-x/" < "/a/tmp/" < "/a/tmp/b/" — so a
	// container is exactly a path the next one starts with.
	keys := make([]string, 0, len(rows))
	for _, r := range rows {
		keys = append(keys, filepath.Clean(r.Path)+string(filepath.Separator))
	}
	slices.Sort(keys)
	out := map[string]bool{}
	for i := 0; i+1 < len(keys); i++ {
		if strings.HasPrefix(keys[i+1], keys[i]) {
			out[strings.TrimSuffix(keys[i], string(filepath.Separator))] = true
		}
	}
	return out
}

// worktreeOwner is one board row whose linked worktrees the loop keeps graft
// indexes for, as the main thread can see it before any file is read.
type worktreeOwner struct {
	gitDir string
	name   string
}

// worktreeOwners is every main checkout on the board whose worktrees are in
// scope. Main thread: it reads the row model. A row kept out of batch actions
// keeps its worktrees out too — the X flag is about the repository.
func worktreeOwners(rows []board.Row) []worktreeOwner {
	var out []worktreeOwner
	for _, r := range rows {
		if r.NoGit || r.IsWorktree || r.Excluded || r.GitDir == "" {
			continue
		}
		out = append(out, worktreeOwner{gitDir: r.GitDir, name: r.Name})
	}
	return out
}

// worktreeCandidates is every linked worktree of the owners that is not
// already a row, as a graft-only candidate. Off the main thread: it reads
// each repository's worktree registry and each worktree's graft/.
//
// The board hides worktrees by default (see discover.Options.ShowWorktrees)
// and that is right for graphify — a worktree is the same repository on
// another branch, and extracting it pays twice for one codebase. graft's index
// is the opposite case: free to build and per working tree, and a worktree is
// exactly where an agent is working. So the loop finds them through git's own
// registry, wherever they live, and keeps their graft index and nothing else.
func (a *App) worktreeCandidates(owners []worktreeOwner, boarded []autofix.Candidate) []autofix.Candidate {
	have := make(map[string]bool, len(boarded))
	for _, c := range boarded {
		have[c.Path] = true
	}
	var out []autofix.Candidate
	for _, o := range owners {
		for _, wt := range discover.Worktrees(o.gitDir) {
			if have[wt] {
				continue // ShowWorktrees is on and it is a row of its own
			}
			have[wt] = true
			gr, err := a.grafts.Read(graftstate.Options{Repo: wt})
			if err != nil {
				gr.State = graftstate.StateBroken
				gr.Err = err.Error()
			}
			_, busy := a.runner.Busy(wt)
			out = append(out, autofix.Candidate{
				Path:      wt,
				Name:      o.name + "@" + filepath.Base(wt),
				Graft:     gr,
				GraftOnly: true,
				Busy:      busy,
			})
		}
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
		// First builds — rows and their linked worktrees with no graft/ at
		// all. A preference on top of the machine fact: it writes into the
		// working tree.
		pol.GraftCreate = set.AutoFixGraftCreate()
	} else {
		applog.Debugf("auto-fix: graft indexes left alone — %s", why)
	}

	// The third index. Unlike graft this is a preference and nothing else:
	// `graphify global add` is the same binary every other step runs, so there
	// is no machine fact to resolve — only whether the user wants the loop
	// touching the set of repositories they assembled by hand.
	pol.Global = set.AutoFixGlobal()

	// The membership half, and the workspace federations that go with it.
	// Also a preference and nothing else — but a narrower one than it looks,
	// because store.Settings.Fleets bounds what it can ever touch to the
	// roots the user nominated.
	pol.Enroll = set.AutoFixEnroll()

	if set.AutoFixFollowPins {
		// The pins are the answer, so there is no fallback to resolve — only
		// whether they are free. When they all are, the loop may run the full
		// plan on them; when one bills, the metered switch decides alone.
		pol.Pins = true
		backend, model, ok, why := autoFixPinsLocal(set)
		if !ok {
			if pol.Metered {
				applog.Debugf("auto-fix: following the work pins — %s; metered fixes are on, so they run as set", why)
			} else {
				applog.Debugf("auto-fix: following the work pins — %s; metered fixes are off, so free steps only", why)
			}
			return pol
		}
		pol.Local = true
		pol.LocalBackend = backend
		pol.LocalModel = model
		return pol
	}

	if !set.AutoFixLocal() {
		return pol
	}

	backend, preferred := autoFixLocalPin(set)
	model, ok, why := gfy.AutoLocalModel(backend, preferred)
	if !ok {
		// Not an error and not a toast: on a machine that has never run a
		// local model this is the ordinary state of the world, every tick,
		// forever. The loop carries on with the free plan.
		if pol.Metered {
			applog.Debugf("auto-fix: no local model available (%s) — LLM steps run on their own pins", why)
		} else {
			applog.Debugf("auto-fix: no local model available (%s) — free steps only", why)
		}
		return pol
	}
	pol.Local = true
	pol.LocalBackend = backend
	pol.LocalModel = model
	return pol
}

// autoFixLocalPin is the local backend the loop falls back to, and the model it
// would prefer there. It reads the Heavy work pin, not the general Backend: the
// LLM step the loop exists to run is the full extraction, and Heavy work is
// where the user said extractions go. When that pin is already a local one, it
// and its model are the answer; when it is claude-cli or an API, its model is
// an id ollama has never heard of, so the loop asks for the server's own
// default instead. gfy.AutoLocalModel resolves both cases and refuses rather
// than guessing when there is nothing usable.
func autoFixLocalPin(set store.Settings) (backend, preferred string) {
	b, m := set.BackendFor(gfy.Heavy)
	if eff := gfy.EffectiveBackend(b); gfy.IsLocalBackend(eff) {
		return eff, m
	}
	return gfy.OllamaBackend, ""
}

// autoFixPinsLocal reports whether both work pins are free right now: each
// resolves to a backend on this machine, the server answers, and it serves the
// model the pin names (a blank model is whatever graphify defaults to). It
// does not substitute — following the pins means a pinned model that is not
// there is a reason to stay on the free plan, not a cue to pick another one.
// backend and model are the Heavy pin's, for Policy.LocalBackend/LocalModel.
//
// Off the main thread: it probes.
func autoFixPinsLocal(set store.Settings) (backend, model string, ok bool, why string) {
	for _, w := range []struct {
		weight gfy.Weight
		name   string
	}{{gfy.Heavy, "Heavy work"}, {gfy.Light, "Light work"}} {
		b, m := set.BackendFor(w.weight)
		eff := gfy.EffectiveBackend(b)
		if !gfy.IsLocalBackend(eff) {
			return "", "", false, w.name + " is on " + eff + ", which bills"
		}
		got, up, why := gfy.AutoLocalModel(eff, m)
		if !up {
			return "", "", false, w.name + ": " + why
		}
		if m = strings.TrimSpace(m); m != "" && got != m {
			return "", "", false, w.name + ": " + eff + " does not serve " + m
		}
		if w.weight == gfy.Heavy {
			backend, model = eff, m
		}
	}
	return backend, model, true, ""
}

// autoFixToLocal reports whether one metered step, whose own pin resolved to
// pinned, must be moved onto the policy's local model.
//
// The pin wins whenever the loop may use it. A local pin is free, so it runs
// as configured; a billed one — claude-cli, an API — runs as configured once
// the user has allowed metered fixes. Only a billed pin with metering off is
// rerouted, because the alternative is the loop spending without a click.
func autoFixToLocal(pinned string, act autofix.Action, pol autofix.Policy) bool {
	return act.Local && !pol.Metered && !gfy.IsLocalBackend(pinned)
}

// runAutoFix queues one chain per repository, exactly as the Fix button does —
// one chain each rather than one across all of them, so a failure on one
// repository does not stop the others.
func (a *App) runAutoFix(actions []autofix.Action, pol autofix.Policy) {
	anyBilled := false
	var where []string
	for _, act := range actions {
		act := act
		if act.GraftOnly {
			a.runGraftOnly(act)
			continue
		}
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
		global := false
		// Where the LLM steps actually went, after the reroute — for the log
		// line and the toast, which used to name the local model even for a
		// step that ran on the Heavy work pin.
		var llm []string
		billed := false
		for n, s := range act.Plan.Steps {
			p := a.paramsFor(s.Kind, *row)
			s.Apply(&p)
			if gfy.Global(s.Kind) {
				// The same tag the Global screen would have sent. Without it
				// `global add` would merge this graph under a name of its own
				// choosing and the manifest would grow a second entry for a
				// repository that already has one.
				p.Tag = globalTag(*row)
				global = true
			}
			if s.Cost() == gfy.Metered {
				if autoFixToLocal(p.Backend, act, pol) {
					// The step that would have been a bill is a local run
					// instead. The sizing has to be redone after the backend
					// moves — a.params sized the chunk and the timeout for
					// whatever backend was configured, and against a local
					// server those two numbers are the difference between a
					// graph and six hours of truncated prose.
					p.Backend = pol.LocalBackend
					p.Model = pol.LocalModel
					gfy.ApplyLocalSizing(&p)
				}
				where := p.Backend
				if p.Model != "" {
					where += "/" + p.Model
				}
				if !slices.Contains(llm, where) {
					llm = append(llm, where)
				}
				if !gfy.IsLocalBackend(p.Backend) {
					billed = true
				}
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
			row.Name, act.Attempt, act.Sig, autoFixPlanLine(act, llm, billed))
		if billed {
			anyBilled = true
		}
		for _, w := range llm {
			if !slices.Contains(where, w) {
				where = append(where, w)
			}
		}
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
				if global {
					// The memo is keyed on the manifest's mtime and size, so
					// it would catch up on its own — but not before the
					// refresh below reads it, which is how a re-added
					// repository keeps its stale chip for one more tick.
					a.globals.Invalidate()
					if a.globalPane != nil {
						a.globalPane.reload()
					}
				}
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
	case anyBilled:
		how = "metered — " + strings.Join(where, ", ")
	case len(where) > 0:
		how = "local model " + strings.Join(where, ", ")
	}
	a.toastf("auto-fix: %s (%s)", strings.Join(names, ", "), how)
	a.refreshStatus()
}

// runGraftOnly queues the lone graft-build of a checkout with no board row — a
// linked worktree. There is no row to size params from, and none is needed:
// graft build takes the path and nothing else.
func (a *App) runGraftOnly(act autofix.Action) {
	steps := make([]jobs.ChainStep, 0, len(act.Plan.Steps))
	for _, s := range act.Plan.Steps {
		p := gfy.Params{Repo: act.Path}
		s.Apply(&p)
		step := jobs.ChainStep{
			Kind:   s.Kind,
			Repo:   act.Path,
			Label:  "Auto-fix · " + gfy.Title(s.Kind) + " · " + act.Name,
			Params: p,
		}
		if a.opts.Store != nil {
			step.Env = a.opts.Store.Overlay(act.Path)
		}
		steps = append(steps, step)
	}
	applog.Infof("auto-fix %s (attempt %d): %s — %s (free)",
		act.Name, act.Attempt, act.Sig, board.Tilde(act.Path))
	a.runner.SubmitChain(steps, func(res jobs.ChainResult) {
		errText := ""
		if res.Err != nil {
			errText = res.Err.Error()
		}
		// No row will rescan this path and invalidate its cache entry, so the
		// next tick would read the index as it was before the build.
		a.grafts.Invalidate(act.Path)
		a.autofix.Done(act, errText)
		idle(func() {
			if res.Err != nil {
				applog.Errorf("auto-fix %s: %v", act.Name, res.Err)
			} else {
				applog.Infof("auto-fix %s: graft index built", act.Name)
			}
		})
	})
}

// autoFixPlanLine is the plan in one log line: the commands, in order, and
// where the LLM steps are going — llm is each backend/model they resolved to,
// and billed says at least one of them is not on this machine.
func autoFixPlanLine(act autofix.Action, llm []string, billed bool) string {
	kinds := make([]string, 0, len(act.Plan.Steps))
	for _, s := range act.Plan.Steps {
		kinds = append(kinds, s.Kind)
	}
	line := strings.Join(kinds, " → ")
	switch {
	case !act.Plan.Metered() || len(llm) == 0:
		return line + " (free)"
	case billed:
		return line + " (LLM steps on " + strings.Join(llm, ", ") + ", METERED)"
	default:
		return line + " (LLM steps on " + strings.Join(llm, ", ") + ", free)"
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
