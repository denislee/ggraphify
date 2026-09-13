// Command ggraphify-scan prints the board's rows without a display.
//
// It exists to prove the rule the architecture rests on: everything the GUI
// renders is computed by packages that do not import GTK. If this command can
// print it, the board is showing derived state rather than something only the
// widgets know.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/dns/ggraphify/internal/board"
	"github.com/dns/ggraphify/internal/discover"
	"github.com/dns/ggraphify/internal/graphstate"
	"github.com/dns/ggraphify/internal/store"
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
		issues    = flag.Bool("issues", false, "add a column naming each row's issues (no-graph, drift, unnamed, no-report…)")
		rawDrift  = flag.Bool("raw-drift", false, "ignore the board's settled-drift baselines and show the unadjudicated walk")
	)
	flag.Parse()

	opts := board.Options{Depth: *depth, SkipDrift: *skipDrift, OutName: *outName, OutBase: *outBase, ShowHidden: *hidden}
	if *roots != "" {
		for _, r := range strings.Split(*roots, ",") {
			if r = strings.TrimSpace(r); r != "" {
				opts.Roots = append(opts.Roots, r)
			}
		}
	}

	// The same baselines the GUI reads, so the two agree about what is stale.
	// A settled path is drift a successful graphify run looked at and declined
	// to graph; --raw-drift is how you see what that is hiding.
	if !*rawDrift {
		st := store.Open(store.DefaultPath())
		opts.Baseline = st.DriftBaseline
	}

	rows, err := board.Scan(opts)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ggraphify-scan:", err)
		os.Exit(1)
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

func filter(rows []board.Row, keep func(board.Row) bool) []board.Row {
	out := rows[:0]
	for _, r := range rows {
		if keep(r) {
			out = append(out, r)
		}
	}
	return out
}
