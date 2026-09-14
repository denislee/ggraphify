package usage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// graft keeps a counter file per agent session inside the repository it was
// used on: graft/.cache/session/<session-id>.json, maintained by its hooks.
// It is the only place on the machine where the interesting ratio lives — how
// many reads went through the index versus straight to the source files — and
// it carries what the session was billed, which no transcript states.
//
// The file is cumulative for the life of the session, so it is read as a
// counter and not as an event: what a scan records is the delta since the last
// scan, attributed to the day the file was last written. A session left open
// across midnight therefore lands its morning work on the morning's day, which
// is the answer a daily breakdown should give.
const (
	graftDir     = "graft"
	graftCache   = ".cache"
	graftSessDir = "session"
)

// SessionState is the previous reading of one graft session file.
type SessionState struct {
	Size   int64 `json:"size"`
	ModNS  int64 `json:"mod_ns"`
	Reads  int   `json:"reads"`
	Source int   `json:"source"`
	Nudges int   `json:"nudges"`
	Saved  int64 `json:"saved"`
	Cost   int64 `json:"cost_micros"`
	Billed int64 `json:"billed"`
}

// graftSession is the file's shape. Fields graft added and this package does
// not use are ignored by the decoder, including lastQuery — the prompt text,
// which is deliberately never read into this process.
type graftSession struct {
	GraftReads        int   `json:"graftReads"`
	SourceReads       int   `json:"sourceReads"`
	SavedTokens       int64 `json:"savedTokens"`
	Nudges            int   `json:"nudges"`
	InputCostMicros   int64 `json:"inputCostMicros"`
	InputTokensBilled int64 `json:"inputTokensBilled"`
}

// delta is one scan's worth of movement in a graft session file.
type delta struct {
	Repo    string
	Session string
	At      time.Time
	Reads   int
	Source  int
	Nudges  int
	Saved   int64
	Cost    int64
	Billed  int64
}

// empty reports a file that has not moved in any way worth recording.
func (d delta) empty() bool {
	return d.Reads == 0 && d.Source == 0 && d.Nudges == 0 &&
		d.Saved == 0 && d.Cost == 0 && d.Billed == 0
}

// GraftSessionDir is where a checkout's graft session counters live.
func GraftSessionDir(repo string) string {
	return filepath.Join(repo, graftDir, graftCache, graftSessDir)
}

// scanGraftSessions reads one repository's session counters, emitting the
// movement since the state each file was last seen in.
//
// A counter that went DOWN — graft reset the file, or the session id was
// reused — is taken as a fresh start rather than as a negative delta: the
// whole current value is emitted and the state is rebased. Subtracting into
// negatives would silently eat a day's real usage.
func scanGraftSessions(repo string, prev map[string]SessionState, since time.Time, emit func(delta), keep func(string, SessionState)) {
	dir := GraftSessionDir(repo)
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		fi, err := e.Info()
		if err != nil {
			continue
		}
		st, seen := prev[path]
		if seen && st.Size == fi.Size() && st.ModNS == fi.ModTime().UnixNano() {
			keep(path, st)
			continue
		}
		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var s graftSession
		if err := json.Unmarshal(b, &s); err != nil {
			continue
		}
		next := SessionState{
			Size:   fi.Size(),
			ModNS:  fi.ModTime().UnixNano(),
			Reads:  s.GraftReads,
			Source: s.SourceReads,
			Nudges: s.Nudges,
			Saved:  s.SavedTokens,
			Cost:   s.InputCostMicros,
			Billed: s.InputTokensBilled,
		}
		keep(path, next)
		if fi.ModTime().Before(since) {
			continue
		}
		d := delta{
			Repo:    repo,
			Session: strings.TrimSuffix(e.Name(), ".json"),
			At:      fi.ModTime(),
			Reads:   sub(next.Reads, st.Reads),
			Source:  sub(next.Source, st.Source),
			Nudges:  sub(next.Nudges, st.Nudges),
			Saved:   sub64(next.Saved, st.Saved),
			Cost:    sub64(next.Cost, st.Cost),
			Billed:  sub64(next.Billed, st.Billed),
		}
		if !d.empty() {
			emit(d)
		}
	}
}

func sub(now, was int) int {
	if now < was {
		return now
	}
	return now - was
}

func sub64(now, was int64) int64 {
	if now < was {
		return now
	}
	return now - was
}
