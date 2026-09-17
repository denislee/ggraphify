package ui

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/diamondburned/gotk4-adwaita/pkg/adw"
	coreglib "github.com/diamondburned/gotk4/pkg/core/glib"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"

	"github.com/dns/ggraphify/internal/applog"
	"github.com/dns/ggraphify/internal/board"
	"github.com/dns/ggraphify/internal/gfy"
	"github.com/dns/ggraphify/internal/usage"
)

// The usage half of the board answers the question the rest of it cannot:
// not "does this repository have an index" but "does anything read it".
//
// It is derived in internal/usage from Claude Code's transcripts and graft's
// own per-session counters — see that package for why those two sources and
// no others. Everything here is the board's side of it: when the rollup is
// refreshed, how a row renders it, and the dashboard behind U.
//
// The refresh is deliberately not on the board's scan path. A cold rollup is
// a six-second walk over gigabytes of transcript; a warm one is a few
// milliseconds of stat calls. Folding that into board.Scan would make the
// first tick of every session six seconds late for no benefit, so it runs on
// its own goroutine and publishes when it is done.

// usageColumnDays is the window the board column summarises. A week is what
// makes a row's sparkline mean "lately" rather than "ever".
const usageColumnDays = 7

// usageSaveEvery bounds how often the rollup is written back. It is a few
// hundred kilobytes; writing it on every refresh tick would be a needless
// write amplification on a file nothing reads until the next launch.
const usageSaveEvery = 5 * time.Minute

// usageSpark is the ramp a sparkline is drawn from, lowest to highest.
var usageSpark = []rune("▁▂▃▄▅▆▇█")

// startUsage loads the rollup and kicks the first refresh. Called once, from
// activate, after the window is up: the board must not wait on it.
func (a *App) startUsage() {
	dir := "."
	if a.opts.Store != nil {
		dir = filepath.Dir(a.opts.Store.Path())
	}
	a.usagePath = usage.DefaultPath(dir)
	a.usage = usage.Load(a.usagePath)
	a.refreshUsage()
}

// refreshUsage folds everything new in the transcripts and in graft's session
// counters into the rollup, then republishes the per-row summaries.
//
// It is guarded rather than queued: if a refresh is still running when the
// next tick arrives, the tick is dropped. Nothing is lost by that — the next
// one reads from the same offsets.
func (a *App) refreshUsage() {
	if a.usage == nil || a.usageBusy {
		return
	}
	a.usageBusy = true

	accounts := a.usageAccounts()
	repos := make([]string, 0, len(a.allRows()))
	for _, r := range a.allRows() {
		repos = append(repos, r.Path)
	}
	idx := a.usage
	path := a.usagePath

	go func() {
		t0 := time.Now()
		err := idx.Update(context.Background(), usage.Options{Accounts: accounts, Repos: repos})
		_, scanned := idx.LastUpdate()
		if err == nil && scanned > 0 {
			applog.Debugf("usage: %d transcripts read in %v", scanned, time.Since(t0).Round(time.Millisecond))
		}
		coreglib.IdleAdd(func() {
			a.usageBusy = false
			if err != nil {
				applog.Errorf("usage scan: %v", err)
				return
			}
			a.publishUsage(repos)
			if time.Since(a.usageSaved) > usageSaveEvery {
				a.usageSaved = time.Now()
				go func() {
					if err := idx.Save(path); err != nil {
						applog.Errorf("usage: saving the rollup: %v", err)
					}
				}()
			}
		})
	}()
}

// publishUsage recomputes the per-row window and repaints. Main thread.
//
// Usage lives BESIDE the rows rather than in them — it is keyed by path in a
// map this type owns, not carried on board.Row — so nothing the model knows
// about changes when the rollup lands. That is why a repaint is not enough:
// GTK's factory binds a cell's text once and QueueDraw redraws pixels without
// re-running the bind, so the Used column kept whatever it was bound with.
// On a cold start that is the empty map the first refresh publishes before the
// first scan has produced any rows at all, and the column stayed blank until
// an unrelated rescan happened to rebind it.
//
// Three things are stale at once and each is told separately: the Used cell's
// text, the Used ✕ filter — which selects on usage and would otherwise keep a
// row it should now drop — and the usage sort key.
func (a *App) publishUsage(repos []string) {
	uses := a.usage.RepoUses(repos, usage.Window{Days: usageColumnDays})

	a.mu.Lock()
	changed := !sameUses(a.usageUses, uses)
	a.usageUses = uses
	a.mu.Unlock()

	// Only when it actually moved. The live watch republishes every couple of
	// seconds, and a full re-filter and re-sort of three hundred rows on every
	// tick would shuffle the board under someone reading it.
	if changed {
		// Three separate staleness problems, and each needs its own signal.
		//
		// The TEXT is repainted cell by cell rather than by invalidating the
		// model. GTK does not re-bind a cell whose item did not itself change,
		// so neither a filter nor a sorter signal refreshes it — and
		// items-changed over the whole range, which would, is the same thing
		// a splice emits: it drops the scroll position and the selection. On
		// a live watch republishing every couple of seconds that would yank
		// the board out from under whoever is reading it. There are about as
		// many realised cells as visible rows, so doing it directly is both
		// cheaper and quieter.
		a.repaintUsageCells()
		// The FILTER, because the Used ✕ chip selects on usage: a row that
		// just earned its first use has to appear, and one whose usage aged
		// out of the window has to go.
		if a.cfilt != nil && a.filterID == filterGap {
			a.cfilt.Changed(gtk.FilterChangeDifferent)
		}
		// The SORT KEY, but only when the board is actually sorted by it —
		// every Changed costs a full re-sort.
		if col, _ := a.currentSort(); col == "usage" {
			if s := a.sorters["usage"]; s != nil {
				s.Changed(gtk.SorterChangeDifferent)
			}
		}
	}
	if a.view != nil {
		a.view.QueueDraw()
	}
	if a.onUsagePage() && a.usagePane != nil {
		a.usagePane.reload()
	}
}

// repaintUsageCells re-renders the Used column's realised cells in place,
// for the rows they are currently bound to. Main thread only.
func (a *App) repaintUsageCells() {
	for _, c := range a.usageCells {
		if c == nil || c.row == nil || c.label == nil {
			continue
		}
		u := a.usageFor(c.row.Path)
		gap := a.usageGap(c.row)
		text, class := usageCell(u, gap)
		if c.text != text {
			c.label.SetText(text)
			c.text = text
		}
		setClass(c.label, &c.class, class)
	}
}

// sameUses reports whether two windows of per-repository usage would render
// and filter identically. Only the fields the board reads are compared: the
// per-tool totals and the mark's input. The daily series is not — it is drawn
// from the same counts, and a series that moved without a total moving is a
// day boundary passing, which no cell renders differently.
func sameUses(old, next map[string]usage.RepoUse) bool {
	if len(old) != len(next) {
		return false
	}
	for path, n := range next {
		o, ok := old[path]
		if !ok || o.Graphify != n.Graphify || o.Graft != n.Graft {
			return false
		}
	}
	return true
}

// usageAccounts is every Claude Code login on this machine, because usage is
// a fact about the machine and not about the account the board happens to be
// configured to run jobs as. A directory that is selected in settings but no
// longer exists is skipped rather than reported: there is nothing to read.
func (a *App) usageAccounts() []usage.Account {
	sel := ""
	if a.opts.Store != nil {
		sel = a.opts.Store.Settings().ClaudeAccount
	}
	var out []usage.Account
	for _, c := range gfy.ClaudeAccounts(sel) {
		if c.Missing || c.Dir == "" {
			continue
		}
		out = append(out, usage.Account{Name: c.Name, Dir: c.Dir})
	}
	return out
}

// saveUsage writes the rollup back, unconditionally. Called when the window
// closes: losing the last few minutes of offsets only costs the next launch a
// re-read of the transcripts those minutes appended, but losing the whole file
// costs a six-second cold scan.
func (a *App) saveUsage() {
	if a.usage == nil || a.usagePath == "" {
		return
	}
	if err := a.usage.Save(a.usagePath); err != nil {
		applog.Errorf("usage: saving the rollup: %v", err)
	}
}

// usageFor is one row's window summary.
func (a *App) usageFor(path string) usage.RepoUse {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.usageUses[path]
}

// usageCell renders the board column: how much an agent used either tool on
// this checkout in the last week, as a shape and a number.
//
// The two tools are not split here — the Graph and Graft columns already say
// which indexes exist, and what this column adds is whether anything reads
// them. The tooltip carries the split.
func usageCell(u usage.RepoUse, gap bool) (string, string) {
	if u.Total() == 0 {
		return "", "st-none"
	}
	cell := sparkline(u.Series) + " " + fmt.Sprint(u.Total())
	if gap {
		// The conjunction the board could not previously state. Both halves
		// were already on screen — the sparkline here, the state dot in the
		// first column — but reading them together meant tracking two columns
		// across ninety rows, so the one combination that is always wrong is
		// marked where the usage is.
		return cell + " ✕", "st-broken"
	}
	return cell, ""
}

// sparkline draws a series as block characters, scaled to its own maximum.
//
// Scaling per row rather than across the board is the right call for a column
// whose job is "is this repository being used lately": a row with three calls
// a day every day should look steady, not invisible beside the one with two
// hundred.
func sparkline(series []int) string {
	if len(series) == 0 {
		return ""
	}
	max := 0
	for _, v := range series {
		if v > max {
			max = v
		}
	}
	var b strings.Builder
	for _, v := range series {
		switch {
		case v <= 0:
			b.WriteRune('·')
		case max <= 1:
			b.WriteRune(usageSpark[len(usageSpark)-1])
		default:
			i := (v - 1) * (len(usageSpark) - 1) / max
			b.WriteRune(usageSpark[i])
		}
	}
	return b.String()
}

// usageTooltip is the long form behind the column.
func usageTooltip(r *board.Row, u usage.RepoUse, gap bool) string {
	if u.Total() == 0 {
		return "No agent used graphify or graft on this repository in the last " +
			fmt.Sprint(usageColumnDays) + " days"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s — last %d days\n", r.Name, usageColumnDays)
	fmt.Fprintf(&b, "graphify %d · graft %d\n", u.Graphify, u.Graft)
	if !u.Last.IsZero() {
		fmt.Fprintf(&b, "last used %s\n", board.Age(u.Last))
	}
	if gap {
		fmt.Fprintf(&b, "\n✕  used %d times with %s — every one of those queries fell "+
			"through to raw source. Extract is what closes it.", u.Total(), stateWord(r.Graph.State))
	}
	return strings.TrimSpace(b.String())
}

// usageGap is the board's side of usage.UsedWithoutGraph, over the same window
// the Used column draws.
func (a *App) usageGap(r *board.Row) bool {
	return usage.UsedWithoutGraph(*r, a.usageFor(r.Path).Total())
}

// usageWeight sorts the column: most-used first, then by recency, so the
// rows worth extracting next float to the top of a descending sort.
func usageWeight(a *App, r *board.Row) int {
	u := a.usageFor(r.Path)
	return u.Total()
}

// --- the Usage tab --------------------------------------------------------

// usagePane is the Usage tab: the same rollup at two scopes — this
// repository, or every repository on the machine — with an optional live
// watch.
//
// It is a tab and not a dialog on purpose. A dialog is something you open,
// read and dismiss; usage is something you leave up beside the board while an
// agent works, which is also why the live watch exists. The scope switch is
// what lets one page answer both "is anything reading THIS index" and "what is
// this machine doing with either tool".
type usagePane struct {
	a      *App
	widget gtk.Widgetter

	days     int
	scopeAll bool
	live     bool
	row      *board.Row

	scope    *gtk.Box
	scopeBtn []*gtk.ToggleButton
	lastLive time.Time
	windows  *gtk.Box
	wbtns    []*gtk.ToggleButton
	liveBtn  *gtk.ToggleButton
	spin     *gtk.Spinner

	head     *gtk.Label
	tiles    *gtk.FlowBox
	recs     *gtk.Box
	recsSec  gtk.Widgetter
	timeline *gtk.Label
	verbs    *gtk.Box
	where    gtk.Widgetter
	whereBox *gtk.Box
	recent   *gtk.Box
	foot     *gtk.Label
}

// usageWindows are the windows the page offers. Seven days is the board
// column's window, thirty is the habit, ninety is everything retained.
var usageWindows = []int{7, 30, 90}

// usageLiveEvery is how often the live watch re-reads. A warm read is a few
// milliseconds of stat calls over the transcript corpus, so a two-second
// cadence is affordable — and two seconds is about how fast a running agent
// produces a line worth seeing.
const usageLiveEvery = 2 * time.Second

func (a *App) newUsagePane() *usagePane {
	p := &usagePane{a: a, days: 30}

	p.head = gtk.NewLabel("")
	p.head.SetXAlign(0)
	p.head.SetWrap(true)
	p.head.AddCSSClass("title-2")

	p.scope = gtk.NewBox(gtk.OrientationHorizontal, 0)
	p.scope.AddCSSClass("linked")
	for i, label := range []string{"This repository", "Everything"} {
		all := i == 1
		b := gtk.NewToggleButtonWithLabel(label)
		b.SetActive(all == p.scopeAll)
		b.ConnectClicked(func() {
			if p.scopeAll == all {
				return
			}
			p.scopeAll = all
			p.syncButtons()
			p.reload()
		})
		p.scope.Append(b)
		p.scopeBtn = append(p.scopeBtn, b)
	}

	p.windows = gtk.NewBox(gtk.OrientationHorizontal, 0)
	p.windows.AddCSSClass("linked")
	for _, d := range usageWindows {
		days := d
		b := gtk.NewToggleButtonWithLabel(fmt.Sprintf("%dd", days))
		b.SetActive(days == p.days)
		b.ConnectClicked(func() {
			if p.days == days {
				return
			}
			p.days = days
			p.syncButtons()
			p.reload()
		})
		p.windows.Append(b)
		p.wbtns = append(p.wbtns, b)
	}

	p.spin = gtk.NewSpinner()
	p.spin.SetSizeRequest(16, 16)
	p.spin.SetVisible(false)

	p.liveBtn = gtk.NewToggleButtonWithLabel("Watch live")
	p.liveBtn.SetTooltipText("Re-read the transcripts every couple of seconds, so a call an agent makes right now shows up here")
	p.liveBtn.ConnectToggled(func() {
		p.live = p.liveBtn.Active()
		p.spin.SetVisible(p.live)
		if p.live {
			p.spin.Start()
			p.a.refreshUsage()
		} else {
			p.spin.Stop()
		}
		p.reload()
	})

	copyBtn := gtk.NewButtonFromIconName("edit-copy-symbolic")
	copyBtn.AddCSSClass("flat")
	copyBtn.SetTooltipText("Copy this window as markdown — the briefing to paste into a Claude Code " +
		"session to be told where the two indexes are being left on the table")
	copyBtn.ConnectClicked(func() { p.copyReport() })

	refresh := gtk.NewButtonFromIconName("view-refresh-symbolic")
	refresh.AddCSSClass("flat")
	refresh.SetTooltipText("Re-read the transcripts now")
	refresh.ConnectClicked(func() { p.a.refreshUsage() })

	bar := gtk.NewBox(gtk.OrientationHorizontal, 6)
	bar.Append(p.scope)
	spacer := gtk.NewLabel("")
	spacer.SetHExpand(true)
	bar.Append(spacer)
	bar.Append(p.spin)
	bar.Append(p.liveBtn)
	bar.Append(p.windows)
	bar.Append(copyBtn)
	bar.Append(refresh)

	// A flow box rather than a row: the detail pane is half of a window the
	// compositor sizes, and five fixed tiles run off the end of a narrow one.
	p.tiles = gtk.NewFlowBox()
	p.tiles.SetSelectionMode(gtk.SelectionNone)
	p.tiles.SetColumnSpacing(18)
	p.tiles.SetRowSpacing(12)
	p.tiles.SetMinChildrenPerLine(2)
	p.tiles.SetMaxChildrenPerLine(5)
	p.tiles.SetHomogeneous(true)

	p.timeline = gtk.NewLabel("")
	p.timeline.SetXAlign(0)
	p.timeline.SetSelectable(true)
	p.timeline.AddCSSClass("argv")

	p.recs = gtk.NewBox(gtk.OrientationVertical, 4)
	p.recsSec = usageSection("Worth doing next", p.recs)

	p.verbs = gtk.NewBox(gtk.OrientationHorizontal, 24)
	p.verbs.SetHomogeneous(true)
	p.whereBox = gtk.NewBox(gtk.OrientationVertical, 2)
	p.where = usageSection("Where", p.whereBox)
	p.recent = gtk.NewBox(gtk.OrientationVertical, 2)

	p.foot = gtk.NewLabel("")
	p.foot.SetXAlign(0)
	p.foot.SetWrap(true)
	p.foot.AddCSSClass("dim")

	// The controls are pinned above the scroller, not scrolled with it: the
	// window switch, the scope and the live watch are how you drive the page,
	// and a control you have to scroll back up to reach is a control that
	// gets used once.
	bar.SetMarginTop(12)
	bar.SetMarginBottom(6)
	bar.SetMarginStart(18)
	bar.SetMarginEnd(18)

	body := gtk.NewBox(gtk.OrientationVertical, 18)
	body.SetMarginTop(6)
	body.SetMarginBottom(18)
	body.SetMarginStart(18)
	body.SetMarginEnd(18)
	body.Append(p.head)
	body.Append(p.tiles)
	// The recommendations sit directly under the tiles, above every
	// breakdown: they are the only part of this page that asks for an action,
	// and a work queue below three sections of statistics is a work queue
	// nobody scrolls to.
	body.Append(p.recsSec)
	body.Append(usageSection("Every day", p.timeline))
	body.Append(usageSection("What was run", p.verbs))
	body.Append(p.where)
	body.Append(usageSection("Latest", p.recent))
	body.Append(p.foot)

	// Clamped, because this page now takes the whole window: on a 4K screen
	// an unclamped row puts its action button three thousand pixels from the
	// text that explains it, and a line of prose that wide cannot be read at
	// all. The clamp is what makes the same page work at both sizes.
	sw := gtk.NewScrolledWindow()
	sw.SetChild(clampWidth(body))
	sw.SetVExpand(true)
	sw.SetPolicy(gtk.PolicyNever, gtk.PolicyAutomatic)

	page := gtk.NewBox(gtk.OrientationVertical, 0)
	page.Append(clampWidth(bar))
	page.Append(sw)
	p.widget = page
	return p
}

func (p *usagePane) syncButtons() {
	for i, b := range p.scopeBtn {
		b.SetActive((i == 1) == p.scopeAll)
		// "This repository" is meaningless with nothing selected.
		b.SetSensitive(i == 1 || p.row != nil)
	}
	for i, b := range p.wbtns {
		b.SetActive(usageWindows[i] == p.days)
	}
}

// setScopeAll is how the keyboard opens the machine-wide view.
func (p *usagePane) setScopeAll(all bool) {
	if p.scopeAll == all {
		return
	}
	p.scopeAll = all
	p.syncButtons()
	p.reload()
}

// show binds the pane to a row and repaints. A nil row is legitimate: it is
// the machine-wide scope with nothing selected on the board.
func (p *usagePane) show(r *board.Row) {
	p.row = r
	if r == nil {
		p.scopeAll = true
	}
	p.syncButtons()
	p.reload()
}

// setRow follows the board's selection without forcing a repaint of a page
// that is not on screen.
func (p *usagePane) setRow(r *board.Row) {
	if p == nil {
		return
	}
	p.row = r
	if p.a.onUsagePage() {
		p.syncButtons()
		p.reload()
	}
}

// repo is the checkout the current scope narrows to, empty for everything.
func (p *usagePane) repo() string {
	if p.scopeAll || p.row == nil {
		return ""
	}
	return p.row.Path
}

// reload repaints the whole page from the rollup.
func (p *usagePane) reload() {
	if p == nil || p.a.usage == nil {
		return
	}
	repo := p.repo()
	s := p.a.usage.Summarize(usage.Window{Days: p.days, Repo: repo})

	if repo == "" {
		p.head.SetText(fmt.Sprintf("%d uses over %d days, every repository", s.Events, s.Days))
	} else {
		p.head.SetText(fmt.Sprintf("%d uses over %d days — %s", s.Events, s.Days, p.row.Name))
	}

	clearFlow(p.tiles)
	p.tiles.Append(usageTile(fmt.Sprint(s.ByTool[usage.Graphify]), "graphify calls"))
	p.tiles.Append(usageTile(fmt.Sprint(s.ByTool[usage.Graft]), "graft calls"))
	p.tiles.Append(usageTile(fmt.Sprint(s.Sessions), "agent sessions"))
	if mix, ok := s.Mix(); ok {
		p.tiles.Append(usageTile(fmt.Sprintf("%d%%", mix), "reads through graft"))
	} else {
		p.tiles.Append(usageTile("—", "reads through graft"))
	}
	p.tiles.Append(usageTile(shortCount(s.SavedTokens), "tokens graft saved"))

	p.fillRecs(s, repo)

	p.timeline.SetText(usageTimeline(s))

	clearBox(p.verbs)
	p.verbs.Append(verbColumn("graphify", s.Verbs[usage.Graphify]))
	p.verbs.Append(verbColumn("graft", s.Verbs[usage.Graft]))

	// "Where" only means something across repositories; in the narrow scope
	// the answer is the row the board already has selected.
	p.where.(*gtk.Box).SetVisible(repo == "")
	if repo == "" {
		clearBox(p.whereBox)
		if len(s.Repos) == 0 {
			p.whereBox.Append(dimLabel("Nothing yet. A repository appears here once an agent has run either tool in it."))
		}
		for i, r := range s.Repos {
			if i >= 12 {
				break
			}
			p.whereBox.Append(p.a.usageRepoRow(r))
		}
	}

	clearBox(p.recent)
	events := p.a.usage.Recents(repo, 14)
	if len(events) == 0 {
		p.recent.Append(dimLabel("Nothing recorded here yet."))
	}
	for _, e := range events {
		p.recent.Append(usageEventRow(e))
	}

	p.foot.SetText(p.footText(s))
}

// fillRecs repaints the recommendation list: the repositories where agent
// usage and index state disagree.
//
// In the narrow scope it is the selected repository's own verdict, which is
// the answer to "should I build something here" for the row the board has
// highlighted; in the wide scope it is every repository, ranked by how much
// use is going unindexed.
func (p *usagePane) fillRecs(s usage.Summary, repo string) {
	rows := p.a.allRows()
	recs := usage.Recommend(rows, s, 8)
	if repo != "" {
		// Summarize already narrowed the counts, so anything Recommend
		// returns here belongs to this repository or to a directory under it.
		if len(recs) > 1 {
			recs = recs[:1]
		}
	}

	clearBox(p.recs)
	if len(recs) == 0 {
		switch {
		case s.Events == 0 && repo != "":
			p.recs.Append(dimLabel("No agent has used either tool here in this window, so there is nothing to recommend."))
		case repo != "":
			p.recs.Append(dimLabel("This repository's indexes are current — nothing to build."))
		default:
			p.recs.Append(dimLabel("Every repository an agent used in this window already has a current index."))
		}
		return
	}
	for _, r := range recs {
		p.recs.Append(p.recRow(r))
	}
}

// recRow is one recommendation: what to do, why, and the button that does it.
//
// The button runs the same job kind the board's own actions do, which means a
// metered one goes through the same confirm dialog naming the backend, the
// model and the repository count. Nothing here is a shortcut around the cost
// gate — it is the cost gate, reached from the page that explains why the
// money would be worth spending.
func (p *usagePane) recRow(r usage.Rec) gtk.Widgetter {
	title := fmt.Sprintf("%s — %s", r.Name, r.Why)
	name := gtk.NewLabel(title)
	name.SetXAlign(0)
	name.SetWrap(true)
	name.SetHExpand(true)

	uses := gtk.NewLabel(fmt.Sprintf("%d", r.Uses))
	uses.AddCSSClass("heading")
	uses.SetWidthChars(5)
	uses.SetXAlign(1)
	uses.SetTooltipText(fmt.Sprintf("%d uses in this window", r.Uses))

	box := gtk.NewBox(gtk.OrientationHorizontal, 12)
	box.Append(uses)
	box.Append(name)
	box.Append(p.recButton(r))
	return box
}

// recButton is the action itself.
func (p *usagePane) recButton(r usage.Rec) *gtk.Button {
	if r.Action == usage.AddRoot {
		b := gtk.NewButtonWithLabel("Add a scan root…")
		b.AddCSSClass("flat")
		b.SetTooltipText(gfy.Tilde(r.Repo) + " is not under any root this board scans")
		b.ConnectClicked(func() { p.a.showSettings() })
		return b
	}

	label := r.Action.Command()
	b := gtk.NewButtonWithLabel(label)
	b.AddCSSClass("flat")
	if r.Metered() {
		// The same mark and the same colour the rest of the board gives work
		// that spends money — `.metered`, not `suggested-action`, which on a
		// flat button paints white text onto no background at all.
		b.SetLabel(label + "  $")
		b.AddCSSClass("metered")
		b.SetTooltipText("Metered: this runs an LLM backend and costs money. A confirm names the backend and the model first.")
	} else {
		b.SetTooltipText("Free: an AST pass, no API key and no LLM call")
	}
	b.ConnectClicked(func() {
		row := p.a.row(r.Repo)
		if row == nil {
			p.a.toastf("%s is no longer on the board", r.Name)
			return
		}
		p.a.run(string(r.Action), []board.Row{*row}, nil)
	})
	return b
}

// copyReport puts the whole window on the clipboard as markdown.
//
// The dashboard can show that graft was offered to two hundred sessions and
// reached for in nine; it cannot say whether that is bad. The thing qualified
// to judge it is an agent, so the button produces the artefact worth handing
// to one — the same text `ggraphify-scan -usage -markdown` prints, rendered by
// internal/usage so the two can never drift.
func (p *usagePane) copyReport() {
	if p == nil || p.a.usage == nil {
		return
	}
	repo := p.repo()
	s := p.a.usage.Summarize(usage.Window{Days: p.days, Repo: repo})
	when, scanned := p.a.usage.LastUpdate()
	rows := p.a.allRows()
	text := usage.Report(s, usage.Recommend(rows, s, 0), usage.ReportOptions{
		Scope:      repo,
		Version:    p.a.opts.Version,
		Accounts:   p.a.usageAccounts(),
		LastUpdate: when,
		Scanned:    scanned,
		Repos:      repoPaths(rows),
		Recents:    p.a.usage.Recents(repo, 20),
	})
	p.a.win.Clipboard().SetText(text)
	p.a.toastf("copied %s of usage — paste it to an agent and ask where the indexes are being missed",
		board.Bytes(int64(len(text))))
}

// repoPaths is the board's checkout list, which the report uses to keep its
// told-and-never-used section to directories a person can act on.
func repoPaths(rows []board.Row) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Path)
	}
	return out
}

// footText is the honest small print: what the numbers are not, what the money
// one actually means, and when the rollup was last brought up to date.
func (p *usagePane) footText(s usage.Summary) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d hook injections in this window are counted separately and not as uses — "+
		"the integration firing is not an agent choosing to use anything.\n", s.Hooks)
	if s.CostMicros > 0 {
		fmt.Fprintf(&b, "The sessions that used graft were billed $%.2f of input over this window "+
			"(graft's own figure, for the whole session — not the cost of graft).\n", s.CostUSD())
	}
	when, scanned := p.a.usage.LastUpdate()
	switch {
	case when.IsZero():
		b.WriteString("The transcripts have not been read yet.")
	case p.live:
		fmt.Fprintf(&b, "Watching live: %d Claude Code account(s), re-read every %s; last read %s, %d transcript(s) were new.",
			len(p.a.usageAccounts()), usageLiveEvery, board.Age(when), scanned)
	default:
		fmt.Fprintf(&b, "Read from %d Claude Code account(s) %s; %d transcript(s) were new. "+
			"Refreshes with the board; Watch live re-reads every %s.",
			len(p.a.usageAccounts()), board.Age(when), scanned, usageLiveEvery)
	}
	return b.String()
}

// tick is the live watch. It is driven by the board's one-second repaint and
// does nothing unless the watch is on and this page is the visible one: a
// dashboard nobody is looking at has no business re-reading gigabytes of
// transcript.
func (p *usagePane) tick(visible bool) {
	if p == nil || !p.live || !visible {
		return
	}
	if time.Since(p.lastLive) < usageLiveEvery {
		return
	}
	p.lastLive = time.Now()
	p.a.refreshUsage()
}

// The window's two pages.
const (
	pageBoard     = "board"
	pageUsageName = "usage"
)

// pageUsage maps the header toggle's state to a page name.
func pageUsage(on bool) string {
	if on {
		return pageUsageName
	}
	return pageBoard
}

// onUsagePage reports whether the dashboard is the page on screen.
func (a *App) onUsagePage() bool {
	return a.main != nil && a.main.VisibleChildName() == pageUsageName
}

// setMainPage switches the window between the board and the dashboard.
//
// The filter bar goes with the board: it filters rows, and rows are not on
// screen here. The header and the status line stay, so the window never loses
// the controls that are about the whole board.
func (a *App) setMainPage(name string) {
	if a.main == nil || a.main.VisibleChildName() == name {
		return
	}
	a.main.SetVisibleChildName(name)
	if a.filterBar != nil {
		a.filterBar.SetVisible(name == pageBoard)
	}
	a.usageGuard = true
	if a.usageBtn != nil {
		a.usageBtn.SetActive(name == pageUsageName)
	}
	a.usageGuard = false
	if name == pageUsageName && a.usagePane != nil {
		a.usagePane.show(a.current())
		a.refreshUsage()
	}
	a.refreshStatus()
}

// showUsage puts the dashboard on screen; U toggles back to the board.
func (a *App) showUsage() {
	if a.onUsagePage() {
		a.setMainPage(pageBoard)
		return
	}
	a.setMainPage(pageUsageName)
}

// clampWidth bounds a widget's width and centres it, so the dashboard reads the
// same on a tiled half-screen and on a fullscreen 4K panel.
func clampWidth(child gtk.Widgetter) gtk.Widgetter {
	c := adw.NewClamp()
	c.SetMaximumSize(1180)
	c.SetTighteningThreshold(820)
	c.SetChild(child)
	return c
}

// usageSection wraps a widget in a titled block.
func usageSection(title string, child gtk.Widgetter) gtk.Widgetter {
	lbl := gtk.NewLabel(title)
	lbl.SetXAlign(0)
	lbl.AddCSSClass("heading")
	box := gtk.NewBox(gtk.OrientationVertical, 6)
	box.Append(lbl)
	box.Append(child)
	return box
}

// usageTimeline is the per-day line under the tiles: one glyph per day for
// each tool, with the busiest day named.
func usageTimeline(s usage.Summary) string {
	gfy := make([]int, len(s.Series))
	graft := make([]int, len(s.Series))
	peak, peakDay := 0, ""
	for i, pt := range s.Series {
		gfy[i], graft[i] = pt.Graphify, pt.Graft
		if pt.Total() > peak {
			peak, peakDay = pt.Total(), pt.Day
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "graphify  %s\n", sparkline(gfy))
	fmt.Fprintf(&b, "graft     %s\n", sparkline(graft))
	if peak > 0 {
		fmt.Fprintf(&b, "%s → %s, busiest %s with %d",
			s.From.Local().Format("Jan 2"), s.To.Local().Format("Jan 2"),
			usage.ParseDay(peakDay).Format("Jan 2"), peak)
	} else {
		fmt.Fprintf(&b, "%s → %s", s.From.Local().Format("Jan 2"), s.To.Local().Format("Jan 2"))
	}
	return b.String()
}

// verbColumn is one tool's subcommand breakdown, as a small bar chart made of
// text so it scales with the font and copies as plain text.
func verbColumn(title string, counts []usage.Count) gtk.Widgetter {
	box := gtk.NewBox(gtk.OrientationVertical, 2)
	head := gtk.NewLabel(title)
	head.SetXAlign(0)
	head.AddCSSClass("heading")
	box.Append(head)
	if len(counts) == 0 {
		box.Append(dimLabel("not used"))
		return box
	}
	// The bars are scaled to the busiest REAL subcommand, not to the busiest
	// row. A hook that injected eleven thousand times would otherwise flatten
	// every command an agent actually chose to run into a single block.
	max := 0
	for _, c := range counts {
		if c.Name != hookVerbName && c.Count > max {
			max = c.Count
		}
	}
	counts = hookLast(counts)
	for i, c := range counts {
		if i >= 8 {
			break
		}
		width := 1 + c.Count*12/maxInt(max, 1)
		if width > 13 {
			width = 13 // the hook row, pinned to full width rather than off the page
		}
		l := gtk.NewLabel(fmt.Sprintf("%-14s %4d %s", ellipsis(c.Name, 14), c.Count, strings.Repeat("▇", width)))
		l.SetXAlign(0)
		l.AddCSSClass("argv")
		if c.Name == hookVerbName {
			// The hook's own injections: shown, because a silent integration
			// that fires a thousand times is worth knowing about, but dimmed,
			// because nobody asked for it.
			l.AddCSSClass("dim")
			l.SetTooltipText("Context the integration's hook injected on its own")
		}
		box.Append(l)
	}
	return box
}

// hookVerbName is what internal/usage calls a hook's own injected context.
// It is the one row in a breakdown that nobody asked for, so it is sorted
// last, dimmed, and kept out of the bar scale.
const hookVerbName = "context"

// hookLast moves the hook row to the end without disturbing the rest of the
// order.
func hookLast(counts []usage.Count) []usage.Count {
	out := make([]usage.Count, 0, len(counts))
	var hooks []usage.Count
	for _, c := range counts {
		if c.Name == hookVerbName {
			hooks = append(hooks, c)
			continue
		}
		out = append(out, c)
	}
	return append(out, hooks...)
}

// usageRepoRow is one line of the "Where" list: the checkout, how much, and
// whether the board even knows about it.
func (a *App) usageRepoRow(c usage.Count) gtk.Widgetter {
	name := board.Tilde(c.Name)
	dim := false
	if a.row(c.Name) == nil && a.ownerRow(c.Name) == nil {
		// Usage in a directory that is not a boarded checkout. It is still
		// usage, and hiding it would make the totals not add up.
		dim = true
		name += "  (not on the board)"
	}
	l := gtk.NewLabel(fmt.Sprintf("%5d  %s", c.Count, name))
	l.SetXAlign(0)
	l.AddCSSClass("argv")
	if dim {
		l.AddCSSClass("dim")
	}
	return l
}

// ownerRow finds the boarded checkout a working directory belongs to.
func (a *App) ownerRow(cwd string) *board.Row {
	a.mu.RLock()
	defer a.mu.RUnlock()
	var best *board.Row
	for i := range a.rows {
		r := &a.rows[i]
		if usage.RepoMatch(r.Path, cwd) && (best == nil || len(r.Path) > len(best.Path)) {
			best = r
		}
	}
	return best
}

func usageEventRow(e usage.Event) gtk.Widgetter {
	where := filepath.Base(e.Repo)
	if where == "." || where == "" {
		where = "—"
	}
	// Local time: transcripts record UTC, and a list headed "Latest" that
	// disagrees with the clock on the wall by three hours is worse than no
	// list at all.
	l := gtk.NewLabel(fmt.Sprintf("%s  %-9s %-16s %-18s %s",
		e.At.Local().Format("Jan 2 15:04"), e.Tool, ellipsis(e.Verb, 16), ellipsis(where, 18), e.Account))
	l.SetXAlign(0)
	l.AddCSSClass("argv")
	l.SetTooltipText(e.Repo + "\nsession " + e.Session + "\nvia " + e.Kind.String())
	return l
}

func usageTile(value, label string) gtk.Widgetter {
	v := gtk.NewLabel(value)
	v.SetXAlign(0)
	v.AddCSSClass("title-2")
	l := gtk.NewLabel(label)
	l.SetXAlign(0)
	l.AddCSSClass("dim")
	l.SetWrap(true)
	box := gtk.NewBox(gtk.OrientationVertical, 0)
	box.Append(v)
	box.Append(l)
	return box
}

func dimLabel(s string) *gtk.Label {
	l := gtk.NewLabel(s)
	l.SetXAlign(0)
	l.SetWrap(true)
	l.AddCSSClass("dim")
	return l
}

// clearFlow empties a flow box, which does not share GtkBox's Remove.
func clearFlow(f *gtk.FlowBox) {
	for child := f.FirstChild(); child != nil; child = f.FirstChild() {
		f.Remove(child)
	}
}

func clearBox(b *gtk.Box) {
	for child := b.FirstChild(); child != nil; child = b.FirstChild() {
		b.Remove(child)
	}
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func ellipsis(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 1 {
		return string(r[:n])
	}
	return string(r[:n-1]) + "…"
}

// shortCount is the compact form big token counts are read in.
func shortCount(n int64) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1_000:
		return fmt.Sprintf("%.0fk", float64(n)/1e3)
	}
	return fmt.Sprint(n)
}
