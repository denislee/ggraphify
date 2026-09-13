package ui

import (
	"strings"
	"unsafe"

	coreglib "github.com/diamondburned/gotk4/pkg/core/glib"
	"github.com/diamondburned/gotk4/pkg/gio/v2"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"

	"github.com/dns/ggraphify/internal/board"
	"github.com/dns/ggraphify/internal/graphstate"
)

// colSpec is one column's registry entry: its stable id (which is what the
// sidecar remembers and what a settings switch names), its title, its default
// width, how it sorts, and how it renders.
type colSpec struct {
	ID     string
	Title  string
	Width  int
	Expand bool
	// cmp sorts two rows. A nil cmp makes the column unsortable.
	cmp func(a *App, x, y *board.Row) int
	// render fills a cell. It is handed a fresh label per realised cell and
	// the row currently bound to it.
	render func(a *App, l *gtk.Label, r *board.Row) (text, class string)
}

// columns is the whole board. Adding one here is the only thing needed to add
// one to the board, to the settings dialog's visibility switches and to the
// persisted layout — a column cannot exist in one of those and not the others.
var columns = []colSpec{
	{
		ID: "state", Title: "●", Width: 44,
		cmp: func(a *App, x, y *board.Row) int { return stateWeight(a, x) - stateWeight(a, y) },
		render: func(a *App, l *gtk.Label, r *board.Row) (string, string) {
			s := a.displayState(r)
			return stateGlyph(s), stateClass(s)
		},
	},
	{
		ID: "repo", Title: "Repo", Width: 240, Expand: true,
		cmp: func(a *App, x, y *board.Row) int { return strings.Compare(x.Name, y.Name) },
		render: func(a *App, l *gtk.Label, r *board.Row) (string, string) {
			name := r.Name
			if r.IsWorktree {
				name += " ⧉" // a linked worktree is its own row, and says so
			}
			if r.Excluded {
				name += " ⊘" // kept out of batch actions
			}
			if r.Group != "" {
				name += "  " + r.Group
			}
			return name, ""
		},
	},
	{
		// Which folder a repository was found in. The Repo column carries the
		// subdirectory below a root; this one carries the root itself, which
		// is what tells two checkouts of the same name apart and what the
		// folder filter is keyed on.
		ID: "folder", Title: "Folder", Width: 130,
		cmp: func(a *App, x, y *board.Row) int {
			return strings.Compare(board.GroupOf(*x), board.GroupOf(*y))
		},
		render: func(a *App, l *gtk.Label, r *board.Row) (string, string) {
			return board.Tilde(r.Root), "dim"
		},
	},
	{
		ID: "branch", Title: "Branch", Width: 180,
		cmp: func(a *App, x, y *board.Row) int { return strings.Compare(x.Ref(), y.Ref()) },
		render: func(a *App, l *gtk.Label, r *board.Row) (string, string) {
			text, behind := branchCell(*r)
			if behind {
				// The graph was built at a different commit than HEAD. This is
				// not the same as file drift and the board refuses to conflate
				// them: a branch switch moves this without touching an mtime.
				return text + " ≠", "behind"
			}
			return text, ""
		},
	},
	{
		ID: "graph", Title: "Graph", Width: 130,
		cmp: func(a *App, x, y *board.Row) int { return x.Graph.Nodes - y.Graph.Nodes },
		render: func(a *App, l *gtk.Label, r *board.Row) (string, string) {
			if r.Graph.State == graphstate.StateNone {
				return "", "st-none"
			}
			return graphCell(r.Graph), ""
		},
	},
	{
		ID: "comm", Title: "Comm.", Width: 110,
		cmp: func(a *App, x, y *board.Row) int { return x.Graph.Communities - y.Graph.Communities },
		render: func(a *App, l *gtk.Label, r *board.Row) (string, string) {
			if r.Graph.Communities > 0 && !r.Graph.Labeled {
				return commCell(r.Graph), "st-raw"
			}
			return commCell(r.Graph), ""
		},
	},
	{
		ID: "drift", Title: "Drift", Width: 150,
		// Sorting by the total, not by the string: "+12 ~30" and "~3" compare
		// the wrong way round alphabetically, and drift is the column people
		// sort by to find what to update next.
		cmp: func(a *App, x, y *board.Row) int { return driftWeight(x) - driftWeight(y) },
		render: func(a *App, l *gtk.Label, r *board.Row) (string, string) {
			s := r.Graph.DriftString()
			if s == "" {
				return "", ""
			}
			if r.Graph.NeedsUpdate {
				return s, "st-broken"
			}
			return s, "st-stale"
		},
	},
	{
		ID: "built", Title: "Built", Width: 80,
		cmp: func(a *App, x, y *board.Row) int {
			switch {
			case x.Graph.BuiltAt.Equal(y.Graph.BuiltAt):
				return 0
			case x.Graph.BuiltAt.After(y.Graph.BuiltAt):
				return -1 // newest first, which is the useful default here
			}
			return 1
		},
		render: func(a *App, l *gtk.Label, r *board.Row) (string, string) {
			return board.Age(r.Graph.BuiltAt), "st-none"
		},
	},
	{
		ID: "size", Title: "Size", Width: 90,
		cmp: func(a *App, x, y *board.Row) int {
			d := x.Graph.SizeBytes - y.Graph.SizeBytes
			switch {
			case d > 0:
				return 1
			case d < 0:
				return -1
			}
			return 0
		},
		render: func(a *App, l *gtk.Label, r *board.Row) (string, string) {
			return board.Bytes(r.Graph.SizeBytes), "st-none"
		},
	},
	{
		ID: "job", Title: "Job", Width: 190,
		cmp: func(a *App, x, y *board.Row) int { return jobWeight(a, x) - jobWeight(a, y) },
		render: func(a *App, l *gtk.Label, r *board.Row) (string, string) {
			return jobCell(a.jobFor(r.Path))
		},
	},
}

// jobWeight orders the Job column by how much attention the row wants:
// failures first, then running, then queued, then everything else.
func jobWeight(a *App, r *board.Row) int {
	s := a.jobFor(r.Path)
	if s == nil {
		return 0
	}
	switch s.Status.String() {
	case "failed":
		return -3
	case "running":
		return -2
	case "queued":
		return -1
	}
	return 1
}

// stateWeight orders the state column by how much attention a row wants,
// which is not the order the constants happen to be declared in.
//
// Sorting by the raw ordinal put "no graph" — 140 rows of it on this machine —
// above everything worth looking at. The useful default is: what is happening
// now, what is broken, what has fallen behind, what needs the expensive pass,
// what is fine, and what was never started.
func stateWeight(a *App, r *board.Row) int {
	switch a.displayState(r) {
	case graphstate.StateRunning:
		return 0
	case graphstate.StateBroken:
		return 1
	case graphstate.StateStale:
		return 2
	case graphstate.StateRaw:
		return 3
	case graphstate.StateFresh:
		return 4
	}
	return 5 // StateNone
}

// driftWeight is the single number the Drift column sorts on. A pending
// semantic re-extraction outranks any amount of file drift, because it is the
// one that needs the expensive command rather than the free one.
func driftWeight(r *board.Row) int {
	g := r.Graph
	n := g.DriftAdded + g.DriftChanged + g.DriftRemoved
	if g.NeedsUpdate {
		n += 100000
	}
	return n
}

// rowCell is the per-cell record: the widget and what was last written into
// it. Every field written is at least one cgo call, and bind runs for every
// realised cell on every repaint, so nothing is written unless it moved.
type rowCell struct {
	label *gtk.Label
	text  string
	class string
}

// buildBoard constructs the ColumnView and its models.
func (a *App) buildBoard() gtk.Widgetter {
	a.model = gio.NewListStore(gtk.GTypeStringObject)

	a.cfilt = gtk.NewCustomFilter(func(obj *coreglib.Object) bool {
		r := a.rowFor(obj)
		return r == nil || a.matches(r)
	})
	a.filter = gtk.NewFilterListModel(a.model, &a.cfilt.Filter)
	a.sorted = gtk.NewSortListModel(a.filter, nil)
	a.sel = gtk.NewSingleSelection(a.sorted)
	a.sel.SetAutoselect(false)
	a.sel.ConnectSelectionChanged(func(_, _ uint) { a.onSelectionChanged() })

	a.view = gtk.NewColumnView(a.sel)
	a.view.SetVExpand(true)
	a.view.SetHExpand(true)
	a.view.SetShowRowSeparators(false)
	a.view.SetShowColumnSeparators(false)
	a.view.SetReorderable(true)
	// Activating a row (double-click, Enter) opens the detail pane's most
	// useful page for what that row is: there is nothing to visualize on a
	// repository with no graph, so it gets the action list instead.
	a.view.ConnectActivate(func(pos uint) { a.activateRow() })

	// The sorter is the view's own, so clicking a header sorts and the
	// direction is GTK's to manage.
	a.sorted.SetSorter(a.view.Sorter())

	hidden := map[string]bool{}
	for _, id := range a.opts.Store.Hidden() {
		hidden[id] = true
	}
	widths := a.opts.Store.Columns()

	for i := range columns {
		spec := columns[i]
		w := spec.Width
		if v, ok := widths[spec.ID]; ok && v > 20 && v < 2000 {
			w = v
		}
		col := a.addColumn(spec, w)
		col.SetVisible(!hidden[spec.ID])
	}
	a.applyStoredSort()

	sw := gtk.NewScrolledWindow()
	sw.SetChild(a.view)
	sw.SetHExpand(true)
	sw.SetVExpand(true)
	sw.SetPolicy(gtk.PolicyAutomatic, gtk.PolicyAutomatic)
	a.rowsSW = sw
	return sw
}

func (a *App) addColumn(spec colSpec, width int) *gtk.ColumnViewColumn {
	factory := gtk.NewSignalListItemFactory()
	// One record per realised cell, keyed on the list item's native pointer.
	// GTK recycles cells aggressively, so a closure over a row would be bound
	// to whichever row happened to be there when the cell was created.
	cells := map[uintptr]*rowCell{}

	factory.ConnectSetup(func(obj *coreglib.Object) {
		li := asCell(obj)
		if li == nil {
			return
		}
		l := label("")
		if spec.ID == "state" {
			l.SetXAlign(0.5)
		}
		li.SetChild(cell(l))
		cells[li.Native()] = &rowCell{label: l}
	})
	factory.ConnectBind(func(obj *coreglib.Object) {
		li := asCell(obj)
		if li == nil {
			return
		}
		c := cells[li.Native()]
		if c == nil {
			return
		}
		r := a.rowFor(li.Item())
		if r == nil {
			return
		}
		text, class := spec.render(a, c.label, r)
		if c.text != text {
			c.label.SetText(text)
			c.text = text
		}
		setClass(c.label, &c.class, class)
		// The tooltip is built on hover, not on bind: it is a paragraph, and
		// building one per cell per repaint would dwarf the render itself.
		c.label.SetHasTooltip(true)
	})
	factory.ConnectUnbind(func(obj *coreglib.Object) {
		if li := asCell(obj); li != nil {
			if c := cells[li.Native()]; c != nil {
				c.label.SetHasTooltip(false)
			}
		}
	})
	factory.ConnectTeardown(func(obj *coreglib.Object) {
		if li := asCell(obj); li != nil {
			delete(cells, li.Native())
		}
	})

	col := gtk.NewColumnViewColumn(spec.Title, &factory.ListItemFactory)
	col.SetID(spec.ID)
	col.SetResizable(true)
	col.SetFixedWidth(width)
	col.SetExpand(spec.Expand)
	if spec.cmp != nil {
		cmp := spec.cmp
		sorter := gtk.NewCustomSorter(func(x, y unsafe.Pointer) int {
			rx, ry := a.rowForRaw(x), a.rowForRaw(y)
			if rx == nil || ry == nil {
				return 0
			}
			return cmp(a, rx, ry)
		})
		col.SetSorter(&sorter.Sorter)
	}
	a.view.AppendColumn(col)
	a.cols[spec.ID] = col
	// Wired after SetFixedWidth above, so the board's own write is not read
	// back as a drag.
	col.NotifyProperty("fixed-width", func() {
		a.opts.Store.SetColumnWidth(spec.ID, col.FixedWidth())
	})
	return col
}

// rowForRaw resolves the borrowed GObject pointers GTK hands a sorter, by
// address where possible. The sorter runs O(n log n) times per re-sort, so a
// map lookup is worth the indirection over a cgo cast per comparison.
func (a *App) rowForRaw(p unsafe.Pointer) *board.Row {
	if p == nil {
		return nil
	}
	a.mu.RLock()
	r := a.byPtr[uintptr(p)]
	a.mu.RUnlock()
	if r != nil {
		return r
	}
	return a.rowFor(coreglib.Take(p))
}

// cellItem is the part of a list item a factory needs. A ColumnView hands its
// factories *gtk.ColumnViewCell (which embeds ListItem); a plain ListView
// hands them *gtk.ListItem. Accept either.
type cellItem interface {
	SetChild(gtk.Widgetter)
	Item() *coreglib.Object
	Native() uintptr
}

func asCell(obj *coreglib.Object) cellItem {
	switch v := obj.Cast().(type) {
	case *gtk.ColumnViewCell:
		return v
	case *gtk.ListItem:
		return v
	}
	return nil
}

// matches is the search-and-filter predicate.
func (a *App) matches(r *board.Row) bool {
	if !board.InGroup(*r, a.groupID) {
		return false
	}
	if f := a.activeFilter(); f != "" {
		want, ok := graphstate.ParseState(f)
		if ok && a.displayState(r) != want {
			return false
		}
	}
	q := strings.TrimSpace(strings.ToLower(a.searchText()))
	if q == "" {
		return true
	}
	// Every space-separated term must match somewhere, fuzzily: "nova go"
	// means both, which is what a person typing two words into a filter box
	// expects. Each term is matched per field rather than against one joined
	// string, so a term cannot match half in the name and half in the path.
	name, group, path, branch := strings.ToLower(r.Name), strings.ToLower(r.Group),
		strings.ToLower(r.Path), strings.ToLower(r.Branch)
	for _, term := range strings.Fields(q) {
		if !fuzzyMatch(term, name, group, path, branch) {
			return false
		}
	}
	return true
}

func (a *App) searchText() string {
	if a.search == nil {
		return ""
	}
	return a.search.Text()
}

// applyStoredSort puts the board back on the column it was sorted by.
func (a *App) applyStoredSort() {
	set := a.opts.Store.Settings()
	col := a.cols[set.SortCol]
	if col == nil {
		col = a.cols["state"]
	}
	if col == nil {
		return
	}
	dir := gtk.SortAscending
	if set.SortDesc {
		dir = gtk.SortDescending
	}
	a.view.SortByColumn(col, dir)
}

// onSelectionChanged reloads the detail pane and remembers the selection.
func (a *App) onSelectionChanged() {
	r := a.current()
	if r == nil {
		a.detail.show(nil)
		return
	}
	a.opts.Store.SetSelected(r.Path)
	a.detail.show(r)
}

// restoreSelection puts the cursor back on the repository the last run ended
// on, if it is still there.
func (a *App) restoreSelection() {
	want := a.opts.Store.Selected()
	if want == "" {
		return
	}
	n := a.sorted.NItems()
	for i := uint(0); i < n; i++ {
		if r := a.rowFor(a.sorted.Item(i)); r != nil && r.Path == want {
			a.sel.SetSelected(i)
			a.view.ScrollTo(i, nil, gtk.ListScrollFocus, nil)
			return
		}
	}
}

// activateRow is Enter / double-click on a row.
func (a *App) activateRow() {
	r := a.current()
	if r == nil {
		return
	}
	if a.selectMode {
		a.toggleSelected(r)
		return
	}
	a.detail.focusBestPage(r)
}
