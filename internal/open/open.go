// Package open launches things outside the board: a URL or file in the
// desktop's handler, a path in the user's editor, a directory in the file
// manager.
//
// Nothing here blocks: every launch is a detached subprocess whose output goes
// nowhere, because a GUI that waited for a browser to exit would be frozen for
// as long as the browser lived.
package open

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"syscall"
)

// Config is the resolved external-tool preference.
type Config struct {
	Terminal string // terminal emulator to host a TUI editor in
	Editor   string // editor binary
}

// Terminals are tried in order when none is configured. foot is the Wayland
// native one and is first for that reason.
var Terminals = []string{"foot", "alacritty", "kitty", "wezterm", "ghostty", "xterm"}

// Editors are tried in order when neither $VISUAL nor $EDITOR is set.
var Editors = []string{"nvim", "vim", "hx", "nano"}

// Terminal resolves the terminal emulator: the configured one, then
// $TERMINAL, then the first of Terminals present on PATH.
func (c Config) Term() string {
	for _, cand := range []string{c.Terminal, os.Getenv("TERMINAL")} {
		if cand = strings.TrimSpace(cand); cand != "" {
			return cand
		}
	}
	for _, cand := range Terminals {
		if _, err := exec.LookPath(cand); err == nil {
			return cand
		}
	}
	return ""
}

// Edit resolves the editor: the configured one, then $VISUAL, then $EDITOR,
// then the first of Editors present on PATH.
func (c Config) Edit() string {
	for _, cand := range []string{c.Editor, os.Getenv("VISUAL"), os.Getenv("EDITOR")} {
		if cand = strings.TrimSpace(cand); cand != "" {
			return cand
		}
	}
	for _, cand := range Editors {
		if _, err := exec.LookPath(cand); err == nil {
			return cand
		}
	}
	return ""
}

// Path hands a file or directory to the desktop's handler. This is the
// fallback the Visualize page uses when WebKitGTK is not available, so it has
// to work on a machine with nothing but xdg-open.
func Path(p string) error {
	if p == "" {
		return errors.New("open: empty path")
	}
	if _, err := os.Stat(p); err != nil {
		return err
	}
	return spawn("xdg-open", p)
}

// URL is Path for something that is not on disk.
func URL(u string) error {
	if u == "" {
		return errors.New("open: empty url")
	}
	return spawn("xdg-open", u)
}

// InEditor opens a path in the user's editor, in a new terminal window when the
// editor is a terminal one. A GUI editor named in $VISUAL is launched directly.
func (c Config) InEditor(path string) error {
	ed := c.Edit()
	if ed == "" {
		return Path(path) // no editor at all: let the desktop decide
	}
	// A terminal editor needs a terminal. The heuristic is the binary's name,
	// because asking is not possible and guessing wrong in the other direction
	// — hosting a GUI editor inside foot — is harmless.
	if !isTUI(ed) {
		return spawn(ed, path)
	}
	term := c.Term()
	if term == "" {
		return errors.New("open: no terminal emulator found to run " + ed + " in")
	}
	// -e is the flag every terminal in Terminals understands for "run this",
	// except foot, which takes the command positionally but also accepts -e.
	return spawn(term, "-e", ed, path)
}

func isTUI(ed string) bool {
	base := ed
	if i := strings.LastIndexByte(base, '/'); i >= 0 {
		base = base[i+1:]
	}
	base, _, _ = strings.Cut(base, " ")
	switch base {
	case "nvim", "vim", "vi", "nano", "hx", "helix", "emacs", "kak", "micro":
		return true
	}
	return false
}

// Manager opens a directory in the file manager.
func Manager(dir string) error { return Path(dir) }

// spawn starts a detached process and does not wait for it.
//
// Setsid matters: without it the child stays in the board's process group and
// a Ctrl-C in the shell the board was launched from would take the user's
// browser down with it.
func spawn(name string, args ...string) error {
	bin, err := exec.LookPath(name)
	if err != nil {
		return err
	}
	cmd := exec.Command(bin, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	if err := cmd.Start(); err != nil {
		return err
	}
	// Reap it so the board does not accumulate zombies over a long session.
	go func() { _ = cmd.Wait() }()
	return nil
}
