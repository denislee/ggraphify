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
	"strings"
	"sync"
	"time"

	"github.com/dns/ggraphify/internal/gfy"
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
	// It never BUILDS a first index and never runs the deep pass: see
	// graftstate.Index.Repairable for why those two are not unattended work.
	Graft bool

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
	Graft    graftstate.Index
	Excluded bool
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
// one repository: the graph's issue codes, then the graft index's when the
// policy allows graft work at all.
//
// The two indexes share one signature rather than keeping one each, because
// the loop attempts a repository rather than an index: a checkout whose graph
// was rebuilt and whose graft index is still stale has a signature that
// CHANGED, which is exactly the fresh budget of attempts that case deserves.
// Gating on pol.Graft keeps a machine without graft installed on the old
// signature — otherwise every stale index on it would read as a new defect the
// loop can never clear.
func candSig(c Candidate, pol Policy) string {
	sig := Sig(c.Graph)
	if !pol.Graft {
		return sig
	}
	code := c.Graft.Issue()
	switch {
	case code == "":
		return sig
	case sig == "":
		return code
	}
	return sig + "," + code
}

type record struct {
	sig      string
	attempts int
	last     time.Time
	running  bool
	err      string
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
		plan := heal.For(c.Graph, allow)
		if pol.Graft {
			if step, ok := heal.GraftStep(c.Graft); ok {
				// Appended LAST, and the order is the point: a chain stops at
				// its first failure, so a `graft build` that cannot run must
				// not be what stops this checkout's graph being rebuilt.
				plan.Steps = append(plan.Steps, step)
			}
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
		case rec.attempts >= attemptCap(pol, sig):
			skips = append(skips, Skip{c.Path, c.Name, gaveUp(sig, rec.attempts)})
			continue
		case now.Sub(rec.last) < backoff(pol.Cooldown, rec.attempts):
			skips = append(skips, Skip{c.Path, c.Name, "cooling down"})
			continue
		}

		rec.sig = sig
		rec.attempts++
		rec.last = now
		rec.running = true
		inflight++
		actions = append(actions, Action{
			Path: c.Path, Name: c.Name, Plan: plan, Sig: sig,
			Attempt: rec.attempts, Local: pol.Local && plan.Metered(),
		})
	}
	return actions, skips
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
func attemptCap(pol Policy, sig string) int {
	if sig == graphstate.IssueBehind {
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
