package usage

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// otherToolRec is a tool call that has nothing to do with either index. It is
// what makes the denominator meaningful, and the byte scan must count it
// without the decoder ever seeing the line.
func otherToolRec(repo, session string, at time.Time, name string) map[string]any {
	return map[string]any{
		"type": "assistant", "timestamp": at.Format(time.RFC3339Nano),
		"cwd": repo, "gitBranch": "main", "sessionId": session,
		"message": map[string]any{"content": []any{
			map[string]any{"type": "tool_use", "name": name,
				"input": map[string]any{"command": "true"}},
		}},
	}
}

func TestSessionRollsCountPerSession(t *testing.T) {
	dir := t.TempDir()
	acct := filepath.Join(dir, ".claude")
	repo := filepath.Join(dir, "repo")
	now := time.Now()

	writeTranscript(t, acct, "sess-a",
		bashRec(repo, "sess-a", now.Add(-3*time.Hour), `graphify query "how does X work"`),
		bashRec(repo, "sess-a", now.Add(-2*time.Hour), `graft ask "y" --source`),
		hookRec(repo, "sess-a", now.Add(-2*time.Hour), "[graft] SessionStart: this repo is indexed"),
		otherToolRec(repo, "sess-a", now.Add(-time.Hour), "Read"),
		otherToolRec(repo, "sess-a", now.Add(-time.Hour), "Edit"),
	)
	writeTranscript(t, acct, "sess-b",
		bashRec(repo, "sess-b", now.Add(-time.Hour), `graft grep "Foo"`),
		bashRec(repo, "sess-b", now, `graft grep "Bar"`),
	)

	x := New()
	opts := Options{Accounts: []Account{{Name: "default", Dir: acct}}, Now: now}
	if err := x.Update(context.Background(), opts); err != nil {
		t.Fatal(err)
	}

	rolls := x.SessionRolls(Window{Days: 7, Now: now}, 0)
	if len(rolls) != 2 {
		t.Fatalf("rolls = %d, want 2: %+v", len(rolls), rolls)
	}
	// Newest activity first: sess-b's last call is now, sess-a's an hour ago.
	if rolls[0].Session != "sess-b" || rolls[1].Session != "sess-a" {
		t.Fatalf("order = %s, %s; want sess-b then sess-a", rolls[0].Session, rolls[1].Session)
	}

	by := map[string]SessionRoll{rolls[0].Session: rolls[0], rolls[1].Session: rolls[1]}

	a := by["sess-a"]
	if got := a.ByTool(Graphify); got != 1 {
		t.Fatalf("sess-a graphify = %d, want 1", got)
	}
	if got := a.ByTool(Graft); got != 1 {
		t.Fatalf("sess-a graft = %d, want 1", got)
	}
	// The hook fired in sess-a and belongs apart from the uses, exactly as the
	// window summary keeps them apart.
	if got := a.HooksByTool(Graft); got != 1 {
		t.Fatalf("sess-a graft hooks = %d, want 1", got)
	}
	if got := a.Uses(); got != 2 {
		t.Fatalf("sess-a uses = %d, want 2 (hooks excluded)", got)
	}
	if a.Repo != repo || a.Branch != "main" || a.Account != "default" {
		t.Fatalf("sess-a metadata = %q %q %q", a.Repo, a.Branch, a.Account)
	}
	if !a.Start.Equal(a.Start.Truncate(0)) || a.Start.After(a.Last) {
		t.Fatalf("sess-a span = %s .. %s", a.Start, a.Last)
	}
	// Four tool_use blocks: the two calls into the indexes, plus a Read and an
	// Edit that are not. The two that are not are the whole point — without
	// them the share of every session is 100%.
	if a.Tools != 4 {
		t.Fatalf("sess-a tools = %d, want 4", a.Tools)
	}
	if share, ok := a.Share(); !ok || share != 50 {
		t.Fatalf("sess-a share = %d (%v), want 50", share, ok)
	}

	b := by["sess-b"]
	if got := b.ByTool(Graft); got != 2 {
		t.Fatalf("sess-b graft = %d, want 2", got)
	}
	if share, ok := b.Share(); !ok || share != 100 {
		t.Fatalf("sess-b share = %d (%v), want 100", share, ok)
	}
}

// A session that used neither tool still has to appear: it is the denominator
// of the whole page, and a list that shows only users cannot say how many
// sittings ignored both indexes.
func TestSessionRollsIncludeSessionsThatUsedNeitherTool(t *testing.T) {
	dir := t.TempDir()
	acct := filepath.Join(dir, ".claude")
	repo := filepath.Join(dir, "repo")
	now := time.Now()

	writeTranscript(t, acct, "sess-idle",
		otherToolRec(repo, "sess-idle", now.Add(-time.Hour), "Read"),
		otherToolRec(repo, "sess-idle", now, "Edit"),
	)

	x := New()
	if err := x.Update(context.Background(), Options{
		Accounts: []Account{{Name: "default", Dir: acct}}, Now: now,
	}); err != nil {
		t.Fatal(err)
	}

	rolls := x.SessionRolls(Window{Days: 7, Now: now}, 0)
	if len(rolls) != 1 {
		t.Fatalf("rolls = %d, want the idle session: %+v", len(rolls), rolls)
	}
	r := rolls[0]
	if r.Session != "sess-idle" || !r.Idle() {
		t.Fatalf("roll = %+v, want an idle sess-idle", r)
	}
	if r.Tools != 2 {
		t.Fatalf("tools = %d, want 2", r.Tools)
	}
	if share, ok := r.Share(); !ok || share != 0 {
		t.Fatalf("share = %d (%v), want 0", share, ok)
	}
	if r.Repo != repo {
		t.Fatalf("repo = %q, want %q", r.Repo, repo)
	}

	// The window summary counts only the sessions that touched a tool, so the
	// two numbers must disagree — that gap IS the adoption answer.
	total, used := x.SessionCount(Window{Days: 7, Now: now})
	if total != 1 || used != 0 {
		t.Fatalf("session count = %d total, %d used; want 1 and 0", total, used)
	}
	if got := x.Summarize(Window{Days: 7, Now: now}).Sessions; got != 0 {
		t.Fatalf("summary sessions = %d, want 0 — no event names this session", got)
	}
}

// The denominator is folded incrementally, and a transcript re-read from the
// start must not count twice: a doubled Tools halves every share derived from
// it.
func TestSessionToolCountIsIncrementalAndSurvivesRewrite(t *testing.T) {
	dir := t.TempDir()
	acct := filepath.Join(dir, ".claude")
	repo := filepath.Join(dir, "repo")
	now := time.Now()
	opts := Options{Accounts: []Account{{Name: "default", Dir: acct}}, Now: now}

	path := writeTranscript(t, acct, "sess-r",
		bashRec(repo, "sess-r", now.Add(-time.Hour), `graft ask "x" --source`),
		otherToolRec(repo, "sess-r", now.Add(-time.Hour), "Read"),
	)

	x := New()
	if err := x.Update(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if got := x.SessionRolls(Window{Days: 7, Now: now}, 0)[0].Tools; got != 2 {
		t.Fatalf("tools after first pass = %d, want 2", got)
	}

	// Appending must add only what was appended.
	writeTranscript(t, acct, "sess-r", otherToolRec(repo, "sess-r", now, "Edit"))
	if err := x.Update(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if got := x.SessionRolls(Window{Days: 7, Now: now}, 0)[0].Tools; got != 3 {
		t.Fatalf("tools after append = %d, want 3", got)
	}

	// A shorter file at the same path: the offset is stale, so the whole thing
	// is re-read. The count must end at the NEW file's total, not the sum.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	writeTranscript(t, acct, "sess-r", otherToolRec(repo, "sess-r", now, "Read"))
	if err := x.Update(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if got := x.SessionRolls(Window{Days: 7, Now: now}, 0)[0].Tools; got != 1 {
		t.Fatalf("tools after rewrite = %d, want 1 — the new file's count", got)
	}
}

// Sessions fall out of the rollup on their LAST line of activity, not on the
// day they started.
func TestPruneDropsSessionsByLastActivity(t *testing.T) {
	dir := t.TempDir()
	acct := filepath.Join(dir, ".claude")
	repo := filepath.Join(dir, "repo")
	now := time.Now()

	writeTranscript(t, acct, "sess-old",
		bashRec(repo, "sess-old", now.Add(-200*24*time.Hour), `graft ask "ancient" --source`),
	)
	writeTranscript(t, acct, "sess-new",
		bashRec(repo, "sess-new", now, `graft ask "current" --source`),
	)

	x := New()
	if err := x.Update(context.Background(), Options{
		Accounts: []Account{{Name: "default", Dir: acct}}, Now: now,
	}); err != nil {
		t.Fatal(err)
	}
	if _, ok := x.Sessions["sess-old"]; ok {
		t.Fatal("a session 200 days out of the retention window was kept")
	}
	if _, ok := x.Sessions["sess-new"]; !ok {
		t.Fatal("the current session was pruned")
	}
}

// The rolls a caller reads are copies: the next update keeps folding into the
// originals, and the UI renders from the main thread on the same tick.
func TestSessionRollsAreCopies(t *testing.T) {
	x := New()
	now := time.Now()
	r := x.session("s")
	r.Counts[counterKey(Graft, CLI, "ask")] = 1
	r.Last = now

	got := x.SessionRolls(Window{Days: 7, Now: now}, 0)
	if len(got) != 1 {
		t.Fatalf("rolls = %d, want 1", len(got))
	}
	got[0].Counts[counterKey(Graft, CLI, "ask")] = 99
	if x.Sessions["s"].Counts[counterKey(Graft, CLI, "ask")] != 1 {
		t.Fatal("mutating a returned roll reached the index")
	}
}
