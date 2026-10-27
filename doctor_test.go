package main

import (
	"os"
	"testing"
)

// W28: `ggraphify doctor -h` printed its usage and then exited 2, as if the
// help request were a mistake.
func TestDoctorHelpExitsZero(t *testing.T) {
	saved := os.Stderr
	devnull, err := os.Open(os.DevNull)
	if err == nil {
		os.Stderr = devnull
		defer func() { os.Stderr = saved; devnull.Close() }()
	}
	for _, arg := range []string{"-h", "-help", "--help"} {
		if code := doctorCmd([]string{arg}); code != 0 {
			t.Errorf("doctor %s exit code = %d, want 0", arg, code)
		}
	}
	if code := doctorCmd([]string{"-no-such-flag"}); code != 2 {
		t.Errorf("doctor -no-such-flag exit code = %d, want 2", code)
	}
}
