package ui

import "testing"

// The word beside the buttons. Three states rather than two: "off" and "not
// here" lead to different next actions, and the group's whole job is to say
// which of the three the machine is in.
func TestOllamaStateSplitsStoppedFromNotInstalled(t *testing.T) {
	for _, tc := range []struct {
		name             string
		reach, installed bool
		word, class      string
	}{
		{"running", true, true, "Running", "st-fresh"},
		{"running against a server elsewhere", true, false, "Running", "st-fresh"},
		{"installed but silent", false, true, "Stopped", "st-stale"},
		{"nothing on this machine", false, false, "Not installed", "st-none"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			word, class := ollamaState(tc.reach, tc.installed)
			if word != tc.word || class != tc.class {
				t.Fatalf("ollamaState(%v, %v) = %q/%q, want %q/%q",
					tc.reach, tc.installed, word, class, tc.word, tc.class)
			}
		})
	}
}

// The ownership rule, at the button: a server the board did not start is
// never stoppable from here, and the button says so rather than vanishing.
func TestOllamaStopButtonRefusesAServerWeDidNotStart(t *testing.T) {
	if vis, _, _ := ollamaStopButton(false, false); vis {
		t.Fatal("nothing is running, so there is nothing to offer to stop")
	}
	if vis, _, _ := ollamaStopButton(false, true); vis {
		t.Fatal("a server we started but that is no longer answering is not stoppable")
	}

	vis, sens, tip := ollamaStopButton(true, false)
	if !vis || sens {
		t.Fatalf("a foreign server wants a visible, insensitive Stop; got vis=%v sens=%v", vis, sens)
	}
	if tip == "" {
		t.Fatal("an insensitive button with no tooltip explains nothing")
	}

	vis, sens, tip = ollamaStopButton(true, true)
	if !vis || !sens || tip == "" {
		t.Fatalf("our own running server is stoppable; got vis=%v sens=%v tip=%q", vis, sens, tip)
	}
}
