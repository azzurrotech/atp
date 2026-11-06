// Package atp is the AzzurroTech Platform: a modified Go fileserver that adds a
// serverless function registry, an encrypted secrets vault, central head and
// header management, hosted-application deployment and generated client-app
// capabilities.
//
// atp is a front door. The three platform services it used to embed —
// pod (records), song (static hosting) and shepherd (capabilities, firewall,
// gateway) — are standalone servers, and atp reaches each of them over HTTP.
// Nothing in this package imports them: PodURL, SongURL and ShepherdURL are the
// whole contract. That keeps atp a library with no cross-project replace
// directives, and it means a deployment can run one pod for several atp
// instances, or upgrade song without rebuilding atp.
package atp

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Version is the atp release string.
const Version = "2.0.0"

// MinSecretLen is the minimum accepted master-secret length: it must be long
// enough to derive the vault key.
const MinSecretLen = 32

// Upstream is one gateway route. atp forwards the list to shepherd at startup;
// shepherd performs the actual proxying at /gw/{prefix}/….
type Upstream struct {
	Prefix string            `json:"prefix"`
	Target string            `json:"target"`
	Strip  bool              `json:"strip,omitempty"`
	Header map[string]string `json:"header,omitempty"`
}

// Options configures a Server.
type Options struct {
	// Root is the directory that holds hosted applications and the atp state
	// (vault, encrypted assets). Default "./data".
	Root string
	// Addr is the server-mode listen address (host:port). Default ":8080".
	Addr string
	// Secret is the master secret (>= MinSecretLen bytes). It derives the
	// vault key and the static-overlay key.
	Secret string
	// Encrypt seals the static overlay at rest (it always is) and marks
	// dynamic content as encrypted for reporting purposes.
	Encrypt bool
	// Headless serves applications and functions without the management view
	// at "/".
	Headless bool
	// ClientIPHeader, when set (e.g. "X-Forwarded-For"), is trusted as the real
	// client IP. atp forwards it to shepherd.
	ClientIPHeader string
	// Upstreams configures shepherd's gateway mounted at /gw/. They are
	// registered with shepherd when ShepherdAdmin is set.
	Upstreams []Upstream
	// Title is the default <title> the head manager applies.
	Title string

	// PodURL is the base URL of a running pod server. Default
	// DefaultPodURL (http://127.0.0.1:8081).
	PodURL string
	// SongURL is the base URL of a running song server. Default
	// DefaultSongURL (http://127.0.0.1:8082).
	SongURL string
	// ShepherdURL is the base URL of a running shepherd server. Default
	// DefaultShepherdURL (http://127.0.0.1:8083).
	ShepherdURL string
	// ShepherdAdmin is shepherd's admin token. When set, atp attaches it as
	// X-Shepherd-Admin to proxied requests, so a browser can use /api/keys
	// without ever seeing the token.
	ShepherdAdmin string
	// HTTPClient is used for service probes and startup registration. Nil
	// means a 10-second default client.
	HTTPClient *http.Client
}

// Server is the atp platform. It is an http.Handler; a host application can
// run it directly, embed it with Middleware, and extend it with Handle.
type Server struct {
	opts   Options
	root   string
	addr   string
	secret string

	podProxy      *proxy
	songProxy     *proxy
	shepherdProxy *proxy
	client        *http.Client

	vault    *Vault
	head     *Head
	headers  *Headers
	registry *Registry
	apps     *Apps
	box      *StaticBox

	handler http.Handler

	silos     siloCache
	encrypted bool

	warnMu   sync.Mutex
	warnings []string
}

// New builds the platform: it opens the vault, discovers hosted applications,
// wires the reverse proxies to pod, song and shepherd, and returns a ready
// Server. It does not fail when a service is down — the proxies answer 502 and
// /api/services reports which service is unreachable.
func New(o Options) (*Server, error) {
	if o.Root == "" {
		o.Root = "./data"
	}
	if o.Addr == "" {
		o.Addr = ":8080"
	}
	if len(o.Secret) < MinSecretLen {
		return nil, fmt.Errorf("atp: secret must be at least %d bytes (got %d)", MinSecretLen, len(o.Secret))
	}
	if o.PodURL == "" {
		o.PodURL = DefaultPodURL
	}
	if o.SongURL == "" {
		o.SongURL = DefaultSongURL
	}
	if o.ShepherdURL == "" {
		o.ShepherdURL = DefaultShepherdURL
	}
	root, err := filepath.Abs(o.Root)
	if err != nil {
		return nil, err
	}
	appsDir := filepath.Join(root, "apps")
	for _, d := range []string{root, appsDir, filepath.Join(root, "secrets"), filepath.Join(root, ".atp", "static")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, err
		}
	}

	s := &Server{
		opts:      o,
		root:      root,
		addr:      o.Addr,
		secret:    o.Secret,
		client:    o.HTTPClient,
		vault:     newVault(filepath.Join(root, "secrets"), deriveKey(o.Secret, "vault")),
		head:      newHead(o.Title),
		headers:   newHeaders(),
		registry:  newRegistry(),
		apps:      newApps(appsDir),
		box:       newStaticBox(filepath.Join(root, ".atp", "static"), deriveKey(o.Secret, "static")),
		encrypted: o.Encrypt,
	}

	if s.podProxy, err = newProxy(o.PodURL, podStrip, nil); err != nil {
		return nil, err
	}
	if s.songProxy, err = newProxy(o.SongURL, nil, nil); err != nil {
		return nil, err
	}
	shepMutate := func(req *http.Request) {
		if o.ShepherdAdmin != "" {
			req.Header.Set("X-Shepherd-Admin", o.ShepherdAdmin)
		}
		if o.ClientIPHeader != "" {
			req.Header.Set("X-Forwarded-For", req.Header.Get(o.ClientIPHeader))
		}
	}
	if s.shepherdProxy, err = newProxy(o.ShepherdURL, nil, shepMutate); err != nil {
		return nil, err
	}

	if err := s.apps.Discover(); err != nil {
		return nil, err
	}
	s.registerPlatformFunctions()
	s.syncUpstreams()
	s.handler = s.wrap(s.core(http.FileServer(http.Dir(root)), true), true)
	return s, nil
}

// podStrip removes the atp-side prefix before forwarding to pod. The pod
// server runs at "/" and knows nothing about atp's "/pod" mount or the
// per-application "/{app}/api/pod" mount.
func podStrip(r *http.Request) string {
	if p := r.URL.Path; p == "/pod" || strings.HasPrefix(p, "/pod/") {
		return "/pod"
	}
	app := firstSegment(r.URL.Path)
	if app == "" {
		return ""
	}
	return "/" + app + "/api/pod"
}

// ServeHTTP serves the whole platform in server mode.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.handler.ServeHTTP(w, r)
}

// Middleware mounts atp inside a host server: atp serves the platform paths and
// hosted applications it owns and calls next for everything else.
func (s *Server) Middleware(next http.Handler) http.Handler {
	if next == nil {
		next = http.NotFoundHandler()
	}
	return s.wrap(s.core(next, false), false)
}

// wrap applies the header policy and, for HTML responses, head injection.
func (s *Server) wrap(next http.Handler, injectAll bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.headers.applyRequest(r)
		inject := injectAll || s.ownsPath(r.URL.Path, true) || s.registry.Match(r) || s.hasCapability(r.URL.Path)
		iw := newInjectWriter(w, r, s, inject)
		next.ServeHTTP(iw, r)
		iw.finish()
	})
}

// core is the fixed routing order: registered function, gateway, encrypted
// static overlay, platform managers, generated capabilities, pod, then song,
// and finally the local fileserver.
func (s *Server) core(next http.Handler, serverMode bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		switch {
		case s.registry.Match(r):
			s.registry.Serve(w, r)
		case p == "/gw" || strings.HasPrefix(p, "/gw/"):
			s.shepherdProxy.ServeHTTP(w, r)
		case strings.HasPrefix(p, "/_atp/static/"):
			s.serveStaticBox(w, r)
		case s.ownsPath(p, serverMode):
			s.servePlatform(w, r)
		case s.podHandlerFor(p):
			s.podProxy.ServeHTTP(w, r)
		default:
			if app, kind, ok := s.capabilityMatch(p); ok {
				s.serveCapability(w, r, app, kind)
				return
			}
			if s.songOwnsPath(p) {
				s.songProxy.ServeHTTP(w, r)
				return
			}
			next.ServeHTTP(w, r)
		}
	})
}

// ownsPath reports whether atp's platform managers own a request path.
func (s *Server) ownsPath(p string, serverMode bool) bool {
	switch {
	case p == "/_atp/view.css":
		return true
	case p == "/secrets" || strings.HasPrefix(p, "/secrets/"):
		return true
	case p == "/head" || strings.HasPrefix(p, "/head/"):
		return true
	case p == "/headers" || strings.HasPrefix(p, "/headers/"):
		return true
	case serverMode && !s.opts.Headless && p == "/":
		return true
	}
	return false
}

func (s *Server) hasCapability(p string) bool {
	_, _, ok := s.capabilityMatch(p)
	return ok
}

// podHandlerFor reports whether the path is pod's: /pod/… or a per-application
// /{app}/api/pod/… mount.
func (s *Server) podHandlerFor(p string) bool {
	if p == "/pod" || strings.HasPrefix(p, "/pod/") {
		return true
	}
	app := firstSegment(p)
	if app == "" {
		return false
	}
	rest := strings.TrimPrefix(p, "/"+app)
	if rest != "/api/pod" && !strings.HasPrefix(rest, "/api/pod/") {
		return false
	}
	_, ok := s.apps.Get(app)
	return ok
}

// --- accessors (the seams a host application uses) ---

// Addr returns the configured listen address.
func (s *Server) Addr() string { return s.addr }

// Root returns the absolute root directory.
func (s *Server) Root() string { return s.root }

// Secrets returns the encrypted secrets vault.
func (s *Server) Secrets() *Vault { return s.vault }

// Head returns the central head manager.
func (s *Server) Head() *Head { return s.head }

// Headers returns the request/response/internal header manager.
func (s *Server) Headers() *Headers { return s.headers }

// Apps returns the hosted-application registry.
func (s *Server) Apps() *Apps { return s.apps }

// Registry returns the function registry.
func (s *Server) Registry() *Registry { return s.registry }

// StaticBox returns the encrypted static overlay.
func (s *Server) StaticBox() *StaticBox { return s.box }

// Encrypted reports whether dynamic content is encrypted at rest.
func (s *Server) Encrypted() bool { return s.encrypted }

// Warnings returns the non-fatal problems atp found while wiring services
// (for example a shepherd that refused the configured upstreams).
func (s *Server) Warnings() []string {
	s.warnMu.Lock()
	defer s.warnMu.Unlock()
	return append([]string(nil), s.warnings...)
}

func (s *Server) warn(format string, args ...any) {
	s.warnMu.Lock()
	defer s.warnMu.Unlock()
	s.warnings = append(s.warnings, fmt.Sprintf(format, args...))
}

// Handle registers a serverless function against one method and one endpoint.
// It returns an error for a malformed pattern or a duplicate method+pattern.
func (s *Server) Handle(method, pattern string, fn http.HandlerFunc) error {
	return s.registry.add(method, pattern, "", fn)
}

// HandleFunc is an alias of Handle, named for http.HandlerFunc symmetry.
func (s *Server) HandleFunc(method, pattern string, fn http.HandlerFunc) error {
	return s.registry.add(method, pattern, "", fn)
}

// Close releases the server's resources. atp holds no background goroutines or
// long-lived handles beyond the filesystem and the shared HTTP client, so it is
// a no-op kept for symmetry.
func (s *Server) Close() error { return nil }

// ext returns the lowercase file extension of a path, including the dot.
func ext(p string) string {
	if i := strings.LastIndexByte(p, '.'); i >= 0 {
		if j := strings.LastIndexAny(p, "/\\"); j < i {
			return strings.ToLower(p[i:])
		}
	}
	return ""
}
