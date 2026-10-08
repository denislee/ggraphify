// Package autofix decides, tick by tick, which repositories the board should
// repair on its own — and stops deciding when repairing them is not working.
//
// It is the loop behind the Fix button rather than a second implementation of
// it: the plan for each repository still comes from internal/heal, and every
// command it queues is one the Fix dialog could have queued by hand. What
// lives here is the part a button does not need — which repositories to touch
// at all, how many at once, how long to wait before trying one again, and when
// to give up on one that keeps coming back with the same defect.
//
// Like heal it is a pure decision: no filesystem, no subprocess, no GTK. The
// only state it keeps is the memory of what it has already tried, which is
// what separates "this repository has drift" from "this repository has had the
// same drift through three fixes and the fourth will not help either".
package autofix

import (
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/dns/ggraphify/internal/gfy"
	"github.com/dns/ggraphify/internal/globalgraph"
	"github.com/dns/ggraphify/internal/graftstate"
	"github.com/dns/ggraphify/internal/graphstate"
	"github.com/dns/ggraphify/internal/heal"
)

// Default bounds. They are deliberately timid: this loop runs without anybody
// watching it, so its failure mode has to be "did less than it could have"
// rather than "kept a machine busy all night".
const (
	// DefaultMax is how many repositories the loop keeps in flight. Two,
	// because the runner's own lanes are the real limiter and this number
	// exists to stop a hundred-checkout board from queueing a hundred chains
	// the moment it starts.
	DefaultMax = 2
	// DefaultCooldown is the minimum wait before the same repository is
	// attempted again. Ten minutes is longer than a free rebuild and shorter
	// than a working day.
	DefaultCooldown = 10 * time.Minute
	// DefaultAttempts is how many times the loop may attack the SAME set of
	// issues before it declares the repository stuck and leaves it alone.
	// Two: once in case the failure was transient, and never again after
	// that, because a third identical run is a third identical failure.
	DefaultAttempts = 2
)

// Policy is what the settings say, resolved against what the machine can
// actually do. The UI builds it; nothing here reads a setting itself.
type Policy struct {
	// Enabled is the master switch.
	Enabled bool

	// Local says the loop may run the LLM steps — full extraction, community
	// naming — against a model on THIS machine, and LocalBackend/LocalModel
	// are the ones it would use. Local is only ever true when the server was
	// actually probed and answered: an unreachable ollama makes this false,
	// and the loop falls back to the free plan rather than queueing a hundred
	// jobs that each fail on connect.
	Local        bool
	LocalBackend string
	LocalModel   string

	// Pins says the LLM steps run on the Heavy and Light work pins as they
	// are configured rather than on LocalBackend/LocalModel. Local then means
	// every pin is a local server that answered with its pinned model, and
	// LocalBackend/LocalModel are the Heavy pin — kept only as the safety net
	// for a per-repository or per-command override that would otherwise bill
	// with metered fixes off.
	Pins bool

	// Metered lets the loop spend money — LLM steps against a billed backend.
	// It has no default: nothing in this package turns it on, and the settings
	// page starts it off. A loop that bills a card without a click would be
	// the one bug in this application nobody could undo.
	Metered bool

	// Graft says the loop may also repair the OTHER index — graft's own graph
	// under <repo>/graft — when the tree has moved under it or it has become
	// unreadable. It is a fact about this machine rather than a preference:
	// true only when the graft CLI is actually runnable here, because a loop
	// that queued `graft build` on a machine without graft would fail once per
	// stale checkout per cooldown, forever.
	//
	// It never runs the deep pass, and builds a first index only under
	// GraftCreate: see graftstate.Index.Repairable for why neither is a repair.
	Graft bool

	// GraftCreate widens Graft from "repair the indexes this machine has" to
	// "every checkout has one": a git checkout with no graft/ at all gets a
	// first `graft build`. Only meaningful with Graft — without the CLI there
	// is nothing to build with. It is a preference, because that build writes
	// graft/ into the working tree and graft's rule into its .gitignore.
	GraftCreate bool

	// Global says the loop may also bring the THIRD index up to date — the
	// cross-repo graph under ~/.graphify — by re-merging a member whose own
	// graph has been rebuilt since it was added. Unlike Graft this is a
	// preference rather than a fact about the machine: the global graph is a
	// thing the user assembled deliberately, and a loop that rewrites it is
	// touching their working set rather than repairing a derived file.
	//
	// It never ADDS a repository that is not already a member on its own:
	// see heal.GlobalStep for why joining is not unattended work. Enroll is
	// the second key that unlocks that half.
	Global bool

	// Enroll widens the third index's half from "keep the members current" to
	// "keep the membership itself current", for repositories the user has
	// nominated by root rather than one at a time — see Candidate.Enrollable.
	//
	// It is deliberately a second switch rather than part of Global, because
	// the two do different kinds of thing. Global re-merges: it rewrites a
	// derived copy of nodes that already belong there, and switching it on
	// cannot change what the global graph is ABOUT. Enroll changes the set of
	// repositories in it — the thing the user assembled — and also maintains
	// graft's workspace federation at each root. Neither is reversible by
	// switching the flag back off, so it gets its own line in settings.
	Enroll bool

	Max      int
	Cooldown time.Duration
	Attempts int
}

// withDefaults fills the zeroes, so a Policy built from a settings file that
// predates any of these fields is still a usable one.
func (p Policy) withDefaults() Policy {
	if p.Max <= 0 {
		p.Max = DefaultMax
	}
	if p.Cooldown <= 0 {
		p.Cooldown = DefaultCooldown
	}
	if p.Attempts <= 0 {
		p.Attempts = DefaultAttempts
	}
	return p
}

// AllowMetered is the single question heal.For is asked: may this plan contain
// an LLM step at all? A local model answers it yes for free, which is the
// whole reason the loop can be on by default.
func (p Policy) AllowMetered() bool { return p.Metered || p.Local }

// Spends reports whether acting on this policy can put a charge on the user's
// account — which is true of the billed backend and false of a local model, no
// matter how much LLM work either of them does.
func (p Policy) Spends() bool { return p.Metered && !p.Local }

// Candidate is one repository as the board currently sees it. It is a value,
// not a pointer into the board's model, because the decision is taken off the
// main thread.
type Candidate struct {
	Path  string
	Name  string
	Graph graphstate.Graph
	// Graft is the same repository's graft index, the second thing on the row
	// that goes stale. Its zero value is StateNone, which asks for nothing, so
	// a caller that does not read graft state simply gets the old behaviour.
	Graft graftstate.Index
	// Global is this repository's membership of the cross-repo graph — the
	// third thing on the row that goes stale, and the only one that is stale
	// against work this loop itself does: every rebuild below makes the
	// merged copy older than the graph it came from.
	Global globalgraph.Member
	// Enrollable says this repository sits under a root the user nominated as
	// a fleet, which is the fact that turns "not a member of the global
	// graph" from a choice into a defect. It is resolved by the caller
	// against the configured roots rather than here: which directories are
	// fleets is a setting, and this package takes no settings.
	Enrollable bool
	Excluded   bool
	// NoGit is the row's: a directory boarded on its graph alone. graft
	// indexes repositories, so it never gets a first build.
	NoGit bool
	// GraftOnly marks a checkout the loop keeps a graft index for and nothing
	// else — a linked worktree the board does not show. Its Graph and Global
	// are zero and ignored: a worktree is the same repository on another
	// branch, and extracting it would pay for a graph its main checkout's
	// already covers, while graft's index is free and per working tree.
	GraftOnly bool
	// Busy is set when a job for this repository is already queued or running
	// — the user's own, or one of this loop's earlier chains.
	Busy bool
}

// Action is one repository the loop has decided to fix, with the plan it
// decided on and the signature it will be judged against afterwards.
type Action struct {
	Path string
	Name string
	Plan heal.Plan
	// Sig is the issue set this plan answers. It comes back to Done so a
	// repository that finishes with exactly the issues it started with is
	// counted as an attempt that changed nothing.
	Sig string
	// Attempt is which try this is, 1-based, against Sig.
	Attempt int
	// Local says the metered steps in this plan will run against the local
	// model rather than a billed backend.
	Local bool
	// GraftOnly is the candidate's: the plan is a lone graft-build for a
	// checkout that has no row on the board.
	GraftOnly bool
}

// Skip is one repository the loop looked at and declined, for the diagnostics
// pane — the loop being quiet and the loop being off look identical from the
// outside otherwise.
type Skip struct {
	Path string
	Name string
	Why  string
}

// Stuck is a repository the loop has given up on: the same defects survived
// Attempts fixes, so a further identical run is a further identical failure.
type Stuck struct {
	Path string
	Name string
	Sig  string
	// Tries is how many attempts it took to conclude that.
	Tries int
	// Err is the last failure, when the attempts failed rather than merely
	// achieved nothing.
	Err string
	// Why is the reason in one sentence, the same one the skip line carries.
	// Err, when there is one, is the more specific answer and wins.
	Why string
}

// Sig is the issue signature of a graph: its issue codes, in order, joined.
//
// It is what makes "tried this already" mean something precise. A repository
// whose drift was fixed and which now has unnamed communities has a different
// signature and gets a fresh budget of attempts; one that comes back from a
// fix with the byte-identical complaint does not.
func Sig(g graphstate.Graph) string {
	issues := g.Issues()
	if len(issues) == 0 {
		return ""
	}
	codes := make([]string, 0, len(issues))
	for _, i := range issues {
		codes = append(codes, i.Code)
	}
	return strings.Join(codes, ",")
}

// candSig is the signature of everything the loop is willing to repair about
// one repository: the graph's issue codes, then the graft index's, then the
// global graph's, each included only when the policy allows that kind of work
// at all.
//
// The three indexes share one signature rather than keeping one each, because
// the loop attempts a repository rather than an index: a checkout whose graph
// was rebuilt and whose graft index is still stale has a signature that
// CHANGED, which is exactly the fresh budget of attempts that case deserves.
// Gating on the policy keeps a machine without graft installed — or a board
// with the global loop switched off — on the old signature; otherwise every
// stale index on it would read as a new defect the loop can never clear.
func candSig(c Candidate, pol Policy) string {
	if c.GraftOnly {
		return graftCode(c, pol)
	}
	codes := make([]string, 0, 3)
	if sig := Sig(c.Graph); sig != "" {
		codes = append(codes, sig)
	}
	if code := graftCode(c, pol); code != "" {
		codes = append(codes, code)
	}
	if pol.Global {
		if code := globalCode(c, pol); code != "" {
			codes = append(codes, code)
		}
	}
	return strings.Join(codes, ",")
}

// graftCode is what is wrong with this checkout's graft index under the
// policy: a defect graft build repairs, or — when first builds are wanted and
// this is a git checkout — that there is no index at all.
func graftCode(c Candidate, pol Policy) string {
	if !pol.Graft {
		return ""
	}
	if code := c.Graft.Issue(); code != "" {
		return code
	}
	if pol.GraftCreate && !c.NoGit && c.Graft.State == graftstate.StateNone {
		return graftstate.IssueMissing
	}
	return ""
}

// graftStep is the graft-build this candidate's plan should end with, if any.
func graftStep(c Candidate, pol Policy) (heal.Step, bool) {
	switch graftCode(c, pol) {
	case "":
		return heal.Step{}, false
	case graftstate.IssueMissing:
		return heal.GraftCreateStep(c.Graft)
	}
	return heal.GraftStep(c.Graft)
}

// globalCode is what is wrong with this repository's place in the global
// graph: the member's own defect, or — when enrolment is on and this root was
// nominated — that it has no place in it at all.
//
// The absent case is gated on the graph as well as on the policy, and has to
// be: a checkout that has never been extracted is not a repository missing
// from the global graph, it is a repository with nothing to put in it. Left
// ungated it would give every unextracted checkout a permanent defect the
// loop can never clear, which is the exact shape of the bug that burns an
// attempt budget every cooldown forever.
func globalCode(c Candidate, pol Policy) string {
	if code := c.Global.Issue(); code != "" {
		return code
	}
	if !pol.Enroll || c.Global.In || !c.Enrollable {
		return ""
	}
	if _, ok := heal.JoinStep(c.Graph); !ok {
		return ""
	}
	return globalgraph.IssueOut
}

// Defect is candSig for callers outside this package: what is wrong with one
// candidate under this policy, "" when nothing is.
//
// It exists so a caller can ask "is there anything here to repair?" WITHOUT
// planning, which is the difference between a button that reports "already
// done" and one that reserves an attempt, burns a slot and then reports
// nothing happened.
func Defect(c Candidate, pol Policy) string { return candSig(c, pol.withDefaults()) }

// CanonicalByOrigin picks the one enrollable checkout per origin repository.
// paths and origins are parallel slices; an empty origin forms its own group
// keyed by the path.
//
// The global graph holds one copy of each repository; extra full clones of it
// (task worktrees made with git clone) are not separate repositories, so only
// the canonical checkout may be enrolled. The canonical one is the path whose
// base name matches the origin's last segment, else the shortest base name
// (ties broken by path).
func CanonicalByOrigin(paths []string, origins []string) map[string]bool {
	groups := map[string][]string{}
	order := make([]string, 0, len(paths))
	for i, p := range paths {
		origin := ""
		if i < len(origins) {
			origin = origins[i]
		}
		key := origin
		if key == "" {
			key = "\x00" + p // an empty origin is its own group, keyed by path
		}
		if _, ok := groups[key]; !ok {
			order = append(order, key)
		}
		groups[key] = append(groups[key], p)
	}

	out := make(map[string]bool, len(paths))
	for _, key := range order {
		group := groups[key]
		if key[0] == 0 {
			out[group[0]] = true
			continue
		}
		last := key
		if i := strings.LastIndex(key, "/"); i >= 0 {
			last = key[i+1:]
		}
		best := group[0]
		for _, p := range group[1:] {
			if betterCanonical(p, best, last) {
				best = p
			}
		}
		out[best] = true
	}
	return out
}

// betterCanonical reports whether a should be picked over b: the base name
// matching the origin's last segment wins, then the shortest base name, and a
// lexicographic tie-break keeps the choice stable.
func betterCanonical(a, b, last string) bool {
	ab, bb := filepath.Base(a), filepath.Base(b)
	if (ab == last) != (bb == last) {
		return ab == last
	}
	if len(ab) != len(bb) {
		return len(ab) < len(bb)
	}
	return a < b
}

// globalStep is the re-merge this candidate's plan should end with, if any.
//
// There are two ways to earn one, and the second is the reason this is not
// simply heal.GlobalStep(c.Global). A membership that is ALREADY stale needs
// re-adding, obviously. But so does one that is about to become stale: every
// plan below rewrites graph.json, and a member whose graph is rewritten is a
// member whose merged copy is now the older of the two. Adding the step in the
// same chain closes that in one pass — otherwise the global graph would trail
// the board by a full cooldown, every time, and the loop would spend a second
// attempt discovering drift it caused itself.
func globalStep(c Candidate, pol Policy, plan heal.Plan) (heal.Step, bool) {
	if !pol.Global {
		return heal.Step{}, false
	}
	if !c.Global.In {
		// The join, when the policy and the root both allow one. It is placed
		// here rather than beside the re-merge so it lands at the same point
		// in the chain: after whatever rewrites graph.json, because merging
		// the copy that is about to be replaced would record a member that is
		// stale before the chain it is part of has finished.
		if !pol.Enroll || !c.Enrollable {
			return heal.Step{}, false
		}
		return heal.JoinStep(c.Graph)
	}
	if step, ok := heal.GlobalStep(c.Global); ok {
		return step, true
	}
	if plan.Empty() {
		return heal.Step{}, false
	}
	return heal.Step{
		Kind: "global-add",
		Why: "re-merge this repository into the global graph — the steps above rewrite " +
			"its graph, which leaves the merged copy a rebuild behind. Free",
	}, true
}

type record struct {
	sig      string
	attempts int
	last     time.Time
	running  bool
	err      string
	// built is the graph's built_at_commit as it stood when the last attempt
	// was made. It is the only thing that tells a stamp which never moved
	// apart from one that moved and was overtaken — see restamped.
	built string
	// declared is set once the repository has been reported stuck, so the log
	// says so exactly once rather than on every tick for the rest of the day.
	declared bool
}

// Engine is the loop's memory. Safe for concurrent use: Plan runs on a worker
// goroutine and Done is called from the runner's.
type Engine struct {
	mu   sync.Mutex
	seen map[string]*record
	// now is time.Now, replaced in tests.
	now func() time.Time
}

// New makes an engine with no memory of anything.
func New() *Engine { return &Engine{seen: map[string]*record{}, now: time.Now} }

// Plan chooses what to fix now, and marks those repositories as in flight —
// so a caller that submits everything Plan returned, and calls Done for each,
// never double-submits the same repository across two ticks.
//
// The skips are returned rather than swallowed because "auto-fix did nothing
// this tick" has a dozen different reasons and the user is entitled to the
// one that applies.
func (e *Engine) Plan(cands []Candidate, pol Policy) (actions []Action, skips []Skip) {
	pol = pol.withDefaults()
	if !pol.Enabled {
		return nil, nil
	}
	allow := pol.AllowMetered()

	e.mu.Lock()
	defer e.mu.Unlock()
	now := e.now()

	// In-flight is counted from the engine's own memory rather than from the
	// runner: the runner's queue also holds the user's jobs, and those must
	// not throttle the loop or be throttled by it.
	inflight := 0
	for _, r := range e.seen {
		if r.running {
			inflight++
		}
	}

	for _, c := range cands {
		if inflight >= pol.Max {
			break
		}
		if c.Excluded {
			continue // the X flag means exactly this; not worth a skip line
		}
		if c.Busy {
			skips = append(skips, Skip{c.Path, c.Name, "a job is already running here"})
			continue
		}
		sig := candSig(c, pol)
		if sig == "" {
			// Healthy. Forget it, so a repository that goes bad again next
			// month starts from a clean budget of attempts.
			delete(e.seen, c.Path)
			continue
		}
		var plan heal.Plan
		if !c.GraftOnly {
			plan = heal.For(c.Graph, allow)
			// Order is the point, because a chain stops at its first failure.
			// The re-merge comes after the steps that rewrite graph.json — it
			// exists to pick up what they wrote — and before `graft build`,
			// which is unrelated to both and must not be what stops either.
			if step, ok := globalStep(c, pol, plan); ok {
				plan.Steps = append(plan.Steps, step)
			}
		}
		if step, ok := graftStep(c, pol); ok {
			// Appended LAST, for the same reason: a `graft build` that cannot
			// run must not be what stops this checkout's graph being rebuilt.
			plan.Steps = append(plan.Steps, step)
		}
		if plan.Empty() {
			skips = append(skips, Skip{c.Path, c.Name,
				"nothing free left to do — what remains needs an LLM"})
			continue
		}
		rec := e.seen[c.Path]
		switch {
		case rec == nil:
			rec = &record{}
			e.seen[c.Path] = rec
		case rec.running:
			continue
		case rec.sig != sig:
			// Different defect: a fresh budget. This is the ordinary path
			// through a multi-stage repair — no-graph becomes unnamed becomes
			// healthy — and each stage gets its own attempts.
			rec.attempts, rec.err, rec.declared = 0, "", false
		case restamped(rec, sig, c.Graph):
			// Same defect, but the stamp moved: the last attempt worked and
			// HEAD ran past it. A fresh budget, for the same reason a changed
			// signature gets one — this is progress, not a repeat.
			rec.attempts, rec.err, rec.declared = 0, "", false
		case rec.attempts >= attemptCap(pol, sig):
			if !rec.declared {
				// Only until Declare has said it out loud. A repository the
				// loop has given up on stays a candidate forever — its defect
				// is still there — and a skip line per tick would be the same
				// sentence every thirty seconds for as long as the board is
				// open. Declare is once per repository; so is this.
				skips = append(skips, Skip{c.Path, c.Name, gaveUp(sig, rec.attempts)})
			}
			continue
		case now.Sub(rec.last) < backoff(pol.Cooldown, rec.attempts):
			skips = append(skips, Skip{c.Path, c.Name, "cooling down"})
			continue
		}

		rec.sig = sig
		rec.built = c.Graph.BuiltCommit
		rec.attempts++
		rec.last = now
		rec.running = true
		inflight++
		actions = append(actions, Action{
			Path: c.Path, Name: c.Name, Plan: plan, Sig: sig,
			Attempt: rec.attempts, Local: pol.Local && plan.Metered(),
			GraftOnly: c.GraftOnly,
		})
	}
	return actions, skips
}

// restamped reports whether the graph moved between the last attempt and now,
// for the one signature where that distinction decides anything.
//
// behind-HEAD gets a single attempt because an update that leaves the graph
// still behind has usually proved the stamp cannot be advanced at all. But
// there is a second way to come back behind, and it is not a failure: the
// restamp DID land, on the commit that was HEAD when the rescan finished, and
// a further commit arrived while it ran. The repository is then behind by one
// fresh commit rather than stuck on an old one, and the giving-up sentence —
// "the commit stamp is not advancing" — would be false.
//
// built_at_commit is what separates them: unchanged means the write never
// took, changed means it took and was overtaken. Only behind-HEAD consults
// this. Signatures whose repair rewrites the graph move built_at_commit on
// every attempt, so applying it generally would hand them an attempt budget
// that never runs out.
func restamped(rec *record, sig string, g graphstate.Graph) bool {
	if rec == nil || sig != graphstate.IssueBehind || rec.built == "" {
		return false
	}
	return rec.built != g.BuiltCommit
}

// attemptCap is how many times this particular signature is worth attacking.
//
// It is pol.Attempts for everything except a repository whose ONLY complaint
// is behind-HEAD, which gets exactly one. The plan for behind-HEAD is `update`,
// and an update that ran and left the graph still behind has already told us
// the whole story: graphify compared the rebuild to what was on disk, found no
// difference and therefore rewrote nothing — including the commit stamp. The
// board answers that by advancing the stamp itself after the rescan; when even
// that does not take (an unreadable graph.json, a graph whose stamp lives
// somewhere this version cannot splice), a second and third identical update
// are a second and third identical no-op, and the honest move is to say so on
// the first one rather than spend the budget discovering it again.
//
// A membership that is stale on its own gets one attempt for the same shape of
// reason: `global add` either replaces the merged nodes or it does not, and a
// re-add that ran and left the entry older than the graph has proved the
// timestamps cannot be made to agree by repeating it.
//
// A first graft build is the third: a `graft build` that ran and left no
// index behind found nothing it parses (a docs-only checkout, say), and the
// next one will find the same nothing.
func attemptCap(pol Policy, sig string) int {
	switch sig {
	case graphstate.IssueBehind, globalgraph.IssueStale, graftstate.IssueMissing:
		return 1
	}
	return pol.Attempts
}

// gaveUp is the skip line: the reason, with the verb the skip pane wants in
// front of it.
func gaveUp(sig string, attempts int) string {
	return "gave up: " + stuckWhy(sig, attempts)
}

// stuckWhy is why the loop has stopped attacking this signature — one sentence,
// used both on the skip line and in the give-up report, because they are the
// same fact told twice and had drifted apart: the report used to say "the same
// defects came back" for behind-HEAD, which is the one signature where that is
// not what happened.
//
// For behind-HEAD it has to name the actual obstacle: "survived 1 attempt"
// would read as a loop that barely tried.
func stuckWhy(sig string, attempts int) string {
	if sig == globalgraph.IssueStale {
		return "the global graph survived a re-add — its manifest entry is still older " +
			"than this repository's graph, so merging it again cannot clear it"
	}
	if sig == graftstate.IssueMissing {
		return "graft build left no index here — it found nothing it parses, " +
			"so building again cannot create one"
	}
	if sig == graphstate.IssueBehind {
		return "behind HEAD survived an update — the graph's commit stamp is not advancing, " +
			"so re-running update cannot clear it"
	}
	word := " attempts"
	if attempts == 1 {
		word = " attempt"
	}
	return sig + " survived " + gfy.Itoa(attempts) + word
}

// backoff grows the wait with each failed attempt, so a repository that cannot
// be fixed costs less and less of the machine before it is given up on.
func backoff(base time.Duration, attempts int) time.Duration {
	if attempts < 1 {
		return base
	}
	return base * time.Duration(attempts)
}

// Done records how one action ended. err is empty when the chain ran clean;
// note that a clean run is NOT the same as a fixed repository, and the next
// Plan is where that difference is noticed.
func (e *Engine) Done(a Action, err string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	rec := e.seen[a.Path]
	if rec == nil {
		return
	}
	rec.running = false
	rec.err = err
	rec.last = e.now()
}

// Declare returns the repositories that have just become stuck and have not
// been reported yet, marking them reported. It is how the loop says "I have
// stopped trying" once per repository rather than once per tick.
func (e *Engine) Declare(cands []Candidate, pol Policy) []Stuck {
	pol = pol.withDefaults()
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []Stuck
	for _, c := range cands {
		rec := e.seen[c.Path]
		if rec == nil || rec.running || rec.declared || rec.attempts < attemptCap(pol, rec.sig) {
			continue
		}
		if c.Busy {
			// Something is still working on this checkout — the runner, or the
			// board settling what the last rescan left behind. Its row is the
			// world as it was BEFORE that finishes, and declaring a repository
			// stuck from a stale row is how a checkout that was repaired
			// milliseconds ago gets written off as beyond help. The next tick
			// asks again, against a row that has caught up.
			continue
		}
		if candSig(c, pol) != rec.sig {
			continue // it moved; it is not stuck on this signature
		}
		if restamped(rec, rec.sig, c.Graph) {
			// Plan resets this record's budget, and in the ordinary tick it
			// has already done so by the time Declare runs — but Plan stops at
			// pol.Max and leaves the rest of the list untouched. A repository
			// whose stamp advanced must not be written off on the one tick the
			// loop was too busy to re-plan it.
			continue
		}
		rec.declared = true
		out = append(out, Stuck{Path: c.Path, Name: c.Name, Sig: rec.sig,
			Tries: rec.attempts, Err: rec.err, Why: stuckWhy(rec.sig, rec.attempts)})
	}
	return out
}

// Retry clears the memory for one repository — the "try this one again" the
// user reaches for after fixing by hand whatever the loop could not.
func (e *Engine) Retry(path string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.seen, path)
}

// Reset forgets everything, which is what a settings change has to do: a loop
// that was refusing to retry under the old policy must not keep refusing
// under the new one.
func (e *Engine) Reset() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.seen = map[string]*record{}
}

// Prune forgets every record whose key keep rejects, and reports how many it
// dropped. Nothing else ever removes a record for a subject that is simply no
// longer there — a deleted checkout, an agent worktree that was cleaned up, a
// fleet root taken out of settings — so without it the memory holds every
// subject the loop has ever seen for the life of the process.
//
// A record whose chain is still running is kept whatever keep says: it is the
// in-flight count Plan throttles on, and Done for it has yet to arrive.
//
// Keys are repository paths for Plan's records and fleet keys (see
// ParseFleetKey) for PlanFleet's; keep must answer for both. Call it only with
// the full current subject set — a partial candidate list would wipe the
// attempt budget of everything it happened to leave out.
func (e *Engine) Prune(keep func(key string) bool) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	n := 0
	for k, r := range e.seen {
		if r != nil && r.running {
			continue
		}
		if !keep(k) {
			delete(e.seen, k)
			n++
		}
	}
	return n
}

// Running is how many chains the loop believes it has in flight, for the
// status line.
func (e *Engine) Running() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	n := 0
	for _, r := range e.seen {
		if r.running {
			n++
		}
	}
	return n
}
