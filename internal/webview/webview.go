// Package webview binds WebKitGTK 6.0 at run time through dlopen, not at link
// time through pkg-config.
//
// The reason is a hard requirement of this application: a machine without
// webkitgtk-6.0 must degrade to "the Visualize tab opens graph.html in
// xdg-open instead", never to "the binary will not start". Linking the library
// would make its absence a load-time failure of the whole board, which is a
// steep price for one optional page.
//
// Only gtk4 is a hard link-time dependency. Everything in here is best-effort,
// and Available() is the single question the UI asks before using any of it.
package webview

/*
#cgo pkg-config: gtk4
#include <dlfcn.h>
#include <stdlib.h>
#include <gtk/gtk.h>

// The three symbols the Visualize page needs, resolved at run time.
typedef GtkWidget *(*webkit_web_view_new_fn)(void);
typedef void (*webkit_web_view_load_uri_fn)(GtkWidget *, const char *);
typedef void (*webkit_web_view_reload_fn)(GtkWidget *);
typedef const char *(*webkit_get_version_fn)(void);

static void *wk_handle = NULL;
static webkit_web_view_new_fn wk_new = NULL;
static webkit_web_view_load_uri_fn wk_load = NULL;
static webkit_web_view_reload_fn wk_reload = NULL;

// ggraphify_webkit_open dlopens the library and resolves the symbols. It is
// idempotent and returns 1 on success.
//
// The soname is tried in the order a GTK4 application should want them:
// the GTK4 API version (6.0) first, then the older GTK4-compatible 5.0 build
// that some distributions still ship. A GTK3 webkit (4.x) is deliberately NOT
// tried: loading it would pull a second GTK into the process and crash.
static int ggraphify_webkit_open(void) {
	if (wk_handle) return 1;
	const char *names[] = {
		"libwebkitgtk-6.0.so.4",
		"libwebkitgtk-6.0.so",
		"libwebkit2gtk-5.0.so.0",
		NULL
	};
	for (int i = 0; names[i]; i++) {
		wk_handle = dlopen(names[i], RTLD_LAZY | RTLD_LOCAL);
		if (wk_handle) break;
	}
	if (!wk_handle) return 0;
	wk_new = (webkit_web_view_new_fn)dlsym(wk_handle, "webkit_web_view_new");
	wk_load = (webkit_web_view_load_uri_fn)dlsym(wk_handle, "webkit_web_view_load_uri");
	wk_reload = (webkit_web_view_reload_fn)dlsym(wk_handle, "webkit_web_view_reload");
	if (!wk_new || !wk_load) {
		dlclose(wk_handle);
		wk_handle = NULL;
		return 0;
	}
	return 1;
}

static GtkWidget *ggraphify_webkit_new(void) {
	if (!ggraphify_webkit_open()) return NULL;
	return wk_new();
}

static void ggraphify_webkit_load(GtkWidget *w, const char *uri) {
	if (wk_load && w) wk_load(w, uri);
}

static void ggraphify_webkit_reload(GtkWidget *w) {
	if (wk_reload && w) wk_reload(w);
}
*/
import "C"

import (
	"errors"
	"net/url"
	"path/filepath"
	"sync"
	"unsafe"

	coreglib "github.com/diamondburned/gotk4/pkg/core/glib"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
)

var (
	probeOnce sync.Once
	available bool
)

// Available reports whether WebKitGTK could be loaded. The UI asks once and
// builds either the embedded view or the "Open in browser" fallback.
func Available() bool {
	probeOnce.Do(func() { available = C.ggraphify_webkit_open() == 1 })
	return available
}

// View is an embedded WebKitWebView, wrapped as an ordinary GTK widget.
//
// The board keeps exactly one of these and reuses it across repositories: a
// 1.9 MB graph.html running a force-directed D3 layout is not a cheap object,
// and one per row would be a memory leak with a user interface on it.
type View struct {
	widget gtk.Widgetter
	// native is the WebKitWebView's own address, kept from the constructor.
	//
	// It is deliberately NOT re-derived from widget at each call. gotk4 gives
	// *gtk.Widget a Native() method that is GTK's gtk_widget_get_native() —
	// the GtkNative the widget is hosted in, returned as a Go wrapper struct —
	// while the GObject address lives on the embedded coreglib.Object. Both
	// spellings compile, and picking the wrong one hands C a Go pointer: it
	// panics under cgocheck and would be a wild pointer without it. Holding
	// the address the C constructor returned removes the choice.
	native *C.GtkWidget
}

// New creates a web view, or returns an error when the library is absent.
func New() (*View, error) {
	if !Available() {
		return nil, errors.New("webkitgtk-6.0 is not installed")
	}
	w := C.ggraphify_webkit_new()
	if w == nil {
		return nil, errors.New("webkit_web_view_new returned NULL")
	}
	// Take the GObject into gotk4's world. The widget is floating on return
	// from the constructor, which is exactly what gtk.Widgetter expects of a
	// freshly built widget.
	obj := coreglib.Take(unsafe.Pointer(w))
	widget, ok := obj.Cast().(gtk.Widgetter)
	if !ok {
		return nil, errors.New("webkit web view is not a GtkWidget")
	}
	return &View{widget: widget, native: w}, nil
}

// Widget is the view as a GTK widget, for packing into a container.
func (v *View) Widget() gtk.Widgetter { return v.widget }

// LoadFile points the view at a local file.
func (v *View) LoadFile(path string) {
	if v == nil {
		return
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	v.LoadURI((&url.URL{Scheme: "file", Path: abs}).String())
}

// LoadURI points the view at a URI.
func (v *View) LoadURI(uri string) {
	if v == nil || v.native == nil {
		return
	}
	c := C.CString(uri)
	defer C.free(unsafe.Pointer(c))
	C.ggraphify_webkit_load(v.native, c)
}

// Reload re-fetches the current page, which is what the Visualize page does
// after a regenerate job finishes.
func (v *View) Reload() {
	if v == nil || v.native == nil {
		return
	}
	C.ggraphify_webkit_reload(v.native)
}
