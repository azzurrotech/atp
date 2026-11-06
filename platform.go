package atp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
)

// servePlatform dispatches the platform-manager paths owned by atp.
func (s *Server) servePlatform(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path
	switch {
	case p == "/_atp/view.css":
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
		_, _ = io.WriteString(w, viewCSS)
		return
	case p == "/secrets" || strings.HasPrefix(p, "/secrets/"):
		s.serveSecrets(w, r)
		return
	case p == "/head" || strings.HasPrefix(p, "/head/"):
		s.serveHead(w, r)
		return
	case p == "/headers" || strings.HasPrefix(p, "/headers/"):
		s.serveHeaders(w, r)
		return
	case p == "/":
		s.renderView(w, r)
		return
	}
	http.NotFound(w, r)
}

// --- secrets ---

func (s *Server) serveSecrets(w http.ResponseWriter, r *http.Request) {
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/secrets"), "/")
	if rest == "" {
		if r.Method != http.MethodGet {
			methodNotAllowed(w, "GET")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"apps": s.vault.Apps()})
		return
	}
	segs := strings.Split(rest, "/")
	app := segs[0]

	// /secrets/{app}
	if len(segs) == 1 {
		if r.Method != http.MethodGet {
			methodNotAllowed(w, "GET")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"app": app, "secrets": nonNil(s.vault.List(app))})
		return
	}
	name := segs[1]

	// /secrets/{app}/{name}
	if len(segs) == 2 {
		switch r.Method {
		case http.MethodGet:
			value, ok := s.vault.Get(app, name)
			if !ok {
				writeErr(w, http.StatusNotFound, "secret not found")
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"app": app, "name": name, "value": value})
		case http.MethodPut, http.MethodPost:
			var body struct {
				Value string `json:"value"`
				Note  string `json:"note"`
			}
			if !readJSON(w, r, &body) {
				return
			}
			if err := s.vault.Set(app, name, body.Value, body.Note); err != nil {
				writeErr(w, http.StatusBadRequest, err.Error())
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"app": app, "name": name, "stored": true})
		case http.MethodDelete:
			if !s.vault.Delete(app, name) {
				writeErr(w, http.StatusNotFound, "secret not found")
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"deleted": name})
		default:
			methodNotAllowed(w, "GET, PUT, POST, DELETE")
		}
		return
	}

	// /secrets/{app}/{name}/share[/{token}]
	if segs[2] == "share" {
		if len(segs) == 3 {
			if r.Method != http.MethodPost {
				methodNotAllowed(w, "POST")
				return
			}
			var body struct {
				TTLSeconds int `json:"ttl_seconds"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			tok, exp, err := s.vault.Share(app, name, ShareOptions{TTL: time.Duration(body.TTLSeconds) * time.Second})
			if err != nil {
				writeErr(w, http.StatusNotFound, err.Error())
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"token": tok, "expires": exp})
			return
		}
		tok := segs[3]
		switch r.Method {
		case http.MethodGet:
			value, ok := s.vault.Resolve(tok)
			if !ok {
				writeErr(w, http.StatusNotFound, "share not found or expired")
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"app": app, "name": name, "value": value})
		case http.MethodDelete:
			if !s.vault.Revoke(tok) {
				writeErr(w, http.StatusNotFound, "share not found")
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"revoked": tok})
		default:
			methodNotAllowed(w, "GET, DELETE")
		}
		return
	}
	http.NotFound(w, r)
}

// --- head ---

func (s *Server) serveHead(w http.ResponseWriter, r *http.Request) {
	app := strings.Trim(strings.TrimPrefix(r.URL.Path, "/head"), "/")
	switch r.Method {
	case http.MethodGet:
		payload := map[string]any{
			"title":     s.head.TitleValue(app),
			"fragments": nonNil(s.head.Fragments(app)),
		}
		if app == "" {
			payload["global"] = nonNil(s.head.GlobalFragments())
			apps := map[string][]string{}
			for _, name := range s.apps.Names() {
				if f := s.head.FragmentsFor(name); len(f) > 0 {
					apps[name] = f
				}
			}
			payload["apps"] = apps
		}
		writeJSON(w, http.StatusOK, payload)
	case http.MethodPost:
		var body struct {
			Fragment string `json:"fragment"`
			Title    string `json:"title"`
		}
		if !readJSON(w, r, &body) {
			return
		}
		if body.Fragment != "" {
			s.head.AddFor(app, body.Fragment)
		}
		if body.Title != "" {
			s.head.TitleFor(app, body.Title)
		}
		writeJSON(w, http.StatusOK, map[string]any{"fragments": nonNil(s.head.Fragments(app))})
	case http.MethodDelete:
		var body struct {
			Fragment string `json:"fragment"`
		}
		if !readJSON(w, r, &body) {
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"removed": s.head.RemoveFor(app, body.Fragment)})
	default:
		methodNotAllowed(w, "GET, POST, DELETE")
	}
}

// --- headers ---

func (s *Server) serveHeaders(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{"rules": nonNil(s.headers.Rules())})
	case http.MethodPost:
		var rule HeaderRule
		if !readJSON(w, r, &rule) {
			return
		}
		if err := s.headers.AddRule(rule); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"rules": nonNil(s.headers.Rules())})
	case http.MethodDelete:
		var rule HeaderRule
		if !readJSON(w, r, &rule) {
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"removed": s.headers.RemoveRule(rule)})
	default:
		methodNotAllowed(w, "GET, POST, DELETE")
	}
}

// --- platform surface ---

// shepherdRoutes are the shepherd endpoints atp fronts. Registering each one
// keeps /api/functions an honest inventory of what this deployment serves, and
// lets an operator switch an individual platform endpoint off.
var shepherdRoutes = []struct{ method, pattern string }{
	{"POST", "/api/keys"},
	{"POST", "/api/keys/block"},
	{"POST", "/api/keys/magic"},
	{"GET", "/api/keys/javascript"},
	{"POST", "/api/keys/regenerate"},
	{"POST", "/api/token/verify"},
	{"POST", "/api/revoke"},
	{"POST", "/api/revoke/block"},
	{"POST", "/api/provider/authenticate"},
	{"GET", "/api/firewall/rules"},
	{"POST", "/api/firewall/rules"},
	{"DELETE", "/api/firewall/rules"},
	{"DELETE", "/api/firewall/rules/{id}"},
	{"GET", "/api/ratelimit/status"},
	{"POST", "/api/ratelimit/reset"},
	{"GET", "/api/upstreams"},
	{"POST", "/api/upstreams"},
	{"DELETE", "/api/upstreams/{prefix}"},
	{"GET", "/api/track/{token}"},
	{"GET", "/verify"},
}

// registerPlatformFunctions wires atp's own JSON endpoints and the shepherd
// routes it forwards.
func (s *Server) registerPlatformFunctions() {
	must := func(err error) {
		if err != nil {
			panic(err)
		}
	}

	// registry surface (atp-local)
	must(s.Handle("GET", "/api/functions", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"functions": nonNil(s.registry.Routes())})
	}))
	must(s.Handle("POST", "/api/functions", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Method  string `json:"method"`
			Pattern string `json:"pattern"`
			Enabled *bool  `json:"enabled"`
		}
		if !readJSON(w, r, &body) {
			return
		}
		on := true
		if body.Enabled != nil {
			on = *body.Enabled
		}
		writeJSON(w, http.StatusOK, map[string]any{"updated": s.registry.SetEnabled(body.Method, body.Pattern, on)})
	}))

	// orchestration surface (atp-local)
	must(s.Handle("GET", "/api/platform", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"service":  "atp",
			"version":  Version,
			"root":     s.root,
			"addr":     s.addr,
			"services": s.Services(),
			"upstreams": nonNil(s.opts.Upstreams),
			"warnings": nonNil(s.Warnings()),
		})
	}))
	must(s.Handle("GET", "/api/services", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		writeJSON(w, http.StatusOK, map[string]any{"services": s.ProbeServices(ctx)})
	}))
	must(s.Handle("GET", "/api/static", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"static": nonNil(s.box.List())})
	}))
	must(s.Handle("GET", "/api/apps", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"apps": nonNil(s.apps.List())})
	}))

	// shepherd surface: a transparent proxy, with the admin token attached so
	// a browser never needs to hold it.
	shep := func(w http.ResponseWriter, r *http.Request) {
		s.shepherdProxy.ServeHTTP(w, r)
	}
	for _, rt := range shepherdRoutes {
		must(s.Handle(rt.method, rt.pattern, shep))
	}
}

// --- small helpers ---

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"error": msg})
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	defer r.Body.Close()
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "could not read body")
		return false
	}
	if len(body) == 0 {
		return true
	}
	if err := json.Unmarshal(body, v); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return false
	}
	return true
}

func methodNotAllowed(w http.ResponseWriter, allow string) {
	w.Header().Set("Allow", allow)
	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
