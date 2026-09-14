package gfy

import (
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// A local backend is one whose requests never leave this machine: an ollama
// server, or any OpenAI-compatible server (llama.cpp's llama-server, vLLM, LM
// Studio) reached through a loopback OPENAI_BASE_URL.
//
// It matters to this board for one reason above all others: the whole UI is
// built on the claim that an LLM-backed command spends money, and against a
// local model that claim is false. A run costs minutes and fan speed, not
// dollars. Everything here exists so the confirm dialog can say which of the
// two it is, instead of warning about a bill that will never arrive.
//
// The env-var names below are graphify 0.9.58's, read out of its llm.py rather
// than guessed. Two of them are NOT prefixed GRAPHIFY_ — they are ollama's own
// variables, which graphify deliberately honours so a machine already
// configured for ollama needs no second configuration for graphify.
const (
	// OllamaBackend is graphify's name for the ollama backend.
	OllamaBackend = "ollama"
	// OpenAIBackend is the OpenAI backend, which doubles as the generic
	// OpenAI-compatible client whenever OPENAI_BASE_URL is repointed.
	OpenAIBackend = "openai"

	// OllamaBaseURLVar wins outright when set, and is taken verbatim.
	OllamaBaseURLVar = "OLLAMA_BASE_URL"
	// OllamaHostVar is ollama's own variable, accepted as a bare host,
	// host:port, ":port" or a bare port.
	OllamaHostVar = "OLLAMA_HOST"
	// OllamaModelVar is the default model for the ollama backend. Note the
	// missing GRAPHIFY_ prefix: graphify reads ollama's variable, and a
	// GRAPHIFY_OLLAMA_MODEL is read by nothing at all.
	OllamaModelVar = "OLLAMA_MODEL"
	// OllamaKeyVar is accepted but ignored by ollama itself; graphify prints a
	// warning naming the URL it is about to send the corpus to when it is
	// unset, which is a warning worth keeping rather than papering over.
	OllamaKeyVar = "OLLAMA_API_KEY"
	// OpenAIBaseURLVar repoints the openai backend at any OpenAI-compatible
	// server. Pointed at loopback it makes "openai" a local backend.
	OpenAIBaseURLVar = "OPENAI_BASE_URL"
	// OpenAIModelVar is graphify's model override for the openai backend.
	OpenAIModelVar = "GRAPHIFY_OPENAI_MODEL"

	// OllamaBinVar overrides where the ollama executable lives, or "off" to
	// make the board behave as though this machine had none — the same
	// contract as GGRAPHIFY_CLAUDE_BIN.
	OllamaBinVar = "GGRAPHIFY_OLLAMA_BIN"

	// DefaultOllamaBaseURL and DefaultOllamaModel are graphify's own defaults
	// for the backend, restated here only so the UI can say what will happen
	// when nothing is configured.
	DefaultOllamaBaseURL = "http://localhost:11434/v1"
	DefaultOllamaModel   = "qwen2.5-coder:7b"
)

// Ollama resolves the ollama executable, or "" when this machine has none.
// It looks past $PATH for the same reason ClaudeCLI does: the board is often
// launched from a .desktop entry whose PATH is the session's minimal one.
//
// A missing binary does not mean there is no ollama to talk to — the server
// may be on another host entirely, which OLLAMA_HOST names. Probe() is the
// question that actually matters; this one only decides whether the UI can
// offer to pull a model.
func Ollama() string {
	switch v := strings.TrimSpace(os.Getenv(OllamaBinVar)); strings.ToLower(v) {
	case "":
	case "off", "none", "0":
		return ""
	default:
		return v
	}
	ollamaOnce.Do(func() { ollamaOnce.path = resolveOllama() })
	return ollamaOnce.path
}

var ollamaOnce struct {
	sync.Once
	path string
}

func resolveOllama() string {
	if p, err := exec.LookPath("ollama"); err == nil {
		return p
	}
	if home, err := os.UserHomeDir(); err == nil {
		for _, c := range []string{
			filepath.Join(home, ".local", "bin", "ollama"),
			filepath.Join(home, ".ollama", "bin", "ollama"),
		} {
			if executableFile(c) {
				return c
			}
		}
	}
	for _, c := range []string{
		"/usr/local/bin/ollama", "/usr/bin/ollama", "/opt/homebrew/bin/ollama",
	} {
		if executableFile(c) {
			return c
		}
	}
	return ""
}

// HasOllama reports whether an ollama executable is installed here.
func HasOllama() bool { return Ollama() != "" }

// OllamaBaseURL mirrors graphify's _resolve_ollama_base_url exactly: an
// explicit OLLAMA_BASE_URL verbatim, else ollama's own OLLAMA_HOST normalized
// the way the ollama client normalizes it, else graphify's default.
//
// Mirroring rather than approximating is the point. The confirm dialog prints
// this URL as the place the corpus is about to be sent, and a URL that differs
// from the one graphify will actually use would be worse than printing nothing.
func OllamaBaseURL() string { return ollamaBaseURL(DefaultOllamaBaseURL) }

// ollamaBaseURL is OllamaBaseURL with a caller-chosen default, so a caller can
// pass "" to ask "is one configured at all?" — which is the test graphify's own
// auto-detection uses to decide whether ollama is opted into.
func ollamaBaseURL(def string) string {
	if v, ok := os.LookupEnv(OllamaBaseURLVar); ok {
		return v
	}
	host, ok := os.LookupEnv(OllamaHostVar)
	if !ok {
		return def
	}
	host = strings.TrimSpace(host)
	if host == "" {
		return def
	}
	// A bare port, ":port", "host", "host:port" or a full URL — ollama accepts
	// all five and so must this.
	scheme := "http://"
	rest := host
	if s, r, ok := strings.Cut(host, "://"); ok {
		scheme, rest = s+"://", r
	}
	rest = strings.TrimSuffix(rest, "/")
	if n, err := strconv.Atoi(rest); err == nil && n > 0 && n < 65536 {
		rest = "127.0.0.1:" + rest
	} else if strings.HasPrefix(rest, ":") {
		rest = "127.0.0.1" + rest
	}
	// Only default the port when there is no path component to confuse with
	// one, and no port already.
	if !strings.Contains(rest, "/") {
		if _, _, err := net.SplitHostPort(rest); err != nil {
			rest += ":11434"
		}
	}
	u := scheme + rest
	if !strings.HasSuffix(u, "/v1") && !strings.Contains(rest, "/") {
		u += "/v1"
	}
	return u
}

// LocalBaseURL is the endpoint a backend will talk to when that endpoint is on
// this machine or this network, or "" when the backend is a vendor API.
func LocalBaseURL(backend string) string {
	switch strings.TrimSpace(backend) {
	case OllamaBackend:
		return OllamaBaseURL()
	case OpenAIBackend:
		u := strings.TrimSpace(os.Getenv(OpenAIBaseURLVar))
		if u != "" && IsLocalURL(u) {
			return u
		}
	}
	return ""
}

// IsLocalBackend reports whether a run against this backend keeps the corpus —
// and the bill — on this machine.
//
// ollama always qualifies: even pointed at another host it is a self-hosted
// model and not a metered vendor API. openai qualifies only when
// OPENAI_BASE_URL has been repointed at a loopback or private-network server,
// which is how llama.cpp's llama-server, vLLM and LM Studio are reached.
func IsLocalBackend(backend string) bool {
	switch strings.TrimSpace(backend) {
	case OllamaBackend:
		return true
	case OpenAIBackend:
		return LocalBaseURL(OpenAIBackend) != ""
	}
	return false
}

// IsLocalURL reports whether a URL names a host on this machine or on a
// private network — the test that separates "you are self-hosting a model"
// from "you have repointed the openai backend at some other vendor".
//
// It is deliberately conservative: anything it cannot resolve to a literal
// private address is treated as remote, because the cost of being wrong is a
// confirm dialog that tells someone a paid API call is free.
func IsLocalURL(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return false
	}
	host := u.Hostname()
	switch strings.ToLower(host) {
	case "localhost", "ip6-localhost", "host.docker.internal":
		return true
	}
	if strings.HasSuffix(strings.ToLower(host), ".local") {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast()
}

// LocalProbe is what a live look at a local server found. The zero value means
// "not probed"; Reachable false with an Err set means "probed and it is down",
// which is a materially different thing to tell a user.
type LocalProbe struct {
	Backend  string   // "ollama" or "openai"
	BaseURL  string   // the endpoint that was probed
	Bin      string   // the local executable, when there is one
	Reach    bool     // the server answered
	Models   []string // models it reports, sorted
	Err      string   // why it did not answer
	ProbedAt time.Time
}

// Has reports whether a named model is loaded on the server. An empty name
// asks whether the server has any model at all.
func (p LocalProbe) Has(model string) bool {
	model = strings.TrimSpace(model)
	if model == "" {
		return len(p.Models) > 0
	}
	for _, m := range p.Models {
		// `ollama list` reports "qwen2.5-coder:7b"; a user who typed
		// "qwen2.5-coder" means that same tag, and graphify resolves it the
		// same way ollama's own CLI does.
		if m == model || strings.TrimSuffix(m, ":latest") == model {
			return true
		}
	}
	return false
}

// probeTTL keeps the settings page and the confirm dialog from issuing an HTTP
// request per redraw. A local server coming up is not an event worth
// sub-second latency; five seconds is short enough that "I just started it"
// followed by a click sees the new state.
const probeTTL = 5 * time.Second

var probeCache struct {
	sync.Mutex
	m map[string]LocalProbe
}

// ProbeLocal asks a local backend's server what it has, with a short timeout
// and a short cache. It never returns an error: "down" is an answer, and the
// caller's job is to render it, not to handle it.
func ProbeLocal(backend string) LocalProbe {
	backend = strings.TrimSpace(backend)
	base := LocalBaseURL(backend)
	if base == "" {
		return LocalProbe{Backend: backend}
	}
	key := backend + " " + base

	probeCache.Lock()
	if p, ok := probeCache.m[key]; ok && time.Since(p.ProbedAt) < probeTTL {
		probeCache.Unlock()
		return p
	}
	probeCache.Unlock()

	p := LocalProbe{Backend: backend, BaseURL: base, ProbedAt: time.Now()}
	if backend == OllamaBackend {
		p.Bin = Ollama()
	}
	p.Models, p.Err = fetchModels(base)
	p.Reach = p.Err == ""

	probeCache.Lock()
	if probeCache.m == nil {
		probeCache.m = map[string]LocalProbe{}
	}
	probeCache.m[key] = p
	probeCache.Unlock()
	return p
}

// InvalidateLocalProbe drops the cached probe, so an action that just changed
// the server's state — starting it, pulling a model — is reflected at once
// rather than up to probeTTL later.
func InvalidateLocalProbe() {
	probeCache.Lock()
	probeCache.m = nil
	probeCache.Unlock()
}

// fetchModels reads /v1/models, the one endpoint every OpenAI-compatible
// server implements — ollama, llama-server, vLLM and LM Studio alike. Going
// through the compatibility surface rather than ollama's native /api/tags is
// deliberate: it is the same surface graphify will use, so a server that
// answers here is a server graphify can actually drive.
func fetchModels(base string) ([]string, string) {
	u := strings.TrimSuffix(base, "/")
	if !strings.HasSuffix(u, "/v1") {
		u += "/v1"
	}
	u += "/models"

	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(u)
	if err != nil {
		return nil, shortErr(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "HTTP " + strconv.Itoa(resp.StatusCode) + " from " + u
	}
	var body struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, "unreadable /v1/models response: " + err.Error()
	}
	out := make([]string, 0, len(body.Data))
	for _, m := range body.Data {
		if m.ID != "" {
			out = append(out, m.ID)
		}
	}
	sort.Strings(out)
	return out, ""
}

// shortErr trims net/http's wrapping down to the part a human needs. The full
// text names the URL twice and the Go method once, none of which is news in a
// dialog that already printed the URL.
func shortErr(err error) string {
	s := err.Error()
	if i := strings.LastIndex(s, ": "); i >= 0 && i+2 < len(s) {
		s = s[i+2:]
	}
	switch {
	case strings.Contains(s, "connection refused"):
		return "connection refused — the server is not running"
	case strings.Contains(s, "deadline exceeded") || strings.Contains(s, "timeout"):
		return "timed out"
	}
	return s
}

// LocalModel is the model a local backend will actually run, given the board's
// setting, the environment, and graphify's own default — in that order, which
// is the order graphify resolves them in.
func LocalModel(backend, model string) string {
	if m := strings.TrimSpace(model); m != "" {
		return m
	}
	switch strings.TrimSpace(backend) {
	case OllamaBackend:
		if m := strings.TrimSpace(os.Getenv(OllamaModelVar)); m != "" {
			return m
		}
		return DefaultOllamaModel
	case OpenAIBackend:
		if m := strings.TrimSpace(os.Getenv(OpenAIModelVar)); m != "" {
			return m
		}
	}
	return ""
}

// LocalReady is BackendReady for a local backend, with the server actually
// probed rather than assumed. model is the board's setting, which may be blank.
//
// The three failures it separates are the three a user hits, in the order they
// hit them: no server, a server with nothing pulled, and a server missing the
// one model the board is about to ask for. Each names its own fix.
func LocalReady(backend, model string) (bool, string) {
	p := ProbeLocal(backend)
	if p.BaseURL == "" {
		return false, "No local endpoint is configured for the " + backend + " backend."
	}
	want := LocalModel(backend, model)

	if !p.Reach {
		why := "No local model server answered at " + p.BaseURL + " (" + p.Err + ")."
		switch {
		case backend == OllamaBackend && p.Bin != "":
			why += " Start it with `ollama serve`, or `systemctl --user start ollama`."
		case backend == OllamaBackend:
			why += " ollama is not installed here either — see https://ollama.com/download," +
				" or point " + OllamaHostVar + " at a server elsewhere."
		default:
			why += " Start your OpenAI-compatible server, or clear " + OpenAIBaseURLVar +
				" to go back to the OpenAI API."
		}
		return false, why
	}
	if len(p.Models) == 0 {
		why := p.BaseURL + " is up but is serving no models."
		if backend == OllamaBackend && p.Bin != "" {
			why += " Pull one: `ollama pull " + want + "`."
		}
		return false, why
	}
	if want != "" && !p.Has(want) {
		why := p.BaseURL + " is up but does not have " + want + ". It has: " +
			strings.Join(p.Models, ", ") + "."
		if backend == OllamaBackend && p.Bin != "" {
			why += " Pull it with `ollama pull " + want + "`, or pick one it already has."
		}
		return false, why
	}
	ran := want
	if ran == "" {
		ran = p.Models[0] + " (the server's own default)"
	}
	return true, "Local model server at " + p.BaseURL + " — running " + ran +
		". No API key, and nothing leaves this machine: this run costs time, not money."
}

// LocalNotice is the cost paragraph for a local backend, replacing the "this
// will be billed" one. It is the reason this file exists.
func LocalNotice(backend, model string) string {
	p := ProbeLocal(backend)
	var b strings.Builder
	b.WriteString("This runs against a LOCAL model and costs no money. " +
		"The corpus is sent to " + p.BaseURL + " and to nowhere else.\n")
	b.WriteString("Backend: " + backend + " (local)\nModel: " + LocalModel(backend, model) + "\n")
	b.WriteString("Local models are slow: graphify forces one request at a time for ollama " +
		"regardless of the metered lane, so a large repository takes minutes to hours.\n")
	return b.String()
}

// StartOllama brings a local ollama server up, and reports how.
//
// Two routes, tried in order, because they are the two ways an ollama is
// installed on a Linux desktop and they are not interchangeable:
//
//   - a systemd unit — the system one the distro package ships, or a per-user
//     one — which is started through systemctl so that it stays under
//     systemd's supervision and survives this process exiting;
//   - otherwise a detached `ollama serve`, which is what a user without
//     systemd (or without the right to touch the system unit) would type.
//
// It returns as soon as the server answers, or after about ten seconds, which
// is generously longer than a cold start on any machine that can run a model
// at all. A non-nil error means the server is still not answering; the string
// return says which route was taken either way, for the log.
func StartOllama() (string, error) {
	// Already up is the commonest case by far — the settings page offers the
	// button whenever the port looked silent, and a slow cold start means two
	// clicks. Nothing below is worth doing twice.
	InvalidateLocalProbe()
	if ProbeLocal(OllamaBackend).Reach {
		return "already running", nil
	}
	bin := Ollama()
	if bin == "" {
		return "", errNoOllama
	}

	var how string
	switch {
	case unitEnabled("ollama.service", "--user"):
		// A per-user unit is the one route this process can take on its own:
		// no privilege, and systemd keeps supervising the server after the
		// board exits.
		how = "systemctl --user start ollama"
		_ = exec.Command("systemctl", "--user", "start", "ollama").Run()

	case unitExists("ollama.service"):
		// A SYSTEM unit owns ollama on this machine. Starting a user unit
		// instead would contend for the same port behind systemd's back, and a
		// detached `ollama serve` would run a second server against a
		// different model store — which is precisely the shape of confusion
		// that makes an already-pulled model appear to have vanished. Neither
		// is a thing to do silently, and the right command needs root, which
		// this process does not have. Name it and stop.
		return SudoStartOllama, errNeedsRoot

	default:
		how = bin + " serve (detached)"
		cmd := exec.Command(bin, "serve")
		// Detached: the server must outlive the board, not die with it. Its
		// output goes nowhere — the server logs to journald or to its own
		// file, and a pipe nobody reads would eventually block it.
		cmd.Stdout, cmd.Stderr, cmd.Stdin = nil, nil, nil
		if err := cmd.Start(); err != nil {
			return how, err
		}
		go func() { _ = cmd.Wait() }() // reap, so it never becomes a zombie
	}

	deadline := time.Now().Add(10 * time.Second)
	for {
		InvalidateLocalProbe()
		if ProbeLocal(OllamaBackend).Reach {
			return how, nil
		}
		if time.Now().After(deadline) {
			return how, errOllamaSilent
		}
		time.Sleep(400 * time.Millisecond)
	}
}

// SudoStartOllama is the command a user has to run themselves when a system
// unit owns ollama. A constant, so the UI puts it on the clipboard verbatim
// rather than reconstructing it out of an error string.
const SudoStartOllama = "sudo systemctl start ollama"

// NeedsRoot reports whether StartOllama stopped because the right command
// needs privileges this process has not got. The caller shows the command
// instead of reporting a failure.
func NeedsRoot(err error) bool { return err == errNeedsRoot }

// unitExists asks systemd whether it knows a unit, without starting anything.
// `systemctl cat` exits non-zero for a unit that does not exist, which is the
// cheapest available yes/no. scope is "--user", or nothing at all for the
// system manager.
func unitExists(unit string, scope ...string) bool {
	return systemctlOK(append(append([]string{}, scope...), "cat", unit))
}

// unitEnabled is stricter than unitExists: the unit file is there AND systemd
// has been told to run it.
//
// The distinction is load-bearing. `systemctl --user disable` removes the
// wants/ symlink and leaves the unit file exactly where it was, so a machine
// that has moved from a per-user ollama to the distro's system-wide one still
// has a user unit that `cat` finds. Acting on that would start a second
// server against a second model store.
func unitEnabled(unit string, scope ...string) bool {
	if !unitExists(unit, scope...) {
		return false
	}
	return systemctlOK(append(append([]string{}, scope...), "is-enabled", unit))
}

func systemctlOK(args []string) bool {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return false
	}
	return exec.Command("systemctl", args...).Run() == nil
}

// PullOllamaCommand is the exact command that would fetch a model, for the UI
// to show and to put on the clipboard.
//
// The board deliberately does not run this itself. A pull is gigabytes over
// somebody's network; a GUI button that starts one with no progress, no
// cancel and no way to see how far it got would be a worse experience than
// the terminal it replaces, and this application's whole posture is that an
// expensive thing is never started without the user seeing what it costs.
func PullOllamaCommand(model string) string {
	if strings.TrimSpace(model) == "" {
		model = DefaultOllamaModel
	}
	bin := Ollama()
	if bin == "" {
		bin = "ollama"
	}
	return Quote([]string{bin, "pull", model})
}

var (
	errNoOllama     = errStr("no ollama executable on this machine")
	errOllamaSilent = errStr("started, but nothing is answering on the port yet")
	errNeedsRoot    = errStr("a system-wide ollama.service owns this machine's ollama, " +
		"and starting it needs root")
)

// errStr is a string that is an error, so this file needs no errors import for
// two sentinels.
type errStr string

func (e errStr) Error() string { return string(e) }
