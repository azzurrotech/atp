# web — the atp orchestrator over HTTP

## Overview

`web` is the HTTP surface and orchestrator of `atp`. It lives in the
`azzurrotech/atp` module at the import path `azzurrotech/atp/web`.

`atp` embeds three modules in-process and exposes every one of their endpoints
through a single API plus a management UI, adding what they lack: client
management, an encrypted secrets vault, per-client usage logging with
configurable retention, hourly silo-size sampling and billing against the
average hourly size in GB.

- `song` (`azzurrotech/song/pkg/song`) — siloed static hosting, SSR templates
  and magic-link auth, mounted at `/api/song`.
- `pod` (`azzurrotech/pod`) — filesystem XML database, mounted at `/api/pod`.
- `shepherd` (`azzurrotech/shepherd/{middleware,firewall,token}`) —
  identity-less IAM, firewall, rate limiter and reverse-proxy gateway, mounted
  at `/api/shepherd` and `/gw`.

`atp` is itself embedded by the `stenella` platform (module
`azzurrotech/stenella`), which mounts atp as middleware. There are no
third-party dependencies: the package imports the Go standard library, the
in-repo sibling modules above, and `azzurrotech/atp/internal/{auth,store}`.

atp runs in two modes:

- **Server mode** — `(*ATPService).Start()` runs a standalone `http.Server` on
  its own port.
- **Middleware mode** — `(*ATPService).Middleware(next)` serves the paths atp
  owns and delegates everything else to `next`. atp does not claim bare `/` in
  middleware mode, so the host keeps its own root.

The management UI is plain HTML plus vanilla ES6 JavaScript embedded from
`web/templates/*.html`; it talks to the same JSON API.

## Public API

### Constants

```go
// Version of the atp orchestrator.
const Version = "2.0.0"

// DefaultPrice is the default billing rate per GB per hour (USD).
const DefaultPrice = store.DefaultPricePerGBHour
```

### Options and construction

```go
type Options struct {
	Port                 string  // server-mode port (default "8080")
	Root                 string  // data root (default "./data")
	Secret               string  // master secret, >= 32 bytes
	AdminUser            string  // admin username (default "admin")
	AdminPassword        string  // admin password (default "admin")
	DisableUI            bool    // hide the management pages (API stays up)
	Seed                 bool    // provision a demo client
	DefaultRatePerMinute float64 // embedded rate limiter (default 120)
	Burst                float64 // rate limiter burst (default 30)
	PublicBase           string  // public base URL for silos (optional)
}

type ATPService struct { /* unexported fields */ }

func NewATPService(opts Options) (*ATPService, error)
```

### Entry points

```go
// Handler returns the full atp HTTP handler (server mode).
func (s *ATPService) Handler() http.Handler

// Middleware serves the paths atp owns and delegates everything else to next.
func (s *ATPService) Middleware(next http.Handler) http.Handler

// Start runs atp as a standalone webserver (server mode).
func (s *ATPService) Start() error
```

### Accessors

```go
func (s *ATPService) Clients() *store.ClientStore
func (s *ATPService) Vault() *store.SecretsVault
func (s *ATPService) Usage() *store.UsageStore
func (s *ATPService) Logs() *store.LogStore
func (s *ATPService) Auth() *auth.Manager
func (s *ATPService) Song() *song.Song
func (s *ATPService) Pod() *pod.Handler
func (s *ATPService) PodStore() *pod.Store
func (s *ATPService) Shepherd() *middleware.Shepherd
```

## Route table

Auth legend:

- **public** — no admin credential required.
- **admin** — a valid atp session, presented as the `atp_session` cookie or an
  `X-ATP-Token` header (`adminToken`). Browsers that miss the gate on a page are
  redirected to `/login`; API calls get `401`.

### Public surface

| Method | Path | Auth | Notes |
| --- | --- | --- | --- |
| GET | `/health` | public | liveness JSON (`handleHealth`) |
| GET | `/favicon.ico` | public | always `204` |
| GET | `/login` | public | login page; redirects to `/` when already authed |
| POST | `/login` | public | JSON or form; sets the session cookie |
| POST | `/logout` | public | clears the cookie and redirects to `/login` |
| GET | `/c/{client}/{rest...}` | public | the client's hosted song site; `404` if unknown/disabled |
| GET, POST | `/api/auth/{rest...}` | public | song's own silo-end-user magic-link auth |
| GET | `/api/shepherd/token/verify` | public | validates a presented capability token |
| GET | `/api/shepherd/verify` | public | redeems a magic link and sets the capability cookie |

### Management pages (HTML)

| Method | Path | Auth |
| --- | --- | --- |
| GET | `/` | admin |
| GET | `/clients` | admin |
| GET | `/clients/{id}` | admin |
| GET | `/clients/{id}/files` | admin |
| GET | `/clients/{id}/tables` | admin |
| GET | `/clients/{id}/security` | admin |
| GET | `/clients/{id}/usage` | admin |
| GET | `/clients/{id}/secrets` | admin |
| GET | `/sessions` | admin |

### atp JSON API

| Method | Path | Auth | Purpose |
| --- | --- | --- | --- |
| GET | `/api/summary` | admin | dashboard totals and per-client summaries |
| GET | `/api/services` | admin | embedded service versions and mounts |
| GET | `/api/config` | admin | platform settings, admin user, port |
| PUT | `/api/config` | admin | update platform settings |
| GET | `/api/clients` | admin | list clients |
| POST | `/api/clients` | admin | create client + provision its song silo |
| GET | `/api/clients/{id}` | admin | client detail (size, billing, tables) |
| PUT | `/api/clients/{id}` | admin | partial update |
| DELETE | `/api/clients/{id}` | admin | delete registry entry + silo dir |
| GET | `/api/clients/{id}/secrets` | admin | list secret metadata |
| POST | `/api/clients/{id}/secrets` | admin | store/encrypt a secret |
| GET | `/api/clients/{id}/secrets/{name}` | admin | decrypt one secret |
| DELETE | `/api/clients/{id}/secrets/{name}` | admin | delete a secret |
| GET | `/api/clients/{id}/tables` | admin | the client's pod tables |
| GET | `/api/clients/{id}/usage` | admin | recent request-log records (`limit`, default 200, max 5000) |
| GET | `/api/clients/{id}/usage/hourly` | admin | hourly usage rollups |
| GET | `/api/clients/{id}/billing` | admin | computed `store.Cost` |
| POST | `/api/clients/{id}/keys` | admin | issue a `client:<id>`-scoped capability token |
| POST | `/api/clients/{id}/keys/block` | admin | issue a block of scoped keys (count ≤ 10000) |
| POST | `/api/clients/{id}/keys/magic` | admin | create a scoped magic link |
| POST | `/api/clients/{id}/revoke` | admin | revoke a token or block |
| GET | `/api/clients/{id}/jskey` | admin | JS (WebCrypto) crypto key for a scope |
| GET | `/api/clients/{id}/verify` | admin | verify a token is scoped to this client |
| GET | `/api/sessions` | admin | list sessions (marks the current one) |
| POST | `/api/sessions/revoke` | admin | revoke one other session |
| POST | `/api/sessions/revoke-all` | admin | revoke all other sessions and clear the cookie |

### Embedded-service pass-through (admin)

| Method | Path | Auth | Purpose |
| --- | --- | --- | --- |
| GET, POST, PUT, DELETE | `/api/song/{rest...}` | admin | every song endpoint |
| GET, POST, PUT, DELETE | `/api/pod/{rest...}` | admin | every pod endpoint |
| GET | `/api/shepherd/keys/javascript` | admin | JS crypto key (global) |
| POST | `/api/shepherd/keys` | admin | issue an unscoped token |
| POST | `/api/shepherd/keys/block` | admin | issue an unscoped block |
| POST | `/api/shepherd/keys/magic` | admin | create a global magic link |
| POST | `/api/shepherd/revoke` | admin | revoke a token |
| POST | `/api/shepherd/revoke/block` | admin | revoke a block |
| GET, POST | `/api/shepherd/firewall/rules` | admin | list/create firewall rules |
| DELETE | `/api/shepherd/firewall/rules/{id}` | admin | delete a rule |
| GET | `/api/shepherd/ratelimit/status` | admin | rate-limit status for a key |
| POST | `/api/shepherd/ratelimit/reset` | admin | reset the rate limiter |
| GET, POST | `/api/shepherd/upstreams` | admin | list/register gateway upstreams |
| DELETE | `/api/shepherd/upstreams/{prefix}` | admin | delete an upstream |
| GET, POST, PUT, DELETE | `/gw/{rest...}` | admin | shepherd reverse-proxy gateway |

### Client-scoped proxies (admin)

| Method | Path | Auth | Purpose |
| --- | --- | --- | --- |
| GET, POST, PUT, DELETE | `/c/{client}/api/song/{rest...}` | admin | song, scoped to `client` |
| GET, POST, PUT, DELETE | `/c/{client}/api/pod/{rest...}` | admin | pod, scoped to `client` |

## Usage

Server mode:

```go
package main

import (
	"log"

	"azzurrotech/atp/web"
)

func main() {
	svc, err := web.NewATPService(web.Options{
		Port: "8080", Root: "./data", AdminUser: "admin", AdminPassword: "change-me",
		Secret: "at-least-32-bytes-of-master-secret!!",
	})
	if err != nil {
		log.Fatal(err)
	}
	log.Fatal(svc.Start())
}
```

Middleware mode (as `stenella` embeds it):

```go
svc, err := web.NewATPService(web.Options{Root: "./data", Secret: "…"})
if err != nil {
	log.Fatal(err)
}
http.Handle("/", svc.Middleware(hostHandler))
```

## Configuration, inputs, defaults and limits

`NewATPService` defaults `Port = "8080"`, `Root = "./data"`, `AdminUser =
"admin"`, `AdminPassword = "admin"`, `DefaultRatePerMinute = 120` and
`Burst = 30`. `Secret` falls back to a built-in default only when empty, but a
secret shorter than 32 bytes is rejected. The standalone binary (`atp/main.go`)
requires `-secret`/`ATP_SECRET` and exposes `-port`, `-root`, `-admin-user`,
`-admin-password`, `-price`, `-retention`, `-rate`, `-burst`, `-no-ui`,
`-seed`, `-version`, `-help`.

Other limits: `readJSON` caps bodies at `4 << 20` bytes (4 MiB) and rejects
unknown fields; block issuance caps `count` at `10000`; the usage API accepts
`limit` up to `5000` (default `200`); `Start` sets `ReadHeaderTimeout 5s`,
`ReadTimeout 30s`, `WriteTimeout 60s`, `IdleTimeout 120s`; the background loop
flushes usage every 30s, prunes hourly and samples silo sizes when the UTC hour
flips.

## Testing

From the `atp` module root:

```sh
cd /home/matthew/Projects/Platform/stenella/atp
go test ./web/
# or the whole module:
go test ./...
```

`atp_web_test.go` covers middleware path ownership, health, login/admin gating,
client lifecycle and silo provisioning, the public client web app with disabled
clients returning `404`, scoped pod access and cross-namespace `403`, the
secrets vault end-to-end (no plaintext on disk, hidden values in list), shepherd
key scoping/verify/revoke/block/magic, usage logging and billing, firewall and
upstream management, middleware mode, the demo seed (magic-link redemption
included), seed idempotence and short-secret rejection. `ui_pages_test.go`
renders every admin page and confirms `-no-ui` disables the UI while `/health`
still works.

## Design notes and invariants

- **No feature is re-implemented.** Song, pod and shepherd are embedded
  in-process; atp handlers are thin bindings that add scoping, logging and
  billing, and `/api/{song,pod,shepherd}/*` expose their full surfaces.
- **Scoping is enforced at the proxy.** `/c/{client}/api/song/...` refuses
  another client's silo with `403`; `/c/{client}/api/pod/...` refuses any
  namespace other than `<client>/...` and `403`s pod's admin endpoints (`/sql`,
  `/normalize`, `/reindex`, `/health`). Issued tokens always carry
  `client:<id>` (`clientScopes`).
- **Disabled clients are invisible.** `activeClient` returns `404` for a
  disabled client's hosted site and scoped APIs, though the files remain.
- **Attribution happens once.** `usageMiddleware` is outermost; `clientForPath`
  maps `/c/{client}/...`, `/api/song/silos/{client}/...`,
  `/api/pod/table|schema|record/{client}/...` and `/api/clients/{client}/...`
  to a client and records usage plus one log line. System traffic is not logged
  per client.
- **Request logging is metadata only.** A `store.RequestRecord` holds time,
  client, method, path, status, bytes, duration, IP and a 200-byte user agent —
  never bodies or headers.
- **UI templates are embedded and rendered per page.** `ui.go` parses
  `base.html` plus exactly one page file per set so the `content` block is
  unambiguous, and `html/template` auto-escapes server-rendered values.
