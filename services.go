// services.go — atp's network boundary with the first-party platform services.
//
// atp used to import pod, song and shepherd and mount them in-process. The
// modules are standalone servers now, so atp reaches them over HTTP instead:
//
//	pod       the record database   (default http://127.0.0.1:8081)
//	song      static silo hosting   (default http://127.0.0.1:8082)
//	shepherd  capabilities, firewall and the gateway (default http://127.0.0.1:8083)
//
// Every public atp path that used to be served by a module is a reverse proxy
// in front of that module's own HTTP API, so the wire contract is the module's
// documented contract rather than a reimplementation of it. The only thing atp
// adds is identity: it can attach shepherd's admin token to the requests it
// forwards, which is why a browser can drive /api/keys without holding it.
package atp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Default service endpoints. They match each module's documented default port
// once it runs standalone.
const (
	DefaultPodURL      = "http://127.0.0.1:8081"
	DefaultSongURL     = "http://127.0.0.1:8082"
	DefaultShepherdURL = "http://127.0.0.1:8083"
)

// Service describes one remote first-party service atp fronts.
type Service struct {
	Name string `json:"name"`
	URL  string `json:"url"`
	// Health is the path atp probes to answer "is it up?".
	Health string `json:"health"`
	// Mounts are the atp URL prefixes forwarded to this service.
	Mounts []string `json:"mounts,omitempty"`
	// Started is filled in by the /api/services report.
	Started time.Time `json:"-"`
}

// Services returns the service table atp was configured with.
func (s *Server) Services() []Service {
	return []Service{
		{Name: "pod", URL: s.opts.PodURL, Health: "/health", Mounts: []string{"/pod/", "/{app}/api/pod/"}},
		{Name: "song", URL: s.opts.SongURL, Health: "/_song/health", Mounts: []string{"/_song/", "/{silo}/"}},
		{Name: "shepherd", URL: s.opts.ShepherdURL, Health: "/health", Mounts: []string{"/api/", "/gw/", "/verify", "/api/track/"}},
	}
}

// ServiceHealth is one service's live probe result.
type ServiceHealth struct {
	Service
	OK      bool   `json:"ok"`
	Status  int    `json:"status,omitempty"`
	Detail  string `json:"detail,omitempty"`
	Error   string `json:"error,omitempty"`
	Elapsed string `json:"elapsed,omitempty"`
}

// ProbeServices asks each configured service for its health document. It never
// fails: an unreachable service is reported with OK=false.
func (s *Server) ProbeServices(ctx context.Context) []ServiceHealth {
	out := make([]ServiceHealth, 0, 3)
	for _, svc := range s.Services() {
		h := ServiceHealth{Service: svc}
		start := time.Now()
		body, status, err := s.getJSON(ctx, svc.URL+svc.Health)
		h.Elapsed = time.Since(start).Round(time.Millisecond).String()
		h.Status = status
		switch {
		case err != nil:
			h.Error = err.Error()
		case status < 200 || status >= 300:
			h.Error = fmt.Sprintf("health returned HTTP %d", status)
		default:
			h.OK = true
			h.Detail = string(body)
		}
		out = append(out, h)
	}
	return out
}

// getJSON performs a short GET and returns the body and status.
func (s *Server) getJSON(ctx context.Context, url string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, err
	}
	resp, err := s.httpClient().Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return body, resp.StatusCode, nil
}

// httpClient returns the configured client or a sane default.
func (s *Server) httpClient() *http.Client {
	if s.client != nil {
		return s.client
	}
	return defaultHTTPClient
}

var defaultHTTPClient = &http.Client{Timeout: 10 * time.Second}

// proxy is one reverse proxy in front of a remote service. strip reports the
// prefix to remove for a given inbound request (it depends on the path for the
// per-application pod mount, so it cannot be a constant).
type proxy struct {
	handler http.Handler
}

func newProxy(target string, strip func(*http.Request) string, mutate func(*http.Request)) (*proxy, error) {
	u, err := url.Parse(strings.TrimRight(target, "/"))
	if err != nil {
		return nil, fmt.Errorf("atp: invalid service URL %q: %w", target, err)
	}
	if u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("atp: invalid service URL %q", target)
	}
	rp := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			if strip != nil {
				if p := strip(req); p != "" {
					req.URL.Path = strings.TrimPrefix(req.URL.Path, p)
					if req.URL.RawPath != "" {
						req.URL.RawPath = strings.TrimPrefix(req.URL.RawPath, p)
					}
				}
			}
			if req.URL.Path == "" {
				req.URL.Path = "/"
				req.URL.RawPath = ""
			}
			req.URL.Scheme = u.Scheme
			req.URL.Host = u.Host
			req.Host = u.Host
			req.Header.Del("X-Shepherd-Admin")
			if mutate != nil {
				mutate(req)
			}
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			writeErr(w, http.StatusBadGateway, "atp: upstream "+u.Host+" unavailable: "+err.Error())
		},
		FlushInterval: 100 * time.Millisecond,
	}
	return &proxy{handler: rp}, nil
}

func (p *proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.handler.ServeHTTP(w, r)
}

// songSilos caches song's silo listing so the routing decision ("does song own
// this path?") does not cost a round trip per request.
type siloCache struct {
	mu      sync.Mutex
	names   map[string]bool
	expires time.Time
}

const siloCacheTTL = 5 * time.Second

func (s *Server) songOwnsPath(p string) bool {
	if p == "/_song" || strings.HasPrefix(p, "/_song/") {
		return true
	}
	seg := firstSegment(p)
	if seg == "" {
		return false
	}
	if _, ok := s.apps.Get(seg); ok {
		return true
	}
	return s.songSiloNames()[seg]
}

func (s *Server) songSiloNames() map[string]bool {
	s.silos.mu.Lock()
	defer s.silos.mu.Unlock()
	if time.Now().Before(s.silos.expires) {
		return s.silos.names
	}
	names, _ := s.fetchSilos()
	set := make(map[string]bool, len(names))
	for _, n := range names {
		set[n] = true
	}
	s.silos.names = set
	s.silos.expires = time.Now().Add(siloCacheTTL)
	return set
}

// fetchSilos reads song's silo listing. A failure yields an empty list: routing
// then falls through to the local fileserver instead of failing the request.
func (s *Server) fetchSilos() ([]string, error) {
	body, status, err := s.getJSON(context.Background(), s.opts.SongURL+"/_song/silos")
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("atp: song silos returned HTTP %d", status)
	}
	var payload struct {
		Silos []string `json:"silos"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	return payload.Silos, nil
}

// listSilos returns song's silos for the management view.
func (s *Server) listSilos() []string {
	names, _ := s.fetchSilos()
	return names
}

// syncUpstreams registers the configured gateway routes with shepherd. It is
// best-effort: a shepherd that is not running yet must not stop atp from
// starting, so the failure is recorded as a warning and surfaced through
// /api/platform and /api/services.
func (s *Server) syncUpstreams() {
	if len(s.opts.Upstreams) == 0 {
		return
	}
	if s.opts.ShepherdAdmin == "" {
		s.warn("upstreams configured but ShepherdAdmin is empty; shepherd rejected the registration")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, u := range s.opts.Upstreams {
		if _, err := s.doJSON(ctx, http.MethodPost, s.opts.ShepherdURL+"/api/upstreams", u); err != nil {
			s.warn("upstream %q: %v", u.Prefix, err)
		}
	}
}

