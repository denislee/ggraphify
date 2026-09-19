package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/dns/ggraphify/internal/board"
	"github.com/dns/ggraphify/internal/discover"
	"github.com/dns/ggraphify/internal/doctor"
	"github.com/dns/ggraphify/internal/gfy"
	"github.com/dns/ggraphify/internal/store"
)

// doctorCmd is `ggraphify doctor`: the alignment asserted in one command, so
// that neither a human nor an agent has to remember the checks.
//
// It is a subcommand of the GUI binary rather than a fourth executable because
// what it reports on is the GUI's own configuration — the backend it would
// dispatch to, the out-base it writes into, the Claude Code account its jobs
// run as. A separate binary would have to resolve all of that a second time,
// which is precisely the class of drift being checked for.
//
// Exit status: 0 when every invariant holds, 1 when one is broken, and — with
// -strict — 1 for a warning too, so a cron or a pre-flight can choose how
// much it wants to care about work that is merely undone.
func doctorCmd(args []string) int {
	fs := flag.NewFlagSet("ggraphify doctor", flag.ContinueOnError)
	var (
		asJSON = fs.Bool("json", false, "emit the checks as JSON")
		strict = fs.Bool("strict", false, "exit non-zero on a warning as well as on a broken invariant")
		depth  = fs.Int("depth", 0, "how deep below a root to look for checkouts (default: the stored preference)")
	)
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, `ggraphify doctor — assert the invariants this board and its agents share

usage: ggraphify doctor [-json] [-strict]

It checks, against live state: that graphify is installed and is the release the
command builders target; that the configured LLM backend could actually run a
metered job right now; that every graph is discoverable from the checkout it
describes (the index under the out-base); how many graphs were built before the
current HEAD; which Claude Code account jobs run as; and how far the semantic
tier has got.

Exit status is 0 when every invariant holds and 1 when one is broken.

`)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}

	st := store.Open(store.DefaultPath())
	set := st.Settings()
	outName, outBase := st.OutLocation()

	roots := set.Roots
	if len(roots) == 0 {
		roots = discover.DefaultRoots()
	}
	d := *depth
	if d <= 0 {
		d = set.Depth
	}

	rows, err := board.Scan(board.Options{
		Roots:      roots,
		Depth:      d,
		OutName:    outName,
		OutBase:    outBase,
		ShowHidden: set.ShowHidden,
		// The drift walk is the expensive half of a scan and no check here
		// asks about drift: what doctor wants is where each graph is, what
		// commit it came from and what tier it reached.
		SkipDrift: true,
		Override: func(path string) (string, bool, bool) {
			o := st.Override(path)
			return o.Out, o.ExcludeBatch, o.Pinned
		},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "ggraphify doctor:", err)
		return 1
	}

	// graphify's own probe runs a subprocess, so it gets a deadline of its
	// own: a doctor that hangs is a doctor nobody puts in a cron.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	rep := doctor.Run(doctor.Options{
		Rows:          rows,
		OutBase:       doctor.ExpandBase(outBase),
		Backend:       set.Backend,
		Model:         set.Model,
		OpenCodeModel: set.OpenCodeModel,
		ClaudeAccount: set.ClaudeAccount,
		Version:       gfy.Probe(ctx),
	})

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(rep); err != nil {
			fmt.Fprintln(os.Stderr, "ggraphify doctor:", err)
			return 1
		}
	} else {
		fmt.Print(rep.Text())
	}

	switch rep.Worst() {
	case doctor.Bad:
		return 1
	case doctor.Warn:
		if *strict {
			return 1
		}
	}
	return 0
}
