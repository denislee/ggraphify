package usage

import (
	"strings"
	"testing"
	"time"
)

// The report is an artefact a person pastes into an agent, so what is tested
// is what that agent must be able to read out of it: the two ratios, the
// directories that were told and never used, and the disclaimer that keeps it
// from being read as a count of all sessions.

func reportFixture(t *testing.T) (Summary, time.Time) {
	t.Helper()
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.Local)
	x := New()
	day := DayKey(now)

	// A repository that is offered graft constantly and uses it twice.
	rd := x.repoDay(day, "/home/u/git/busy")
	rd.Counts[counterKey(Graft, Hook, "session-start")] = 40
	rd.Counts[counterKey(Graft, CLI, "ask")] = 2
	rd.Counts[counterKey(Graphify, MCP, "query")] = 3
	rd.Sessions = []string{"s1", "s2"}
	rd.GraftReads = 2
	rd.SourceReads = 18

	// A repository whose hooks fire and where neither tool is ever run.
	silent := x.repoDay(day, "/home/u/git/ignored")
	silent.Counts[counterKey(Graft, Hook, "session-start")] = 9

	return x.Summarize(Window{Days: 7, Now: now}), now
}

func TestReportCarriesTheOpportunityRatios(t *testing.T) {
	s, now := reportFixture(t)
	out := Report(s, nil, ReportOptions{Now: now, Version: "test"})

	for _, want := range []string{
		"# graphify + graft usage — every repository on this machine",
		"injected its pointer **49** times",
		"invoked **2** times (4% of them)",
		"graft read **10%** of the time",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("report is missing %q\n---\n%s", want, out)
		}
	}
}

func TestReportNamesDirectoriesToldAndNeverUsed(t *testing.T) {
	s, now := reportFixture(t)
	out := Report(s, nil, ReportOptions{Now: now})

	if !strings.Contains(out, "Told, and never used") {
		t.Fatalf("no told-and-never-used section:\n%s", out)
	}
	if !strings.Contains(out, "git/ignored (9 injections, 0 uses)") {
		t.Errorf("the silent repository is not named:\n%s", out)
	}
	// The busy one did use the tools, so it must not be listed as silent.
	i := strings.Index(out, "Told, and never used")
	if strings.Contains(out[i:], "git/busy") {
		t.Errorf("a repository with uses was listed as silent:\n%s", out[i:])
	}
}

// The session count covers hook-only sessions, which makes it a denominator
// inside a wired repository and no denominator at all outside one. A reader
// who misses that distinction draws the opposite conclusion from the same
// numbers, so the disclaimer is load-bearing, not decoration.
func TestReportRefusesToPoseAsADenominator(t *testing.T) {
	s, now := reportFixture(t)
	out := Report(s, nil, ReportOptions{Now: now})
	if !strings.Contains(out, "not** a count of all Claude Code sessions") {
		t.Errorf("the report does not say what it cannot see:\n%s", out)
	}
}

// An empty window must still render — the button is always clickable.
func TestReportOfAQuietMachine(t *testing.T) {
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.Local)
	s := New().Summarize(Window{Days: 3, Now: now})
	out := Report(s, nil, ReportOptions{Now: now})
	if !strings.Contains(out, "0 uses") {
		t.Errorf("missing the zero headline:\n%s", out)
	}
	if !strings.Contains(out, "3 of 3 days saw neither tool run") {
		t.Errorf("missing the quiet-days line:\n%s", out)
	}
}

// A working directory that is not a boarded checkout — a scratchpad, an agent
// worktree, the home directory — can have a hook fire in it, and naming it as
// a repository that ignored its index is advice nobody can act on.
func TestReportKeepsUnboardedDirectoriesOutOfTheSilentList(t *testing.T) {
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.Local)
	x := New()
	day := DayKey(now)
	x.repoDay(day, "/home/u/git/real").Counts[counterKey(Graft, Hook, "context")] = 5
	x.repoDay(day, "/home/u/.cache/scratchpad/wt").Counts[counterKey(Graft, Hook, "context")] = 4
	s := x.Summarize(Window{Days: 3, Now: now})

	out := Report(s, nil, ReportOptions{Now: now, Repos: []string{"/home/u/git/real"}})
	if !strings.Contains(out, "git/real (5 injections") {
		t.Errorf("the boarded checkout is missing:\n%s", out)
	}
	if strings.Contains(out, "scratchpad") {
		t.Errorf("an unboarded directory was named as a repository:\n%s", out)
	}

	// With no board to filter against, the raw answer is better than none.
	raw := Report(s, nil, ReportOptions{Now: now})
	if !strings.Contains(raw, "scratchpad") {
		t.Errorf("without a row list every directory should still be listed:\n%s", raw)
	}
}

// The sessions section has to carry the denominator in prose as well as in the
// table, because the table is truncated on a busy machine and the ratio is the
// part a reader acts on.
func TestReportSessionsCarryTheDenominator(t *testing.T) {
	s, now := reportFixture(t)
	rolls := []SessionRoll{{
		Session: "s1", Account: "default",
		Repo: "/home/u/git/busy", Branch: "main",
		Start: now.Add(-time.Hour), Last: now,
		Tools:  40,
		Counts: map[string]int{counterKey(Graft, CLI, "ask"): 4},
	}, {
		// The session that used nothing, with plenty of work in it. This row is
		// the reason the section exists.
		Session: "s2", Account: "default",
		Repo: "/home/u/git/ignored", Branch: "main",
		Start: now.Add(-2 * time.Hour), Last: now.Add(-time.Hour),
		Tools:  67,
		Counts: map[string]int{},
	}}
	out := Report(s, nil, ReportOptions{
		Now: now, Sessions: rolls, SessionsTotal: 293, SessionsUsed: 104,
	})

	for _, want := range []string{
		"## Every session",
		"293 Claude Code sessions ran in this window; **104 of them reached for either tool**, and 189 did not.",
		// s1: four graft calls out of forty tool calls. The path is not
		// shortened because the fixture's home is not this machine's.
		"| /home/u/git/busy | main | 0 | 4 | 40 | 10% |",
		// s2 is present with a zero share rather than filtered out.
		"| /home/u/git/ignored | main | 0 | 0 | 67 | 0% |",
		"(2 most recent of 293.)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("sessions section is missing %q\n---\n%s", want, out)
		}
	}
}

// With no session data passed in, the section is absent rather than empty: the
// report is also rendered by callers that never collected it.
func TestReportOmitsSessionsWhenNotProvided(t *testing.T) {
	s, now := reportFixture(t)
	if out := Report(s, nil, ReportOptions{Now: now}); strings.Contains(out, "## Every session") {
		t.Errorf("sessions section rendered with nothing to put in it:\n%s", out)
	}
}
