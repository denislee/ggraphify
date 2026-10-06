package ui

import (
	"testing"

	"github.com/dns/ggraphify/internal/board"
)

// A scratch root that is a checkout of its own holds the other rows, and the
// loop must leave it alone — its rebuild is every nested repository again.
// A sibling that merely shares a name prefix ("tmp-x" beside "tmp") is not
// nested, and neither is a leaf.
func TestContainers(t *testing.T) {
	rows := []board.Row{
		{Path: "/h/tmp/b"},
		{Path: "/h/tmp-x"},
		{Path: "/h/tmp"},
		{Path: "/h/tmp/a"},
		{Path: "/h/git/docs"},
		{Path: "/h/git/nexus"},
	}
	got := containers(rows)
	if len(got) != 1 || !got["/h/tmp"] {
		t.Fatalf("containers = %v, want only /h/tmp", got)
	}
}
