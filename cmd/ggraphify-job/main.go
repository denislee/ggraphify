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

func main() {
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
		return
	}
	if *accts {
		for _, a := range gfy.ClaudeAccounts(*account) {
			mark := " "
			if a.Dir == gfy.ClaudeDirFor(*account) {
				mark = "*"
			}
			fmt.Printf("%s %-12s %s\n", mark, a.Name, a.Label())
		}
		return
	}
	if *kind == "" {
		fmt.Fprintln(os.Stderr, "ggraphify-job: -kind is required (see -list)")
		os.Exit(2)
	}
	if _, ok := gfy.Known[*kind]; !ok {
		fmt.Fprintf(os.Stderr, "ggraphify-job: unknown kind %q (see -list)\n", *kind)
		os.Exit(2)
	}

	abs, err := filepath.Abs(*repo)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ggraphify-job:", err)
		os.Exit(1)
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
	argv := gfy.Argv(*kind, p)
	if argv == nil {
		fmt.Fprintf(os.Stderr, "ggraphify-job: no command builder for %q\n", *kind)
		os.Exit(2)
	}

	cost := gfy.CostOf(*kind)
	fmt.Fprintf(os.Stderr, "# %s (%s)\n%s\n", gfy.Title(*kind), cost, gfy.Quote(argv))
	if *dry {
		return
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
			os.Exit(3)
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
		os.Exit(3)
	}

	r := jobs.New(jobs.Options{Precheck: jobs.RequireGraph})
	defer r.Close()

	var env gfy.Env
	if spec.Name != "" || spec.Base != "" || spec.Repo != "" {
		env = gfy.Env{"GRAPHIFY_OUT": p.Out}
	}
	job, err := r.SubmitCmd(*kind, abs, gfy.Title(*kind)+" · "+filepath.Base(abs), p, env)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ggraphify-job:", err)
		os.Exit(1)
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
	for ev := range r.Events() {
		if ev.Job.ID != job.ID {
			continue
		}
		flush()
		if ev.Job.Status.Done() {
			fmt.Fprintf(os.Stderr, "\n# %s in %s (exit %d)\n",
				ev.Job.Status, ev.Job.Elapsed().Round(time.Millisecond), ev.Job.Exit)
			switch ev.Job.Status {
			case jobs.Succeeded:
				os.Exit(0)
			case jobs.Canceled:
				os.Exit(130)
			default:
				if line := gfy.FirstErrorLine(job.Log.String()); line != "" {
					fmt.Fprintln(os.Stderr, "#", strings.TrimSpace(line))
				}
				os.Exit(1)
			}
		}
	}
}
