package gfy

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dns/ggraphify/internal/applog"
)

// OpenCode Go refuses a request that does not identify its conversation:
//
//	POST /zen/go/v1/chat/completions   (no x-opencode-session)
//	→ 400 MissingSessionID — "Request is missing x-opencode-session and
//	  cannot be routed efficiently"
//
// Every model on the plan answers that way, so the header is a requirement
// and not the recommendation the plan's documentation makes it sound like.
// graphify cannot send it: llm.py builds its client as
// OpenAI(api_key, base_url, timeout, max_retries) with no default_headers,
// and no provider field or environment variable adds one.
//
// So the board puts itself in the path. This is a proxy on loopback that
// forwards one upstream — the gateway, and nothing else — adding the two
// headers the plan asks a client for: a stable session id, and a User-Agent
// that names this application instead of the Python SDK. The provider entry
// in ~/.graphify/providers.json points at the proxy, which is why that entry
// reads http://127.0.0.1:… rather than the URL a human would expect;
// llm.py's own base_url check treats a loopback http endpoint as the one safe
// plaintext case, so it passes without a warning.
//
// What it deliberately is not: a place the credential is handled. The
// Authorization header graphify composed from OPENCODE_API_KEY is copied
// across untouched and is never read, logged or stored, and the proxy accepts
// connections from this machine only.
const (
	// OpenCodePortVar moves the proxy's port, for a machine where something
	// else owns the default. 0 asks the kernel for any free port, which is
	// what the tests use.
	OpenCodePortVar = "GGRAPHIFY_OPENCODE_PORT"

	// DefaultOpenCodePort is high, fixed and otherwise unclaimed. Fixed
	// matters: the URL is written into a file another ggraphify process may
	// read, and a port that moved every launch would leave stale entries
	// pointing at nothing.
	DefaultOpenCodePort = 11437

	// openCodeProbePath answers "is the thing on that port ours?" — the
	// question that decides whether a bound port is a second ggraphify to
	// share with or a stranger to refuse.
	openCodeProbePath = "/__ggraphify/opencode"
	openCodeProbeBody = "ggraphify opencode-go proxy"

	// openCodeProbePlans is appended to the probe body, and is the whole
	// reason the body is parsed rather than only prefix-matched. A proxy
	// bound by an OLDER ggraphify serves the Go path and 404s the Zen one, so
	// a board that inherited it and wrote a Zen entry pointing at it would
	// have produced a run that failed every chunk on a path that does not
	// exist. Sharing is still right when the other end can serve the plan
	// being asked for; it is refused, by name, when it cannot.
	openCodeProbePlans = " plans=go,zen"
)

// AppVersion is ggraphify's own version, set by main from the value the
// Makefile stamps. It exists here for one reason: the User-Agent this proxy
// sends, which OpenCode Go asks to be the client's own rather than a generic
// SDK name.
var AppVersion = "dev"

var proxyState struct {
	sync.Mutex
	root    string // http://127.0.0.1:port — the proxy, without a plan's path
	base    string // root + the Go plan's path, which is what Activity reports
	session string
	// owned is true when THIS process bound the port. A proxy inherited from
	// another ggraphify is served by that process and outlives this one, so
	// quitting here takes nothing away from anybody; a proxy we bound goes
	// down with us, which is the fact the quit dialog is about.
	owned bool
	// requests and last are what that dialog needs: this gateway is useful to
	// tools outside ggraphify — a fleet-wide `graft build --deep` runs against
	// it — and an external consumer whose LLM vanishes mid-run sees only a
	// stream of connection refusals. Counting the traffic turns "quitting
	// silently breaks something" into "quitting will break this, carry on?".
	requests int
	last     time.Time
}

// OpenCodeProxyActivity reports what the loopback gateway has been doing:
// whether this process is the one serving it, how many requests it has
// forwarded, and when the last one was.
//
// It is deliberately a count of requests and not of clients: the proxy cannot
// tell graphify's own chunk from another tool's, and pretending otherwise
// would let a quit dialog claim nobody is out there when somebody is.
func OpenCodeProxyActivity() (base string, owned bool, requests int, last time.Time) {
	proxyState.Lock()
	defer proxyState.Unlock()
	return proxyState.base, proxyState.owned, proxyState.requests, proxyState.last
}

// noteProxyRequest records one forwarded exchange.
func noteProxyRequest() {
	proxyState.Lock()
	proxyState.requests++
	proxyState.last = time.Now()
	proxyState.Unlock()
}

// OpenCodeSession is the conversation id every proxied request carries.
//
// One per process: a sweep of many repositories from one board is one session
// for the gateway's routing and prompt caching, which is the granularity the
// plan's documentation describes. Finer than that is not available to a proxy
// — graphify's requests carry nothing that says which job they belong to.
func OpenCodeSession() string {
	proxyState.Lock()
	defer proxyState.Unlock()
	if proxyState.session == "" {
		b := make([]byte, 12)
		if _, err := rand.Read(b); err != nil {
			// A session id only has to be stable and unique-ish; the clock is
			// a fine fallback for the one machine that cannot give us bytes.
			proxyState.session = "ggraphify-" + strconv.FormatInt(time.Now().UnixNano(), 16)
			return proxyState.session
		}
		proxyState.session = "ggraphify-" + hex.EncodeToString(b)
	}
	return proxyState.session
}

// OpenCodeProxyPort is the port the proxy listens on.
func OpenCodeProxyPort() int {
	if v := strings.TrimSpace(os.Getenv(OpenCodePortVar)); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 && n <= 65535 {
			return n
		}
	}
	return DefaultOpenCodePort
}

// OpenCodeProxyBase is the URL a plan's provider entry should carry, given
// the proxy's root. One port, one path per plan: the handler reads the path to
// know which gateway to forward to, so the two plans cannot be confused by
// anything the client does or does not send.
func OpenCodeProxyBase(root, backend string) string {
	return strings.TrimSuffix(root, "/") + OpenCodePlanFor(backend).ProxyPath
}

// StartOpenCodeProxy brings the proxy up if it is not already, and returns the
// base URL a provider entry for this plan should carry. It is idempotent: the
// second call in a process returns the first call's answer, and a port already
// held by another ggraphify's proxy is shared rather than fought over.
func StartOpenCodeProxy(backend string) (string, error) {
	plan := OpenCodePlanFor(backend)
	proxyState.Lock()
	if proxyState.root != "" {
		root := proxyState.root
		proxyState.Unlock()
		return OpenCodeProxyBase(root, plan.Backend), nil
	}
	proxyState.Unlock()

	port := OpenCodeProxyPort()
	addr := "127.0.0.1:" + Itoa(port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		// Something has the port. If it is another ggraphify, that is not a
		// conflict — its proxy adds the same headers to the same upstream, so
		// sharing it is correct and costs nothing.
		if root, plans, ok := probeOpenCodeProxy(addr); ok {
			if !strings.Contains(plans, "zen") && plan.Backend == OpenCodeZenBackend {
				return "", errors.New("the proxy on " + addr + " belongs to an older ggraphify " +
					"that only serves the OpenCode Go path, so it cannot carry an " +
					OpenCodeZenBackend + " run. Quit that board, or set " + OpenCodePortVar +
					" to another port for this one")
			}
			proxyState.Lock()
			proxyState.root = root
			proxyState.base = OpenCodeProxyBase(root, OpenCodeBackend)
			proxyState.Unlock()
			applog.Infof("opencode proxy: sharing the one another ggraphify serves at %s (plans=%s)", root, plans)
			return OpenCodeProxyBase(root, plan.Backend), nil
		}
		applog.Errorf("opencode proxy: cannot bind %s and the listener there is not ggraphify's: %v", addr, err)
		return "", errors.New("the OpenCode Go proxy cannot bind " + addr + " (" + err.Error() +
			") and what is listening there is not one of ours. Free the port, or set " +
			OpenCodePortVar + " to another one")
	}

	actual := ln.Addr().(*net.TCPAddr).Port
	root := "http://127.0.0.1:" + Itoa(actual)

	srv := &http.Server{
		Handler: openCodeProxyHandler(),
		// Not a timeout on the exchange: a model answering a 60k-token chunk
		// legitimately takes minutes, and graphify has its own deadline for
		// that. This one only bounds how long a client may dawdle over the
		// request line, which is what gosec's G112 is about.
		ReadHeaderTimeout: 15 * time.Second,
	}
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			applog.Infof("opencode-go proxy stopped: %v", err)
		}
	}()

	proxyState.Lock()
	proxyState.root = root
	proxyState.base = OpenCodeProxyBase(root, OpenCodeBackend)
	proxyState.owned = true
	proxyState.Unlock()
	applog.Infof("opencode proxy: listening on %s → %s (go), %s (zen)", root,
		OpenCodeUpstream(OpenCodeBackend), OpenCodeUpstream(OpenCodeZenBackend))
	return OpenCodeProxyBase(root, plan.Backend), nil
}

// openCodeRoute maps a request path to the plan it belongs to and the part of
// the path the gateway should see after its own /v1.
//
// Zen is matched first and by its longer prefix, because "/v1" is a prefix of
// nothing here but is the fallback: a path that is neither is refused rather
// than guessed at.
func openCodeRoute(path string) (plan OpenCodePlan, rest string, ok bool) {
	for _, p := range []OpenCodePlan{OpenCodePlanFor(OpenCodeZenBackend), OpenCodePlanFor(OpenCodeBackend)} {
		if path == p.ProxyPath {
			return p, "", true
		}
		if strings.HasPrefix(path, p.ProxyPath+"/") {
			return p, strings.TrimPrefix(path, p.ProxyPath), true
		}
	}
	return OpenCodePlan{}, "", false
}

// probeOpenCodeProxy asks whatever holds an address whether it is a ggraphify
// proxy. Anything else — a different application, a silent socket — answers
// no, and the caller refuses rather than sending a corpus to it.
func probeOpenCodeProxy(addr string) (root, plans string, ok bool) {
	c := &http.Client{Timeout: 2 * time.Second}
	resp, err := c.Get("http://" + addr + openCodeProbePath) // #nosec G107 -- loopback, address composed here
	if err != nil {
		return "", "", false
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 256))
	if err != nil || !strings.HasPrefix(string(body), openCodeProbeBody) {
		return "", "", false
	}
	// "plans=…" is absent from an older proxy's body, which is precisely the
	// answer the caller needs: it serves the Go path only.
	if _, rest, found := strings.Cut(string(body), "plans="); found {
		plans, _, _ = strings.Cut(rest, " ")
	}
	return "http://" + addr, plans, true
}

// proxyTransport is shared so a sweep's requests reuse connections to the
// gateway instead of paying a TLS handshake per chunk.
var proxyTransport = &http.Transport{
	Proxy:               http.ProxyFromEnvironment,
	MaxIdleConnsPerHost: 8,
	IdleConnTimeout:     90 * time.Second,
	DialContext:         (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
	TLSHandshakeTimeout: 10 * time.Second,
}

func openCodeProxyHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(openCodeProbePath, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, openCodeProbeBody+" "+AppVersion+openCodeProbePlans)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// Two upstreams, one per plan, and only the paths they serve. Which
		// plan a request belongs to is read off the path rather than from any
		// board-wide state: the path was written into that provider entry when
		// the plan was chosen, so a request cannot arrive here meaning one
		// plan and be billed to the other. A loopback proxy that forwarded an
		// arbitrary absolute URL would be an open relay for anything else
		// running on this machine.
		plan, rest, ok := openCodeRoute(r.URL.Path)
		if !ok {
			applog.Warnf("opencode proxy: refused %s %s — not a plan path", r.Method, r.URL.Path)
			http.Error(w, "this proxy forwards "+OpenCodeBackend+"'s /v1 to "+
				OpenCodeUpstream(OpenCodeBackend)+" and "+OpenCodeZenBackend+"'s /zen/v1 to "+
				OpenCodeUpstream(OpenCodeZenBackend)+", and nothing else", http.StatusNotFound)
			return
		}
		upstream := OpenCodeUpstream(plan.Backend)
		up, err := url.Parse(strings.TrimSuffix(upstream, "/v1"))
		if err != nil {
			http.Error(w, "bad upstream: "+err.Error(), http.StatusInternalServerError)
			return
		}
		target := *up
		target.Path = strings.TrimSuffix(up.Path, "/") + "/v1" + rest
		target.RawQuery = r.URL.RawQuery

		// The body is read whole so the log can name the model the client
		// asked for — the one fact that says which model a job is really
		// running on. A chunk is at most a few hundred kilobytes, and the
		// request was never streamed to begin with.
		body, err := io.ReadAll(io.LimitReader(r.Body, maxProxyBody))
		if err != nil {
			applog.Warnf("opencode proxy: reading the request for %s: %v", r.URL.Path, err)
			http.Error(w, "opencode-go: "+err.Error(), http.StatusBadRequest)
			return
		}
		model := requestModel(body)
		started := time.Now()

		req, err := http.NewRequestWithContext(r.Context(), r.Method, target.String(), bytes.NewReader(body))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		for k, vs := range r.Header {
			if strings.EqualFold(k, "Host") || strings.EqualFold(k, "Connection") {
				continue
			}
			for _, v := range vs {
				req.Header.Add(k, v)
			}
		}
		// The two the plan asks for. Set rather than added, and only when the
		// client did not already say: a future graphify that grows a header
		// hook should win over this.
		if req.Header.Get("x-opencode-session") == "" {
			req.Header.Set("x-opencode-session", OpenCodeSession())
		}
		req.Header.Set("User-Agent", "ggraphify/"+AppVersion)

		noteProxyRequest()
		resp, err := proxyTransport.RoundTrip(req)
		if err != nil {
			applog.Errorf("opencode proxy: %s %s %s model=%s → upstream unreachable after %s: %v",
				plan.Backend, r.Method, rest, model, time.Since(started).Round(time.Millisecond), err)
			http.Error(w, "opencode-go: "+err.Error(), http.StatusBadGateway)
			return
		}
		defer func() { _ = resp.Body.Close() }()
		for k, vs := range resp.Header {
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
		// Flushed as it arrives, so a streamed completion is streamed rather
		// than buffered until the model has finished thinking.
		//
		// A refusal's body is also kept — the first few hundred bytes of it —
		// because the gateway's error message is the whole diagnosis, and
		// graphify reduces it to "chunk failed".
		var errBody bytes.Buffer
		failed := resp.StatusCode >= 400
		var sent int64
		defer func() {
			took := time.Since(started).Round(time.Millisecond)
			if failed {
				msg := gatewayErrorMessage(errBody.Bytes())
				if msg == "" {
					msg = strings.TrimSpace(errBody.String())
				}
				applog.Warnf("opencode proxy: %s %s %s model=%s → HTTP %d in %s: %s",
					plan.Backend, r.Method, rest, model, resp.StatusCode, took, msg)
				return
			}
			applog.Infof("opencode proxy: %s %s %s model=%s → HTTP %d in %s (%d bytes)",
				plan.Backend, r.Method, rest, model, resp.StatusCode, took, sent)
		}()
		buf := make([]byte, 16*1024)
		rc := http.NewResponseController(w)
		for {
			n, rerr := resp.Body.Read(buf)
			if n > 0 {
				sent += int64(n)
				if failed && errBody.Len() < 512 {
					errBody.Write(buf[:min(n, 512-errBody.Len())])
				}
				if _, werr := w.Write(buf[:n]); werr != nil {
					applog.Warnf("opencode proxy: client went away mid-response (%s model=%s): %v", rest, model, werr)
					return
				}
				_ = rc.Flush()
			}
			if rerr != nil {
				if !errors.Is(rerr, io.EOF) {
					applog.Warnf("opencode proxy: upstream response cut off (%s model=%s): %v", rest, model, rerr)
				}
				return
			}
		}
	})
	return mux
}

// maxProxyBody caps what one request may carry through the proxy. The largest
// chunk graphify sends is a few hundred kilobytes; this is far above it and
// still keeps a runaway client from filling memory.
const maxProxyBody = 64 << 20

// requestModel is the "model" field of an OpenAI-shaped request body, or "-"
// when there is none (GET /models, or a body that is not JSON).
func requestModel(body []byte) string {
	var v struct {
		Model string `json:"model"`
	}
	if json.Unmarshal(body, &v) != nil || v.Model == "" {
		return "-"
	}
	return v.Model
}
