package atp

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// ShareOptions controls a secret share link.
type ShareOptions struct {
	// TTL is how long the link stays valid. A non-positive value means one hour.
	TTL time.Duration
}

// SecretInfo is the metadata of one stored secret. It never carries the value.
type SecretInfo struct {
	Name    string    `json:"name"`
	Note    string    `json:"note,omitempty"`
	Updated time.Time `json:"updated"`
}

// secretEntry is the on-disk (encrypted) form of one secret.
type secretEntry struct {
	Note    string    `json:"note,omitempty"`
	Updated time.Time `json:"updated"`
	Sealed  []byte    `json:"sealed"` // AES-256-GCM(nonce||ciphertext)
}

// shareEntry is one outstanding share link.
type shareEntry struct {
	App     string    `json:"app"`
	Name    string    `json:"name"`
	Expires time.Time `json:"expires"`
}

// Vault is the per-application secrets store. Values are encrypted at rest with
// AES-256-GCM under a key derived from the master secret; the on-disk file
// never holds plaintext.
type Vault struct {
	mu     sync.Mutex
	dir    string
	key    []byte
	shares map[string]shareEntry
}

func newVault(dir string, key []byte) *Vault {
	v := &Vault{dir: dir, key: key, shares: map[string]shareEntry{}}
	_ = os.MkdirAll(dir, 0o700)
	v.loadShares()
	return v
}

func safeApp(app string) (string, error) {
	app = strings.TrimSpace(app)
	if app == "" || !isToken(app) || strings.Contains(app, "..") {
		return "", fmt.Errorf("atp: invalid application name %q", app)
	}
	return app, nil
}

func (v *Vault) appPath(app string) (string, error) {
	a, err := safeApp(app)
	if err != nil {
		return "", err
	}
	return filepath.Join(v.dir, a+".json"), nil
}

func (v *Vault) loadApp(app string) (map[string]secretEntry, error) {
	path, err := v.appPath(app)
	if err != nil {
		return nil, err
	}
	m := map[string]secretEntry{}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return m, nil
		}
		return nil, err
	}
	if len(data) == 0 {
		return m, nil
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return m, nil
}

func (v *Vault) saveApp(app string, m map[string]secretEntry) error {
	path, err := v.appPath(app)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(v.dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// Set creates or rotates a secret. note is optional metadata.
func (v *Vault) Set(app, name, value, note string) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("atp: empty secret name")
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	m, err := v.loadApp(app)
	if err != nil {
		return err
	}
	sealed, err := seal(v.key, []byte(value))
	if err != nil {
		return err
	}
	m[name] = secretEntry{Note: note, Updated: time.Now().UTC(), Sealed: sealed}
	return v.saveApp(app, m)
}

// Get returns the plaintext value of a secret.
func (v *Vault) Get(app, name string) (string, bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	m, err := v.loadApp(app)
	if err != nil {
		return "", false
	}
	e, ok := m[name]
	if !ok {
		return "", false
	}
	plain, err := open(v.key, e.Sealed)
	if err != nil {
		return "", false
	}
	return string(plain), true
}

// List returns the secret names and notes for an application, never values.
func (v *Vault) List(app string) []SecretInfo {
	v.mu.Lock()
	defer v.mu.Unlock()
	m, err := v.loadApp(app)
	if err != nil {
		return nil
	}
	out := make([]SecretInfo, 0, len(m))
	for name, e := range m {
		out = append(out, SecretInfo{Name: name, Note: e.Note, Updated: e.Updated})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Delete removes a secret. It reports whether one existed.
func (v *Vault) Delete(app, name string) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	m, err := v.loadApp(app)
	if err != nil {
		return false
	}
	if _, ok := m[name]; !ok {
		return false
	}
	delete(m, name)
	_ = v.saveApp(app, m)
	return true
}

// Apps lists the application namespaces that hold at least one secret.
func (v *Vault) Apps() []string {
	v.mu.Lock()
	defer v.mu.Unlock()
	entries, err := os.ReadDir(v.dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") || strings.HasPrefix(e.Name(), "_") {
			continue
		}
		out = append(out, strings.TrimSuffix(e.Name(), ".json"))
	}
	sort.Strings(out)
	return out
}

// Share mints an opaque, expiring, revocable link token for a secret.
func (v *Vault) Share(app, name string, o ShareOptions) (string, time.Time, error) {
	if _, ok := v.Get(app, name); !ok {
		return "", time.Time{}, fmt.Errorf("atp: secret %q not found for %q", name, app)
	}
	ttl := o.TTL
	if ttl <= 0 {
		ttl = time.Hour
	}
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return "", time.Time{}, err
	}
	tok := hex.EncodeToString(raw)
	exp := time.Now().UTC().Add(ttl)
	v.mu.Lock()
	defer v.mu.Unlock()
	v.shares[tok] = shareEntry{App: app, Name: name, Expires: exp}
	if err := v.saveShares(); err != nil {
		return "", time.Time{}, err
	}
	return tok, exp, nil
}

// Resolve redeems a share token, returning the plaintext value while it is
// valid.
func (v *Vault) Resolve(token string) (string, bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	e, ok := v.shares[token]
	if !ok || time.Now().UTC().After(e.Expires) {
		return "", false
	}
	m, err := v.loadApp(e.App)
	if err != nil {
		return "", false
	}
	se, ok := m[e.Name]
	if !ok {
		return "", false
	}
	plain, err := open(v.key, se.Sealed)
	if err != nil {
		return "", false
	}
	return string(plain), true
}

// Revoke removes a share token early.
func (v *Vault) Revoke(token string) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	if _, ok := v.shares[token]; !ok {
		return false
	}
	delete(v.shares, token)
	_ = v.saveShares()
	return true
}

func (v *Vault) sharesPath() string { return filepath.Join(v.dir, "_shares.json") }

func (v *Vault) loadShares() {
	data, err := os.ReadFile(v.sharesPath())
	if err != nil {
		return
	}
	_ = json.Unmarshal(data, &v.shares)
	if v.shares == nil {
		v.shares = map[string]shareEntry{}
	}
}

func (v *Vault) saveShares() error {
	data, err := json.MarshalIndent(v.shares, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(v.sharesPath(), data, 0o600)
}
