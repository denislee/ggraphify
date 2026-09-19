// Package doctor asserts the invariants that keep this application, the
// graphs it builds and the agents that read them pointing at the same things.
//
// Each of them was a real disagreement on this machine, and each was invisible
// until somebody went looking:
//
//   - graphs are written to a central out-base, and an agent that does not
//     know that reads an empty in-tree directory instead;
//   - a backend can be pinned to a server that is not running, and nothing
//     says so until the next metered job — which, on a board that mostly runs
//     the free lane, may be weeks away;
//   - a graph built before the current HEAD still answers, with file:line
//     spans pointing at code the checkout has moved past;
//   - the Claude Code account a job runs as is a setting, and it is not the
//     default one here;
//   - a semantic tier that has never completed looks exactly like a healthy
//     AST graph from the outside.
//
// So there is one command that checks all of them, for a human or an agent
// that should not have to remember the list. Nothing here imports GTK, and
// nothing here writes: it reads state and renders a verdict.
package doctor

import (
	"path/filepath"
	"strings"
	"time"

	"github.com/dns/ggraphify/internal/board"
	"github.com/dns/ggraphify/internal/discover"
	"github.com/dns/ggraphify/internal/gfy"
	"github.com/dns/ggraphify/internal/graphstate"
	"github.com/dns/ggraphify/internal/knowledge"
)

// Level is how bad a check's finding is.
type Level int

const (
	// OK is the invariant holding.
	OK Level = iota
	// Warn is something worth doing that is not a broken invariant: work left
	// undone rather than a claim that is false. A warning does not fail the
	// command unless the caller asks it to.
	Warn
	// Bad is an invariant that does not hold. It is what makes `doctor` exit
	// non-zero, so a cron or a pre-flight can gate on it.
	Bad
)

// String is the marker the report prints.
func (l Level) String() string {
	switch l {
	case Warn:
		return "warn"
	case Bad:
		return "BAD"
	}
	return "ok"
}

// Check is one invariant's verdict: what was asked, what was found, and — when
// something is wrong — what to do about it.
type Check struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Detail string `json:"detail,omitempty"`
	Do     string `json:"do,omitempty"`
	Level  Level  `json:"-"`
	Status string `json:"status"`
}

// Report is the whole run.
type Report struct {
	Checks []Check `json:"checks"`
}

// Worst is the highest level any check reached.
func (r Report) Worst() Level {
	worst := OK
	for _, c := range r.Checks {
		if c.Level > worst {
			worst = c.Level
		}
	}
	return worst
}

// Options is what the command line decided.
type Options struct {
	// Rows is the board, already scanned. doctor does not scan for itself:
	// the caller owns the roots, the depth and the out-base, and a second
	// resolution of those here is exactly the kind of drift this package
	// exists to catch.
	Rows []board.Row
	// OutBase is where graphs live, resolved and expanded.
	OutBase string
	// Backend, Model, OpenCodeModel and ClaudeAccount are the settings a
	// metered job would run with.
	Backend       string
	Model         string
	OpenCodeModel string
	ClaudeAccount string
	// Version is graphify's own probe result.
	Version gfy.Version
}

// Run evaluates every invariant.
func Run(opts Options) Report {
	var r Report
	add := func(c Check) {
		c.Status = c.Level.String()
		r.Checks = append(r.Checks, c)
	}

	add(graphifyCheck(opts))
	add(backendCheck(opts))
	add(storageCheck(opts))
	add(stalenessCheck(opts))
	add(accountCheck(opts))
	add(tierCheck(opts))
	return r
}

func graphifyCheck(opts Options) Check {
	v := opts.Version
	switch {
	case !v.Found:
		return Check{
			Name: "graphify", Value: "NOT FOUND", Detail: v.Err, Level: Bad,
			Do: "install graphify, or put it on this session's PATH",
		}
	case !v.Supported():
		return Check{
			Name: "graphify", Value: v.Number + " at " + gfy.Tilde(v.Bin), Level: Warn,
			Detail: "ggraphify's command builders were written against " + gfy.BuiltAgainst,
			Do:     "check the argv of one job before trusting a sweep",
		}
	}
	return Check{Name: "graphify", Value: v.Number + " at " + gfy.Tilde(v.Bin), Level: OK}
}

func backendCheck(opts Options) Check {
	model := opts.Model
	if gfy.EffectiveBackend(opts.Backend) == gfy.OpenCodeBackend {
		model = opts.OpenCodeModel
	}
	eff, ready, why := gfy.BackendVerdict(opts.Backend, model)
	name := eff
	if name == "" {
		name = "none detectable"
	}
	if model != "" {
		name += " / " + model
	}
	if ready {
		return Check{Name: "backend", Value: name, Detail: why, Level: OK}
	}
	do := "no backend on this machine is ready; export an API key, log into the Claude Code CLI, " +
		"set " + gfy.OpenCodeKeyVar + ", or start a local model server"
	if alts := gfy.ReadyBackends(eff, model); len(alts) > 0 {
		names := make([]string, 0, len(alts))
		for _, a := range alts {
			n := a.Name
			if n == "" {
				n = "auto-detect"
			}
			names = append(names, n)
		}
		do = "ready instead: " + strings.Join(names, ", ")
	}
	return Check{
		Name: "backend", Value: name + " — NOT READY", Detail: why, Do: do, Level: Bad,
	}
}

// storageCheck is the one that was actually costing something: 212 graphs that
// no agent had ever read, because the place they live was not discoverable
// from the repository they describe.
func storageCheck(opts Options) Check {
	graphed := 0
	for _, row := range opts.Rows {
		if row.Graph.State != graphstate.StateNone {
			graphed++
		}
	}
	if opts.OutBase == "" {
		return Check{
			Name:  "out_base",
			Value: "in-tree",
			Detail: itoa(graphed) + " graph(s), each inside its own checkout — already discoverable " +
				"from the repository it describes, so there is no index to publish",
			Level: OK,
		}
	}
	path := knowledge.Path(opts.OutBase)
	ix, err := knowledge.Load(path)
	switch {
	case err != nil:
		return Check{
			Name: "out_base", Value: gfy.Tilde(opts.OutBase), Level: Bad,
			Detail: "the index at " + gfy.Tilde(path) + " is unreadable: " + err.Error(),
			Do:     "ggraphify-scan -index",
		}
	case len(ix.Entries) == 0:
		return Check{
			Name: "out_base", Value: gfy.Tilde(opts.OutBase), Level: Bad,
			Detail: itoa(graphed) + " graph(s) live here and nothing points back at them from the " +
				"checkouts they describe; " + gfy.Tilde(path) + " has not been written",
			Do: "ggraphify-scan -index",
		}
	}
	missing := 0
	for _, row := range opts.Rows {
		if row.Graph.State == graphstate.StateNone {
			continue
		}
		if _, ok := ix.Entries[row.Path]; !ok {
			missing++
		}
	}
	detail := itoa(graphed) + " graph(s), " + itoa(len(ix.Entries)) + " in " + gfy.Tilde(path) +
		", written " + age(ix.Generated) + " ago"
	if missing > 0 {
		return Check{
			Name: "out_base", Value: gfy.Tilde(opts.OutBase), Level: Warn,
			Detail: detail + "; " + itoa(missing) + " graphed checkout(s) are not in it",
			Do:     "ggraphify-scan -index",
		}
	}
	return Check{
		Name: "out_base", Value: gfy.Tilde(opts.OutBase), Level: OK,
		Detail: detail + `; look one up with: jq -r '.entries["'"$PWD"'"].graph' ` + gfy.Tilde(path),
	}
}

func stalenessCheck(opts Options) Check {
	c := board.Summarize(opts.Rows)
	value := itoa(c.Behind) + " of " + itoa(c.Graphed) + " graphs behind HEAD"
	if c.Behind == 0 {
		return Check{Name: "staleness", Value: value, Level: OK}
	}
	return Check{
		Name: "staleness", Value: value, Level: Warn,
		Detail: "a graph built before the current HEAD still answers, with spans pointing at " +
			"lines the checkout has moved past",
		Do: "Ctrl+U on the board, or `ggraphify-job -kind update -repo <path>` — free, AST only",
	}
}

func accountCheck(opts Options) Check {
	acc := gfy.ClaudeAccountFor(opts.ClaudeAccount)
	detail := ""
	level := OK
	switch {
	case acc.Missing:
		detail = "that directory does not exist"
		level = Bad
	case !acc.Default:
		detail = "not the default ~/.claude — its hooks, skills, settings and login are the ones " +
			"a claude-cli job runs with, and a Claude Code session in a checkout runs with " +
			"different ones"
	case !acc.Credential:
		detail = "no stored login file in that directory"
		level = Warn
	}
	do := ""
	if level == Bad {
		do = "pick an account in Settings ▸ Claude Code"
	}
	return Check{
		Name: "claude account", Value: acc.Name + " (" + acc.Dir + ")",
		Detail: detail, Do: do, Level: level,
	}
}

// tierCheck is the indirect symptom of a backend that has never worked: every
// graph at the AST tier, community names that are raw symbols, and no line
// anywhere saying the semantic pass has not run.
func tierCheck(opts Options) Check {
	var files, semantic, ast, graphs int
	for _, row := range opts.Rows {
		if row.Graph.State == graphstate.StateNone || row.Graph.Out == "" {
			continue
		}
		cov, ok := graphstate.CoverageOf(row.Graph.Out)
		if !ok || cov.Files == 0 {
			continue
		}
		graphs++
		files += cov.Files
		semantic += cov.Semantic
		if cov.Semantic == 0 {
			ast++
		}
	}
	if graphs == 0 {
		return Check{Name: "semantic tier", Value: "no graph to measure", Level: Warn}
	}
	pct := semantic * 100 / files
	value := itoa(pct) + "% of " + itoa(files) + " files carry a semantic hash"
	switch {
	case semantic == 0:
		return Check{
			Name: "semantic tier", Value: value, Level: Warn,
			Detail: "no metered pass has ever completed here: community names are raw symbols " +
				"rather than prose, and docs, images and concept nodes are absent from every graph",
			Do: "fix the backend first, then `extract` (METERED) or a local-model sweep",
		}
	case ast > 0:
		return Check{
			Name: "semantic tier", Value: value, Level: Warn,
			Detail: itoa(ast) + " of " + itoa(graphs) + " graphs are AST-only",
			Do:     "run the metered pass on the ones that matter; the rest stay free and structural",
		}
	}
	return Check{Name: "semantic tier", Value: value, Level: OK}
}

// Text renders the report the way the plan asked for it: one line per
// invariant, aligned, with the remedy after an arrow when there is one.
func (r Report) Text() string {
	width := 0
	for _, c := range r.Checks {
		if len(c.Name) > width {
			width = len(c.Name)
		}
	}
	var b strings.Builder
	for _, c := range r.Checks {
		b.WriteString("  ")
		b.WriteString(c.Name)
		b.WriteString(strings.Repeat(" ", width-len(c.Name)+2))
		b.WriteString(c.Value)
		if c.Level != OK {
			b.WriteString("   [")
			b.WriteString(c.Level.String())
			b.WriteString("]")
		}
		b.WriteByte('\n')
		pad := strings.Repeat(" ", width+4)
		if c.Detail != "" {
			b.WriteString(pad)
			b.WriteString(c.Detail)
			b.WriteByte('\n')
		}
		if c.Do != "" {
			b.WriteString(pad)
			b.WriteString("→ ")
			b.WriteString(c.Do)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// age is a coarse duration for a report line.
func age(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return itoa(int(d.Seconds())) + "s"
	case d < time.Hour:
		return itoa(int(d.Minutes())) + "m"
	case d < 24*time.Hour:
		return itoa(int(d.Hours())) + "h"
	}
	return itoa(int(d.Hours()/24)) + "d"
}

func itoa(n int) string { return gfy.Itoa(n) }

// ExpandBase is the out-base as a path on disk, for a caller holding the raw
// setting. It is here so the command and the board expand it identically.
func ExpandBase(base string) string {
	b := discover.Expand(strings.TrimSpace(base))
	if b == "" {
		return ""
	}
	return filepath.Clean(b)
}
