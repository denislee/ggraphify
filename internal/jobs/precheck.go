package jobs

import (
	"errors"
	"path/filepath"

	"github.com/dns/ggraphify/internal/gfy"
	"github.com/dns/ggraphify/internal/graphstate"
)

// RequireGraph is the default Precheck: a command that reads graph.json is
// refused when the output directory does not hold one.
//
// It lives here rather than in the GUI because the GUI is not the only caller
// that can submit a job — `ggraphify-job` runs the same kinds headlessly, from
// a cron line or a CI step, where the refusal matters more rather than less.
//
// The check is against the filesystem at submission time, not against a board
// scan: a build that finished a moment ago must not leave its own repository
// looking ungraphable, and a graph deleted by hand must not look present.
func RequireGraph(j *Job) error {
	if j == nil || !gfy.NeedsGraph(j.Kind) || graphstate.HasGraph(j.Out) {
		return nil
	}
	where := "its graphify-out directory"
	if j.Out != "" {
		where = filepath.Join(j.Out, "graph.json")
	}
	// graphify's own message for this case — "no graph found at …/graph.json —
	// run /graphify first" — arrives only after the job has started, and for a
	// metered command only after a confirm dialog has named a bill. This is the
	// same verdict, delivered before anything is spent.
	return errors.New(gfy.Title(j.Kind) + " needs a graph: " + where +
		" does not exist yet — run Extract (or Update, which is free) first")
}
