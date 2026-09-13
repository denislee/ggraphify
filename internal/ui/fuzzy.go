package ui

import "strings"

// Fuzzy matching for the filter box.
//
// A term matches when its characters appear in the haystack in order, not
// necessarily adjacent: `npc` finds nova-platform-console and `gfy` finds
// ggraphify. A plain substring is a subsequence too, so everything that
// matched before still matches — the box got looser, never stricter.
//
// Two guards keep "looser" from becoming "useless" on a board with a few
// hundred rows, where a three-letter subsequence otherwise matches nearly
// every path:
//
//   - a term of one or two characters must appear as a substring. Short
//     terms are typed as prefixes ("go", "cc"), not as skeletons, and a
//     two-letter subsequence matches almost anything.
//   - a subsequence may not sprawl: the matched characters have to fall
//     inside a window proportional to the term, so `npc` matches
//     nova-platform-console but not a path that happens to contain an n, a p
//     and a c thirty characters apart.
//
// Both are measured per haystack *field* rather than over one joined string,
// so a term cannot match half in the name and half in the branch.

// fuzzyMinLen is the shortest term that may match as a subsequence rather
// than as a substring.
const fuzzyMinLen = 3

// fuzzySpread is how far a subsequence match may spread: the term's length
// times this, plus a small constant, bounds the window it must fit in.
const fuzzySpread = 6

// fuzzyMatch reports whether term matches any of the fields. Both sides are
// expected to be lowercased already.
func fuzzyMatch(term string, fields ...string) bool {
	if term == "" {
		return true
	}
	for _, f := range fields {
		if strings.Contains(f, term) {
			return true
		}
	}
	if len([]rune(term)) < fuzzyMinLen {
		return false
	}
	for _, f := range fields {
		if subsequenceWithin(f, term) {
			return true
		}
	}
	return false
}

// subsequenceWithin reports whether term is a subsequence of hay that fits
// inside the allowed window. It restarts the search at each position where
// the first character occurs, because the greedy leftmost match is not always
// the tight one: in "nova-platform-console" the first `c` is far from the
// second `n`, and a person typing `npc` means the compact match.
func subsequenceWithin(hay, term string) bool {
	h, t := []rune(hay), []rune(term)
	if len(t) == 0 || len(h) < len(t) {
		return false
	}
	window := len(t)*fuzzySpread + fuzzySpread
	for start := 0; start+len(t) <= len(h); start++ {
		if h[start] != t[0] {
			continue
		}
		i := start
		k := 0
		for ; i < len(h) && k < len(t); i++ {
			if h[i] == t[k] {
				k++
			}
		}
		if k == len(t) && i-start <= window {
			return true
		}
	}
	return false
}
