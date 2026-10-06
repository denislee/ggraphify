package autofix

import (
	"sort"
	"testing"
)

func picked(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for p, ok := range m {
		if ok {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Only one checkout of an origin may enroll: the canonical one is the path
// named after the repository, or the shortest when none is.
func TestCanonicalByOrigin(t *testing.T) {
	cases := []struct {
		name    string
		paths   []string
		origins []string
		want    []string
	}{
		{
			name: "full clones of pulumi collapse to the canonical checkout",
			paths: []string{
				"/home/dns/git/pulumi",
				"/home/dns/git/pulumi-wt-a",
				"/home/dns/git/pulumi-wt-b",
			},
			origins: []string{
				"github.com/pulumi/pulumi",
				"github.com/pulumi/pulumi",
				"github.com/pulumi/pulumi",
			},
			want: []string{"/home/dns/git/pulumi"},
		},
		{
			name:    "a lone checkout is picked even when its name differs",
			paths:   []string{"/home/dns/git/open-finance-payments-wt-19867"},
			origins: []string{"github.com/nova-pay/open-finance-payments"},
			want:    []string{"/home/dns/git/open-finance-payments-wt-19867"},
		},
		{
			name:    "two clones neither matching the name pick the shortest base",
			paths:   []string{"/a/foobar", "/a/foo"},
			origins: []string{"github.com/x/some-repo", "github.com/x/some-repo"},
			want:    []string{"/a/foo"},
		},
		{
			name:    "an empty origin is its own group",
			paths:   []string{"/a/one", "/a/two"},
			origins: []string{"", ""},
			want:    []string{"/a/one", "/a/two"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := picked(CanonicalByOrigin(c.paths, c.origins))
			if !equalStrings(got, c.want) {
				t.Fatalf("CanonicalByOrigin = %v, want %v", got, c.want)
			}
		})
	}
}
