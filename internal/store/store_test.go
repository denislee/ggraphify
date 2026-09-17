package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dns/ggraphify/internal/graphstate"
)

func tempStore(t *testing.T) (*Store, string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "state.json")
	return Open(p), p
}

func TestRoundTrip(t *testing.T) {
	s, p := tempStore(t)
	set := s.Settings()
	set.Backend = "gemini"
	set.Model = "g-2"
	set.Roots = []string{"/a", "/b"}
	set.Overlay["GRAPHIFY_MAX_WORKERS"] = "8"
	s.SetSettings(set)
	s.SetOverride("/repo", RepoOverride{Backend: "openai", ExcludeBatch: true})
	s.SetWindow(Geometry{Width: 1400, Height: 900})
	s.SetColumnWidth("repo", 260)
	s.SetSelected("/repo")
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}

	again := Open(p)
	got := again.Settings()
	if got.Backend != "gemini" || got.Model != "g-2" {
		t.Errorf("settings did not survive: %+v", got)
	}
	if got.Overlay["GRAPHIFY_MAX_WORKERS"] != "8" {
		t.Error("the overlay did not survive")
	}
	if o := again.Override("/repo"); o.Backend != "openai" || !o.ExcludeBatch {
		t.Errorf("override did not survive: %+v", o)
	}
	if g, ok := again.Window(); !ok || g.Width != 1400 {
		t.Errorf("geometry did not survive: %+v", g)
	}
	if again.Columns()["repo"] != 260 {
		t.Error("column width did not survive")
	}
	if again.Selected() != "/repo" {
		t.Error("selection did not survive")
	}
}

// A board that refused to start because its preferences file has a stray brace
// in it would be worse than one that starts fresh.
func TestCorruptFileFallsBackToDefaults(t *testing.T) {
	p := filepath.Join(t.TempDir(), "state.json")
	os.WriteFile(p, []byte("{not json"), 0o644)

	s := Open(p)
	if got := s.Settings().Depth; got != Defaults().Depth {
		t.Fatalf("Depth = %d, want the default %d", got, Defaults().Depth)
	}
	if s.Settings().Overlay == nil {
		t.Fatal("a corrupt file must still produce a usable overlay map")
	}
}

// A file written by an older build has fewer keys; every one it does not
// mention keeps the default that was already there, rather than becoming a
// zero value the board then has to defend against everywhere.
func TestForwardCompatibleMerge(t *testing.T) {
	p := filepath.Join(t.TempDir(), "state.json")
	os.WriteFile(p, []byte(`{"version":1,"settings":{"backend":"openai"}}`), 0o644)

	s := Open(p)
	got := s.Settings()
	if got.Backend != "openai" {
		t.Errorf("Backend = %q", got.Backend)
	}
	if got.Depth != Defaults().Depth {
		t.Errorf("Depth = %d, want the default", got.Depth)
	}
	if got.Refresh != Defaults().Refresh {
		t.Errorf("Refresh = %d, want the default", got.Refresh)
	}
	// The metered lane in particular must never come back as zero: the runner
	// would then be told "no limit" for the lane that spends money.
	if got.MeteredLanes != 1 {
		t.Errorf("MeteredLanes = %d, want 1", got.MeteredLanes)
	}
	if got.ConfirmBatchAt <= 0 {
		t.Errorf("ConfirmBatchAt = %d", got.ConfirmBatchAt)
	}
}

// An override that says nothing is removed rather than accumulated: otherwise
// the file grows one entry per repository ever selected.
func TestEmptyOverrideIsRemoved(t *testing.T) {
	s, _ := tempStore(t)
	s.SetOverride("/repo", RepoOverride{Backend: "openai"})
	if len(s.Overrides()) != 1 {
		t.Fatal("the override was not stored")
	}
	s.SetOverride("/repo", RepoOverride{})
	if len(s.Overrides()) != 0 {
		t.Fatal("an empty override was kept")
	}
}

func TestHistoryIsBounded(t *testing.T) {
	s, _ := tempStore(t)
	var list []JobEntry
	for i := 0; i < MaxHistory+50; i++ {
		list = append(list, JobEntry{Kind: "update", Repo: "/r", Status: "ok", Started: time.Now()})
	}
	s.SetJobs(list)
	if n := len(s.History()); n != MaxHistory {
		t.Fatalf("history has %d entries, want %d", n, MaxHistory)
	}
}

func TestHistoryIsNewestFirst(t *testing.T) {
	s, _ := tempStore(t)
	s.SetJobs([]JobEntry{{Kind: "second", Status: "ok"}, {Kind: "first", Status: "ok"}})
	if s.History()[0].Kind != "second" {
		t.Fatal("history is not newest-first")
	}
}

// The bound is on what has FINISHED. A queue is not a log: dropping its oldest
// entry means losing work somebody asked for, so an outstanding job survives
// however far down the list it sits.
func TestQueuedJobsAreNeverTrimmed(t *testing.T) {
	s, _ := tempStore(t)
	list := []JobEntry{{Kind: "extract", Repo: "/r", Status: "queued", Held: true}}
	for i := 0; i < MaxHistory+50; i++ {
		list = append(list, JobEntry{Kind: "update", Repo: "/r", Status: "ok"})
	}
	s.SetJobs(list)
	got := s.Jobs()
	if n := len(got); n != MaxHistory+1 {
		t.Fatalf("kept %d entries, want %d", n, MaxHistory+1)
	}
	if got[0].Status != "queued" || !got[0].Held {
		t.Fatalf("the queued job did not survive: %+v", got[0])
	}
}

// The queue and the run log survive the process they were made in.
func TestJobsRoundTripThroughTheFile(t *testing.T) {
	s, path := tempStore(t)
	s.SetJobs([]JobEntry{
		{Kind: "extract", Repo: "/r", Label: "Extract · r", Cost: "metered",
			Status: "queued", Held: true, Argv: []string{"graphify", "extract"}, Dir: "/r"},
		{Kind: "update", Repo: "/r", Status: "failed", Exit: 2,
			Error: "boom", Log: "traceback\n", Argv: []string{"graphify", "update"}},
	})
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	got := Open(path).Jobs()
	if len(got) != 2 {
		t.Fatalf("reloaded %d jobs, want 2", len(got))
	}
	if got[0].Status != "queued" || !got[0].Held || got[0].Dir != "/r" {
		t.Fatalf("the queued job came back wrong: %+v", got[0])
	}
	if got[1].Log != "traceback\n" || got[1].Exit != 2 {
		t.Fatalf("the failed job lost its log or its exit code: %+v", got[1])
	}
}

// A file written before jobs survived a restart keeps its run log.
func TestLegacyHistoryIsMigrated(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	legacy := `{"version":1,"history":[{"kind":"update","repo":"/r","status":"ok","exit":0,"duration_seconds":12}]}`
	if err := os.WriteFile(path, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	s := Open(path)
	jobs := s.Jobs()
	if len(jobs) != 1 || jobs[0].Kind != "update" || jobs[0].Status != "ok" {
		t.Fatalf("legacy history did not migrate: %+v", jobs)
	}
	if h := s.History(); len(h) != 1 || h[0].Duration != 12 {
		t.Fatalf("the migrated duration was lost: %+v", h)
	}
	// And it is written back under the new key only, so the two lists can
	// never disagree about what ran.
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), `"history"`) {
		t.Fatal("the legacy history key was written back")
	}
}

// A crash mid-write must leave the previous state, not a truncated file.
func TestSaveIsAtomic(t *testing.T) {
	s, p := tempStore(t)
	s.SetSelected("/a")
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	// After a save there is exactly one file in the directory: no temp file
	// left behind.
	ents, _ := os.ReadDir(filepath.Dir(p))
	if len(ents) != 1 {
		t.Fatalf("directory holds %d files after a save, want 1", len(ents))
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("the written file is not valid JSON: %v", err)
	}
}

// The Overlay for a repository is the board-wide one with that repository's
// own GRAPHIFY_OUT on top — and no API key, ever.
func TestOverlayLayersRepoOut(t *testing.T) {
	s, _ := tempStore(t)
	set := s.Settings()
	set.Overlay["GRAPHIFY_MAX_WORKERS"] = "4"
	s.SetSettings(set)
	s.SetOverride("/repo", RepoOverride{Out: "/elsewhere"})

	e := s.Overlay("/repo")
	if e["GRAPHIFY_MAX_WORKERS"] != "4" {
		t.Error("the board-wide overlay was lost")
	}
	if e["GRAPHIFY_OUT"] != "/elsewhere" {
		t.Errorf("GRAPHIFY_OUT = %q", e["GRAPHIFY_OUT"])
	}
	if s.Overlay("/other")["GRAPHIFY_OUT"] != "" {
		t.Error("one repository's out leaked into another's overlay")
	}
}

// Settings() hands back a copy: a caller mutating it must not be able to
// change the store without going through SetSettings.
func TestSettingsIsACopy(t *testing.T) {
	s, _ := tempStore(t)
	got := s.Settings()
	got.Backend = "mutated"
	got.Overlay["X"] = "1"
	if s.Settings().Backend == "mutated" {
		t.Error("Settings() shares its struct")
	}
	if _, ok := s.Settings().Overlay["X"]; ok {
		t.Error("Settings() shares its overlay map")
	}
}

// The board-wide graph location has to reach the jobs, or the board would read
// one directory while graphify wrote another and every row would show drift
// that no update could clear.
func TestOverlayCarriesConfiguredOutLocation(t *testing.T) {
	t.Setenv("GRAPHIFY_OUT", "")
	t.Setenv("GRAPHIFY_OUT_NAME", "")
	s, _ := tempStore(t)

	if _, ok := s.Overlay("/repo")["GRAPHIFY_OUT"]; ok {
		t.Error("an unconfigured board pinned GRAPHIFY_OUT; graphify's own default must decide")
	}

	set := s.Settings()
	set.OutBase = "/var/graphs"
	s.SetSettings(set)

	want := s.Out("/repo")
	if !strings.HasPrefix(want, "/var/graphs/") {
		t.Fatalf("Out = %q, want a path under the configured base", want)
	}
	if got := s.Overlay("/repo")["GRAPHIFY_OUT"]; got != want {
		t.Errorf("GRAPHIFY_OUT = %q, want %q", got, want)
	}

	// A per-repository override still outranks the board-wide location.
	s.SetOverride("/repo", RepoOverride{Out: "/elsewhere"})
	if got := s.Overlay("/repo")["GRAPHIFY_OUT"]; got != "/elsewhere" {
		t.Errorf("override lost: GRAPHIFY_OUT = %q", got)
	}
	if got := s.Out("/other"); !strings.HasPrefix(got, "/var/graphs/") {
		t.Errorf("one repository's override leaked: %q", got)
	}
}

// The storage setting is composed last, so it wins over an overlay entry that
// names the same variable — which is what the settings dialog tells the user.
func TestOverlayOutBeatsUserOverlayEntry(t *testing.T) {
	s, _ := tempStore(t)
	set := s.Settings()
	set.Overlay["GRAPHIFY_OUT"] = "/from/overlay"
	set.OutName = ".graph"
	s.SetSettings(set)

	if got, want := s.Overlay("/repo")["GRAPHIFY_OUT"], "/repo/.graph"; got != want {
		t.Errorf("GRAPHIFY_OUT = %q, want %q", got, want)
	}
}

// The bottom panel is on by default, and that has to survive a state file
// written before the panel existed: the flags are stored as *hide* precisely
// so the zero value is the default rather than a choice nobody made.
func TestBottomPanelDefaultsOn(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"settings":{"depth":3}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	set := Open(path).Settings()
	if set.HideBottom || set.HideJobsBar || set.HideLogBar {
		t.Fatalf("panel hidden by an old state file: %+v", set)
	}
	if set.BottomHeight <= 0 {
		t.Fatalf("BottomHeight = %d, want the default", set.BottomHeight)
	}
	if set.BottomPage == "" {
		t.Fatal("BottomPage left empty, so the panel would open on no page at all")
	}
}

func TestBottomPanelRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s := Open(path)
	set := s.Settings()
	set.HideLogBar = true
	set.BottomHeight = 314
	set.BottomPage = "log"
	s.SetSettings(set)
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}

	got := Open(path).Settings()
	if !got.HideLogBar || got.HideJobsBar || got.BottomHeight != 314 || got.BottomPage != "log" {
		t.Fatalf("round trip lost the panel state: %+v", got)
	}
}

// The drift baseline is what makes a repository's settled drift survive a
// restart. An empty one must delete rather than accumulate: a sidecar carrying
// a blank entry per repository forever is a file nobody benefits from.
func TestDriftBaselineRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	s := Open(path)
	b := graphstate.Baseline{
		At:      time.Now().Truncate(time.Second),
		Added:   map[string]float64{"data/huge.json": 1757000000},
		Removed: map[string]bool{"gone.go": true},
	}
	s.SetDriftBaseline("/repo/a", b)
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}

	got := Open(path).DriftBaseline("/repo/a")
	if got.Added["data/huge.json"] != 1757000000 || !got.Removed["gone.go"] {
		t.Fatalf("baseline did not survive a reload: %+v", got)
	}

	s.SetDriftBaseline("/repo/a", graphstate.Baseline{})
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	if got := Open(path).DriftBaseline("/repo/a"); len(got.Added) != 0 || len(got.Removed) != 0 {
		t.Fatalf("an empty baseline was stored rather than deleted: %+v", got)
	}
}

// Auto-fix is on by default, and — the part that actually matters — a state
// file written before it existed reads back as on rather than as a deliberate
// "no". That is the whole reason the two switches are stored inverted, and it
// is the one property of the encoding worth a test.
func TestAutoFixDefaultsOnForAStateFileThatPredatesIt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"settings":{"depth":3}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	set := Open(path).Settings()
	if !set.AutoFix() {
		t.Error("auto-fix should be on for a state file that never heard of it")
	}
	if !set.AutoFixLocal() {
		t.Error("the local-model half should be on too — it is what makes the loop free")
	}
	if set.AutoFixMetered {
		t.Error("spending must never be on by default")
	}
	if !Defaults().AutoFix() || Defaults().AutoFixMetered {
		t.Error("Defaults() disagrees with the zero value, which is the one thing it may not do")
	}
}

// Turning it off survives a round trip: the inversion has to work in both
// directions or "off" quietly becomes "on" at the next launch.
func TestAutoFixOffRoundTrips(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	st := Open(path)
	s := st.Settings()
	s.NoAutoFix = true
	s.AutoFixMax = 5
	st.SetSettings(s)
	if err := st.Flush(); err != nil {
		t.Fatal(err)
	}

	got := Open(path).Settings()
	if got.AutoFix() {
		t.Error("auto-fix came back on after being turned off")
	}
	if got.AutoFixMax != 5 {
		t.Errorf("AutoFixMax = %d, want 5", got.AutoFixMax)
	}
}

// A migrated history entry never carried a label, and the board draws a job
// by its label: without one it arrives as a nameless row.
func TestMigratedHistoryGetsALabel(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	legacy := `{"version":1,"history":[{"kind":"update","repo":"/src/nexus","status":"ok","exit":0,"duration_seconds":12}]}`
	if err := os.WriteFile(path, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	jobs := Open(path).Jobs()
	if len(jobs) != 1 {
		t.Fatalf("migrated %d jobs, want 1", len(jobs))
	}
	if jobs[0].Label == "" {
		t.Fatal("the migrated job has no label — it draws as a blank row")
	}
	if !strings.Contains(jobs[0].Label, "nexus") {
		t.Fatalf("label %q does not name the checkout it ran against", jobs[0].Label)
	}
}

// The negative control: an entry that DOES carry a label keeps its own, rather
// than having the rebuilt one written over it.
func TestRecordedJobLabelIsNotRewritten(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	saved := `{"version":1,"jobs":[{"kind":"update","repo":"/src/nexus","label":"Fix 2/7 · nexus","argv":["graphify"],"status":"ok"}]}`
	if err := os.WriteFile(path, []byte(saved), 0o644); err != nil {
		t.Fatal(err)
	}
	jobs := Open(path).Jobs()
	if len(jobs) != 1 || jobs[0].Label != "Fix 2/7 · nexus" {
		t.Fatalf("a recorded label was not kept: %+v", jobs)
	}
}

// The board must read from the directory its jobs write to. These used to be
// two resolvers — the UI applied flag precedence to the scan, OutSpec read the
// stored settings alone — so `-out-base` made the board read one place and
// every job it launched write another. A run under that split reported 27
// graphed repositories as ungraphed and re-extracted them into their own
// checkouts.
func TestOutLocationIsTheOnlyResolver(t *testing.T) {
	s, _ := tempStore(t)
	set := s.Settings()
	set.OutBase = "/stored/knowledge"
	s.SetSettings(set)

	// No flag: the stored preference wins over the built-in default.
	if _, base := s.OutLocation(); base != "/stored/knowledge" {
		t.Errorf("base = %q, want the stored one", base)
	}

	// A flag outranks it — and OutSpec, which is what every job's
	// GRAPHIFY_OUT is built from, must agree.
	s.SetOutOverride("", false, "/flag/knowledge", true)
	_, base := s.OutLocation()
	if base != "/flag/knowledge" {
		t.Errorf("base = %q, want the flag", base)
	}
	if spec := s.OutSpec("/repo/x"); spec.Base != base {
		t.Errorf("OutSpec.Base = %q but OutLocation says %q — the read and write "+
			"locations have come apart again", spec.Base, base)
	}

	// The job's environment is the thing that actually writes, so it is
	// asserted directly rather than inferred from the spec.
	if got := s.Overlay("/repo/x")["GRAPHIFY_OUT"]; got == "" ||
		!strings.HasPrefix(got, "/flag/knowledge") {
		t.Errorf("GRAPHIFY_OUT = %q, want it under the flag's base", got)
	}

	// An explicitly-empty flag is a choice: it means the checkout's own
	// directory, and must not fall back to the stored value.
	s.SetOutOverride("", false, "", true)
	if _, base := s.OutLocation(); base != "" {
		t.Errorf("base = %q, want empty — the flag said so", base)
	}

	// Nothing about the flags is persisted: a launch option must not become a
	// saved preference that outlives the launch.
	if got := s.Settings().OutBase; got != "/stored/knowledge" {
		t.Errorf("stored OutBase = %q — a flag leaked into the settings", got)
	}
}

// The ollama lifecycle has to be on for a state file written before it
// existed, which is what "enabled by default" means in practice: the default
// is carried by the zero value, not by a migration.
func TestOllamaLifecycleDefaultsOnForAnOldStateFile(t *testing.T) {
	var s Settings
	if !s.AutoOllama() {
		t.Fatal("AutoOllama() is false for a zero Settings; the default must be on")
	}
	if got := s.OllamaIdle(); got != DefaultOllamaIdleStop {
		t.Fatalf("OllamaIdle() = %v for a zero Settings, want %v", got, DefaultOllamaIdleStop)
	}
	// And switching it off has to survive a write, which is the half a stored
	// inverted flag gets wrong when the JSON tag says omitempty and the
	// reader forgets the inversion.
	s.NoAutoOllama = true
	if s.AutoOllama() {
		t.Fatal("AutoOllama() is true after NoAutoOllama was set")
	}
	s.OllamaIdleStop = 90
	if got := s.OllamaIdle(); got != 90*time.Second {
		t.Fatalf("OllamaIdle() = %v, want 90s", got)
	}
	// A negative or zero value is one nobody chose, not an instruction to
	// stop the server the instant a job ends.
	for _, n := range []int{0, -1} {
		s.OllamaIdleStop = n
		if got := s.OllamaIdle(); got != DefaultOllamaIdleStop {
			t.Fatalf("OllamaIdle() with %d = %v, want the default", n, got)
		}
	}
}

// The round trip, because the switch is only useful if it is remembered.
func TestOllamaLifecycleRoundTrips(t *testing.T) {
	s, path := tempStore(t)
	set := s.Settings()
	set.NoAutoOllama = true
	set.OllamaIdleStop = 120
	s.SetSettings(set)
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}

	got := Open(path).Settings()
	if got.AutoOllama() {
		t.Fatal("the lifecycle came back on after being switched off")
	}
	if got.OllamaIdle() != 120*time.Second {
		t.Fatalf("idle period came back as %v, want 120s", got.OllamaIdle())
	}
}
