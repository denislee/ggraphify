package open

import (
	"reflect"
	"testing"
)

func TestSplitWords(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"nvim", []string{"nvim"}},
		{"  code   --wait ", []string{"code", "--wait"}},
		{"nvim -u NONE", []string{"nvim", "-u", "NONE"}},
		{`'/opt/My Editor/bin/ed' --flag`, []string{"/opt/My Editor/bin/ed", "--flag"}},
		{`"/opt/My Editor/ed" -a "b \"c\""`, []string{"/opt/My Editor/ed", "-a", `b "c"`}},
		{`ed a\ b`, []string{"ed", "a b"}},
		{`ed ''`, []string{"ed", ""}},
		// No shell: operators and expansions are plain text in a word.
		{`vim; rm -rf ~ $(id) | x`, []string{"vim;", "rm", "-rf", "~", "$(id)", "|", "x"}},
		{"", nil},
	}
	for _, c := range cases {
		got, err := SplitWords(c.in)
		if err != nil {
			t.Fatalf("SplitWords(%q): %v", c.in, err)
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("SplitWords(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSplitWordsRejectsUnterminated(t *testing.T) {
	for _, in := range []string{`ed 'a`, `ed "a`, `ed \`} {
		if _, err := SplitWords(in); err == nil {
			t.Errorf("SplitWords(%q): want an error", in)
		}
	}
}

func TestIsTUIHonoursArguments(t *testing.T) {
	cases := []struct {
		in   []string
		want bool
	}{
		{[]string{"nvim", "-u", "NONE"}, true},
		{[]string{"/usr/bin/vim"}, true},
		{[]string{"emacsclient", "-t"}, true},
		{[]string{"emacsclient", "-nw"}, true},
		{[]string{"emacsclient", "--tty"}, true},
		{[]string{"emacsclient", "-c"}, false},
		{[]string{"code", "--wait"}, false},
		{nil, false},
	}
	for _, c := range cases {
		if got := isTUI(c.in); got != c.want {
			t.Errorf("isTUI(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

// The regression: EDITOR="code --wait" used to be handed to LookPath whole,
// and "nvim -u NONE" to the terminal as one program name.
func TestEditorArgvSplitsTheEditorsArguments(t *testing.T) {
	cases := []struct {
		editor, term string
		want         []string
	}{
		{"code --wait", "foot", []string{"code", "--wait", "/f"}},
		{"nvim -u NONE", "foot", []string{"foot", "-e", "nvim", "-u", "NONE", "/f"}},
		{"emacsclient -t", "kitty --single-instance", []string{"kitty", "--single-instance", "-e", "emacsclient", "-t", "/f"}},
		{"emacsclient -c", "foot", []string{"emacsclient", "-c", "/f"}},
	}
	for _, c := range cases {
		ed, err := SplitWords(c.editor)
		if err != nil {
			t.Fatal(err)
		}
		term, err := SplitWords(c.term)
		if err != nil {
			t.Fatal(err)
		}
		got, err := editorArgv(ed, term, "/f")
		if err != nil {
			t.Fatalf("editorArgv(%q): %v", c.editor, err)
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("editorArgv(%q, %q) = %q, want %q", c.editor, c.term, got, c.want)
		}
	}
}

func TestEditorArgvNeedsATerminalForATUI(t *testing.T) {
	if _, err := editorArgv([]string{"nvim"}, nil, "/f"); err == nil {
		t.Fatal("a terminal editor with no terminal must be an error")
	}
}
