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
	for i := 0; i < MaxHistory+50; i++ {
		s.AddHistory(HistoryEntry{Kind: "update", Repo: "/r", Started: time.Now()})
	}
	if n := len(s.History()); n != MaxHistory {
		t.Fatalf("history has %d entries, want %d", n, MaxHistory)
	}
}

func TestHistoryIsNewestFirst(t *testing.T) {
	s, _ := tempStore(t)
	s.AddHistory(HistoryEntry{Kind: "first"})
	s.AddHistory(HistoryEntry{Kind: "second"})
	if s.History()[0].Kind != "second" {
		t.Fatal("history is not newest-first")
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
