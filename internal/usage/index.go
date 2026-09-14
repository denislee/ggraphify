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
const Version = 1

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
	// Owners maps a session id to the account whose transcript carried it,
	// so graft's own counter files — which do not name an account — can be
	// attributed to the login that paid for them.
	Owners    map[string]string `json:"owners"`
	Recent    []Event           `json:"recent"`
	UpdatedAt time.Time         `json:"updated_at"`
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
	}
}

// Load reads a rollup from disk. A missing, unreadable or future-versioned
// file yields an empty index and no error: this is a derived cache, and the
// only cost of losing it is one slow scan.
func Load(path string) *Index {
	x := New()
	b, err := os.ReadFile(path)
	if err != nil {
		return x
	}
	var in Index
	if err := json.Unmarshal(b, &in); err != nil || in.Ver > Version {
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
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
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
		Recent     []Event                 `json:"recent"`
		UpdatedAt  time.Time               `json:"updated_at"`
	}
	return json.Marshal(alias{
		Ver: Version, Files: x.Files, GraftFiles: x.GraftFiles,
		Days: x.Days, Owners: x.Owners, Recent: x.Recent, UpdatedAt: x.UpdatedAt,
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
	x.mu.Unlock()

	var (
		events   []Event
		deltas   []delta
		scanned  int
		nextF    = map[string]FileState{}
		nextG    = map[string]SessionState{}
		emitE    = func(e Event) { events = append(events, e) }
		emitD    = func(d delta) { deltas = append(deltas, d) }
		keepSess = func(p string, s SessionState) { nextG[p] = s }
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
			end, err := scanTranscript(ctx, p, acct.Name, off, since, emitE)
			if err != nil && ctx.Err() != nil {
				return err
			}
			nextF[p] = FileState{Size: fi.Size(), ModNS: fi.ModTime().UnixNano(), Offset: end}
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
	for _, e := range events {
		x.add(e)
	}
	for _, d := range deltas {
		x.addDelta(d)
	}
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

// addDelta folds one graft counter movement in. Caller holds the lock.
func (x *Index) addDelta(d delta) {
	rd := x.repoDay(DayKey(d.At), d.Repo)
	rd.GraftReads += d.Reads
	rd.SourceReads += d.Source
	rd.Nudges += d.Nudges
	rd.SavedTokens += d.Saved
	rd.CostMicros += d.Cost
	rd.BilledTokens += d.Billed
	if d.Session != "" && !contains(rd.Sessions, d.Session) {
		rd.Sessions = append(rd.Sessions, d.Session)
	}
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
	live := map[string]bool{}
	for _, d := range x.Days {
		for _, rd := range d.Repos {
			for _, s := range rd.Sessions {
				live[s] = true
			}
		}
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
