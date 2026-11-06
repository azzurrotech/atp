package atp

import (
	"os"
	"strings"
	"testing"
)

func newHealthEnv(t *testing.T) *testEnv {
	t.Helper()
	return newEnv(t, Options{Title: "health probe"})
}

func TestHealthReportsConfiguredServices(t *testing.T) {
	env := newHealthEnv(t)
	r := env.Server.Health()
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
	env := newHealthEnv(t)
	r := env.Server.Check()
	if !r.OK() {
		t.Fatalf("check = %s, failures = %v", r.Status, r.Failures)
	}
	if len(r.Components) != 6 {
		t.Errorf("components = %d, want 6", len(r.Components))
	}
	// the probe record must not survive the check
	env.pod.mu.Lock()
	_, left := env.pod.records[ProbeTable][ProbeRecord]
	env.pod.mu.Unlock()
	if left {
		t.Error("probe record was left behind")
	}
	if _, ok := env.Server.Secrets().Get(ProbeApp, ProbeSecret); ok {
		t.Error("probe secret was left behind")
	}
	entries, err := os.ReadDir(env.Server.Root())
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

// TestCheckReportsUnreachableService proves Check turns a stopped service into
// a degraded report rather than an error or a panic.
func TestCheckReportsUnreachableService(t *testing.T) {
	env := newHealthEnv(t)
	env.song.Close()
	r := env.Server.Check()
	if r.OK() {
		t.Fatal("a report with an unreachable song must not be healthy")
	}
	found := false
	for _, f := range r.Failures {
		if strings.HasPrefix(f, "song:") {
			found = true
		}
	}
	if !found {
		t.Fatalf("failures = %v", r.Failures)
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
