package web

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestAllAdminPagesRender(t *testing.T) {
	root := filepath.Join(t.TempDir(), "data")
	s, err := NewATPService(Options{Root: root, Secret: testSecret, AdminUser: "admin", AdminPassword: "s3cret", Seed: true})
	if err != nil {
		t.Fatal(err)
	}
	cookie := login(t, s.Handler())
	pages := []string{
		"/",
		"/clients",
		"/clients/demo",
		"/clients/demo/files",
		"/clients/demo/tables",
		"/clients/demo/security",
		"/clients/demo/usage",
		"/clients/demo/secrets",
	}
	for _, p := range pages {
		req := httptest.NewRequest("GET", p, nil)
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("%s: HTTP %d %s", p, rec.Code, rec.Body.String())
			continue
		}
		body := rec.Body.String()
		if !strings.Contains(body, "<!doctype html>") || !strings.Contains(body, "</html>") {
			t.Errorf("%s: not a complete page", p)
		}
	}
}

func TestNoUI(t *testing.T) {
	root := filepath.Join(t.TempDir(), "data")
	s, err := NewATPService(Options{Root: root, Secret: testSecret, DisableUI: true})
	if err != nil {
		t.Fatal(err)
	}
	rec := doJSON(t, s.Handler(), "GET", "/login", "", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("UI should be disabled, got %d", rec.Code)
	}
	rec = doJSON(t, s.Handler(), "GET", "/health", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("health must still work with UI disabled: %d", rec.Code)
	}
}