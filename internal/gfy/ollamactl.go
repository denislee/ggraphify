package gfy

import (
	"os/exec"
	"strconv"
	"sync"
	"syscall"
	"time"
)

// The ollama lifecycle: bring the local model server up when something needs
// it, and take it down again when nothing does.
//
// A local model is free but not cheap. An idle ollama with a model resident
// holds however many gigabytes the weights are — 5 for a 7B at q4, far more
// for anything worth using on the metered commands — and on a laptop it holds
// them out of the page cache the rest of the desktop wants. So the board runs
// the server the way it runs a graphify process: for exactly as long as a job
// is using it.
//
// The whole design rests on one rule, and every decision below follows from
// it: THE BOARD ONLY STOPS A SERVER IT STARTED ITSELF. An ollama that was
// already answering when the first job asked for one belongs to the user —
// they may have a chat open against it, a second tool pointed at it, or they
// may simply like it running — and a GUI that helpfully stopped it would be
// destroying state nobody asked it to touch. There is no setting that relaxes
// this, because there is no reading of "manage the server automatically" that
// means "kill things other people started".

// LOCK ORDERING, for the two mutexes in this file: an AutoOllama's a.mu is
// never held across anything that takes the package-global ollamaStart. Read
// the global flags first (StartedOllama, OllamaPinned) and act on the values;
// call out to StartOllama/StopOllama with a.mu dropped. Every site here obeys
// that, so there is no cycle for a future caller to complete from the other
// side.

// ollamaStart records how, and whether, THIS PROCESS started the server. It
// is the only evidence StopOllama will accept.
type ollamaStartRoute int

const (
	startedNot      ollamaStartRoute = iota // nobody here started it
	startedUserUnit                         // systemctl --user start ollama
	startedDetached                         // a detached `ollama serve` we own
)

var ollamaStart struct {
	sync.Mutex
	route ollamaStartRoute
	pid   int
	// pinned is set when a PERSON started this server — the Start button —
	// rather than a job needing one. Such a server is ours by the ownership
	// rule (we did start it) but it is still not the lifecycle's to stop: the
	// click asked for a running server, not for one that runs until a timer
	// somebody never saw decides otherwise. Manual start, manual stop.
	pinned bool
}

// noteOllamaStart is called by StartOllama on each route it actually takes.
// The "already running" path deliberately does not call it: that server is
// somebody else's, and recording it here is exactly the mistake the rule
// above exists to prevent.
func noteOllamaStart(route ollamaStartRoute, pid int) {
	ollamaStart.Lock()
	defer ollamaStart.Unlock()
	ollamaStart.route, ollamaStart.pid = route, pid
	if route == startedNot {
		ollamaStart.pinned = false
	}
}

// PinOllama marks the running server as one a person asked for, which exempts
// it from the idle stop and from the stop at exit. The settings page's Start
// button calls it; nothing else should.
func PinOllama() {
	ollamaStart.Lock()
	defer ollamaStart.Unlock()
	ollamaStart.pinned = true
}

// OllamaPinned reports whether the running server was started by hand, and so
// whether the lifecycle must leave it alone.
func OllamaPinned() bool {
	ollamaStart.Lock()
	defer ollamaStart.Unlock()
	return ollamaStart.pinned
}

// StartedOllama reports whether the running server is one this process
// started, and so whether StopOllama has anything it is allowed to do.
func StartedOllama() bool {
	ollamaStart.Lock()
	defer ollamaStart.Unlock()
	return ollamaStart.route != startedNot
}

// StopOllama stops a server this process started, and nothing else.
//
// The two routes mirror StartOllama's, because they are the only two it can
// have created:
//
//   - a per-user systemd unit, stopped through systemctl so systemd does not
//     immediately restart what we just killed;
//   - a detached `ollama serve`, signalled by process GROUP so the model
//     runner subprocesses go with it.
//
// It returns the route taken, for the log. errNotOurs means the running
// server was not started here — which is not a failure, it is the answer.
func StopOllama() (string, error) {
	ollamaStart.Lock()
	route, pid := ollamaStart.route, ollamaStart.pid
	ollamaStart.Unlock()

	var how string
	switch route {
	case startedUserUnit:
		how = "systemctl --user stop ollama"
		if err := exec.Command("systemctl", "--user", "stop", "ollama").Run(); err != nil {
			return how, err
		}

	case startedDetached:
		if pid <= 0 {
			return "", errNotOurs
		}
		how = "SIGTERM to process group " + strconv.Itoa(pid)
		// The negative pid is the process group. SIGTERM rather than SIGKILL:
		// ollama unloads its models and closes its store on the way out, and
		// a half-written manifest is a model that has to be pulled again.
		if err := syscall.Kill(-pid, syscall.SIGTERM); err != nil {
			return how, err
		}

	default:
		return "", errNotOurs
	}

	// Forget the start before waiting, not after: whatever the port does now,
	// this process has spent the one claim it had, and a second Stop must not
	// signal a pid that may since have been reused.
	noteOllamaStart(startedNot, 0)
	InvalidateLocalProbe()
	return how, nil
}

// NotOurs reports whether a StopOllama error means only that the server was
// not started here. Callers log this at most once and never as a failure.
func NotOurs(err error) bool { return err == errNotOurs }

var errNotOurs = errStr("the running ollama was not started by this board, so it is not ours to stop")

// AutoOllama is the lease counter that decides when the server is up.
//
// Every job that will talk to a local model takes a lease before its process
// starts and gives it back when the process ends. While at least one lease is
// out the server stays up; when the last one comes back an idle timer starts,
// and if no new lease is taken before it fires the server is stopped.
//
// Leases rather than "is a job running?" because the question is asked from
// several places — the board's buttons, the batch sweep, the unattended fix
// loop — and a counter that each of them increments is the one shape where
// adding a fourth caller cannot forget the server. Pairing is enforced by
// handing back a release func rather than exposing a Release method: the only
// way to take a lease is to receive the thing that returns it.
type AutoOllama struct {
	// Enabled is consulted at every acquire rather than captured once, so
	// toggling the setting takes effect on the next job with no plumbing to
	// re-apply it. Nil means enabled.
	Enabled func() bool
	// Idle is the grace period after the last lease. Nil means the package
	// default.
	Idle func() time.Duration
	// OneShot suppresses the grace period entirely: when the last lease comes
	// back nothing is armed and nothing is announced, because the caller has
	// no next job for the timer to be waiting for.
	//
	// It exists for the headless supervisor, which runs exactly one job and
	// then exits through Shutdown. There the grace period is not a grace
	// period — it is a timer whose only possible outcome is being cancelled a
	// moment later by the exit, after logging a five-minute promise the
	// process will not be alive to keep.
	OneShot bool
	// Logf, when set, narrates each transition. The board points it at the
	// application log; a test at its own recorder.
	Logf func(format string, args ...any)

	// start and stop are the actions, indirected only so a test can observe
	// the state machine without a model server on the machine. Nil means the
	// real StartOllama and StopOllama.
	start func() (string, error)
	stop  func() (string, error)

	mu    sync.Mutex
	n     int
	timer *time.Timer
}

// DefaultAutoOllamaIdle is the grace period an AutoOllama with no Idle func
// waits before stopping a server it started. The store's setting is the one
// users see; this is the fallback for a supervisor built without one.
const DefaultAutoOllamaIdle = 5 * time.Minute

// Acquire declares that something is about to use the local model server, and
// returns the handle that declares it finished — or suspended, when the job is
// paused, and taken up again when it resumes.
//
// It BLOCKS while a cold server starts, which is up to about ten seconds, and
// is therefore called from the job's own goroutine rather than from anything
// that draws. The handle is always non-nil, and every one of its three methods
// is safe to call in any order and any number of times: a deferred Release
// next to an early return cannot drive the count negative, and a Release of an
// already-suspended lease gives nothing back twice.
//
// A start that fails is not an error the caller has to handle: the job runs
// anyway and fails against an unreachable server with graphify's own message,
// which names the endpoint and is the message the user already knows. Refusing
// to run here would instead produce a second, worse diagnosis of the same
// fact.
func (a *AutoOllama) Acquire() *OllamaLease {
	l := &OllamaLease{a: a}
	if a == nil || !a.enabled() {
		l.state = leaseInert
		return l
	}
	a.take()
	return l
}

// OllamaLease is one holder's claim on the local model server: a job's, for as
// long as its process is actually running.
//
// It is a handle with three transitions rather than a bare release func
// because a job has three ends, not one. It can finish (Release), and it can
// stop consuming the server without finishing (Suspend) and start again later
// (Resume) — which is exactly what pausing a job is. A paused graphify is a
// SIGSTOPped process that will not send another request until somebody says
// so, and holding several gigabytes of weights resident for it defeats the
// point of having paused it.
type OllamaLease struct {
	a *AutoOllama

	mu    sync.Mutex
	state leaseState
}

type leaseState int

const (
	leaseHeld      leaseState = iota // counted; the server is up for it
	leaseSuspended                   // given back, and takeable again
	leaseReleased                    // done with, permanently
	leaseInert                       // the lifecycle is off; all three are no-ops
)

// Release gives the lease up for good. The server is stopped after the grace
// period if nothing else holds one.
func (l *OllamaLease) Release() {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	switch l.state {
	case leaseHeld:
		// Held to the end, which is the ordinary case: the gap before the
		// next job is worth waiting out.
		l.state = leaseReleased
		l.a.give(false)
	case leaseSuspended:
		// Already given back when it was suspended — a paused job that is
		// then cancelled comes through here, and taking a second count off
		// would stop a server another job is using.
		l.state = leaseReleased
	}
}

// Suspend gives the lease back and stops the server NOW rather than after the
// grace period, if nothing else holds one.
//
// Now, because a pause is not a gap. The grace period exists because the lull
// between two jobs in a sweep is shorter than a cold start is expensive; a
// pause is somebody asking for their machine back, and a lifecycle that
// answered "in five minutes" would be answering a different question.
func (l *OllamaLease) Suspend() {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.state != leaseHeld {
		return
	}
	l.state = leaseSuspended
	l.a.give(true)
}

// Resume takes the lease again, starting the server if it went away, and
// BLOCKS while it does.
//
// Blocking is the contract, and the caller depends on it: the process this
// lease belongs to must not be woken until the server it is mid-conversation
// with is answering again. A SIGCONT sent first would wake graphify into a
// dead port.
func (l *OllamaLease) Resume() {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.state != leaseSuspended {
		return
	}
	l.state = leaseHeld
	l.a.take()
}

// Suspended reports whether this lease is currently given back, for a caller
// deciding whether a resume has anything to do.
func (l *OllamaLease) Suspended() bool {
	if l == nil {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.state == leaseSuspended
}

// take adds one holder, starting the server when it is the first.
func (a *AutoOllama) take() {
	a.mu.Lock()
	a.n++
	first := a.n == 1
	// Cancel any pending stop before doing anything slow: the commonest case
	// by far is a new job arriving during the grace period, where the right
	// outcome is that nothing happens at all.
	a.disarm()
	a.mu.Unlock()

	if !first {
		return
	}
	if how, err := a.startFn()(); err != nil {
		if NeedsRoot(err) {
			a.logf("local model server is owned by a system unit; run %q", SudoStartOllama)
		} else {
			a.logf("could not start ollama for a job (%s): %v", how, err)
		}
	} else if how != alreadyRunning {
		// A start that found the server already up is the ordinary state
		// inside a sweep — each gap between two jobs ends this way — and
		// logging it would fill the log with the absence of an event.
		a.logf("ollama up for a local job: %s", how)
	}
}

// Leases is how many are outstanding, for the settings page's subtitle.
func (a *AutoOllama) Leases() int {
	if a == nil {
		return 0
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.n
}

// give takes one holder away. When it was the last, the server is stopped
// after the grace period — or immediately, when now is set, which is what a
// pause asks for.
func (a *AutoOllama) give(now bool) {
	// Read before a.mu, not under it. Both of these take the package-global
	// ollamaStart mutex, and taking it while holding a.mu is the ONE ordering
	// this file must not establish: take() and stopNow() reach ollamaStart with
	// a.mu dropped, so a site that reversed it would make a->ollamaStart and
	// ollamaStart->a both live and close the cycle. Nothing holds ollamaStart
	// across a call into AutoOllama today; this is what keeps that from being a
	// property the next caller has to know.
	//
	// Reading a moment early costs nothing: a server that becomes pinned
	// between here and the decision below is caught by idleStop, which
	// re-checks precisely for that.
	ours, pinned := StartedOllama(), OllamaPinned()

	a.mu.Lock()
	if a.n > 0 {
		a.n--
	}
	if a.n > 0 {
		a.mu.Unlock()
		return
	}
	// Not our server, or one a person asked for: nothing to do — and saying
	// so once here keeps the timer from firing every idle period to
	// rediscover it.
	if !ours || pinned {
		a.mu.Unlock()
		return
	}
	a.disarm()
	if now {
		a.mu.Unlock()
		a.stopNow()
		return
	}
	if a.OneShot {
		// Nothing to wait for. Shutdown does the stopping, and saying
		// anything here would only contradict it.
		a.mu.Unlock()
		return
	}
	idle := a.idle()
	a.timer = time.AfterFunc(idle, a.idleStop)
	a.mu.Unlock()
	a.logf("no local job left; stopping ollama in %s unless one arrives", idle)
}

// idleStop is the timer's callback: stop, unless a lease was taken in the
// window between the timer firing and this acquiring the lock.
func (a *AutoOllama) idleStop() {
	a.mu.Lock()
	a.timer = nil
	if a.n > 0 {
		a.mu.Unlock()
		return
	}
	a.mu.Unlock()

	// Re-checked here and not only in give(), because the Start button can be
	// clicked during the grace period — which is precisely somebody saying
	// they want this server to stay.
	if OllamaPinned() {
		return
	}
	a.stopNow()
}

// stopNow stops the server and says what happened. Both automatic stops — the
// idle timer and a pause — end here, so there is one place that decides how a
// stop is reported.
func (a *AutoOllama) stopNow() {
	how, err := a.stopFn()()
	switch {
	case err == nil:
		a.logf("stopped the ollama this board started (%s)", how)
	case NotOurs(err):
		// Nothing to report: a server we did not start was never ours, and
		// give() already declines to stop one.
	default:
		a.logf("could not stop ollama (%s): %v", how, err)
	}
}

// Shutdown cancels a pending stop and takes the server down now if the board
// started it, for the application's exit path.
//
// An auto-started server outliving the board would be the one outcome nobody
// could have intended: the board started it silently, so nothing on the
// machine tells the user where the resident weights came from. A server that
// was already running is left up, by the same rule as everywhere else.
func (a *AutoOllama) Shutdown() {
	if a == nil {
		return
	}
	// Read before a.mu, for the ordering invariant documented in give().
	ours, pinned := StartedOllama(), OllamaPinned()

	a.mu.Lock()
	a.disarm()
	a.n = 0
	a.mu.Unlock()
	if !ours || pinned {
		return
	}
	if how, err := a.stopFn()(); err != nil && !NotOurs(err) {
		a.logf("could not stop ollama at exit (%s): %v", how, err)
	} else if err == nil {
		a.logf("stopped the ollama this board started (%s)", how)
	}
}

// disarm stops a pending idle timer. Caller holds mu.
func (a *AutoOllama) disarm() {
	if a.timer != nil {
		a.timer.Stop()
		a.timer = nil
	}
}

func (a *AutoOllama) enabled() bool {
	return a.Enabled == nil || a.Enabled()
}

func (a *AutoOllama) idle() time.Duration {
	if a.Idle == nil {
		return DefaultAutoOllamaIdle
	}
	if d := a.Idle(); d > 0 {
		return d
	}
	return DefaultAutoOllamaIdle
}

func (a *AutoOllama) startFn() func() (string, error) {
	if a.start != nil {
		return a.start
	}
	return StartOllama
}

func (a *AutoOllama) stopFn() func() (string, error) {
	if a.stop != nil {
		return a.stop
	}
	return StopOllama
}

func (a *AutoOllama) logf(format string, args ...any) {
	if a.Logf != nil {
		a.Logf(format, args...)
	}
}
