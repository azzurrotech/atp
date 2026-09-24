package store

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// gb is the number of bytes in one gibibyte (binary GB, the standard for
// hosting billing).
const gb int64 = 1024 * 1024 * 1024

// RequestRecord is one logged HTTP request, counted against a client's usage.
type RequestRecord struct {
	Time       string `json:"time"`
	Client     string `json:"client"`
	Method     string `json:"method"`
	Path       string `json:"path"`
	Status     int    `json:"status"`
	ReqBytes   int64  `json:"req_bytes"`
	RespBytes  int64  `json:"resp_bytes"`
	DurationMS int64  `json:"duration_ms"`
	RemoteIP   string `json:"remote_ip,omitempty"`
	UserAgent  string `json:"ua,omitempty"`
}

// HourUsage is the rolled-up state of one client for one wall-clock hour.
type HourUsage struct {
	Hour        string `json:"hour"` // 2006-01-02T15 (UTC)
	ReqBytes    int64  `json:"req_bytes"`
	RespBytes   int64  `json:"resp_bytes"`
	Requests    int64  `json:"requests"`
	SiloBytes   int64  `json:"silo_bytes,omitempty"`   // last sampled silo size
	Sampled     bool   `json:"sampled,omitempty"`      // whether a size sample exists
	SampleCount int    `json:"sample_count,omitempty"` // number of size samples taken
}

// Cost summaries the billing of a client over the retained usage window.
type Cost struct {
	Client          string    `json:"client"`
	Chargeable      bool      `json:"chargeable"`
	PricePerGBHour  float64   `json:"price_per_gb_hour"`
	Hours           int       `json:"hours"`
	SampledHours    int       `json:"sampled_hours"`
	AvgSiloBytes    int64     `json:"avg_silo_bytes"`
	AvgSiloGB       float64   `json:"avg_silo_gb"`
	CurrentSiloGB   float64   `json:"current_silo_gb"`
	TotalRequests   int64     `json:"total_requests"`
	TotalReqBytes   int64     `json:"total_req_bytes"`
	TotalRespBytes  int64     `json:"total_resp_bytes"`
	TotalCost       float64   `json:"total_cost"`
	WindowStart     string    `json:"window_start"`
	WindowEnd       string    `json:"window_end"`
	Hourly          []HourCost `json:"hourly,omitempty"`
}

// HourCost is one billable hour.
type HourCost struct {
	Hour      string  `json:"hour"`
	SiloBytes int64   `json:"silo_bytes"`
	SiloGB    float64 `json:"silo_gb"`
	Requests  int64   `json:"requests"`
	ReqBytes  int64   `json:"req_bytes"`
	Cost      float64 `json:"cost"`
}

// UsageStore tracks per-client request volume and hourly silo-size samples,
// persists them under <root>/atp/usage/<client>.json and computes billing.
//
// Request logs themselves are written by LogStore; this store only keeps the
// hourly rollups used for billing ("average hourly size in the database").
type UsageStore struct {
	mu      sync.Mutex
	root    string
	clients *ClientStore

	// live holds the current, partially-elapsed hour per client so request
	// volume is counted without touching disk.
	live map[string]*HourUsage
}

// NewUsageStore builds the usage store.
func NewUsageStore(root string, clients *ClientStore) (*UsageStore, error) {
	dir := filepath.Join(root, "atp", "usage")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &UsageStore{root: root, clients: clients, live: map[string]*HourUsage{}}, nil
}

func (u *UsageStore) path(client string) string {
	return filepath.Join(u.root, "atp", "usage", SanitizeSegment(client)+".json")
}

// Record counts one request against a client's current hourly bucket.
func (u *UsageStore) Record(client string, reqBytes, respBytes int64) {
	u.mu.Lock()
	defer u.mu.Unlock()
	h := u.hour()
	b := u.live[client]
	if b == nil {
		b = u.loadLocked(client)[h]
		if b == nil {
			b = &HourUsage{Hour: h}
		}
		u.live[client] = b
	}
	if b.Hour != h {
		// Hour rolled over between calls; persist the finished bucket first.
		u.flushLocked(client)
		b = &HourUsage{Hour: h}
		u.live[client] = b
	}
	b.ReqBytes += reqBytes
	b.RespBytes += respBytes
	b.Requests++
}

// SampleSize records a silo-size sample for a client's current hour. Multiple
// samples in the same hour are averaged into SiloBytes.
func (u *UsageStore) SampleSize(client string, siloBytes int64) {
	u.mu.Lock()
	defer u.mu.Unlock()
	h := u.hour()
	b := u.live[client]
	if b == nil {
		b = u.loadLocked(client)[h]
		if b == nil {
			b = &HourUsage{Hour: h}
		}
		u.live[client] = b
	}
	if b.Hour != h {
		u.flushLocked(client)
		b = &HourUsage{Hour: h}
		u.live[client] = b
	}
	if b.SampleCount == 0 {
		b.SiloBytes = siloBytes
	} else {
		// Running average of the samples seen this hour.
		b.SiloBytes = (b.SiloBytes*int64(b.SampleCount) + siloBytes) / int64(b.SampleCount+1)
	}
	b.SampleCount++
	b.Sampled = true
}

// Flush persists every live bucket (called periodically and on hour change).
func (u *UsageStore) Flush() {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.flushLocked("")
}

func (u *UsageStore) flushLocked(_ string) {
	for client, b := range u.live {
		b := b
		prev := map[string]*HourUsage{}
		for _, h := range u.loadLocked(client) {
			prev[h.Hour] = h
		}
		prev[b.Hour] = b
		now := time.Now().UTC()
		cutoff := now.Add(-48 * time.Hour).Format(hourFormat)
		for k, hb := range prev {
			if k < cutoff {
				delete(prev, k)
			} else {
				_ = hb // keep recent history as-is
			}
			// (nothing else; 48h safety retention keeps billing live)
		}
		if err := u.saveLocked(client, prev); err != nil {
			_ = err // persistence failures must not break request handling
		}
	}
	u.live = map[string]*HourUsage{}
}

// Prune trims usage history according to a client's retention window. The
// billing view is only as deep as the retained window.
func (u *UsageStore) Prune(client string, retentionHours int) {
	if retentionHours <= 0 {
		retentionHours = DefaultRetentionHours
	}
	cutoff := time.Now().UTC().Add(-time.Duration(retentionHours) * time.Hour).Format(hourFormat)
	u.mu.Lock()
	defer u.mu.Unlock()
	hist := u.loadLocked(client)
	for k := range hist {
		if k < cutoff {
			delete(hist, k)
		}
	}
	_ = u.saveLocked(client, hist)
}

// Hourly returns the retained hourly rollups of a client, newest first.
func (u *UsageStore) Hourly(client string) []HourUsage {
	u.mu.Lock()
	defer u.mu.Unlock()
	hist := u.loadLocked(client)
	// The live bucket for the current hour is always a superset of any
	// persisted bucket for the same hour (it was loaded from the file and
	// has been accumulating since), so it wins for display and billing.
	if b := u.live[client]; b != nil && b.Hour == u.hour() {
		hist[b.Hour] = b
	}
	out := make([]HourUsage, 0, len(hist))
	for _, b := range hist {
		out = append(out, *b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Hour > out[j].Hour })
	return out
}

// Billing computes the cost of the retained usage window using the average
// hourly silo size, per the platform pricing model:
//
//	cost(hour) = silo_bytes(hour) / 1GB * price_per_gb_hour
//
// Chargeable clients are billed; disabled or non-chargeable clients cost $0.
func (u *UsageStore) Billing(client string) (*Cost, error) {
	c, err := u.clients.Get(client)
	if err != nil {
		return nil, err
	}
	hourly := u.Hourly(client)
	now := time.Now().UTC()
	cost := &Cost{
		Client:         client,
		Chargeable:     c.Chargeable,
		PricePerGBHour: c.PricePerGBHour,
		Hours:          len(hourly),
		TotalCost:      0,
		WindowEnd:      now.Format(time.RFC3339),
	}
	var sizeSum int64
	var sizeCount int
	for i := len(hourly) - 1; i >= 0; i-- {
		h := hourly[i]
		cost.TotalRequests += h.Requests
		cost.TotalReqBytes += h.ReqBytes
		cost.TotalRespBytes += h.RespBytes
		if cost.WindowStart == "" {
			cost.WindowStart = h.Hour + ":00Z"
		}
		hc := HourCost{
			Hour:      h.Hour,
			SiloBytes: h.SiloBytes,
			SiloGB:    float64(h.SiloBytes) / float64(gb),
			Requests:  h.Requests,
			ReqBytes:  h.ReqBytes,
		}
		if h.Sampled {
			sizeSum += h.SiloBytes
			sizeCount++
			cost.SampledHours++
		}
		if c.Chargeable && h.Sampled {
			hc.Cost = hc.SiloGB * c.PricePerGBHour
			cost.TotalCost += hc.Cost
		}
		cost.Hourly = append([]HourCost{hc}, cost.Hourly...)
	}
	if sizeCount > 0 {
		cost.AvgSiloBytes = sizeSum / int64(sizeCount)
	}
	cost.AvgSiloGB = float64(cost.AvgSiloBytes) / float64(gb)
	if cost.WindowStart == "" {
		cost.WindowStart = now.Format(time.RFC3339)
	}
	return cost, nil
}

// SampleSiloSizes walks every client's silo and records a size sample for the
// current hour. Called just after each hour boundary (and at startup).
func (u *UsageStore) SampleSiloSizes(sizer func(client string) int64) {
	clients := u.clients.List()
	for _, c := range clients {
		u.SampleSize(c.ID, sizer(c.ID))
	}
}

// --- internal -----------------------------------------------------------------

const hourFormat = "2006-01-02T15"

func (u *UsageStore) hour() string { return time.Now().UTC().Format(hourFormat) }

type usageFile struct {
	Version int                  `json:"version"`
	Hours   map[string]*HourUsage `json:"hours"`
}

func (u *UsageStore) loadLocked(client string) map[string]*HourUsage {
	f := &usageFile{Version: 1, Hours: map[string]*HourUsage{}}
	data, err := os.ReadFile(u.path(client))
	if err == nil {
		_ = json.Unmarshal(data, f)
	}
	if f.Hours == nil {
		f.Hours = map[string]*HourUsage{}
	}
	return f.Hours
}

func (u *UsageStore) saveLocked(client string, hours map[string]*HourUsage) error {
	f := &usageFile{Version: 1, Hours: hours}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(u.path(client), data)
}

// LogStore appends one line per request to per-client daily files under
// <root>/atp/logs/<client>/<YYYY-MM-DD>.log and prunes files older than the
// client's retention window (24h by default, configurable per client).
type LogStore struct {
	mu      sync.Mutex
	root    string
	clients *ClientStore
}

// NewLogStore builds the request-log store.
func NewLogStore(root string, clients *ClientStore) (*LogStore, error) {
	if err := os.MkdirAll(filepath.Join(root, "atp", "logs"), 0o755); err != nil {
		return nil, err
	}
	return &LogStore{root: root, clients: clients}, nil
}

func (l *LogStore) dir(client string) string {
	return filepath.Join(l.root, "atp", "logs", SanitizeSegment(client))
}

// Write appends one request record to the client's daily log.
func (l *LogStore) Write(rec RequestRecord) {
	l.mu.Lock()
	defer l.mu.Unlock()
	day, err := time.Parse(time.RFC3339Nano, rec.Time)
	if err != nil {
		day = time.Now().UTC()
	}
	dir := l.dir(rec.Client)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	path := filepath.Join(dir, day.UTC().Format("2006-01-02")+".log")
	line, err := json.Marshal(rec)
	if err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	bw := bufio.NewWriter(f)
	bw.Write(line)
	bw.WriteByte('\n')
	bw.Flush()
	f.Close()
}

// Recent returns the newest n log entries of a client across retained days.
func (l *LogStore) Recent(client string, n int) ([]RequestRecord, error) {
	if n <= 0 {
		n = 200
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	dir := l.dir(client)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var files []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".log") {
			files = append(files, filepath.Join(dir, e.Name()))
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(files))) // newest day first
	// Cap how many days we scan so a client with a long retention stays fast.
	var out []RequestRecord
	for _, fp := range files {
		f, err := os.Open(fp)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		var lines []RequestRecord
		for sc.Scan() {
			var rec RequestRecord
			if err := json.Unmarshal(sc.Bytes(), &rec); err == nil {
				lines = append(lines, rec)
			}
		}
		f.Close()
		// Newest day's entries first, then by time descending.
		sort.Slice(lines, func(i, j int) bool { return lines[i].Time > lines[j].Time })
		out = append(out, lines...)
		if len(out) >= n {
			break
		}
	}
	if len(out) > n {
		out = out[:n]
	}
	return out, nil
}

// Prune deletes log files older than the client's retention window. Called
// hourly by the background sweeper. retentionHours <= 0 keeps the default.
func (l *LogStore) Prune(client string, retentionHours int) {
	if retentionHours <= 0 {
		retentionHours = DefaultRetentionHours
	}
	cutoff := time.Now().UTC().Add(-time.Duration(retentionHours) * time.Hour)
	l.mu.Lock()
	defer l.mu.Unlock()
	dir := l.dir(client)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".log") {
			continue
		}
		day, err := time.Parse("2006-01-02", strings.TrimSuffix(e.Name(), ".log"))
		if err != nil {
			// Unparseable file name: delete defensively.
			os.Remove(filepath.Join(dir, e.Name()))
			continue
		}
		if day.UTC().Add(24 * time.Hour).Before(cutoff) {
			os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

// TotalDBBytes returns the on-disk size of the log directory of a client
// (useful for the admin overview).
func (l *LogStore) TotalDBBytes(client string) int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	dir := l.dir(client)
	var total int64
	filepath.WalkDir(dir, func(_ string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, err := d.Info(); err == nil {
			total += info.Size()
		}
		return nil
	})
	return total
}

// SiloSize walks a client's song silo directory (skipping hidden meta) plus
// its pod namespace and returns the combined on-disk bytes. This is the size
// that is billed against.
func SiloSize(storeRoot, client string) (songBytes, podBytes int64, err error) {
	songDir := filepath.Join(storeRoot, "song", SanitizeSegment(client))
	podDir := filepath.Join(storeRoot, "pod", SanitizeSegment(client))
	walk := func(dir string, skipHidden bool) int64 {
		var total int64
		filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			name := d.Name()
			if p != dir && (name == ".song" || (skipHidden && strings.HasPrefix(name, "."))) {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if d.IsDir() {
				return nil
			}
			if info, err := d.Info(); err == nil {
				total += info.Size()
			}
			return nil
		})
		return total
	}
	songBytes = walk(songDir, true)
	podBytes = walk(podDir, false)
	return songBytes, podBytes, nil
}