package gfy

import (
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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

	// OllamaSlotsVar states outright how many requests the server answers at
	// once, skipping the derivation below. It is the escape hatch for the one
	// case that derivation cannot cover: a server on ANOTHER host, whose
	// configuration is not on this disk under any manager this board can ask.
	// Somebody who administers that server knows the number; nothing here
	// could ever discover it.
	OllamaSlotsVar = "GGRAPHIFY_OLLAMA_SLOTS"

	// DefaultOllamaBaseURL and DefaultOllamaModel are graphify's own defaults
	// for the backend, restated here only so the UI can say what will happen
	// when nothing is configured.
	DefaultOllamaBaseURL = "http://localhost:11434/v1"
	DefaultOllamaModel   = "qwen2.5-coder:7b"

	// OllamaContextVar sets the context window an ollama SERVER allocates for
	// every model it loads. It is deliberately not in the per-job overlay: it
	// is read by the server process at model-load time, so exporting it into a
	// graphify subprocess changes nothing at all.
	OllamaContextVar = "OLLAMA_CONTEXT_LENGTH"

	// DefaultOllamaContext is what an ollama server started without
	// OllamaContextVar runs every model at — 4096 tokens, however large the
	// model's own trained context is. A 7B coder model trained to 32768 is
	// still served in a 4096 slot unless the server was told otherwise, and
	// the model list says nothing about it: only the running server does.
	DefaultOllamaContext = 4096

	// WantOllamaContext is the slot the board asks for when it starts a server
	// itself. Large enough that graphify's system prompt plus a real source
	// file plus the reply all fit, small enough to be honest about memory on a
	// machine that is hosting a model at all.
	WantOllamaContext = 32768

	// OllamaParallelVar is how many requests an ollama SERVER answers at once.
	// Like OllamaContextVar it is read by the server process, not by a client,
	// so it is not in the per-job overlay either — but unlike the context slot
	// it is measurable only indirectly, which is what ollamaSlots is for.
	//
	// It is the single most important number for throughput here. graphify
	// dispatches its chunks through a thread pool, and every slot the server
	// has is a chunk that can be in flight; a server left at one slot spends
	// the whole extraction with one core-group busy and the rest of the
	// machine idle.
	OllamaParallelVar = "OLLAMA_NUM_PARALLEL"
	// OllamaMaxLoadedVar bounds how many distinct models the server keeps
	// resident. One is right for an extraction: a second model evicting the
	// first mid-run costs a full reload per alternation.
	OllamaMaxLoadedVar = "OLLAMA_MAX_LOADED_MODELS"
	// OllamaFlashAttnVar and OllamaKVCacheVar are the two settings that decide
	// what a slot costs in memory. Flash attention plus an 8-bit K/V cache
	// roughly halves the per-slot cache against the f16 default, which is what
	// makes a second slot affordable on a machine that could not otherwise
	// hold one.
	OllamaFlashAttnVar = "OLLAMA_FLASH_ATTENTION"
	OllamaKVCacheVar   = "OLLAMA_KV_CACHE_TYPE"
	// OllamaKeepAliveVar is how long the server keeps a model resident after a
	// request. graphify asks for 30m per request, but it asks inside
	// `extra_body`, on the compatibility endpoint that drops it — so the
	// server-side variable is the only one that actually holds.
	OllamaKeepAliveVar = "OLLAMA_KEEP_ALIVE"

	// GraphifyOllamaParallelVar is graphify's own opt-in, and the reason a
	// board that only passed --max-concurrency would change nothing at all:
	// llm.py clamps max_concurrency back to 1 for the ollama backend unless
	// this is exactly "1", in both the extraction dispatcher and the
	// community-labelling one. The flag and the variable have to travel
	// together or neither has an effect.
	GraphifyOllamaParallelVar = "GRAPHIFY_OLLAMA_PARALLEL"

	// WantOllamaKeepAlive is how long a server the board starts holds the
	// model. It spans the gap between one job finishing and the next starting
	// — a sweep across 128 repositories is a sequence of graphify processes,
	// and a model unloaded between two of them is a cold load per repository.
	WantOllamaKeepAlive = "30m"

	// MaxLocalLanes is the ceiling the settings spinner offers for concurrent
	// jobs against one local server, and the ceiling the request timeout
	// scales to.
	//
	// Eight is where a desktop stops being able to pretend. Each lane is
	// another graphify process holding another corpus in memory and running
	// its own cpu_count-wide AST pass, and — since each also keeps one chunk
	// per server slot in flight — the eighth lane's requests wait behind seven
	// rounds of somebody else's generation. The timeout scales with it, so
	// they do not fail; they are simply not faster, and past this the machine
	// starts swapping instead of working.
	MaxLocalLanes = 8

	// MaxOllamaSlots caps the derived slot count. Past four, an extraction is
	// no longer bounded by the model at all: the chunks queue inside the
	// server, every queued one still holds its K/V cache, and the machine has
	// nothing left for the AST phase graphify runs in the same process.
	MaxOllamaSlots = 4

	// slotKVBytes is the memory one extra slot costs at WantOllamaContext, for
	// a 7B-class coder model with an 8-bit K/V cache: 28 layers x 1024 cached
	// values per token x ~1 byte is a little under 31 KB a token, so a 32768
	// slot is about a gigabyte. It is a scale, not a measurement — the number
	// varies by model — and it is used only to refuse a slot the machine
	// plainly cannot hold, never to promise one it can.
	slotKVBytes = 1 << 30
)

// graphify's request shape, restated here only so the chunk budget below can
// be derived rather than guessed. The first two are read out of graphify
// 0.9.58's llm.py: the extraction system prompt is a little over 3300
// characters, and the OpenAI-compatible call asks for 8192 output tokens.
const (
	extractionSystemTokens = 1024
	extractionOutputTokens = 8192
	// extractionHeadroom covers the gap between graphify's estimate of a
	// chunk and the model's own count of it. graphify packs chunks with
	// tiktoken plus a flat per-file overhead; the server tokenizes the real
	// thing, wrapper tags and all, with the model's tokenizer. Measured on a
	// 4096 slot the two differ by tens of tokens in the wrong direction —
	// close enough to the truncation limit to land on it. Being a few hundred
	// tokens pessimistic costs a little speed; being one token optimistic
	// costs the whole run, silently.
	extractionHeadroom = 512
	// charsPerToken is the crude ratio used only to state a chunk budget in
	// units a person can check against a file listing. English prose and code
	// both land near four characters a token; the number is a scale, never an
	// input to the budget itself.
	charsPerToken = 4
)

// LocalAPITimeoutSeconds is the per-request timeout the board passes for a
// local backend, replacing graphify's 600-second default.
//
// A 7B model on CPU generates at single-digit tokens a second — 3.7 t/s
// measured here on qwen2.5-coder:7b — and graphify asks for up to 8192 output
// tokens per chunk. 600 seconds is under half of what one full reply needs, so
// a chunk that answers at length is killed mid-answer and reported as
// "Request timed out"; graphify then drops those files from the graph and the
// shrink guard fails the whole run over them. Thirty minutes is generous for
// the replies a correctly sized chunk produces, and still bounds the runaway
// case (a truncated prompt that never emits a stop token) rather than letting
// a sweep hang on one file forever.
const LocalAPITimeoutSeconds = 1800

// ApplyLocalSizing fits a job to the local model server it is about to run
// against: chunks sized to the measured context slot, and a request timeout
// matched to how slowly that server answers. A metered backend is left exactly
// as it was, so graphify's own defaults still stand there.
//
// It lives here rather than in the GUI because the board is not the only thing
// that submits jobs — ggraphify-job runs the same commands headlessly, and a
// sweep driven through it would otherwise send graphify's default 60_000-token
// chunks at an ollama slot that cannot hold them. One caller getting the
// sizing and the other not is the kind of split that only shows up as a graph
// full of prose hours later.
func ApplyLocalSizing(p *Params) {
	if p == nil || !IsLocalBackend(p.Backend) {
		return
	}
	p.TokenBudget = LocalTokenBudget(p.Backend)
	p.APITimeout = LocalAPITimeout(p.Backend, p.LocalLanes)
	// And chunks in flight to match the slots the server actually has. This is
	// the one parameter that changes how long a run takes rather than whether
	// it succeeds: a two-slot server driven one chunk at a time finishes in
	// twice the wall-clock it needs to, with half the machine idle throughout.
	//
	// It is deliberately derived from the server rather than from this
	// machine's core count. Asking for more chunks than there are slots does
	// not make the server faster — the surplus queues inside it, each queued
	// request holding its share of the K/V cache — and that is precisely the
	// VRAM-pressure failure graphify's own clamp was written to prevent.
	p.MaxConcurrency = LocalConcurrency(p.Backend)
}

// LocalConcurrency is how many semantic chunks the board asks graphify to keep
// in flight against a local server, and 0 — leave graphify's own default
// alone — whenever the answer is "one", which graphify already enforces.
//
// Only ollama is derived. A generic OpenAI-compatible server (llama-server,
// vLLM) is not clamped by graphify at all, serves concurrently by design, and
// publishes no slot count this board could read: graphify's default of four is
// as good a number as anything measurable from here.
func LocalConcurrency(backend string) int {
	if strings.TrimSpace(backend) != OllamaBackend {
		return 0
	}
	if n := ProbeLocal(OllamaBackend).Slots; n > 1 {
		return n
	}
	return 0
}

// LocalEnv is the job-environment half of the same decision, and the half
// without which the other does nothing.
//
// graphify clamps its own max_concurrency back to 1 for the ollama backend
// unless GRAPHIFY_OLLAMA_PARALLEL is set — so a --max-concurrency flag alone
// is silently discarded, and the run looks exactly like the serial one it was
// meant to replace. The flag is set from ApplyLocalSizing and the variable
// here; the two are only ever correct together.
//
// It is never set for a server with one slot. Unlocking the clamp there would
// hand graphify's default of four chunks to a server that can hold one, which
// queues three of them against a K/V cache sized for none — the hollow-200
// failure the clamp exists for. And an explicit value in the user's own
// overlay wins outright: somebody who wrote GRAPHIFY_OLLAMA_PARALLEL=0 meant
// to turn this off.
func LocalEnv(e Env, backend string) Env {
	if strings.TrimSpace(backend) != OllamaBackend {
		return e
	}
	if LocalConcurrency(backend) <= 1 {
		return e
	}
	if _, ok := e[GraphifyOllamaParallelVar]; ok {
		return e
	}
	c := e.Clone()
	if c == nil {
		c = Env{}
	}
	c[GraphifyOllamaParallelVar] = "1"
	return c
}

// LocalAPITimeout is the per-request timeout for a local backend, and 0 —
// leave graphify's own default alone — for a metered one, whose server is not
// the slow party.
//
// It scales with the number of jobs sharing the server, because those jobs
// share its slots too. With `lanes` jobs each keeping one chunk per slot in
// flight, a request can sit in the server's queue behind up to lanes-1 rounds
// of other work before it is even dispatched — and the OpenAI SDK's timeout
// covers the queue wait, not just the generation. A fixed 1800 seconds would
// therefore turn the second lane into a source of timeouts: chunks killed
// while still queued, their files dropped from the graph, and the run failed
// on the shrink guard. Exactly the failure this timeout was raised to fix,
// re-introduced by a setting meant to make things faster.
//
// The runaway case it bounds is unaffected: a truncated prompt that never
// emits a stop token still hits a wall, just a proportionate one.
func LocalAPITimeout(backend string, lanes int) int {
	if !IsLocalBackend(backend) {
		return 0
	}
	if lanes < 1 {
		lanes = 1
	}
	if lanes > MaxLocalLanes {
		lanes = MaxLocalLanes
	}
	return LocalAPITimeoutSeconds * lanes
}

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

	// Ctx is the context window the server has actually allocated for the
	// model it currently has loaded, and 0 when that could not be measured —
	// a server with nothing loaded, or one that is not ollama. It is NOT the
	// model's trained context: see CtxMax for that, and see LocalTokenBudget
	// for why the difference between the two is the whole reason this field
	// exists.
	Ctx int
	// CtxMax is the largest context the loaded model was trained for, which is
	// the ceiling OllamaContextVar could be raised to. 0 when unknown.
	CtxMax int
	// CtxModel names the model Ctx was measured on, which need not be the one
	// the board is set to run.
	CtxModel string

	// Slots is how many requests the server answers at once — the number that
	// decides whether an extraction uses this machine or a quarter of it. It
	// is 1 both when the server really serves one at a time and when the
	// answer could not be established, which are not the same thing: SlotsWhy
	// says which, so the UI can distinguish "configured for one" from "not
	// readable from here" instead of blaming the user for either.
	Slots    int
	SlotsWhy string
}

// fetchOllamaContext asks ollama's NATIVE api what slot the loaded model is
// running in, and what slot it could run in.
//
// It has to be the native API: /v1/models is the compatibility surface and
// says nothing about context at all, and the compatibility surface is also
// precisely the one that drops the num_ctx graphify sends.
//
// What it reports is the PER-SLOT context, not the server's total, which is
// the number the budget needs: measured against a server running
// `-c 65536 -np 2`, /api/ps answered 32768. So a server configured for
// concurrency is sized correctly without the caller knowing how many slots it
// was split into. /api/ps answers
// only while a model is loaded — a cold server returns an empty list, and the
// honest answer there is "not measured", not a guess.
func fetchOllamaContext(base string) (ctx, ctxMax int, model string) {
	root := strings.TrimSuffix(strings.TrimSuffix(strings.TrimSpace(base), "/"), "/v1")
	client := &http.Client{Timeout: 2 * time.Second}

	get := func(path string) []ollamaModelInfo {
		resp, err := client.Get(root + path)
		if err != nil {
			return nil
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil
		}
		var body struct {
			Models []ollamaModelInfo `json:"models"`
		}
		if json.NewDecoder(resp.Body).Decode(&body) != nil {
			return nil
		}
		return body.Models
	}

	for _, m := range get("/api/ps") {
		if m.ContextLength > 0 {
			ctx, model = m.ContextLength, m.Model
			break
		}
	}
	if model == "" {
		return 0, 0, ""
	}
	// The trained ceiling comes from the model catalogue rather than from the
	// running slot, and only for the model actually loaded — quoting another
	// model's ceiling as the headroom available would be worse than quoting
	// none.
	for _, m := range get("/api/tags") {
		if m.Model == model || m.Name == model {
			ctxMax = m.Details.ContextLength
			break
		}
	}
	return ctx, ctxMax, model
}

// ollamaModelInfo is the intersection of /api/ps and /api/tags that this board
// reads. Both endpoints return a `models` array; the running slot is reported
// at the top level and the trained ceiling inside `details`.
type ollamaModelInfo struct {
	Name          string `json:"name"`
	Model         string `json:"model"`
	ContextLength int    `json:"context_length"`
	Details       struct {
		ContextLength int `json:"context_length"`
	} `json:"details"`
}

// ollamaSlots establishes how many requests the server answers at once, and
// says where the number came from.
//
// There is no endpoint for this. /api/ps reports the PER-SLOT context and
// nothing about the slot count, and ollama publishes its own configuration
// nowhere on the wire — so the question is answered by reading the
// configuration of the process that is serving, which is only possible when
// that process is on this machine and under a manager that will say.
//
// The order is authority-first, not convenience-first. A systemd unit's
// Environment= is what the running server was actually given; this process's
// own environment is what a server started FROM this session would have been
// given, which is the right answer for a detached `ollama serve` and the wrong
// one for a system unit configured differently. Getting that order backwards
// would have the board confidently drive two chunks at a one-slot server on
// the strength of a variable in the user's shell profile.
func ollamaSlots(base string) (int, string) {
	// Stated beats derived. Whoever set this knows the server; the code below
	// only ever infers it.
	if v := strings.TrimSpace(os.Getenv(OllamaSlotsVar)); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n, OllamaSlotsVar + "=" + strconv.Itoa(n)
		}
	}
	// A server somewhere else is a server whose configuration is not on this
	// disk. Guessing at it is how a remote one-slot server gets four chunks.
	if !IsLocalURL(base) {
		return 1, "not measurable for a server on another host — assuming one at a time; " +
			"set " + OllamaSlotsVar + " if you know it serves more"
	}
	for _, u := range []struct {
		scope []string
		what  string
	}{
		{[]string{"--user"}, "the per-user ollama.service"},
		{nil, "the system ollama.service"},
	} {
		if u.scope != nil && !unitEnabled("ollama.service", u.scope...) {
			continue
		}
		if u.scope == nil && !unitExists("ollama.service") {
			continue
		}
		env := unitEnviron("ollama.service", u.scope...)
		if v, ok := env[OllamaParallelVar]; ok {
			if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n > 0 {
				return n, OllamaParallelVar + "=" + strconv.Itoa(n) + " in " + u.what
			}
		}
		return 1, u.what + " sets no " + OllamaParallelVar + ", so ollama serves one at a time"
	}
	if v := strings.TrimSpace(os.Getenv(OllamaParallelVar)); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n, OllamaParallelVar + "=" + strconv.Itoa(n) + " in this session's environment"
		}
	}
	return 1, "no " + OllamaParallelVar + " anywhere this board can read, so one at a time"
}

// unitEnviron reads a unit's Environment= as a map, without starting anything.
//
// systemd prints the whole block on one line, space-separated, quoting only
// values that contain spaces — and none of ollama's do, which is why this
// splits on whitespace rather than carrying a shell parser. A value that did
// contain a space would be read wrong; it would also not be a value this
// function is ever asked about.
func unitEnviron(unit string, scope ...string) map[string]string {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return nil
	}
	args := append(append([]string{}, scope...), "show", unit, "--property=Environment")
	out, err := exec.Command("systemctl", args...).Output()
	if err != nil {
		return nil
	}
	line := strings.TrimSpace(string(out))
	line = strings.TrimPrefix(line, "Environment=")
	m := map[string]string{}
	for _, f := range strings.Fields(line) {
		k, v, ok := strings.Cut(f, "=")
		if ok {
			m[k] = strings.Trim(v, `"`)
		}
	}
	return m
}

// OllamaSizing is the server configuration the board starts ollama with, and
// the one it measures a running server against.
type OllamaSizing struct {
	// Slots is the parallel-request count this machine can actually feed.
	Slots int
	// PerSlot is the context each slot gets. Total is what OllamaParallelVar's
	// sibling OllamaContextVar has to be set to in order to produce it:
	// ollama DIVIDES the configured context across the slots, so a server told
	// 32768 with two slots serves two 16384 slots, not two 32768 ones. Halving
	// the usable chunk while believing you had widened it is the exact shape
	// of the bug this field exists to prevent.
	PerSlot int
	Total   int
	// Why is the one-line derivation, for the log and the settings row.
	Why string
}

// SizeOllama derives what a server on THIS machine should be run with.
//
// Two bounds, and the smaller wins. Cores, because a slot is not free even
// when the weights are on a GPU — prompt processing, sampling and the
// server's own bookkeeping are CPU, and graphify runs a cpu_count-wide AST
// phase in the same wall-clock window, so handing every core to the model
// makes the run slower rather than faster. Memory, because a slot's K/V cache
// is resident for as long as the slot exists and a machine that swaps to hold
// it is a machine doing nothing else at all.
//
// The result is deliberately modest. "Use the whole computer" means every part
// of it doing useful work at once, not one part of it oversubscribed while the
// rest waits on it.
func SizeOllama() OllamaSizing {
	s := OllamaSizing{PerSlot: WantOllamaContext}

	byCPU := runtime.NumCPU() / 4
	if byCPU < 1 {
		byCPU = 1
	}
	if byCPU > MaxOllamaSlots {
		byCPU = MaxOllamaSlots
	}
	s.Slots = byCPU
	s.Why = strconv.Itoa(byCPU) + " slot(s) for " + strconv.Itoa(runtime.NumCPU()) + " cores"

	// Half of what is available now, because the other half is the model's own
	// weights, the AST phase, and the desktop this is running on.
	if avail := memAvailableBytes(); avail > 0 {
		byMem := int(avail / 2 / slotKVBytes)
		if byMem < 1 {
			byMem = 1
		}
		if byMem < s.Slots {
			s.Slots = byMem
			s.Why = strconv.Itoa(byMem) + " slot(s) for " +
				strconv.FormatInt(avail>>30, 10) + " GiB available memory"
		}
	}
	s.Total = s.Slots * s.PerSlot
	return s
}

// memAvailableBytes is MemAvailable from /proc/meminfo — what can be handed
// out without swapping — and 0 where that cannot be read, which makes the
// memory bound above fall away rather than guess.
func memAvailableBytes() int64 {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(b), "\n") {
		rest, ok := strings.CutPrefix(line, "MemAvailable:")
		if !ok {
			continue
		}
		f := strings.Fields(rest)
		if len(f) == 0 {
			return 0
		}
		kb, err := strconv.ParseInt(f[0], 10, 64)
		if err != nil {
			return 0
		}
		return kb * 1024
	}
	return 0
}

// LocalTokenBudget is the per-chunk corpus budget (graphify's --token-budget)
// that fits a local server's context slot, or 0 when the slot is unknown and
// graphify's own default should be left alone.
//
// This is the fix for the failure mode that motivated the whole function.
// graphify packs files into chunks of up to 60_000 tokens by default and
// derives an ollama num_ctx to match — but it sends that num_ctx inside
// `extra_body.options` on the OpenAI-COMPATIBLE endpoint, and ollama's
// compatibility layer does not read `options`. The server therefore keeps
// whatever slot it was started with, 4096 unless OllamaContextVar said
// otherwise, and llama.cpp then truncates the oversized prompt from the FRONT
// with n_keep=4:
//
//	msg="truncating input prompt" limit=2050 prompt=7394 keep=4 new=2050
//
// The four tokens kept are the head of the system prompt. Everything that told
// the model to answer in JSON, and the schema it was to answer with, is gone
// before the model sees a thing — so it does the only sensible remaining
// thing and describes the file in prose. graphify reads that as invalid JSON,
// calls the chunk hollow, retries it twice more into the same truncation,
// gives up, and finishes with zero semantic nodes; the shrink guard then
// refuses to overwrite the older, larger graph and the run exits non-zero.
// Hours of a CPU-bound model, and nothing written.
//
// Nothing on the client side can widen the slot, so the chunk is sized to it
// instead. Room = the slot, less what ollama reserves for the reply (it caps
// a request for more than half the slot at half the slot), less graphify's
// own system prompt.
func LocalTokenBudget(backend string) int {
	return contextTokenBudget(ProbeLocal(backend).Ctx)
}

func contextTokenBudget(ctx int) int {
	if ctx <= 0 {
		return 0
	}
	out := extractionOutputTokens
	if half := ctx / 2; out > half {
		out = half
	}
	budget := ctx - out - extractionSystemTokens - extractionHeadroom
	// Below 512 tokens a chunk cannot hold one useful file and the number
	// would be noise on the command line; leave graphify's default and let the
	// warning elsewhere carry the message instead. ollama's stock 4096 slot
	// lands exactly on this floor, which is the honest answer for it: it works,
	// and it is the reason ContextAdvice asks for a wider one.
	if budget < 512 {
		return 0
	}
	return budget
}

// ContextTooSmall reports whether a measured slot is ollama's stock one, which
// works only because the budget above shrinks every chunk to fit it. It is a
// warning and never a refusal: a narrow slot makes a run slower and its chunks
// less connected, but it no longer makes the run fail.
func (p LocalProbe) ContextTooSmall() bool {
	return p.Ctx > 0 && p.Ctx <= DefaultOllamaContext
}

// ContextAdvice is the one-paragraph explanation of a narrow slot, and the
// command that widens it. Empty when there is nothing to say.
//
// The command depends on how ollama is installed, because the two cases are
// not interchangeable: a system unit is edited with a drop-in and needs root,
// and anything else takes the variable from the environment it is started in.
func (p LocalProbe) ContextAdvice() string {
	if !p.ContextTooSmall() {
		return ""
	}
	want := WantOllamaContext
	if p.CtxMax > 0 && p.CtxMax < want {
		want = p.CtxMax
	}
	b := &strings.Builder{}
	b.WriteString("The server is running " + p.CtxModel + " in a " + strconv.Itoa(p.Ctx) +
		"-token context — ollama's default, not the model's")
	if p.CtxMax > 0 {
		b.WriteString(", which is " + strconv.Itoa(p.CtxMax))
	}
	b.WriteString(". The board caps every chunk to fit it, which holds for a chunk packed " +
		"out of several small files — but a SINGLE file bigger than the cap cannot be " +
		"split any further, and graphify sends it whole. In a slot this narrow that is " +
		"most files over about " + strconv.Itoa(contextTokenBudget(p.Ctx)*charsPerToken/1024) +
		" KB: the prompt is truncated from the front, the chunk comes back as prose or " +
		"runs past its timeout, and those files are missing from the graph. Widen the " +
		"slot with " + OllamaContextVar + "=" + strconv.Itoa(want) + " on the SERVER — it " +
		"is read when a model is loaded, so nothing a client sends can change it.")
	if systemOwnsOllama() {
		b.WriteString(" Here a system unit owns ollama: `sudo systemctl edit ollama` and add " +
			"Environment=\"" + OllamaContextVar + "=" + strconv.Itoa(want) + "\", then " +
			"`sudo systemctl restart ollama`.")
	}
	return b.String()
}

// ThroughputAdvice is ContextAdvice's sibling for the other half of the
// server's sizing: it says so when the machine could serve more requests at
// once than the server is configured to, and gives the command that changes
// it. Empty when the server is already sized to the machine, which is the
// common case once it has been set once.
//
// The two are separate paragraphs because they are separate failures with
// separate consequences. A narrow context slot makes a run produce a WRONG
// graph — truncated prompts, prose instead of JSON, files silently missing. A
// single slot makes the same run produce the RIGHT graph in four times the
// wall clock. Only the first is a warning; this one is an offer.
func (p LocalProbe) ThroughputAdvice() string {
	if !p.Reach || p.Backend != OllamaBackend {
		return ""
	}
	want := SizeOllama()
	if p.Slots >= want.Slots {
		return ""
	}
	b := &strings.Builder{}
	b.WriteString("The server answers " + strconv.Itoa(p.Slots) + " request at a time (" +
		p.SlotsWhy + "), so an extraction uses a fraction of this machine and waits on " +
		"the rest. It has room for " + strconv.Itoa(want.Slots) + " (" + want.Why + "): set " +
		OllamaParallelVar + "=" + strconv.Itoa(want.Slots) + " and " + OllamaContextVar + "=" +
		strconv.Itoa(want.Total) + " on the SERVER. Both matter together — ollama DIVIDES " +
		OllamaContextVar + " across the slots, so raising the slot count alone would halve " +
		"every chunk the board is allowed to send.")
	if systemOwnsOllama() {
		b.WriteString(" Here a system unit owns ollama: `sudo systemctl edit ollama` and add " +
			"Environment=\"" + OllamaParallelVar + "=" + strconv.Itoa(want.Slots) + "\" and " +
			"Environment=\"" + OllamaContextVar + "=" + strconv.Itoa(want.Total) + "\", then " +
			"`sudo systemctl restart ollama`.")
	}
	return b.String()
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
	// The context slot is an ollama question and only worth asking of a server
	// that answered: it is a second and third HTTP call, and on anything but
	// ollama the native paths 404.
	if p.Reach && backend == OllamaBackend {
		p.Ctx, p.CtxMax, p.CtxModel = fetchOllamaContext(base)
		// The slot count is not on the wire at all, so it is read off the
		// manager of the process that is serving. Cached with the rest of the
		// probe: it costs up to two systemctl subprocesses, and the settings
		// page renders on every redraw.
		p.Slots, p.SlotsWhy = ollamaSlots(base)
	}

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
	// How many chunks will be in flight is the difference between a run that
	// finishes tonight and one that finishes tomorrow, so it is stated rather
	// than left for the reader to infer from the argv.
	if n := LocalConcurrency(backend); n > 1 {
		b.WriteString("Local models are slow, so the board drives " + strconv.Itoa(n) +
			" chunks at once — one per request slot this server has (" + p.SlotsWhy +
			"). A large repository still takes minutes to hours.\n")
	} else if backend == OllamaBackend {
		b.WriteString("Local models are slow, and this server answers one request at a " +
			"time (" + p.SlotsWhy + "), so graphify sends one chunk at a time: a large " +
			"repository takes minutes to hours.\n")
	} else {
		b.WriteString("Local models are slow: a large repository takes minutes to hours.\n")
	}
	// The chunk size is not a detail here. It is the difference between a run
	// that writes a graph and one that spends hours producing prose, so it
	// goes in the argv AND in the paragraph above it.
	if n := contextTokenBudget(p.Ctx); n > 0 {
		b.WriteString("Context slot: " + strconv.Itoa(p.Ctx) + " tokens, so chunks are capped at " +
			"--token-budget " + strconv.Itoa(n) + " to fit it.\n")
	}
	if a := p.ContextAdvice(); a != "" {
		b.WriteString("\n" + a + "\n")
	}
	if a := p.ThroughputAdvice(); a != "" {
		b.WriteString("\n" + a + "\n")
	}
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
		// The one moment the board gets to size the server. Every variable
		// below is read by the server process at startup or at model-load
		// time, so this is the only route by which ggraphify can set any of
		// them: a client cannot widen a slot, add a slot, or keep a model
		// resident, however it phrases the request.
		//
		// An explicit value already in the environment always wins — somebody
		// who set one meant it — so each is a default, not an override.
		tuning := ollamaServerEnv(SizeOllama())
		cmd.Env = append(os.Environ(), tuning...)
		if len(tuning) > 0 {
			how += " with " + strings.Join(tuning, " ")
		}
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

// ollamaServerEnv is the tuning a server the board starts itself is given,
// as KEY=VALUE, omitting anything the environment already sets.
//
// The pairing of the first two is the whole point and the easiest thing to get
// wrong: ollama DIVIDES OllamaContextVar across OllamaParallelVar slots. Two
// slots and a 32768 context is two 16384 slots — half the chunk the board
// would then size against — so the total is always slots x per-slot, and the
// two are never set apart from one another.
//
// The rest are what make those slots affordable and keep them warm: flash
// attention and an 8-bit K/V cache roughly halve what a slot's cache costs,
// one loaded model stops a second from evicting the first mid-sweep, and a
// 30-minute keep-alive spans the gap between one repository's graphify process
// and the next one's.
func ollamaServerEnv(s OllamaSizing) []string {
	want := [][2]string{
		{OllamaParallelVar, strconv.Itoa(s.Slots)},
		{OllamaContextVar, strconv.Itoa(s.Total)},
		{OllamaFlashAttnVar, "1"},
		{OllamaKVCacheVar, "q8_0"},
		{OllamaMaxLoadedVar, "1"},
		{OllamaKeepAliveVar, WantOllamaKeepAlive},
	}
	out := make([]string, 0, len(want))
	for _, kv := range want {
		if strings.TrimSpace(os.Getenv(kv[0])) != "" {
			continue
		}
		out = append(out, kv[0]+"="+kv[1])
	}
	return out
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

// systemOwnsOllama reports whether the distro's system-wide unit is the one
// running ollama here, which decides which command widens its context slot.
//
// Memoised because ContextAdvice is rendered on the main thread, into a
// settings row that redraws, and three systemctl subprocesses per redraw is
// three too many. How ollama is installed does not change while the board is
// open; if it does, so does the answer on the next launch.
func systemOwnsOllama() bool {
	systemUnitOnce.Do(func() {
		systemUnitOnce.owns = unitExists("ollama.service") &&
			!unitEnabled("ollama.service", "--user")
	})
	return systemUnitOnce.owns
}

var systemUnitOnce struct {
	sync.Once
	owns bool
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

// EmbeddingModel reports whether a served model name is an embedding or
// reranking model rather than a chat one.
//
// It exists for exactly one caller — the unattended fix loop, which may have
// to choose a model with nobody to ask — and it is a name test because the
// OpenAI-compatible model list carries no capability flag. A false negative
// costs one failed run; a false positive costs a model that is never picked
// automatically and can still be chosen by hand in settings. The list is the
// families ollama actually ships as embedders.
func EmbeddingModel(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	for _, s := range []string{
		"embed", "rerank", "bge-", "gte-", "e5-", "minilm", "all-mpnet",
	} {
		if strings.Contains(n, s) {
			return true
		}
	}
	return false
}

// AutoLocalModel resolves the model an UNATTENDED run should send to a local
// server, which is a different question from the one LocalReady answers.
//
// LocalReady is for a person who chose a model and is owed a refusal naming
// the model they chose. This is for the auto-fix loop, which runs with nobody
// watching and which must not stall a night's repairs because the board's
// model setting names a frontier model — the ordinary state of a board whose
// backend is claude-cli and whose local server is only there as a fallback.
//
// So it prefers, in order: the model asked for, graphify's own default, and
// then whatever the server actually serves — skipping embedding models, which
// answer a chat request with a 400 and would otherwise be picked whenever they
// sort first. It never starts a server and never pulls anything; a machine
// with no model gets a refusal, not a download.
func AutoLocalModel(backend, preferred string) (model string, ok bool, why string) {
	if !IsLocalBackend(backend) {
		return "", false, backend + " is not a model on this machine."
	}
	p := ProbeLocal(backend)
	if !p.Reach || len(p.Models) == 0 {
		// LocalReady already phrases every way this fails, with the fix for
		// each; there is no second wording worth having.
		_, why := LocalReady(backend, preferred)
		return "", false, why
	}
	if want := strings.TrimSpace(preferred); want != "" && p.Has(want) {
		return want, true, ""
	}
	if want := LocalModel(backend, ""); want != "" && p.Has(want) {
		return want, true, ""
	}
	for _, m := range p.Models {
		if !EmbeddingModel(m) {
			return m, true, ""
		}
	}
	return "", false, p.BaseURL + " serves only embedding models (" +
		strings.Join(p.Models, ", ") + "), none of which can answer an extraction."
}
