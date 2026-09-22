package heal

import (
	"testing"

	"github.com/dns/ggraphify/internal/gfy"
	"github.com/dns/ggraphify/internal/globalgraph"
)

// The one defect a membership can have, and the one free command that answers
// it. Everything else about a membership is a choice, not a defect.
func TestGlobalStepCoversStaleMembersOnly(t *testing.T) {
	cases := []struct {
		name string
		m    globalgraph.Member
		want bool
	}{
		{"stale member", globalgraph.Member{In: true, Tag: "a", Stale: true}, true},
		{"fresh member", globalgraph.Member{In: true, Tag: "a"}, false},
		{"not a member", globalgraph.Member{}, false},
		// Stale without In cannot happen — MemberOf only sets Stale on a hit —
		// but a zero-value struct assembled by hand must not produce a merge.
		{"stale but not a member", globalgraph.Member{Stale: true}, false},
	}
	for _, c := range cases {
		s, got := GlobalStep(c.m)
		if got != c.want {
			t.Errorf("%s: GlobalStep = %v, want %v", c.name, got, c.want)
		}
		if !got {
			continue
		}
		if s.Kind != "global-add" {
			t.Errorf("%s: kind = %q, want global-add", c.name, s.Kind)
		}
		if s.Cost() != gfy.Free {
			t.Errorf("%s: a re-merge must be free, got %v", c.name, s.Cost())
		}
	}
}
