// Package board joins discovery and graph state into the rows the GUI renders,
// headlessly.
//
// It exists so that the interesting half of ggraphify can be run, tested and
// debugged without a display: `ggraphify-scan` prints exactly what the board
// would show, as JSON. Nothing here imports GTK.
package board

import (
	"runtime"
	"sort"
	"sync"
	"time"

	"github.com/dns/ggraphify/internal/discover"
	"github.com/dns/ggraphify/internal/graftstate"
	"github.com/dns/ggraphify/internal/graphstate"
)

// Row is one line of the board: a checkout and what is known about its graph.
type Row struct {
	discover.Repo
	Graph graphstate.Graph `json:"graph"`
	// Graft is the state of the *other* index a checkout can carry: graft's
	// wiring graph under graft/. It is derived the same way and shown beside
	// the graphify one, because "which of my repositories does an agent
	// actually have a map of" is one question, not two.
	Graft graftstate.Index `json:"graft"`

	// Behind is true when the graph was built at a commit that is not HEAD.
	// It is a different question from drift — a branch switch moves it without
	// touching a single file's mtime — and the Branch column shows both.
	Behind bool `json:"behind"`

	// Excluded mirrors the per-repo "keep out of batch actions" override, so
	// a batch can be assembled from rows alone.
	Excluded bool `json:"excluded"`
	Pinned   bool `json:"pinned"`
}

// Key is the row's stable identity: the checkout path. Names collide, output
// directories can be shared, paths do not.
func (r Row) Key() string { return r.Path }

// Options configures a scan.
type Options struct {
	Roots []string
	Depth int
	// OutName and OutBase say where each repository's graphify knowledge
	// lives: a directory name inside every checkout, or one central directory
	// holding a subdirectory per checkout. Both blank is graphify's own
	// default, "graphify-out" in-tree.
	OutName string
	OutBase string
	// ShowHidden boards the checkouts inside dot-named directories too.
	ShowHidden bool
	// SkipDrift omits the per-repo tree walk. The walk is the expensive half
	// of a refresh and a caller that only wants counters can skip it.
	SkipDrift bool
	// Workers bounds the concurrent graph reads. Zero means NumCPU, capped at
	// 8: the work is IO-bound on a page cache that is usually warm, and more
	// goroutines past that only add contention.
	Workers int
	// Override is consulted per repository for the exclude/pin flags and for a
	// redirected output directory. It may be nil.
	Override func(path string) (out string, excluded, pinned bool)
	// Baseline is consulted for the drift a previous successful graphify run
	// already adjudicated, so the board does not report the same files graphify
	// declined to graph as drift on every tick. It may be nil, which is the raw
	// walk — what `ggraphify-scan` shows.
	Baseline func(path string) graphstate.Baseline
	// Cache, when non-nil, memoises the discovery walk between ticks.
	Cache *discover.Cache
	// Graphs, when non-nil, memoises the per-repository derivation on the
	// identity of the files it came from. This is what makes a refresh tick
	// over a hundred repositories nearly free in the steady state.
	Graphs *graphstate.Cache
	// Grafts is the same memo for the graft index. Separate from Graphs
	// because the two are invalidated by different runs: a `graphify update`
	// says nothing about graft/, and a `graft build` says nothing about
	// graphify-out/.
	Grafts *graftstate.Cache
}

// Scan walks the roots and derives every row.
//
// The graph reads fan out across a bounded worker pool: 130 repositories each
// needing a streaming parse of a multi-megabyte graph.json and a tree walk is
// several hundred milliseconds serially, and the refresh tick has to stay well
// inside its 30-second budget even on a cold page cache.
func Scan(opts Options) ([]Row, error) {
	dopts := discover.Options{
		Roots:   opts.Roots,
		Depth:   opts.Depth,
		OutName: opts.OutName,
		OutBase: opts.OutBase,

		ShowHidden: opts.ShowHidden,
	}
	if len(dopts.Roots) == 0 {
		dopts.Roots = discover.DefaultRoots()
	}
	var (
		repos []discover.Repo
		err   error
	)
	if opts.Cache != nil {
		repos, err = opts.Cache.Walk(dopts)
	} else {
		repos, err = discover.Walk(dopts)
	}
	if err != nil {
		return nil, err
	}

	workers := opts.Workers
	if workers <= 0 {
		workers = runtime.NumCPU()
	}
	if workers > 8 {
		workers = 8
	}
	if workers > len(repos) {
		workers = len(repos)
	}

	rows := make([]Row, len(repos))
	var wg sync.WaitGroup
	ch := make(chan int)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range ch {
				rows[i] = derive(repos[i], opts)
			}
		}()
	}
	for i := range repos {
		ch <- i
	}
	close(ch)
	wg.Wait()

	// A deterministic order, so a refresh that found nothing new produces an
	// identical slice and the UI's "did anything move?" comparison is honest.
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Pinned != rows[j].Pinned {
			return rows[i].Pinned
		}
		if rows[i].Name != rows[j].Name {
			return rows[i].Name < rows[j].Name
		}
		return rows[i].Path < rows[j].Path
	})
	return rows, nil
}

func derive(repo discover.Repo, opts Options) Row {
	r := Row{Repo: repo}
	if opts.Override != nil {
		out, excluded, pinned := opts.Override(repo.Path)
		r.Excluded, r.Pinned = excluded, pinned
		if out != "" {
			// A per-repository override outranks the board-wide location, and
			// is resolved the same way a job would resolve it — relative to
			// the checkout unless it is absolute.
			r.Out = discover.OutSpec{Repo: out}.For(repo.Path)
		}
	}
	gopts := graphstate.Options{
		Repo:      repo.Path,
		Out:       r.Out,
		Head:      repo.HeadSHA,
		SkipDrift: opts.SkipDrift,
	}
	if opts.Baseline != nil {
		gopts.Baseline = opts.Baseline(repo.Path)
	}
	var (
		g   graphstate.Graph
		err error
	)
	if opts.Graphs != nil {
		g, err = opts.Graphs.Read(gopts)
	} else {
		g, err = graphstate.Read(gopts)
	}
	if err != nil {
		g.State = graphstate.StateBroken
		g.Err = err.Error()
	}
	r.Graph = g

	gropts := graftstate.Options{Repo: repo.Path, SkipDrift: opts.SkipDrift}
	var gr graftstate.Index
	if opts.Grafts != nil {
		gr, err = opts.Grafts.Read(gropts)
	} else {
		gr, err = graftstate.Read(gropts)
	}
	if err != nil {
		gr.State = graftstate.StateBroken
		gr.Err = err.Error()
	}
	r.Graft = gr

	r.Behind = g.Behind()
	return r
}

// Counts is the status-bar summary.
type Counts struct {
	Repos   int `json:"repos"`
	Graphed int `json:"graphed"`
	Fresh   int `json:"fresh"`
	Stale   int `json:"stale"`
	Raw     int `json:"raw"`
	Broken  int `json:"broken"`
	None    int `json:"none"`
	Behind  int `json:"behind"`
	// Grafted is how many checkouts carry a readable graft index, and
	// GraftStale how many of those the tree has moved under. They are counted
	// here rather than in the UI so `ggraphify-scan` reports them too.
	Grafted    int `json:"grafted"`
	GraftStale int `json:"graft_stale"`
}

// Summarize counts the rows by state.
func Summarize(rows []Row) Counts {
	var c Counts
	c.Repos = len(rows)
	for _, r := range rows {
		if r.Behind {
			c.Behind++
		}
		switch r.Graft.State {
		case graftstate.StateFresh, graftstate.StateRaw:
			c.Grafted++
		case graftstate.StateStale:
			c.Grafted++
			c.GraftStale++
		}
		switch r.Graph.State {
		case graphstate.StateFresh:
			c.Fresh++
			c.Graphed++
		case graphstate.StateStale:
			c.Stale++
			c.Graphed++
		case graphstate.StateRaw:
			c.Raw++
			c.Graphed++
		case graphstate.StateBroken:
			c.Broken++
		default:
			c.None++
		}
	}
	return c
}

// Age is a compact relative time for the Built column: "3d", "2h", "now".
// Absolute timestamps are in the tooltip; the column is scanned, not read.
func Age(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := time.Since(t)
	switch {
	case d < 0:
		return "now"
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return itoa(int(d.Minutes())) + "m"
	case d < 24*time.Hour:
		return itoa(int(d.Hours())) + "h"
	case d < 365*24*time.Hour:
		return itoa(int(d.Hours()/24)) + "d"
	}
	return itoa(int(d.Hours()/24/365)) + "y"
}

// Bytes renders a size the way a column should: three significant figures at
// most, no decimal point below a kilobyte.
func Bytes(n int64) string {
	if n <= 0 {
		return ""
	}
	const unit = 1024
	if n < unit {
		return itoa(int(n)) + " B"
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit && exp < 4; m /= unit {
		div *= unit
		exp++
	}
	whole := n / div
	frac := (n % div) * 10 / div
	s := itoa(int(whole))
	if whole < 10 {
		s += "." + itoa(int(frac))
	}
	return s + " " + [...]string{"KB", "MB", "GB", "TB", "PB"}[exp]
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var d [20]byte
	i := len(d)
	for n > 0 {
		i--
		d[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		d[i] = '-'
	}
	return string(d[i:])
}
