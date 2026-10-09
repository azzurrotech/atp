# Security — `azzurrotech/atp/internal/auth`

## Assets protected

- **The admin credential.** The package stores a salted, iterated SHA-256
  digest (`Manager.hash`) rather than the password. The plaintext password is
  never persisted by this package. The salt is 16 bytes from `crypto/rand`
  (`NewManager`).
- **Admin sessions.** Session tokens are held server-side in
  `Manager.sessions` (`map[string]time.Time`). The cookie only ever carries the
  random token; no identity or claim data is encoded in it.
- **Session validity/expiry.** `Authenticate`, `Count` and `ListSessions` purge
  expired entries; `Logout`, `RevokeAll` and `RevokeOthers` remove live ones.

This package is one half of atp's admin login. The secrets vault, per-client
isolation and usage/billing records are protected by `azzurrotech/atp/internal/store`
and `azzurrotech/atp/web`; see their `SECURITY.md` files.

## Threat model and mitigations actually implemented

- **Offline password cracking after a file leak.** The stored value is
  `SHA-256(salt || SHA-256^100000(password))` with a per-manager random salt
  (`deriveHash`, `hashIterations = 100_000`). This raises the cost per guess but
  is not memory-hard.
- **Timing attacks on the digest comparison.** `Verify` compares digests with
  `subtle.ConstantTimeCompare` (`crypto/subtle`), returning `1` only on an exact
  match.
- **Guessable session tokens.** Tokens are 32 bytes from `crypto/rand`,
  hex-encoded (`LoginWithTTL`), giving 256 bits of entropy. The login endpoint
  returns the same opaque error for a bad user or a bad password, so usernames
  are not enumerated by response text.
- **Server-side revocation.** Because sessions are an in-memory table, a token
  can be revoked at any time with `Logout`/`RevokeOthers`/`RevokeAll` without
  needing a token blacklist.
- **Cookie flags.** This package does not touch cookies; the HTTP layer sets the
  `atp_session` cookie with `HttpOnly: true` and `SameSite: http.SameSiteLaxMode`
  (`web.setSessionCookie`). Note that `Secure` is not set for the admin cookie.
- **CSPRNG failure fallback.** `NewManager` falls back to a time-derived salt if
  `crypto/rand.Read` errors. This path is documented as effectively unreachable;
  it is a startup-availability trade-off, not strong randomness.

## Authentication and authorization model

- **Single administrator.** `Manager` stores one `user` and one `hash`. `Verify`
  accepts only that exact username/password pair; there is no user table.
- **No roles.** The package has no notion of scopes, roles or permissions. atp's
  HTTP layer treats any valid session as full admin. Per-client scoping lives in
  `web`, not here.
- **Session TTL.** Default `DefaultTTL` (12 hours); `RememberTTL` (30 days) is
  used when the caller passes it to `LoginWithTTL` (the web login sets it for the
  "remember me" checkbox).

## Honest limits / non-claims

- **Passwords are stretched, not memory-hard.** 100 000 iterations of SHA-256 is
  weaker against GPU/ASIC cracking than bcrypt/scrypt/Argon2. This is a
  standard-library-only constraint, not a claim of modern password-hashing
  strength.
- **No MFA, no lockout, no password policy.** There is no second factor, no
  failed-login throttling and no password strength enforcement in this package.
- **Sessions are in memory.** All sessions are lost on restart (`NewManager`
  starts with an empty map). There is no session persistence and no token
  hashing at rest — a memory dump or core file exposes live tokens.
- **No token rotation.** A session token stays valid until expiry or an explicit
  revoke; there is no automatic rotation.
- **Username check is not constant-time.** Only the digest comparison is
  constant-time; the username comparison short-circuits. This exposes username
  existence only to a local timing observer, and login response text does not
  distinguish the cases.
- **No CSRF/clickjacking protection here.** Those are HTTP-layer concerns handled
  (partly) by `web`; this package issues tokens only.

## Dependency note

`auth` imports the Go standard library only: `crypto/rand`, `crypto/sha256`,
`crypto/subtle`, `encoding/hex`, `sync` and `time`. It has no third-party or
in-module dependencies.

## Reporting

Report suspected vulnerabilities privately to **security@azzurro.tech**. Do not
open a public issue or pull request describing the problem.
