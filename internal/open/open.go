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
	"strconv"
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
//
// $EDITOR and $VISUAL are command lines, not program names — "code --wait",
// "emacsclient -t", "nvim -u NONE" are all ordinary — so they are split into
// words the way a shell would split them, and executed without one: quoting
// is honoured, but nothing in the variable is ever interpreted as shell syntax.
func (c Config) InEditor(path string) error {
	ed := c.Edit()
	if ed == "" {
		return Path(path) // no editor at all: let the desktop decide
	}
	edWords, err := SplitWords(ed)
	if err != nil {
		return errors.New("open: cannot parse editor " + strconv.Quote(ed) + ": " + err.Error())
	}
	if len(edWords) == 0 {
		return Path(path)
	}
	var termWords []string
	// A terminal editor needs a terminal. The heuristic is the binary's name
	// and its arguments, because asking is not possible and guessing wrong in
	// the other direction — hosting a GUI editor inside foot — is harmless.
	if isTUI(edWords) {
		term := c.Term()
		if termWords, err = SplitWords(term); err != nil {
			return errors.New("open: cannot parse terminal " + strconv.Quote(term) + ": " + err.Error())
		}
	}
	argv, err := editorArgv(edWords, termWords, path)
	if err != nil {
		return err
	}
	return spawn(argv[0], argv[1:]...)
}

// editorArgv is the command line InEditor runs: the editor's own words and the
// path, wrapped in the terminal's words when the editor is a terminal one.
func editorArgv(edWords, termWords []string, path string) ([]string, error) {
	if len(edWords) == 0 {
		return nil, errors.New("open: empty editor command")
	}
	if !isTUI(edWords) {
		return append(append([]string(nil), edWords...), path), nil
	}
	if len(termWords) == 0 {
		return nil, errors.New("open: no terminal emulator found to run " + edWords[0] + " in")
	}
	// -e is the flag every terminal in Terminals understands for "run this",
	// except foot, which takes the command positionally but also accepts -e.
	// The editor goes in as separate words, so its own arguments reach it
	// rather than being taken as part of the program name.
	argv := append([]string(nil), termWords...)
	argv = append(argv, "-e")
	argv = append(argv, edWords...)
	return append(argv, path), nil
}

// isTUI reports whether an editor command line runs in a terminal: by the
// program's name, or — for the editors that can be either — by the flag that
// asks for the terminal (emacsclient -t, emacs -nw).
func isTUI(words []string) bool {
	if len(words) == 0 {
		return false
	}
	base := words[0]
	if i := strings.LastIndexByte(base, '/'); i >= 0 {
		base = base[i+1:]
	}
	switch base {
	case "nvim", "vim", "vi", "nano", "hx", "helix", "emacs", "kak", "micro":
		return true
	case "emacsclient":
		for _, a := range words[1:] {
			switch a {
			case "-t", "-nw", "--tty":
				return true
			}
		}
	}
	return false
}

// SplitWords splits a command line into words the way a POSIX shell does for
// a simple command — whitespace separates, single quotes are literal, double
// quotes allow \" \\ \$ and \` escapes, a backslash outside quotes escapes
// the next character — and nothing else: no expansion, no globbing, no
// operators. An unterminated quote or trailing backslash is an error.
func SplitWords(s string) ([]string, error) {
	var (
		words []string
		cur   strings.Builder
		inTok bool
	)
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		switch {
		case r == ' ' || r == '\t' || r == '\n':
			if inTok {
				words = append(words, cur.String())
				cur.Reset()
				inTok = false
			}
		case r == '\\':
			if i+1 >= len(rs) {
				return nil, errors.New("trailing backslash")
			}
			i++
			if rs[i] != '\n' { // backslash-newline is a line continuation
				cur.WriteRune(rs[i])
				inTok = true
			}
		case r == '\'':
			inTok = true
			j := i + 1
			for j < len(rs) && rs[j] != '\'' {
				j++
			}
			if j >= len(rs) {
				return nil, errors.New("unterminated single quote")
			}
			cur.WriteString(string(rs[i+1 : j]))
			i = j
		case r == '"':
			inTok = true
			j := i + 1
			for ; j < len(rs) && rs[j] != '"'; j++ {
				if rs[j] == '\\' && j+1 < len(rs) {
					switch rs[j+1] {
					case '"', '\\', '$', '`':
						j++
						cur.WriteRune(rs[j])
						continue
					case '\n':
						j++
						continue
					}
				}
				cur.WriteRune(rs[j])
			}
			if j >= len(rs) {
				return nil, errors.New("unterminated double quote")
			}
			i = j
		default:
			cur.WriteRune(r)
			inTok = true
		}
	}
	if inTok {
		words = append(words, cur.String())
	}
	return words, nil
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
