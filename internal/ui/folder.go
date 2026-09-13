package ui

import (
	"context"
	"os"

	"github.com/diamondburned/gotk4/pkg/gio/v2"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
)

// chooseFolder presents the portal's folder picker and calls done with the
// chosen absolute path, on the main thread.
//
// Every path ggraphify can be pointed at — a scan root, the directory graphs
// are kept in — is a directory the user has to be able to pick rather than
// spell. Cancelling is not an error: GTK reports a dismissed dialog as one,
// and there is nothing to say about it.
func (a *App) chooseFolder(title, start string, done func(path string)) {
	dlg := gtk.NewFileDialog()
	dlg.SetTitle(title)
	dlg.SetModal(true)
	if start != "" {
		if fi, err := os.Stat(start); err == nil && fi.IsDir() {
			dlg.SetInitialFolder(gio.NewFileForPath(start))
		}
	}
	var parent *gtk.Window
	if a.win != nil {
		parent = &a.win.Window
	}
	dlg.SelectFolder(context.Background(), parent, func(res gio.AsyncResulter) {
		f, err := dlg.SelectFolderFinish(res)
		if err != nil || f == nil {
			return
		}
		if p := f.Path(); p != "" {
			done(p)
		}
	})
}
