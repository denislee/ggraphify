// Package store is ggraphify's sidecar state: settings, per-repo overrides,
// window geometry and the job list — the queue and a bounded run history —
// under $XDG_DATA_HOME/ggraphify/.
//
// The rule this package exists to enforce is that ggraphify never writes
// inside a repository. Everything it remembers about a checkout lives here,
// keyed by absolute path; the only writes into a repository are graphify's
// own, and only ever from a job the user confirmed.
//
// Nothing here is secret. A backend is stored by name and an environment
// overlay by variable name; an API key is read from the process environment at
// job time and is never written to this file.
package store

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/dns/ggraphify/internal/discover"
	"github.com/dns/ggraphify/internal/gfy"
	"github.com/dns/ggraphify/internal/graphstate"
)

// Version is the state file's schema version. Readers accept anything at or
// below their own and fill in defaults for keys added later, so a board rolled
// back after an upgrade does not lose the whole file over one unknown field.
const Version = 1

// SaveDebounce is how long a change waits before it is written. The board
// touches settings on every column drag and every sort click; writing the file
// on each one would be a syscall storm for no benefit.
const SaveDebounce = 750 * time.Millisecond

// Settings is everything the preferences dialog edits.
type Settings struct {
	Roots   []string `json:"roots"`
	Depth   int      `json:"depth"`
	Refresh int      `json:"refresh_seconds"`
	// ShowHidden boards checkouts that live inside a dot-named directory
	// below a root. Off by default, which is why the zero value is the one
	// that hides them: a state file written before this setting existed reads
	// back as the default rather than as a choice nobody made.
	ShowHidden bool   `json:"show_hidden"`
	Scheme     string `json:"scheme"` // system | light | dark

	Backend string `json:"backend"`
	Model   string `json:"model"`

	// ClaudeAccount is the Claude Code configuration directory jobs run
	// against — one of this machine's ~/.claude* logins. Blank means the
	// inherited environment decides, which is the behaviour every state file
	// written before this setting existed reads back as.
	ClaudeAccount string `json:"claude_account,omitempty"`

	// The ollama lifecycle: the board starts the local model server when a
	// job needs it and stops it again once nothing does, so a machine that
	// uses a local model occasionally does not keep several gigabytes of
	// weights resident between sweeps.
	//
	// Stored as an *off* flag, so the zero value — a state file written
	// before this existed — reads back as the default rather than as a choice
	// nobody made, and the default is on. It is safe as a default because the
	// board only ever stops a server IT started: an ollama that was already
	// running when the first job asked for it is left alone forever.
	NoAutoOllama bool `json:"no_auto_ollama,omitempty"`
	// OllamaIdleStop is how many seconds of no local job pass before a server
	// the board started is stopped. Zero means DefaultOllamaIdleStop.
	//
	// It is a grace period rather than an immediate stop because the cost of
	// stopping too eagerly is paid twice: a cold start on the next job, and a
	// model loaded from disk again. A sweep of many repositories is a run of
	// short jobs with gaps between them, and every gap shorter than this one
	// is free.
	OllamaIdleStop int `json:"ollama_idle_stop_seconds,omitempty"`

	// OutName is the directory name graphify writes its knowledge into inside
	// each checkout. Blank means $GRAPHIFY_OUT_NAME, then "graphify-out".
	OutName string `json:"out_name"`
	// OutBase collects every repository's graph under one directory instead,
	// a subdirectory per checkout. Blank keeps them in-tree. Setting it moves
	// where the board *looks*; it does not move graphs that already exist.
	OutBase string `json:"out_base"`

	FreeLanes    int `json:"free_lanes"`
	MeteredLanes int `json:"metered_lanes"`
	// LocalLanes is the metered lane's counterpart for a model on this
	// machine, where the limit is cores and memory rather than spend.
	LocalLanes int `json:"local_lanes"`

	// ConfirmBatchAt is how many repositories a batch may cover before the
	// confirm dialog demands the count be typed rather than merely clicked.
	ConfirmBatchAt int `json:"confirm_batch_at"`

	// The auto-fix loop: the board takes unhealthy repositories to a healthy
	// graph on its own, on the same tick that scans them, running the same
	// plan the Fix button would have run.
	//
	// The two switches that matter are stored as *off* flags, so the zero
	// value — a state file written before auto-fix existed — reads back as
	// the default rather than as a choice nobody made, and the default for
	// both is on. AutoFixMetered is the other way round for the same reason:
	// its safe value is its zero one, and a loop that bills a card without a
	// click is the one thing here nobody could undo.
	NoAutoFix      bool `json:"no_auto_fix,omitempty"`
	NoAutoFixLocal bool `json:"no_auto_fix_local,omitempty"`
	AutoFixMetered bool `json:"auto_fix_metered,omitempty"`

	// AutoFixMax is how many repositories the loop keeps in flight,
	// AutoFixCooldown the minimum seconds before it attempts the same one
	// again, and AutoFixAttempts how many times the same set of defects may
	// be attacked before the repository is left alone. Zero means the
	// autofix package's own default for each.
	AutoFixMax      int `json:"auto_fix_max,omitempty"`
	AutoFixCooldown int `json:"auto_fix_cooldown_seconds,omitempty"`
	AutoFixAttempts int `json:"auto_fix_attempts,omitempty"`

	// Overlay is the GRAPHIFY_* environment applied to every job.
	Overlay map[string]string `json:"overlay"`

	Terminal string `json:"terminal"`
	Editor   string `json:"editor"`

	LogBytes int `json:"log_bytes"`

	// The bottom panel: the jobs strip and the application log. Both are
	// stored as *hide* flags so the zero value — a state file written before
	// the panel existed, or one where nobody ever touched it — reads back as
	// "shown", which is the default the panel is meant to have.
	HideJobsBar bool `json:"hide_jobs_bar"`
	HideLogBar  bool `json:"hide_log_bar"`
	// HideBottom hides the panel as a whole without forgetting which of its
	// pages were switched on, which is what the `b` key toggles.
	HideBottom bool `json:"hide_bottom"`
	// BottomHeight is the panel's height in pixels, and BottomPage which of
	// its two pages was last on top.
	BottomHeight int    `json:"bottom_height"`
	BottomPage   string `json:"bottom_page"`

	// Filter and Sort are board state rather than preferences, but they live
	// in the same file because they are restored at the same moment.
	Filter string `json:"filter"` // a graphstate.State name, or "" for all
	// Group is the folder the board is narrowed to — an absolute directory
	// path, or "" for every folder.
	Group    string `json:"group"`
	Search   string `json:"search"`
	SortCol  string `json:"sort_col"`
	SortDesc bool   `json:"sort_desc"`
}

// AutoFix reports whether the auto-fix loop runs at all, and AutoFixLocal
// whether it may use a model on this machine for the LLM steps. Both are
// stored inverted so that "not written down" means "on"; these two readers are
// the only place that inversion is spelled out.
func (s Settings) AutoFix() bool { return !s.NoAutoFix }

// AutoOllama reports whether the board manages the local model server's
// lifecycle, and OllamaIdle how long it waits before stopping one it started.
// AutoOllama is stored inverted for the same reason AutoFix is: not written
// down has to mean on.
func (s Settings) AutoOllama() bool { return !s.NoAutoOllama }

// OllamaIdle is always a positive duration, so no caller has to decide what a
// zero or negative stored value meant.
func (s Settings) OllamaIdle() time.Duration {
	if s.OllamaIdleStop <= 0 {
		return DefaultOllamaIdleStop
	}
	return time.Duration(s.OllamaIdleStop) * time.Second
}

// DefaultOllamaIdleStop is the idle grace period before an auto-started
// server is stopped.
//
// Five minutes, because it has to be longer than the gap between two jobs in
// the same sweep — one repository's graphify process exiting and the next
// one's starting, with a scan tick in between — and shorter than the time
// somebody would notice weights sitting in RAM after they walked away. It is
// deliberately the same order as ollama's own 30-minute keep-alive is not:
// keep-alive decides when the SERVER unloads the model, this decides when the
// board stops the server, and the board can afford to be the more eager of
// the two because it knows whether any job still wants it.
const DefaultOllamaIdleStop = 5 * time.Minute

// AutoFixLocal is what makes the loop worth having on by default: against a
// model on this machine the full extraction and the community naming cost
// nothing, so the loop can finish a repository rather than stopping at the
// free half of it.
func (s Settings) AutoFixLocal() bool { return !s.NoAutoFixLocal }

// RepoOverride is the per-repository half of the settings: what this one
// checkout does differently from the board's defaults.
type RepoOverride struct {
	Backend string `json:"backend,omitempty"`
	Model   string `json:"model,omitempty"`
	// Out redirects graphify's output directory for this repository only,
	// through GRAPHIFY_OUT.
	Out string `json:"out,omitempty"`
	// Extra flags appended to every job for this repository — the escape hatch
	// for a flag this build does not know about.
	Extra []string `json:"extra,omitempty"`
	// ExcludeBatch keeps the repository out of every fan-out action. The
	// monorepo nobody wants to extract by accident lives behind this.
	ExcludeBatch bool `json:"exclude_batch,omitempty"`
	// Pinned floats the row to the top of the board.
	Pinned bool `json:"pinned,omitempty"`
}

// Empty reports whether an override says nothing, so the writer can drop the
// entry rather than accumulate one per repository ever selected.
func (o RepoOverride) Empty() bool {
	return o.Backend == "" && o.Model == "" && o.Out == "" &&
		len(o.Extra) == 0 && !o.ExcludeBatch && !o.Pinned
}

// Geometry is the window size the board reopens at.
type Geometry struct {
	Width     int  `json:"width"`
	Height    int  `json:"height"`
	Maximized bool `json:"maximized"`
}

// JobEntry is one job as it survives a restart — the queue and the run log in
// a single list, because from the sidecar's point of view they are the same
// thing seen at different moments, and a board that reopened its history but
// forgot its queue would be a board that quietly dropped work.
//
// The log travels with it, as a tail rather than the whole ring buffer. A
// restored failure whose log was thrown away is a row that says "failed" and
// nothing about why, which is exactly the row somebody reopens the board to
// read; MaxLogTail is what keeps that answer from turning the sidecar into a
// log file.
//
// Env is deliberately absent. It is rebuilt on restore from the overlay and
// the backend named in the argv, the same way the Retry button rebuilds it —
// so this file keeps holding variable names and never a credential's value,
// which is the rule the package doc states.
type JobEntry struct {
	Kind    string    `json:"kind"`
	Repo    string    `json:"repo"`
	Label   string    `json:"label,omitempty"`
	Argv    []string  `json:"argv"`
	Cost    string    `json:"cost"`
	Local   bool      `json:"local,omitempty"`
	Dir     string    `json:"dir,omitempty"`
	Out     string    `json:"out,omitempty"`
	Status  string    `json:"status"`
	Held    bool      `json:"held,omitempty"`
	Exit    int       `json:"exit"`
	Queued  time.Time `json:"queued,omitempty"`
	Started time.Time `json:"started"`
	Ended   time.Time `json:"ended,omitempty"`
	Error   string    `json:"error,omitempty"`
	Log     string    `json:"log,omitempty"`
}

// Done reports whether this entry describes a job the next session does NOT
// owe any work for. Only "queued" and "running" are outstanding; everything
// else, a status this version does not recognise included, is treated as
// finished. The asymmetry is deliberate — misreading a finished job as
// outstanding would run it a second time, and running a job twice is the one
// mistake this file exists to avoid.
func (e JobEntry) Done() bool {
	switch e.Status {
	case "queued", "running":
		return false
	}
	return true
}

// Duration is how long the job ran, in seconds.
func (e JobEntry) Duration() float64 {
	// Ended before Started covers both of the cases that are not a duration:
	// a job that never ran, and one that had not ended when this was written.
	if e.Ended.Before(e.Started) {
		return 0
	}
	return e.Ended.Sub(e.Started).Seconds()
}

// HistoryEntry is one finished job as the detail pane and the diagnostics page
// read it: a projection of JobEntry, kept because those two readers want the
// run log and nothing else from the list.
type HistoryEntry struct {
	Kind     string    `json:"kind"`
	Repo     string    `json:"repo"`
	Argv     []string  `json:"argv"`
	Cost     string    `json:"cost"`
	Status   string    `json:"status"`
	Exit     int       `json:"exit"`
	Started  time.Time `json:"started"`
	Duration float64   `json:"duration_seconds"`
	Error    string    `json:"error,omitempty"`
}

// MaxHistory bounds the finished jobs the sidecar keeps. Jobs that have not
// finished are never dropped by it: a queue is not a log, and trimming the
// oldest entry of a queue means losing work somebody asked for.
const MaxHistory = 200

// MaxLogTail is how much of a job's log is written to the sidecar. It is the
// end of the log, which is where a traceback and an exit message are.
const MaxLogTail = 8 << 10

type state struct {
	Version   int                     `json:"version"`
	Settings  Settings                `json:"settings"`
	Repos     map[string]RepoOverride `json:"repos"`
	Window    Geometry                `json:"window"`
	Columns   map[string]int          `json:"columns"` // id → width
	ColOrder  []string                `json:"col_order"`
	ColHidden []string                `json:"col_hidden"`
	Selected  string                  `json:"selected"`
	// Jobs is the queue and the run log, newest first. History is the shape
	// the same list had before jobs survived a restart; it is read once, on
	// load, and never written again.
	Jobs    []JobEntry     `json:"jobs,omitempty"`
	History []HistoryEntry `json:"history,omitempty"`
	// Baselines is the drift each repository's last successful graphify run
	// left behind — paths graphify has looked at and declined to graph. It
	// lives here rather than in graphify-out/ because this package's one rule
	// is that ggraphify never writes inside a repository, and a baseline is
	// the board's opinion, not graphify's output.
	Baselines map[string]graphstate.Baseline `json:"drift_baselines,omitempty"`
}

// Store is the sidecar, loaded once and saved on a debounce.
type Store struct {
	path string

	mu sync.RWMutex
	s  state

	// The command line's say in where graphify knowledge lives, held in
	// memory and never persisted: a flag is this launch's opinion, not a
	// preference the user set. See SetOutOverride for why it lives here
	// rather than in the UI.
	outName, outBase       string
	outNameSet, outBaseSet bool

	timer   *time.Timer
	saveErr error
}

// SetOutOverride records what the command line said about where graphify
// knowledge lives, so that OutSpec — and therefore every job's GRAPHIFY_OUT —
// resolves it the same way the board's scan does.
//
// It exists because the two used to disagree. The scan applied flag precedence
// in the UI while OutSpec read the stored settings alone, so `ggraphify
// -out-base /somewhere` made the board READ from /somewhere and every job it
// launched WRITE to the stored base — or, with nothing stored, into the
// checkout. A board that reads one directory and writes another reports every
// repository as ungraphed however many times it extracts them.
//
// The flags are not persisted. A launch flag must not quietly become a saved
// preference that outlives the launch.
func (s *Store) SetOutOverride(name string, nameSet bool, base string, baseSet bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.outName, s.outNameSet = name, nameSet
	s.outBase, s.outBaseSet = base, baseSet
}

// OutLocation is the one resolver for where knowledge lives: a stored
// preference outranks the built-in default, and an explicit flag outranks
// both. Everything that needs the answer — the scan, OutSpec, the job overlay,
// the diagnostics report — goes through here.
func (s *Store) OutLocation() (name, base string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	name, base = s.s.Settings.OutName, s.s.Settings.OutBase
	if s.outNameSet {
		name = s.outName
	}
	if s.outBaseSet {
		base = s.outBase
	}
	return name, base
}

// DefaultDir is $XDG_DATA_HOME/ggraphify, or ~/.local/share/ggraphify.
func DefaultDir() string {
	if v := os.Getenv("XDG_DATA_HOME"); v != "" {
		return filepath.Join(v, "ggraphify")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".ggraphify"
	}
	return filepath.Join(home, ".local", "share", "ggraphify")
}

// DefaultPath is the state file inside it.
func DefaultPath() string { return filepath.Join(DefaultDir(), "state.json") }

// Defaults are the settings a board with no sidecar starts from.
func Defaults() Settings {
	return Settings{
		Depth:          3,
		Refresh:        30,
		Scheme:         "system",
		FreeLanes:      0, // 0 → the runner's own min(4, NumCPU/2)
		MeteredLanes:   1,
		LocalLanes:     0, // 0 → the runner's own jobs.DefaultLocalLanes
		ConfirmBatchAt: 3,
		Overlay:        map[string]string{},
		LogBytes:       0,
		SortCol:        "state",
		BottomHeight:   220,
		BottomPage:     "jobs",
	}
}

// Open loads the state file, returning a usable Store even when the file is
// missing or corrupt — a board that refuses to start because its preferences
// file has a stray brace in it would be worse than one that starts fresh.
func Open(path string) *Store {
	st := &Store{path: path, s: state{Version: Version, Settings: Defaults(), Repos: map[string]RepoOverride{}, Columns: map[string]int{}}}
	b, err := os.ReadFile(path)
	if err != nil {
		return st
	}
	var loaded state
	if err := json.Unmarshal(b, &loaded); err != nil {
		return st
	}
	// Forward-compatible merge: whatever the file did not say keeps the
	// default that was already there.
	def := st.s.Settings
	if loaded.Settings.Depth == 0 {
		loaded.Settings.Depth = def.Depth
	}
	if loaded.Settings.Refresh == 0 {
		loaded.Settings.Refresh = def.Refresh
	}
	if loaded.Settings.Scheme == "" {
		loaded.Settings.Scheme = def.Scheme
	}
	if loaded.Settings.MeteredLanes <= 0 {
		loaded.Settings.MeteredLanes = def.MeteredLanes
	}
	if loaded.Settings.ConfirmBatchAt <= 0 {
		loaded.Settings.ConfirmBatchAt = def.ConfirmBatchAt
	}
	if loaded.Settings.BottomHeight <= 0 {
		loaded.Settings.BottomHeight = def.BottomHeight
	}
	if loaded.Settings.BottomPage == "" {
		loaded.Settings.BottomPage = def.BottomPage
	}
	if loaded.Settings.Overlay == nil {
		loaded.Settings.Overlay = map[string]string{}
	}
	if loaded.Repos == nil {
		loaded.Repos = map[string]RepoOverride{}
	}
	if loaded.Columns == nil {
		loaded.Columns = map[string]int{}
	}
	// A file written before jobs survived a restart carries its finished runs
	// under "history". Fold them into the one list and stop writing the old
	// key, so the two can never disagree about what ran.
	if len(loaded.Jobs) == 0 && len(loaded.History) > 0 {
		for _, h := range loaded.History {
			loaded.Jobs = append(loaded.Jobs, JobEntry{
				// A history entry never carried a label — the old list was
				// rendered from the kind and the argv. Rebuild it, or the
				// folded-in rows arrive on the board with no name at all.
				Kind: h.Kind, Repo: h.Repo, Label: gfy.JobLabel(h.Kind, h.Repo),
				Argv: h.Argv, Cost: h.Cost,
				Status: h.Status, Exit: h.Exit, Started: h.Started,
				Ended: h.Started.Add(time.Duration(h.Duration * float64(time.Second))),
				Error: h.Error,
			})
		}
	}
	loaded.History = nil
	loaded.Jobs = trimJobs(loaded.Jobs)
	loaded.Version = Version
	st.s = loaded
	return st
}

// Settings returns a copy. Callers mutate the copy and hand it back to
// SetSettings, so a half-applied edit is never visible to a reader.
func (s *Store) Settings() Settings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneSettings(s.s.Settings)
}

// SetSettings replaces the settings and schedules a save.
func (s *Store) SetSettings(v Settings) {
	s.mu.Lock()
	s.s.Settings = cloneSettings(v)
	s.mu.Unlock()
	s.schedule()
}

func cloneSettings(v Settings) Settings {
	c := v
	c.Roots = append([]string(nil), v.Roots...)
	c.Overlay = map[string]string{}
	for k, val := range v.Overlay {
		c.Overlay[k] = val
	}
	return c
}

// Override returns the per-repository settings for path, zero when there are
// none.
func (s *Store) Override(path string) RepoOverride {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.s.Repos[path]
}

// SetOverride stores one, or removes it when it says nothing.
func (s *Store) SetOverride(path string, o RepoOverride) {
	s.mu.Lock()
	if o.Empty() {
		delete(s.s.Repos, path)
	} else {
		s.s.Repos[path] = o
	}
	s.mu.Unlock()
	s.schedule()
}

// Overrides returns every stored override, for the settings dialog's list.
func (s *Store) Overrides() map[string]RepoOverride {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]RepoOverride, len(s.s.Repos))
	for k, v := range s.s.Repos {
		out[k] = v
	}
	return out
}

// Window and SetWindow carry the geometry across runs.
func (s *Store) Window() (Geometry, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	g := s.s.Window
	return g, g.Width > 0 && g.Height > 0
}

func (s *Store) SetWindow(g Geometry) {
	s.mu.Lock()
	s.s.Window = g
	s.mu.Unlock()
	s.schedule()
}

// Columns and SetColumnWidth remember what the user dragged.
func (s *Store) Columns() map[string]int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]int, len(s.s.Columns))
	for k, v := range s.s.Columns {
		out[k] = v
	}
	return out
}

func (s *Store) SetColumnWidth(id string, w int) {
	if w <= 0 {
		return
	}
	s.mu.Lock()
	if s.s.Columns[id] == w {
		s.mu.Unlock()
		return
	}
	s.s.Columns[id] = w
	s.mu.Unlock()
	s.schedule()
}

// Hidden is the set of columns switched off.
func (s *Store) Hidden() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]string(nil), s.s.ColHidden...)
}

func (s *Store) SetHidden(ids []string) {
	sort.Strings(ids)
	s.mu.Lock()
	s.s.ColHidden = append([]string(nil), ids...)
	s.mu.Unlock()
	s.schedule()
}

// Selected is the repository path the board reopens on.
func (s *Store) Selected() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.s.Selected
}

func (s *Store) SetSelected(p string) {
	s.mu.Lock()
	if s.s.Selected == p {
		s.mu.Unlock()
		return
	}
	s.s.Selected = p
	s.mu.Unlock()
	s.schedule()
}

// Jobs is everything the previous session knew about, newest first: what was
// queued, what was in flight, what had finished.
func (s *Store) Jobs() []JobEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]JobEntry(nil), s.s.Jobs...)
}

// SetJobs replaces the list. The runner owns the queue, so the board writes it
// whole on every transition rather than trying to keep a second copy of it in
// step one edit at a time.
func (s *Store) SetJobs(list []JobEntry) {
	s.mu.Lock()
	s.s.Jobs = trimJobs(list)
	s.mu.Unlock()
	s.schedule()
}

// trimJobs bounds the finished jobs at MaxHistory and keeps every unfinished
// one, wherever in the list it sits.
func trimJobs(list []JobEntry) []JobEntry {
	out := make([]JobEntry, 0, len(list))
	done := 0
	for _, e := range list {
		if e.Done() {
			if done >= MaxHistory {
				continue
			}
			done++
		}
		out = append(out, e)
	}
	return out
}

// History is the persisted job log, newest first — the finished entries of
// Jobs, in the shape the detail pane and the diagnostics page read.
func (s *Store) History() []HistoryEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]HistoryEntry, 0, len(s.s.Jobs))
	for _, e := range s.s.Jobs {
		if !e.Done() {
			continue
		}
		out = append(out, HistoryEntry{
			Kind: e.Kind, Repo: e.Repo, Argv: e.Argv, Cost: e.Cost,
			Status: e.Status, Exit: e.Exit, Started: e.Started,
			Duration: e.Duration(), Error: e.Error,
		})
	}
	return out
}

// DriftBaseline is the adjudicated drift for one repository, or a zero
// Baseline when none has been recorded.
func (s *Store) DriftBaseline(repo string) graphstate.Baseline {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.s.Baselines[repo]
}

// SetDriftBaseline records what a successful graphify run left undrifted.
//
// An empty baseline deletes the entry rather than storing a blank one: a
// repository whose drift is genuinely zero needs nothing remembered, and a
// sidecar that accumulates an entry per repository forever is a file that grows
// without ever being read.
func (s *Store) SetDriftBaseline(repo string, b graphstate.Baseline) {
	if repo == "" {
		return
	}
	s.mu.Lock()
	if len(b.Added) == 0 && len(b.Removed) == 0 {
		delete(s.s.Baselines, repo)
	} else {
		if s.s.Baselines == nil {
			s.s.Baselines = map[string]graphstate.Baseline{}
		}
		s.s.Baselines[repo] = b
	}
	s.mu.Unlock()
	s.schedule()
}

// Scheme is the colour-scheme preference.
func (s *Store) Scheme() string {
	v := s.Settings().Scheme
	if v == "" {
		return "system"
	}
	return v
}

// SetScheme persists it.
func (s *Store) SetScheme(v string) {
	st := s.Settings()
	st.Scheme = v
	s.SetSettings(st)
}

// OutSpec is where this store says a repository's graphify knowledge lives:
// the board-wide name and central base, plus that repository's own override.
// The board resolves rows through it and every job is launched pointing at the
// same directory.
func (s *Store) OutSpec(repo string) discover.OutSpec {
	name, base := s.OutLocation()
	return discover.OutSpec{
		Name: name,
		Base: base,
		Repo: s.Override(repo).Out,
	}
}

// Out is the resolved output directory for one repository.
func (s *Store) Out(repo string) string { return s.OutSpec(repo).For(repo) }

// Overlay is the composed GRAPHIFY_* environment for a repository: the
// board-wide overlay with the resolved output directory layered on top.
//
// GRAPHIFY_OUT is set whenever the location has been configured away from
// graphify's own default, so the subprocess writes exactly where the board
// reads. When nothing is configured it is left alone, and graphify's own
// defaults — including an inherited $GRAPHIFY_OUT — decide.
func (s *Store) Overlay(repo string) gfy.Env {
	set := s.Settings()
	e := gfy.Env{}
	for k, v := range set.Overlay {
		e[k] = v
	}
	spec := s.OutSpec(repo)
	if spec.Repo != "" || spec.Base != "" || spec.Name != "" {
		e["GRAPHIFY_OUT"] = spec.For(repo)
	}
	return e
}

// schedule debounces a save.
func (s *Store) schedule() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.timer != nil {
		s.timer.Reset(SaveDebounce)
		return
	}
	s.timer = time.AfterFunc(SaveDebounce, func() {
		if err := s.Save(); err != nil {
			s.mu.Lock()
			s.saveErr = err
			s.mu.Unlock()
		}
	})
}

// Save writes the file now. Flush on the way out calls it; the debounce timer
// calls it in the steady state.
//
// The write is atomic — a temp file in the same directory, then os.Rename — so
// a crash mid-write leaves the previous state rather than a truncated file.
func (s *Store) Save() error {
	s.mu.RLock()
	snap := s.s
	snap.Version = Version
	path := s.path
	s.mu.RUnlock()

	if path == "" {
		return errors.New("store: no path")
	}
	// 0700/0600, not 0755/0644. This file holds ClaudeAccount — which names one
	// of this machine's Claude logins by its configuration directory — and
	// Overlay, an arbitrary environment map the user fills in themselves and
	// which is therefore exactly where a key ends up. It is per-user state in
	// the user's own data directory; nothing else on the machine has a reason
	// to read it.
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')

	tmp, err := os.CreateTemp(filepath.Dir(path), ".state-*.json")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }() // no-op once the rename has succeeded
	// The Close errors on these two paths are deliberately dropped: the write
	// or the sync has already failed, that is the error worth returning, and
	// the deferred Remove above takes the temp file either way.
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(name, 0o600); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// Flush cancels any pending debounce and writes immediately. The window's
// close handler calls it.
func (s *Store) Flush() error {
	s.mu.Lock()
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
	s.mu.Unlock()
	return s.Save()
}

// SaveError is the last background save failure, so the UI can toast it rather
// than silently losing preferences.
func (s *Store) SaveError() error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.saveErr
}

// Path is where this store writes, for the About page.
func (s *Store) Path() string { return s.path }
