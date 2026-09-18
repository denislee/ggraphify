package usage

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Version is the rollup file's schema version. A file written by a newer
// board is discarded rather than half-read: the whole thing can be rebuilt
// from the transcripts in one pass, so there is nothing to salvage.
const Version = 3

// Retain is how much history the rollup keeps. Three months is long enough to
// see a habit form and short enough that the file stays a few hundred
// kilobytes on a machine with a hundred repositories.
const Retain = 90 * 24 * time.Hour

// MaxRecent bounds the tail of individual events kept for the activity list.
const MaxRecent = 400

// Index is the rollup: per-day, per-repository counters, plus the bookkeeping
// that makes the next scan incremental.
//
// It is safe for concurrent use. The board updates it on a worker goroutine
// and renders from the main thread on the same tick.
type Index struct {
	mu sync.RWMutex

	Ver        int                     `json:"version"`
	Files      map[string]FileState    `json:"files"`
	GraftFiles map[string]SessionState `json:"graft_files"`
	Days       map[string]*Day         `json:"days"`
	// Pending is the calls whose result has not been read yet, by tool_use
	// id. It is persisted because a call and its answer are routinely written
	// to the transcript in different scans — see failure.go.
	Pending map[string]pendingCall `json:"pending"`
	// Owners maps a session id to the account whose transcript carried it,
	// so graft's own counter files — which do not name an account — can be
	// attributed to the login that paid for them.
	Owners map[string]string `json:"owners"`
	// Sessions is the per-session rollup, by session id — see session.go. It
	// is keyed globally rather than under a day because a sitting spans
	// midnight and splitting one in half would make every derived share wrong
	// on both sides of it.
	Sessions  map[string]*SessionRoll `json:"sessions"`
	Recent    []Event                 `json:"recent"`
	UpdatedAt time.Time               `json:"updated_at"`
	// Scanned is how many transcripts the last update actually opened, as
	// opposed to skipped on a stat. It is shown in the UI because "this
	// number did not move" and "nothing was read" are different answers.
	Scanned int `json:"-"`
}

// Day is one calendar day's usage, by repository.
type Day struct {
	Repos map[string]*RepoDay `json:"repos"`
}

// RepoDay is what happened in one repository on one day.
type RepoDay struct {
	// Counts is keyed by tool/kind/verb — see counterKey.
	Counts   map[string]int `json:"counts"`
	Accounts map[string]int `json:"accounts"`
	Sessions []string       `json:"sessions"`
	// Fails is the subset of those calls that came back unusable, keyed by
	// tool/reason/verb — see failKey. It is a separate map rather than a
	// variant of Counts because a failure is learnt AFTER the call it belongs
	// to has already been counted: the result arrives in a later line, often
	// in a later scan, and a total that had to be moved between buckets at
	// that point could not be folded incrementally at all.
	Fails map[string]int `json:"fails,omitempty"`

	// The graft counter half. These come from graft's own per-session files
	// and are never derived from transcripts, so they are not double counted.
	GraftReads   int   `json:"graft_reads"`
	SourceReads  int   `json:"source_reads"`
	Nudges       int   `json:"nudges"`
	SavedTokens  int64 `json:"saved_tokens"`
	CostMicros   int64 `json:"cost_micros"`
	BilledTokens int64 `json:"billed_tokens"`
}

// Options configures one update.
type Options struct {
	// Accounts are the Claude Code configuration directories to read.
	Accounts []Account
	// Repos are the checkouts whose graft session counters to read. The board
	// passes every row it has; a repository with no graft directory costs one
	// failed ReadDir.
	Repos []string
	// Since bounds how far back events are recorded. Zero means Retain before
	// Now. Transcripts are still read past it — the offset has to advance —
	// but nothing older is counted.
	Since time.Time
	// Now is the clock, for tests. Zero means time.Now.
	Now time.Time
}

// New returns an empty index.
func New() *Index {
	return &Index{
		Ver:        Version,
		Files:      map[string]FileState{},
		GraftFiles: map[string]SessionState{},
		Days:       map[string]*Day{},
		Owners:     map[string]string{},
		Sessions:   map[string]*SessionRoll{},
		Pending:    map[string]pendingCall{},
	}
}

// Load reads a rollup from disk. A missing, unreadable or differently
// versioned file yields an empty index and no error: this is a derived cache,
// and the only cost of losing it is one slow scan.
//
// An OLDER file is discarded as well as a newer one, which is the one place
// this package pays for a schema change. A v1 rollup carries the offsets that
// say every transcript has already been read, and no failures at all — so
// keeping it would leave every repository looking like it had never had a call
// come back empty until the next time an agent happened to use it. One cold
// pass over the corpus is the cheaper wrong answer to avoid.
func Load(path string) *Index {
	x := New()
	b, err := os.ReadFile(path)
	if err != nil {
		return x
	}
	var in Index
	if err := json.Unmarshal(b, &in); err != nil || in.Ver != Version {
		return x
	}
	if in.Files != nil {
		x.Files = in.Files
	}
	if in.GraftFiles != nil {
		x.GraftFiles = in.GraftFiles
	}
	if in.Days != nil {
		x.Days = in.Days
	}
	if in.Owners != nil {
		x.Owners = in.Owners
	}
	if in.Sessions != nil {
		x.Sessions = in.Sessions
	}
	if in.Pending != nil {
		x.Pending = in.Pending
	}
	x.Recent = in.Recent
	x.UpdatedAt = in.UpdatedAt
	return x
}

// DefaultPath is the rollup's place beside the rest of the sidecar state.
func DefaultPath(dir string) string { return filepath.Join(dir, "usage.json") }

// Save writes the rollup atomically.
func (x *Index) Save(path string) error {
	x.mu.RLock()
	b, err := json.Marshal(x)
	x.mu.RUnlock()
	if err != nil {
		return err
	}
	// Same directory and the same reasoning as the state file beside it: this
	// is per-user state, and it records which repositories on this machine the
	// user works on.
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// MarshalJSON serialises under the read lock.
func (x *Index) MarshalJSON() ([]byte, error) {
	type alias struct {
		Ver        int                     `json:"version"`
		Files      map[string]FileState    `json:"files"`
		GraftFiles map[string]SessionState `json:"graft_files"`
		Days       map[string]*Day         `json:"days"`
		Owners     map[string]string       `json:"owners"`
		Sessions   map[string]*SessionRoll `json:"sessions"`
		Pending    map[string]pendingCall  `json:"pending"`
		Recent     []Event                 `json:"recent"`
		UpdatedAt  time.Time               `json:"updated_at"`
	}
	return json.Marshal(alias{
		Ver: Version, Files: x.Files, GraftFiles: x.GraftFiles,
		Days: x.Days, Owners: x.Owners, Sessions: x.Sessions,
		Pending: x.Pending, Recent: x.Recent, UpdatedAt: x.UpdatedAt,
	})
}

// Update folds everything that has happened since the last update into the
// rollup. It is the expensive call and belongs off the main thread; ctx
// cancels it between lines, leaving the offsets consistent with what was
// actually counted.
func (x *Index) Update(ctx context.Context, opts Options) error {
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	since := opts.Since
	if since.IsZero() {
		since = now.Add(-Retain)
	}

	x.mu.Lock()
	files := make(map[string]FileState, len(x.Files))
	for k, v := range x.Files {
		files[k] = v
	}
	graftFiles := make(map[string]SessionState, len(x.GraftFiles))
	for k, v := range x.GraftFiles {
		graftFiles[k] = v
	}
	// The calls still waiting on a result, carried over from the last scan.
	// Anything older than the TTL is dropped here rather than re-persisted: a
	// session that was interrupted mid-call never produces the answer.
	pend := newPendingSet(x.Pending, now.Add(-pendingTTL))
	x.mu.Unlock()

	var (
		events   []Event
		fails    []failure
		deltas   []delta
		scans    []sessionScan
		scanned  int
		nextF    = map[string]FileState{}
		nextG    = map[string]SessionState{}
		emitE    = func(e Event) { events = append(events, e); pend.add(e, now) }
		emitD    = func(d delta) { deltas = append(deltas, d) }
		keepSess = func(p string, s SessionState) { nextG[p] = s }
		// A result resolves the call it answers: a failing one is recorded,
		// a successful one simply stops being waited on.
		emitR = func(id string, f Fail) {
			e, ok := pend.take(id)
			if !ok {
				return
			}
			if f != FailNone {
				fails = append(fails, failure{Event: e, Reason: f})
			}
		}
	)

	for _, acct := range opts.Accounts {
		paths, err := transcripts(acct.Dir)
		if err != nil {
			continue
		}
		for _, p := range paths {
			if err := ctx.Err(); err != nil {
				return err
			}
			fi, err := os.Stat(p)
			if err != nil {
				continue
			}
			st := files[p]
			if st.Size == fi.Size() && st.ModNS == fi.ModTime().UnixNano() {
				nextF[p] = st
				continue
			}
			off := st.Offset
			if fi.Size() < off {
				// The file shrank: it was rotated or rewritten, and the old
				// offset points into the middle of a different file. Start
				// over rather than parse garbage.
				off = 0
			}
			scanned++
			end, sc, err := scanTranscript(ctx, p, acct.Name, off, since, pend, emitE, emitR)
			if err != nil && ctx.Err() != nil {
				return err
			}
			if off == 0 && st.Tools > 0 {
				// Re-read from the start: this pass counted the whole file, so
				// what the old one contributed has to come back off. The result
				// is the new file's count, not the sum of both.
				sc.Tools -= st.Tools
			}
			scans = append(scans, sc)
			nextF[p] = FileState{
				Size: fi.Size(), ModNS: fi.ModTime().UnixNano(), Offset: end,
				Tools: st.Tools + sc.Tools,
			}
		}
	}

	for _, repo := range opts.Repos {
		if err := ctx.Err(); err != nil {
			return err
		}
		scanGraftSessions(repo, graftFiles, since, emitD, keepSess)
	}

	x.mu.Lock()
	defer x.mu.Unlock()
	x.Ver = Version
	x.Files = nextF
	// A repository the board no longer lists keeps its state: dropping it
	// would make every counter in it look new the next time it is boarded,
	// and re-emit a whole session's worth of deltas.
	for k, v := range nextG {
		graftFiles[k] = v
	}
	x.GraftFiles = graftFiles
	// The session registry is folded before the events, so a roll already
	// knows its working directory and its start by the time a call is counted
	// into it. It also covers the sessions no event will ever name — the ones
	// that used neither tool, which are the denominator.
	for _, sc := range scans {
		x.addScan(sc)
	}
	for _, e := range events {
		x.add(e)
	}
	// Failures are folded after the calls, never instead of them: a call that
	// failed is still a call an agent chose to make, and the moment it stops
	// being counted as one the "used with no index" gap this package exists to
	// find disappears from the totals.
	for _, f := range fails {
		x.addFail(f)
	}
	for _, d := range deltas {
		x.addDelta(d)
	}
	x.Pending = pend.snapshot()
	x.prune(since)
	x.UpdatedAt = now
	x.Scanned = scanned
	return nil
}

// add folds one event in. Caller holds the lock.
func (x *Index) add(e Event) {
	rd := x.repoDay(DayKey(e.At), e.Repo)
	rd.Counts[counterKey(e.Tool, e.Kind, e.Verb)]++
	if e.Account != "" {
		rd.Accounts[e.Account]++
	}
	if e.Session != "" {
		if !contains(rd.Sessions, e.Session) {
			rd.Sessions = append(rd.Sessions, e.Session)
		}
		if e.Account != "" {
			x.Owners[e.Session] = e.Account
		}
		r := x.session(e.Session)
		r.Counts[counterKey(e.Tool, e.Kind, e.Verb)]++
		if r.Account == "" {
			r.Account = e.Account
		}
		if r.Repo == "" {
			r.Repo = e.Repo
		}
		if r.Branch == "" {
			r.Branch = e.Branch
		}
		if r.Start.IsZero() || e.At.Before(r.Start) {
			r.Start = e.At
		}
		if e.At.After(r.Last) {
			r.Last = e.At
		}
	}
	// Hooks fire on their own, dozens of times a session; putting them in the
	// activity list would bury every command an agent actually ran.
	if e.Kind != Hook {
		x.Recent = append(x.Recent, e)
		if len(x.Recent) > MaxRecent*2 {
			sort.SliceStable(x.Recent, func(i, j int) bool { return x.Recent[i].At.Before(x.Recent[j].At) })
			x.Recent = x.Recent[len(x.Recent)-MaxRecent:]
		}
	}
}

// addFail folds one failed call in: the counter for the day the CALL was
// made — not the day its result arrived — and the mark on the event in the
// activity list, so "Latest" can say which of those calls came back empty.
// Caller holds the lock.
func (x *Index) addFail(f failure) {
	e := f.Event
	rd := x.repoDay(DayKey(e.At), e.Repo)
	if rd.Fails == nil {
		rd.Fails = map[string]int{}
	}
	key := failKey(e.Tool, f.Reason, e.Verb)
	rd.Fails[key]++
	if e.Session != "" {
		r := x.session(e.Session)
		if r.Fails == nil {
			r.Fails = map[string]int{}
		}
		r.Fails[key]++
	}
	if e.ID == "" {
		return
	}
	for i := len(x.Recent) - 1; i >= 0; i-- {
		if x.Recent[i].ID == e.ID {
			x.Recent[i].Fail = f.Reason
			return
		}
	}
}

// addDelta folds one graft counter movement in. Caller holds the lock.
func (x *Index) addDelta(d delta) {
	rd := x.repoDay(DayKey(d.At), d.Repo)
	rd.GraftReads += d.Reads
	rd.SourceReads += d.Source
	rd.Nudges += d.Nudges
	rd.SavedTokens += d.Saved
	rd.CostMicros += d.Cost
	rd.BilledTokens += d.Billed
	if d.Session == "" {
		return
	}
	if !contains(rd.Sessions, d.Session) {
		rd.Sessions = append(rd.Sessions, d.Session)
	}
	r := x.session(d.Session)
	r.GraftReads += d.Reads
	r.SourceReads += d.Source
	r.Nudges += d.Nudges
	r.SavedTokens += d.Saved
	r.CostMicros += d.Cost
	r.BilledTokens += d.Billed
	if r.Repo == "" {
		r.Repo = d.Repo
	}
	// graft's counter files name no account. Owners is the join that fixes
	// that, and it is the only thing it is for.
	if r.Account == "" {
		r.Account = x.Owners[d.Session]
	}
	if r.Start.IsZero() || d.At.Before(r.Start) {
		r.Start = d.At
	}
	if d.At.After(r.Last) {
		r.Last = d.At
	}
}

// addScan folds one transcript pass's view of its session in: the metadata
// every session has whether or not it ever reached for either tool, and the
// total tool-call count that makes the per-session shares mean anything.
// Caller holds the lock.
func (x *Index) addScan(sc sessionScan) {
	if sc.Session == "" {
		return
	}
	r := x.session(sc.Session)
	if sc.Account != "" {
		r.Account = sc.Account
		x.Owners[sc.Session] = sc.Account
	}
	// The newest line wins for the mutable pair: a session that changed
	// directory or switched branch mid-sitting is reported as where it ended
	// up, which is what the board can still match against a row.
	if sc.Repo != "" {
		r.Repo = sc.Repo
	}
	if sc.Branch != "" {
		r.Branch = sc.Branch
	}
	if !sc.Start.IsZero() && (r.Start.IsZero() || sc.Start.Before(r.Start)) {
		r.Start = sc.Start
	}
	if sc.Last.After(r.Last) {
		r.Last = sc.Last
	}
	if r.Tools += sc.Tools; r.Tools < 0 {
		// Only reachable through the rotation correction above, and only if the
		// replacement file is shorter than what the old one contributed. A
		// negative denominator is worse than a low one.
		r.Tools = 0
	}
}

// session returns the roll for one session id, creating it if this is the
// first thing ever seen from it. Caller holds the lock.
func (x *Index) session(id string) *SessionRoll {
	if x.Sessions == nil {
		x.Sessions = map[string]*SessionRoll{}
	}
	r := x.Sessions[id]
	if r == nil {
		r = &SessionRoll{Session: id, Counts: map[string]int{}}
		x.Sessions[id] = r
	}
	if r.Counts == nil {
		r.Counts = map[string]int{}
	}
	return r
}

func (x *Index) repoDay(day, repo string) *RepoDay {
	d := x.Days[day]
	if d == nil {
		d = &Day{Repos: map[string]*RepoDay{}}
		x.Days[day] = d
	}
	if d.Repos == nil {
		d.Repos = map[string]*RepoDay{}
	}
	rd := d.Repos[repo]
	if rd == nil {
		rd = &RepoDay{Counts: map[string]int{}, Accounts: map[string]int{}}
		d.Repos[repo] = rd
	}
	if rd.Counts == nil {
		rd.Counts = map[string]int{}
	}
	if rd.Accounts == nil {
		rd.Accounts = map[string]int{}
	}
	return rd
}

// prune drops days that have fallen out of the retention window, the events
// that belong to them, and the session-to-account mapping nothing references.
func (x *Index) prune(since time.Time) {
	cut := DayKey(since)
	for day := range x.Days {
		if day < cut {
			delete(x.Days, day)
		}
	}
	// A session is dropped on its last line of activity, not on the day it
	// started: a sitting that ran across the edge of the window still has
	// counters inside it.
	for id, r := range x.Sessions {
		if r == nil || r.Last.Before(since) {
			delete(x.Sessions, id)
		}
	}
	live := map[string]bool{}
	for _, d := range x.Days {
		for _, rd := range d.Repos {
			for _, s := range rd.Sessions {
				live[s] = true
			}
		}
	}
	for id := range x.Sessions {
		live[id] = true
	}
	for s := range x.Owners {
		if !live[s] {
			delete(x.Owners, s)
		}
	}
	sort.SliceStable(x.Recent, func(i, j int) bool { return x.Recent[i].At.Before(x.Recent[j].At) })
	keep := x.Recent[:0]
	for _, e := range x.Recent {
		if !e.At.Before(since) {
			keep = append(keep, e)
		}
	}
	x.Recent = keep
	if len(x.Recent) > MaxRecent {
		x.Recent = x.Recent[len(x.Recent)-MaxRecent:]
	}
	for id, c := range x.Pending {
		if c.Seen.Before(since) {
			delete(x.Pending, id)
		}
	}
}

func contains(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}

// RepoMatch reports whether a session's working directory belongs to a
// checkout. An agent run from a subdirectory is still that repository's usage,
// which is why this is a prefix test and not equality.
func RepoMatch(repo, cwd string) bool {
	if repo == "" || cwd == "" {
		return false
	}
	if repo == cwd {
		return true
	}
	return strings.HasPrefix(cwd, strings.TrimSuffix(repo, string(filepath.Separator))+string(filepath.Separator))
}
