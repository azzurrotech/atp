# Security — `azzurrotech/atp/web`

## Assets protected

- **The admin session.** Gated by `requireAdmin` / `adminToken`: the
  `atp_session` cookie or an `X-ATP-Token` header validated by
  `auth.Manager`. Tokens are opaque 256-bit random values kept server-side.
- **The secrets vault.** The `/api/clients/{id}/secrets` endpoints expose
  `store.SecretsVault`; values are AES-256-GCM encrypted at rest and returned
  decrypted only to an authenticated admin (`handleGetSecret`).
- **Per-client isolation.** A client's song silo, pod table namespace and
  shepherd scope all derive from its id; the `/c/{client}/...` proxies enforce
  that a request only touches the named client's data.
- **Usage and billing records.** `usageMiddleware` counts attributed traffic and
  writes request-log lines; `/api/clients/{id}/billing` returns the computed
  `store.Cost`.
- **The capability-token trust root.** The master secret signs shepherd tokens
  (via `middleware.New`) and derives the vault key.

## Threat model and mitigations actually implemented

- **Unauthenticated access to admin surfaces.** Every management page and JSON
  endpoint is wrapped in `admin`/`requireAdmin`. API calls without a credential
  get `401`; browser navigations are redirected to `/login` (`StatusFound`).
- **Credential brute force.** Password verification lives in `auth`: 100 000
  iterations of salted SHA-256 (`hashIterations = 100_000`) with
  `subtle.ConstantTimeCompare`.
- **Session theft / fixation.** Session tokens are 32 bytes from `crypto/rand`
  (`auth.LoginWithTTL`); they are never accepted from the client as a chosen
  value, so fixation is not possible. The admin cookie is set with
  `HttpOnly: true` and `SameSite: http.SameSiteLaxMode` (`setSessionCookie`).
- **Cross-client data access.** `handleClientSongAPI` returns `403
  "cannot access another client's silo"` for any silo other than `{client}` and
  `405` for non-GET silo listing; `handleClientPodAPI` returns `403
  "cannot access another client's tables"` for foreign namespaces and `403
  "endpoint is only available to admins"` for pod's admin endpoints
  (`/sql`, `/normalize`, `/reindex`, `/health`); `handleClientVerify` returns
  `403 "token is not scoped to this client"` unless a claim carries
  `client:<id>` or `*`.
- **Token scope escalation.** Every token issued through a client endpoint is
  passed through `clientScopes`, which prepends `client:<id>` and cannot be
  removed by the caller.
- **Disabled tenants.** `activeClient` makes a disabled client's hosted site and
  scoped APIs return `404`, even though its files remain on disk.
- **Secret disclosure on disk.** Values are sealed with AES-256-GCM before
  storage (`store.SecretsVault`); the JSON list endpoints never include values.
- **Magic-link redirect abuse.** `safeRedirect` rejects empty, protocol-relative
  (`//`) and cross-host targets in `handleShepherdMagicRedeem`.
- **Oversized / malformed requests.** `readJSON` limits bodies to 4 MiB
  (`io.LimitReader(r.Body, 4<<20)`) and rejects unknown JSON fields
  (`DisallowUnknownFields`).
- **Stored-value XSS (server-rendered).** UI page values are rendered through
  Go's `html/template` with contextual auto-escaping (`ui.go`, `renderPage`),
  which escapes `{{...}}` interpolations such as `{{.Title}}`, `{{.ClientID}}`,
  `{{.Client.Name}}` and `{{.UserName}}`.
- **Stored-value XSS (client-rendered).** The browser helper `esc()` in
  `templates/base.html` maps `&`, `<`, `>`, `"` and `'` to HTML entities before
  values are written with `innerHTML`. `TestBaseEscaperEscapesHTML`
  (`ui_escape_test.go`) asserts the mapping and fails if it regresses to an
  identity map.
- **Request-log minimisation.** The logged `store.RequestRecord` contains time,
  client, method, path, status, request/response byte counts, duration, remote
  IP and a user agent truncated to 200 bytes (`truncate(r.UserAgent(), 200)`) —
  never bodies, headers or cookies.

## Authentication and authorization model

- **Single admin, no RBAC.** Any valid session is full admin. There are no
  roles, scopes or per-tenant admin accounts; `auth.Manager` holds one identity.
- **Two credential transports.** `adminToken` accepts the `atp_session` cookie
  or an `X-ATP-Token` header; `currentSessionToken` uses the same order so
  session-management actions can identify "this" session.
- **Client scoping is authorization, not authentication.** The
  `/c/{client}/...` proxies still require the admin session; scoping decides
  *which client's* data that admin request may touch. Note the client's *public*
  web app (`GET /c/{client}/...`) is deliberately unauthenticated and only
  serves the silo if the client is active.
- **Token scopes gate capabilities, not admin access.** `client:<id>` scopes are
  enforced when verifying tokens (`handleClientVerify`) and when issuing them
  (`clientScopes`).
- **Session management.** `/api/sessions` lists sessions and marks the current
  one; `/api/sessions/revoke` refuses to revoke the current token (use
  `/logout`); `/api/sessions/revoke-all` revokes all others and clears the
  cookie.

## Honest limits / non-claims

- **No MFA, no RBAC, no lockout.** The admin gate is a single password; there is
  no second factor, no account roles and no server-side failed-login throttling
  in this package.
- **Default admin credentials.** `NewATPService` defaults `AdminPassword` to
  `"admin"` when unset; `Start` logs a warning in that case. The standalone
  binary requires a `-secret` but still defaults the admin password unless
  `-admin-password`/`ATP_ADMIN_PASSWORD` is set.
- **The admin cookie is not marked `Secure`.** `setSessionCookie` sets
  `HttpOnly` and `SameSite=Lax` but not `Secure`, so the cookie can traverse
  plain HTTP. Only the shepherd magic-link cookie sets `Secure`, and only when
  TLS or `X-Forwarded-Proto: https` is present.
- **No explicit CSRF tokens.** State-changing routes rely on `SameSite=Lax`
  cookies (and the header-token transport) rather than a CSRF token.
- **Client-side escaping depends on the helper contract.** Pages that build
  table rows with `innerHTML` must interpolate API values through `esc()` (see
  the mitigation above). A page that bypasses `esc()` would reintroduce the old
  gap; the helper itself is now covered by `TestBaseEscaperEscapesHTML`.
- **The privileged token issuance bodies set no operation timeout.** Shepherd
  token operations defer to the embedded middleware; atp adds no extra
  authorization on top of the admin session.
- **`remote_ip` trusts `X-Forwarded-For`.** `remoteIP` takes the first
  `X-Forwarded-For` value without verifying the peer, so log/attribution IPs are
  spoofable unless a trusted proxy rewrites the header.
- **Sessions are in memory.** Restarting the process drops all sessions
  (`auth.Manager` has no persistence).
- **No audit log of admin actions.** Usage logs record client traffic, not
  which admin performed a management action.

## Dependency note

`web` imports no third-party packages. It uses the Go standard library, the
in-repo sibling modules `azzurrotech/pod`, `azzurrotech/shepherd/{firewall,middleware,token}`
and `azzurrotech/song/{pkg/song,pkg/static}`, and atp's own
`azzurrotech/atp/internal/{auth,store}`. The sibling modules are themselves
standard-library-only; the whole system is dependency-free.

## Reporting

Report suspected vulnerabilities privately to **security@azzurro.tech**. Do not
open a public issue or pull request describing the problem.
