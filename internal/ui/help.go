package ui

import (
	"github.com/diamondburned/gotk4-adwaita/pkg/adw"
)

// showHelp presents the shortcuts dialog, built from the same table the key
// handler dispatches from. There is no second list to keep in step.
func (a *App) showHelp() {
	if a.helpDlg != nil {
		a.helpDlg.Present(a.win)
		return
	}
	dlg := adw.NewShortcutsDialog()

	sections := map[string]*adw.ShortcutsSection{}
	var order []string
	for _, k := range keyBindings {
		sec, ok := sections[k.Group]
		if !ok {
			sec = adw.NewShortcutsSection(k.Group)
			sections[k.Group] = sec
			order = append(order, k.Group)
		}
		sec.Add(adw.NewShortcutsItem(k.What, accelFor(k.Keys)))
	}
	for _, g := range order {
		dlg.Add(sections[g])
	}

	a.helpDlg = dlg
	dlg.Present(a.win)
}

// accelFor turns the table's display string into the accelerator syntax
// AdwShortcutsItem parses. The table is written for a human reading the README;
// this is the one place that has to speak GTK's dialect.
func accelFor(keys string) string {
	switch keys {
	case "j / ↓":
		return "j Down"
	case "k / ↑":
		return "k Up"
	case "g / G":
		return "g G"
	case "Enter":
		return "Return"
	case "/":
		return "slash"
	case "Esc":
		return "Escape"
	case "?":
		return "question"
	case ",":
		return "comma"
	case "Space":
		return "space"
	case "Ctrl+F / Ctrl+B":
		return "<Control>f <Control>b"
	case "Ctrl+Q":
		return "<Control>q"
	}
	return keys
}
