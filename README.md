# atp — AzzurroTech Platform

**MIT License © Azzurro Technology Inc.** · module `azzurrotech/atp` · **standard library only**

atp is the orchestrator of the AzzurroTech stack. It embeds **song**, **pod** and
**shepherd** in a single process and exposes every one of their endpoints through
one API surface and management UI, adding what the three services do not have on
their own:

- **client management** (persisted registry, per-client enable/disable),
- an **encrypted secrets vault** per client (AES-256-GCM at rest),
- **per-client usage logging** with per-client retention (default 24 h),
- **billing** against the average hourly silo size in GiB at a per-client price
  (default USD 0.05 / GiB / hour),
- identity-less, per-client **security** through shepherd (keys, blocks, magic
  links, firewall, rate limiting, gateway).

Everything is written with the Go standard library only — no external modules, no
databases, no frameworks. The UI is plain HTML + vanilla ES6 JavaScript and the
web platform (`fetch`, `localStorage`, standard cookies).

---

## How it fits together

| Service | Role | atp mount |
|---|---|---|
| **song** | siloed static-file hosting, server-side template routes, magic-link auth | `/api/song`, `/api/auth`, client web apps at `/c/{client}/` |
| **pod** | filesystem XML database (HTML-form driven), XML/JSON responses | `/api/pod`, client-scoped tables at `/c/{client}/api/pod/...` |
| **shepherd** | zero-knowledge key/token issuance, blocks, magic links, firewall, rate limiter, upstream gateway | `/api/shepherd`, `/gw/` |
| **atp** | clients, secrets, usage/logs, billing, admin UI | `/` (UI), `/api/...`, `/c/{client}/...` |

song, pod and shepherd are submodules of atp and are **embedded in-process**, not
proxied. Every feature of the three services is reachable either through atp's
admin prefixes (`/api/song/...`, `/api/pod/...`, `/api/shepherd/...`) or through
per-client scoped prefixes (`/c/{client}/api/song/...`, `/c/{client}/api/pod/...`).

### Data layout (under `--root`, default `./data`)

```
data/
├── atp/
│   ├── clients.json              # client registry + platform settings
│   ├── secrets/<client>.json     # AES-256-GCM sealed secrets (one file/client)
│   └── logs/<client>/<YYYY-MM-DD>.log
├── song/<silo>/...               # song store; one silo per client
└── pod/<client>/<table>/...      # pod database; one namespace per client
```

---

## Running

### Server mode (this repo's `main.go`)

```bash
go build -o atp .
./atp --secret 'at least 32 characters' --admin-password 'change me'
# options are also available as env vars: ATP_PORT, ATP_ROOT, ATP_SECRET, ...
```

Required: `--secret` (≥ 32 bytes). It derives the secrets-vault key and signs
capability tokens. The admin credentials are `--admin-user` / `--admin-password`
(env `ATP_ADMIN_USER` / `ATP_ADMIN_PASSWORD`); the default password is `admin`,
which prints a warning at startup — set a real password.

| Flag | Default | Meaning |
|---|---|---|
| `--port` | `8080` | HTTP listen port (`ATP_PORT`) |
| `--root` | `./data` | data root (`ATP_ROOT`) |
| `--secret` | — | master secret ≥ 32 chars (`ATP_SECRET`) |
| `--admin-user` | `admin` | admin username |
| `--admin-password` | `admin` | admin password (warned) |
| `--price` | `0.05` | default USD per GiB per hour |
| `--retention` | `24` | default request-log retention (hours) |
| `--rate` | `120` | rate limit per minute per client (0 = off) |
| `--burst` | `30` | rate limiter burst |
| `--no-ui` | off | disable the management web UI |
| `--seed` | off | provision a demo client exercising every feature |
| `--version` / `--help` | — | print and exit |

Try it end to end:

```bash
./atp --seed --secret '0123456789abcdef0123456789abcdef' --admin-password changeme
# open http://localhost:8080  (login, then click 🌐 on the demo client)
# public demo app:             http://localhost:8080/c/demo/
# admin API:                   curl -b cookies http://localhost:8080/api/summary
```

### Middleware mode (embed in another Go server)

`web.ATPService.Middleware(next)` serves the paths atp owns (`/api/...`,
`/clients/...`, `/c/...`, `/gw/...`, `/login`, `/health`) and delegates everything
else to `next`, so any standard-library Go server can host atp:

```go
import "azzurrotech/atp/web"

srv, err := web.NewATPService(web.Options{Root: "./data", Secret: "..."})
if err != nil { /* ... */ }
http.Handle("/", srv.Middleware(myHostHandler))
```

See `examples/middleware/main.go`.

---

## Admin interface

Sign in at `/` (admin session cookie, HttpOnly + SameSite=Lax). The UI is plain
HTML + vanilla JS; every screen talks to the JSON API below.

| Page | Path |
|---|---|
| Overview | `/` |
| Clients | `/clients`, `/clients/{id}` |
| Client files (song) | `/clients/{id}/files` |
| Client tables (pod) | `/clients/{id}/tables` |
| Client security (shepherd) | `/clients/{id}/security` |
| Client usage & billing | `/clients/{id}/usage` |
| Client secrets | `/clients/{id}/secrets` |

---

## HTTP API

All `/api/...` admin and `/c/{client}/api/...` scoped endpoints require the admin
session cookie. Public surfaces: `/health`, `/login`, `/logout`, `/c/{client}/...`,
`/api/auth/...`, `/api/shepherd/token/verify`, `/api/shepherd/verify`.

### atp core

| Endpoint | Description |
|---|---|
| `GET /health` | liveness + embedded service summary (public) |
| `POST /login` `POST /logout` | admin session (public) |
| `GET /api/summary` | dashboard numbers: clients, silo bytes, services |
| `GET /api/services` | embedded service mounts and status |
| `GET/PUT /api/config` | platform settings (default price, retention, upload cap) |

### Clients

| Endpoint | Description |
|---|---|
| `GET /api/clients` · `POST /api/clients` | list / create (id, name, notes) |
| `GET /api/clients/{id}` · `PUT /api/clients/{id}` · `DELETE /api/clients/{id}` | read / partial update (`name`, `notes`, `chargeable`, `disabled`, `price_per_gb_hour`, `retention_hours`) / delete |
| `GET /api/clients/{id}/tables` | the client's pod tables with row counts |
| `GET /api/clients/{id}/usage?limit=` · `/usage/hourly` | recent request log / hourly rollups |
| `GET /api/clients/{id}/billing` | billing summary (see below) |
| `GET /api/clients/{id}/jskey` | client-scoped JS crypto key (for song/pod front ends) |
| `GET /api/clients/{id}/verify?t=` | check a capability token against the client's scope |

Creating a client provisions its song silo and pod namespace; deleting a client
removes them. Client ids must match song silo rules (`[A-Za-z0-9][A-Za-z0-9._-]*`)
and cannot collide with atp/song reserved names (`api`, `login`, `c`, …).

### Secrets vault

| Endpoint | Description |
|---|---|
| `GET /api/clients/{id}/secrets` | list names + notes (values never returned) |
| `POST /api/clients/{id}/secrets` | set/rotate a secret (`name`, `value`, `note`) |
| `GET /api/clients/{id}/secrets/{name}` | read the decrypted value (admin only) |
| `DELETE /api/clients/{id}/secrets/{name}` | delete |

Values are encrypted at rest with AES-256-GCM (key derived from the master
secret); the on-disk file never contains plaintext.

### security (shepherd, hosted through atp)

Global admin, plus per-client scoped variants under `/api/clients/{id}/...`:

| Endpoint | Description |
|---|---|
| `POST /api/shepherd/keys` | issue a capability token (subject, scopes, TTL) |
| `POST /api/shepherd/keys/block` | issue a block of keys (`count`, `block`, `ttl`) |
| `POST /api/shepherd/keys/magic` | create a magic link (`next`) |
| `POST /api/shepherd/revoke` · `/revoke/block` | revoke a token / a whole block |
| `GET /api/shepherd/keys/javascript` | JS crypto key for the front ends |
| `GET /api/shepherd/ratelimit/status` · `POST /api/shepherd/ratelimit/reset` | per-scope rate limit view/reset |
| `GET/POST /api/shepherd/firewall/rules` · `DELETE .../{id}` | path-glob firewall rules |
| `GET/POST /api/shepherd/upstreams` · `DELETE .../{prefix}` | gateway upstreams |
| `GET /api/shepherd/token/verify` | verify any capability token (public, `Authorization: Bearer` or `?t=`) |
| `GET /api/shepherd/verify?t=` | redeem a magic link → sets the `shepherd_cap` cookie (public) |

Per-client key issuance forces the token scope to `client:<id>`, and the gateway
at `/gw/` routes to upstreams (subject to firewall + rate limits).

### song / pod pass-through

`/api/song/{rest...}` and `/api/pod/{rest...}` (GET/POST/PUT/DELETE) mirror the
embedded services' endpoints exactly — file CRUD, silo metadata, table CRUD,
record queries, XML or JSON output. The client-scoped prefixes
`/c/{client}/api/song/...` and `/c/{client}/api/pod/...` do the same but refuse
any path that leaves the client's own silo/namespace (403).

### Public client web apps

`GET /c/{client}/...` serves the client's song silo as a public website
(index resolution, SSR template routes, transparent decryption of encrypted
files) without any auth — that is the tenant's website.

---

## Usage logging & billing

Every HTTP request whose path belongs to a client (its web app, scoped API, or
the admin calls made on its behalf) is logged to
`<root>/atp/logs/<client>/<YYYY-MM-DD>.log`: timestamp, method, path, status,
request/response bytes, duration, remote address, user agent. Logs are pruned to
each client's `retention_hours` (default 24).

A background sampler measures each client's **silo size** (song silo + pod
namespace bytes) every 30 s and rolls it into hourly buckets. Billing is:

```
cost(hour) = (silo_bytes / 1 GiB) × price_per_gb_hour
total      = Σ cost(hour) over the retained window
```

`GET /api/clients/{id}/billing` reports `avg_silo_gb`, `sampled_hours`, hourly
breakdown, request totals and `total_cost`. Non-chargeable clients (`chargeable:
false`) always bill zero; `price_per_gb_hour` defaults to the platform setting
`--price` (0.05 USD/GiB/h).

---

## Development

```bash
go build ./...      # builds atp + examples
go vet ./...        # clean
go test ./...       # unit + integration tests (httptest against the full surface)
go test -race ./...
```

Tests cover the client store, the secrets vault (encryption at rest), usage
rollups and billing math, admin sessions, and an end-to-end HTTP pass: login,
client lifecycle and silo provisioning, public web apps, song/pod scoping
(enforcement included), secrets, shepherd keys/magic links, firewall/upstreams,
usage logging and billing, seed idempotency, and middleware mode.

## Repository

`atp` is a submodule of the `stenella` superproject and itself superprojects
`song`, `pod` and `shepherd` (local `replace` directives in `go.mod` — all four
modules remain standard-library only).

Was formerly a thin service registry (in-memory, hardcoded defaults); that code
was replaced by the embedded-service orchestrator described here.