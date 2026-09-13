package ui

import "testing"

func TestFuzzyMatch(t *testing.T) {
	const (
		name   = "nova-platform-console"
		path   = "/home/dns/git/nova-platform-console"
		branch = "tech-17302-fix"
	)
	fields := []string{name, "", path, branch}

	cases := []struct {
		term string
		want bool
		why  string
	}{
		{"nova", true, "a plain substring still matches"},
		{"console", true, "a substring anywhere in the field"},
		{"npc", true, "the initials of a dashed name are a compact subsequence"},
		{"nplat", true, "a skipped dash"},
		{"17302", true, "the branch is searched too"},
		{"go", false, "a two-letter term is a substring test, and this is not one"},
		{"dns", true, "the path is searched too"},
		{"zzz", false, "nothing like it"},
		{"nvcx", false, "a subsequence that is not there"},
		{"nge", false, "letters in order but sprawled across the whole string"},
	}
	for _, c := range cases {
		if got := fuzzyMatch(c.term, fields...); got != c.want {
			t.Errorf("fuzzyMatch(%q) = %v, want %v — %s", c.term, got, c.want, c.why)
		}
	}
}

// A term may not be satisfied by halves of two different fields: that is what
// makes the per-field call in matches() worth the extra argument.
func TestFuzzyMatchDoesNotSpanFields(t *testing.T) {
	if fuzzyMatch("abcxyz", "abc", "xyz") {
		t.Error("a term matched across two fields")
	}
	if !fuzzyMatch("abc", "abc", "xyz") {
		t.Error("a term that fits one field was rejected")
	}
}

// Short terms are prefixes, not skeletons.
func TestShortTermsAreSubstrings(t *testing.T) {
	if fuzzyMatch("ab", "a-big-thing") {
		t.Error("a two-letter term matched as a subsequence")
	}
	if !fuzzyMatch("ab", "a-big-thing", "lab-notes") {
		t.Error("a two-letter substring was rejected")
	}
	if !fuzzyMatch("", "anything") {
		t.Error("an empty term should match")
	}
}
