// Package atp is the AzzurroTech Platform: a modified Go fileserver that adds a
// serverless function registry, an encrypted secrets vault, central head and
// header management, hosted-application deployment, generated client-app
// capabilities, and in-process mounting of song, pod and shepherd.
//
// atp is both a library (import "azzurrotech/atp") and a server (cmd/atp). Its
// dependencies are the Go standard library plus exactly the three first-party
// modules song, pod and shepherd.
package atp

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"azzurrotech/pod"
	"azzurrotech/shepherd/firewall"
	"azzurrotech/shepherd/middleware"
	songsrv "azzurrotech/song/pkg/song"
)

// Version is the atp release string.
const Version = "1.0.0"

// MinSecretLen is the minimum accepted master-secret length: it must be long
// enough to derive the vault key and sign shepherd tokens.
const MinSecretLen = 32

// Options configures a Server.
type Options struct {
	// Root is the directory that holds hosted applications, the pod store and
	// the atp state (vault, encrypted assets). Default "./data".
	Root string
	// Addr is the server-mode listen address (host:port). Default ":8080".
	Addr string
	// Secret is the master secret (>= MinSecretLen bytes). It derives the
	// vault key, the pod encryption key and shepherd's signing key.
	Secret string
	// Encrypt seals dynamic content (pod records and uploads) at rest. The
	// vault and the static overlay are always encrypted.
	Encrypt bool
	// Headless serves applications and functions without the management view
	// at "/".
	Headless bool
	// ClientIPHeader, when set (e.g. "X-Forwarded-For"), is trusted as the real
	// client IP. Only set it behind a proxy you control.
	ClientIPHeader string
	// Upstreams configures shepherd's gateway mounted at /gw/.
	Upstreams []middleware.Upstream
	// Title is the default <title> the head manager applies.
	Title string
}

// Server is the atp platform. It is an http.Handler; a host application can
// run it directly, embed it with Middleware, and extend it with Handle.
type Server struct {
	opts   Options
	root   string
	addr   string
	secret string

	song     *songsrv.Song
	podStore *pod.Store
	podH     *pod.Handler
	shepherd *middleware.Shepherd
	firewall *firewall.Firewall
	gateway  http.Handler

	vault    *Vault
	head     *Head
	headers  *Headers
	registry *Registry
	apps     *Apps
	box      *StaticBox

	handler http.Handler

	podMu     sync.Mutex
	perAppPod map[string]*pod.Handler
	encrypted bool
}

// New builds the platform: it opens song, pod and the vault, builds shepherd's
// capability engine, wires the routing order and returns a ready Server.
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
	root, err := filepath.Abs(o.Root)
	if err != nil {
		return nil, err
	}
	appsDir := filepath.Join(root, "apps")
	for _, d := range []string{root, appsDir, filepath.Join(root, "pod"), filepath.Join(root, "secrets"), filepath.Join(root, ".atp", "static")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, err
		}
	}

	songSvc, err := songsrv.New(songsrv.Config{Root: appsDir})
	if err != nil {
		return nil, fmt.Errorf("atp: song: %w", err)
	}

	var podStore *pod.Store
	if o.Encrypt {
		podStore, err = pod.OpenEncrypted(filepath.Join(root, "pod"), deriveKey(o.Secret, "pod"))
	} else {
		podStore, err = pod.Open(filepath.Join(root, "pod"))
	}
	if err != nil {
		return nil, fmt.Errorf("atp: pod: %w", err)
	}

	fw := firewall.New(true)
	shep, err := middleware.New(middleware.Config{
		Secret:         []byte(o.Secret),
		ClientIPHeader: o.ClientIPHeader,
		Firewall:       fw,
	})
	if err != nil {
		return nil, fmt.Errorf("atp: shepherd: %w", err)
	}

	s := &Server{
		opts:      o,
		root:      root,
		addr:      o.Addr,
		secret:    o.Secret,
		song:      songSvc,
		podStore:  podStore,
		podH:      pod.NewHandler(pod.HandlerOptions{Store: podStore, Mount: "/pod"}),
		shepherd:  shep,
		firewall:  fw,
		gateway:   shep.Gateway(middleware.GatewayOptions{Mount: "/gw/", Upstreams: o.Upstreams}),
		vault:     newVault(filepath.Join(root, "secrets"), deriveKey(o.Secret, "vault")),
		head:      newHead(o.Title),
		headers:   newHeaders(),
		registry:  newRegistry(),
		apps:      newApps(appsDir),
		box:       newStaticBox(filepath.Join(root, ".atp", "static"), deriveKey(o.Secret, "static")),
		perAppPod: map[string]*pod.Handler{},
		encrypted: o.Encrypt,
	}
	if err := s.apps.Discover(); err != nil {
		return nil, err
	}
	s.registerCapabilityFunctions()
	s.handler = s.wrap(s.core(http.FileServer(http.Dir(root)), true), true)
	return s, nil
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
// static overlay, platform managers, generated capabilities, pod, then song
// (which owns the app/static space and delegates to next).
func (s *Server) core(next http.Handler, serverMode bool) http.Handler {
	songMW := s.song.Middleware(next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		switch {
		case s.registry.Match(r):
			s.registry.Serve(w, r)
		case p == "/gw" || strings.HasPrefix(p, "/gw/"):
			s.gateway.ServeHTTP(w, r)
		case strings.HasPrefix(p, "/_atp/static/"):
			s.serveStaticBox(w, r)
		case s.ownsPath(p, serverMode):
			s.servePlatform(w, r)
		default:
			if app, kind, ok := s.capabilityMatch(p); ok {
				s.serveCapability(w, r, app, kind)
				return
			}
			if h, ok := s.podHandlerFor(p); ok {
				h.ServeHTTP(w, r)
				return
			}
			songMW.ServeHTTP(w, r)
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

// podHandlerFor returns the pod handler for /pod/… or a per-application
// /{app}/api/pod/… mount.
func (s *Server) podHandlerFor(p string) (http.Handler, bool) {
	if p == "/pod" || strings.HasPrefix(p, "/pod/") {
		return s.podH, true
	}
	app := firstSegment(p)
	if app == "" {
		return nil, false
	}
	rest := strings.TrimPrefix(p, "/"+app)
	if rest != "/api/pod" && !strings.HasPrefix(rest, "/api/pod/") {
		return nil, false
	}
	if _, ok := s.apps.Get(app); !ok {
		return nil, false
	}
	mount := "/" + app + "/api/pod"
	s.podMu.Lock()
	defer s.podMu.Unlock()
	h, ok := s.perAppPod[mount]
	if !ok {
		h = pod.NewHandler(pod.HandlerOptions{Store: s.podStore, Mount: mount})
		s.perAppPod[mount] = h
	}
	return h, true
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

// Song returns the embedded song instance.
func (s *Server) Song() *songsrv.Song { return s.song }

// Pod returns the embedded pod store.
func (s *Server) Pod() *pod.Store { return s.podStore }

// PodHandler returns the pod HTTP handler mounted at /pod.
func (s *Server) PodHandler() *pod.Handler { return s.podH }

// Shepherd returns the capability engine.
func (s *Server) Shepherd() *middleware.Shepherd { return s.shepherd }

// Firewall returns shepherd's firewall.
func (s *Server) Firewall() *firewall.Firewall { return s.firewall }

// Apps returns the hosted-application registry.
func (s *Server) Apps() *Apps { return s.apps }

// Registry returns the function registry.
func (s *Server) Registry() *Registry { return s.registry }

// StaticBox returns the encrypted static overlay.
func (s *Server) StaticBox() *StaticBox { return s.box }

// Gateway returns shepherd's gateway handler mounted at /gw/.
func (s *Server) Gateway() http.Handler { return s.gateway }

// Encrypted reports whether dynamic content is encrypted at rest.
func (s *Server) Encrypted() bool { return s.encrypted }

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
// long-lived handles beyond the filesystem, so it is a no-op kept for
// symmetry.
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
