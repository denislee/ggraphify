package ui

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/dns/ggraphify/internal/applog"
	"github.com/dns/ggraphify/internal/autofix"
	"github.com/dns/ggraphify/internal/board"
	"github.com/dns/ggraphify/internal/gfy"
	"github.com/dns/ggraphify/internal/globalgraph"
	"github.com/dns/ggraphify/internal/jobs"
	"github.com/dns/ggraphify/internal/store"
	"github.com/dns/ggraphify/internal/workspace"
)

// The fleet half of the auto-fix loop: the repairs whose subject is a
// DIRECTORY of checkouts rather than a checkout.
//
// autofix.go queues one chain per repository and that covers every defect a
// repository can have. Two things it cannot cover, and they are the two this
// file adds:
//
//   - ~/git/graft/workspace.json — the list of children `graft ask` at the
//     root fans a query across. Clone a repository and it is not in there;
//     delete one and it still is. Either way the query keeps working and
//     keeps being wrong by omission, which is the failure nobody reports.
//   - a global-graph member whose graph has been deleted. It has no board
//     row — that is the point — so the per-repository pass cannot see it, and
//     its nodes go on citing file:line in a directory that is gone.
//
// Both are gated on the fleet roots in settings, which is where the judgement
// "which repositories do I want to query across" now lives: once, about a
// directory, instead of once per clone forever.

// fleetTick is the fleet pass, called from autoFixTick with the same rows.
//
// Split across the thread boundary for the same reason the repository pass
// is: gathering rows is a main-thread read, but reading each root's
// workspace.json and stat-ing every member's graph is filesystem I/O and has
// no business on the GTK thread.
func (a *App) fleetTick(set store.Settings, rows []board.Row) {
	if !set.AutoFixEnroll() {
		return
	}
	roots := a.fleetScan(rows)
	if len(roots) == 0 {
		return
	}
	go func() {
		pol := autoFixPolicy(set)
		inspected := make([]autofix.Root, 0, len(roots))
		for _, r := range roots {
			inspected = append(inspected, autofix.Root{
				Path:      r.path,
				Workspace: workspace.Inspect(r.path, r.children),
				Busy:      r.busy,
			})
		}
		m := a.globals.Load()
		stranded := strandedMembers(m)
		a.autofix.Prune(fleetKeep(inspected, m))
		actions, skips := a.autofix.PlanFleet(inspected, stranded, pol)
		idle(func() {
			if len(actions) == 0 {
				logAutoFixSkips(skips)
				return
			}
			a.runFleetFix(actions)
		})
	}()
}

// rootScan is one root as the main thread can see it, before any file is read.
type rootScan struct {
	path string
	// children is every direct-child checkout under the root, by directory
	// name — the set graft would federate.
	children []string
	busy     bool
}

// fleetScan gathers the configured roots and what the board found under each.
// Main thread: it reads the row model and the job table.
func (a *App) fleetScan(rows []board.Row) []rootScan {
	if a.opts.Store == nil {
		return nil
	}
	roots := a.opts.Store.Settings().Fleets()
	if len(roots) == 0 {
		return nil
	}
	out := make([]rootScan, 0, len(roots))
	for _, root := range roots {
		scan := rootScan{path: root, busy: a.busyAt(root)}
		for _, r := range rows {
			name, ok := fleetChild(root, r)
			if !ok {
				continue
			}
			scan.children = append(scan.children, name)
			// A `graft build` at the root rewrites every child's graft/
			// directory. One running against a child is therefore a writer
			// for the same files, and two graft builds in one tree is not a
			// race this board needs to find out about experimentally.
			if !scan.busy && a.busyAt(r.Path) {
				scan.busy = true
			}
		}
		out = append(out, scan)
	}
	return out
}

// fleetChild reports whether a row is a direct-child checkout of root, and
// under what name graft would federate it.
//
// Three exclusions, each of which would otherwise produce drift that no
// rebuild can ever clear — the shape that burns an attempt budget every
// cooldown and then declares the root stuck:
//
//   - not a direct child. graft federates one level; a row nested deeper is
//     reached through its parent, not listed at the top.
//   - no git. graft's workspace is a federation of repositories, so a
//     graph-only row (the plan-doc corpus, say) is never going to appear in
//     workspace.json no matter how often it is rebuilt.
//   - a dotted name. graft skips dot-directories unconditionally — its own
//     help says they are not overridable — so .github would read as missing
//     forever.
func fleetChild(root string, r board.Row) (string, bool) {
	if r.NoGit || filepath.Dir(r.Path) != filepath.Clean(root) {
		return "", false
	}
	name := filepath.Base(r.Path)
	if name == "" || strings.HasPrefix(name, ".") {
		return "", false
	}
	return name, true
}

// busyAt reports whether anything is already running against a path.
func (a *App) busyAt(path string) bool {
	if job := a.jobFor(path); job != nil && !job.Status.Done() {
		return true
	}
	if a.runner != nil {
		if _, busy := a.runner.Busy(path); busy {
			return true
		}
	}
	return a.settling[path]
}

// strandedMembers is every global-graph member whose graph is gone.
//
// The test is deliberately stricter than "graph.json is missing", and the
// extra condition is the whole safety of this function. Under a central
// output layout every member's source is ~/knowledge/<slug>/graph.json, so
// re-pointing the output base — one field in settings — makes every recorded
// source path miss at once. A prune is not undoable without re-extracting the
// repository, and a loop that dropped 130 members because a path setting
// moved would be the one bug here nobody could walk back.
//
// Requiring the CONTAINING DIRECTORY to be gone as well separates the two
// cases. A re-pointed base leaves the old directories sitting there full of
// graphs; a repository that was actually removed takes its directory with it.
func strandedMembers(m *globalgraph.Manifest) []autofix.Stranded {
	if m == nil || !m.Exists || m.Err != nil {
		// An unreadable manifest is not an empty one. Deriving "these members
		// are gone" from a parse failure would prune the whole graph.
		return nil
	}
	var out []autofix.Stranded
	for _, e := range m.Entries {
		if strings.TrimSpace(e.Source) == "" {
			continue
		}
		if _, err := os.Stat(e.Source); err == nil || !os.IsNotExist(err) {
			continue
		}
		dir := filepath.Dir(e.Source)
		if _, err := os.Stat(dir); err == nil || !os.IsNotExist(err) {
			continue
		}
		out = append(out, autofix.Stranded{Tag: e.Tag, Source: e.Source})
	}
	return out
}

// runFleetFix queues one job per fleet action — never a chain, because no
// fleet repair is a precondition for another and a failed workspace rebuild
// must not be what stops a stranded member being dropped.
func (a *App) runFleetFix(actions []autofix.FleetAction) {
	for _, act := range actions {
		act := act

		p := gfy.Params{Repo: act.Path, Tag: act.Tag}
		act.Step.Apply(&p)
		label := gfy.Title(act.Step.Kind) + " · "
		subject := act.Tag
		if subject == "" {
			subject = board.Tilde(act.Path)
			// The graft build is pointed at the root itself, which is the
			// one place p.Repo is a directory of checkouts rather than one.
			p.Repo = act.Path
		} else {
			// A prune's repository may not exist any more; the command reads
			// and writes only ~/.graphify, and leaving Repo blank is what
			// every other global-remove on this board does.
			p.Repo = ""
		}
		label += subject

		step := jobs.ChainStep{
			Kind:   act.Step.Kind,
			Repo:   p.Repo,
			Label:  "Auto-fix fleet · " + label,
			Params: p,
		}
		if p.Repo != "" && a.opts.Store != nil {
			step.Env = a.opts.Store.Overlay(p.Repo)
		}

		applog.Infof("auto-fix fleet %s (attempt %d): %s — %s",
			subject, act.Attempt, act.Step.Kind, act.Why)

		global := gfy.Global(act.Step.Kind)
		a.runner.SubmitChain([]jobs.ChainStep{step}, func(res jobs.ChainResult) {
			errText := ""
			if res.Err != nil {
				errText = res.Err.Error()
			}
			a.autofix.FleetDone(act, errText)
			idle(func() {
				if global {
					a.globals.Invalidate()
					if a.globalPane != nil {
						a.globalPane.reload()
					}
				}
				if res.Err != nil {
					applog.Errorf("auto-fix fleet %s: %v", subject, res.Err)
					a.toastf("auto-fix fleet %s: %v", subject, res.Err)
				} else {
					applog.Infof("auto-fix fleet %s: done", subject)
				}
				a.refresh(false)
			})
		})
	}

	names := make([]string, 0, len(actions))
	for _, act := range actions {
		if act.Tag != "" {
			names = append(names, act.Tag)
			continue
		}
		names = append(names, board.Tilde(act.Path))
	}
	a.toastf("auto-fix fleet: %s (free)", strings.Join(names, ", "))
	a.refreshStatus()
}

// autoFixEnrollSubtitle is the enrolment switch's explanation, with the roots
// it is bounded by named in it.
//
// The roots are in the sentence rather than left to the field below because
// this is the one switch on the page that changes a set the user assembled
// rather than refreshing a derived file. "Enrols new checkouts" is alarming
// without them and ordinary with them.
func (a *App) autoFixEnrollSubtitle(set store.Settings) string {
	roots := set.Fleets()
	if len(roots) == 0 {
		return "No fleet roots, so this does nothing. Name a directory below — the one " +
			"holding the checkouts you want to query across — and its repositories are " +
			"kept in the global graph and in graft's workspace federation for you."
	}
	names := make([]string, 0, len(roots))
	for _, r := range roots {
		names = append(names, board.Tilde(r))
	}
	return "A repository under " + strings.Join(names, ", ") + " is merged into the global " +
		"graph when it has a graph to merge, a member whose graph has been deleted is " +
		"dropped, and graft's workspace.json at each root is re-federated so a query there " +
		"covers the checkouts that are actually present. Free. It never touches a " +
		"repository outside those roots — that is what the list below is for."
}

// fleetKeep is the Engine.Prune predicate for the fleet pass. A workspace
// record lives while its root is still a fleet root; a prune record lives
// while its tag is still in the manifest — once the member is gone the prune
// succeeded and there is nothing left to remember. An unreadable manifest
// keeps every prune record, for the same reason strandedMembers derives
// nothing from one. Repository records belong to the repository pass.
func fleetKeep(roots []autofix.Root, m *globalgraph.Manifest) func(string) bool {
	rootSet := make(map[string]bool, len(roots))
	for _, r := range roots {
		rootSet[r.Path] = true
	}
	var tags map[string]bool
	if m != nil && m.Exists && m.Err == nil {
		tags = make(map[string]bool, len(m.Entries))
		for _, e := range m.Entries {
			tags[e.Tag] = true
		}
	}
	return func(key string) bool {
		kind, subject, fleet := autofix.ParseFleetKey(key)
		switch {
		case !fleet:
			return true
		case kind == "workspace":
			return rootSet[subject]
		case kind == "prune":
			return tags == nil || tags[subject]
		}
		return true
	}
}
