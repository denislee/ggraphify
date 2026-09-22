package ui

import "testing"

// The policy, stated as a table: a queue past the threshold is asked about,
// and running jobs never make the difference on their own.
func TestCancelAllConfirmPolicy(t *testing.T) {
	cases := []struct {
		name            string
		queued, running int
		want            bool
	}{
		{"nothing at all", 0, 0, false},
		{"only running, however many", 0, 64, false},
		{"a queue somebody can retype", cancelAllConfirmAt, 0, false},
		{"one past the threshold", cancelAllConfirmAt + 1, 0, true},
		{"the sweep that started this", 137, 1, true},
		{"a big queue is asked about even with nothing running", 137, 0, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := cancelAllNeedsConfirm(c.queued, c.running); got != c.want {
				t.Fatalf("cancelAllNeedsConfirm(%d, %d) = %v, want %v",
					c.queued, c.running, got, c.want)
			}
		})
	}
}
