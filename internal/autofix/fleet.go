package autofix

import (
	"sort"
	"strings"
	"time"

	"github.com/dns/ggraphify/internal/globalgraph"
	"github.com/dns/ggraphify/internal/heal"
	"github.com/dns/ggraphify/internal/workspace"
)

// The fleet pass: the two repairs that are not about any one repository.
//
// Everything in autofix.go decides per checkout, and that is the right shape
// for every defect a checkout can have — its graph, its graft index, its own
// merged copy. Two of the things enrolment makes the loop responsible for do
// not fit it, and forcing them in would put the work on an arbitrary row:
//
//   - graft's workspace federation is ONE file at the root listing every
//     child. A checkout that is missing from it is not a defect of that
//     checkout — the file it is missing from belongs to the directory above,
//     and the command that fixes it (`graft build <root>`) fixes every other
//     missing child at the same time. Attributing it to a row would queue the
//     same whole-root rebuild once per unfederated checkout.
//   - a member whose checkout has been DELETED has no row at all. That is
//     precisely why it needs a pass of its own: the per-candidate loop
//     iterates the board, and the board cannot show a repository that is not
//     there. A stranded member is invisible to every mechanism in this
//     package except this one.
//
// So: one action per root, one per stranded member, planned against the same
// attempt/backoff memory as everything else so a rebuild that never sticks
// gives up like any other repair.

// Root is one nominated fleet directory, as the board sees it.
type Root struct {
	// Path is the directory itself — ~/git, typically.
	Path string
	// Workspace is its graft federation versus what the board found under it.
	Workspace workspace.State
	// Busy is set when a job is already running against this root, which for
	// a workspace rebuild means any job at the root path itself.
	Busy bool
}

// Stranded is one global-graph member whose source graph is gone.
//
// The caller resolves "gone" — this package touches no filesystem — and
// should resolve it strictly. A removal cannot be undone without re-extracting
// the repository it dropped, so a graph that is merely unreachable (an
// unmounted disk, a checkout mid-move) must not arrive here.
type Stranded struct {
	// Tag is the name the member was added under, which is the only handle
	// `graphify global remove` accepts.
	Tag string
	// Source is the graph.json it was merged from, for the log line: the
	// whole claim being made is that this path is not there.
	Source string
}

// FleetAction is one root-level repair the loop has decided to run.
//
// It is separate from Action rather than a variant of it because the two are
// addressed differently and the runner has to know which it has: an Action is
// a chain of steps against a repository row, with that row's params, output
// directory and environment overlay. A FleetAction names no row — its Path is
// a directory that is deliberately not a checkout, and its Tag names a
// repository that may no longer exist on this machine at all.
type FleetAction struct {
	// Path is the root this is about. For a prune it is the root the member
	// was nominated under, which is where the log line points; the command
	// itself touches only ~/.graphify.
	Path string
	// Step is the single command to run. Fleet repairs are never chained:
	// each one is independently idempotent and none of them is a
	// precondition for another.
	Step heal.Step
	// Tag is the member to remove, on a prune. Empty otherwise.
	Tag string
	// Sig is what this action answers, and what comes back to FleetDone.
	Sig string
	// Attempt is which try this is, 1-based, against Sig.
	Attempt int
	// Why is the one-line reason, for the log and the toast.
	Why string
}

// fleetKey is the engine-memory key for a fleet action. It is namespaced away
// from repository paths on purpose: a root can also be a checkout (a fleet
// directory that is itself a git repository is unusual but legal), and its
// workspace rebuild must keep an attempt budget of its own rather than
// sharing one with that checkout's graph repairs.
func fleetKey(kind, path, tag string) string {
	if tag != "" {
		return "fleet:" + kind + ":" + tag
	}
	return "fleet:" + kind + ":" + path
}

// ParseFleetKey splits an engine-memory key made by fleetKey into its kind
// ("workspace", whose subject is a root path, or "prune", whose subject is a
// member tag). ok is false for a repository key, which is a bare path.
func ParseFleetKey(key string) (kind, subject string, ok bool) {
	rest, isFleet := strings.CutPrefix(key, "fleet:")
	if !isFleet {
		return "", "", false
	}
	kind, subject, ok = strings.Cut(rest, ":")
	return kind, subject, ok
}

// PlanFleet chooses which root-level repairs to run now, and marks them in
// flight, exactly as Plan does for repositories. Call FleetDone for each
// action returned, or the slot it reserved is never released.
//
// It shares Max with Plan by design: the cap is on how much this loop is
// doing at once, and a whole-root `graft build` is the single most expensive
// free thing it can queue. A tick that has already filled the budget with
// repository repairs does no fleet work, and the next one picks it up.
func (e *Engine) PlanFleet(roots []Root, stranded []Stranded, pol Policy) (actions []FleetAction, skips []Skip) {
	pol = pol.withDefaults()
	if !pol.Enabled || !pol.Enroll {
		return nil, nil
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	now := e.now()

	inflight := 0
	for _, r := range e.seen {
		if r.running {
			inflight++
		}
	}

	// Workspace federations, one action per root. Gated on graft being
	// runnable for the same reason every other graft step is: the repair is
	// `graft build`, and queueing it on a machine without graft would fail
	// once per root per cooldown for a reason that has nothing to do with the
	// root.
	if pol.Graft {
		for _, r := range roots {
			if inflight >= pol.Max {
				break
			}
			sig := r.Workspace.Issue()
			key := fleetKey("workspace", r.Path, "")
			if sig == "" {
				delete(e.seen, key)
				continue
			}
			if r.Busy {
				skips = append(skips, Skip{r.Path, rootName(r.Path),
					"a job is already running at this root"})
				continue
			}
			step, ok := heal.WorkspaceStep(r.Workspace)
			if !ok {
				continue
			}
			// The signature carries the drift itself, not just its code: a
			// rebuild that federated four of five new checkouts made real
			// progress, and a bare "workspace-drift" would read as the
			// identical complaint coming back and spend an attempt on it.
			sig = sig + ":" + r.Workspace.Summary()
			rec, ok := e.reserve(key, sig, now, pol, &skips, Skip{r.Path, rootName(r.Path), ""})
			if !ok {
				continue
			}
			inflight++
			actions = append(actions, FleetAction{
				Path: r.Path, Step: step, Sig: sig, Attempt: rec.attempts,
				Why: step.Why,
			})
		}
	}

	// Stranded members, one action each. Not gated on graft — this is
	// graphify's file — and not gated on Global either: Global governs
	// re-merging members that are still there, and a member whose source is
	// gone can never be re-merged at all.
	sort.Slice(stranded, func(i, j int) bool { return stranded[i].Tag < stranded[j].Tag })
	for _, s := range stranded {
		if inflight >= pol.Max {
			break
		}
		if strings.TrimSpace(s.Tag) == "" {
			continue
		}
		key := fleetKey("prune", "", s.Tag)
		sig := globalgraph.IssueStranded
		step := heal.PruneStep(s.Tag)
		rec, ok := e.reserve(key, sig, now, pol, &skips, Skip{s.Source, s.Tag, ""})
		if !ok {
			continue
		}
		inflight++
		actions = append(actions, FleetAction{
			Path: s.Source, Step: step, Tag: s.Tag, Sig: sig,
			Attempt: rec.attempts, Why: step.Why,
		})
	}
	return actions, skips
}

// reserve is the attempt/cooldown/backoff bookkeeping Plan does inline,
// factored out because the fleet pass needs exactly the same rules and a
// second copy of them would be a second place for them to drift.
//
// The caller must hold e.mu. tmpl carries the Path/Name a skip line should be
// reported under; its Why is filled in here.
func (e *Engine) reserve(key, sig string, now time.Time, pol Policy, skips *[]Skip, tmpl Skip) (*record, bool) {
	rec := e.seen[key]
	switch {
	case rec == nil:
		rec = &record{}
		e.seen[key] = rec
	case rec.running:
		return nil, false
	case rec.sig != sig:
		rec.attempts, rec.err, rec.declared = 0, "", false
	case rec.attempts >= attemptCap(pol, sig):
		if rec.declared {
			return nil, false // already said once; see Plan's give-up branch
		}
		// Nothing declares a fleet record stuck — Declare walks repository
		// candidates, and a workspace or a stranded member is neither — so
		// the give-up line is said here, once, and the flag is what remembers
		// it. A fresh signature clears it again above.
		rec.declared = true
		tmpl.Why = gaveUp(sig, rec.attempts)
		*skips = append(*skips, tmpl)
		return nil, false
	case now.Sub(rec.last) < backoff(pol.Cooldown, rec.attempts):
		tmpl.Why = "cooling down"
		*skips = append(*skips, tmpl)
		return nil, false
	}
	rec.sig = sig
	rec.attempts++
	rec.last = now
	rec.running = true
	return rec, true
}

// FleetDone releases the slot a FleetAction reserved, and records whether it
// achieved anything — the same contract Done has for repositories.
func (e *Engine) FleetDone(a FleetAction, err string) {
	kind := "workspace"
	if a.Tag != "" {
		kind = "prune"
	}
	key := fleetKey(kind, a.Path, a.Tag)

	e.mu.Lock()
	defer e.mu.Unlock()
	rec := e.seen[key]
	if rec == nil {
		return
	}
	// The record is kept even when the repair ran clean. A clean exit is not
	// the drift going away — `graft build` exits 0 on a federation it rewrote
	// identically — and forgetting the record here reset both the attempt
	// count and the cooldown, so the next tick re-queued the whole-root
	// rebuild at once, forever. PlanFleet drops the record itself on the
	// first tick that reads the subject healthy; one that comes back with the
	// same signature starts from the attempt it left off at.
	rec.running = false
	rec.err = err
}

// rootName is the directory name a root is reported under, so a log line says
// "git" rather than the whole path.
func rootName(path string) string {
	path = strings.TrimRight(path, "/")
	if i := strings.LastIndex(path, "/"); i >= 0 && i+1 < len(path) {
		return path[i+1:]
	}
	if path == "" {
		return "fleet"
	}
	return path
}
