package graphstate

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ErrNoStamp is Restamp's answer for a graph.json with no top-level
// built_at_commit to advance. Old graphify versions kept the commit inside the
// "graph" object instead, and one that never recorded a commit at all is never
// Behind in the first place — so this is a "nothing to do here", not a defect.
var ErrNoStamp = errors.New("graphstate: graph.json carries no top-level built_at_commit")

// Restamp advances graph.json's built_at_commit to head, and nothing else.
//
// It exists because `graphify update` cannot do it. Both of update's
// no-change exits — same topology, and same graph plus same report — return
// having left graph.json untouched on purpose, and the comparison that decides
// they are "the same" pops built_at_commit out of both sides before comparing.
// So a commit of files graphify had already extracted produces an update that
// succeeds, changes nothing, and leaves the stamp on the old commit: the row
// stays `behind` forever, Fix prescribes the same update every time, and the
// auto-fix loop spends its whole attempt budget before declaring the
// repository stuck. This is the one write that clears it.
//
// It is only honest to call after a successful rescan that found no drift:
// graphify has then just asserted that the graph it holds is what this tree at
// this commit produces, which makes the stamp the only stale thing in the
// directory. Calling it on a graph that genuinely lags the tree would launder
// an out-of-date graph into a fresh-looking row, which is the exact failure
// the behind check exists to catch.
//
// The rewrite is a byte splice — the stamp's value range is located with a
// streaming decoder and the file is copied around it — so a 200 MB graph.json
// costs one pass and no allocation of its node array, and a graph too large to
// parse is restamped as readily as a small one.
func Restamp(out, head string) (bool, error) {
	if out == "" || head == "" {
		return false, nil
	}
	if !hexSHA(head) {
		// Never interpolated unchecked: this value is written straight into a
		// JSON string, and the only thing that may reach it is a commit id.
		return false, errors.New("graphstate: not a commit id: " + head)
	}

	path := filepath.Join(out, "graph.json")
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()

	start, end, cur, err := locateStamp(f)
	if err != nil {
		return false, err
	}
	if sameCommit(cur, head) {
		return false, nil
	}

	fi, err := f.Stat()
	if err != nil {
		return false, err
	}
	tmp, err := os.CreateTemp(out, ".graph.stamp-*")
	if err != nil {
		return false, err
	}
	defer func() {
		tmp.Close()
		os.Remove(tmp.Name()) // no-op once the rename has taken it
	}()

	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return false, err
	}
	if _, err := io.CopyN(tmp, f, start); err != nil {
		return false, err
	}
	if _, err := io.WriteString(tmp, `: "`+head+`"`); err != nil {
		return false, err
	}
	if _, err := f.Seek(end, io.SeekStart); err != nil {
		return false, err
	}
	if _, err := io.Copy(tmp, f); err != nil {
		return false, err
	}
	if err := tmp.Sync(); err != nil {
		return false, err
	}
	if err := tmp.Chmod(fi.Mode().Perm()); err != nil {
		return false, err
	}
	if err := tmp.Close(); err != nil {
		return false, err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return false, err
	}

	// Best effort, and deliberately not fatal: the report is a rendering of
	// the graph, and a stale line in it is worth fixing but not worth failing
	// a write that already succeeded.
	restampReport(out, head)
	return true, nil
}

// locateStamp finds the byte range of the top-level built_at_commit's value,
// and its current contents.
//
// start is the offset just past the key — the range it returns therefore spans
// the colon and the value, which is why the replacement writes both back. The
// walk stops at the key, so on graphify's own layout (the stamp is the last
// top-level member) it costs one pass and on any other it costs less.
func locateStamp(r io.Reader) (start, end int64, cur string, err error) {
	dec := json.NewDecoder(r)
	tok, err := dec.Token()
	if err != nil {
		return 0, 0, "", err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return 0, 0, "", errors.New("graphstate: graph.json is not a JSON object")
	}
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return 0, 0, "", err
		}
		key, _ := keyTok.(string)
		if key == "built_at_commit" {
			start = dec.InputOffset()
			var v string
			if err := dec.Decode(&v); err != nil {
				return 0, 0, "", err
			}
			return start, dec.InputOffset(), v, nil
		}
		if err := skipValue(dec); err != nil {
			return 0, 0, "", err
		}
	}
	return 0, 0, "", ErrNoStamp
}

// reportCommitPrefix is how graphify's report names the commit it was built
// from. Matching on the whole prefix rather than a regexp keeps this a line
// replacement and not a parser.
const reportCommitPrefix = "- Built from commit: "

// restampReport rewrites GRAPH_REPORT.md's provenance line, so the file agents
// are pointed at does not contradict the graph beside it.
func restampReport(out, head string) {
	path := filepath.Join(out, "GRAPH_REPORT.md")
	fi, err := os.Stat(path)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > MaxReportBytes {
		return
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	lines := strings.Split(string(b), "\n")
	changed := false
	for i, ln := range lines {
		if strings.HasPrefix(ln, reportCommitPrefix) {
			// graphify writes the first eight characters, in backticks.
			lines[i] = reportCommitPrefix + "`" + shortCommit(head) + "`"
			changed = true
		}
	}
	if !changed {
		return
	}
	_ = os.WriteFile(path, []byte(strings.Join(lines, "\n")), fi.Mode().Perm())
}

// MaxReportBytes is the ceiling on a GRAPH_REPORT.md this package will read
// into memory to fix one line of. A report larger than this is not a report.
const MaxReportBytes = 32 << 20

// shortCommit is the eight-character form graphify's report renders.
func shortCommit(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}

// hexSHA reports whether s is plausibly a git object id.
func hexSHA(s string) bool {
	if len(s) < 7 || len(s) > 64 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'f', r >= 'A' && r <= 'F':
		default:
			return false
		}
	}
	return true
}

// NeedsUpdateFlag reads graphify's own re-extraction flag straight from an
// output directory, for callers that hold no Graph — the post-job path, which
// has to decide whether a rescan settled everything before it touches the
// commit stamp.
func NeedsUpdateFlag(out string) bool {
	return out != "" && exists(filepath.Join(out, "needs_update"))
}
