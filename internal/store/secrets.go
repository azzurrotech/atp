package store

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Secret is a named value owned by a client, stored encrypted at rest.
type Secret struct {
	Name    string `json:"name"`
	Value   string `json:"value,omitempty"` // decrypted form; never persisted
	Created string `json:"created"`
	Updated string `json:"updated"`
	Note    string `json:"note,omitempty"`
}

// sealedSecret is the on-disk (encrypted) form of a secret value.
type sealedSecret struct {
	Ciphertext string `json:"ciphertext"` // base64(nonce || AES-GCM ciphertext)
	Created    string `json:"created"`
	Updated    string `json:"updated"`
	Note       string `json:"note"`
}

type secretsFile struct {
	Version int                    `json:"version"`
	Secrets map[string]sealedSecret `json:"secrets"`
}

// SecretsVault stores per-client secrets under <root>/atp/secrets/<client>.json.
// Every value is encrypted at rest with AES-256-GCM using a key derived from
// the atp master secret. Nothing sensitive is ever written in plaintext.
type SecretsVault struct {
	mu   sync.Mutex
	root string
	key  []byte // 32 bytes, derived from the atp master secret
}

// NewSecretsVault derives the encryption key from the master secret.
// The master secret must be at least 32 bytes long.
func NewSecretsVault(root, masterSecret string) (*SecretsVault, error) {
	if len(masterSecret) < 32 {
		return nil, fmt.Errorf("atp master secret must be at least 32 bytes long")
	}
	sum := sha256.Sum256([]byte(masterSecret))
	if err := os.MkdirAll(filepath.Join(root, "atp", "secrets"), 0o755); err != nil {
		return nil, err
	}
	return &SecretsVault{root: root, key: sum[:]}, nil
}

func (v *SecretsVault) path(client string) string {
	return filepath.Join(v.root, "atp", "secrets", SanitizeSegment(client)+".json")
}

// Set stores (or replaces) a named secret for a client. value is encrypted
// before it hits the disk.
func (v *SecretsVault) Set(client, name, value, note string) (*Secret, error) {
	if name == "" {
		return nil, fmt.Errorf("secret name is required")
	}
	if value == "" {
		return nil, fmt.Errorf("secret value is required")
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	f, err := v.load(client)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	prev, ok := f.Secrets[name]
	created := now
	if ok {
		created = prev.Created
	}
	blob, err := v.seal(value)
	if err != nil {
		return nil, err
	}
	f.Secrets[name] = sealedSecret{Ciphertext: blob, Created: created, Updated: now, Note: note}
	if err := v.save(client, f); err != nil {
		return nil, err
	}
	return &Secret{Name: name, Created: created, Updated: now, Note: note}, nil
}

// Get returns a decrypted secret.
func (v *SecretsVault) Get(client, name string) (*Secret, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	f, err := v.load(client)
	if err != nil {
		return nil, err
	}
	ss, ok := f.Secrets[name]
	if !ok {
		return nil, fmt.Errorf("secret %q not found", name)
	}
	val, err := v.open(ss.Ciphertext)
	if err != nil {
		return nil, err
	}
	return &Secret{Name: name, Value: val, Created: ss.Created, Updated: ss.Updated, Note: ss.Note}, nil
}

// List returns the names and metadata of all secrets (values stay hidden).
func (v *SecretsVault) List(client string) ([]Secret, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	f, err := v.load(client)
	if err != nil {
		return nil, err
	}
	out := make([]Secret, 0, len(f.Secrets))
	for name, ss := range f.Secrets {
		out = append(out, Secret{Name: name, Created: ss.Created, Updated: ss.Updated, Note: ss.Note})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Has reports whether a secret exists.
func (v *SecretsVault) Has(client, name string) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	f, err := v.load(client)
	if err != nil {
		return false
	}
	_, ok := f.Secrets[name]
	return ok
}

// Delete removes a secret.
func (v *SecretsVault) Delete(client, name string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	f, err := v.load(client)
	if err != nil {
		return err
	}
	if _, ok := f.Secrets[name]; !ok {
		return fmt.Errorf("secret %q not found", name)
	}
	delete(f.Secrets, name)
	return v.save(client, f)
}

// Rotate regenerates the encryption key (used when the master secret changes)
// by re-encrypting every value with the new key.
func (v *SecretsVault) Rotate(newMasterSecret string) error {
	if len(newMasterSecret) < 32 {
		return fmt.Errorf("atp master secret must be at least 32 bytes long")
	}
	newKey := sha256.Sum256([]byte(newMasterSecret))
	v.mu.Lock()
	defer v.mu.Unlock()
	clients, err := v.clientList()
	if err != nil {
		return err
	}
	old := append([]byte(nil), v.key...)
	for _, client := range clients {
		f, err := v.load(client)
		if err != nil {
			return err
		}
		for name, ss := range f.Secrets {
			val, err := v.openWith(old, ss.Ciphertext)
			if err != nil {
				return fmt.Errorf("re-encrypt %s/%s: %w", client, name, err)
			}
			blob, err := sealWith(newKey[:], val)
			if err != nil {
				return err
			}
			ss.Ciphertext = blob
			f.Secrets[name] = ss
		}
		if err := v.save(client, f); err != nil {
			return err
		}
	}
	v.key = newKey[:]
	return nil
}

// --- internal -----------------------------------------------------------------

func (v *SecretsVault) load(client string) (*secretsFile, error) {
	f := &secretsFile{Version: 1, Secrets: map[string]sealedSecret{}}
	data, err := os.ReadFile(v.path(client))
	if err != nil {
		if os.IsNotExist(err) {
			return f, nil
		}
		return nil, err
	}
	if err := json.Unmarshal(data, f); err != nil {
		return nil, fmt.Errorf("secrets for %q: %w", client, err)
	}
	if f.Secrets == nil {
		f.Secrets = map[string]sealedSecret{}
	}
	return f, nil
}

func (v *SecretsVault) save(client string, f *secretsFile) error {
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(v.path(client), data)
}

func (v *SecretsVault) clientList() ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(v.root, "atp", "secrets"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".json" {
			out = append(out, e.Name()[:len(e.Name())-len(".json")])
		}
	}
	sort.Strings(out)
	return out, nil
}

func (v *SecretsVault) seal(plain string) (string, error)   { return sealWith(v.key, plain) }
func (v *SecretsVault) open(blob string) (string, error)    { return openWith(v.key, blob) }
func (v *SecretsVault) openWith(k []byte, blob string) (string, error) { return openWith(k, blob) }

func sealWith(key []byte, plain string) (string, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	ct := gcm.Seal(nonce, nonce, []byte(plain), nil)
	return base64.StdEncoding.EncodeToString(ct), nil
}

func openWith(key []byte, blob string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(blob)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(raw) < gcm.NonceSize() {
		return "", fmt.Errorf("ciphertext too short")
	}
	nonce, ct := raw[:gcm.NonceSize()], raw[gcm.NonceSize():]
	plain, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}