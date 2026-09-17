package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/dns/ggraphify/internal/board"
	"github.com/dns/ggraphify/internal/graftstate"
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

// graftGlyph is the shape the Graft column leads with. It is the same
// vocabulary the state dot uses — empty ring for nothing, half-filled for
// "the tree has moved", filled for built, triangle for broken — so the two
// columns can be read together without learning a second alphabet.
func graftGlyph(s graftstate.State) string {
	switch s {
	case graftstate.StateNone:
		return "○"
	case graftstate.StateRaw:
		return "◍"
	case graftstate.StateStale:
		return "◐"
	case graftstate.StateFresh:
		return "●"
	case graftstate.StateBroken:
		return "△"
	}
	return "·"
}

// graftClass is the colour, reusing the state classes: the meanings line up
// one for one, and a second palette for the same five verdicts would only be
// a second thing to keep in step with the theme.
func graftClass(s graftstate.State) string {
	switch s {
	case graftstate.StateRaw:
		return "st-raw"
	case graftstate.StateStale:
		return "st-stale"
	case graftstate.StateFresh:
		return "st-fresh"
	case graftstate.StateBroken:
		return "st-broken"
	}
	return "st-none"
}

// graftWord is the long form, for the tooltip and the sync dialog.
func graftWord(s graftstate.State) string {
	switch s {
	case graftstate.StateNone:
		return "no graft index"
	case graftstate.StateRaw:
		return "wiring only"
	case graftstate.StateStale:
		return "stale"
	case graftstate.StateFresh:
		return "fresh"
	case graftstate.StateBroken:
		return "broken"
	}
	return "?"
}

// graftCell is what the Graft column renders: the shape, the node count, and
// the drift when there is any. A repository graft has never run on is a bare
// ring — the row says "nothing here" without spending a word on it.
func graftCell(i graftstate.Index) (text, class string) {
	class = graftClass(i.State)
	switch i.State {
	case graftstate.StateNone:
		return graftGlyph(i.State), class
	case graftstate.StateBroken:
		return graftGlyph(i.State) + " broken", class
	}
	text = graftGlyph(i.State)
	if i.Nodes > 0 {
		text += " " + fmt.Sprintf("%dn", i.Nodes)
	}
	if d := i.DriftString(); d != "" {
		text += " " + d
	}
	return text, class
}

// graftFact is the Graft line in the detail pane's fact grid: the word the
// column's glyph stands for, the node count behind it, and the drift. The
// column has room for a glyph and a number; this has room to say what they
// mean, which is the only difference between them.
func graftFact(i graftstate.Index) string {
	if i.State == graftstate.StateNone {
		return ""
	}
	parts := []string{graftWord(i.State)}
	if i.Nodes > 0 {
		parts = append(parts, fmt.Sprintf("%d nodes", i.Nodes))
	}
	if i.Files > 0 {
		parts = append(parts, fmt.Sprintf("%d files", i.Files))
	}
	if d := i.DriftString(); d != "" {
		parts = append(parts, d)
	}
	if i.Err != "" {
		parts = append(parts, i.Err)
	}
	return strings.Join(parts, " · ")
}

// graftExposureNote is the one thing about a graft index that is invisible in
// its own column and matters to the other one: whether its cards are in the
// working tree where graphify will index them.
//
// It is a note on a fact and not an Issue, deliberately. Issues are what the
// Fix button drives to zero, and this is not fixable by any graphify command —
// the remedy is one line in a .gitignore, which is the repository's business.
// Making it an issue would put a permanently unhealthy row on the board.
func graftExposureNote(i graftstate.Index) string {
	if !i.Exposed || i.State == graftstate.StateNone {
		return ""
	}
	return "graft/ is not in .gitignore — its cards are in the working tree, and graphify's " +
		"next extract will index one per source file as if it were source"
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
		if s.Held {
			return "held " + s.Kind, "st-stale"
		}
		return "queued " + s.Kind, "st-none"
	case jobs.Running:
		if s.Paused {
			return s.Kind + " paused " + shortDur(s.Elapsed()), "st-none"
		}
		if n := progressNote(s); n != "" {
			return s.Kind + " " + n + " " + shortDur(s.Elapsed()), "st-running"
		}
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
	b.WriteString("\n\n" + graftTooltip(r.Graft))
	if r.Excluded {
		b.WriteString("\n\nExcluded from batch actions.")
	}
	return b.String()
}

// graftTooltip is the Graft column's paragraph: what graft knows about this
// checkout, and what the drift number does and does not mean.
func graftTooltip(i graftstate.Index) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Graft:  %s", graftWord(i.State))
	if i.State == graftstate.StateNone {
		b.WriteString("\n        `graft build` has never run here")
		return b.String()
	}
	if i.Err != "" {
		fmt.Fprintf(&b, "\n        %s", i.Err)
	}
	if i.Nodes > 0 || i.Edges > 0 {
		fmt.Fprintf(&b, "\n        %d nodes, %d edges", i.Nodes, i.Edges)
	}
	if len(i.Languages) > 0 {
		fmt.Fprintf(&b, " (%s)", strings.Join(i.Languages, ", "))
	}
	if i.Files > 0 {
		fmt.Fprintf(&b, "\n        %s indexed", plural(i.Files, "file", "files"))
	}
	if !i.Deep {
		b.WriteString("\n        wiring only — no --deep concept layer (that pass is METERED and ggraphify does not run it)")
	}
	if !i.BuiltAt.IsZero() {
		fmt.Fprintf(&b, "\n        built %s", i.BuiltAt.Format("2006-01-02 15:04"))
	}
	if i.SizeBytes > 0 {
		fmt.Fprintf(&b, ", %s", board.Bytes(i.SizeBytes))
	}
	if d := i.DriftString(); d != "" {
		fmt.Fprintf(&b, "\n        drift %s  (~changed −removed, against graft's own fingerprint)", d)
		for _, line := range i.DriftFiles {
			if strings.Count(b.String(), "\n") > 14 {
				break
			}
			fmt.Fprintf(&b, "\n        %s", line)
		}
		if i.Unverified > 0 {
			fmt.Fprintf(&b, "\n        %d assumed changed on its timestamp alone (re-hash budget spent)", i.Unverified)
		}
		b.WriteString("\n        Added files are not counted: only graft knows which files it indexes.")
	}
	return b.String()
}

// escapeMarkup makes a string safe to hand to a widget that parses Pango
// markup — an AdwActionRow subtitle, which is most of what the two integration
// groups render.
//
// It is not hypothetical: every check detail is built from paths and error
// text, and a subtitle reading "Rebuild <repo>/graft" was silently dropped by
// GTK with "Element “markup” was closed, but the currently open element is
// “repo”". A path containing & or a quote fails the same way.
func escapeMarkup(s string) string {
	return strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		"'", "&apos;",
		`"`, "&quot;",
	).Replace(s)
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
