package atp

// test services — minimal stand-ins for pod, song and shepherd.
//
// atp talks to the three services over HTTP, so these fakes are the contract
// the tests verify: request shape in, documented JSON out. Token cryptography
// and record XML are the modules' own business and are covered by their own
// suites; what atp must get right is the forwarding, the prefix stripping and
// the admin-token attachment.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// --- pod ---

type fakePod struct {
	*httptest.Server
	mu      sync.Mutex
	records map[string]map[string]map[string]string // table -> id -> fields
	nextID  int
}

func newFakePod(t *testing.T) *fakePod {
	t.Helper()
	p := &fakePod{records: map[string]map[string]map[string]string{}}
	p.Server = httptest.NewServer(http.HandlerFunc(p.serve))
	t.Cleanup(p.Close)
	return p
}

func (p *fakePod) serve(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/")
	switch {
	case path == "health":
		writeJSON(w, http.StatusOK, map[string]any{"status": "healthy", "service": "pod"})
	case path == "tables":
		p.mu.Lock()
		names := make([]string, 0, len(p.records))
		for n := range p.records {
			names = append(names, n)
		}
		p.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"tables": names})
	case strings.HasPrefix(path, "table/"):
		p.table(w, r, strings.TrimPrefix(path, "table/"))
	case strings.HasPrefix(path, "record/"):
		rest := strings.TrimPrefix(path, "record/")
		i := strings.LastIndexByte(rest, '/')
		if i < 0 {
			http.NotFound(w, r)
			return
		}
		p.record(w, r, rest[:i], rest[i+1:])
	default:
		http.NotFound(w, r)
	}
}

func (p *fakePod) table(w http.ResponseWriter, r *http.Request, table string) {
	switch r.Method {
	case http.MethodGet:
		p.mu.Lock()
		rows := make([]map[string]any, 0, len(p.records[table]))
		for id, fields := range p.records[table] {
			row := map[string]any{"id": id, "fields": fields}
			rows = append(rows, row)
		}
		p.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"count": len(rows), "records": rows})
	case http.MethodPost, http.MethodPut:
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		id, _ := body["id"].(string)
		if id == "" {
			p.mu.Lock()
			p.nextID++
			id = "rec-" + strconv.Itoa(p.nextID)
			p.mu.Unlock()
		}
		fields := map[string]string{}
		for k, v := range body {
			if k == "id" || k == "_id" {
				continue
			}
			fields[k] = fmt.Sprint(v)
		}
		p.mu.Lock()
		if p.records[table] == nil {
			p.records[table] = map[string]map[string]string{}
		}
		p.records[table][id] = fields
		p.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"id": id, "fields": fields})
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
	}
}

func (p *fakePod) record(w http.ResponseWriter, r *http.Request, table, id string) {
	switch r.Method {
	case http.MethodGet:
		p.mu.Lock()
		fields, ok := p.records[table][id]
		p.mu.Unlock()
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "record not found"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"id": id, "fields": fields})
	case http.MethodDelete:
		p.mu.Lock()
		_, ok := p.records[table][id]
		delete(p.records[table], id)
		p.mu.Unlock()
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "record not found"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"deleted": id})
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
	}
}

// --- song ---

type fakeSong struct {
	*httptest.Server
	root string
	mu   sync.Mutex
	reqs []string
}

func newFakeSong(t *testing.T, appsDir string) *fakeSong {
	t.Helper()
	s := &fakeSong{root: appsDir}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.Close)
	return s
}

func (s *fakeSong) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.reqs = append(s.reqs, r.Method+" "+r.URL.Path)
	s.mu.Unlock()

	switch r.URL.Path {
	case "/_song/health":
		writeJSON(w, http.StatusOK, map[string]any{"status": "healthy", "service": "song"})
		return
	case "/_song/silos":
		entries, _ := os.ReadDir(s.root)
		silos := []string{}
		for _, e := range entries {
			if e.IsDir() {
				silos = append(silos, e.Name())
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"root": s.root, "silos": silos})
		return
	}
	if r.Method == http.MethodPost {
		rel := strings.TrimPrefix(r.URL.Path, "/")
		body, _ := io.ReadAll(r.Body)
		if err := os.MkdirAll(filepath.Dir(filepath.Join(s.root, rel)), 0o755); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := os.WriteFile(filepath.Join(s.root, rel), body, 0o644); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"message": "resource created"})
		return
	}
	http.FileServer(http.Dir(s.root)).ServeHTTP(w, r)
}

// --- shepherd ---

type fakeShepherd struct {
	*httptest.Server
	mu       sync.Mutex
	admin    []string // X-Shepherd-Admin values seen, in order
	methods  []string // "METHOD path" seen, in order
	revoked  map[string]bool
	issues   int
	rules    []map[string]any
	upstream []map[string]any
}

func newFakeShepherd(t *testing.T) *fakeShepherd {
	t.Helper()
	s := &fakeShepherd{revoked: map[string]bool{}}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.Close)
	return s
}

func (s *fakeShepherd) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.admin = append(s.admin, r.Header.Get("X-Shepherd-Admin"))
	s.methods = append(s.methods, r.Method+" "+r.URL.Path)
	s.mu.Unlock()

	switch {
	case r.URL.Path == "/health":
		writeJSON(w, http.StatusOK, map[string]any{"status": "healthy", "service": "shepherd"})
	case r.URL.Path == "/api/keys/block":
		writeJSON(w, http.StatusCreated, map[string]any{"block": "b1", "count": 1, "keys": []any{}})
	case r.URL.Path == "/api/keys/magic":
		writeJSON(w, http.StatusCreated, map[string]any{"url": "http://x/verify?t=m", "token": "magic-1"})
	case r.URL.Path == "/api/keys/javascript":
		writeJSON(w, http.StatusOK, map[string]any{"algorithm": "AES-GCM", "key": "k"})
	case r.URL.Path == "/api/keys/regenerate":
		writeJSON(w, http.StatusCreated, map[string]any{"token": "regenerated"})
	case r.URL.Path == "/api/keys":
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		s.mu.Lock()
		s.issues++
		id := s.issues
		s.mu.Unlock()
		writeJSON(w, http.StatusCreated, map[string]any{
			"token": fmt.Sprintf("tok-%d", id),
			"claims": map[string]any{
				"jti": fmt.Sprintf("jti-%d", id),
				"sub": body["subject"],
				"exp": 4102444800,
			},
		})
	case r.URL.Path == "/api/token/verify":
		var body struct {
			Token string `json:"token"`
			Magic bool   `json:"magic"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if !strings.HasPrefix(body.Token, "tok-") {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"valid": false, "error": "invalid token"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"valid":  true,
			"claims": map[string]any{"jti": body.Token, "sub": "user@example"},
		})
	case r.URL.Path == "/api/revoke":
		var body struct {
			JTI string `json:"jti"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		writeJSON(w, http.StatusOK, map[string]any{"revoked": body.JTI})
	case r.URL.Path == "/api/revoke/block":
		writeJSON(w, http.StatusOK, map[string]any{"revoked_block": "b1"})
	case r.URL.Path == "/api/provider/authenticate":
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if tok == "" {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"authenticated": false})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"authenticated": true,
			"subject":       "user@example",
			"principal":     map[string]any{"subject": "user@example", "scopes": []string{"read:orders"}},
		})
	case r.URL.Path == "/api/firewall/rules":
		switch r.Method {
		case http.MethodGet:
			s.mu.Lock()
			rules := append([]map[string]any(nil), s.rules...)
			s.mu.Unlock()
			writeJSON(w, http.StatusOK, map[string]any{"default_allow": true, "rules": rules})
		case http.MethodPost:
			var rule map[string]any
			_ = json.NewDecoder(r.Body).Decode(&rule)
			s.mu.Lock()
			s.rules = append(s.rules, rule)
			s.mu.Unlock()
			writeJSON(w, http.StatusCreated, map[string]any{"id": "r1"})
		case http.MethodDelete:
			writeJSON(w, http.StatusOK, map[string]any{"removed": "r1"})
		}
	case r.URL.Path == "/api/ratelimit/status":
		writeJSON(w, http.StatusOK, map[string]any{"key": "ip:127.0.0.1", "limited": false})
	case r.URL.Path == "/api/ratelimit/reset":
		writeJSON(w, http.StatusOK, map[string]any{"reset": true})
	case r.URL.Path == "/api/upstreams":
		if r.Method == http.MethodPost {
			var u map[string]any
			_ = json.NewDecoder(r.Body).Decode(&u)
			s.mu.Lock()
			s.upstream = append(s.upstream, u)
			s.mu.Unlock()
			writeJSON(w, http.StatusCreated, map[string]any{"upstream": u, "mount": "/gw"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"mount": "/gw", "upstreams": s.upstream})
	case r.URL.Path == "/verify":
		writeJSON(w, http.StatusOK, map[string]any{"verified": true})
	case strings.HasPrefix(r.URL.Path, "/api/track/"):
		writeJSON(w, http.StatusOK, map[string]any{"tracked": true, "subject": "user@example"})
	case strings.HasPrefix(r.URL.Path, "/gw/"):
		_, _ = io.WriteString(w, "gateway reached")
	default:
		http.NotFound(w, r)
	}
}

// sawAdmin reports whether any forwarded request carried the admin token.
func (s *fakeShepherd) sawAdmin(token string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, v := range s.admin {
		if v == token {
			return true
		}
	}
	return false
}

func (s *fakeShepherd) saw(methodPath string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, v := range s.methods {
		if v == methodPath {
			return true
		}
	}
	return false
}
