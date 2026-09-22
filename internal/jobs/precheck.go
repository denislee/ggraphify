package jobs

import (
	"errors"
	"fmt"
	"path/filepath"

	"github.com/dns/ggraphify/internal/gfy"
	"github.com/dns/ggraphify/internal/graphstate"
)

// RequireGraph is the default Precheck: a command that reads graph.json is
// refused when the output directory does not hold one.
//
// It lives here rather than in the GUI because the GUI is not the only caller
// that can submit a job — `ggraphify-job` runs the same kinds headlessly, from
// a cron line or a CI step, where the refusal matters more rather than less.
//
// The check is against the filesystem at submission time, not against a board
// scan: a build that finished a moment ago must not leave its own repository
// looking ungraphable, and a graph deleted by hand must not look present.
func RequireGraph(j *Job) error {
	if j == nil || !gfy.NeedsGraph(j.Kind) || graphstate.HasGraph(j.Out) {
		return nil
	}
	where := "its graphify-out directory"
	if j.Out != "" {
		where = filepath.Join(j.Out, "graph.json")
	}
	// graphify's own message for this case — "no graph found at …/graph.json —
	// run /graphify first" — arrives only after the job has started, and for a
	// metered command only after a confirm dialog has named a bill. This is the
	// same verdict, delivered before anything is spent.
	return errors.New(gfy.Title(j.Kind) + " needs a graph: " + where +
		" does not exist yet — run Extract (or Update, which is free) first")
}

// Checks runs several prechecks in order, stopping at the first refusal. It
// exists because Options.Precheck is one func and there is now more than one
// thing to check; the order is the caller's, and it is the order the messages
// should arrive in.
func Checks(fs ...func(*Job) error) func(*Job) error {
	return func(j *Job) error {
		for _, f := range fs {
			if f == nil {
				continue
			}
			if err := f(j); err != nil {
				return err
			}
		}
		return nil
	}
}

// RequireLocalServer refuses a job that will talk to THIS machine's ollama
// when the server is not answering and nothing here can make it answer.
//
// It exists because of the run that produced it: an extract against a system
// unit's ollama that was installed, enabled and not running. The lifecycle did
// its job — it took a lease and called StartOllama — and StartOllama correctly
// declined, because a system-wide ollama.service needs root and the two
// alternatives (a user unit, a detached serve against a different model store)
// are worse than not starting. That verdict then went to the application log
// and nowhere else, and the job ran: twelve chunks, twelve "Connection error",
// a partial graph and an error that names neither the cause nor the cure.
//
// So the verdict is moved to the one place that can act on it. The three
// arms, and why each is a refusal rather than a warning:
//
//   - the server is answering: nothing to decide, whoever started it.
//   - the server is down but startable — a per-user unit, or a machine where
//     a detached `ollama serve` is the right answer: NOT refused. This is the
//     ordinary state between two jobs in a sweep, and the lease will start it.
//   - the server is down and unstartable, or down with the lifecycle switched
//     off: refused, naming the command that fixes it. Nothing about running
//     the job could have discovered anything the refusal does not already say.
//
// auto reports whether the ollama lifecycle is on; nil means on, for a caller
// with no setting to consult.
func RequireLocalServer(auto func() bool) func(*Job) error {
	return func(j *Job) error {
		if j == nil || !j.Local || !gfy.ArgvOllama(j.Argv) {
			return nil
		}
		// ProbeLocal rather than a fresh dial: SubmitCmd already probes this
		// backend through LocalConcurrency, so the answer is in the cache and
		// this costs nothing. A stale "up" is allowed through for ProbeLocal's
		// own stated reason — it was true when measured — and the job then
		// fails the way it always did, which is no worse than today.
		if gfy.ProbeLocal(gfy.OllamaBackend).Reach {
			return nil
		}
		if auto != nil && !auto() {
			return errors.New(gfy.Title(j.Kind) + " needs the local model server: nothing is " +
				"answering at " + gfy.OllamaBaseURL() + ", and the ollama lifecycle is " +
				"switched off, so the board will not start one — start it yourself, or " +
				"turn the lifecycle back on in Settings")
		}
		err := gfy.CanStartOllama()
		if err == nil {
			return nil
		}
		// %w and not err.Error(): NeedsRoot on the other end is what puts the
		// command on the clipboard, and it can only read a wrapped sentinel.
		msg := gfy.Title(j.Kind) + " needs the local model server: nothing is answering at " +
			gfy.OllamaBaseURL() + ", and %w"
		if gfy.NeedsRoot(err) {
			return fmt.Errorf(msg+" — run `"+gfy.SudoStartOllama+"` and try again", err)
		}
		return fmt.Errorf(msg, err)
	}
}
