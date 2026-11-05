package atp

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ATP-CORE-001: Test server creation
func TestATP_CORE_001_NewValidConfig(t *testing.T) {
	srv := newServer(t, Options{Root: t.TempDir(), Secret: testSecret})
	if srv == nil {
		t.Fatal("ATP-CORE-001: expected server instance")
	}
	if srv.Addr() == "" {
		t.Fatal("ATP-CORE-001: expected non-empty addr")
	}
}

// ATP-CORE-002: Test routing
func TestATP_CORE_002_Routing(t *testing.T) {
	srv := newServer(t, Options{})
	rec := do(t, srv, "GET", "/nonexistent/path/12345", nil, nil)
	if rec.Code != 404 {
		t.Fatalf("ATP-CORE-002: expected 404, got %d", rec.Code)
	}
}

// ATP-FUNC-001: Test function registration
func TestATP_FUNC_001_RegisterFunction(t *testing.T) {
	srv := newServer(t, Options{})
	called := false
	if err := srv.Handle("GET", "/api/test", func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(200)
	}); err != nil {
		t.Fatalf("ATP-FUNC-001: registration failed: %v", err)
	}
	rec := do(t, srv, "GET", "/api/test", nil, nil)
	if rec.Code != 200 {
		t.Fatalf("ATP-FUNC-001: expected 200, got %d", rec.Code)
	}
	if !called {
		t.Fatal("ATP-FUNC-001: handler not called")
	}
}

// ATP-FUNC-002: Test function execution
func TestATP_FUNC_002_FunctionExecution(t *testing.T) {
	srv := newServer(t, Options{})
	if err := srv.HandleFunc("POST", "/api/echo", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		w.Write([]byte(`{"ok":true}`))
	}); err != nil {
		t.Fatalf("ATP-FUNC-002: registration failed: %v", err)
	}
	rec := do(t, srv, "POST", "/api/echo", bytes.NewBufferString("{}"), map[string]string{"Content-Type": "application/json"})
	if rec.Code != 200 {
		t.Fatalf("ATP-FUNC-002: expected 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "ok") {
		t.Fatalf("ATP-FUNC-002: unexpected response: %s", rec.Body.String())
	}
}

// ATP-MW-001: Test middleware
func TestATP_MW_001_Middleware(t *testing.T) {
	srv := newServer(t, Options{})
	marked := false
	handler := srv.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		marked = true
		w.WriteHeader(200)
	}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)
	handler.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("ATP-MW-001: expected 200, got %d", rec.Code)
	}
	_ = marked
}

// ATP-API-001: Test functions API
func TestATP_API_001_FunctionsAPI(t *testing.T) {
	srv := newServer(t, Options{})
	rec := do(t, srv, "GET", "/api/functions", nil, nil)
	if rec.Code != 200 {
		t.Fatalf("ATP-API-001: expected 200, got %d", rec.Code)
	}
	var v any
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("ATP-API-001: invalid JSON: %v", err)
	}
}

// ATP-API-002: Test keys API
func TestATP_API_002_KeysAPI(t *testing.T) {
	srv := newServer(t, Options{})
	rec := do(t, srv, "POST", "/api/keys", bytes.NewBufferString(`{"scopes":["read"]}`), map[string]string{"Content-Type": "application/json"})
	if rec.Code != 200 && rec.Code != 401 { // may require auth
		t.Fatalf("ATP-API-002: unexpected status %d", rec.Code)
	}
}

// ATP-API-003: Test firewall API
func TestATP_API_003_FirewallAPI(t *testing.T) {
	srv := newServer(t, Options{})
	rec := do(t, srv, "GET", "/api/firewall/rules", nil, nil)
	if rec.Code != 200 {
		t.Fatalf("ATP-API-003: expected 200, got %d", rec.Code)
	}
}

// ATP-API-004: Test rate limit API
func TestATP_API_004_RateLimitAPI(t *testing.T) {
	srv := newServer(t, Options{})
	rec := do(t, srv, "GET", "/api/ratelimit/status", nil, nil)
	if rec.Code != 200 {
		t.Fatalf("ATP-API-004: expected 200, got %d", rec.Code)
	}
}

// ATP-API-005: Test upstreams API
func TestATP_API_005_UpstreamsAPI(t *testing.T) {
	srv := newServer(t, Options{})
	rec := do(t, srv, "GET", "/api/upstreams", nil, nil)
	if rec.Code != 200 {
		t.Fatalf("ATP-API-005: expected 200, got %d", rec.Code)
	}
}
