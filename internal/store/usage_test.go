package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newTestUsage(t *testing.T) (*UsageStore, *LogStore, *ClientStore) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "data")
	clients, err := NewClientStore(root)
	if err != nil {
		t.Fatal(err)
	}
	clients.Create("acme", "Acme", "")
	u, err := NewUsageStore(root, clients)
	if err != nil {
		t.Fatal(err)
	}
	l, err := NewLogStore(root, clients)
	if err != nil {
		t.Fatal(err)
	}
	return u, l, clients
}

func TestBillingDefaultRate(t *testing.T) {
	u, _, clients := newTestUsage(t)
	c, _ := clients.Get("acme")
	if c.PricePerGBHour != 0.05 {
		t.Fatalf("default rate = %v, want 0.05", c.PricePerGBHour)
	}
	// Sample a silo of exactly 1 GiB for the current hour.
	u.SampleSize("acme", gb)
	u.Flush()

	bill, err := u.Billing("acme")
	if err != nil {
		t.Fatal(err)
	}
	// One sampled hour at 1 GiB at 0.05 USD/GB/h = 0.05.
	if bill.TotalCost != 0.05 {
		t.Errorf("cost = %v, want 0.05", bill.TotalCost)
	}
	if bill.AvgSiloGB != 1.0 || bill.SampledHours != 1 {
		t.Errorf("avg=%v sampled=%d", bill.AvgSiloGB, bill.SampledHours)
	}
}

func TestBillingAveragesAcrossHours(t *testing.T) {
	u, _, _ := newTestUsage(t)
	// Two hours: 1 GiB and 3 GiB → avg 2 GiB → 2*0.05 = 0.10.
	// The usage store keys buckets by the CURRENT wall-clock hour, so to
	// simulate past hours we write usage files directly.
	now := time.Now().UTC()
	h1 := now.Add(-2 * time.Hour).Format(hourFormat)
	h2 := now.Add(-1 * time.Hour).Format(hourFormat)
	f := &usageFile{Version: 1, Hours: map[string]*HourUsage{
		h1: {Hour: h1, SiloBytes: gb, Sampled: true, SampleCount: 1, Requests: 10},
		h2: {Hour: h2, SiloBytes: 3 * gb, Sampled: true, SampleCount: 1, Requests: 5},
	}}
	data, _ := json.Marshal(f)
	if err := os.WriteFile(u.path("acme"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	bill, err := u.Billing("acme")
	if err != nil {
		t.Fatal(err)
	}
	// Two billable GiB-hours at 0.05 USD/GB/h = 0.20.
	if bill.TotalCost != 0.20 {
		t.Errorf("cost = %v, want 0.20", bill.TotalCost)
	}
	if bill.AvgSiloGB != 2.0 {
		t.Errorf("avg = %v, want 2.0", bill.AvgSiloGB)
	}
	if bill.TotalRequests != 15 {
		t.Errorf("requests = %d, want 15", bill.TotalRequests)
	}
}

func TestBillingNotChargeableIsZero(t *testing.T) {
	u, _, clients := newTestUsage(t)
	clients.SetChargeable("acme", false)
	u.SampleSize("acme", gb)
	u.Flush()
	bill, _ := u.Billing("acme")
	if bill.TotalCost != 0 {
		t.Errorf("non-chargeable client billed %v", bill.TotalCost)
	}
}

func TestRequestVolumeCounted(t *testing.T) {
	u, _, _ := newTestUsage(t)
	u.Record("acme", 100, 250)
	u.Record("acme", 50, 75)
	u.Flush()
	bill, _ := u.Billing("acme")
	if bill.TotalRequests != 2 {
		t.Errorf("requests = %d", bill.TotalRequests)
	}
	if bill.TotalReqBytes != 150 || bill.TotalRespBytes != 325 {
		t.Errorf("bytes wrong: req=%d resp=%d", bill.TotalReqBytes, bill.TotalRespBytes)
	}
}

func TestLogStoreRecentAndPrune(t *testing.T) {
	_, l, _ := newTestUsage(t)
	now := time.Now().UTC()

	older := RequestRecord{
		Time: now.Add(-48 * time.Hour).Format(time.RFC3339Nano), Client: "acme",
		Method: "GET", Path: "/old", Status: 200, ReqBytes: 1, RespBytes: 2,
	}
	newer := RequestRecord{
		Time: now.Format(time.RFC3339Nano), Client: "acme",
		Method: "POST", Path: "/new", Status: 201, ReqBytes: 3, RespBytes: 4,
	}
	l.Write(older)
	l.Write(newer)

	recs, err := l.Recent("acme", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 {
		t.Fatalf("recs = %d, want 2", len(recs))
	}
	if recs[0].Path != "/new" { // newest first
		t.Errorf("first = %s, want /new", recs[0].Path)
	}

	// Prune with 24h retention removes the old day's file.
	l.Prune("acme", DefaultRetentionHours)
	recs, err = l.Recent("acme", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || recs[0].Path != "/new" {
		t.Fatalf("after prune got %d records, want only /new", len(recs))
	}
}

func TestLogStoreComesBackViaReload(t *testing.T) {
	root := filepath.Join(t.TempDir(), "data")
	clients, _ := NewClientStore(root)
	clients.Create("acme", "A", "")
	l, _ := NewLogStore(root, clients)
	l.Write(RequestRecord{Time: time.Now().UTC().Format(time.RFC3339Nano), Client: "acme", Method: "GET", Path: "/x"})

	l2, _ := NewLogStore(root, clients)
	recs, err := l2.Recent("acme", 10)
	if err != nil || len(recs) != 1 {
		t.Fatalf("reload: %d recs err=%v", len(recs), err)
	}
}

func TestUsageStorePersistsAcrossReload(t *testing.T) {
	root := filepath.Join(t.TempDir(), "data")
	clients, _ := NewClientStore(root)
	clients.Create("acme", "A", "")
	u, _ := NewUsageStore(root, clients)
	u.SampleSize("acme", gb)
	u.Record("acme", 10, 20)
	u.Flush()

	u2, _ := NewUsageStore(root, clients)
	bill, err := u2.Billing("acme")
	if err != nil || bill.TotalCost != 0.05 {
		t.Fatalf("billing after reload: cost=%v err=%v", bill, err)
	}
}