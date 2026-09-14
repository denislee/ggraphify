package ui

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/diamondburned/gotk4/pkg/gtk/v4"

	"github.com/dns/ggraphify/internal/jobs"
)

// Progress is read out of a job's own stdout rather than reported by the
// runner, because the runner starts a subprocess and has no idea what that
// subprocess is doing. graphify narrates itself — "extract  42%", "[3/17]
// communities" — and the tail of the log is therefore the only honest source
// of a completion fraction there is.
//
// Nothing depends on the parse succeeding. A job whose output says nothing
// about how far along it is still gets a bar; it pulses instead of filling,
// which says "moving, length unknown" rather than inventing a number.

// progressLines is how far back up the log to look. A percentage is normally
// on the last line, but a chatty step will have logged a line or two under it.
const progressLines = 12

var (
	// "42%", "42.5 %"
	reProgressPct = regexp.MustCompile(`(\d{1,3}(?:\.\d+)?)\s*%`)
	// "[3/17]", "(3/17)", "3 / 17"
	reProgressFrac = regexp.MustCompile(`\b(\d+)\s*/\s*(\d+)\b`)
	// "step 3 of 17", "file 3 of 17"
	reProgressOf = regexp.MustCompile(`(?i)\b(\d+)\s+of\s+(\d+)\b`)
)

// jobProgress returns how far along a job is, as a fraction in [0,1] and as
// the short text the row shows beside it. ok is false when the log says
// nothing a fraction can be read out of — the caller pulses in that case.
func jobProgress(s *jobs.Snapshot) (frac float64, text string, ok bool) {
	if s == nil || s.Log == nil || s.Status != jobs.Running {
		return 0, "", false
	}
	lines := strings.Split(s.Log.Tail(progressLines), "\n")
	// Newest first: the most recent line that carries a number wins, so a
	// stale figure further up the log cannot outrank a fresh one.
	for i := len(lines) - 1; i >= 0; i-- {
		if f, t, found := parseProgress(lines[i]); found {
			return f, t, true
		}
	}
	return 0, "", false
}

// parseProgress reads a fraction out of one line of output. A percentage wins
// over a ratio: a line that carries both ("[3/17] 18%") is reporting the same
// thing twice and the percentage is the one already rounded for a human.
func parseProgress(line string) (frac float64, text string, ok bool) {
	if m := reProgressPct.FindStringSubmatch(line); m != nil {
		if p, err := strconv.ParseFloat(m[1], 64); err == nil && p >= 0 && p <= 100 {
			return p / 100, strconv.Itoa(int(p)) + "%", true
		}
	}
	// Every match on the line, not just the first: a line that opens with a
	// timestamp ("2026/09/13 [3/17] …") would otherwise be rejected on the
	// date and never reach the ratio that follows it.
	for _, re := range []*regexp.Regexp{reProgressFrac, reProgressOf} {
		for _, m := range re.FindAllStringSubmatch(line, -1) {
			done, err1 := strconv.Atoi(m[1])
			total, err2 := strconv.Atoi(m[2])
			// total == 0 is a division; done > total is a version string, a
			// date or a path that happens to look like a ratio.
			if err1 == nil && err2 == nil && total > 0 && done >= 0 && done <= total {
				return float64(done) / float64(total), m[1] + "/" + m[2], true
			}
		}
	}
	return 0, "", false
}

// progressNote is the fragment a status line adds for a job that reports its
// own progress, and "" for one that does not.
func progressNote(s *jobs.Snapshot) string {
	if _, text, ok := jobProgress(s); ok {
		return text
	}
	return ""
}

// --- the widget --------------------------------------------------------------

// jobProgressWidth is the bar's width in pixels. It is a glance at how far
// along a job is, not a measurement, so it stays narrow enough that the label
// beside it keeps the row's width.
const jobProgressWidth = 90

// newJobProgressBar builds the bar itself. It is created for every row,
// running or not, and hidden on the rows that have nothing to report, so a
// job that starts running does not have to rebuild the row to gain one.
func newJobProgressBar() *gtk.ProgressBar {
	p := gtk.NewProgressBar()
	p.SetVAlign(gtk.AlignCenter)
	p.SetHExpand(false)
	p.SetSizeRequest(jobProgressWidth, -1)
	p.SetPulseStep(0.12)
	p.AddCSSClass("jobprogress")
	p.SetVisible(false)
	return p
}

// progressSlot wraps the bar in a fixed-width box so the columns to its right
// sit at the same place whether or not the row's job is running — a hidden
// widget takes no space in a GTK box, and the status text would otherwise
// slide sideways the moment a job finished.
func progressSlot(p *gtk.ProgressBar) *gtk.Box {
	slot := gtk.NewBox(gtk.OrientationHorizontal, 0)
	slot.SetSizeRequest(jobProgressWidth, -1)
	slot.SetVAlign(gtk.AlignCenter)
	slot.Append(p)
	return slot
}

// setJobProgress repaints one bar: filled to the fraction the job reported,
// pulsing when it is running but silent about how far along it is, and hidden
// when the job is not running at all.
func setJobProgress(p *gtk.ProgressBar, s *jobs.Snapshot) {
	if p == nil {
		return
	}
	if s == nil || s.Status != jobs.Running {
		p.SetVisible(false)
		return
	}
	p.SetVisible(true)
	if frac, text, ok := jobProgress(s); ok {
		p.SetFraction(frac)
		p.SetTooltipText(text + " complete, by the job's own account")
		return
	}
	// Nothing to read: move the bar without claiming a number.
	p.Pulse()
	p.SetTooltipText("running — this job does not report a percentage")
}
