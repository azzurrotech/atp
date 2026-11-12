package atp

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

// portOf extracts the port httptest assigned.
func portOf(t *testing.T, rawURL string) int {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse %q: %v", rawURL, err)
	}
	_, port, err := net.SplitHostPort(u.Host)
	if err != nil {
		t.Fatalf("split %q: %v", u.Host, err)
	}
	var n int
	if _, err := fmt.Sscanf(port, "%d", &n); err != nil {
		t.Fatalf("port %q: %v", port, err)
	}
	return n
}

// freePort returns a port number nothing is listening on. It binds and closes,
// so the port is free at the moment it is returned.
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return port
}

// TestAutoDiscoveryIdentifiesServices proves a scan reports what answered, not
// just that a port was open, for the three health-document shapes in use.
func TestAutoDiscoveryIdentifiesServices(t *testing.T) {
	srv := newServer(t, Options{})

	atpLike := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/health":
			_, _ = w.Write([]byte(`{"status":"healthy","version":"9.9.9","components":[]}`))
		case "/api/apps":
			_, _ = w.Write([]byte(`{"apps":[{"name":"discovered-app"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer atpLike.Close()

	podLike := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			_, _ = w.Write([]byte(`{"service":"pod","version":"1.2.3","status":"ok"}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer podLike.Close()

	songLike := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/_song/health" {
			_, _ = w.Write([]byte(`{"service":"song","version":"2.0.0","status":"healthy"}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer songLike.Close()

	ports := []int{portOf(t, atpLike.URL), portOf(t, podLike.URL), portOf(t, songLike.URL)}
	res, err := srv.AutoDiscovery(context.Background(), AutoDiscoveryConfig{
		Host:         "127.0.0.1",
		Ports:        ports,
		RegisterApps: true,
		CheckTimeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("AutoDiscovery: %v", err)
	}
	if len(res.Probes) != len(ports) {
		t.Fatalf("got %d probes, want %d", len(res.Probes), len(ports))
	}

	wantService := map[int]string{ports[0]: "atp", ports[1]: "pod", ports[2]: "song"}
	wantVersion := map[int]string{ports[0]: "9.9.9", ports[1]: "1.2.3", ports[2]: "2.0.0"}
	for _, pr := range res.Probes {
		if !pr.Healthy {
			t.Errorf("port %d: not healthy (%v)", pr.Port, pr.Error)
		}
		if got, want := pr.Service, wantService[pr.Port]; got != want {
			t.Errorf("port %d: service = %q, want %q", pr.Port, got, want)
		}
		if got, want := pr.Version, wantVersion[pr.Port]; got != want {
			t.Errorf("port %d: version = %q, want %q", pr.Port, got, want)
		}
	}

	// Registration is narrow by design: only an atp identity has /api/apps.
	if res.TotalApps != 1 {
		t.Errorf("TotalApps = %d, want 1", res.TotalApps)
	}
	if _, ok := srv.Apps().Get("discovered-app"); !ok {
		t.Error("app from discovered atp was not registered")
	}
	if got := res.Services["pod"]; len(got) != 1 || got[0] != ports[1] {
		t.Errorf("Services[pod] = %v, want [%d]", got, ports[1])
	}
}

// TestAutoDiscoveryUnhealthyPort proves a closed port is a normal result, not an
// error, and carries the reason.
func TestAutoDiscoveryUnhealthyPort(t *testing.T) {
	srv := newServer(t, Options{})
	dead := freePort(t)

	res, err := srv.AutoDiscovery(context.Background(), AutoDiscoveryConfig{
		Host:         "127.0.0.1",
		Ports:        []int{dead},
		CheckTimeout: 500 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("AutoDiscovery: %v", err)
	}
	pr := res.Probes[0]
	if pr.Healthy {
		t.Fatalf("port %d reported healthy, but nothing listens there", dead)
	}
	if pr.Error == nil {
		t.Error("unhealthy probe should carry an error")
	}
}

// TestAutoDiscoveryHealthyOn404 pins the rule that any HTTP response counts:
// a 404 means something is listening, so the port is healthy.
func TestAutoDiscoveryHealthyOn404(t *testing.T) {
	srv := newServer(t, Options{})
	notFound := httptest.NewServer(http.NotFoundHandler())
	defer notFound.Close()

	res, err := srv.AutoDiscovery(context.Background(), AutoDiscoveryConfig{
		Host:         "127.0.0.1",
		Ports:        []int{portOf(t, notFound.URL)},
		CheckTimeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("AutoDiscovery: %v", err)
	}
	pr := res.Probes[0]
	if !pr.Healthy {
		t.Fatalf("404-only port reported unhealthy: %v", pr.Error)
	}
	if pr.Service != "" {
		t.Errorf("404-only port identified as %q, want empty", pr.Service)
	}
}

// TestAutoDiscoveryObjectComponents proves a service whose health document uses
// an object map for "components" (rapid) is still identified. A slice-typed
// field would fail to decode and drop the service/version that follow.
func TestAutoDiscoveryObjectComponents(t *testing.T) {
	srv := newServer(t, Options{})
	rapidLike := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/health" {
			_, _ = w.Write([]byte(`{"components":{"rapid":true},"ok":true,"platform":"azzurro","service":"rapid","status":"ok","version":"1.0.0"}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer rapidLike.Close()

	res, err := srv.AutoDiscovery(context.Background(), AutoDiscoveryConfig{
		Host:         "127.0.0.1",
		Ports:        []int{portOf(t, rapidLike.URL)},
		CheckTimeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("AutoDiscovery: %v", err)
	}
	pr := res.Probes[0]
	if pr.Service != "rapid" {
		t.Errorf("service = %q, want %q", pr.Service, "rapid")
	}
	if pr.Version != "1.0.0" {
		t.Errorf("version = %q, want %q", pr.Version, "1.0.0")
	}
}

// TestAutoDiscoveryDefaults proves an empty config scans DefaultPortsToScan.
func TestAutoDiscoveryDefaults(t *testing.T) {
	srv := newServer(t, Options{Addr: ":0"})
	res, err := srv.AutoDiscovery(context.Background(), AutoDiscoveryConfig{
		Host:         "127.0.0.1",
		CheckTimeout: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("AutoDiscovery: %v", err)
	}
	if len(res.Probes) != len(DefaultPortsToScan) {
		t.Fatalf("scanned %d ports, want %d", len(res.Probes), len(DefaultPortsToScan))
	}
	for i, pr := range res.Probes {
		if pr.Port != DefaultPortsToScan[i] {
			t.Errorf("probe %d = port %d, want %d", i, pr.Port, DefaultPortsToScan[i])
		}
	}
}

// TestOwnPort proves a server recognises its own listen port so a scan does not
// re-register its own apps.
func TestOwnPort(t *testing.T) {
	cases := map[string]int{
		":8080":          8080,
		"127.0.0.1:9090": 9090,
		"[::1]:8083":     8083,
		"not-an-address": 0,
	}
	for addr, want := range cases {
		srv := newServer(t, Options{Addr: addr})
		if got := srv.ownPort(); got != want {
			t.Errorf("ownPort(%q) = %d, want %d", addr, got, want)
		}
	}
}
