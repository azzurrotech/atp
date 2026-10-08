// Package auth implements atp's admin authentication with nothing but the
// standard library: an in-memory session store, salted+iterated SHA-256
// password hashing and constant-time comparison. Session tokens are CSPRNG
// random and held server-side; the cookie carries only the random token.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"sync"
	"time"
)

// DefaultTTL is how long an admin session lives.
const DefaultTTL = 12 * time.Hour

// RememberTTL is how long a "remember me" session lives (30 days).
const RememberTTL = 30 * 24 * time.Hour

// hashIterations stretches SHA-256 to slow brute force a little. 100k rounds
// stays instant on modern hardware while being far costlier than one round.
const hashIterations = 100_000

// Manager owns the admin identity and the session table.
type Manager struct {
	mu       sync.Mutex
	user     string
	salt     []byte
	hash     []byte // SHA-256(salt || SHA-256^iterations(password))
	ttl      time.Duration
	sessions map[string]time.Time
}

// NewManager builds a Manager for the given admin credentials.
func NewManager(user, password string) *Manager {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		// crypto/rand failure is unrecoverable; fall back to time-based salt
		// so the server can still start (this path is effectively never hit).
		now := time.Now().UnixNano()
		for i := 0; i < 16; i++ {
			salt[i] = byte(now >> (i % 8 * 8))
		}
	}
	if user == "" {
		user = "admin"
	}
	return &Manager{
		user:     user,
		salt:     salt,
		hash:     deriveHash(password, salt),
		ttl:      DefaultTTL,
		sessions: map[string]time.Time{},
	}
}

// User returns the configured admin username.
func (m *Manager) User() string { return m.user }

// Verify checks a username/password pair. Constant-time on the hash compare.
func (m *Manager) Verify(user, password string) bool {
	if user != m.user {
		return false
	}
	got := deriveHash(password, m.salt)
	return subtle.ConstantTimeCompare(got, m.hash) == 1
}

// Login starts a session and returns its token.
func (m *Manager) Login(user, password string) (string, error) {
	return m.LoginWithTTL(user, password, m.ttl)
}

// LoginWithTTL starts a session with a custom TTL and returns its token.
func (m *Manager) LoginWithTTL(user, password string, ttl time.Duration) (string, error) {
	if !m.Verify(user, password) {
		return "", ErrUnauthorized
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	token := hex.EncodeToString(b)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.purgeLocked()
	m.sessions[token] = time.Now().Add(ttl)
	return token, nil
}

// Authenticate reports whether a session token is valid and unexpired.
func (m *Manager) Authenticate(token string) bool {
	if token == "" {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	exp, ok := m.sessions[token]
	if !ok {
		return false
	}
	if time.Now().After(exp) {
		delete(m.sessions, token)
		return false
	}
	return true
}

// Logout revokes a session token.
func (m *Manager) Logout(token string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sessions, token)
}

// Count returns the number of live sessions (for the admin overview).
func (m *Manager) Count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.purgeLocked()
	return len(m.sessions)
}

// SessionInfo represents an active session.
type SessionInfo struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
	IsCurrent bool      `json:"is_current"`
}

// ListSessions returns all active sessions with their expiry times.
func (m *Manager) ListSessions() []SessionInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.purgeLocked()
	out := make([]SessionInfo, 0, len(m.sessions))
	for token, exp := range m.sessions {
		out = append(out, SessionInfo{
			Token:     token,
			ExpiresAt: exp,
		})
	}
	return out
}

// RevokeAll revokes all sessions.
func (m *Manager) RevokeAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessions = map[string]time.Time{}
}

// RevokeOthers revokes all sessions except the current one.
func (m *Manager) RevokeOthers(currentToken string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.purgeLocked()
	for token := range m.sessions {
		if token != currentToken {
			delete(m.sessions, token)
		}
	}
}

func (m *Manager) purgeLocked() {
	now := time.Now()
	for t, exp := range m.sessions {
		if now.After(exp) {
			delete(m.sessions, t)
		}
	}
}

// deriveHash computes salted, iterated SHA-256 of the password.
func deriveHash(password string, salt []byte) []byte {
	h := sha256.New()
	h.Write(salt)
	h.Write([]byte(password))
	digest := h.Sum(nil)
	for i := 0; i < hashIterations; i++ {
		h := sha256.New()
		h.Write(digest)
		h.Write(salt)
		digest = h.Sum(nil)
	}
	return digest
}

// ErrUnauthorized is returned for bad credentials.
var ErrUnauthorized = &unauthorizedError{}

type unauthorizedError struct{}

func (*unauthorizedError) Error() string { return "invalid username or password" }
