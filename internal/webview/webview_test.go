package webview

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// A nil or unconstructed View must be inert. The UI holds one lazily and calls
// into it from the page's show path, so "WebKitGTK is not installed" has to be
// a quiet no-op rather than a nil dereference.
func TestNilViewIsInert(t *testing.T) {
	var v *View
	v.LoadURI("file:///tmp/x.html")
	v.LoadFile("/tmp/x.html")
	v.Reload()

	zero := &View{}
	zero.LoadURI("file:///tmp/x.html")
	zero.Reload()
}

// Regression guard for a crash that shipped once.
//
// gotk4 gives *gtk.Widget a Native() method that is GTK's
// gtk_widget_get_native() — it returns a *gtk.NativeSurface wrapper, a Go
// pointer — while the GObject address lives on the embedded coreglib.Object.
// Both spellings compile. Using the widget one handed a Go pointer to C and
// panicked with "argument of cgo function has Go pointer to unpinned Go
// pointer" the first time anyone opened the Visualize page.
//
// The View now holds the address its C constructor returned, so there is no
// choice to get wrong. This test fails if anyone reintroduces the derivation.
func TestNoWidgetNativeDerivation(t *testing.T) {
	src := source(t)
	for _, bad := range []string{"BaseWidget(", ".Native()"} {
		if strings.Contains(src, bad) {
			t.Errorf("internal/webview must not use %s to find the web view's address — "+
				"gtk.Widget.Native() is gtk_widget_get_native(), not the GObject pointer. "+
				"Use the *C.GtkWidget kept from the constructor.", bad)
		}
	}
}

// Available must answer without a display and without panicking, because the
// UI asks it on every visit to the Visualize page to decide whether to embed
// or to offer the browser fallback.
func TestAvailableIsSafeToCall(t *testing.T) {
	if Available() != Available() {
		t.Fatal("Available() is not stable")
	}
}

func source(t *testing.T) string {
	t.Helper()
	_, self, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate the package source")
	}
	b, err := os.ReadFile(filepath.Join(filepath.Dir(self), "webview.go"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
