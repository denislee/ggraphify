package usage

import (
	"fmt"
	"path/filepath"
	"sort"

	"github.com/dns/ggraphify/internal/board"
	"github.com/dns/ggraphify/internal/graftstate"
	"github.com/dns/ggraphify/internal/graphstate"
)

// A recommendation is the join the rest of this application cannot make on its
// own: the board knows which repositories have an index, this package knows
// which ones agents actually work in, and the interesting repositories are the
// ones where those two answers disagree.
//
// The direction that matters is usage without an index. A checkout an agent
// ran forty queries in with no graph at all is not "unconfigured" in the
// abstract — it is a specific extraction that would have paid for itself
// forty times. Ranking by usage is what makes the list a work queue rather
// than an audit.
//
// Nothing here recommends spending money quietly: every recommendation carries
// whether the command it names is free (an AST pass) or metered (an LLM call),
// and the UI refuses to run a metered one without the same confirm every other
// metered action goes through.

// Action is what a recommendation asks for. The values are the job kinds
// internal/jobs already knows, so the UI can run one without a translation
// table — except AddRoot, which is a settings change and not a job.
type Action string

const (
	// Extract is the full LLM extraction: what a repository with no graph at
	// all needs. Metered.
	Extract Action = "extract"
	// Update is the free AST re-extraction, for a graph the tree has moved
	// under.
	Update Action = "update"
	// Label names the communities of a graph that has placeholders. Metered.
	Label Action = "label"
	// GraftBuild builds or refreshes the graft wiring index. Free.
	GraftBuild Action = "graft-build"
	// AddRoot is not a job: the directory an agent worked in is not a boarded
	// checkout, so there is nothing to run until a scan root covers it.
	AddRoot Action = "add-root"
)

// Metered reports whether acting on this recommendation costs money.
func (a Action) Metered() bool { return a == Extract || a == Label }

// Command is the graphify or graft invocation a recommendation stands for,
// for a report that has no buttons.
func (a Action) Command() string {
	switch a {
	case Extract:
		return "graphify extract ."
	case Update:
		return "graphify update ."
	case Label:
		return "graphify label ."
	case GraftBuild:
		return "graft build"
	}
	return ""
}

// Rec is one recommendation.
type Rec struct {
	// Repo is the checkout it applies to, or — for AddRoot — the directory an
	// agent was working in.
	Repo string
	// Name is what to show: the repository's name, or the directory.
	Name string
	// Uses is how often either tool was used there in the window. It is the
	// whole argument for the recommendation, so it is never zero.
	Uses   int
	Action Action
	// Why is the one line under the name: what is missing, in the terms the
	// board's own columns use.
	Why string
}

// Metered is shorthand for the UI.
func (r Rec) Metered() bool { return r.Action.Metered() }

// Recommend ranks the repositories where agent usage and index state
// disagree, worst first by usage.
//
// One recommendation per repository, and it is the FIRST thing that is wrong
// in the order a person would fix them: a missing graph before a stale one, a
// stale one before a missing graft index, and cosmetics — unnamed communities
// — last. A repository with nothing wrong produces nothing, which is why a
// well-kept machine shows an empty list rather than a list of nits.
//
// limit caps the result; zero means everything.
func Recommend(rows []board.Row, s Summary, limit int) []Rec {
	byPath := make(map[string]*board.Row, len(rows))
	paths := make([]string, 0, len(rows))
	for i := range rows {
		byPath[rows[i].Path] = &rows[i]
		paths = append(paths, rows[i].Path)
	}

	// Usage is recorded per working directory; several of them can belong to
	// one checkout, so they are folded together before anything is judged.
	uses := map[string]int{}
	var loose []Count
	for _, c := range s.Repos {
		if owner, ok := ownerOf(paths, c.Name); ok {
			uses[owner] += c.Count
			continue
		}
		loose = append(loose, c)
	}

	var out []Rec
	for path, n := range uses {
		if r := recFor(byPath[path], n); r != nil {
			out = append(out, *r)
		}
	}
	for _, c := range loose {
		if c.Name == "" || containsCheckout(paths, c.Name) {
			// A directory that boarded checkouts live UNDER is not a missing
			// repository — it is ~/git, and an agent ran one command there.
			// Recommending a scan root for it would be advice to add a root
			// that already exists.
			continue
		}
		out = append(out, Rec{
			Repo: c.Name, Name: filepath.Base(c.Name), Uses: c.Count, Action: AddRoot,
			Why: "an agent worked here, but it is not a checkout the board scans — add its folder as a scan root",
		})
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Uses != out[j].Uses {
			return out[i].Uses > out[j].Uses
		}
		return out[i].Repo < out[j].Repo
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// recFor is the per-repository verdict.
func recFor(r *board.Row, uses int) *Rec {
	if r == nil || uses <= 0 {
		return nil
	}
	rec := func(a Action, why string) *Rec {
		return &Rec{Repo: r.Path, Name: r.Name, Uses: uses, Action: a, Why: why}
	}
	switch {
	case r.Graph.State == graphstate.StateNone:
		return rec(Extract, fmt.Sprintf("used %s, and has no graphify graph at all", times(uses)))
	case r.Graph.State == graphstate.StateBroken:
		return rec(Extract, "its graphify output is there but unreadable — a fresh extraction is the way out")
	case r.Graph.NeedsUpdate:
		return rec(Extract, "graphify flagged a semantic re-extraction as pending")
	case r.Graph.Drift():
		return rec(Update, fmt.Sprintf("the tree has moved under the graph (%s) — the free AST pass catches it up", r.Graph.DriftString()))
	case r.Graft.State == graftstate.StateNone:
		return rec(GraftBuild, fmt.Sprintf("used %s, with a graphify graph but no graft index — graft build is free", times(uses)))
	case r.Graft.NeedsBuild():
		return rec(GraftBuild, "its graft index has fallen behind the tree — rebuilding is free")
	case r.Graph.Communities > 0 && !r.Graph.Labeled:
		return rec(Label, fmt.Sprintf("%d communities still carry placeholder names", r.Graph.Communities))
	}
	return nil
}

// containsCheckout reports whether any boarded checkout lives below dir.
func containsCheckout(paths []string, dir string) bool {
	for _, p := range paths {
		if RepoMatch(dir, p) {
			return true
		}
	}
	return false
}

func times(n int) string {
	if n == 1 {
		return "once"
	}
	return fmt.Sprintf("%d times", n)
}
