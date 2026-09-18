package gfy

import (
	"crypto/rand"
	"encoding/hex"
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
)

// AppVersion is ggraphify's own version, set by main from the value the
// Makefile stamps. It exists here for one reason: the User-Agent this proxy
// sends, which OpenCode Go asks to be the client's own rather than a generic
// SDK name.
var AppVersion = "dev"

var proxyState struct {
	sync.Mutex
	base    string // what the provider entry should point at
	session string
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

// StartOpenCodeProxy brings the proxy up if it is not already, and returns the
// base URL the provider entry should carry. It is idempotent: the second call
// in a process returns the first call's answer, and a port already held by
// another ggraphify's proxy is shared rather than fought over.
func StartOpenCodeProxy() (string, error) {
	proxyState.Lock()
	if proxyState.base != "" {
		defer proxyState.Unlock()
		return proxyState.base, nil
	}
	proxyState.Unlock()

	port := OpenCodeProxyPort()
	addr := "127.0.0.1:" + Itoa(port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		// Something has the port. If it is another ggraphify, that is not a
		// conflict — its proxy adds the same headers to the same upstream, so
		// sharing it is correct and costs nothing.
		if base, ok := probeOpenCodeProxy(addr); ok {
			proxyState.Lock()
			proxyState.base = base
			proxyState.Unlock()
			return base, nil
		}
		return "", errors.New("the OpenCode Go proxy cannot bind " + addr + " (" + err.Error() +
			") and what is listening there is not one of ours. Free the port, or set " +
			OpenCodePortVar + " to another one")
	}

	actual := ln.Addr().(*net.TCPAddr).Port
	base := "http://127.0.0.1:" + Itoa(actual) + "/v1"

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
	proxyState.base = base
	proxyState.Unlock()
	return base, nil
}

// probeOpenCodeProxy asks whatever holds an address whether it is a ggraphify
// proxy. Anything else — a different application, a silent socket — answers
// no, and the caller refuses rather than sending a corpus to it.
func probeOpenCodeProxy(addr string) (string, bool) {
	c := &http.Client{Timeout: 2 * time.Second}
	resp, err := c.Get("http://" + addr + openCodeProbePath) // #nosec G107 -- loopback, address composed here
	if err != nil {
		return "", false
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 256))
	if err != nil || !strings.HasPrefix(string(body), openCodeProbeBody) {
		return "", false
	}
	return "http://" + addr + "/v1", true
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
		_, _ = io.WriteString(w, openCodeProbeBody+" "+AppVersion)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// One upstream, and only the paths it serves. A loopback proxy that
		// forwarded an arbitrary absolute URL would be an open relay for
		// anything else running on this machine.
		if !strings.HasPrefix(r.URL.Path, "/v1/") && r.URL.Path != "/v1" {
			http.Error(w, "this proxy forwards /v1 to "+OpenCodeUpstream()+" and nothing else", http.StatusNotFound)
			return
		}
		up, err := url.Parse(strings.TrimSuffix(OpenCodeUpstream(), "/v1"))
		if err != nil {
			http.Error(w, "bad upstream: "+err.Error(), http.StatusInternalServerError)
			return
		}
		target := *up
		target.Path = strings.TrimSuffix(up.Path, "/") + r.URL.Path
		target.RawQuery = r.URL.RawQuery

		req, err := http.NewRequestWithContext(r.Context(), r.Method, target.String(), r.Body)
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

		resp, err := proxyTransport.RoundTrip(req)
		if err != nil {
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
		buf := make([]byte, 16*1024)
		rc := http.NewResponseController(w)
		for {
			n, rerr := resp.Body.Read(buf)
			if n > 0 {
				if _, werr := w.Write(buf[:n]); werr != nil {
					return
				}
				_ = rc.Flush()
			}
			if rerr != nil {
				return
			}
		}
	})
	return mux
}
