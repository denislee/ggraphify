// Command ggraphify-scan prints the board's rows without a display.
//
// It exists to prove the rule the architecture rests on: everything the GUI
// renders is computed by packages that do not import GTK. If this command can
// print it, the board is showing derived state rather than something only the
// widgets know.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/dns/ggraphify/internal/board"
	"github.com/dns/ggraphify/internal/discover"
	"github.com/dns/ggraphify/internal/gfy"
	"github.com/dns/ggraphify/internal/graphstate"
	"github.com/dns/ggraphify/internal/knowledge"
	"github.com/dns/ggraphify/internal/store"
	"github.com/dns/ggraphify/internal/usage"
)

func main() {
	var (
		roots     = flag.String("roots", "", "comma-separated scan roots (default: ~/git, ~/tmp, ~/.graphify/repos)")
		depth     = flag.Int("depth", discover.DefaultDepth, "how deep below a root to look for checkouts")
		asJSON    = flag.Bool("json", false, "emit JSON instead of a table")
		state     = flag.String("state", "", "only rows in this state (none|raw|stale|fresh|broken)")
		graphed   = flag.Bool("graphed", false, "only rows that have a graph at all")
		skipDrift = flag.Bool("no-drift", false, "skip the per-repo tree walk that computes drift")
		outName   = flag.String("out-name", "", "directory name graphify's knowledge lives in inside each checkout (default: $GRAPHIFY_OUT_NAME, else graphify-out)")
		outBase   = flag.String("out-base", "", "look for every repository's graph under this one directory instead, a subdirectory per checkout")
		group     = flag.String("group", "", "only rows under this folder — a scan root, or a directory below one")
		hidden    = flag.Bool("hidden", false, "board checkouts inside dot-named directories too")
		groups    = flag.Bool("groups", false, "list the folders the rows fall into, with counts, and exit")
		unhealthy = flag.Bool("unhealthy", false, "only rows with something wrong — what the board's Fix button would act on")
		behind    = flag.Bool("behind", false, "only rows whose graph was built from a commit that is no longer HEAD — what the board's Ctrl+U sweep would act on")
		gap       = flag.Bool("gap", false, "only rows an agent used that have no graphify graph to have answered with — the board's Used ✕ chip")
		gapDays   = flag.Int("gap-days", 7, "the window -gap judges usage over, in days")
		issues    = flag.Bool("issues", false, "add a column naming each row's issues (no-graph, drift, unnamed, no-report…)")
		rawDrift  = flag.Bool("raw-drift", false, "ignore the board's settled-drift baselines and show the unadjudicated walk")
		usageMode = flag.Bool("usage", false, "report which agents actually ran graphify and graft, instead of the board")
		usageDays = flag.Int("usage-days", 30, "the window -usage reports over, in days")
		usageMD   = flag.Bool("markdown", false, "with -usage: emit the markdown briefing meant to be pasted into an agent")
		writeIdx  = flag.Bool("index", false, "republish <out-base>/index.json from this scan — the lookup table that maps a checkout's absolute path to its graph — and exit")
	)
	flag.Parse()

	// flag.Visit walks only what the command line actually said, which is the
	// difference this command needs: a stored preference outranks a built-in
	// default but never outranks an explicit flag.
	setFlags := map[string]bool{}
	flag.Visit(func(f *flag.Flag) { setFlags[f.Name] = true })

	// The stored settings, read the same way the board reads them. Without
	// this the two disagree about the one thing that decides whether a
	// repository HAS a graph: a board configured with an out-base keeps every
	// graph under one directory, and a scan that defaulted to the in-checkout
	// graphify-out/ would report each of those repositories as ungraphed.
	// That is not a cosmetic difference — it is the whole `-usage`
	// recommendation list inverted.
	st := store.Open(store.DefaultPath())
	set := st.Settings()
	outNameV, outBaseV := resolveOut(set, setFlags, *outName, *outBase)

	opts := board.Options{Depth: *depth, SkipDrift: *skipDrift, OutName: outNameV, OutBase: outBaseV, ShowHidden: *hidden}
	if !setFlags["depth"] && set.Depth > 0 {
		opts.Depth = set.Depth
	}
	if !setFlags["hidden"] {
		opts.ShowHidden = set.ShowHidden
	}
	if *roots != "" {
		for _, r := range strings.Split(*roots, ",") {
			if r = strings.TrimSpace(r); r != "" {
				opts.Roots = append(opts.Roots, r)
			}
		}
	} else if len(set.Roots) > 0 {
		opts.Roots = set.Roots
	}

	// The same baselines the GUI reads, so the two agree about what is stale.
	// A settled path is drift a successful graphify run looked at and declined
	// to graph; --raw-drift is how you see what that is hiding.
	if !*rawDrift {
		opts.Baseline = st.DriftBaseline
	}

	if *usageMode {
		// The usage rollup is derived from Claude Code's transcripts and
		// graft's own session counters, not from the checkouts, so it is
		// reported before the scan filters get a say — but it still needs the
		// repository list to find the graft counter files.
		rows, err := board.Scan(board.Options{
			Roots: opts.Roots, Depth: opts.Depth, SkipDrift: true,
			OutName: opts.OutName, OutBase: opts.OutBase, ShowHidden: opts.ShowHidden,
		})
		if err != nil {
			fmt.Fprintln(os.Stderr, "ggraphify-scan:", err)
			os.Exit(1)
		}
		printUsage(rows, *usageDays, *asJSON, *usageMD)
		return
	}

	rows, err := board.Scan(opts)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ggraphify-scan:", err)
		os.Exit(1)
	}

	if *writeIdx {
		// Before every filter below: an index narrowed by a -state chip would
		// publish a partial answer to a question — "where is this checkout's
		// graph" — that has nothing to do with which states are interesting
		// right now.
		writeIndex(opts.OutBase, rows)
		return
	}

	if *groups {
		w := tabwriter.NewWriter(os.Stdout, 0, 8, 2, ' ', 0)
		fmt.Fprintln(w, "REPOS\tFOLDER\tPATH")
		fmt.Fprintf(w, "%d\t%s\t\n", len(rows), "all folders")
		for _, g := range board.Groups(rows) {
			label := g.Label
			if g.Depth > 0 {
				label = "  " + label
			}
			fmt.Fprintf(w, "%d\t%s\t%s\n", g.Count, label, g.Path)
		}
		w.Flush()
		return
	}

	if *group != "" {
		rows = filter(rows, func(r board.Row) bool { return board.InGroup(r, *group) })
	}
	if *state != "" {
		want, ok := graphstate.ParseState(*state)
		if !ok {
			fmt.Fprintf(os.Stderr, "ggraphify-scan: unknown state %q\n", *state)
			os.Exit(2)
		}
		rows = filter(rows, func(r board.Row) bool { return r.Graph.State == want })
	}
	if *unhealthy {
		rows = filter(rows, func(r board.Row) bool { return !r.Graph.Healthy() })
	}
	if *behind {
		// Not a state, so it cannot be spelled -state: a graph can be behind
		// HEAD and `fresh` at the same time, which is precisely why this is
		// worth asking about separately.
		rows = filter(rows, func(r board.Row) bool { return r.Behind })
	}
	if *gap {
		// The same join the board's Used ✕ chip makes, over the same window:
		// the rollup says who worked where, the rows say what there was to
		// answer them with, and this is where the two disagree.
		x := usage.Load(usage.DefaultPath(store.DefaultDir()))
		paths := make([]string, 0, len(rows))
		for _, r := range rows {
			paths = append(paths, r.Path)
		}
		uses := x.RepoUses(paths, usage.Window{Days: *gapDays})
		rows = filter(rows, func(r board.Row) bool {
			return usage.UsedWithoutGraph(r, uses[r.Path].Total())
		})
	}
	if *graphed {
		rows = filter(rows, func(r board.Row) bool { return r.Graph.State != graphstate.StateNone })
	}

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(struct {
			Counts board.Counts `json:"counts"`
			Rows   []board.Row  `json:"rows"`
		}{board.Summarize(rows), rows}); err != nil {
			fmt.Fprintln(os.Stderr, "ggraphify-scan:", err)
			os.Exit(1)
		}
		return
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 8, 2, ' ', 0)
	head := "STATE\tREPO\tBRANCH\tGRAPH\tCOMM\tDRIFT\tBUILT\tSIZE\tPATH"
	if *issues {
		head = "STATE\tREPO\tISSUES\tBRANCH\tGRAPH\tCOMM\tDRIFT\tBUILT\tSIZE\tPATH"
	}
	fmt.Fprintln(w, head)
	for _, r := range rows {
		g := r.Graph
		nodes := ""
		if g.Nodes > 0 || g.Links > 0 {
			nodes = fmt.Sprintf("%dn/%de", g.Nodes, g.Links)
		}
		comm := ""
		if g.Communities > 0 {
			comm = fmt.Sprint(g.Communities)
			if !g.Labeled {
				comm += "*" // unlabeled
			}
		}
		branch := r.Ref()
		if r.Behind {
			branch += " ≠"
		}
		if *issues {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				g.State, r.Name, g.IssueSummary(), branch, nodes, comm, g.DriftString(),
				board.Age(g.BuiltAt), board.Bytes(g.SizeBytes), r.Path)
		} else {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				g.State, r.Name, branch, nodes, comm, g.DriftString(),
				board.Age(g.BuiltAt), board.Bytes(g.SizeBytes), r.Path)
		}
	}
	w.Flush()

	c := board.Summarize(rows)
	fmt.Printf("\n%d repos · %d graphed · %d fresh · %d stale · %d raw · %d broken · %d behind HEAD\n",
		c.Repos, c.Graphed, c.Fresh, c.Stale, c.Raw, c.Broken, c.Behind)
	fmt.Println("(* on COMM means the communities have placeholder names — run `label`)")
}

// resolveOut applies the board's own precedence to where graphify knowledge
// lives: a stored preference beats the built-in default, and an explicit flag
// beats both. It mirrors (*ui.App).outLocation, and the two must not drift —
// if they do, this command reports a graphed repository as ungraphed and the
// -usage recommendations invert.
func resolveOut(set store.Settings, flags map[string]bool, name, base string) (string, string) {
	outName, outBase := set.OutName, set.OutBase
	if flags["out-name"] {
		outName = name
	}
	if flags["out-base"] {
		outBase = base
	}
	return outName, outBase
}

// writeIndex republishes the knowledge index from a completed scan — the same
// file the board writes on every refresh, for a machine that runs the board
// rarely or not at all.
//
// A blank out-base is refused rather than silently doing nothing: somebody who
// typed -index asked for a file, and "your graphs live inside their own
// checkouts, so there is no index to write" is the answer, not silence.
func writeIndex(outBase string, rows []board.Row) {
	base := discover.Expand(strings.TrimSpace(outBase))
	if base == "" {
		fmt.Fprintln(os.Stderr, "ggraphify-scan: -index needs a central out-base; this board keeps every "+
			"graph inside its own checkout, where it is already discoverable (set one in the GUI's "+
			"settings, or pass -out-base)")
		os.Exit(2)
	}
	n, wrote, err := knowledge.Sync(base, rows)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ggraphify-scan:", err)
		os.Exit(1)
	}
	verb := "unchanged"
	if wrote {
		verb = "written"
	}
	fmt.Printf("%s: %d repositories, %s\n", knowledge.Path(base), n, verb)
	fmt.Printf("look one up with: jq -r '.entries[\"'\"$PWD\"'\"].graph' %s\n", knowledge.Path(base))
}

// printUsage is the headless twin of the board's Usage column and its U
// dashboard: the same rollup, the same window, printed.
func printUsage(rows []board.Row, days int, asJSON, asMarkdown bool) {
	home, _ := os.UserHomeDir()
	var accounts []usage.Account
	for _, c := range gfy.ClaudeAccounts("") {
		if !c.Missing && c.Dir != "" {
			accounts = append(accounts, usage.Account{Name: c.Name, Dir: c.Dir})
		}
	}
	repos := make([]string, 0, len(rows))
	for _, r := range rows {
		repos = append(repos, r.Path)
	}

	path := usage.DefaultPath(store.DefaultDir())
	x := usage.Load(path)
	started := time.Now()
	if err := x.Update(context.Background(), usage.Options{Accounts: accounts, Repos: repos}); err != nil {
		fmt.Fprintln(os.Stderr, "ggraphify-scan:", err)
		os.Exit(1)
	}
	if err := x.Save(path); err != nil {
		fmt.Fprintln(os.Stderr, "ggraphify-scan: saving the rollup:", err)
	}
	took := time.Since(started).Round(time.Millisecond)
	updated, scanned := x.LastUpdate()

	win := usage.Window{Days: days}
	s := x.Summarize(win)
	sessTotal, sessUsed := x.SessionCount(win)
	if asMarkdown {
		// The same text the GUI's Copy button puts on the clipboard, so a
		// headless machine — or a pipe into wl-copy — is not a second format
		// to keep in step with the first.
		fmt.Print(usage.Report(s, usage.Recommend(rows, s, 0), usage.ReportOptions{
			Accounts:      accounts,
			LastUpdate:    updated,
			Scanned:       scanned,
			Repos:         repos,
			Recents:       x.Recents("", 20),
			Blocked:       usage.Blockages(rows, s, 0),
			Sessions:      x.SessionRolls(win, 40),
			SessionsTotal: sessTotal,
			SessionsUsed:  sessUsed,
		}))
		return
	}
	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(s); err != nil {
			fmt.Fprintln(os.Stderr, "ggraphify-scan:", err)
			os.Exit(1)
		}
		return
	}

	fmt.Printf("%d uses over %d days — graphify %d · graft %d · %d sessions · %d hook injections\n",
		s.Events, s.Days, s.ByTool[usage.Graphify], s.ByTool[usage.Graft], s.Sessions, s.Hooks)
	// The denominator: every session the transcripts recorded, against the ones
	// that reached for either tool. Three of three and three of forty are the
	// same line above and opposite findings.
	fmt.Printf("%d Claude Code sessions ran, %d of them used either tool\n", sessTotal, sessUsed)
	if mix, ok := s.Mix(); ok {
		fmt.Printf("graft read %d%% of the time (%d through the index, %d straight to source), ~%d tokens saved\n",
			mix, s.GraftReads, s.SourceReads, s.SavedTokens)
	}
	fmt.Printf("read %d account(s) in %v, %d transcripts were new; rollup at %s\n\n",
		len(accounts), took, scanned, strings.Replace(path, home, "~", 1))

	if blocked := usage.Blockages(rows, s, 10); len(blocked) > 0 {
		fmt.Printf("ASKED AND GOT NOTHING — %d of %d calls failed", s.Fails, s.Events)
		if n := s.FailByReason[usage.FailNoGraph]; n > 0 {
			fmt.Printf(", %d of them for want of an index", n)
		}
		fmt.Println()
		bw := tabwriter.NewWriter(os.Stdout, 0, 8, 2, ' ', 0)
		fmt.Fprintln(bw, "FAILED\tNO INDEX\tREPOSITORY\tDO\tWHY")
		for _, b := range blocked {
			do := b.Action.Command()
			switch {
			case b.Action == usage.AddRoot:
				do = "add a scan root"
			case do == "":
				do = "-"
			}
			if b.Metered() {
				do += " ($)"
			}
			fmt.Fprintf(bw, "%d\t%d\t%s\t%s\t%s\n", b.Fails, b.NoGraph, b.Name, do, b.Why)
		}
		_ = bw.Flush()
		fmt.Println()
	}

	if recs := usage.Recommend(rows, s, 10); len(recs) > 0 {
		fmt.Println("WORTH DOING NEXT — where usage and index state disagree")
		rw := tabwriter.NewWriter(os.Stdout, 0, 8, 2, ' ', 0)
		fmt.Fprintln(rw, "USES\tREPOSITORY\tDO\tWHY")
		for _, r := range recs {
			do := r.Action.Command()
			if do == "" {
				do = "add a scan root"
			}
			if r.Metered() {
				do += " ($)"
			}
			fmt.Fprintf(rw, "%d\t%s\t%s\t%s\n", r.Uses, r.Name, do, r.Why)
		}
		_ = rw.Flush() // a terminal that will not take our output cannot be told so
		fmt.Println()
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 8, 2, ' ', 0)
	fmt.Fprintln(w, "TOOL\tVERB\tCOUNT")
	for _, tool := range []usage.Tool{usage.Graphify, usage.Graft} {
		for _, c := range s.Verbs[tool] {
			fmt.Fprintf(w, "%s\t%s\t%d\n", tool, c.Name, c.Count)
		}
	}
	fmt.Fprintln(w, "\nCOUNT\tREPOSITORY\t")
	for i, r := range s.Repos {
		if i >= 20 {
			break
		}
		fmt.Fprintf(w, "%d\t%s\t\n", r.Count, strings.Replace(r.Name, home, "~", 1))
	}
	w.Flush()
}

func filter(rows []board.Row, keep func(board.Row) bool) []board.Row {
	out := rows[:0]
	for _, r := range rows {
		if keep(r) {
			out = append(out, r)
		}
	}
	return out
}
