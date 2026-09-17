// Command ggraphify-job runs one supervised graphify job and streams it to
// stdout.
//
// It is the job runner without the GUI: the same argv builders, the same
// process-group supervision, the same cost policy. Use it to check what the
// board *would* run (-n), to reproduce a failure outside the GUI, or from a
// script that wants ggraphify's confirm discipline without a display.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/dns/ggraphify/internal/discover"
	"github.com/dns/ggraphify/internal/gfy"
	"github.com/dns/ggraphify/internal/jobs"
)

func main() { os.Exit(run()) }

// supervisor is the local model server's lifecycle as run() consumes it: take
// a lease per job, and put the server back at exit.
//
// It is an interface, and newSupervisor a var, for exactly one reason: the bug
// this file shipped was teardown that never ran, and the only assertion that
// catches that class is one made from outside, on every terminal status. A
// recorder substituted here proves the defers are reached; nothing else does.
type supervisor interface {
	Acquire() jobs.Lease
	Shutdown()
}

// autoOllama adapts gfy.AutoOllama to that interface — Acquire returns a
// concrete *gfy.OllamaLease, which is already everything jobs.Lease asks for.
type autoOllama struct{ *gfy.AutoOllama }

func (a autoOllama) Acquire() jobs.Lease { return a.AutoOllama.Acquire() }

var newSupervisor = func(enabled func() bool, logf func(string, ...any)) supervisor {
	return autoOllama{&gfy.AutoOllama{
		Enabled: enabled,
		OneShot: true,
		Logf:    logf,
	}}
}

// run is main's body, returning the exit code rather than calling os.Exit.
//
// The split is not style. This tool owns a local model server it may have
// started (-auto-ollama) and a runner holding a live subprocess, and both are
// given back by deferred calls — which os.Exit does not run. Every terminal
// path used to leave through an os.Exit inside the event loop, so the ollama
// the job started stayed resident forever on a machine with nothing to say
// where it came from. One return per outcome, and main's os.Exit is the last
// statement in the program.
func run() int {
	var (
		kind    = flag.String("kind", "", "what to run (see -list)")
		repo    = flag.String("repo", ".", "checkout to run it against")
		list    = flag.Bool("list", false, "list the commands this build knows and exit")
		dry     = flag.Bool("n", false, "print the command line and exit without running it")
		yes     = flag.Bool("y", false, "run a metered (LLM-backed, billed) command without asking")
		backend = flag.String("backend", "", "LLM backend for metered commands (blank: auto-detect from an API key, else the local claude-cli)")
		model   = flag.String("model", "", "model override")
		force   = flag.Bool("force", false, "pass --force")
		ask     = flag.String("q", "", "the question/node for query|explain|affected")
		nodeB   = flag.String("to", "", "the second node for `path`")
		outName = flag.String("out-name", "", "directory name graphify's knowledge lives in inside the checkout (default: $GRAPHIFY_OUT_NAME, else graphify-out)")
		outBase = flag.String("out-base", "", "keep the graph under this directory instead, in a subdirectory named after the checkout")
		out     = flag.String("out", "", "this checkout's output directory outright, absolute or relative to it")
		account = flag.String("claude-account", "", "Claude Code configuration directory to run as, for the claude-cli backend and for `install` (blank: $CLAUDE_CONFIG_DIR, else ~/.claude)")
		accts   = flag.Bool("claude-accounts", false, "list the Claude Code logins found on this machine and exit")
		autoOll = flag.Bool("auto-ollama", true, "start this machine's ollama when the job needs it, and stop it again on the way out if we were the ones who started it")
	)
	flag.Parse()

	if *list {
		kinds := make([]string, 0, len(gfy.Known))
		for k := range gfy.Known {
			kinds = append(kinds, k)
		}
		sort.Strings(kinds)
		for _, k := range kinds {
			s := gfy.Known[k]
			fmt.Printf("%-16s %-8s %s\n", k, s.Cost, s.Title)
		}
		return 0
	}
	if *accts {
		for _, a := range gfy.ClaudeAccounts(*account) {
			mark := " "
			if a.Dir == gfy.ClaudeDirFor(*account) {
				mark = "*"
			}
			fmt.Printf("%s %-12s %s\n", mark, a.Name, a.Label())
		}
		return 0
	}
	if *kind == "" {
		fmt.Fprintln(os.Stderr, "ggraphify-job: -kind is required (see -list)")
		return 2
	}
	if _, ok := gfy.Known[*kind]; !ok {
		fmt.Fprintf(os.Stderr, "ggraphify-job: unknown kind %q (see -list)\n", *kind)
		return 2
	}

	abs, err := filepath.Abs(*repo)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ggraphify-job:", err)
		return 1
	}
	// The same resolver the board goes through, so -out/-out-base/-out-name
	// here and the equivalent settings there land on one directory.
	spec := discover.OutSpec{Name: *outName, Base: *outBase, Repo: *out}
	p := gfy.Params{
		Repo:      abs,
		Out:       spec.For(abs),
		Backend:   gfy.EffectiveBackend(*backend),
		Model:     *model,
		Force:     *force,
		ClaudeDir: *account,
		Question:  *ask,
		NodeA:     *ask,
		NodeB:     *nodeB,
	}
	// The same sizing the board applies before it submits: without it a local
	// backend gets graphify's 60_000-token default chunks, which an ollama
	// context slot silently truncates from the front.
	gfy.ApplyLocalSizing(&p)
	argv := gfy.Argv(*kind, p)
	if argv == nil {
		fmt.Fprintf(os.Stderr, "ggraphify-job: no command builder for %q\n", *kind)
		return 2
	}

	cost := gfy.CostOf(*kind)
	fmt.Fprintf(os.Stderr, "# %s (%s)\n%s\n", gfy.Title(*kind), cost, gfy.Quote(argv))
	if *dry {
		return 0
	}
	// The same gate the GUI puts on a metered action: this command dispatches
	// LLM requests against the user's own key and costs real money, so it does
	// not run on a typo.
	if cost == gfy.Metered && !*yes {
		if gfy.IsLocalBackend(p.Backend) {
			// Not a bill — a local model spends time, not money. The gate
			// stays, because a local extraction over a large repository is
			// still hours of this machine's CPU and not a thing to start by
			// typo, but it must not claim a charge that will never appear.
			fmt.Fprint(os.Stderr, "\n"+gfy.LocalNotice(p.Backend, p.Model)+
				"Re-run with -y to proceed.\n")
			if ok, why := gfy.LocalReady(p.Backend, p.Model); !ok {
				fmt.Fprintln(os.Stderr, "("+why+")")
			}
			return 3
		}
		if p.Backend == gfy.ClaudeCLIBackend {
			fmt.Fprintf(os.Stderr, "\nThis is a metered command — it dispatches LLM requests through the Claude\n"+
				"Code CLI on this machine, billed to that login's plan. Re-run with -y to proceed.\n")
		} else {
			fmt.Fprintf(os.Stderr, "\nThis is a metered command — it dispatches LLM requests and will be billed\n"+
				"to whichever API key is in this environment. Re-run with -y to proceed.\n")
		}
		if ok, why := gfy.BackendReady(p.Backend); !ok {
			fmt.Fprintln(os.Stderr, "("+why+")")
		}
		return 3
	}

	// The local model server's lifecycle, for the one job this process runs.
	// Defaults rather than the GUI's settings, because this tool deliberately
	// reads no state file — a headless run must behave identically whoever's
	// board last touched which switch.
	//
	// OneShot because there is no next job to wait for: the process exits as
	// soon as this one ends, so a grace period is a timer that can only be
	// cancelled by the exit that follows it. Without it the supervisor logs
	// "stopping ollama in 5m0s unless one arrives" immediately before the
	// stop-at-exit it contradicts.
	auto := newSupervisor(
		func() bool { return *autoOll },
		func(format string, args ...any) { fmt.Fprintf(os.Stderr, "# "+format+"\n", args...) },
	)
	r := jobs.New(jobs.Options{
		Precheck: jobs.RequireGraph,
		LocalLease: func(j *jobs.Job) jobs.Lease {
			if j == nil || !j.Local || gfy.ArgvBackend(j.Argv) != gfy.OllamaBackend {
				return nil
			}
			return auto.Acquire()
		},
	})
	defer auto.Shutdown() // after Close: the job must be gone before its server is
	defer r.Close()

	var env gfy.Env
	if spec.Name != "" || spec.Base != "" || spec.Repo != "" {
		env = gfy.Env{"GRAPHIFY_OUT": p.Out}
	}
	job, err := r.SubmitCmd(*kind, abs, gfy.Title(*kind)+" · "+filepath.Base(abs), p, env)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ggraphify-job:", err)
		return 1
	}

	// Ctrl-C cancels the job rather than killing this process out from under
	// it, so graphify gets its SIGTERM and its chance to flush graph.json.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		fmt.Fprintln(os.Stderr, "\n# interrupt — cancelling the job")
		r.Cancel(job.ID)
	}()

	// Stream the log as it grows. The runner's events say "there is more";
	// what to print is the delta against what has already been written.
	printed := 0
	flush := func() {
		s := job.Log.String()
		if len(s) > printed {
			fmt.Print(s[printed:])
			printed = len(s)
		}
	}
	// code survives the loop so the deferred Close/Shutdown/stop all run before
	// main turns it into an exit status. Breaking out rather than returning
	// from inside the switch keeps that single path obvious.
	code := 1
	for ev := range r.Events() {
		// ev.ID, not ev.Job.ID: an output event carries no snapshot, so
		// filtering on the snapshot's ID would discard the whole log stream.
		if ev.ID != job.ID {
			continue
		}
		flush()
		if !ev.Job.Status.Done() {
			continue
		}
		fmt.Fprintf(os.Stderr, "\n# %s in %s (exit %d)\n",
			ev.Job.Status, ev.Job.Elapsed().Round(time.Millisecond), ev.Job.Exit)
		switch ev.Job.Status {
		case jobs.Succeeded:
			code = 0
		case jobs.Canceled:
			code = 130
		default:
			if line := gfy.FirstErrorLine(job.Log.String()); line != "" {
				fmt.Fprintln(os.Stderr, "#", strings.TrimSpace(line))
			}
			code = 1
		}
		break
	}
	return code
}
