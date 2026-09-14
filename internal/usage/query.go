package usage

import (
	"sort"
	"time"
)

// Window is what a summary is taken over: the last Days days ending at Now,
// optionally narrowed to one checkout.
//
// Days counts calendar days including today, so Days:7 is "this day and the
// six before it" — the answer a person means by "this week", not a rolling
// 168 hours.
type Window struct {
	Days int
	Now  time.Time
	// Repo narrows to one checkout and everything under it. Empty is the
	// whole machine.
	Repo string
}

func (w Window) resolve() (from time.Time, now time.Time, days int) {
	now = w.Now
	if now.IsZero() {
		now = time.Now()
	}
	days = w.Days
	if days <= 0 {
		days = 30
	}
	y, m, d := now.Date()
	today := time.Date(y, m, d, 0, 0, 0, 0, now.Location())
	return today.AddDate(0, 0, -(days - 1)), now, days
}

// DayPoint is one column of the timeline.
type DayPoint struct {
	Day      string
	Graphify int
	Graft    int
}

// Total is both tools together, which is what a sparkline plots.
func (p DayPoint) Total() int { return p.Graphify + p.Graft }

// Summary is everything the dashboard shows for one window.
type Summary struct {
	From, To time.Time
	Days     int

	// Events counts real invocations: CLI, MCP and skill. Hooks are counted
	// separately in Hooks, because they are the integration firing rather
	// than an agent choosing to use anything — and ByTool follows Events, so
	// a tool's headline number is never inflated by its own hook.
	Events     int
	Hooks      int
	ByTool     map[Tool]int
	HookByTool map[Tool]int
	ByKind     map[Kind]int

	// Verbs is the per-tool subcommand breakdown, hooks included, sorted.
	Verbs map[Tool][]Count
	// Accounts is which login the usage came through, by event.
	Accounts []Count
	// Repos is the busiest checkouts, by event, keyed by working directory.
	Repos []Count
	// Sessions is how many distinct agent sessions touched either tool.
	Sessions int

	// The graft counter half, summed over the window.
	GraftReads   int
	SourceReads  int
	Nudges       int
	SavedTokens  int64
	BilledTokens int64
	CostMicros   int64

	Series []DayPoint
}

// Mix is the share of reads that went through graft rather than straight to
// the source, in percent, and whether there were any reads at all to divide.
func (s Summary) Mix() (int, bool) {
	total := s.GraftReads + s.SourceReads
	if total == 0 {
		return 0, false
	}
	return s.GraftReads * 100 / total, true
}

// CostUSD is what the sessions that used graft were billed for input, in
// dollars. graft records it in millionths.
func (s Summary) CostUSD() float64 { return float64(s.CostMicros) / 1e6 }

// Summarize rolls the window up.
func (x *Index) Summarize(w Window) Summary {
	from, now, days := w.resolve()
	s := Summary{
		From: from, To: now, Days: days,
		ByTool: map[Tool]int{}, HookByTool: map[Tool]int{}, ByKind: map[Kind]int{},
		Verbs: map[Tool][]Count{},
	}

	verbs := map[Tool]map[string]int{Graphify: {}, Graft: {}}
	accounts := map[string]int{}
	repos := map[string]int{}
	sessions := map[string]bool{}
	points := map[string]*DayPoint{}

	x.mu.RLock()
	defer x.mu.RUnlock()

	for i := 0; i < days; i++ {
		key := DayKey(from.AddDate(0, 0, i))
		points[key] = &DayPoint{Day: key}
		day := x.Days[key]
		if day == nil {
			continue
		}
		for cwd, rd := range day.Repos {
			if w.Repo != "" && !RepoMatch(w.Repo, cwd) {
				continue
			}
			for ck, n := range rd.Counts {
				tool, kind, verb := splitCounter(ck)
				verbs[tool][verb] += n
				s.ByKind[kind] += n
				if kind == Hook {
					s.Hooks += n
					s.HookByTool[tool] += n
				} else {
					s.Events += n
					s.ByTool[tool] += n
					repos[cwd] += n
					points[key].add(tool, n)
				}
			}
			for acct, n := range rd.Accounts {
				accounts[acct] += n
			}
			for _, sess := range rd.Sessions {
				sessions[sess] = true
			}
			s.GraftReads += rd.GraftReads
			s.SourceReads += rd.SourceReads
			s.Nudges += rd.Nudges
			s.SavedTokens += rd.SavedTokens
			s.BilledTokens += rd.BilledTokens
			s.CostMicros += rd.CostMicros
		}
	}

	s.Sessions = len(sessions)
	for tool, m := range verbs {
		s.Verbs[tool] = counts(m)
	}
	s.Accounts = counts(accounts)
	s.Repos = counts(repos)
	s.Series = make([]DayPoint, 0, days)
	for i := 0; i < days; i++ {
		s.Series = append(s.Series, *points[DayKey(from.AddDate(0, 0, i))])
	}
	return s
}

func (p *DayPoint) add(t Tool, n int) {
	if t == Graft {
		p.Graft += n
		return
	}
	p.Graphify += n
}

func counts(m map[string]int) []Count {
	out := make([]Count, 0, len(m))
	for k, v := range m {
		out = append(out, Count{Name: k, Count: v})
	}
	sortCounts(out)
	return out
}

// RepoUse is the compact per-repository answer the board column needs: how
// much each tool was used in the window, when it was last used, and the daily
// series to draw.
type RepoUse struct {
	Graphify int
	Graft    int
	Last     time.Time
	Series   []int
}

// Total is both tools.
func (u RepoUse) Total() int { return u.Graphify + u.Graft }

// RepoUses answers for every checkout at once.
//
// The board repaints ~300 rows a second and a per-row Summarize would walk
// the whole rollup once per row; this walks it once for all of them. Repos
// that saw no usage are absent from the result, which is what the column
// renders as an empty cell.
func (x *Index) RepoUses(repos []string, w Window) map[string]RepoUse {
	from, _, days := w.resolve()
	out := make(map[string]RepoUse, len(repos))
	if len(repos) == 0 {
		return out
	}
	x.mu.RLock()
	defer x.mu.RUnlock()

	for i := 0; i < days; i++ {
		dayTime := from.AddDate(0, 0, i)
		key := DayKey(dayTime)
		day := x.Days[key]
		if day == nil {
			continue
		}
		for cwd, rd := range day.Repos {
			repo, ok := ownerOf(repos, cwd)
			if !ok {
				continue
			}
			u := out[repo]
			if u.Series == nil {
				u.Series = make([]int, days)
			}
			n := 0
			for ck, c := range rd.Counts {
				tool, kind, _ := splitCounter(ck)
				if kind == Hook {
					continue
				}
				n += c
				if tool == Graft {
					u.Graft += c
				} else {
					u.Graphify += c
				}
			}
			u.Series[i] += n
			if n > 0 && dayTime.After(u.Last) {
				u.Last = dayTime
			}
			out[repo] = u
		}
	}
	return out
}

// ownerOf picks the checkout a working directory belongs to: the longest
// match, so a repository nested inside another — a vendored checkout, a
// worktree under a parent — is credited to the inner one.
func ownerOf(repos []string, cwd string) (string, bool) {
	best := ""
	for _, r := range repos {
		if RepoMatch(r, cwd) && len(r) > len(best) {
			best = r
		}
	}
	return best, best != ""
}

// Recents returns the newest events first, at most n of them, optionally
// narrowed to one checkout. It is the activity list under the dashboard.
func (x *Index) Recents(repo string, n int) []Event {
	x.mu.RLock()
	defer x.mu.RUnlock()
	out := make([]Event, 0, n)
	for i := len(x.Recent) - 1; i >= 0 && len(out) < n; i-- {
		e := x.Recent[i]
		if repo != "" && !RepoMatch(repo, e.Repo) {
			continue
		}
		out = append(out, e)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	return out
}

// LastUpdate is when the rollup was last refreshed, and how many transcripts
// that update had to open.
func (x *Index) LastUpdate() (time.Time, int) {
	x.mu.RLock()
	defer x.mu.RUnlock()
	return x.UpdatedAt, x.Scanned
}
