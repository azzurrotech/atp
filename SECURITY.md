# atp — Security Overview

atp orchestrates **song**, **pod** and **shepherd** in one process and adds client
management, an encrypted secrets vault, per-client usage logging and billing.
This document states what the code actually does today. It is deliberately
conservative: claims here match implementations, not aspirations.

> Deployment note: atp serves plain HTTP. Terminate TLS in front of it (Caddy is
> the reference proxy for this stack — see the workspace AGENTS.md port table).
> Behind TLS, run the admin UI and magic links over `https://` only.

## Authentication

- **Admin sessions** (`internal/auth`): in-memory session store; session tokens
  are 32 random bytes from `crypto/rand`; the cookie carries only the token and
  is `HttpOnly` + `SameSite=Lax`. Passwords are verified with a salted,
  100 000-iteration SHA-256 stretch and constant-time comparison. The default
  password is `admin` — the server prints a warning; set `--admin-password` /
  `ATP_ADMIN_PASSWORD`.
- **Magic links** (from shepherd, hosted by atp): redeeming
  `/api/shepherd/verify?t=...` validates a capability token and sets the
  `shepherd_cap` session cookie (`HttpOnly`; `Secure` when the link itself is
  `https://`), then redirects to the link's `next` target. `next` is restricted
  to the atp-owned origin, `/c/{client}/...`, `/login` or `/`.
- **Capability tokens** (shepherd): signed scope-bearing keys, per-client scope
  `client:<id>` enforced by atp's client-scoped endpoints.

## Authorization

- All `/api/...` admin routes and `/c/{client}/api/...` scoped routes require the
  admin session. Unauthenticated API calls get `401`; unauthenticated page
  navigations redirect to `/login`.
- Per-client scoping is enforced at the boundary: the scoped song/pod proxy
  refuses (`403`) any path that resolves outside the client's own silo / table
  namespace, and per-client key issuance forces the issued token's scope.
- Public by design: `/health`, `/login`, `/logout`, `/c/{client}/...` (the
  tenant's public website), `/api/auth/...`, `/api/shepherd/token/verify` and
  `/api/shepherd/verify`.

## Data protection

- **Secrets vault** (`internal/store/secrets.go`): per-client values encrypted at
  rest with **AES-256-GCM**; the key is derived from the atp master secret
  (`--secret`, ≥ 32 bytes) via SHA-256; the nonce is prepended to the ciphertext
  and the whole blob is base64. The plaintext value exists only in memory during
  `GET`/`POST` handling and is never persisted. Set/rotate/list responses never
  return values.
- **Request logs**: plaintext JSON files under `<root>/atp/logs/` — they contain
  request metadata (method, path, headers like user-agent, remote address) but
  never request bodies or secret values. Pruned per client to `retention_hours`.
- **No database**: all persistence is files managed by atp itself.

## Trust boundary

- atp is single-admin. Client tenants are isolated from *each other* (silo +
  namespace scoping) and from admin surfaces, but the admin session can read and
  operate everything — that is the model.
- The three embedded services are trusted code running in-process; their
  endpoints are not independently network-accessible when hosted through atp.
- Web UI output: files/tables/secrets rendered in the admin pages are escaped via
  the plain-text DOM helpers (`textContent`); file listings are escaped HTML.
  Client web apps are served by song, which stores whatever the client uploaded —
  the tenant is responsible for the content it publishes at `/c/{client}/`.

## Rate limiting & firewall

- Per-scope rate limits (default 120/min, burst 30; `--rate`/`--burst`) applied
  inside shepherd's gateway; inspect/reset via `/api/shepherd/ratelimit/status`
  and `/ratelimit/reset`.
- Path-glob firewall rules (`/api/shepherd/firewall/rules`) block or allow
  traffic into the gateway.

## Operational security checklist

1. Set a non-default `--admin-password`.
2. Set a long random `--secret` (≥ 32 bytes) and keep it out of the repo
   (env `ATP_SECRET`).
3. Put Caddy/TLS in front; terminate TLS before atp sees traffic.
4. Keep `--root` on private storage with filesystem permissions 0700 — it holds
   the sealed secrets.
5. Set `--no-ui` if you manage clients exclusively through the API.
6. Read the workspace AGENTS.md security baseline for the embedded services
   (song magic links for authenticated intent only; pod XML escaping and no raw
   HTML injection from stored values; shepherd zero-knowledge — store hashes of
   anonymized keys, never identifying data).

---

*This document reflects the current code. If a feature is not listed here, it is
not implemented — do not assume otherwise.*