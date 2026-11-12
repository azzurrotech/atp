package atp

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// doAs issues a request from a specific peer address. The shared do() helper
// always presents loopback, so the control-plane policy needs its own entry
// point to test the remote case.
func doAs(t *testing.T, h http.Handler, peer, method, target string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	req.RemoteAddr = peer
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// TestControlPlaneLoopbackOnly pins the default policy: with no admin token,
// instance control answers loopback clients and refuses everyone else.
func TestControlPlaneLoopbackOnly(t *testing.T) {
	srv := newServer(t, Options{})

	if rec := doAs(t, srv, loopbackAddr, "GET", "/api/instances"); rec.Code != http.StatusOK {
		t.Fatalf("loopback control = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	rec := doAs(t, srv, remoteAddr, "GET", "/api/instances")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("remote control = %d, want 403 (%s)", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); !strings.Contains(body, "loopback-only") {
		t.Fatalf("403 body should name the policy, got %q", body)
	}

	// The guard must cover every control verb, not just the list endpoint.
	for _, tc := range []struct{ method, target string }{
		{"GET", "/api/instances/anything"},
		{"POST", "/api/instances/anything"},
	} {
		if rec := doAs(t, srv, remoteAddr, tc.method, tc.target); rec.Code != http.StatusForbidden {
			t.Fatalf("%s %s from remote = %d, want 403", tc.method, tc.target, rec.Code)
		}
	}
}

// TestControlPlaneAdminToken pins the remote policy: setting ATP_ADMIN_TOKEN
// replaces loopback-only with a shared token, and the token is required from
// loopback too (a configured token is never bypassed).
func TestControlPlaneAdminToken(t *testing.T) {
	srv := newServer(t, Options{AdminToken: testAdminToken})

	cases := []struct {
		name   string
		peer   string
		token  string
		status int
	}{
		{"remote with token", remoteAddr, testAdminToken, http.StatusOK},
		{"remote without token", remoteAddr, "", http.StatusUnauthorized},
		{"remote with wrong token", remoteAddr, "not-the-token", http.StatusUnauthorized},
		{"loopback with token", loopbackAddr, testAdminToken, http.StatusOK},
		{"loopback without token", loopbackAddr, "", http.StatusUnauthorized},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/api/instances", nil)
			req.RemoteAddr = tc.peer
			if tc.token != "" {
				req.Header.Set("X-ATP-Admin", tc.token)
			}
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, req)
			if rec.Code != tc.status {
				t.Fatalf("%s = %d, want %d (%s)", tc.name, rec.Code, tc.status, rec.Body.String())
			}
		})
	}
}

func TestIsLoopbackRequest(t *testing.T) {
	cases := []struct {
		addr string
		want bool
	}{
		{"", true}, // in-process (test or embedder)
		{"127.0.0.1:1234", true},
		{"[::1]:1234", true},
		{"127.0.0.2:1234", true},
		{"203.0.113.9:1234", false},
		{"[2001:db8::1]:1234", false},
		{"garbage", false},
	}
	for _, tc := range cases {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = tc.addr
		if got := isLoopbackRequest(r); got != tc.want {
			t.Errorf("isLoopbackRequest(%q) = %v, want %v", tc.addr, got, tc.want)
		}
	}
}

func TestShepherdRouteNeedsAdmin(t *testing.T) {
	for _, p := range []string{
		"/api/keys", "/api/keys/block", "/api/keys/magic", "/api/keys/javascript",
		"/api/keys/regenerate", "/api/revoke", "/api/revoke/block",
		"/api/firewall/rules", "/api/firewall/rules/{id}",
		"/api/ratelimit/status", "/api/ratelimit/reset",
		"/api/upstreams", "/api/upstreams/{prefix}",
	} {
		if !shepherdRouteNeedsAdmin(p) {
			t.Errorf("shepherdRouteNeedsAdmin(%q) = false, want true", p)
		}
	}
	for _, p := range []string{"/api/token/verify", "/api/provider/authenticate", "/api/track/{token}"} {
		if shepherdRouteNeedsAdmin(p) {
			t.Errorf("shepherdRouteNeedsAdmin(%q) = true, want false (public route)", p)
		}
	}
}
