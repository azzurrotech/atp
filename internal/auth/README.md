# auth — in-memory admin authentication for atp

## Overview

`auth` is the admin-authentication package of the `atp` orchestrator. It lives
in the `azzurrotech/atp` module at the import path
`azzurrotech/atp/internal/auth`, so Go's `internal/` rule restricts it to code
rooted inside the `atp` module. Its only in-tree consumer is `azzurrotech/atp/web`.

`atp` is itself embedded by the `stenella` platform (module
`azzurrotech/stenella`), which mounts atp as middleware, and `atp` embeds the
`pod`, `shepherd` and `song` modules. This package is independent of all of
those: it imports only the standard library (`crypto/rand`, `crypto/sha256`,
`crypto/subtle`, `encoding/hex`, `sync`, `time`) and talks to no storage.

`auth` provides exactly one administrative identity. The username and password
come from `web.Options.AdminUser` / `web.Options.AdminPassword`; the package
stores a salted, iterated SHA-256 digest of the password and keeps a table of
opaque session tokens in memory. There are no user accounts, roles, groups or
persistence — see `SECURITY.md` for the honest limits.

## Public API

All exported identifiers are listed below with their exact signatures.

### Constants

```go
// DefaultTTL is how long an admin session lives.
const DefaultTTL = 12 * time.Hour

// RememberTTL is how long a "remember me" session lives (30 days).
const RememberTTL = 30 * 24 * time.Hour
```

### The manager

```go
// Manager owns the admin identity and the session table.
type Manager struct { /* unexported fields */ }

// NewManager builds a Manager for the given admin credentials. An empty user
// becomes "admin". The initial TTL is DefaultTTL.
func NewManager(user, password string) *Manager

// User returns the configured admin username.
func (m *Manager) User() string

// Verify checks a username/password pair. Constant-time on the hash compare.
func (m *Manager) Verify(user, password string) bool
```

### Sessions

```go
// Login starts a session and returns its token (TTL = m.ttl, i.e. DefaultTTL).
func (m *Manager) Login(user, password string) (string, error)

// LoginWithTTL starts a session with a custom TTL and returns its token.
func (m *Manager) LoginWithTTL(user, password string, ttl time.Duration) (string, error)

// Authenticate reports whether a session token is valid and unexpired.
func (m *Manager) Authenticate(token string) bool

// Logout revokes a session token.
func (m *Manager) Logout(token string)

// Count returns the number of live sessions (purging expired ones first).
func (m *Manager) Count() int

// ListSessions returns all active sessions with their expiry times.
func (m *Manager) ListSessions() []SessionInfo

// RevokeAll revokes all sessions.
func (m *Manager) RevokeAll()

// RevokeOthers revokes all sessions except the current one.
func (m *Manager) RevokeOthers(currentToken string)
```

### Session listing

```go
// SessionInfo represents an active session.
type SessionInfo struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
	IsCurrent bool      `json:"is_current"`
}
```

`IsCurrent` is not set by `ListSessions`; the HTTP layer fills it in by
comparing the caller's own token (`web.ATPService.handleListSessions`).

### Errors

```go
// ErrUnauthorized is returned for bad credentials.
var ErrUnauthorized = &unauthorizedError{}
```

`ErrUnauthorized.Error()` is `"invalid username or password"`.

## Usage

```go
package main

import (
	"log"
	"net/http"
	"time"

	"azzurrotech/atp/internal/auth"
)

func main() {
	m := auth.NewManager("admin", "correct horse battery staple")

	// Successful login returns a 64-character hex token (32 random bytes).
	tok, err := m.LoginWithTTL("admin", "correct horse battery staple", auth.RememberTTL)
	if err != nil {
		log.Fatal(err)
	}

	// The token is the only thing the client holds; the server keeps the
	// session table. Validate on every protected request.
	if !m.Authenticate(tok) {
		log.Fatal("session invalid")
	}

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if !m.Authenticate(tok) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Write([]byte("hello, admin"))
	})
	_ = time.Second
}
```

## Configuration, inputs, defaults and limits

- `NewManager(user, password)` derives a 16-byte salt with `crypto/rand`.
  If `crypto/rand` fails (effectively never) it falls back to a time-derived
  non-random salt so the process can still start.
- An empty `user` is replaced with `"admin"`.
- The password digest is `SHA-256(salt || SHA-256^100000(password))`; the
  stretch count is the unexported constant `hashIterations = 100_000`.
- Sessions live in a `map[string]time.Time` guarded by a `sync.Mutex`.
- `Login`/`LoginWithTTL` return an error (wrapping semantics: the exact
  `ErrUnauthorized` value) for any bad username or password.
- Tokens are 32 CSPRNG bytes hex-encoded, i.e. 64 lowercase hex characters.
- `Authenticate` rejects the empty string and removes expired entries on
  access. `Count` and `ListSessions` purge expired entries before returning.
- There is no persistence: restarting the process drops every session.
- There is no rate limiting or lockout inside this package; the atp HTTP
  layer leaves that responsibility to the deployment.

## Testing

From the `atp` module root:

```sh
cd /home/matthew/Projects/Platform/stenella/atp
go test ./internal/auth/
```

`auth_test.go` covers:

- `TestLoginAuthenticateLogout` — a fresh token is 64 hex characters,
  authenticates, and stops authenticating after `Logout`.
- `TestLoginRejectsBadPassword` — both a wrong password and a wrong username
  fail.
- `TestVerify` — `Verify` accepts only the exact pair.
- `TestDistinctSalts` — two managers built with identical inputs get different
  salts and therefore different stored hashes.

## Design notes and invariants

- **Opaque, server-side sessions.** The token is random and carries no data;
  the server owns the session table. A leaked token is only useful while it is
  live and cannot be forged without the random bytes.
- **Constant-time compare.** `Verify` uses `subtle.ConstantTimeCompare` for the
  digest; the username comparison is an early string comparison.
- **Stretching, not memory-hard hashing.** 100 000 SHA-256 rounds slow brute
  force relative to a single hash but are not a memory-hard KDF. This is a
  deliberate standard-library-only trade-off; see `SECURITY.md`.
- **Monotonic wall clock.** Expiry is stored as `time.Now().Add(ttl)` and
  compared with `time.Now().After(exp)`. Clock jumps backwards could extend a
  session; the process is expected to run with a sane clock.
- **One identity.** `Manager` holds a single user string and one digest, so the
  whole package assumes a single administrator.
