package gfy

// The three verdict helpers both integration reports share. They are here
// rather than duplicated because the two groups are read side by side in one
// settings page: a "1 warning." that counted differently from the "1 warning."
// above it would be a bug nobody would think to look for.

// worstCheck is the worst outcome across a set of checks, which is what a
// summary row shows.
func worstCheck(checks []Check) CheckState {
	worst := CheckOK
	for _, c := range checks {
		if c.State > worst {
			worst = c.State
		}
	}
	return worst
}

// checksFixable reports whether the installer this group offers would change
// anything: some check is both wrong and something it writes.
func checksFixable(checks []Check) bool {
	for _, c := range checks {
		if c.Fixable && c.State != CheckOK {
			return true
		}
	}
	return false
}

// summarizeChecks is the one-line verdict. ok is what to say when there is
// nothing to say, which is the only part the two reports word differently.
func summarizeChecks(checks []Check, ok string) string {
	var warn, bad int
	for _, c := range checks {
		switch c.State {
		case CheckWarn:
			warn++
		case CheckBad:
			bad++
		}
	}
	switch {
	case bad == 0 && warn == 0:
		return ok
	case bad > 0 && warn > 0:
		return count(bad, "problem") + " and " + count(warn, "warning") + "."
	case bad > 0:
		return count(bad, "problem") + "."
	default:
		return count(warn, "warning") + "."
	}
}
