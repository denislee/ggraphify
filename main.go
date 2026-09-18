// Command ggraphify is a native Wayland GUI for graphify across every local
// repository: one board where each checkout is a row and each graphify
// operation is a supervised, streamable job against that row.
//
// It never re-implements graphify. Every mutation is a subprocess whose exact
// argv is shown before it runs, and every fact on the board is derived from
// the files graphify leaves in graphify-out/ — which is why the board is
// correct even on a machine where graphify is not installed at all.
//
// The two rules that matter to a user:
//
//   - AST work is free and fans out. LLM-backed work is metered, runs one at
//     a time, and is gated behind a confirm that names the backend, the model
//     and the repository count.
//   - ggraphify never writes inside a repository. Its own state is a sidecar
//     under ~/.local/share/ggraphify/. The one exception is an explicit,
//     confirmed `hook install`, which is graphify's write and not ours.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"github.com/dns/ggraphify/internal/applog"
	"github.com/dns/ggraphify/internal/discover"
	"github.com/dns/ggraphify/internal/gfy"
	"github.com/dns/ggraphify/internal/open"
	"github.com/dns/ggraphify/internal/store"
	"github.com/dns/ggraphify/internal/ui"
)

// version is stamped by the Makefile; "dev" when built with a bare `go build`.
var version = "dev"

func main() {
	var (
		roots   = flag.String("roots", "", "comma-separated scan roots (default: the stored preference, else ~/git, ~/tmp, ~/.graphify/repos)")
		depth   = flag.Int("depth", 0, "how deep below a root to look for checkouts (default: the stored preference, else 3)")
		refresh = flag.Duration("refresh", 0, "how often to rescan (default: the stored preference, else 30s)")
		outName = flag.String("out-name", "", "directory name graphify's knowledge lives in inside each checkout (default: the stored preference, else $GRAPHIFY_OUT_NAME, else graphify-out)")
		outBase = flag.String("out-base", "", "keep every repository's graph under this one directory instead, a subdirectory per checkout (default: the stored preference, else in-tree)")
		hidden  = flag.Bool("hidden", false, "board checkouts inside dot-named directories too (default: the stored preference, else they are hidden)")
		term    = flag.String("term", "", "terminal emulator to open an editor in (default: $TERMINAL, foot, alacritty, kitty)")
		editor  = flag.String("editor", "", "editor (default: $VISUAL, $EDITOR, nvim, vim)")
		dark    = flag.Bool("dark", false, "force the dark colour scheme for this run")
		light   = flag.Bool("light", false, "force the light colour scheme for this run")
		showVer = flag.Bool("version", false, "print the version and exit")
	)
	flag.Usage = usage
	flag.Parse()

	if *showVer {
		fmt.Println("ggraphify", version)
		return
	}
	if *dark && *light {
		fmt.Fprintln(os.Stderr, "ggraphify: -dark and -light contradict each other")
		os.Exit(2)
	}

	// Which flags were actually typed, as opposed to sitting at their
	// defaults. The board needs the difference: a stored preference outranks a
	// built-in default but never outranks something the command line said, and
	// a settings row whose value came from the command line is shown pinned
	// rather than silently editable. flag.Visit walks only what was set.
	setFlags := map[string]bool{}
	flag.Visit(func(f *flag.Flag) { setFlags[f.Name] = true })

	// GC headroom at Go's default. The board's steady state is a few
	// megabytes of derived state plus GTK's own allocations, and the soft
	// memory limit — not a tighter GC percentage — is the actual guard
	// against a runaway graph.json parse. Tightening the percentage below the
	// default buys CPU cost without buying safety.
	debug.SetGCPercent(100)
	debug.SetMemoryLimit(512 << 20)

	// Everything the standard logger is handed goes into the in-memory
	// application log, which mirrors it to stderr and renders it in the
	// board's bottom panel. The flags and prefix are cleared because that
	// rendering carries its own timestamp — a board launched from fuzzel has
	// no terminal, and before this the log went nowhere at all.
	log.SetFlags(0)
	log.SetPrefix("")
	log.SetOutput(applog.Default)

	if !singleInstance() {
		fmt.Fprintln(os.Stderr, "ggraphify: another instance is already running")
		os.Exit(1)
	}

	// gfy sends this as the User-Agent of the one thing it talks to directly:
	// the OpenCode Go proxy's requests to the gateway, which asks a client to
	// name itself rather than arrive as a generic SDK.
	gfy.AppVersion = version
	applog.Infof("ggraphify %s starting — %s", version, store.DefaultPath())

	st := store.Open(store.DefaultPath())
	// The command line's say in where knowledge lives, handed to the store
	// before anything reads a row or launches a job. Both the board's scan and
	// every job's GRAPHIFY_OUT resolve through it, so they cannot disagree.
	st.SetOutOverride(*outName, setFlags["out-name"], *outBase, setFlags["out-base"])

	opts := ui.Options{
		Store:      st,
		Depth:      *depth,
		Refresh:    *refresh,
		Opener:     open.Config{Terminal: *term, Editor: *editor},
		Dark:       *dark,
		ForceLight: *light,
		SetFlags:   setFlags,
		Version:    version,
		OutName:    *outName,
		OutBase:    *outBase,
		ShowHidden: *hidden,
	}
	if *roots != "" {
		opts.Roots = splitList(*roots)
	} else if s := st.Settings(); len(s.Roots) > 0 {
		opts.Roots = s.Roots
	} else {
		opts.Roots = discover.DefaultRoots()
	}
	if opts.Depth <= 0 {
		if d := st.Settings().Depth; d > 0 {
			opts.Depth = d
		}
	}
	if opts.Refresh <= 0 {
		if r := st.Settings().Refresh; r > 0 {
			opts.Refresh = time.Duration(r) * time.Second
		}
	}

	app := ui.New(opts)
	os.Exit(app.Run(nil))
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// singleInstance takes an exclusive flock on a file in the data directory. A
// second launch exits rather than opening a duplicate window — two boards
// would race on the same state.json and, worse, could each start a job
// against the same repository.
func singleInstance() bool {
	dir := store.DefaultDir()
	// 0700/0600: this is the same directory state.json lives in, and the lock
	// is a single-user guard — a second instance of this user's board, not
	// anybody else's.
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return true // cannot guard; do not block the user over it
	}
	f, err := os.OpenFile(filepath.Join(dir, "ggraphify.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return true
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return false
	}
	// Deliberately leaked: the lock is held for the process's lifetime.
	return true
}

func usage() {
	fmt.Fprint(os.Stderr, `ggraphify — a board over every local repository's graphify graph

usage: ggraphify [flags]

Companion commands, both headless:
  ggraphify-scan   print the board's rows as a table or JSON
  ggraphify-job    run one supervised graphify job, streaming to stdout

`)
	flag.PrintDefaults()
	fmt.Fprint(os.Stderr, "\nkeys — press ? in the app for the same list:\n")
	fmt.Fprint(os.Stderr, ui.UsageKeys())
}
