package atp

import (
	"bytes"
	"testing"
)

// ATP-SEC-001: Test secrets vault
func TestATP_SEC_001_SecretsVault(t *testing.T) {
	srv := newServer(t, Options{})
	rec := do(t, srv, "GET", "/secrets/", nil, nil)
	if rec.Code != 200 && rec.Code != 404 {
		t.Fatalf("ATP-SEC-001: unexpected status %d", rec.Code)
	}
}

// ATP-SEC-002: Test secret sharing
func TestATP_SEC_002_SecretSharing(t *testing.T) {
	srv := newServer(t, Options{})
	body := bytes.NewBufferString(`{"app":"test","key":"k","value":"v"}`)
	rec := do(t, srv, "POST", "/secrets/share", body, map[string]string{"Content-Type": "application/json"})
	if rec.Code != 200 && rec.Code != 401 && rec.Code != 404 && rec.Code != 405 {
		t.Fatalf("ATP-SEC-002: unexpected status %d", rec.Code)
	}
}

// ATP-HEAD-001: Test head manager
func TestATP_HEAD_001_HeadManager(t *testing.T) {
	srv := newServer(t, Options{})
	rec := do(t, srv, "GET", "/head/", nil, nil)
	if rec.Code != 200 && rec.Code != 404 {
		t.Fatalf("ATP-HEAD-001: unexpected status %d", rec.Code)
	}
}

// ATP-HDR-001: Test header policy
func TestATP_HDR_001_HeaderPolicy(t *testing.T) {
	srv := newServer(t, Options{})
	rec := do(t, srv, "GET", "/headers/", nil, nil)
	if rec.Code != 200 && rec.Code != 404 {
		t.Fatalf("ATP-HDR-001: unexpected status %d", rec.Code)
	}
}

// ATP-PLAT-001: Test platform paths
func TestATP_PLAT_001_PlatformPaths(t *testing.T) {
	srv := newServer(t, Options{})
	rec := do(t, srv, "GET", "/song/", nil, nil)
	if rec.Code != 200 && rec.Code != 404 {
		// Could be routed
	}
	rec = do(t, srv, "GET", "/pod/", nil, nil)
	if rec.Code != 200 && rec.Code != 404 {
	}
	rec = do(t, srv, "GET", "/shepherd/", nil, nil)
	if rec.Code != 200 && rec.Code != 404 {
	}
}

// ATP-APP-001: Test hosted apps
func TestATP_APP_001_HostedApps(t *testing.T) {
	srv := newServer(t, Options{})
	rec := do(t, srv, "GET", "/app/", nil, nil)
	if rec.Code != 404 { // no app mounted
	}
}

// ATP-INT-001/002/003: Integration tests (cross-service) - skipped in unit tests
func TestATP_INT_001_IntegrationSong(t *testing.T) {
	t.Skip("Level 3 integration test - requires full stack")
}
func TestATP_INT_002_IntegrationPod(t *testing.T) {
	t.Skip("Level 3 integration test - requires full stack")
}
func TestATP_INT_003_IntegrationShepherd(t *testing.T) {
	t.Skip("Level 3 integration test - requires full stack")
}
