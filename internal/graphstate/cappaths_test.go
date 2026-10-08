package graphstate

import "testing"

// capPaths is the seam between a walk that can name thousands of drifted paths
// and a cached Graph that keeps them for a whole tick. The count is exact and
// the list is capped; the cap has to be a copy, because a reslice of the walk's
// slice would keep the whole backing array — every drifted path — alive for as
// long as the Graph is cached.
func TestCapPaths(t *testing.T) {
	for _, n := range []int{0, 1, 100, 101, 5000} {
		n := n
		t.Run(itoa(n), func(t *testing.T) {
			// Headroom on the source slice so cap is the interesting number:
			// a reslice would carry cap(xs) through, a copy cannot.
			xs := make([]string, n, n*2)
			for i := range xs {
				xs[i] = "path-" + itoa(i)
			}

			out := capPaths(xs)

			want := n
			if want > driftPathsMax {
				want = driftPathsMax
			}
			if len(out) != want {
				t.Fatalf("len = %d, want %d", len(out), want)
			}
			for i := range out {
				if out[i] != xs[i] {
					t.Fatalf("out[%d] = %q, want %q", i, out[i], xs[i])
				}
			}

			if n > driftPathsMax {
				if cap(out) >= cap(xs) {
					t.Errorf("cap(out) = %d, cap(xs) = %d — the capped list kept the source's backing array", cap(out), cap(xs))
				}
				// A separate backing array: mutating the source must not be
				// visible through the returned list.
				xs[0] = "mutated"
				if out[0] == "mutated" {
					t.Error("out aliases xs: writing xs[0] changed out[0]")
				}
			}
		})
	}
}
