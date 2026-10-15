package atp

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
)

// paramKey is the context key under which the registry stashes a matched
// pattern's path parameters.
type paramKey struct{}

// PathParam returns the value of a trailing {name} pattern segment for the
// current request, or "" if the route had none.
func PathParam(r *http.Request, name string) string {
	params, _ := r.Context().Value(paramKey{}).(map[string]string)
	return params[name]
}

// PathParams returns all path parameters captured for the current request.
func PathParams(r *http.Request) map[string]string {
	params, _ := r.Context().Value(paramKey{}).(map[string]string)
	return params
}

// Route is the public description of a registered function.
type Route struct {
	Method  string `json:"method"`
	Pattern string `json:"pattern"`
	Group   string `json:"group,omitempty"`
	Enabled bool   `json:"enabled"`
}

type routeEntry struct {
	method  string
	pattern string
	prefix  string // pattern without the trailing {name}
	param   string // trailing {name} ("" when none)
	fn      http.HandlerFunc
	group   string
	enabled bool
}

// Registry holds the serverless functions registered against a verb and an
// endpoint. It is the seam that lets a host application expand atp without
// forking it.
type Registry struct {
	mu     sync.RWMutex
	routes []*routeEntry
}

func newRegistry() *Registry { return &Registry{} }

// add validates and registers one function. The method accepts a single verb
// or "*" for any verb; pattern must begin with "/" and may end with one
// "{name}" segment.
func (rg *Registry) add(method, pattern, group string, fn http.HandlerFunc) error {
	if fn == nil {
		return fmt.Errorf("atp: nil handler for %s %s", method, pattern)
	}
	method = strings.ToUpper(strings.TrimSpace(method))
	if method == "" {
		return fmt.Errorf("atp: empty method for pattern %q", pattern)
	}
	if method != "*" && !isToken(method) {
		return fmt.Errorf("atp: malformed method %q", method)
	}
	if !strings.HasPrefix(pattern, "/") {
		return fmt.Errorf("atp: pattern %q must begin with /", pattern)
	}
	param := ""
	prefix := pattern
	if i := strings.Index(pattern, "{"); i >= 0 {
		if !strings.HasSuffix(pattern, "}") || strings.Count(pattern, "{") != 1 || strings.Count(pattern, "}") != 1 {
			return fmt.Errorf("atp: malformed pattern %q", pattern)
		}
		param = pattern[i+1 : len(pattern)-1]
		if param == "" || !isToken(param) || strings.ContainsAny(param, "/{}") {
			return fmt.Errorf("atp: malformed pattern %q", pattern)
		}
		prefix = strings.TrimSuffix(pattern, "/{"+param+"}")
		if prefix == pattern {
			return fmt.Errorf("atp: malformed pattern %q", pattern)
		}
	}
	rg.mu.Lock()
	defer rg.mu.Unlock()
	for _, e := range rg.routes {
		if e.method == method && e.pattern == pattern {
			return fmt.Errorf("atp: duplicate route %s %s", method, pattern)
		}
	}
	rg.routes = append(rg.routes, &routeEntry{
		method:  method,
		pattern: pattern,
		prefix:  prefix,
		param:   param,
		fn:      fn,
		group:   group,
		enabled: true,
	})
	return nil
}

// match returns the first enabled route whose method and path match r.
func (rg *Registry) match(r *http.Request) (*routeEntry, map[string]string) {
	rg.mu.RLock()
	defer rg.mu.RUnlock()
	for _, e := range rg.routes {
		if !e.enabled {
			continue
		}
		if e.method != "*" && e.method != strings.ToUpper(r.Method) {
			continue
		}
		if e.param == "" {
			if r.URL.Path == e.pattern {
				return e, nil
			}
			continue
		}
		rest := ""
		if r.URL.Path == e.prefix {
			rest = ""
		} else if strings.HasPrefix(r.URL.Path, e.prefix+"/") {
			rest = r.URL.Path[len(e.prefix)+1:]
		} else {
			continue
		}
		if rest == "" || strings.Contains(rest, "/") {
			continue
		}
		return e, map[string]string{e.param: rest}
	}
	return nil, nil
}

// Match reports whether a registered function owns this request.
func (rg *Registry) Match(r *http.Request) bool {
	e, _ := rg.match(r)
	return e != nil
}

// Serve runs the matched function, injecting any path parameters.
func (rg *Registry) Serve(w http.ResponseWriter, r *http.Request) {
	e, params := rg.match(r)
	if e == nil {
		http.NotFound(w, r)
		return
	}
	if len(params) > 0 {
		r = r.WithContext(context.WithValue(r.Context(), paramKey{}, params))
	}
	e.fn(w, r)
}

// Routes lists the registered functions in registration order.
func (rg *Registry) Routes() []Route {
	rg.mu.RLock()
	defer rg.mu.RUnlock()
	out := make([]Route, 0, len(rg.routes))
	for _, e := range rg.routes {
		out = append(out, Route{Method: e.method, Pattern: e.pattern, Group: e.group, Enabled: e.enabled})
	}
	return out
}

// SetEnabled turns a registered route on or off. It reports whether a matching
// route was found.
func (rg *Registry) SetEnabled(method, pattern string, on bool) bool {
	method = strings.ToUpper(strings.TrimSpace(method))
	rg.mu.Lock()
	defer rg.mu.Unlock()
	for _, e := range rg.routes {
		if e.method == method && e.pattern == pattern {
			e.enabled = on
			return true
		}
	}
	return false
}

// Remove deletes a registered route. It reports whether one was removed.
func (rg *Registry) Remove(method, pattern string) bool {
	method = strings.ToUpper(strings.TrimSpace(method))
	rg.mu.Lock()
	defer rg.mu.Unlock()
	for i, e := range rg.routes {
		if e.method == method && e.pattern == pattern {
			rg.routes = append(rg.routes[:i], rg.routes[i+1:]...)
			return true
		}
	}
	return false
}

func isToken(s string) bool {
	for _, c := range s {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '-', c == '_', c == '.', c == '~':
		default:
			return false
		}
	}
	return s != ""
}
