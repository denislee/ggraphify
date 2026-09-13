package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/dns/ggraphify/internal/board"
	"github.com/dns/ggraphify/internal/graphstate"
	"github.com/dns/ggraphify/internal/jobs"
)

// stateGlyph is the dot in the leading column. Shape carries the meaning as
// well as colour does, so the board is still readable without colour vision:
// an empty ring is "nothing here", a filled dot is "built", a warning sign is
// "broken".
func stateGlyph(s graphstate.State) string {
	switch s {
	case graphstate.StateNone:
		return "○"
	case graphstate.StateRaw:
		return "◍"
	case graphstate.StateStale:
		return "◐"
	case graphstate.StateFresh:
		return "●"
	case graphstate.StateBroken:
		return "△"
	case graphstate.StateRunning:
		return "◌"
	}
	return "·"
}

// stateClass is the CSS class carrying the colour. The classes are defined in
// loadCSS and are named for what they mean, not for the colour they happen to
// be, so the dark and light palettes can disagree.
func stateClass(s graphstate.State) string {
	switch s {
	case graphstate.StateRaw:
		return "st-raw"
	case graphstate.StateStale:
		return "st-stale"
	case graphstate.StateFresh:
		return "st-fresh"
	case graphstate.StateBroken:
		return "st-broken"
	case graphstate.StateRunning:
		return "st-running"
	}
	return "st-none"
}

// stateWord is the long form, for the filter chips and the tooltip.
func stateWord(s graphstate.State) string {
	switch s {
	case graphstate.StateNone:
		return "no graph"
	case graphstate.StateRaw:
		return "unlabeled"
	case graphstate.StateStale:
		return "stale"
	case graphstate.StateFresh:
		return "fresh"
	case graphstate.StateBroken:
		return "broken"
	case graphstate.StateRunning:
		return "running"
	}
	return "?"
}

// graphCell is the `2047n / 4835e` the Graph column renders.
func graphCell(g graphstate.Graph) string {
	if g.Nodes == 0 && g.Links == 0 {
		return ""
	}
	return fmt.Sprintf("%dn / %de", g.Nodes, g.Links)
}

// commCell is the community count, with a marker when the names are still
// graphify's placeholders — which is the difference between a graph that has
// had the expensive labelling pass run over it and one that has not.
func commCell(g graphstate.Graph) string {
	if g.Communities == 0 {
		return ""
	}
	if !g.Labeled {
		return fmt.Sprintf("%d unnamed", g.Communities)
	}
	return fmt.Sprint(g.Communities)
}

// jobCell is what the Job column shows for a row: the live job and how long it
// has been going, or how the last one ended.
func jobCell(s *jobs.Snapshot) (text, class string) {
	if s == nil {
		return "", ""
	}
	switch s.Status {
	case jobs.Queued:
		return "queued " + s.Kind, "st-none"
	case jobs.Running:
		return s.Kind + " " + shortDur(s.Elapsed()), "st-running"
	case jobs.Succeeded:
		return s.Kind + " ok", "st-fresh"
	case jobs.Canceled:
		return s.Kind + " cancelled", "st-none"
	default:
		return fmt.Sprintf("%s failed (%d)", s.Kind, s.Exit), "st-broken"
	}
}

// shortDur renders an elapsed time in as few characters as it can: a running
// job's cell is repainted every second and a widening string makes the column
// jitter.
func shortDur(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	default:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
}

// branchCell is the ref plus its short SHA, with a marker when the graph was
// built at a different commit. Drift and behind-HEAD are different questions —
// a branch switch moves the second without touching a file — so the board
// shows both rather than folding them together.
func branchCell(r board.Row) (text string, behind bool) {
	ref := r.Ref()
	if r.HeadSHA != "" && r.Branch != "" {
		ref += " " + r.ShortSHA()
	}
	return ref, r.Behind
}

// tooltip is the long explanation for a row, built on hover rather than on
// every repaint.
func tooltip(r board.Row) string {
	g := r.Graph
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n%s\n\n", r.Name, r.Path)
	fmt.Fprintf(&b, "State:  %s\n", stateWord(g.State))
	if g.Err != "" {
		fmt.Fprintf(&b, "        %s\n", g.Err)
	}
	fmt.Fprintf(&b, "Output: %s\n", g.Out)
	if r.IsWorktree {
		b.WriteString("        (linked worktree)\n")
	}
	if g.State != graphstate.StateNone {
		fmt.Fprintf(&b, "Graph:  %d nodes, %d links", g.Nodes, g.Links)
		if g.Hyperedges > 0 {
			fmt.Fprintf(&b, ", %d hyperedges", g.Hyperedges)
		}
		b.WriteString("\n")
		if g.Communities > 0 {
			names := "named"
			if !g.Labeled {
				names = "placeholder names — run `label`"
			}
			fmt.Fprintf(&b, "        %d communities, %s\n", g.Communities, names)
		}
		if !g.BuiltAt.IsZero() {
			fmt.Fprintf(&b, "Built:  %s\n", g.BuiltAt.Format("2006-01-02 15:04"))
		}
		if g.BuiltCommit != "" {
			commit := g.BuiltCommit
			if len(commit) > 7 {
				commit = commit[:7]
			}
			if r.Behind {
				fmt.Fprintf(&b, "        at %s — HEAD is now %s\n", commit, r.ShortSHA())
			} else {
				fmt.Fprintf(&b, "        at %s (current)\n", commit)
			}
		}
		if d := g.DriftString(); d != "" {
			fmt.Fprintf(&b, "Drift:  %s  (+added ~changed −removed vs manifest.json)\n", d)
			for _, line := range driftLines(g, 4) {
				fmt.Fprintf(&b, "        %s\n", line)
			}
		}
		if n := g.DriftFiles.Acked; n > 0 {
			fmt.Fprintf(&b, "Settled: %s graphify looked at and did not graph\n",
				plural(n, "path", "paths"))
		}
		fmt.Fprintf(&b, "Size:   %s", board.Bytes(g.SizeBytes))
		var have []string
		for _, h := range [...]struct {
			ok   bool
			name string
		}{
			{g.HasReport, "report"}, {g.HasHTML, "html"}, {g.HasTreeHTML, "tree"},
			{g.HasWiki, "wiki"}, {g.HasCallflow, "callflow"}, {g.HasCache, "cache"},
		} {
			if h.ok {
				have = append(have, h.name)
			}
		}
		if len(have) > 0 {
			fmt.Fprintf(&b, "\nHas:    %s", strings.Join(have, ", "))
		}
	}
	if r.Excluded {
		b.WriteString("\n\nExcluded from batch actions.")
	}
	return b.String()
}

// ellipsize keeps a label from forcing a column wider than its share.
func ellipsize(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n < 2 {
		return s[:n]
	}
	return s[:n-1] + "…"
}

// plural is the smallest possible English-correctness helper: the confirm
// dialogs quote counts and "1 repositories" reads as a bug.
func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

// driftLines names the files behind the drift counters, a few at a time. A
// count with no names is not actionable: "+9 −31, and every update leaves it
// exactly there" is the shape of the bug this answers, and the names are what
// tell you it is a 24 MB JSON and a credential directory rather than work
// graphify has not done yet.
func driftLines(g graphstate.Graph, max int) []string {
	var out []string
	add := func(mark string, paths []string, total int) {
		for _, p := range paths {
			if len(out) >= max {
				return
			}
			out = append(out, mark+" "+p)
		}
		if total > len(paths) && len(out) < max {
			out = append(out, fmt.Sprintf("%s … and %d more", mark, total-len(paths)))
		}
	}
	add("+", g.DriftFiles.Added, g.DriftAdded)
	add("~", g.DriftFiles.Changed, g.DriftChanged)
	add("−", g.DriftFiles.Removed, g.DriftRemoved)
	return out
}

// driftFiles is the detail pane's multi-line form of the same list.
func driftFiles(g graphstate.Graph) string {
	return strings.Join(driftLines(g, 12), "\n")
}

// settledNote explains the drift the board is deliberately not counting: paths
// a successful graphify run looked at and declined to graph. Without this line
// the suppression would be invisible, which is its own kind of lie.
func settledNote(g graphstate.Graph) string {
	n := g.DriftFiles.Acked
	if n == 0 {
		return ""
	}
	return plural(n, "path", "paths") + " graphify looked at and did not graph — " +
		"not counted until they change again"
}
