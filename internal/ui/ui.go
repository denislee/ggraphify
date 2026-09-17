// Package ui is the only package in ggraphify that imports GTK.
//
// Everything the board knows is computed headlessly in internal/board,
// internal/discover, internal/graphstate, internal/gfy and internal/jobs;
// this turns it into widgets. The discipline that keeps the board responsive
// is inherited from docsboard and is worth restating, because breaking it is
// invisible until the frame budget is already gone:
//
//   - Everything in this package runs on the GTK main thread. Nothing else
//     does. Workers talk back with glib.IdleAdd, one small typed message per
//     update.
//   - No package outside this one imports GTK. cmd/ggraphify-scan and
//     cmd/ggraphify-job exist to prove it.
//   - A scan, a graph.json parse and a subprocess never touch the main
//     thread. The refresh tick dispatches work to a goroutine and folds the
//     result back in.
package ui

import (
	"context"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/diamondburned/gotk4-adwaita/pkg/adw"
	coreglib "github.com/diamondburned/gotk4/pkg/core/glib"
	"github.com/diamondburned/gotk4/pkg/gio/v2"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"

	"github.com/dns/ggraphify/internal/applog"
	"github.com/dns/ggraphify/internal/autofix"
	"github.com/dns/ggraphify/internal/board"
	"github.com/dns/ggraphify/internal/discover"
	"github.com/dns/ggraphify/internal/gfy"
	"github.com/dns/ggraphify/internal/graftstate"
	"github.com/dns/ggraphify/internal/graphstate"
	"github.com/dns/ggraphify/internal/jobs"
	"github.com/dns/ggraphify/internal/open"
	"github.com/dns/ggraphify/internal/store"
	"github.com/dns/ggraphify/internal/usage"
)

// AppID is the Wayland app_id the window carries. sway and fuzzel key their
// rules off it, and packaging/dev.dns.ggraphify.desktop names it in
// StartupWMClass so the running window is associated with the entry.
const AppID = "dev.dns.ggraphify"

// WindowTitle is what the compositor shows.
const WindowTitle = "ggraphify"

// Options configures the board. main.go fills it from flags and the sidecar.
type Options struct {
	Store   *store.Store
	Roots   []string
	Depth   int
	Refresh time.Duration
	Opener  open.Config

	// OutName and OutBase are where graphify's knowledge lives for every
	// repository on the board: the directory name inside each checkout, or a
	// single directory holding one subdirectory per checkout. Both blank means
	// the stored preference, and failing that graphify's own default.
	OutName string
	OutBase string
	// ShowHidden is the -hidden flag: board the checkouts inside dot-named
	// directories for this run. Only consulted when the flag was given.
	ShowHidden bool

	Dark       bool
	ForceLight bool

	StartWidth  int
	StartHeight int

	// SetFlags is the set of command-line flags that were actually typed, by
	// name. It is what tells a stored preference apart from a pinned one: a
	// settings row whose value came from the command line is shown
	// insensitive and says which flag decided it, rather than silently
	// disagreeing with what the board is actually doing.
	SetFlags map[string]bool

	// Version is ggraphify's own version string, for the About dialog.
	Version string
}

// App is the running board.
type App struct {
	opts Options

	app    *adw.Application
	win    *adw.ApplicationWindow
	toasts *adw.ToastOverlay
	title  *adw.WindowTitle
	split  *gtk.Paned
	banner *adw.Banner

	// vsplit is the horizontal splitter between the board+detail half and the
	// bottom panel; dock is the panel itself. The panel is built whether or
	// not it is enabled — building it lazily would mean its log page missed
	// everything logged before the first time it was shown — and the setting
	// moves its visibility.
	vsplit *gtk.Paned
	dock   *dockPane

	// The board half.
	view   *gtk.ColumnView
	rowsSW *gtk.ScrolledWindow // the board's scroller; page size is measured from it
	model  *gio.ListStore
	sel    *gtk.SingleSelection
	sorted *gtk.SortListModel
	filter *gtk.FilterListModel
	cfilt  *gtk.CustomFilter

	// groupDrop is the folder filter: which scan root, or which directory
	// under one, the board is narrowed to. groupPaths is the dropdown's model
	// in the same order, because a GtkDropDown reports a position and the
	// board needs the path that position stands for.
	groupDrop  *gtk.DropDown
	groupPaths []string
	groupKey   string // what the dropdown was last built from, to avoid rebuilding it every tick
	groupGuard bool

	search *gtk.SearchEntry
	chips  map[string]*gtk.ToggleButton
	// filterID is the state chip in force, "" for all. chipGuard suppresses a
	// chip's own toggled signal while the board is the one setting it.
	filterID  string
	chipGuard bool
	// groupID is the folder filter in force: an absolute directory path, or
	// "" for every folder.
	groupID string
	selBtn  *gtk.ToggleButton
	status  *gtk.Label

	// The header's background-activity indicator: what is running right now,
	// without opening the jobs dialog. activityKey is the live set it was
	// last built from, so a closed popover is only rebuilt when the queue
	// actually moves.
	activity     *gtk.MenuButton
	activitySpin *gtk.Spinner
	activityLbl  *gtk.Label
	activityList *gtk.Box
	activityPop  *gtk.Popover
	activityKey  string

	cols map[string]*gtk.ColumnViewColumn
	// sorters is each sortable column's comparator, by column id, so a sort
	// key that changed outside the model can be invalidated. See resortJobs.
	sorters map[string]*gtk.CustomSorter

	// usageCells is the Used column's realised cells, by list-item pointer.
	// Every other column renders from the row itself, so a rebind is the only
	// way its text can be stale; usage lives beside the row and arrives later.
	usageCells map[uintptr]*rowCell

	// The detail half.
	detail   *detailPane
	jobsPage *jobsView
	scheme   *gtk.Button
	settings *adw.PreferencesDialog
	helpDlg  *adw.ShortcutsDialog

	// The settings rows the board itself can move. A column hidden from the
	// dialog and a column hidden by some other route are one state, and the
	// dialog has to stay in step with it while it is open.
	setCols      map[string]*adw.SwitchRow
	overlayGroup *adw.PreferencesGroup
	overlayRows  []*adw.EntryRow

	// The bottom panel's three switches. dockGuard suppresses their own
	// signals while the board is the one setting them, which happens whenever
	// `b` or the panel's hide button moves the same state.
	dockRows  map[string]*adw.SwitchRow
	dockGuard bool

	// The Claude Code integration group: one row per check, rebuilt whenever
	// the inspection is re-run.
	claudeGroup    *adw.PreferencesGroup
	claudeSummary  *adw.ActionRow
	claudeRows     []*adw.ActionRow
	claudeCheckBtn *gtk.Button
	claudeFixBtn   *gtk.Button
	claudeSetup    gfy.ClaudeSetup

	// The graft integration group, the same shape one group further down the
	// page: is the Claude Code CLI on this machine configured to use graft?
	graftGroup     *adw.PreferencesGroup
	graftSummary   *adw.ActionRow
	graftSetupRows []*adw.ActionRow
	graftCheckBtn  *gtk.Button
	graftFixBtn    *gtk.Button
	graftSetup     gfy.GraftSetup
	graftVersion   gfy.GraftVersion
	// The local-model group: is there a model server on this machine, is it
	// up, and what has it got. Four rows because the three ways it can be
	// unusable have three different fixes.
	localGroup      *adw.PreferencesGroup
	localServerRow  *adw.ActionRow
	localStartBtn   *gtk.Button
	localAutoRow    *adw.SwitchRow
	localIdleRow    *adw.SpinRow
	localModelsRow  *adw.ComboRow
	localPullRow    *adw.ActionRow
	localCompatRow  *adw.ActionRow
	localModelNames []string
	// localFilling suppresses the model combo's change handler while the
	// combo is being repopulated. Handing GtkDropDown a new string list
	// selects item 0 and notifies, which without this would overwrite a
	// deliberately-chosen model with whatever the server happens to list
	// first, every time the page is redrawn.
	localFilling bool

	// claudeAccountCombo is the account picker in the settings group; held so
	// the rest of the page can read which login it is describing.
	claudeAccountCombo *adw.ComboRow

	// State. Everything below is touched from the main thread except where a
	// mutex is named, and the mutex exists only because the refresh worker
	// publishes rows from a goroutine.
	mu        sync.RWMutex
	rows      []board.Row
	byPath    map[string]*board.Row
	byPtr     map[uintptr]*board.Row
	items     []*coreglib.Object
	jobByRepo map[string]*jobs.Snapshot
	jobIDs    map[string]uint64
	// pausing is the set of jobs whose pause or resume is still in flight.
	// Both halves run off the main thread and a resume can sit inside
	// lease.Resume for the ten seconds a cold model server takes, so without
	// this a second click reads the same pre-click state as the first and
	// queues an opposite transition behind it. Which of the two wins is then
	// whichever goroutine reaches setPaused first.
	pausing map[uint64]bool

	// The usage rollup: which agents actually ran graphify and graft, and
	// where. It is refreshed on its own goroutine (see usage.go) because a
	// cold read walks gigabytes of transcript, and published into usageUses
	// — the per-row, one-week window the board column renders — when it is
	// done. usageSaved throttles writing it back to disk.
	usage      *usage.Index
	usagePath  string
	usageUses  map[string]usage.RepoUse
	usageBusy  bool
	usageSaved time.Time
	// The Usage view is a whole page of the window, not a pane inside the
	// board: main switches between the board and it, and usageGuard stops the
	// header toggle and the keyboard from fighting each other over which one
	// set it.
	main       *gtk.Stack
	usagePane  *usagePane
	usageBtn   *gtk.ToggleButton
	usageGuard bool
	filterBar  *gtk.Box

	runner *jobs.Runner
	// autofix is the unattended repair loop's memory: which repositories it
	// has already tried, how that went, and which ones it has given up on.
	// The decisions live in internal/autofix; autofix.go in this package is
	// what turns them into chains. autofixBusy guards the one goroutine it is
	// allowed to have in flight, since the loop probes the local model server
	// and must not do that on the main thread.
	autofix     *autofix.Engine
	autofixBusy bool
	// ollama is the local model server's lifecycle: the board starts it for
	// a job that needs it and stops it again once nothing does. It is here
	// rather than in the jobs package because the policy — is this switched
	// on, how long is the grace period — is a setting, and the runner is not
	// the thing that reads settings.
	ollama  *gfy.AutoOllama
	cache   discover.Cache
	graphs  graphstate.Cache
	grafts  graftstate.Cache
	version gfy.Version

	// selectMode is the batch-action mode. `Space` adds a row to selected;
	// an action taken in this mode applies to all of them, behind a confirm
	// that names the count.
	selectMode bool
	selected   map[string]bool

	// refreshing guards against a slow scan and a fast tick overlapping: a
	// second scan while the first is in flight would double the work and
	// deliver its rows out of order.
	refreshing bool
	// dirty is set when a scan was requested while one was already running,
	// so the result is not simply dropped.
	dirty bool

	tickID coreglib.SourceHandle
}

// New builds the board.
func New(opts Options) *App {
	if opts.Refresh <= 0 {
		opts.Refresh = 30 * time.Second
	}
	if len(opts.Roots) == 0 {
		opts.Roots = discover.DefaultRoots()
	}
	if opts.Depth <= 0 {
		opts.Depth = discover.DefaultDepth
	}
	if opts.StartWidth <= 0 {
		opts.StartWidth = 1280
	}
	if opts.StartHeight <= 0 {
		opts.StartHeight = 800
	}
	return &App{
		opts:      opts,
		byPath:    map[string]*board.Row{},
		byPtr:     map[uintptr]*board.Row{},
		jobByRepo: map[string]*jobs.Snapshot{},
		jobIDs:    map[string]uint64{},
		pausing:   map[uint64]bool{},
		cols:      map[string]*gtk.ColumnViewColumn{},
		sorters:   map[string]*gtk.CustomSorter{},
		chips:     map[string]*gtk.ToggleButton{},
		setCols:   map[string]*adw.SwitchRow{},
		dockRows:  map[string]*adw.SwitchRow{},
		selected:  map[string]bool{},
		usageUses: map[string]usage.RepoUse{},
		autofix:   autofix.New(),
	}
}

// Run enters the GTK main loop and returns the process exit code.
func (a *App) Run(args []string) int {
	a.app = adw.NewApplication(AppID, gio.ApplicationNonUnique)
	a.app.ConnectActivate(func() { a.activate() })
	code := a.app.Run(args)
	if a.runner != nil {
		// The queue is written down BEFORE Close, which cancels it: the next
		// session is owed the jobs somebody queued, not a list of jobs
		// cancelled by the act of quitting.
		a.persistJobs()
		// A watcher started from the board is a child of the board and must
		// not outlive it. Close cancels everything still in flight and waits
		// for the process groups to go.
		a.runner.Close()
	}
	// After Close, so the last job has already given its lease back: a server
	// stopped while a graphify process was still sending to it would turn a
	// clean shutdown into that job's failure.
	a.ollama.Shutdown()
	if a.opts.Store != nil {
		if err := a.opts.Store.Flush(); err != nil {
			log.Printf("store: %v", err)
		}
	}
	return code
}

func (a *App) activate() {
	a.applyColorScheme()
	a.loadCSS()

	set := a.opts.Store.Settings()
	// Both policy funcs read the store at the moment they are asked rather
	// than closing over `set`, so flipping the switch or dragging the grace
	// period takes effect on the next job with nothing to re-apply.
	a.ollama = &gfy.AutoOllama{
		Enabled: func() bool { return a.opts.Store.Settings().AutoOllama() },
		Idle:    func() time.Duration { return a.opts.Store.Settings().OllamaIdle() },
		Logf:    applog.Infof,
	}
	a.runner = jobs.New(jobs.Options{
		FreeLanes:    set.FreeLanes,
		MeteredLanes: set.MeteredLanes,
		LocalLanes:   set.LocalLanes,
		LogBytes:     set.LogBytes,
		Precheck:     jobs.RequireGraph,
		LocalLease:   a.leaseOllama,
	})
	// Before the pump, because Restore deliberately emits nothing: the views
	// below are built from Snapshot, which already has the restored jobs in
	// it, and an event storm into a channel nobody is draining yet would be
	// dropped anyway.
	restored, held := a.restoreJobs()
	go a.pumpJobs()

	a.win = adw.NewApplicationWindow(&a.app.Application)
	a.win.SetTitle(WindowTitle)
	w, h := a.opts.StartWidth, a.opts.StartHeight
	if g, ok := a.opts.Store.Window(); ok {
		w, h = g.Width, g.Height
	}
	a.win.SetDefaultSize(w, h)

	a.title = adw.NewWindowTitle(WindowTitle, "scanning…")

	toolbar := adw.NewToolbarView()
	toolbar.AddTopBar(a.buildHeader())
	a.filterBar = a.buildFilterBar()
	toolbar.AddTopBar(a.filterBar)

	// The version banner. graphify moves fast and the argv builders in
	// internal/gfy are pinned to one release; a board running against a
	// version it was not written for says so up front rather than failing
	// mysteriously three clicks later.
	a.banner = adw.NewBanner("")
	a.banner.SetRevealed(false)
	toolbar.AddTopBar(a.banner)

	a.split = gtk.NewPaned(gtk.OrientationHorizontal)
	a.split.SetStartChild(a.buildBoard())
	a.detail = a.newDetailPane()
	a.split.SetEndChild(a.detail.widget)
	a.split.SetPosition(w * 3 / 5)
	a.split.SetResizeStartChild(true)
	a.split.SetResizeEndChild(true)
	// Both halves may shrink below their natural size. A tiling compositor
	// hands this window whatever cell it has, and a paned that refuses to
	// shrink turns that into a stream of GTK size warnings and a clipped
	// board rather than a cramped but usable one.
	a.split.SetShrinkStartChild(true)
	a.split.SetShrinkEndChild(true)

	// The bottom panel hangs below both halves rather than inside one of
	// them: what is running, and what the board has to say for itself, are
	// facts about the whole machine and not about the selected row.
	a.dock = a.buildDock()
	a.vsplit = gtk.NewPaned(gtk.OrientationVertical)
	a.vsplit.SetStartChild(a.split)
	a.vsplit.SetEndChild(a.dock.widget)
	a.vsplit.SetVExpand(true)
	a.vsplit.SetResizeStartChild(true)
	a.vsplit.SetResizeEndChild(false)
	a.vsplit.SetShrinkStartChild(true)
	a.vsplit.SetShrinkEndChild(false)
	a.split.SetVExpand(true)

	// The window has two pages, not one. The board — rows, detail pane and
	// bottom dock — is one of them; the Usage dashboard is the other, and it
	// takes the whole window rather than a corner of it, because what it
	// reports is about the machine and not about the selected row.
	a.usagePane = a.newUsagePane()
	a.main = gtk.NewStack()
	a.main.SetTransitionType(gtk.StackTransitionTypeCrossfade)
	a.main.AddNamed(a.vsplit, pageBoard)
	a.main.AddNamed(a.usagePane.widget, pageUsageName)
	a.main.SetVExpand(true)

	body := gtk.NewBox(gtk.OrientationVertical, 0)
	body.Append(a.main)
	body.Append(a.buildStatusBar())

	toolbar.SetContent(body)

	a.toasts = adw.NewToastOverlay()
	a.toasts.SetChild(toolbar)
	a.win.SetContent(a.toasts)

	a.installKeys()
	a.win.ConnectCloseRequest(func() bool {
		a.saveWindow()
		a.saveUsage()
		// Nothing is torn down here: Run's deferred Close does it after the
		// main loop returns, so a job's SIGTERM grace period does not happen
		// with a half-destroyed window on screen.
		return false
	})

	a.win.SetVisible(true)
	a.announceRestored(restored, held)
	a.applyDockSettings()
	a.restoreFilter()
	a.refresh(true)
	a.startUsage()
	a.startTick()
	go a.probeVersion()
}

// buildStatusBar is the one-line summary under the board: how many
// repositories there are, how they break down, what is running, and which
// graphify this board is driving.
func (a *App) buildStatusBar() gtk.Widgetter {
	a.status = gtk.NewLabel("")
	a.status.SetXAlign(0)
	a.status.AddCSSClass("dim-label")
	a.status.AddCSSClass("statusbar")
	a.status.SetEllipsize(3) // PANGO_ELLIPSIZE_END
	box := gtk.NewBox(gtk.OrientationHorizontal, 6)
	box.SetMarginStart(12)
	box.SetMarginEnd(12)
	box.SetMarginTop(4)
	box.SetMarginBottom(4)
	a.status.SetHExpand(true)
	box.Append(a.status)
	return box
}

func (a *App) saveWindow() {
	if a.win == nil || a.opts.Store == nil {
		return
	}
	a.saveDockHeight()
	w, h := a.win.DefaultSize()
	if w > 0 && h > 0 {
		a.opts.Store.SetWindow(store.Geometry{Width: w, Height: h, Maximized: a.win.IsMaximized()})
	}
}

// startTick arms the periodic refresh and the once-a-second repaint.
//
// They are two different cadences on purpose. The scan is expensive — a walk
// plus a streaming parse per repository — and belongs on the user's configured
// interval. The repaint is nearly free and has to be fast: a running job's
// elapsed time and its log are what makes the board feel alive.
func (a *App) startTick() {
	every := int(a.opts.Refresh / time.Second)
	if every < 2 {
		every = 2
	}
	coreglib.TimeoutSecondsAdd(uint(every), func() bool {
		a.refresh(false)
		// The usage rollup rides the same interval. After the first read it
		// is a few milliseconds of stat calls, and it is what keeps the usage
		// column and the dashboard current while the board is left open.
		a.refreshUsage()
		return true
	})
	a.tickID = coreglib.TimeoutAdd(1000, func() bool {
		a.tick()
		return true
	})
}

// tick is the once-a-second repaint: elapsed times, the status bar, and the
// job log if it has moved.
func (a *App) tick() {
	a.refreshStatus()
	a.detail.tick()
	a.usagePane.tick(a.onUsagePage())
	if a.dock != nil {
		a.dock.tick()
	}
	if a.jobsPage != nil {
		a.jobsPage.tick()
	}
}

// outLocation is where the board looks for every repository's graph: the
// command line when it said, else the stored preference, else blank — which
// leaves graphify's own default and $GRAPHIFY_OUT_NAME in charge.
// outLocation delegates to the store, which is the single resolver: the board
// must read from the directory its jobs write to, and two copies of this rule
// is exactly how they came apart. The flags reach the store through
// SetOutOverride at startup.
func (a *App) outLocation() (name, base string) {
	return a.opts.Store.OutLocation()
}

// refresh scans in a goroutine and folds the result in on the main thread.
//
// full forces the discovery cache to be re-walked; an ordinary tick trusts it.
func (a *App) refresh(full bool) {
	if a.refreshing {
		a.dirty = true
		return
	}
	a.refreshing = true
	if full {
		a.cache.Invalidate()
		a.graphs.Clear()
		a.grafts.Clear()
	}

	set := a.opts.Store.Settings()
	roots := a.opts.Roots
	if len(set.Roots) > 0 && !a.opts.SetFlags["roots"] {
		roots = set.Roots
	}
	depth := a.opts.Depth
	if set.Depth > 0 && !a.opts.SetFlags["depth"] {
		depth = set.Depth
	}
	showHidden := set.ShowHidden
	if a.opts.SetFlags["hidden"] {
		showHidden = a.opts.ShowHidden
	}
	outName, outBase := a.outLocation()
	st := a.opts.Store

	go func() {
		rows, err := board.Scan(board.Options{
			Roots:      roots,
			Depth:      depth,
			OutName:    outName,
			OutBase:    outBase,
			ShowHidden: showHidden,
			Cache:      &a.cache,
			Graphs:     &a.graphs,
			Grafts:     &a.grafts,
			Override: func(path string) (string, bool, bool) {
				o := st.Override(path)
				return o.Out, o.ExcludeBatch, o.Pinned
			},
			Baseline: st.DriftBaseline,
		})
		coreglib.IdleAdd(func() {
			a.refreshing = false
			if err != nil {
				applog.Errorf("scan failed: %v", err)
				a.toastf("scan failed: %v", err)
			} else {
				a.setRows(rows)
			}
			if a.dirty {
				a.dirty = false
				a.refresh(false)
			}
		})
	}()
}

// setRows folds a completed scan into the model. Main thread only.
func (a *App) setRows(rows []board.Row) {
	// Splice only when the *set* of repositories moved. When it did not, the
	// existing model objects stay bound at the same index and only the Row
	// behind each pointer is new — which is what lets a 30-second rescan of
	// ~300 repositories repaint without throwing away the scroll position or
	// the selection.
	same := len(rows) == len(a.rows)
	if same {
		for i := range rows {
			if rows[i].Path != a.rows[i].Path {
				same = false
				break
			}
		}
	}

	a.mu.Lock()
	a.rows = rows
	if !same || len(a.items) != len(rows) {
		a.items = make([]*coreglib.Object, len(rows))
		for i := range rows {
			a.items[i] = gtk.NewStringObject(rows[i].Path).Object
		}
	}
	a.byPath = make(map[string]*board.Row, len(rows))
	a.byPtr = make(map[uintptr]*board.Row, len(rows))
	for i := range a.rows {
		r := &a.rows[i]
		a.byPath[r.Path] = r
		if i < len(a.items) {
			a.byPtr[a.items[i].Native()] = r
		}
	}
	items := a.items
	a.mu.Unlock()

	if !same {
		a.model.Splice(0, a.model.NItems(), items)
		a.restoreSelection()
	} else {
		// The rows are the same rows, but their state may have moved across a
		// filter boundary or a sort key — so the filter and sorter are told,
		// and the realised cells are repainted.
		a.cfilt.Changed(gtk.FilterChangeDifferent)
	}
	// The folder list is derived from the rows, so it is rebuilt here and
	// nowhere else: a root added in settings, a checkout cloned into one, a
	// directory emptied out all reach the dropdown by way of a scan.
	a.refreshGroups(rows)

	a.view.QueueDraw()
	a.refreshStatus()
	a.detail.reload()

	// The scan that just landed is the freshest picture of every repository
	// there is, which makes this — and not a timer of its own — the right
	// moment to ask whether any of them should be repaired. See autofix.go.
	a.autoFixTick()
}

// rowFor resolves a model object back to its row.
func (a *App) rowFor(obj *coreglib.Object) *board.Row {
	if obj == nil {
		return nil
	}
	a.mu.RLock()
	if r := a.byPtr[obj.Native()]; r != nil {
		a.mu.RUnlock()
		return r
	}
	a.mu.RUnlock()
	so, ok := obj.Cast().(*gtk.StringObject)
	if !ok {
		return nil
	}
	return a.row(so.String())
}

// row looks a row up by checkout path.
func (a *App) row(path string) *board.Row {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.byPath[path]
}

// current is the selected row, or nil.
func (a *App) current() *board.Row {
	if a.sel == nil {
		return nil
	}
	item := a.sel.SelectedItem()
	if item == nil {
		return nil
	}
	return a.rowFor(item)
}

// allRows is a snapshot of the model, for the batch actions and the settings
// dialog.
func (a *App) allRows() []board.Row {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return append([]board.Row(nil), a.rows...)
}

// visibleRows is what the filter and search currently admit, in sort order —
// which is what a batch action over "everything on screen" has to mean.
func (a *App) visibleRows() []board.Row {
	var out []board.Row
	n := a.sorted.NItems()
	for i := uint(0); i < n; i++ {
		if r := a.rowFor(a.sorted.Item(i)); r != nil {
			out = append(out, *r)
		}
	}
	return out
}

// refreshStatus repaints the summary line. Called on the one-second tick, so
// it must not allocate much and must not touch the filesystem.
func (a *App) refreshStatus() {
	if a.status == nil {
		return
	}
	a.refreshActivity()
	c := board.Summarize(a.allRows())
	queued, running := a.runner.Active()
	free, metered, localLanes := a.runner.Lanes()

	var b strings.Builder
	b.WriteString(plural(c.Repos, "repo", "repos"))
	b.WriteString(" · ")
	b.WriteString(plural(c.Graphed, "graphed", "graphed"))
	if c.Fresh > 0 {
		b.WriteString(" · ")
		b.WriteString(plural(c.Fresh, "fresh", "fresh"))
	}
	if c.Stale > 0 {
		b.WriteString(" · ")
		b.WriteString(plural(c.Stale, "stale", "stale"))
	}
	if c.Raw > 0 {
		b.WriteString(" · ")
		b.WriteString(plural(c.Raw, "unlabeled", "unlabeled"))
	}
	if c.Broken > 0 {
		b.WriteString(" · ")
		b.WriteString(plural(c.Broken, "broken", "broken"))
	}
	if c.Behind > 0 {
		b.WriteString(" · ")
		b.WriteString(plural(c.Behind, "behind HEAD", "behind HEAD"))
	}
	// The graft half of the same summary, kept to one clause: how many
	// checkouts carry a graft index at all, and how many of those the tree has
	// moved under — which is exactly what the sync button acts on.
	if c.Grafted > 0 {
		b.WriteString(" · ")
		b.WriteString(gfy.Itoa(c.Grafted))
		b.WriteString(" grafted")
		if c.GraftStale > 0 {
			b.WriteString(" (")
			b.WriteString(gfy.Itoa(c.GraftStale))
			b.WriteString(" stale)")
		}
	}
	if running > 0 || queued > 0 {
		b.WriteString(" · ")
		b.WriteString(plural(running, "job running", "jobs running"))
		if queued > 0 {
			b.WriteString(", ")
			b.WriteString(plural(queued, "queued", "queued"))
			// A held job is queued but will never start on its own, and a
			// status bar that counted it as queued would leave somebody
			// waiting all afternoon for work that is waiting for them.
			if held := a.runner.HeldCount(); held > 0 {
				b.WriteString(" (")
				b.WriteString(gfy.Itoa(held))
				b.WriteString(" held)")
			}
		}
		b.WriteString(" (")
		b.WriteString(gfy.Itoa(free))
		b.WriteString(" free / ")
		b.WriteString(gfy.Itoa(metered))
		b.WriteString(" metered / ")
		b.WriteString(gfy.Itoa(localLanes))
		b.WriteString(" local lanes)")
	}
	if a.groupID != "" {
		// The counters above are the whole board on purpose — "3 stale" means
		// three on this machine, not three in this folder — so the folder
		// filter says how much of the board is actually on screen.
		b.WriteString(" · folder ")
		b.WriteString(board.Tilde(a.groupID))
		b.WriteString(": ")
		// The filter model already holds the answer; materialising the rows
		// to count them would be a 300-struct copy on every one-second tick.
		b.WriteString(gfy.Itoa(int(a.filter.NItems())))
		b.WriteString(" shown")
	}
	if a.selectMode {
		b.WriteString(" · SELECT: ")
		b.WriteString(plural(len(a.selected), "row", "rows"))
	}
	if a.version.Found {
		b.WriteString(" · graphify ")
		b.WriteString(a.version.Number)
	}
	a.status.SetText(b.String())

	if a.title != nil {
		// The window title carries the same summary in short form: a tiling
		// compositor may give this window a cell too small for the status bar
		// to be legible, and the title is what the window switcher shows
		// either way.
		sub := plural(c.Repos, "repo", "repos") + " · " + gfy.Itoa(c.Graphed) + " graphed"
		if c.Stale > 0 {
			sub += " · " + gfy.Itoa(c.Stale) + " stale"
		}
		if running > 0 {
			sub += " · " + plural(running, "job", "jobs") + " running"
		}
		if a.filterID != "" {
			sub += " · filtered: " + a.filterID
		}
		if a.groupID != "" {
			sub += " · " + board.Tilde(a.groupID)
		}
		a.title.SetSubtitle(sub)
	}
}

// probeVersion resolves the graphify binary off the main thread and raises the
// banner when the version is not the one the argv builders were written for.
func (a *App) probeVersion() {
	v := gfy.Probe(context.TODO())
	coreglib.IdleAdd(func() {
		a.version = v
		if v.Found {
			applog.Infof("graphify %s at %s", v.Number, v.Bin)
		} else {
			applog.Errorf("graphify not usable: %s", v.Err)
		}
		a.refreshStatus()
		switch {
		case !v.Found:
			a.banner.SetTitle("graphify was not found or would not run: " + v.Err)
			a.banner.SetButtonLabel("")
			a.banner.SetRevealed(true)
		case !v.Supported():
			a.banner.SetTitle("ggraphify's command builders were written against graphify " +
				gfy.BuiltAgainst + "; this machine has " + v.Number + " — commands may have moved")
			a.banner.SetButtonLabel("")
			a.banner.SetRevealed(true)
		case v.SkillSkew != "":
			// Real on this machine right now, and a one-click fix: graphify
			// prints this warning on *every* invocation, so it is in every
			// job log until somebody re-runs the installer.
			a.banner.SetTitle("An installed agent skill is older than graphify: " + v.SkillSkew)
			a.banner.SetButtonLabel("Update agent skills")
			a.banner.ConnectButtonClicked(func() { a.updateSkills() })
			a.banner.SetRevealed(true)
		default:
			a.banner.SetRevealed(false)
		}
	})
}

// pumpJobs forwards runner events onto the main thread.
func (a *App) pumpJobs() {
	for ev := range a.runner.Events() {
		ev := ev
		coreglib.IdleAdd(func() { a.onJobEvent(ev) })
	}
}

// onJobEvent folds one job transition into the board. Main thread.
func (a *App) onJobEvent(ev jobs.Event) {
	if ev.Output {
		// Log-only, and it carries no snapshot: the panes render from the ring
		// buffer on their own tick and compare its generation against the last
		// one they drew, so there is nothing to fold in here. Returning before
		// the repo bookkeeping below is not a shortcut — that bookkeeping reads
		// fields an output event does not have, and cannot change mid-run
		// anyway.
		return
	}

	s := ev.Job
	if s.Repo != "" {
		a.mu.Lock()
		cur := a.jobIDs[s.Repo]
		// A finished job never displaces a newer one: events can arrive out
		// of order, and a stale completion overwriting a running job would
		// leave the row showing "ok" while a process is still going.
		if s.ID >= cur {
			a.jobIDs[s.Repo] = s.ID
			snap := s
			a.jobByRepo[s.Repo] = &snap
		}
		a.mu.Unlock()
	}

	if ev.Pause {
		// Not a lifecycle transition: the job is Running on both sides of it,
		// so it goes nowhere near the run history and is not a second "job
		// started" line. The views still have to repaint — the row's status
		// word, the spinner and the button that now says the opposite thing.
		if s.Paused {
			applog.Infof("job %d paused: %s", s.ID, s.Label)
			a.toastf("%s paused", s.Label)
		} else {
			applog.Infof("job %d resumed: %s", s.ID, s.Label)
			a.toastf("%s resumed", s.Label)
		}
		a.view.QueueDraw()
		a.resortJobs()
		a.refreshStatus()
		a.detail.reloadJobs()
		if a.dock != nil {
			a.dock.reloadJobs()
		}
		if a.jobsPage != nil {
			a.jobsPage.reload()
		}
		return
	}

	// Every lifecycle transition rewrites the sidecar's job list — a
	// submission, a start, a completion. It is one debounced write of a
	// bounded list, and it is what makes the queue survive a crash rather
	// than only a clean quit, where the handler in Run would have caught it.
	a.persistJobs()
	switch s.Status {
	case jobs.Running:
		applog.Infof("job %d started: %s", s.ID, s.Command())
	case jobs.Succeeded:
		applog.Infof("job %d succeeded in %s: %s", s.ID, shortDur(s.Elapsed()), s.Label)
		a.toastf("%s finished in %s", s.Label, shortDur(s.Elapsed()))
		// A mutating job changed graphify-out/; re-derive that row now rather
		// than waiting up to 30 seconds for the next tick.
		if s.Kind == "install" {
			a.onSkillInstalled()
		}
		rescans := gfy.Rescans(s.Kind)
		if gfy.Graft(s.Kind) {
			// A graft run rewrote <repo>/graft and nothing in graphify-out/,
			// so only that half of the row is re-derived.
			a.grafts.Invalidate(s.Repo)
			a.refresh(false)
			if s.Kind == "graft-init" {
				// Every check in the settings group just became stale, in the
				// one direction that matters: it was the fix.
				a.checkGraftSetup(false)
			}
		} else if gfy.Known[s.Kind].Mutates {
			// The job changed graphify-out/, so drop that row's cached
			// derivation and re-derive now rather than waiting up to a full
			// tick for the board to catch up with what just happened.
			a.graphs.Invalidate(s.Repo)
			if !rescans {
				a.refresh(false)
			}
		}
		if rescans {
			// Deliberately no refresh here: the scan would race the baseline
			// walk below and derive this row from the *old* baseline, which
			// is the whole reason a repository whose only drift was settled
			// by this very run kept showing as stale. ackDrift refreshes once
			// the baseline is recorded.
			a.ackDrift(s.Repo, s.Started)
		}
	case jobs.Failed:
		// Deliberately not a toast that disappears. A failure pins itself on
		// the row's Job cell and stays there until a later job supersedes it,
		// because "it failed while I was looking elsewhere" is the case that
		// matters.
		line := gfy.FirstErrorLine(s.Log.String())
		applog.Errorf("job %d failed (exit %d): %s — %s", s.ID, s.Exit, s.Command(), firstNonEmpty(line, s.Err))
		if line != "" {
			a.toastf("%s failed: %s", s.Label, ellipsize(line, 90))
		} else {
			a.toastf("%s failed (exit %d)", s.Label, s.Exit)
		}
	case jobs.Canceled:
		applog.Warnf("job %d cancelled: %s", s.ID, s.Label)
		a.toastf("%s cancelled", s.Label)
	}
	a.view.QueueDraw()
	a.resortJobs()
	a.refreshStatus()
	a.detail.reloadJobs()
	if a.dock != nil {
		a.dock.reloadJobs()
	}
	if a.jobsPage != nil {
		a.jobsPage.reload()
	}
}

// ackDrift settles what a successful tree rescan left behind.
//
// graphify has just walked this checkout and written its manifest. Whatever the
// drift walk still reports is therefore not "graphify has not caught up" — it is
// "graphify looked at these files and did not graph them", which it does for
// reasons the board cannot see from outside: a lock file, a credential
// directory, a 24 MB JSON it parsed and found no symbols in, a language whose
// grammar is not installed. Before this, those files pinned the row to `stale`
// forever and made "bring this repository to healthy" an unreachable goal — the
// Fix button would run update, update would succeed, and the number would not
// move.
//
// The walk runs off the main thread: it is the expensive half of a refresh.
func (a *App) ackDrift(repo string, since time.Time) {
	if repo == "" || a.opts.Store == nil {
		return
	}
	out := a.opts.Store.Out(repo)
	if r := a.row(repo); r != nil && r.Graph.Out != "" {
		out = r.Graph.Out
	}
	st := a.opts.Store
	go func() {
		// Deliberately the raw walk, with no baseline: the point is to record
		// the whole of what is left, not to add to what was recorded last time.
		d := graphstate.DriftOf(repo, out, nil, graphstate.Baseline{})
		b := graphstate.MakeBaseline(repo, d, since)
		coreglib.IdleAdd(func() {
			st.SetDriftBaseline(repo, b)
			a.graphs.Invalidate(repo)
			a.refresh(false)
			if n := len(b.Added) + len(b.Removed); n > 0 {
				applog.Infof("drift baseline for %s: %d path(s) graphify declined to graph", repo, n)
			}
		})
	}()
}

// jobFor is the latest job for a repository, or nil.
func (a *App) jobFor(path string) *jobs.Snapshot {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.jobByRepo[path]
}

// displayState is the row's state with a running job folded in, which is what
// the dot actually shows.
func (a *App) displayState(r *board.Row) graphstate.State {
	if s := a.jobFor(r.Path); s != nil && !s.Status.Done() {
		return graphstate.StateRunning
	}
	return r.Graph.State
}

// toast shows a transient message.
//
// Every toast is also an entry in the application log. A toast is by
// definition something the board wanted to say and then took away again; the
// log pane is where it stays long enough to be read, or copied.
func (a *App) toast(msg string) {
	applog.Infof("%s", msg)
	if a.toasts == nil {
		return
	}
	a.toasts.AddToast(adw.NewToast(msg))
	if a.dock != nil {
		a.dock.notifyLog()
	}
}

func (a *App) toastf(format string, args ...any) {
	a.toast(sprintf(format, args...))
}

// applyColorScheme pushes the current preference into libadwaita.
func (a *App) applyColorScheme() {
	sm := adw.StyleManagerGetDefault()
	switch a.currentScheme() {
	case "dark":
		sm.SetColorScheme(adw.ColorSchemeForceDark)
	case "light":
		sm.SetColorScheme(adw.ColorSchemeForceLight)
	default:
		// There may be no portal on this desktop, so honour the plain
		// environment variables GTK understands directly before falling back
		// to "whatever the system says".
		v := strings.ToLower(os.Getenv("ADW_DEBUG_COLOR_SCHEME") + " " + os.Getenv("GTK_THEME"))
		if strings.Contains(v, "dark") {
			sm.SetColorScheme(adw.ColorSchemeForceDark)
		} else {
			sm.SetColorScheme(adw.ColorSchemeDefault)
		}
	}
	a.refreshSchemeButton()
}

// schemePinned reports whether -dark/-light decided the scheme for this run.
func (a *App) schemePinned() bool { return a.opts.Dark || a.opts.ForceLight }

func (a *App) currentScheme() string {
	switch {
	case a.opts.Dark:
		return "dark"
	case a.opts.ForceLight:
		return "light"
	}
	return a.opts.Store.Scheme()
}

var schemeCycle = []string{"system", "light", "dark"}

func schemeLabel(v string) string {
	switch v {
	case "light":
		return "☀"
	case "dark":
		return "☾"
	}
	return "◐"
}

func (a *App) cycleScheme() {
	if a.schemePinned() {
		a.toast("colour scheme is pinned by the -dark/-light flag for this run")
		return
	}
	cur := a.opts.Store.Scheme()
	next := schemeCycle[0]
	for i, v := range schemeCycle {
		if v == cur {
			next = schemeCycle[(i+1)%len(schemeCycle)]
			break
		}
	}
	a.opts.Store.SetScheme(next)
	a.applyColorScheme()
	a.toast("colour scheme: " + next)
}

func (a *App) refreshSchemeButton() {
	if a.scheme == nil {
		return
	}
	a.scheme.SetLabel(schemeLabel(a.currentScheme()))
}

// loadCSS installs the board's own classes. They are deliberately few: the
// board is a libadwaita application and should look like one, so this adds
// only the state colours and the monospace log face, and takes the rest from
// the platform stylesheet.
// boardCSS is the board's own stylesheet. They are deliberately few: the
// board is a libadwaita application and should look like one, so this adds
// only the state colours, the monospace log face and the bottom panel's
// chrome, and takes the rest from the platform stylesheet.
//
// It is a package-level constant rather than a literal inside loadCSS so a
// test can assert that every class the widgets ask for is actually defined
// here — a misspelt class is otherwise invisible until somebody notices a
// mark that never turns red.
const boardCSS = `
.st-fresh   { color: @success_color; }
.st-stale   { color: @warning_color; }
.st-raw     { color: @accent_color; }
.st-broken  { color: @error_color; }
.st-running { color: @accent_color; font-weight: bold; }
.st-none    { opacity: 0.45; }
.behind     { color: @warning_color; }
.joblog     { font-family: monospace; font-size: 0.9em; }
.statusbar  { font-size: 0.9em; }
.cellpad    { padding-left: 6px; padding-right: 6px; }
.dim        { opacity: 0.6; }
.metered    { color: @warning_color; font-weight: bold; }
.argv       { font-family: monospace; font-size: 0.9em; }
.dockbar    { min-height: 28px; }
.dockhead   { font-size: 0.78em; font-weight: bold; letter-spacing: 0.08em; }
.dockheadrow{ background: alpha(currentColor, 0.04); }
.docknote   { font-size: 0.85em; opacity: 0.55; }
.docklist   { background: transparent; }
.jobprogress{ min-height: 6px; }
.check-ok   { color: @success_color; }
.check-warn { color: @warning_color; }
.check-bad  { color: @error_color; }
`

// loadCSS installs the board's own classes.
func (a *App) loadCSS() {
	css := gtk.NewCSSProvider()
	css.LoadFromString(boardCSS)
	if d := gtk.WidgetGetDefaultDirection(); d == gtk.TextDirRTL {
		_ = d // no RTL-specific rules yet; kept so the lint is honest
	}
	gtk.StyleContextAddProviderForDisplay(
		displayDefault(), css, uint(gtk.STYLE_PROVIDER_PRIORITY_APPLICATION))
}
