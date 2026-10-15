package atp

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

// Headers manages request, response and internal headers in one place, for
// both directions and both audiences. The default response policy is the safe
// baseline the client-capability documentation relies on; an application can
// tighten or relax it per path.
type Headers struct {
	mu        sync.RWMutex
	requests  []*requestRule
	responses []*responseRule
	internals []*internalRule
}

type requestRule struct {
	h          *Headers
	pattern    string
	re         *regexp.Regexp
	trustProxy bool
	strip      bool
	set        map[string]string
}

type responseRule struct {
	h       *Headers
	pattern string
	re      *regexp.Regexp
	set     map[string]string
	unset   map[string]bool
}

type internalRule struct {
	h        *Headers
	name     string
	fromPath int
	value    string
	fn       func(*http.Request) string
}

func newHeaders() *Headers {
	h := &Headers{}
	// Inbound spoofing of the internal namespace is dropped by default.
	h.Request("X-Platform-*").Strip()
	return h
}

// Request starts (or returns) a request-header policy rule for a path glob.
func (h *Headers) Request(pattern string) *requestRule {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, r := range h.requests {
		if r.pattern == pattern {
			return r
		}
	}
	r := &requestRule{h: h, pattern: pattern, set: map[string]string{}}
	h.requests = append(h.requests, r)
	return r
}

// Response starts (or returns) a response-header policy rule for a path glob.
func (h *Headers) Response(pattern string) *responseRule {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, r := range h.responses {
		if r.pattern == pattern {
			return r
		}
	}
	r := &responseRule{h: h, pattern: pattern, set: map[string]string{}, unset: map[string]bool{}}
	h.responses = append(h.responses, r)
	return r
}

// Internal starts (or returns) an internal-header rule (atp <-> modules and
// registered functions). Internal headers never leak to the client.
func (h *Headers) Internal(name string) *internalRule {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, r := range h.internals {
		if r.name == name {
			return r
		}
	}
	r := &internalRule{h: h, name: name}
	h.internals = append(h.internals, r)
	return r
}

// Terminal rule builders return the *Headers manager so rules for the two
// directions and the internal namespace can be chained in one expression, as
// the README documents:
//
//	srv.Headers().
//		Request(`X-Forwarded-For`).TrustProxy(true).
//		Request(`X-Platform-*`).Strip().
//		Response(`/apps/**`).Set("Cache-Control", "public, max-age=300").
//		Internal("X-Platform-App").FromPath(1)

// TrustProxy marks a request header as trusted as the real client IP.
func (r *requestRule) TrustProxy(v bool) *Headers { r.trustProxy = v; return r.h }

// Strip removes a request header before a handler sees it.
func (r *requestRule) Strip() *Headers { r.strip = true; return r.h }

// Set sets a request header before a handler sees it.
func (r *requestRule) Set(name, value string) *Headers {
	if r.set == nil {
		r.set = map[string]string{}
	}
	r.set[name] = value
	return r.h
}

// Set sets a response header for matching paths.
func (r *responseRule) Set(name, value string) *Headers {
	if r.set == nil {
		r.set = map[string]string{}
	}
	r.set[name] = value
	return r.h
}

// Unset removes a response header for matching paths.
func (r *responseRule) Unset(name string) *Headers {
	if r.unset == nil {
		r.unset = map[string]bool{}
	}
	r.unset[name] = true
	return r.h
}

// FromPath derives the internal header value from a 1-based path segment.
func (r *internalRule) FromPath(seg int) *Headers { r.fromPath = seg; r.fn = nil; return r.h }

// Set sets a constant internal header value.
func (r *internalRule) Set(value string) *Headers { r.value = value; r.fn = nil; return r.h }

// Value sets a computed internal header value.
func (r *internalRule) Value(fn func(*http.Request) string) *Headers { r.fn = fn; return r.h }

// applyRequest rewrites inbound headers before a handler sees them. Request
// rules are keyed by header name (or name glob), not by path: the README's
// examples are `Request("X-Forwarded-For")` and `Request("X-Platform-*")`.
func (h *Headers) applyRequest(r *http.Request) {
	// Drop client-supplied internal-namespace headers first, then let explicit
	// rules run, so rules can re-add when an author trusts an upstream.
	h.mu.RLock()
	reqs := append([]*requestRule(nil), h.requests...)
	ints := append([]*internalRule(nil), h.internals...)
	h.mu.RUnlock()

	for _, rule := range reqs {
		if rule.strip {
			for name := range r.Header {
				if matchHeaderGlob(rule.pattern, name) {
					r.Header.Del(name)
				}
			}
		}
		// trustProxy configures which header carries the real client IP; that
		// is handed to shepherd via Options.ClientIPHeader.
		for name, value := range rule.set {
			r.Header.Set(name, value)
		}
	}

	// The internal namespace carries the resolved application and a
	// correlation id to the modules and registered functions.
	if app := firstSegment(r.URL.Path); isToken(app) {
		r.Header.Set("X-Platform-App", app)
	}
	if r.Header.Get("X-Platform-Correlation-ID") == "" {
		r.Header.Set("X-Platform-Correlation-ID", randomID())
	}

	for _, rule := range ints {
		switch {
		case rule.fromPath > 0:
			r.Header.Set(rule.name, pathSegment(r.URL.Path, rule.fromPath))
		case rule.fn != nil:
			r.Header.Set(rule.name, rule.fn(r))
		default:
			r.Header.Set(rule.name, rule.value)
		}
	}
}

// applyResponse sets the safe baseline and any matching response rules. It is
// called just before the response header is written, so rules win over headers
// a handler set.
func (h *Headers) applyResponse(hdr http.Header, r *http.Request) {
	baseline(hdr, r)

	h.mu.RLock()
	rules := append([]*responseRule(nil), h.responses...)
	h.mu.RUnlock()
	for _, rule := range rules {
		if !globMatch(rule.regex(), r.URL.Path) {
			continue
		}
		for name := range rule.unset {
			hdr.Del(name)
		}
		for name, value := range rule.set {
			hdr.Set(name, value)
		}
	}
}

// baseline sets the safe-by-default response headers.
func baseline(hdr http.Header, r *http.Request) {
	setIfEmpty := func(k, v string) {
		if hdr.Get(k) == "" {
			hdr.Set(k, v)
		}
	}
	setIfEmpty("X-Content-Type-Options", "nosniff")
	setIfEmpty("Referrer-Policy", "strict-origin-when-cross-origin")
	setIfEmpty("Permissions-Policy", "geolocation=(), microphone=(), camera=()")
	setIfEmpty("Cross-Origin-Opener-Policy", "same-origin")
	setIfEmpty("Cross-Origin-Resource-Policy", "same-origin")
	setIfEmpty("X-Frame-Options", "SAMEORIGIN")
	setIfEmpty("Content-Security-Policy",
		"default-src 'self'; "+
			"script-src 'self' 'unsafe-inline' 'wasm-unsafe-eval'; "+
			"style-src 'self' 'unsafe-inline'; "+
			"img-src 'self' data:; "+
			"media-src 'self' blob:; "+
			"connect-src 'self'; "+
			"worker-src 'self' blob:; "+
			"font-src 'self'; "+
			"object-src 'none'; base-uri 'self'; form-action 'self'; frame-ancestors 'self'")
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		setIfEmpty("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
	}
	if hdr.Get("Cache-Control") == "" {
		hdr.Set("Cache-Control", cacheFor(r.URL.Path))
	}
}

func cacheFor(p string) string {
	switch {
	case strings.HasSuffix(p, ".html"), strings.HasSuffix(p, ".tmpl"):
		return "no-cache"
	case strings.HasPrefix(p, "/_song/"):
		return "public, max-age=3600"
	}
	switch ext(p) {
	case ".css", ".js", ".mjs", ".json", ".wasm", ".svg", ".png", ".jpg", ".jpeg",
		".gif", ".webp", ".avif", ".ico", ".woff", ".woff2", ".ttf", ".mp4", ".webm",
		".mp3", ".ogg", ".vtt":
		return "public, max-age=3600"
	}
	return "no-cache"
}

// HeaderRule is the wire form of a header policy rule, used by the /headers
// management surface.
type HeaderRule struct {
	Direction string `json:"direction"` // request | response | internal
	Pattern   string `json:"pattern,omitempty"`
	Name      string `json:"name,omitempty"`
	Value     string `json:"value,omitempty"`
	Action    string `json:"action,omitempty"` // strip | trust-proxy | set | unset
}

// Rules lists every configured header rule.
func (h *Headers) Rules() []HeaderRule {
	h.mu.RLock()
	defer h.mu.RUnlock()
	var out []HeaderRule
	for _, r := range h.requests {
		if r.strip {
			out = append(out, HeaderRule{Direction: "request", Pattern: r.pattern, Action: "strip"})
		}
		if r.trustProxy {
			out = append(out, HeaderRule{Direction: "request", Pattern: r.pattern, Action: "trust-proxy"})
		}
		for n, v := range r.set {
			out = append(out, HeaderRule{Direction: "request", Pattern: r.pattern, Name: n, Value: v, Action: "set"})
		}
	}
	for _, r := range h.responses {
		for n, v := range r.set {
			out = append(out, HeaderRule{Direction: "response", Pattern: r.pattern, Name: n, Value: v, Action: "set"})
		}
		for n := range r.unset {
			out = append(out, HeaderRule{Direction: "response", Pattern: r.pattern, Name: n, Action: "unset"})
		}
	}
	for _, r := range h.internals {
		rule := HeaderRule{Direction: "internal", Name: r.name, Action: "set"}
		switch {
		case r.fromPath > 0:
			rule.Value = "path:" + strconv.Itoa(r.fromPath)
		case r.fn != nil:
			rule.Value = "computed"
		default:
			rule.Value = r.value
		}
		out = append(out, rule)
	}
	return out
}

// AddRule applies one header rule from the management surface.
func (h *Headers) AddRule(rule HeaderRule) error {
	switch strings.ToLower(rule.Direction) {
	case "request":
		if rule.Pattern == "" {
			return fmt.Errorf("atp: request rule needs a pattern")
		}
		r := h.Request(rule.Pattern)
		switch strings.ToLower(rule.Action) {
		case "strip", "":
			r.Strip()
		case "trust-proxy":
			r.TrustProxy(true)
		case "set":
			if rule.Name == "" {
				return fmt.Errorf("atp: request set needs a name")
			}
			r.Set(rule.Name, rule.Value)
		default:
			return fmt.Errorf("atp: unknown request action %q", rule.Action)
		}
	case "response":
		if rule.Pattern == "" {
			return fmt.Errorf("atp: response rule needs a pattern")
		}
		r := h.Response(rule.Pattern)
		if strings.ToLower(rule.Action) == "unset" {
			if rule.Name == "" {
				return fmt.Errorf("atp: response unset needs a name")
			}
			r.Unset(rule.Name)
		} else {
			if rule.Name == "" {
				return fmt.Errorf("atp: response set needs a name")
			}
			r.Set(rule.Name, rule.Value)
		}
	case "internal":
		if rule.Name == "" {
			return fmt.Errorf("atp: internal rule needs a name")
		}
		r := h.Internal(rule.Name)
		if strings.HasPrefix(rule.Value, "path:") {
			n, err := strconv.Atoi(strings.TrimPrefix(rule.Value, "path:"))
			if err != nil || n < 1 {
				return fmt.Errorf("atp: bad path segment %q", rule.Value)
			}
			r.FromPath(n)
		} else {
			r.Set(rule.Value)
		}
	default:
		return fmt.Errorf("atp: unknown direction %q", rule.Direction)
	}
	return nil
}

// RemoveRule deletes a rule matching the wire form.
func (h *Headers) RemoveRule(rule HeaderRule) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	switch strings.ToLower(rule.Direction) {
	case "request":
		for i, r := range h.requests {
			if r.pattern == rule.Pattern {
				h.requests = append(h.requests[:i:i], h.requests[i+1:]...)
				return true
			}
		}
	case "response":
		for i, r := range h.responses {
			if r.pattern == rule.Pattern {
				h.responses = append(h.responses[:i:i], h.responses[i+1:]...)
				return true
			}
		}
	case "internal":
		for i, r := range h.internals {
			if r.name == rule.Name {
				h.internals = append(h.internals[:i:i], h.internals[i+1:]...)
				return true
			}
		}
	}
	return false
}

func (r *requestRule) regex() *regexp.Regexp {
	if r.re == nil {
		r.re = compileGlob(r.pattern)
	}
	return r.re
}

func (r *responseRule) regex() *regexp.Regexp {
	if r.re == nil {
		r.re = compileGlob(r.pattern)
	}
	return r.re
}

func compileGlob(pattern string) *regexp.Regexp {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(pattern); {
		if pattern[i] == '*' {
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				b.WriteString(".*")
				i += 2
				continue
			}
			b.WriteString("[^/]*")
			i++
			continue
		}
		b.WriteString(regexp.QuoteMeta(string(pattern[i])))
		i++
	}
	b.WriteString("$")
	re, err := regexp.Compile(b.String())
	if err != nil {
		return regexp.MustCompile("^$")
	}
	return re
}

func globMatch(re *regexp.Regexp, s string) bool { return re != nil && re.MatchString(s) }

// matchHeaderGlob matches a header-name glob, where '*' spans any characters.
func matchHeaderGlob(pattern, name string) bool {
	if pattern == name {
		return true
	}
	if !strings.Contains(pattern, "*") {
		return false
	}
	re := regexp.MustCompile("^" + strings.ReplaceAll(regexp.QuoteMeta(pattern), "\\*", ".*") + "$")
	return re.MatchString(name)
}

func firstSegment(p string) string {
	p = strings.TrimPrefix(p, "/")
	if i := strings.IndexByte(p, '/'); i >= 0 {
		return p[:i]
	}
	return p
}

func pathSegment(p string, seg int) string {
	parts := strings.Split(strings.Trim(p, "/"), "/")
	if seg >= 1 && seg <= len(parts) {
		return parts[seg-1]
	}
	return ""
}

func randomID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "0000000000000000"
	}
	return hex.EncodeToString(b)
}

// compressible reports whether a content type benefits from gzip.
func compressible(ct string) bool {
	ct = strings.ToLower(strings.TrimSpace(ct))
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = strings.TrimSpace(ct[:i])
	}
	if strings.HasPrefix(ct, "text/") {
		return true
	}
	switch ct {
	case "application/json", "application/javascript", "application/xml",
		"application/manifest+json", "application/opensearchdescription+xml",
		"image/svg+xml", "application/x-javascript":
		return true
	}
	return false
}
