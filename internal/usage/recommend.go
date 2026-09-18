package usage

import (
	"fmt"
	"path/filepath"
	"sort"
	"time"

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

// UsedWithoutGraph is the sharpest form of the disagreement Recommend ranks:
// an agent worked in this checkout and there is no graphify graph to have
// answered it with.
//
// Broken counts as absent. A graphify-out/ directory that cannot be read is
// not a graph a query can use, and a board that showed it as "has one" would
// be telling a person the work is done when every query in it fell through to
// raw source.
//
// It is exported because three places need the same answer and must not each
// have their own: the board's Used column marks it, the board's filter selects
// on it, and `ggraphify-scan -gap` prints it.
func UsedWithoutGraph(r board.Row, uses int) bool {
	if uses <= 0 {
		return false
	}
	return r.Graph.State == graphstate.StateNone || r.Graph.State == graphstate.StateBroken
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

// --- failures -------------------------------------------------------------

// Blocked is one working directory where calls actually came back unusable.
//
// It is the evidence-backed twin of Rec. A recommendation is an inference —
// this checkout was used and has no graph, so extraction would have paid —
// whereas this is the thing itself: an agent ran `graphify query` here and was
// told there was no graph to answer from. That is why the two lists are not
// merged. A repository can appear in both, and when it does the failure is the
// stronger statement.
type Blocked struct {
	// Repo is the working directory the calls failed in, folded up to the
	// checkout that owns it when the board knows one.
	Repo string
	Name string
	// Fails is every failed call in the window; NoGraph is the subset that
	// failed for want of an index.
	Fails   int
	NoGraph int
	// Uses is how often either tool was called there at all, failures
	// included, which is what says whether the failures are the exception or
	// the whole story.
	Uses   int
	Reason Fail
	// Reasons is the readable breakdown — "no-graph 4 · timeout 1".
	Reasons string
	Last    time.Time
	// OnBoard is whether this is a checkout the board scans. A directory that
	// is not cannot be extracted until a scan root covers it.
	OnBoard bool
	// Action is what to do about it, or "" when there is nothing to build —
	// a call that failed on a timeout or a denied permission is not a missing
	// index, and offering to spend money on one would be a lie.
	Action Action
	Why    string
}

// Metered is shorthand for the UI, matching Rec.
func (b Blocked) Metered() bool { return b.Action.Metered() }

// Blockages ranks the directories where calls failed, worst first.
//
// The ranking is by failures and not by uses, because the question this list
// answers is not "where would an index pay off" — Recommend answers that —
// but "where did an agent already try and get nothing". A single failed query
// in a checkout with no graph is a stronger argument for extracting it than a
// hundred successful ones anywhere else.
//
// limit caps the result; zero means everything.
func Blockages(rows []board.Row, s Summary, limit int) []Blocked {
	byPath := make(map[string]*board.Row, len(rows))
	paths := make([]string, 0, len(rows))
	for i := range rows {
		byPath[rows[i].Path] = &rows[i]
		paths = append(paths, rows[i].Path)
	}

	// Uses and failures are both recorded per working directory; fold each up
	// to the checkout that owns it, so one repository worked in from three
	// subdirectories is one row and not three.
	uses := map[string]int{}
	for _, c := range s.Repos {
		uses[fold(paths, c.Name)] += c.Count
	}
	folded := map[string]RepoFail{}
	for cwd, f := range s.FailSplit {
		if f.Total == 0 {
			continue
		}
		key := fold(paths, cwd)
		cur := folded[key]
		if cur.Reasons == nil {
			cur.Reasons = map[Fail]int{}
		}
		cur.Total += f.Total
		cur.NoGraph += f.NoGraph
		cur.Graphify += f.Graphify
		cur.Graft += f.Graft
		for reason, n := range f.Reasons {
			cur.Reasons[reason] += n
		}
		if f.Last.After(cur.Last) {
			cur.Last = f.Last
		}
		folded[key] = cur
	}

	out := make([]Blocked, 0, len(folded))
	for path, f := range folded {
		b := Blocked{
			Repo: path, Name: filepath.Base(path), Fails: f.Total, NoGraph: f.NoGraph,
			Uses: uses[path], Reason: f.Top(), Reasons: f.ReasonLine(), Last: f.Last,
		}
		row := byPath[path]
		b.OnBoard = row != nil
		b.Action, b.Why = blockedAction(row, f)
		if row != nil {
			b.Name = row.Name
		}
		out = append(out, b)
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Fails != out[j].Fails {
			return out[i].Fails > out[j].Fails
		}
		if out[i].NoGraph != out[j].NoGraph {
			return out[i].NoGraph > out[j].NoGraph
		}
		return out[i].Repo < out[j].Repo
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// blockedAction is the verdict for one directory's failures: what to build, and
// the sentence that says why the failures are evidence for building it.
//
// The board's own state is what decides, not the failure alone. A repository
// whose graph is current and whose calls still failed has a different problem
// — a timeout, a denied permission, a bad command line — and saying "extract
// it" there would be advice that cannot help.
func blockedAction(r *board.Row, f RepoFail) (Action, string) {
	switch {
	case r == nil:
		return AddRoot, fmt.Sprintf("%s failed here, and it is not a checkout the board scans — "+
			"add its folder as a scan root before anything can be built for it", calls(f.Total))
	case r.Graph.State == graphstate.StateNone:
		return Extract, fmt.Sprintf("%s failed with no graphify graph here at all — "+
			"extraction is what creates the thing they were asking for", calls(f.Total))
	case r.Graph.State == graphstate.StateBroken:
		return Extract, fmt.Sprintf("%s failed and the graphify output here is unreadable — "+
			"a fresh extraction is the way out", calls(f.Total))
	case f.NoGraph > 0 && r.Graph.BuiltAt.After(f.Last.AddDate(0, 0, 1)):
		// The graph was built after the last call that failed for want of
		// one: the gap this row records has already been closed, and the row
		// is history rather than work. It is still shown — a person reading
		// "3 failed" wants to know it was three and that it is over — but
		// offering to spend money on another extraction would be answering a
		// question that was settled.
		return "", fmt.Sprintf("%s failed here before the graph was built %s — already settled",
			calls(f.Total), board.Age(r.Graph.BuiltAt))
	case f.NoGraph > 0 && r.Graft.State == graftstate.StateNone:
		return GraftBuild, fmt.Sprintf("%s came back with no index — the graphify graph is here, "+
			"the graft one is not, and graft build is free", calls(f.NoGraph))
	case f.NoGraph > 0 && r.Graph.Drift():
		return Update, fmt.Sprintf("%s came back with no index although a graph exists (%s behind) — "+
			"the free AST pass catches it up", calls(f.NoGraph), r.Graph.DriftString())
	case f.NoGraph > 0:
		return Extract, fmt.Sprintf("%s came back with no index although the board reads one here — "+
			"re-extracting is the way to settle it", calls(f.NoGraph))
	}
	// Everything left failed for a reason no index would have changed. It is
	// still shown: a repository whose every call times out is worth knowing
	// about, and silently dropping it would make the failure count on the
	// dashboard not add up.
	return "", fmt.Sprintf("%s failed here, and the indexes are current — %s. Nothing to build; "+
		"this is the harness or the command line, not a missing graph", calls(f.Total), f.ReasonLine())
}

// fold maps a working directory to the checkout that owns it, or leaves it as
// it is when no checkout does.
func fold(paths []string, cwd string) string {
	if owner, ok := ownerOf(paths, cwd); ok {
		return owner
	}
	return cwd
}

func calls(n int) string {
	if n == 1 {
		return "1 call"
	}
	return fmt.Sprintf("%d calls", n)
}
