package usage

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/dns/ggraphify/internal/board"
)

// The report is this package's answer to a question the dashboard can show but
// cannot settle: are these two indexes actually being reached for, everywhere
// an agent works, as often as they would have paid off?
//
// That is a judgement, and the thing qualified to make it is the agent itself.
// So the report is written to be pasted into a Claude Code session: markdown,
// self-describing, honest about what each number is and — more importantly —
// what it is not. Everything here is derived from Summary and from the board's
// own rows; nothing is read from disk, so the GUI's clipboard button and the
// headless `ggraphify-scan -usage -markdown` emit the identical text.

// ReportOptions is the context the numbers alone do not carry: which machine
// state they were taken from, and how fresh they are.
type ReportOptions struct {
	// Scope is the checkout the summary was narrowed to. Empty means the
	// whole machine, which is the scope the interesting question is asked at.
	Scope string
	// Version is the ggraphify build that produced it.
	Version string
	// Now is the clock, for tests. Zero means time.Now.
	Now time.Time
	// Accounts is the Claude Code logins the transcripts were read from.
	Accounts []Account
	// LastUpdate and Scanned are Index.LastUpdate, so a reader can tell a
	// stale rollup from a quiet machine.
	LastUpdate time.Time
	Scanned    int
	// Repos is every checkout the board knows about. It is what keeps the
	// told-and-never-used list honest: a session's working directory can be a
	// scratchpad, a worktree under .cache, or the home directory itself, and
	// naming those as repositories that ignored their index is a false
	// positive a reader would act on.
	Repos []string
	// Recents is the tail of the event log, newest first. Optional.
	Recents []Event
}

// reportRepos caps the per-repository table. A machine with three hundred
// working directories would otherwise produce a report nobody pastes.
const reportRepos = 40

// Report renders the whole rollup as markdown for an agent to read.
func Report(s Summary, recs []Rec, opt ReportOptions) string {
	now := opt.Now
	if now.IsZero() {
		now = time.Now()
	}

	var b strings.Builder
	scope := "every repository on this machine"
	if opt.Scope != "" {
		scope = board.Tilde(opt.Scope)
	}
	fmt.Fprintf(&b, "# graphify + graft usage — %s\n\n", scope)
	fmt.Fprintf(&b, "%s → %s (%d days), generated %s",
		s.From.Format("2006-01-02"), s.To.Format("2006-01-02"), s.Days, now.Format(time.RFC3339))
	if opt.Version != "" {
		fmt.Fprintf(&b, " by ggraphify %s", opt.Version)
	}
	b.WriteString(".\n\n")

	reportHeadline(&b, s)
	reportOpportunity(&b, s, recs)
	reportRepoTable(&b, s, opt)
	reportVerbs(&b, s)
	reportAccounts(&b, s)
	reportTimeline(&b, s)
	reportRecents(&b, opt)
	reportProvenance(&b, s, opt)
	reportAsk(&b)
	return b.String()
}

func reportHeadline(b *strings.Builder, s Summary) {
	b.WriteString("## Headline\n\n")
	fmt.Fprintf(b, "- **%d uses** over %d days — graphify %d, graft %d\n",
		s.Events, s.Days, s.ByTool[Graphify], s.ByTool[Graft])
	if s.Sessions > 0 {
		fmt.Fprintf(b, "- **%d agent sessions** in which either tool ran *or* one of their hooks fired — "+
			"%s uses per session\n", s.Sessions, per(s.Events, s.Sessions))
	} else {
		b.WriteString("- no session ran either tool or had one of their hooks fire\n")
	}
	fmt.Fprintf(b, "- by route: %s\n", kindLine(s))
	fmt.Fprintf(b, "- **%d hook injections** (graphify %d, graft %d) — counted separately, "+
		"because the integration firing is not an agent choosing to use anything\n",
		s.Hooks, s.HookByTool[Graphify], s.HookByTool[Graft])
	if mix, ok := s.Mix(); ok {
		fmt.Fprintf(b, "- graft read **%d%%** of the time: %d reads through the index, %d straight to source\n",
			mix, s.GraftReads, s.SourceReads)
	} else {
		b.WriteString("- graft recorded no reads at all in this window\n")
	}
	if s.Nudges > 0 {
		fmt.Fprintf(b, "- graft nudged the agent back to the index %d times\n", s.Nudges)
	}
	if s.SavedTokens > 0 || s.BilledTokens > 0 {
		fmt.Fprintf(b, "- graft's own figures: ~%s tokens saved, %s billed as input, $%.2f\n",
			short(s.SavedTokens), short(s.BilledTokens), s.CostUSD())
		b.WriteString("  (the dollars are what the sessions that used graft were billed in total, " +
			"not the cost of graft)\n")
	}
	b.WriteByte('\n')
}

// reportOpportunity is the part worth reading first: the two ratios that say
// whether either tool is being left on the table.
func reportOpportunity(b *strings.Builder, s Summary, recs []Rec) {
	b.WriteString("## Is either tool being left on the table?\n\n")

	if s.Sessions > 0 {
		fmt.Fprintf(b, "- Across **%d sessions** the two tools were invoked **%d** times in total: "+
			"%s graphify and %s graft per session. A session is counted here as soon as a hook fires "+
			"in it, so this includes the sessions that were offered an index and used nothing.\n",
			s.Sessions, s.Events, per(s.ByTool[Graphify], s.Sessions), per(s.ByTool[Graft], s.Sessions))
	}

	// Both integrations' injections currently record under the verb
	// `context` — neither hook's text names the event that fired it — so the
	// denominator is the per-tool hook total, not a verb.
	if h := s.HookByTool[Graft]; h > 0 {
		fmt.Fprintf(b, "- graft's hook injected its pointer **%d** times; graft was then invoked **%d** "+
			"times (%s). Every injection is a session being told the index is there.\n",
			h, s.ByTool[Graft], ratio(s.ByTool[Graft], h))
	} else {
		b.WriteString("- graft's hook never fired in this window, so no session was told the index " +
			"exists. Nothing here can distinguish \"ignored\" from \"never offered\".\n")
	}
	if h := s.HookByTool[Graphify]; h > 0 {
		fmt.Fprintf(b, "- graphify's reminder injected **%d** times against **%d** graphify uses (%s). "+
			"It is a PreToolUse hook, so it fires per tool call rather than per session — a large "+
			"number is expected, and the ratio is about how often the reminder is read past, not how "+
			"many sessions saw it.\n", h, s.ByTool[Graphify], ratio(s.ByTool[Graphify], h))
	}

	if mix, ok := s.Mix(); ok && s.SourceReads > 0 {
		fmt.Fprintf(b, "- %d reads went straight to source rather than through graft (%d%% mix). "+
			"Those are the reads an index was already built for.\n", s.SourceReads, mix)
	}
	if s.Nudges > 0 {
		fmt.Fprintf(b, "- graft nudged the agent back to the index **%d** times — each nudge is a read "+
			"it judged should have gone through the graph.\n", s.Nudges)
	}

	if len(recs) == 0 {
		b.WriteString("\nNo repository on the board has usage its index state disagrees with.\n\n")
		return
	}
	b.WriteString("\n### Worth doing next — where usage and index state disagree\n\n")
	b.WriteString("| uses | repository | do | cost | why |\n|---:|---|---|---|---|\n")
	for _, r := range recs {
		do := r.Action.Command()
		if do == "" {
			do = "add a scan root"
		}
		cost := "free"
		if r.Metered() {
			cost = "METERED"
		}
		fmt.Fprintf(b, "| %d | %s | `%s` | %s | %s |\n", r.Uses, board.Tilde(r.Name), do, cost, r.Why)
	}
	b.WriteByte('\n')
}

func reportRepoTable(b *strings.Builder, s Summary, opt ReportOptions) {
	if len(s.Repos) == 0 {
		b.WriteString("## Where\n\nNeither tool was run in any directory in this window.\n\n")
	} else {
		fmt.Fprintf(b, "## Where — %d working directories\n\n", len(s.Repos))
		b.WriteString("Hook columns are injections, not uses: they are the denominator, " +
			"the number of times the agent was told the index was there.\n\n")
		b.WriteString("| uses | directory | graphify | graft | gfy hooks | graft hooks |\n|---:|---|---:|---:|---:|---:|\n")
		for i, r := range s.Repos {
			if i >= reportRepos {
				fmt.Fprintf(b, "\n…and %d more directories with less usage.\n", len(s.Repos)-reportRepos)
				break
			}
			sp := s.RepoSplit[r.Name]
			fmt.Fprintf(b, "| %d | %s | %d | %d | %d | %d |\n",
				r.Count, board.Tilde(r.Name), sp.Graphify, sp.Graft, sp.GraphifyHooks, sp.GraftHooks)
		}
		b.WriteByte('\n')
	}

	// The silent list is rendered whether or not anything was used, because a
	// window in which NOTHING was used is the one where it matters most: the
	// table above is keyed on uses, so a directory with injections and no uses
	// never appears in it, and an early return on "nothing ran" would drop the
	// only section that still had something to say.
	//
	// Only boarded checkouts are named. A working directory that is not one —
	// a scratchpad, an agent worktree, the home directory — had a hook fire in
	// it, but calling it a repository that ignored its index is advice about
	// something that cannot be acted on. When the caller passes no board at
	// all, every directory is listed rather than none: a headless reader with
	// no row list still deserves the raw answer.
	var silent []string
	for cwd, sp := range s.RepoSplit {
		if sp.Uses() > 0 || sp.Hooks() == 0 {
			continue
		}
		if len(opt.Repos) > 0 && !boarded(opt.Repos, cwd) {
			continue
		}
		silent = append(silent, fmt.Sprintf("%s (%s, 0 uses)", board.Tilde(cwd), injections(sp.Hooks())))
	}
	if len(silent) == 0 {
		return
	}
	sort.Strings(silent)
	b.WriteString("**Told, and never used** — every session here was handed the index and reached for it zero times:\n\n")
	for _, line := range silent {
		fmt.Fprintf(b, "- %s\n", line)
	}
	b.WriteByte('\n')
}

// injections agrees with itself about the singular.
func injections(n int) string {
	if n == 1 {
		return "1 injection"
	}
	return fmt.Sprintf("%d injections", n)
}

// boarded reports whether a working directory is one of the board's checkouts.
func boarded(repos []string, cwd string) bool {
	for _, r := range repos {
		if r == cwd {
			return true
		}
	}
	return false
}

func reportVerbs(b *strings.Builder, s Summary) {
	b.WriteString("## What was run\n\n")
	for _, tool := range []Tool{Graphify, Graft} {
		list := s.Verbs[tool]
		if len(list) == 0 {
			fmt.Fprintf(b, "- **%s**: nothing\n", tool)
			continue
		}
		parts := make([]string, 0, len(list))
		for _, c := range list {
			parts = append(parts, fmt.Sprintf("%s %d", c.Name, c.Count))
		}
		fmt.Fprintf(b, "- **%s**: %s\n", tool, strings.Join(parts, " · "))
	}
	b.WriteString("\n(Verbs include each tool's hook injections. Neither hook's text names the event " +
		"that fired it, so both land under `context`; `session-start`, `pre-tool` and `prompt` appear " +
		"only when the injected text says so.)\n\n")
}

func reportAccounts(b *strings.Builder, s Summary) {
	if len(s.Accounts) == 0 {
		return
	}
	b.WriteString("## Which login\n\n")
	for _, c := range s.Accounts {
		fmt.Fprintf(b, "- %s: %d\n", c.Name, c.Count)
	}
	b.WriteByte('\n')
}

func reportTimeline(b *strings.Builder, s Summary) {
	b.WriteString("## Every day\n\n```\nday         graphify  graft\n")
	quiet := 0
	for _, p := range s.Series {
		if p.Total() == 0 {
			quiet++
		}
		fmt.Fprintf(b, "%s  %8d %6d\n", p.Day, p.Graphify, p.Graft)
	}
	b.WriteString("```\n\n")
	fmt.Fprintf(b, "%d of %d days saw neither tool run.\n\n", quiet, s.Days)
}

func reportRecents(b *strings.Builder, opt ReportOptions) {
	if len(opt.Recents) == 0 {
		return
	}
	b.WriteString("## Latest\n\n")
	for _, e := range opt.Recents {
		fmt.Fprintf(b, "- %s  %s %s (%s) — %s\n",
			e.At.Format("01-02 15:04"), e.Tool, e.Verb, e.Kind, board.Tilde(e.Repo))
	}
	b.WriteByte('\n')
}

func reportProvenance(b *strings.Builder, s Summary, opt ReportOptions) {
	b.WriteString("## Where these numbers come from\n\n")
	b.WriteString("- Uses are parsed out of Claude Code's own transcripts: Bash calls, MCP tool " +
		"calls, and slash-command invocations naming either tool. No prompt, argument value or " +
		"output is recorded — only the subcommand.\n")
	b.WriteString("- The graft read/saved/billed figures are graft's own per-session counter " +
		"files, not derived from transcripts, so the two halves are never double counted.\n")
	if n := len(opt.Accounts); n > 0 {
		names := make([]string, 0, n)
		for _, a := range opt.Accounts {
			names = append(names, a.Name)
		}
		fmt.Fprintf(b, "- Read from %d Claude Code account(s): %s.\n", n, strings.Join(names, ", "))
	}
	if n := len(opt.Repos); n > 0 {
		fmt.Fprintf(b, "- The board knows about %d checkouts; %d working directories showed any usage.\n",
			n, len(s.Repos))
	}
	if !opt.LastUpdate.IsZero() {
		fmt.Fprintf(b, "- Rollup last brought up to date %s; %d transcript(s) were new on that read.\n",
			board.Age(opt.LastUpdate), opt.Scanned)
	}
	b.WriteString("- The session count includes sessions where only a hook fired, which makes it a " +
		"fair denominator for any repository the integrations are wired into. It is **not** a count " +
		"of all Claude Code sessions: a session in a directory with neither tool installed leaves no " +
		"trace here at all, so a repository's absence is not evidence that it was skipped.\n\n")
}

// reportAsk is the part that makes this a prompt rather than a dump: the
// questions the numbers above are evidence for, spelled out so the agent
// reading it answers the right one.
func reportAsk(b *strings.Builder) {
	b.WriteString("## What to do with this\n\n")
	b.WriteString("Judge whether graphify and graft are being reached for at every opportunity, " +
		"and say where they are not. Specifically:\n\n")
	b.WriteString("1. Which directories were handed the index and used it least? (hooks high, uses low)\n")
	b.WriteString("2. Is graft's read mix low enough that reads are going to raw source a built index " +
		"already covers?\n")
	b.WriteString("3. Are the uses concentrated in one or two repositories while the rest of the " +
		"board sits idle — and is that because the work is there, or because nothing is wired up?\n")
	b.WriteString("4. Do the verbs suggest shallow use — one `ask` per session and nothing else — " +
		"where `callers`, `grep` or `skeleton` would have answered better?\n")
	b.WriteString("5. Which of the recommendations above would pay for itself fastest, given the " +
		"usage each repository already has?\n\n")
	b.WriteString("Answer from this report. Do not re-derive the numbers, and do not treat the " +
		"absence of a repository as evidence of anything — it may simply have had no sessions.\n")
}

func kindLine(s Summary) string {
	parts := make([]string, 0, len(Kinds))
	for _, k := range Kinds {
		parts = append(parts, fmt.Sprintf("%s %d", k, s.ByKind[k]))
	}
	return strings.Join(parts, " · ")
}

func verbCount(list []Count, name string) int {
	for _, c := range list {
		if c.Name == name {
			return c.Count
		}
	}
	return 0
}

// ratio renders n/of as a percentage, guarding the empty denominator that a
// quiet window produces.
func ratio(n, of int) string {
	if of <= 0 {
		return "no baseline"
	}
	return fmt.Sprintf("%d%% of them", n*100/of)
}

// per renders a rate as a decimal, guarding the empty denominator.
func per(n, of int) string {
	if of <= 0 {
		return "no sessions"
	}
	return fmt.Sprintf("%.2f", float64(n)/float64(of))
}

// short abbreviates the large counts graft reports, which run to millions.
func short(n int64) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1e3)
	}
	return fmt.Sprint(n)
}
