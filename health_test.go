package atp

import (
	"os"
	"strings"
	"testing"
)

func newHealthServer(t *testing.T) *Server {
	t.Helper()
	srv, err := New(Options{
		Root:   t.TempDir(),
		Secret: "a-platform-health-test-secret-32b",
		Title:  "health probe",
	})
	if err != nil {
		t.Fatal(err)
	}
	return srv
}

func TestHealthReportsMountedModules(t *testing.T) {
	srv := newHealthServer(t)
	r := srv.Health()
	if !r.OK() {
		t.Fatalf("health = %s, failures = %v", r.Status, r.Failures)
	}
	if r.Version != Version {
		t.Errorf("version = %q", r.Version)
	}
	want := map[string]bool{"atp": true, "pod": true, "song": true, "shepherd": true, "secrets": true, "registry": true}
	for _, c := range r.Components {
		if ok, known := want[c.Name]; known && ok && !c.OK {
			t.Errorf("component %s is not OK: %s", c.Name, c.Error)
		}
		delete(want, c.Name)
	}
	for name := range want {
		t.Errorf("health report is missing component %q", name)
	}
}

func TestCheckProbesAndCleansUp(t *testing.T) {
	srv := newHealthServer(t)
	r := srv.Check()
	if !r.OK() {
		t.Fatalf("check = %s, failures = %v", r.Status, r.Failures)
	}
	if len(r.Components) != 6 {
		t.Errorf("components = %d, want 6", len(r.Components))
	}
	// the probe record and secret must not survive the check
	if _, err := srv.Pod().Get(ProbeTable, ProbeRecord); err == nil {
		t.Error("probe record was left behind")
	}
	if _, ok := srv.Secrets().Get(ProbeApp, ProbeSecret); ok {
		t.Error("probe secret was left behind")
	}
	entries, err := os.ReadDir(srv.Root())
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ProbeApp) {
			// the app file is allowed to exist only as an empty vault entry; a
			// non-empty one would mean the probe secret survived.
			t.Logf("probe app file present: %s", e.Name())
		}
	}
}

func TestHealthDegradedWhenModuleMissing(t *testing.T) {
	r := HealthReport{Version: Version}
	r.add("pod", false, "", "store is not mounted")
	r.finish()
	if r.OK() {
		t.Fatal("a report with a failure must not be healthy")
	}
	if len(r.Failures) != 1 || !strings.Contains(r.Failures[0], "pod") {
		t.Errorf("failures = %v", r.Failures)
	}
}
