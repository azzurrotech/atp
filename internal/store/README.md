# store — persisted atp data model (clients, secrets, usage, billing)

## Overview

`store` is the persistence layer of the `atp` orchestrator, at import path
`azzurrotech/atp/internal/store`. Go's `internal/` rule keeps it inside the atp
module; its only in-tree consumer is `azzurrotech/atp/web`.

`atp` is embedded by the `stenella` platform (module `azzurrotech/stenella`),
which mounts atp as middleware, while atp itself embeds `pod`, `shepherd` and
`song`. This package stores everything atp adds on top: the client registry,
per-client secrets, request logs, hourly usage rollups and billing. Everything
is JSON on the local filesystem under the atp root — no database, standard
library only.

On-disk layout (under the configured root, default `./data`):

- `<root>/atp/clients.json` — platform `Settings` and the `Client` list.
- `<root>/atp/secrets/<client>.json` — AES-256-GCM-sealed secret values.
- `<root>/atp/usage/<client>.json` — hourly usage rollups used for billing.
- `<root>/atp/logs/<client>/<YYYY-MM-DD>.log` — one JSON `RequestRecord` per line.
- `<root>/song/<client>/...` and `<root>/pod/<client>/...` — the client's song
  silo and pod namespace (read by `SiloSize`, never written by this package).

## Public API

### Constants and client identity

```go
const (
	DefaultPricePerGBHour    = 0.05       // USD per GB per hour
	DefaultRetentionHours    = 24         // how long request logs are kept
	DefaultMaxRetentionHours = 24 * 365   // cap for retention_hours
)

// ValidateClientID reports whether id may become a client id.
func ValidateClientID(id string) error
```

A valid client id matches `^[A-Za-z0-9](?:[A-Za-z0-9._-]*[A-Za-z0-9])?$` and is
not a reserved id (`api`, `admin`, `health`, `song`, `.song`, `clients`, `login`,
`static`, `assets`, `c`).

### Registry types

```go
type Client struct {
	ID             string  `json:"id"`
	Name           string  `json:"name"`
	Created        string  `json:"created"`
	Disabled       bool    `json:"disabled,omitempty"`
	Chargeable     bool    `json:"chargeable"`
	PricePerGBHour float64 `json:"price_per_gb_hour"`
	RetentionHours int     `json:"retention_hours"`
	Notes          string  `json:"notes,omitempty"`
}

type Settings struct {
	DefaultPricePerGBHour    float64 `json:"default_price_per_gb_hour"`
	DefaultRetentionHours    int     `json:"default_retention_hours"`
	MaxUploadBytes           int64   `json:"max_upload_bytes"`
	RequestLogLimitPerClient int     `json:"request_log_limit_per_client"`
}

// DefaultSettings returns the platform defaults.
func DefaultSettings() Settings
```

`Create` applies the platform defaults for price and retention, defaults the
display name to the id, forces `Chargeable: true`, stamps `Created` with
`time.Now().UTC().Format(time.RFC3339)` and fails on an invalid or duplicate id.
`Update` is partial: `Name`, `PricePerGBHour`, `RetentionHours` and `Notes` are
only overwritten when non-zero; `Disabled` and `Chargeable` always come from the
patch; a `RetentionHours` above `DefaultMaxRetentionHours` is rejected.

### ClientStore

```go
type ClientStore struct { /* unexported fields */ }

func NewClientStore(root string) (*ClientStore, error)
func (cs *ClientStore) Root() string
func (cs *ClientStore) Settings() Settings
func (cs *ClientStore) SetSettings(s Settings) error
func (cs *ClientStore) Create(id, name, notes string) (*Client, error)
func (cs *ClientStore) Get(id string) (*Client, error)
func (cs *ClientStore) List() []Client
func (cs *ClientStore) Update(id string, patch Client) (*Client, error)
func (cs *ClientStore) SetChargeable(id string, chargeable bool) error
func (cs *ClientStore) Delete(id string) error
func (cs *ClientStore) Exists(id string) bool
```

### Path helpers

```go
// Join is a small path helper for callers that need to build atp-managed
// subdirectories inside the root.
func Join(root string, elems ...string) string

// SanitizeSegment ensures a path segment is safe for use in file paths.
func SanitizeSegment(s string) string
```

`SanitizeSegment` trims whitespace, maps the empty string to `"unnamed"` and
replaces `/`, `\`, `..` and NUL with `_`.

### Secrets vault

```go
type Secret struct {
	Name    string `json:"name"`
	Value   string `json:"value,omitempty"` // decrypted form; never persisted
	Created string `json:"created"`
	Updated string `json:"updated"`
	Note    string `json:"note,omitempty"`
}

type SecretsVault struct { /* unexported fields */ }

func NewSecretsVault(root, masterSecret string) (*SecretsVault, error)
func (v *SecretsVault) Set(client, name, value, note string) (*Secret, error)
func (v *SecretsVault) Get(client, name string) (*Secret, error)
func (v *SecretsVault) List(client string) ([]Secret, error)
func (v *SecretsVault) Has(client, name string) bool
func (v *SecretsVault) Delete(client, name string) error
func (v *SecretsVault) Rotate(newMasterSecret string) error
```

`NewSecretsVault` and `Rotate` require a master secret of at least 32 bytes.
The AES-256-GCM key is `SHA-256(masterSecret)`. `Set` rejects an empty name or
value; `Get` returns the decrypted value; `List` never returns values. `Rotate`
re-encrypts every secret of every client under a new master secret.

### Usage and billing

```go
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

type HourUsage struct {
	Hour        string `json:"hour"` // 2006-01-02T15 (UTC)
	ReqBytes    int64  `json:"req_bytes"`
	RespBytes   int64  `json:"resp_bytes"`
	Requests    int64  `json:"requests"`
	SiloBytes   int64  `json:"silo_bytes,omitempty"`
	Sampled     bool   `json:"sampled,omitempty"`
	SampleCount int    `json:"sample_count,omitempty"`
}

type Cost struct {
	Client         string     `json:"client"`
	Chargeable     bool       `json:"chargeable"`
	PricePerGBHour float64    `json:"price_per_gb_hour"`
	Hours          int        `json:"hours"`
	SampledHours   int        `json:"sampled_hours"`
	AvgSiloBytes   int64      `json:"avg_silo_bytes"`
	AvgSiloGB      float64    `json:"avg_silo_gb"`
	CurrentSiloGB  float64    `json:"current_silo_gb"`
	TotalRequests  int64      `json:"total_requests"`
	TotalReqBytes  int64      `json:"total_req_bytes"`
	TotalRespBytes int64      `json:"total_resp_bytes"`
	TotalCost      float64    `json:"total_cost"`
	WindowStart    string     `json:"window_start"`
	WindowEnd      string     `json:"window_end"`
	Hourly         []HourCost `json:"hourly,omitempty"`
}

type HourCost struct {
	Hour      string  `json:"hour"`
	SiloBytes int64   `json:"silo_bytes"`
	SiloGB    float64 `json:"silo_gb"`
	Requests  int64   `json:"requests"`
	ReqBytes  int64   `json:"req_bytes"`
	Cost      float64 `json:"cost"`
}

type UsageStore struct { /* unexported fields */ }

func NewUsageStore(root string, clients *ClientStore) (*UsageStore, error)
func (u *UsageStore) Record(client string, reqBytes, respBytes int64)
func (u *UsageStore) SampleSize(client string, siloBytes int64)
func (u *UsageStore) Flush()
func (u *UsageStore) Prune(client string, retentionHours int)
func (u *UsageStore) Hourly(client string) []HourUsage
func (u *UsageStore) Billing(client string) (*Cost, error)
func (u *UsageStore) SampleSiloSizes(sizer func(client string) int64)
```

`Record` adds one request to the client's current hourly bucket;
`SampleSize` records (and running-averages) a silo-size sample in the same
bucket; `Flush` persists every live bucket; `Hourly` returns the retained
buckets newest-first; `Billing` computes the cost over the retained window; and
`SampleSiloSizes` samples every known client via the supplied sizer.

### Request logs and size measurement

```go
type LogStore struct { /* unexported fields */ }

func NewLogStore(root string, clients *ClientStore) (*LogStore, error)
func (l *LogStore) Write(rec RequestRecord)
func (l *LogStore) Recent(client string, n int) ([]RequestRecord, error)
func (l *LogStore) Prune(client string, retentionHours int)
func (l *LogStore) TotalDBBytes(client string) int64

// SiloSize walks a client's song silo directory (skipping hidden meta) plus
// its pod namespace and returns the combined on-disk bytes.
func SiloSize(storeRoot, client string) (songBytes, podBytes int64, err error)
```

## Usage

```go
clients, _ := store.NewClientStore("./data")
c, _ := clients.Create("acme", "Acme Inc", "seeded by hand")
vault, _ := store.NewSecretsVault("./data", "at-least-32-bytes-of-master-secret!!")
vault.Set(c.ID, "stripe_api_key", "sk_live_…", "billing")
usage, _ := store.NewUsageStore("./data", clients)
usage.Record(c.ID, 120, 4096); usage.SampleSize(c.ID, 1024*1024*1024); usage.Flush()
bill, _ := usage.Billing(c.ID)
log.Printf("%s: $%.4f over %d hour(s)", c.ID, bill.TotalCost, bill.Hours)
```

## Configuration, defaults and limits

- Root defaults to `./data`; `NewClientStore` creates `<root>/atp/` on demand.
- Platform defaults: price `0.05`, retention `24`, `MaxUploadBytes` `64 << 20`,
  `RequestLogLimitPerClient` `2000`. `SetSettings` re-defaults price, retention
  and max upload when `<= 0` (but not the request-log limit).
- `Client.RetentionHours` is capped at `DefaultMaxRetentionHours` (8760); a
  retention `<= 0` falls back to `DefaultRetentionHours`.
- The billing window is exactly the retained history (`UsageStore.Prune`), and
  `LogStore.Prune` deletes daily log files older than the retention window.
- `gb` is `1024 * 1024 * 1024` (binary gibibyte, unexported); JSON is written
  atomically via a temp file plus `os.Rename` (`atomicWrite`).
- `LogStore.Recent` defaults `n` to `200`, scans newest days first, stops at `n`
  records and caps scanner lines at 1 MiB; `LogStore.Write` stamps the day from
  `rec.Time` (RFC3339Nano) or `time.Now().UTC()`.

## Testing

From the `atp` module root:

```sh
cd /home/matthew/Projects/Platform/stenella/atp
go test ./internal/store/
```

`clients_test.go` covers create/defaults, invalid and reserved ids, duplicates,
partial updates preserving flags, persistence across a reload, the id regex and
the retention cap. `secrets_test.go` covers set/get/list/delete, encryption at
rest, rotation, the 32-byte master requirement and `SiloSize` (hidden `.song`
meta excluded). `usage_test.go` covers the default rate, averaging across hours,
non-chargeable clients costing zero, request counting and log `Recent`/`Prune`.

## Design notes and invariants

- **Flat JSON, no database.** Each store is a small JSON document rewritten
  atomically; versioned shapes carry a `version` field (`secretsFile`,
  `usageFile`). No migrations.
- **Mutex-guarded maps and defensive copies.** Each store holds an in-memory map
  behind a mutex; `Get`/`Create`/`Update` return copies so callers cannot mutate
  internal state.
- **One client, one namespace.** A client id is simultaneously its song silo
  name, its pod table prefix (`<id>/…`) and its shepherd scope (`client:<id>`).
- **Billing is sampled.** Only hours with a `Sampled` size sample are billed;
  cost is `silo_bytes/1GiB * price_per_gb_hour` summed over the retained window.
  Non-chargeable or disabled clients cost `$0`.
- **Best-effort durability with a 48-hour floor.** Persistence errors are
  intentionally swallowed so a disk problem cannot break request handling, and
  `flushLocked` keeps recent history even before an explicit `Prune`; live usage
  can still be lost on a crash.
