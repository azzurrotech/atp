package atp

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"azzurrotech/shepherd/firewall"
	"azzurrotech/shepherd/token"
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

// --- capability functions (registered under /api/… ) ---

// issueRequest is the wire form of a capability request.
type issueRequest struct {
	Subject    string            `json:"subject"`
	Scopes     []string          `json:"scopes"`
	Roles      []string          `json:"roles"`
	Audience   string            `json:"audience"`
	TTLSeconds int               `json:"ttl_seconds"`
	Block      string            `json:"block"`
	Magic      bool              `json:"magic"`
	Next       string            `json:"next"`
	Meta       map[string]string `json:"meta"`
}

func (ir issueRequest) options() token.IssueOptions {
	o := token.IssueOptions{
		Subject:  ir.Subject,
		Scopes:   ir.Scopes,
		Roles:    ir.Roles,
		Audience: ir.Audience,
		Block:    ir.Block,
		Magic:    ir.Magic,
		Next:     ir.Next,
		Meta:     ir.Meta,
	}
	if ir.TTLSeconds > 0 {
		o.TTL = time.Duration(ir.TTLSeconds) * time.Second
	}
	return o
}

func (s *Server) registerCapabilityFunctions() {
	must := func(err error) {
		if err != nil {
			panic(err)
		}
	}

	// registry surface
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

	// capability token issuance
	must(s.Handle("POST", "/api/keys", func(w http.ResponseWriter, r *http.Request) {
		var ir issueRequest
		if !readJSON(w, r, &ir) {
			return
		}
		raw, claims, err := s.shepherd.Issue(ir.options())
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"token": raw, "claims": claims})
	}))
	must(s.Handle("POST", "/api/keys/block", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			issueRequest
			BlockID string `json:"block_id"`
			Count   int    `json:"count"`
		}
		if !readJSON(w, r, &body) {
			return
		}
		keys, err := s.shepherd.Manager().IssueBlock(body.issueRequest.options(), body.BlockID, body.Count)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"keys": keys})
	}))
	must(s.Handle("POST", "/api/keys/magic", func(w http.ResponseWriter, r *http.Request) {
		var ir issueRequest
		if !readJSON(w, r, &ir) {
			return
		}
		ir.Magic = true
		raw, claims, err := s.shepherd.Manager().IssueMagicLink(ir.options())
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"token": raw, "claims": claims})
	}))
	must(s.Handle("GET", "/api/keys/javascript", func(w http.ResponseWriter, r *http.Request) {
		scope := r.URL.Query().Get("scope")
		usage := r.URL.Query().Get("usage")
		if usage == "" {
			usage = "encrypt"
		}
		key, err := s.shepherd.JSCryptoKey(scope, usage)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, key)
	}))

	// verification + revocation
	must(s.Handle("POST", "/api/token/verify", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Token string `json:"token"`
			Magic bool   `json:"magic"`
		}
		if !readJSON(w, r, &body) {
			return
		}
		var (
			claims token.Claims
			err    error
		)
		if body.Magic {
			claims, err = s.shepherd.VerifyMagicToken(body.Token)
		} else {
			claims, err = s.shepherd.Manager().Verify(body.Token)
		}
		if err != nil {
			writeErr(w, http.StatusUnauthorized, err.Error())
			return
		}
		// A revoked token still has a valid signature, so verification has to
		// consult the revocation list too. Without this the endpoint would
		// confirm a token the operator has explicitly cancelled — shepherd's
		// own verify endpoint checks this, and atp must agree with it.
		if s.shepherd.IsDenied(claims.ID) || (claims.Block != "" && s.shepherd.IsDenied(claims.Block)) {
			writeErr(w, http.StatusUnauthorized, "token revoked")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"valid": true, "claims": claims})
	}))
	must(s.Handle("POST", "/api/revoke", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			JTI       string `json:"jti"`
			ExpiresAt int64  `json:"expires_at"`
		}
		if !readJSON(w, r, &body) {
			return
		}
		s.shepherd.RevokeToken(body.JTI, body.ExpiresAt)
		writeJSON(w, http.StatusOK, map[string]any{"revoked": body.JTI})
	}))
	must(s.Handle("POST", "/api/revoke/block", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Block     string `json:"block"`
			ExpiresAt int64  `json:"expires_at"`
		}
		if !readJSON(w, r, &body) {
			return
		}
		s.shepherd.RevokeBlock(body.Block, body.ExpiresAt)
		writeJSON(w, http.StatusOK, map[string]any{"revoked_block": body.Block})
	}))

	// identity-free provider
	must(s.Handle("POST", "/api/provider/authenticate", func(w http.ResponseWriter, r *http.Request) {
		principal, err := s.shepherd.Provider().Authenticate(r)
		if err != nil {
			writeErr(w, http.StatusUnauthorized, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"principal": principal})
	}))

	// firewall
	must(s.Handle("GET", "/api/firewall/rules", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"default_allow": s.firewall.DefaultAllow(),
			"rules":         nonNil(s.firewall.Rules()),
		})
	}))
	must(s.Handle("POST", "/api/firewall/rules", func(w http.ResponseWriter, r *http.Request) {
		var rule firewallRule
		if !readJSON(w, r, &rule) {
			return
		}
		id, err := s.firewall.Add(rule.toFirewall())
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"id": id})
	}))
	must(s.Handle("DELETE", "/api/firewall/rules", func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("id")
		if id == "" {
			var body struct {
				ID string `json:"id"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			id = body.ID
		}
		writeJSON(w, http.StatusOK, map[string]any{"removed": s.firewall.Remove(id)})
	}))

	// rate limiting
	must(s.Handle("GET", "/api/ratelimit/status", func(w http.ResponseWriter, r *http.Request) {
		key := r.URL.Query().Get("key")
		if key == "" {
			key = s.shepherd.ClientIP(r)
		}
		status, ok := s.shepherd.RateLimitStatus(key)
		if !ok {
			writeJSON(w, http.StatusOK, map[string]any{"key": key, "limited": false})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"key": key, "limited": true, "status": status})
	}))
	must(s.Handle("POST", "/api/ratelimit/reset", func(w http.ResponseWriter, r *http.Request) {
		s.shepherd.RateLimitReset()
		writeJSON(w, http.StatusOK, map[string]any{"reset": true})
	}))

	// gateway upstreams
	must(s.Handle("GET", "/api/upstreams", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"mount": "/gw/", "upstreams": nonNil(s.opts.Upstreams)})
	}))
}

// firewallRule is the wire form of a firewall rule.
type firewallRule struct {
	ID       string            `json:"id"`
	Name     string            `json:"name"`
	Action   string            `json:"action"`
	Enabled  *bool             `json:"enabled"`
	Methods  []string          `json:"methods"`
	PathGlob string            `json:"path_glob"`
	Sources  []string          `json:"sources"`
	Headers  map[string]string `json:"headers"`
}

func (f firewallRule) toFirewall() firewall.Rule {
	enabled := true
	if f.Enabled != nil {
		enabled = *f.Enabled
	}
	return firewall.Rule{
		ID:       f.ID,
		Name:     f.Name,
		Action:   f.Action,
		Enabled:  enabled,
		Methods:  f.Methods,
		PathGlob: f.PathGlob,
		Sources:  f.Sources,
		Headers:  f.Headers,
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
