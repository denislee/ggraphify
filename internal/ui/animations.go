package ui

import (
	"sync/atomic"

	"github.com/diamondburned/gotk4/pkg/gtk/v4"
)

// animationsOn mirrors store.Settings.Animations for the code that paints
// without an *App in hand — setJobProgress, called from every tick.
var animationsOn atomic.Bool

// applyAnimations pushes the Animations preference into GtkSettings.
// gtk-enable-animations is the one switch GTK and libadwaita both honour:
// stack transitions, CSS animations (the spinners), popovers and toasts all
// stop moving when it is false. It applies live.
func (a *App) applyAnimations() {
	on := a.opts.Store.Settings().Animations
	animationsOn.Store(on)
	if s := gtk.SettingsGetDefault(); s != nil {
		s.SetObjectProperty("gtk-enable-animations", on)
	}
}
