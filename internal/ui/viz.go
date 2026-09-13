package ui

import (
	"os"
	"path/filepath"

	"github.com/diamondburned/gotk4-adwaita/pkg/adw"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"

	"github.com/dns/ggraphify/internal/board"
	"github.com/dns/ggraphify/internal/open"
	"github.com/dns/ggraphify/internal/webview"
)

// vizTarget is one of the HTML artefacts graphify can produce for a repository.
type vizTarget struct {
	id    string
	title string
	file  string
	kind  string // the job kind that regenerates it
}

var vizTargets = []vizTarget{
	{"graph", "Interactive graph", "graph.html", "export-html"},
	{"tree", "Collapsible tree", "GRAPH_TREE.html", "tree"},
	{"callflow", "Call flow", "callflow.html", "export-callflow"},
}

// vizPage is the embedded-visualization page.
//
// It holds at most one WebKitWebView for the whole application, reused across
// repositories. A 1.9 MB graph.html with a force-directed D3 layout is not a
// free object, and the page destroys nothing but simply re-points the one view
// — which is both cheaper and what keeps the memory ceiling predictable.
type vizPage struct {
	a      *App
	widget gtk.Widgetter

	view     *webview.View
	holder   *adw.Bin
	chooser  *gtk.DropDown
	status   *gtk.Label
	fallback *adw.StatusPage

	row    *board.Row
	loaded string // the file currently displayed, so a re-show is free
	target int
}

func (a *App) newVizPage() *vizPage {
	v := &vizPage{a: a}

	names := make([]string, len(vizTargets))
	for i, t := range vizTargets {
		names[i] = t.title
	}
	v.chooser = gtk.NewDropDownFromStrings(names)
	v.chooser.NotifyProperty("selected", func() {
		v.target = int(v.chooser.Selected())
		v.loaded = ""
		v.show(v.row)
	})

	regen := gtk.NewButtonWithLabel("Regenerate")
	regen.AddCSSClass("flat")
	regen.SetTooltipText("Run the graphify export that produces this file. Free.")
	regen.ConnectClicked(func() {
		if v.row == nil {
			return
		}
		t := vizTargets[v.target]
		a.run(t.kind, []board.Row{*v.row}, nil)
	})

	external := gtk.NewButtonWithLabel("Open in browser")
	external.AddCSSClass("flat")
	external.ConnectClicked(func() { v.openExternal() })

	copyPath := gtk.NewButtonWithLabel("Copy path")
	copyPath.AddCSSClass("flat")
	copyPath.ConnectClicked(func() {
		if p := v.path(); p != "" {
			a.win.Clipboard().SetText(p)
			a.toast("path copied")
		}
	})

	v.status = gtk.NewLabel("")
	v.status.SetXAlign(0)
	v.status.SetHExpand(true)
	v.status.SetEllipsize(pangoEllipsizeEnd)
	v.status.AddCSSClass("dim-label")
	v.status.SetSelectable(true)

	// Two rows rather than one. The four controls already fill the detail
	// pane's width at any sane split position, and a path label competing with
	// them for the remainder is ellipsized down to a single "…" — which is
	// worse than no label at all, since the whole point of it is to say which
	// file is on screen.
	buttons := gtk.NewBox(gtk.OrientationHorizontal, 6)
	buttons.Append(v.chooser)
	spacer := gtk.NewLabel("")
	spacer.SetHExpand(true)
	buttons.Append(spacer)
	buttons.Append(regen)
	buttons.Append(copyPath)
	buttons.Append(external)

	bar := gtk.NewBox(gtk.OrientationVertical, 4)
	bar.SetMarginStart(12)
	bar.SetMarginEnd(12)
	bar.SetMarginTop(6)
	bar.SetMarginBottom(6)
	bar.Append(buttons)
	bar.Append(v.status)

	v.fallback = adw.NewStatusPage()
	v.fallback.SetIconName("web-browser-symbolic")
	v.holder = adw.NewBin()
	v.holder.SetVExpand(true)
	v.holder.SetChild(v.fallback)

	box := gtk.NewBox(gtk.OrientationVertical, 0)
	box.Append(bar)
	box.Append(v.holder)
	v.widget = box
	return v
}

// path is the file the current target points at for the current row.
func (v *vizPage) path() string {
	if v.row == nil {
		return ""
	}
	return filepath.Join(v.row.Graph.Out, vizTargets[v.target].file)
}

func (v *vizPage) show(r *board.Row) {
	v.row = r
	if r == nil {
		return
	}
	p := v.path()
	fi, err := os.Stat(p)
	if err != nil {
		v.loaded = ""
		v.holder.SetChild(v.fallback)
		v.fallback.SetTitle("Not generated yet")
		v.fallback.SetDescription(vizTargets[v.target].title + " has not been produced for " +
			r.Name + " yet. \"Regenerate\" runs the graphify export that makes it — it is free.")
		v.status.SetText(p)
		return
	}
	v.status.SetText(p + "  ·  " + board.Bytes(fi.Size()))
	v.status.SetTooltipText(p)

	if !webview.Available() {
		// The documented degradation: no crash, no empty pane, one button.
		v.loaded = ""
		v.holder.SetChild(v.fallback)
		v.fallback.SetTitle("WebKitGTK is not installed")
		v.fallback.SetDescription("ggraphify binds webkitgtk-6.0 at run time and it is not on this " +
			"machine, so the graph cannot be embedded. \"Open in browser\" shows the same file.")
		return
	}
	if v.view == nil {
		view, err := webview.New()
		if err != nil {
			v.holder.SetChild(v.fallback)
			v.fallback.SetTitle("Could not create the web view")
			v.fallback.SetDescription(err.Error() + "\n\n\"Open in browser\" shows the same file.")
			return
		}
		v.view = view
	}
	if v.holder.Child() != v.view.Widget() {
		v.holder.SetChild(v.view.Widget())
	}
	if v.loaded != p {
		v.view.LoadFile(p)
		v.loaded = p
	}
}

// reload re-fetches the page, for after a regenerate job lands.
func (v *vizPage) reload() {
	if v.view != nil && v.loaded != "" {
		v.view.Reload()
	}
}

func (v *vizPage) openExternal() {
	p := v.path()
	if p == "" {
		return
	}
	if err := open.Path(p); err != nil {
		v.a.toastf("open: %v", err)
	}
}
