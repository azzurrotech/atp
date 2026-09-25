// Package web implements the atp orchestrator over the HTTP surface.
//
// atp embeds song (siloed static hosting), pod (filesystem XML database) and
// shepherd (identity-less IAM) in-process and exposes every one of their
// endpoints through a single API + management UI. It adds what the three
// submodules do not have: client management, an encrypted secrets vault,
// per-client usage logging with configurable retention, hourly silo-size
// sampling and billing against the average hourly size in GB.
//
// atp operates in two modes:
//
//   - Server mode: ATPService.Start() runs a standalone standard-library Go
//     webserver on its own port.
//   - Middleware mode: ATPService.Middleware(next) serves the paths atp owns
//     (/api/..., /clients/..., /c/..., /gw/..., /login, /health) and
//     delegates everything else to next, so any other Go server can host atp.
//
// Everything is standard library only — no frameworks, no databases, and
// authentication is implemented with crypto/rand + HMAC/SHA-256.
package web

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"azzurrotech/atp/internal/auth"
	"azzurrotech/atp/internal/store"

	"azzurrotech/pod"
	"azzurrotech/shepherd/firewall"
	"azzurrotech/shepherd/middleware"
	"azzurrotech/shepherd/token"
	"azzurrotech/song/pkg/song"
	"azzurrotech/song/pkg/static"
)

// Version of the atp orchestrator.
const Version = "2.0.0"

// DefaultPrice is the default billing rate per GB per hour (USD).
const DefaultPrice = store.DefaultPricePerGBHour

// ReservedRouteSegments that atp owns at its root (used by Middleware mode).
var reservedRoutePrefixes = []string{
	"/api/", "/clients", "/c/", "/gw/", "/login", "/logout", "/health",
}

// Options configures an atp instance.
type Options struct {
	// Port used in server mode (default "8080").
	Port string
	// Root is the directory that holds clients.json, the secrets vault, the
	// log/usage data and the embedded song + pod stores (default "./data").
	Root string
	// Secret is the master encryption/signing secret; at least 32 bytes. It
	// protects the secrets vault and signs shepherd capability tokens.
	Secret string
	// AdminUser / AdminPassword gate the admin UI + API (stdlib auth).
	AdminUser     string
	AdminPassword string
	// DisableUI hides the built-in management pages (API keeps working).
	DisableUI bool
	// Seed provisions a demo client that exercises every feature.
	Seed bool
	// DefaultRatePerMinute / Burst configure the embedded rate limiter.
	DefaultRatePerMinute float64
	Burst                float64
}

// ATPService is a configured atp orchestrator.
type ATPService struct {
	cfg     Options
	root    string
	clients *store.ClientStore
	vault   *store.SecretsVault
	usage   *store.UsageStore
	logs    *store.LogStore
	users   *auth.Manager

	song *song.Song
	pod  *pod.Handler
	pods *pod.Store
	sec  *middleware.Shepherd
	fw   *firewall.Firewall

	upMu sync.RWMutex
	up   map[string]middleware.Upstream
	gw   http.Handler

	started time.Time
	handler http.Handler
}

// NewATPService wires the three submodules and atp's own services together
// and builds the complete HTTP surface. It is the single entry point used by
// both server mode and middleware mode.
func NewATPService(opts Options) (*ATPService, error) {
	if opts.Port == "" {
		opts.Port = "8080"
	}
	if opts.Secret == "" {
		opts.Secret = "change-me-atp-master-secret-32-bytes-minimum!!"
	}
	if len(opts.Secret) < 32 {
		return nil, fmt.Errorf("atp -secret must be at least 32 bytes")
	}
	if opts.AdminUser == "" {
		opts.AdminUser = "admin"
	}
	if opts.AdminPassword == "" {
		opts.AdminPassword = "admin"
	}
	if opts.Root == "" {
		opts.Root = "./data"
	}
	if opts.DefaultRatePerMinute == 0 {
		opts.DefaultRatePerMinute = 120
	}
	if opts.Burst == 0 {
		opts.Burst = 30
	}

	clients, err := store.NewClientStore(opts.Root)
	if err != nil {
		return nil, fmt.Errorf("atp: clients store: %w", err)
	}
	vault, err := store.NewSecretsVault(opts.Root, opts.Secret)
	if err != nil {
		return nil, fmt.Errorf("atp: secrets vault: %w", err)
	}
	logs, err := store.NewLogStore(opts.Root, clients)
	if err != nil {
		return nil, fmt.Errorf("atp: log store: %w", err)
	}
	usage, err := store.NewUsageStore(opts.Root, clients)
	if err != nil {
		return nil, fmt.Errorf("atp: usage store: %w", err)
	}
	users := auth.NewManager(opts.AdminUser, opts.AdminPassword)

	// song — siloed static hosting + SSR templates + magic-link auth.
	sng, err := song.New(song.Config{
		Root:      store.Join(opts.Root, "song"),
		Secret:    opts.Secret,
		DisableUI: opts.DisableUI,
	})
	if err != nil {
		return nil, fmt.Errorf("atp: song: %w", err)
	}

	// pod — filesystem XML database, mounted at /api/pod.
	pods, err := pod.Open(store.Join(opts.Root, "pod"))
	if err != nil {
		return nil, fmt.Errorf("atp: pod: %w", err)
	}
	ui := !opts.DisableUI
	podHandler := pod.NewHandler(pod.HandlerOptions{
		Store: pods,
		Mount: "/api/pod",
		UI:    &ui,
	})

	// shepherd — capability tokens, firewall, rate limiter, gateway.
	fw := firewall.New(true)
	sec, err := middleware.New(middleware.Config{
		Secret:          []byte(opts.Secret),
		Issuer:          "azzurrotech/atp",
		Firewall:        fw,
		LimitsPerMinute: opts.DefaultRatePerMinute,
		Burst:           opts.Burst,
	})
	if err != nil {
		return nil, fmt.Errorf("atp: shepherd: %w", err)
	}

	s := &ATPService{
		cfg:     opts,
		root:    opts.Root,
		clients: clients,
		vault:   vault,
		logs:    logs,
		usage:   usage,
		users:   users,
		song:    sng,
		pod:     podHandler,
		pods:    pods,
		sec:     sec,
		fw:      fw,
		up:      map[string]middleware.Upstream{},
		started: time.Now(),
	}
	s.rebuildGateway()
	s.handler = s.buildHandler()

	// Initial size samples now, then hourly on the boundary.
	s.sampleSizes()

	go s.background()

	if opts.Seed {
		if err := s.seedDemo(); err != nil {
			log.Printf("atp: seed: %v", err)
		}
	}
	return s, nil
}

// ---- mode entry points ------------------------------------------------------

// Handler returns the full atp HTTP handler (server mode).
func (s *ATPService) Handler() http.Handler { return s.handler }

// Middleware returns an http.Handler serving the paths atp owns and
// delegating everything else to next. This is how another Go server embeds
// atp: serve := atp.Middleware(hostHandler). Note that atp does not claim
// bare "/" in middleware mode so the host keeps its own root.
func (s *ATPService) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.ownsPath(r.URL.Path) {
			s.handler.ServeHTTP(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *ATPService) ownsPath(p string) bool {
	for _, prefix := range reservedRoutePrefixes {
		// Match a complete path segment. Raw prefix matching would make
		// /healthcheck, /login-page, and /logout-help ATP endpoints instead
		// of ordinary static-site paths in middleware mode.
		base := strings.TrimSuffix(prefix, "/")
		if p == base || strings.HasPrefix(p, base+"/") {
			return true
		}
	}
	return false
}

// Start runs atp as a standalone webserver (server mode).
func (s *ATPService) Start() error {
	addr := ":" + s.cfg.Port
	log.Printf("atp v%s listening on %s", Version, addr)
	log.Printf("  root        : %s", s.root)
	log.Printf("  admin       : http://localhost%s/ (user %q, password %q)", addr, s.cfg.AdminUser, s.cfg.AdminPassword)
	log.Printf("  default rate: %.0f/min, burst %.0f", s.cfg.DefaultRatePerMinute, s.cfg.Burst)
	if s.cfg.AdminPassword == "admin" {
		log.Printf("  WARNING: using the default admin password — set -admin-password / ATP_ADMIN_PASSWORD")
	}
	server := &http.Server{
		Addr:              addr,
		Handler:           s.handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	return server.ListenAndServe()
}

// Accessors used by the UI and by external hosts embedding atp.
func (s *ATPService) Clients() *store.ClientStore    { return s.clients }
func (s *ATPService) Vault() *store.SecretsVault     { return s.vault }
func (s *ATPService) Usage() *store.UsageStore       { return s.usage }
func (s *ATPService) Logs() *store.LogStore          { return s.logs }
func (s *ATPService) Auth() *auth.Manager            { return s.users }
func (s *ATPService) Song() *song.Song               { return s.song }
func (s *ATPService) Pod() *pod.Handler              { return s.pod }
func (s *ATPService) PodStore() *pod.Store           { return s.pods }
func (s *ATPService) Shepherd() *middleware.Shepherd { return s.sec }

// ---- background maintenance -------------------------------------------------

// background flushes usage buckets, prunes expired logs, and samples silo
// sizes shortly after each hour flips.
func (s *ATPService) background() {
	flush := time.NewTicker(30 * time.Second)
	prune := time.NewTicker(1 * time.Hour)
	boundary := time.NewTicker(1 * time.Minute)
	defer flush.Stop()
	defer prune.Stop()
	defer boundary.Stop()

	lastHour := time.Now().UTC().Format("2006-01-02T15")
	for {
		select {
		case <-flush.C:
			s.usage.Flush()
		case <-prune.C:
			s.pruneByRetention()
		case <-boundary.C:
			hour := time.Now().UTC().Format("2006-01-02T15")
			if hour != lastHour {
				lastHour = hour
				s.usage.Flush()
				s.sampleSizes()
			}
		}
	}
}

// sampleSizes records an hourly silo-size sample per client (once per hour,
// averaged into the running bucket by the usage store).
func (s *ATPService) sampleSizes() {
	s.usage.SampleSiloSizes(func(client string) int64 {
		a, b, err := store.SiloSize(s.root, client)
		if err != nil {
			return 0
		}
		return a + b
	})
}

// pruneByRetention deletes logs and usage older than each client's window.
func (s *ATPService) pruneByRetention() {
	for _, c := range s.clients.List() {
		s.logs.Prune(c.ID, c.RetentionHours)
		s.usage.Prune(c.ID, c.RetentionHours)
	}
}

// sampleClientSize returns the current on-disk silo size of a client in bytes
// (song silo + pod namespace — the basis of the billed hourly size).
func (s *ATPService) sampleClientSize(client string) int64 {
	a, b, _ := store.SiloSize(s.root, client)
	return a + b
}

// ---- auth helpers -----------------------------------------------------------

const sessionCookie = "atp_session"

// requireAdmin guards a handler: redirects browsers to /login, rejects
// API calls with 401.
func (s *ATPService) requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	if s.adminToken(r) {
		return true
	}
	if strings.Contains(r.Header.Get("Accept"), "text/html") || r.Method == http.MethodGet && !strings.HasPrefix(r.URL.Path, "/api/") {
		http.Redirect(w, r, "/login", http.StatusFound)
		return false
	}
	writeErrJSON(w, http.StatusUnauthorized, "admin session required")
	return false
}

// adminToken extracts and validates the admin credential: the atp session
// cookie or an X-ATP-Token header.
func (s *ATPService) adminToken(r *http.Request) bool {
	if c, err := r.Cookie(sessionCookie); err == nil && s.users.Authenticate(c.Value) {
		return true
	}
	if t := r.Header.Get("X-ATP-Token"); t != "" && s.users.Authenticate(t) {
		return true
	}
	return false
}

// admin wraps a handler with the auth gate.
func (s *ATPService) admin(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.requireAdmin(w, r) {
			return
		}
		h(w, r)
	}
}

// ---- JSON helpers -----------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func writeErrJSON(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func readJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, 4<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	return nil
}

// ---- utility helpers --------------------------------------------------------

// pathValue is a tiny alias for the Go 1.22 mux segment accessor.
func pathValue(r *http.Request, key string) string { return r.PathValue(key) }

// cloneRequest returns a shallow copy of r with a new URL, so handlers can
// rewrite the path without touching the request the outer mux saw.
func cloneRequest(r *http.Request, newPath string) *http.Request {
	r2 := r.Clone(r.Context())
	u := *r.URL
	u.Path = newPath
	u.RawPath = ""
	r2.URL = &u
	r2.RequestURI = newPath
	if q := u.RawQuery; q != "" {
		r2.RequestURI += "?" + q
	}
	return r2
}

// redispatch forwards the request to handler with a rewritten path. Used to
// pass requests into the embedded song/pod handlers after atp's scoping
// checks have run.
func (s *ATPService) redispatch(w http.ResponseWriter, r *http.Request, newPath string, handler http.Handler) {
	handler.ServeHTTP(w, cloneRequest(r, newPath))
}

// splitPath splits p into non-empty segments.
func splitPath(p string) []string {
	p = strings.Trim(p, "/")
	if p == "" {
		return nil
	}
	var out []string
	for _, seg := range strings.Split(p, "/") {
		if seg != "" {
			out = append(out, seg)
		}
	}
	return out
}

// clientForPath attributes a request to a client when the URL clearly
// references one (their web app, their scoped API, or a pass-through call
// naming their silo/table). "" means system traffic.
func (s *ATPService) clientForPath(p string) string {
	segs := splitPath(p)
	if len(segs) == 0 {
		return ""
	}
	switch segs[0] {
	case "c":
		if len(segs) >= 2 && s.clients.Exists(segs[1]) {
			return segs[1]
		}
	case "api":
		if len(segs) >= 3 {
			switch segs[1] {
			case "song":
				// /api/song/silos/{silo}/...
				if len(segs) >= 3 && segs[2] == "silos" && len(segs) >= 4 && s.clients.Exists(segs[3]) {
					return segs[3]
				}
			case "pod":
				// /api/pod/table/{table}/...  — the first table segment is the client id.
				if len(segs) >= 3 {
					switch segs[2] {
					case "table", "schema", "record":
						if len(segs) >= 4 && s.clients.Exists(segs[3]) {
							return segs[3]
						}
					}
				}
			case "clients":
				if len(segs) >= 3 && s.clients.Exists(segs[2]) {
					return segs[2]
				}
			}
		}
	}
	return ""
}

// ---- routing ----------------------------------------------------------------

func (s *ATPService) buildHandler() http.Handler {
	inner := http.NewServeMux()

	// Public surface.
	inner.HandleFunc("GET /health", s.handleHealth)
	inner.HandleFunc("GET /favicon.ico", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	inner.HandleFunc("GET /login", s.handleLoginPage)
	inner.HandleFunc("POST /login", s.handleLogin)
	inner.HandleFunc("POST /logout", s.handleLogout)
	inner.HandleFunc("GET /c/{client}/{rest...}", s.handleClientWeb)
	// song magic-link auth endpoints stay public (they authenticate a silo's
	// own end users), as does the shepherd token-verification endpoint.
	inner.HandleFunc("GET /api/auth/{rest...}", s.handleSongPass)
	inner.HandleFunc("POST /api/auth/{rest...}", s.handleSongPass)
	inner.HandleFunc("GET /api/shepherd/token/verify", s.handleShepherdVerifyToken)
	inner.HandleFunc("GET /api/shepherd/verify", s.handleShepherdMagicRedeem)

	// Admin: management pages.
	inner.HandleFunc("GET /", s.admin(s.handleDashboard))
	inner.HandleFunc("GET /clients", s.admin(s.handleClientsPage))
	inner.HandleFunc("GET /clients/{id}", s.admin(s.handleClientPage))
	inner.HandleFunc("GET /clients/{id}/files", s.admin(s.handleClientFilesPage))
	inner.HandleFunc("GET /clients/{id}/tables", s.admin(s.handleClientTablesPage))
	inner.HandleFunc("GET /clients/{id}/security", s.admin(s.handleClientSecurityPage))
	inner.HandleFunc("GET /clients/{id}/usage", s.admin(s.handleClientUsagePage))
	inner.HandleFunc("GET /clients/{id}/secrets", s.admin(s.handleClientSecretsPage))

	// Admin: atp JSON API.
	inner.HandleFunc("GET /api/summary", s.admin(s.handleSummary))
	inner.HandleFunc("GET /api/services", s.admin(s.handleServices))
	inner.HandleFunc("GET /api/config", s.admin(s.handleGetConfig))
	inner.HandleFunc("PUT /api/config", s.admin(s.handlePutConfig))
	inner.HandleFunc("GET /api/clients", s.admin(s.handleListClients))
	inner.HandleFunc("POST /api/clients", s.admin(s.handleCreateClient))
	inner.HandleFunc("GET /api/clients/{id}", s.admin(s.handleGetClient))
	inner.HandleFunc("PUT /api/clients/{id}", s.admin(s.handleUpdateClient))
	inner.HandleFunc("DELETE /api/clients/{id}", s.admin(s.handleDeleteClient))
	inner.HandleFunc("GET /api/clients/{id}/secrets", s.admin(s.handleListSecrets))
	inner.HandleFunc("POST /api/clients/{id}/secrets", s.admin(s.handleSetSecret))
	inner.HandleFunc("GET /api/clients/{id}/secrets/{name}", s.admin(s.handleGetSecret))
	inner.HandleFunc("DELETE /api/clients/{id}/secrets/{name}", s.admin(s.handleDeleteSecret))
	inner.HandleFunc("GET /api/clients/{id}/tables", s.admin(s.handleClientTables))
	inner.HandleFunc("GET /api/clients/{id}/usage", s.admin(s.handleClientUsage))
	inner.HandleFunc("GET /api/clients/{id}/usage/hourly", s.admin(s.handleClientHourly))
	inner.HandleFunc("GET /api/clients/{id}/billing", s.admin(s.handleClientBilling))
	inner.HandleFunc("POST /api/clients/{id}/keys", s.admin(s.handleClientIssueKey))
	inner.HandleFunc("POST /api/clients/{id}/keys/block", s.admin(s.handleClientIssueBlock))
	inner.HandleFunc("POST /api/clients/{id}/keys/magic", s.admin(s.handleClientMagicLink))
	inner.HandleFunc("POST /api/clients/{id}/revoke", s.admin(s.handleClientRevoke))
	inner.HandleFunc("GET /api/clients/{id}/jskey", s.admin(s.handleClientJSKey))
	inner.HandleFunc("GET /api/clients/{id}/verify", s.admin(s.handleClientVerify))

	// Admin: pass-through into the embedded services (every endpoint of
	// song, pod and shepherd is reachable here — nothing is re-implemented).
	inner.HandleFunc("GET /api/song/{rest...}", s.admin(s.handleSongPass))
	inner.HandleFunc("POST /api/song/{rest...}", s.admin(s.handleSongPass))
	inner.HandleFunc("PUT /api/song/{rest...}", s.admin(s.handleSongPass))
	inner.HandleFunc("DELETE /api/song/{rest...}", s.admin(s.handleSongPass))
	inner.HandleFunc("GET /api/pod/{rest...}", s.admin(s.handlePodPass))
	inner.HandleFunc("POST /api/pod/{rest...}", s.admin(s.handlePodPass))
	inner.HandleFunc("PUT /api/pod/{rest...}", s.admin(s.handlePodPass))
	inner.HandleFunc("DELETE /api/pod/{rest...}", s.admin(s.handlePodPass))
	inner.HandleFunc("GET /api/shepherd/keys/javascript", s.admin(s.handleShepherdJSKey))
	inner.HandleFunc("POST /api/shepherd/keys", s.admin(s.handleShepherdIssue))
	inner.HandleFunc("POST /api/shepherd/keys/block", s.admin(s.handleShepherdIssueBlock))
	inner.HandleFunc("POST /api/shepherd/keys/magic", s.admin(s.handleShepherdMagicLink))
	inner.HandleFunc("POST /api/shepherd/revoke", s.admin(s.handleShepherdRevoke))
	inner.HandleFunc("POST /api/shepherd/revoke/block", s.admin(s.handleShepherdRevokeBlock))
	inner.HandleFunc("GET /api/shepherd/firewall/rules", s.admin(s.handleShepherdFirewallRules))
	inner.HandleFunc("POST /api/shepherd/firewall/rules", s.admin(s.handleShepherdFirewallRules))
	inner.HandleFunc("DELETE /api/shepherd/firewall/rules/{id}", s.admin(s.handleShepherdFirewallRuleDelete))
	inner.HandleFunc("GET /api/shepherd/ratelimit/status", s.admin(s.handleShepherdRateStatus))
	inner.HandleFunc("POST /api/shepherd/ratelimit/reset", s.admin(s.handleShepherdRateReset))
	inner.HandleFunc("GET /api/shepherd/upstreams", s.admin(s.handleShepherdUpstreams))
	inner.HandleFunc("POST /api/shepherd/upstreams", s.admin(s.handleShepherdUpstreams))
	inner.HandleFunc("DELETE /api/shepherd/upstreams/{prefix}", s.admin(s.handleShepherdUpstreamDelete))
	inner.HandleFunc("GET /gw/{rest...}", s.admin(s.handleGateway))
	inner.HandleFunc("POST /gw/{rest...}", s.admin(s.handleGateway))
	inner.HandleFunc("PUT /gw/{rest...}", s.admin(s.handleGateway))
	inner.HandleFunc("DELETE /gw/{rest...}", s.admin(s.handleGateway))

	// Admin: client-scoped proxies into song and pod (thin, scoping access
	// to the same embedded services — no duplicated feature logic).
	inner.HandleFunc("GET /c/{client}/api/song/{rest...}", s.admin(s.handleClientSongAPI))
	inner.HandleFunc("POST /c/{client}/api/song/{rest...}", s.admin(s.handleClientSongAPI))
	inner.HandleFunc("PUT /c/{client}/api/song/{rest...}", s.admin(s.handleClientSongAPI))
	inner.HandleFunc("DELETE /c/{client}/api/song/{rest...}", s.admin(s.handleClientSongAPI))
	inner.HandleFunc("GET /c/{client}/api/pod/{rest...}", s.admin(s.handleClientPodAPI))
	inner.HandleFunc("POST /c/{client}/api/pod/{rest...}", s.admin(s.handleClientPodAPI))
	inner.HandleFunc("PUT /c/{client}/api/pod/{rest...}", s.admin(s.handleClientPodAPI))
	inner.HandleFunc("DELETE /c/{client}/api/pod/{rest...}", s.admin(s.handleClientPodAPI))

	// The outermost layer counts every request against its client's usage
	// and appends the request log line.
	return s.usageMiddleware(inner)
}

// ---- usage + logging middleware ----------------------------------------------

// countWriter records the response status and byte count.
type countWriter struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (c *countWriter) WriteHeader(status int) {
	if c.status == 0 {
		c.status = status
	}
	c.ResponseWriter.WriteHeader(status)
}

func (c *countWriter) Write(b []byte) (int, error) {
	if c.status == 0 {
		c.status = http.StatusOK
	}
	n, err := c.ResponseWriter.Write(b)
	c.bytes += int64(n)
	return n, err
}

func (c *countWriter) Flush() {
	if f, ok := c.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// countReader counts bytes as a request body is read.
type countReader struct {
	r io.Reader
	n int64
}

func (cr *countReader) Read(b []byte) (int, error) {
	n, err := cr.r.Read(b)
	cr.n += int64(n)
	return n, err
}

// usageMiddleware attributes each request to a client (when derivable),
// records the on-the-wire request size, and appends a log line.
func (s *ATPService) usageMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		client := s.clientForPath(r.URL.Path)

		reqBytes := r.ContentLength
		var cr *countReader
		if reqBytes <= 0 {
			cr = &countReader{r: r.Body}
			r.Body = io.NopCloser(cr)
		}
		cw := &countWriter{ResponseWriter: w}
		start := time.Now()

		next.ServeHTTP(cw, r)

		if cr != nil {
			reqBytes = cr.n
		}
		if client == "" {
			return // system traffic is not logged per client
		}
		status := cw.status
		if status == 0 {
			status = http.StatusOK
		}
		s.usage.Record(client, reqBytes, cw.bytes)
		s.logs.Write(store.RequestRecord{
			Time:       time.Now().UTC().Format(time.RFC3339Nano),
			Client:     client,
			Method:     r.Method,
			Path:       r.URL.Path,
			Status:     status,
			ReqBytes:   reqBytes,
			RespBytes:  cw.bytes,
			DurationMS: time.Since(start).Milliseconds(),
			RemoteIP:   remoteIP(r),
			UserAgent:  truncate(r.UserAgent(), 200),
		})
	})
}

func remoteIP(r *http.Request) string {
	ip := r.RemoteAddr
	if h := r.Header.Get("X-Forwarded-For"); h != "" {
		if i := strings.Index(h, ","); i >= 0 {
			ip = strings.TrimSpace(h[:i])
		} else {
			ip = strings.TrimSpace(h)
		}
	}
	if host, _, err := netSplitHostPort(ip); err == nil {
		return host
	}
	return ip
}

func netSplitHostPort(s string) (string, string, error) {
	i := strings.LastIndex(s, ":")
	if i < 0 {
		return s, "", nil
	}
	return s[:i], s[i+1:], nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// ---- pass-through handlers ---------------------------------------------------

// handleSongPass forwards to song's own middleware, which serves /api/song/*,
// /api/auth/* and existing silos, and 404s the rest.
func (s *ATPService) handleSongPass(w http.ResponseWriter, r *http.Request) {
	s.song.Middleware(http.NotFoundHandler()).ServeHTTP(w, r)
}

// handlePodPass forwards a request into the embedded pod handler mounted at
// /api/pod. pod handles the mount stripping itself.
func (s *ATPService) handlePodPass(w http.ResponseWriter, r *http.Request) {
	s.pod.ServeHTTP(w, r)
}

func (s *ATPService) activeClient(id string) bool {
	c, err := s.clients.Get(id)
	return err == nil && !c.Disabled
}

// handleClientWeb serves a client's hosted web application (their song silo)
// publicly at /c/{client}/...
func (s *ATPService) handleClientWeb(w http.ResponseWriter, r *http.Request) {
	id := pathValue(r, "client")
	if !s.activeClient(id) {
		http.NotFound(w, r)
		return
	}
	newPath := "/" + id
	if rest := pathValue(r, "rest"); rest != "" {
		newPath += "/" + rest
	}
	s.redispatch(w, r, newPath, s.song.Middleware(http.NotFoundHandler()))
}

// handleClientSongAPI is a SCOPE-ENFORCING pass-through into song: a client
// may only manage its own silo through the API.
func (s *ATPService) handleClientSongAPI(w http.ResponseWriter, r *http.Request) {
	id := pathValue(r, "client")
	if !s.activeClient(id) {
		http.NotFound(w, r)
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/c/"+id+"/api/song")
	segs := splitPath(rest)

	// /api/song/silos/{silo}... — the silo must be this client's own.
	if len(segs) >= 1 && segs[0] == "silos" {
		switch {
		case len(segs) == 1:
			// List silos: the client only ever sees its own.
			if r.Method != http.MethodGet {
				writeErrJSON(w, http.StatusMethodNotAllowed, "method not allowed")
				return
			}
			files := 0
			if list, err := s.song.Store().List(id, ""); err == nil {
				files = len(list)
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"silos": []map[string]any{{"name": id, "files": files}},
			})
			return
		case segs[1] != id:
			writeErrJSON(w, http.StatusForbidden, "cannot access another client's silo")
			return
		}
	}
	s.redispatch(w, r, "/api/song"+rest, s.song.Middleware(http.NotFoundHandler()))
}

// handleClientPodAPI is a SCOPE-ENFORCING pass-through into pod: a client may
// only touch tables under <client>/... and never the SQL / admin endpoints.
func (s *ATPService) handleClientPodAPI(w http.ResponseWriter, r *http.Request) {
	id := pathValue(r, "client")
	if !s.activeClient(id) {
		http.NotFound(w, r)
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/c/"+id+"/api/pod")
	segs := splitPath(rest)

	switch {
	case len(segs) == 0:
		writeErrJSON(w, http.StatusForbidden, "use the client tables list endpoint")
		return
	case segs[0] == "table" || segs[0] == "schema" || segs[0] == "record":
		if len(segs) < 2 || segs[1] != id {
			writeErrJSON(w, http.StatusForbidden, "cannot access another client's tables")
			return
		}
	case segs[0] == "tables":
		// Listing is provided by GET /api/clients/{id}/tables; a client may
		// not create schemas for other namespaces.
		if r.Method != http.MethodGet {
			writeErrJSON(w, http.StatusForbidden, "tables are managed from the client view")
			return
		}
		writeErrJSON(w, http.StatusForbidden, "use GET /api/clients/{id}/tables")
		return
	default:
		// /sql, /normalize, /reindex, /health and friends stay admin-only.
		writeErrJSON(w, http.StatusForbidden, "endpoint is only available to admins")
		return
	}
	s.redispatch(w, r, "/api/pod"+rest, s.pod)
}

// ---- health / services / summary ----------------------------------------------

func (s *ATPService) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status":   "healthy",
		"service":  "atp",
		"version":  Version,
		"time":     time.Now().UTC().Format(time.RFC3339),
		"clients":  len(s.clients.List()),
		"services": "song,pod,shepherd embedded",
	})
}

func (s *ATPService) handleDashboard(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	s.renderPage(w, r, "dashboard", map[string]any{
		"Title":    "ATP — Admin Overview",
		"Active":   "dashboard",
		"UserName": s.cfg.AdminUser,
	})
}

func (s *ATPService) handleSummary(w http.ResponseWriter, r *http.Request) {
	clients := s.clients.List()
	var (
		totalSize    int64
		totalRevenue float64
		chargeable   int
	)
	type clientSummary struct {
		store.Client
		SiloBytes      int64   `json:"silo_bytes"`
		SiloGB         float64 `json:"silo_gb"`
		AvgSiloGB      float64 `json:"avg_silo_gb"`
		TotalCost      float64 `json:"total_cost"`
		TotalRequests  int64   `json:"total_requests"`
		RetentionHours int     `json:"retention_hours"`
	}
	summaries := make([]clientSummary, 0, len(clients))
	for _, c := range clients {
		size := s.sampleClientSize(c.ID)
		totalSize += size
		bill, err := s.usage.Billing(c.ID)
		cost := 0.0
		var avg float64
		var req int64
		if err == nil && bill != nil {
			cost = bill.TotalCost
			avg = bill.AvgSiloGB
			req = bill.TotalRequests
		}
		if c.Chargeable {
			chargeable++
			totalRevenue += cost
		}
		summaries = append(summaries, clientSummary{
			Client:         c,
			SiloBytes:      size,
			SiloGB:         float64(size) / float64(1024*1024*1024),
			AvgSiloGB:      avg,
			TotalCost:      cost,
			TotalRequests:  req,
			RetentionHours: c.RetentionHours,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"version":            Version,
		"uptime_seconds":     int64(time.Since(s.started).Seconds()),
		"clients_total":      len(clients),
		"clients_chargeable": chargeable,
		"total_silo_bytes":   totalSize,
		"total_silo_gb":      float64(totalSize) / float64(1024*1024*1024),
		"total_revenue":      totalRevenue,
		"clients":            summaries,
		"services": []map[string]string{
			{"name": "song", "status": "embedded", "mount": "/api/song"},
			{"name": "pod", "status": "embedded", "mount": "/api/pod"},
			{"name": "shepherd", "status": "embedded", "mount": "/api/shepherd"},
		},
	})
}

func (s *ATPService) handleServices(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"services": []map[string]any{
			{"name": "atp", "version": Version, "status": "healthy", "type": "orchestrator", "port": s.cfg.Port},
			{"name": "song", "version": song.Version, "status": "healthy", "type": "static-hosting", "mount": "/api/song"},
			{"name": "pod", "version": pod.Version, "status": "healthy", "type": "database", "mount": "/api/pod"},
			{"name": "shepherd", "version": "0.1.0", "status": "healthy", "type": "security", "mount": "/api/shepherd"},
		},
	})
}

func (s *ATPService) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"settings":   s.clients.Settings(),
		"admin_user": s.cfg.AdminUser,
		"port":       s.cfg.Port,
	})
}

func (s *ATPService) handlePutConfig(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DefaultPricePerGBHour    float64 `json:"default_price_per_gb_hour"`
		DefaultRetentionHours    int     `json:"default_retention_hours"`
		MaxUploadBytes           int64   `json:"max_upload_bytes"`
		RequestLogLimitPerClient int     `json:"request_log_limit_per_client"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErrJSON(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	set := s.clients.Settings()
	if req.DefaultPricePerGBHour > 0 {
		set.DefaultPricePerGBHour = req.DefaultPricePerGBHour
	}
	if req.DefaultRetentionHours > 0 {
		set.DefaultRetentionHours = req.DefaultRetentionHours
	}
	if req.MaxUploadBytes > 0 {
		set.MaxUploadBytes = req.MaxUploadBytes
	}
	if req.RequestLogLimitPerClient > 0 {
		set.RequestLogLimitPerClient = req.RequestLogLimitPerClient
	}
	if err := s.clients.SetSettings(set); err != nil {
		writeErrJSON(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"settings": set})
}

// ---- client CRUD --------------------------------------------------------------

func (s *ATPService) handleListClients(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"clients": s.clients.List()})
}

func (s *ATPService) handleCreateClient(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID    string `json:"id"`
		Name  string `json:"name"`
		Notes string `json:"notes,omitempty"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErrJSON(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	c, err := s.clients.Create(req.ID, req.Name, req.Notes)
	if err != nil {
		writeErrJSON(w, http.StatusBadRequest, err.Error())
		return
	}
	// Provision the client's silo inside song so the static repo exists.
	if err := s.song.Store().CreateSilo(c.ID); err != nil && !errors.Is(err, static.ErrExists) {
		writeErrJSON(w, http.StatusInternalServerError, "client registered but silo provisioning failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"client": c})
}

func (s *ATPService) handleGetClient(w http.ResponseWriter, r *http.Request) {
	id := pathValue(r, "id")
	c, err := s.clients.Get(id)
	if err != nil {
		writeErrJSON(w, http.StatusNotFound, err.Error())
		return
	}
	size := s.sampleClientSize(id)
	bill, _ := s.usage.Billing(id)
	writeJSON(w, http.StatusOK, map[string]any{
		"client":     c,
		"silo_bytes": size,
		"silo_gb":    float64(size) / float64(1024*1024*1024),
		"billing":    bill,
		"silo_url":   "/c/" + id + "/",
		"tables":     s.clientTableList(id),
	})
}

func (s *ATPService) handleUpdateClient(w http.ResponseWriter, r *http.Request) {
	id := pathValue(r, "id")
	var req struct {
		Name           string  `json:"name"`
		Disabled       *bool   `json:"disabled"`
		Chargeable     *bool   `json:"chargeable"`
		PricePerGBHour float64 `json:"price_per_gb_hour"`
		RetentionHours int     `json:"retention_hours"`
		Notes          string  `json:"notes"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErrJSON(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	// Partial update: only overwrite the booleans the caller actually sent.
	cur, err := s.clients.Get(id)
	if err != nil {
		writeErrJSON(w, http.StatusNotFound, err.Error())
		return
	}
	patch := store.Client{Name: req.Name, Notes: req.Notes, PricePerGBHour: req.PricePerGBHour, RetentionHours: req.RetentionHours}
	if req.Disabled != nil {
		patch.Disabled = *req.Disabled
	} else {
		patch.Disabled = cur.Disabled
	}
	if req.Chargeable != nil {
		patch.Chargeable = *req.Chargeable
	} else {
		patch.Chargeable = cur.Chargeable
	}
	c, err := s.clients.Update(id, patch)
	if err != nil {
		writeErrJSON(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"client": c})
}

func (s *ATPService) handleDeleteClient(w http.ResponseWriter, r *http.Request) {
	id := pathValue(r, "id")
	if err := s.clients.Delete(id); err != nil {
		writeErrJSON(w, http.StatusNotFound, err.Error())
		return
	}
	// Leave the silo/pod data in place (operator decides); drop the silo
	// directory so the name can be reused safely.
	_ = s.song.Store().RemoveSilo(id)
	writeJSON(w, http.StatusOK, map[string]any{"deleted": id})
}

// clientTableList returns the pod tables belonging to a client.
func (s *ATPService) clientTableList(client string) []string {
	tables, err := s.pods.ListTables()
	if err != nil {
		return nil
	}
	var out []string
	prefix := client + "/"
	for _, t := range tables {
		if t == client || strings.HasPrefix(t, prefix) {
			out = append(out, t)
		}
	}
	return out
}

func (s *ATPService) handleClientTables(w http.ResponseWriter, r *http.Request) {
	id := pathValue(r, "id")
	c, err := s.clients.Get(id)
	if err != nil {
		writeErrJSON(w, http.StatusNotFound, err.Error())
		return
	}
	type tableInfo struct {
		Name  string `json:"name"`
		Count int    `json:"count"`
	}
	prefix := id + "/"
	var out []tableInfo
	tables, err := s.pods.ListTables()
	if err == nil {
		for _, t := range tables {
			if t != id && !strings.HasPrefix(t, prefix) {
				continue
			}
			if ti, err := s.pods.TableInfo(t); err == nil {
				out = append(out, tableInfo{Name: t, Count: ti.Count})
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"client": c.ID, "tables": out, "base_url": "/c/" + id + "/api/pod"})
}

// ---- secrets vault ------------------------------------------------------------

func (s *ATPService) handleListSecrets(w http.ResponseWriter, r *http.Request) {
	id := pathValue(r, "id")
	secrets, err := s.vault.List(id)
	if err != nil {
		writeErrJSON(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"client": id, "secrets": secrets})
}

func (s *ATPService) handleSetSecret(w http.ResponseWriter, r *http.Request) {
	id := pathValue(r, "id")
	var req struct {
		Name  string `json:"name"`
		Value string `json:"value"`
		Note  string `json:"note,omitempty"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErrJSON(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	sec, err := s.vault.Set(id, req.Name, req.Value, req.Note)
	if err != nil {
		writeErrJSON(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, sec)
}

func (s *ATPService) handleGetSecret(w http.ResponseWriter, r *http.Request) {
	id := pathValue(r, "id")
	name := pathValue(r, "name")
	sec, err := s.vault.Get(id, name)
	if err != nil {
		writeErrJSON(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, sec)
}

func (s *ATPService) handleDeleteSecret(w http.ResponseWriter, r *http.Request) {
	id := pathValue(r, "id")
	name := pathValue(r, "name")
	if err := s.vault.Delete(id, name); err != nil {
		writeErrJSON(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": name})
}

// ---- usage / billing ------------------------------------------------------------

func (s *ATPService) handleClientUsage(w http.ResponseWriter, r *http.Request) {
	id := pathValue(r, "id")
	c, err := s.clients.Get(id)
	if err != nil {
		writeErrJSON(w, http.StatusNotFound, err.Error())
		return
	}
	limit := 200
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 5000 {
			limit = n
		}
	}
	recs, err := s.logs.Recent(id, limit)
	if err != nil {
		writeErrJSON(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"client":          c.ID,
		"retention_hours": c.RetentionHours,
		"records":         recs,
	})
}

func (s *ATPService) handleClientHourly(w http.ResponseWriter, r *http.Request) {
	id := pathValue(r, "id")
	hourly := s.usage.Hourly(id)
	writeJSON(w, http.StatusOK, map[string]any{"client": id, "hourly": hourly})
}

func (s *ATPService) handleClientBilling(w http.ResponseWriter, r *http.Request) {
	id := pathValue(r, "id")
	cost, err := s.usage.Billing(id)
	if err != nil {
		writeErrJSON(w, http.StatusNotFound, err.Error())
		return
	}
	cost.CurrentSiloGB = float64(s.sampleClientSize(id)) / float64(1024*1024*1024)
	writeJSON(w, http.StatusOK, cost)
}

// ---- client-scoped shepherd operations -----------------------------------------

type issueRequest struct {
	Subject  string            `json:"subject,omitempty"`
	Scopes   []string          `json:"scopes,omitempty"`
	Roles    []string          `json:"roles,omitempty"`
	Audience string            `json:"audience,omitempty"`
	TTL      string            `json:"ttl,omitempty"`
	Block    string            `json:"block,omitempty"`
	Meta     map[string]string `json:"meta,omitempty"`
	Count    int               `json:"count,omitempty"`
	Next     string            `json:"next,omitempty"`
}

func parseTTL(v string) (time.Duration, error) {
	if v == "" {
		return 0, nil
	}
	return time.ParseDuration(v)
}

// clientScopes forces every issued token to carry the client's own scope.
func clientScopes(client string, extra []string) []string {
	out := []string{"client:" + client}
	for _, e := range extra {
		if e != "" && e != "client:"+client {
			out = append(out, e)
		}
	}
	return out
}

func (s *ATPService) handleClientIssueKey(w http.ResponseWriter, r *http.Request) {
	id := pathValue(r, "id")
	if !s.clients.Exists(id) {
		writeErrJSON(w, http.StatusNotFound, "client not found")
		return
	}
	var req issueRequest
	if err := readJSON(r, &req); err != nil {
		writeErrJSON(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	ttl, err := parseTTL(req.TTL)
	if err != nil {
		writeErrJSON(w, http.StatusBadRequest, "invalid ttl: "+err.Error())
		return
	}
	raw, claims, err := s.sec.Issue(token.IssueOptions{
		Subject:  req.Subject,
		Scopes:   clientScopes(id, req.Scopes),
		Roles:    req.Roles,
		Audience: req.Audience,
		TTL:      ttl,
		Block:    req.Block,
		Meta:     req.Meta,
	})
	if err != nil {
		writeErrJSON(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"token": raw, "claims": claims, "expires_in": claims.ExpiresAt - claims.IssuedAt})
}

func (s *ATPService) handleClientIssueBlock(w http.ResponseWriter, r *http.Request) {
	id := pathValue(r, "id")
	if !s.clients.Exists(id) {
		writeErrJSON(w, http.StatusNotFound, "client not found")
		return
	}
	var req issueRequest
	if err := readJSON(r, &req); err != nil {
		writeErrJSON(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	if req.Count <= 0 {
		req.Count = 1
	}
	if req.Count > 10000 {
		writeErrJSON(w, http.StatusBadRequest, "count must be <= 10000")
		return
	}
	ttl, err := parseTTL(req.TTL)
	if err != nil {
		writeErrJSON(w, http.StatusBadRequest, "invalid ttl: "+err.Error())
		return
	}
	keys, err := s.sec.Manager().IssueBlock(token.IssueOptions{
		Subject:  req.Subject,
		Scopes:   clientScopes(id, req.Scopes),
		Roles:    req.Roles,
		Audience: req.Audience,
		TTL:      ttl,
		Meta:     req.Meta,
	}, req.Block, req.Count)
	if err != nil {
		writeErrJSON(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"block": keys[0].Claims.Block, "count": len(keys), "keys": keys})
}

func (s *ATPService) handleClientMagicLink(w http.ResponseWriter, r *http.Request) {
	id := pathValue(r, "id")
	if !s.clients.Exists(id) {
		writeErrJSON(w, http.StatusNotFound, "client not found")
		return
	}
	var req issueRequest
	if err := readJSON(r, &req); err != nil {
		writeErrJSON(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	if req.Next == "" {
		req.Next = "/"
	}
	ttl, err := parseTTL(req.TTL)
	if err != nil {
		writeErrJSON(w, http.StatusBadRequest, "invalid ttl: "+err.Error())
		return
	}
	raw, claims, err := s.sec.Manager().IssueMagicLink(token.IssueOptions{
		Subject:  req.Subject,
		Scopes:   clientScopes(id, req.Scopes),
		Roles:    req.Roles,
		Audience: req.Audience,
		TTL:      ttl,
		Next:     req.Next,
		Meta:     req.Meta,
	})
	if err != nil {
		writeErrJSON(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"url":   "/api/shepherd/verify?t=" + url.QueryEscape(raw) + "&next=" + url.QueryEscape(req.Next),
		"token": raw, "claims": claims, "expires_in": claims.ExpiresAt - claims.IssuedAt,
	})
}

func (s *ATPService) handleClientRevoke(w http.ResponseWriter, r *http.Request) {
	id := pathValue(r, "id")
	if !s.clients.Exists(id) {
		writeErrJSON(w, http.StatusNotFound, "client not found")
		return
	}
	var req struct {
		Token string `json:"token"`
		Block string `json:"block,omitempty"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErrJSON(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	if req.Block != "" {
		s.sec.RevokeBlock(req.Block, time.Now().Add(365*24*time.Hour).Unix())
		writeJSON(w, http.StatusOK, map[string]any{"revoked_block": req.Block})
		return
	}
	claims, err := s.sec.Manager().Verify(req.Token)
	if err != nil {
		writeErrJSON(w, http.StatusBadRequest, "cannot revoke: "+err.Error())
		return
	}
	s.sec.RevokeToken(claims.ID, claims.ExpiresAt)
	writeJSON(w, http.StatusOK, map[string]any{"revoked": claims.ID})
}

func (s *ATPService) handleClientJSKey(w http.ResponseWriter, r *http.Request) {
	id := pathValue(r, "id")
	if !s.clients.Exists(id) {
		writeErrJSON(w, http.StatusNotFound, "client not found")
		return
	}
	scope := r.URL.Query().Get("scope")
	if scope == "" {
		scope = "client:" + id
	}
	k, err := s.sec.JSCryptoKey(scope, r.URL.Query().Get("usage"))
	if err != nil {
		writeErrJSON(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, k)
}

func (s *ATPService) handleClientVerify(w http.ResponseWriter, r *http.Request) {
	id := pathValue(r, "id")
	if !s.clients.Exists(id) {
		writeErrJSON(w, http.StatusNotFound, "client not found")
		return
	}
	raw := r.URL.Query().Get("t")
	if raw == "" {
		raw = s.sec.TokenFromRequest(r)
	}
	if raw == "" {
		writeErrJSON(w, http.StatusUnauthorized, "missing token")
		return
	}
	claims, err := s.sec.Manager().Verify(raw)
	if err != nil {
		writeErrJSON(w, http.StatusUnauthorized, "invalid token: "+err.Error())
		return
	}
	okScope := false
	for _, sc := range claims.Scopes {
		if sc == "client:"+id || sc == "*" {
			okScope = true
		}
	}
	if !okScope {
		writeErrJSON(w, http.StatusForbidden, "token is not scoped to this client")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"valid": true, "claims": claims})
}

// ---- login ---------------------------------------------------------------------

func (s *ATPService) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	if s.adminToken(r) {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	s.renderPage(w, r, "login", map[string]any{"Title": "ATP — Sign in", "Active": "login"})
}

func (s *ATPService) handleLogin(w http.ResponseWriter, r *http.Request) {
	ct := r.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "application/x-www-form-urlencoded") {
		if err := r.ParseForm(); err == nil {
			user, pass := r.FormValue("user"), r.FormValue("password")
			if tok, err := s.users.Login(user, pass); err == nil {
				s.setSessionCookie(w, tok)
				writeJSON(w, http.StatusOK, map[string]any{"ok": true, "token": tok})
				return
			}
			writeErrJSON(w, http.StatusUnauthorized, "invalid credentials")
			return
		}
	}
	var req struct {
		User     string `json:"user"`
		Password string `json:"password"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErrJSON(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	tok, err := s.users.Login(req.User, req.Password)
	if err != nil {
		writeErrJSON(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	s.setSessionCookie(w, tok)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "token": tok})
}

func (s *ATPService) setSessionCookie(w http.ResponseWriter, tok string) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    tok,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(auth.DefaultTTL.Seconds()),
	})
}

func (s *ATPService) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		s.users.Logout(c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1})
	http.Redirect(w, r, "/login", http.StatusFound)
}

// ---- helpers --------------------------------------------------------------------

func randTokenHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return hex.EncodeToString(b)
}

// mini URL join used in UI links.
func joinURL(base string, parts ...string) string {
	p := path.Join(parts...)
	if p == "." {
		return base
	}
	return strings.TrimSuffix(base, "/") + "/" + strings.TrimPrefix(p, "/")
}
