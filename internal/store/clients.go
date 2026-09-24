// Package store implements atp's persisted data model: clients, per-client
// secrets, request logs, hourly usage samples and billing. Everything is
// JSON on the local filesystem under the atp root — no database, standard
// library only.
package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// clientIDRe accepts the same web-safe names song allows for silos: letters,
// digits, dot, dash and underscore, starting and ending with a letter or digit.
var clientIDRe = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9._-]*[A-Za-z0-9])?$`)

// reservedClientIDs cannot be used as client ids because atp, song or the
// HTTP layer routes those segments to built-in surfaces.
var reservedClientIDs = map[string]bool{
	"api": true, "admin": true, "health": true, "song": true, ".song": true,
	"clients": true, "login": true, "static": true, "assets": true, "c": true,
}

// Defaults applied when a client or the platform does not override them.
const (
	DefaultPricePerGBHour    = 0.05  // USD per GB per hour
	DefaultRetentionHours    = 24    // how long request logs are kept
	DefaultMaxRetentionHours = 24 * 365 // cap for retention_hours
)

// Client is a tenant on the platform. Each client owns exactly one song silo
// (static files), one pod table namespace (<id>/...) and a shepherd scope
// (<id>:*), all derived from the client id.
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

// Settings holds platform-wide defaults that clients inherit.
type Settings struct {
	DefaultPricePerGBHour    float64 `json:"default_price_per_gb_hour"`
	DefaultRetentionHours    int     `json:"default_retention_hours"`
	MaxUploadBytes           int64   `json:"max_upload_bytes"`
	RequestLogLimitPerClient int     `json:"request_log_limit_per_client"`
}

// DefaultSettings returns the platform defaults.
func DefaultSettings() Settings {
	return Settings{
		DefaultPricePerGBHour:    DefaultPricePerGBHour,
		DefaultRetentionHours:    DefaultRetentionHours,
		MaxUploadBytes:           64 << 20, // 64 MiB, matching song/pod
		RequestLogLimitPerClient: 2000,     // newest N entries shown per client
	}
}

// file holds the on-disk shape of clients.json.
type file struct {
	Settings Settings `json:"settings"`
	Clients  []Client `json:"clients"`
	Updated  string   `json:"updated"`
}

// ClientStore persists clients and platform settings under <root>/atp/clients.json.
type ClientStore struct {
	mu       sync.RWMutex
	root     string
	path     string
	settings Settings
	clients  map[string]Client
}

// NewClientStore loads (creating when needed) the client registry.
func NewClientStore(root string) (*ClientStore, error) {
	if root == "" {
		root = "./data"
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	cs := &ClientStore{
		root:     abs,
		path:     filepath.Join(abs, "atp", "clients.json"),
		settings: DefaultSettings(),
		clients:  map[string]Client{},
	}
	if err := os.MkdirAll(filepath.Dir(cs.path), 0o755); err != nil {
		return nil, err
	}
	if data, err := os.ReadFile(cs.path); err == nil {
		var f file
		if err := json.Unmarshal(data, &f); err != nil {
			return nil, fmt.Errorf("clients.json: %w", err)
		}
		if f.Settings.DefaultPricePerGBHour == 0 {
			f.Settings = cs.settings
		}
		cs.settings = f.Settings
		for _, c := range f.Clients {
			cs.clients[c.ID] = c
		}
		return cs, nil
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	return cs, cs.save()
}

// Root returns the atp root directory.
func (cs *ClientStore) Root() string { return cs.root }

// Settings returns the platform settings.
func (cs *ClientStore) Settings() Settings {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	return cs.settings
}

// SetSettings replaces the platform settings and persists them.
func (cs *ClientStore) SetSettings(s Settings) error {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if s.DefaultPricePerGBHour <= 0 {
		s.DefaultPricePerGBHour = DefaultPricePerGBHour
	}
	if s.DefaultRetentionHours <= 0 {
		s.DefaultRetentionHours = DefaultRetentionHours
	}
	if s.MaxUploadBytes <= 0 {
		s.MaxUploadBytes = 64 << 20
	}
	cs.settings = s
	return cs.save()
}

// ValidateClientID reports whether id may become a client id.
func ValidateClientID(id string) error {
	if !clientIDRe.MatchString(id) {
		return fmt.Errorf("client id %q is not web-safe (letters, digits, dot, dash, underscore)", id)
	}
	if reservedClientIDs[id] {
		return fmt.Errorf("client id %q is reserved", id)
	}
	return nil
}

// Create adds a new client, applying platform defaults for pricing and
// retention. It fails when the id is taken or invalid.
func (cs *ClientStore) Create(id, name, notes string) (*Client, error) {
	if err := ValidateClientID(id); err != nil {
		return nil, err
	}
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if _, ok := cs.clients[id]; ok {
		return nil, fmt.Errorf("client %q already exists", id)
	}
	c := Client{
		ID:             id,
		Name:           name,
		Created:        time.Now().UTC().Format(time.RFC3339),
		Chargeable:     true,
		PricePerGBHour: cs.settings.DefaultPricePerGBHour,
		RetentionHours: cs.settings.DefaultRetentionHours,
		Notes:          notes,
	}
	if c.Name == "" {
		c.Name = id
	}
	cs.clients[id] = c
	if err := cs.save(); err != nil {
		delete(cs.clients, id)
		return nil, err
	}
	out := c
	return &out, nil
}

// Get returns a copy of a client.
func (cs *ClientStore) Get(id string) (*Client, error) {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	c, ok := cs.clients[id]
	if !ok {
		return nil, fmt.Errorf("client %q not found", id)
	}
	out := c
	return &out, nil
}

// List returns all clients sorted by id.
func (cs *ClientStore) List() []Client {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	out := make([]Client, 0, len(cs.clients))
	for _, c := range cs.clients {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Update applies a partial update to a client. Zero-valued optional fields
// (PricePerGBHour, RetentionHours, Notes) keep their previous value.
func (cs *ClientStore) Update(id string, patch Client) (*Client, error) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	cur, ok := cs.clients[id]
	if !ok {
		return nil, fmt.Errorf("client %q not found", id)
	}
	if patch.Name != "" {
		cur.Name = patch.Name
	}
	if patch.PricePerGBHour > 0 {
		cur.PricePerGBHour = patch.PricePerGBHour
	}
	if patch.RetentionHours > 0 {
		if patch.RetentionHours > DefaultMaxRetentionHours {
			return nil, fmt.Errorf("retention_hours must be <= %d", DefaultMaxRetentionHours)
		}
		cur.RetentionHours = patch.RetentionHours
	}
	if patch.Notes != "" {
		cur.Notes = patch.Notes
	}
	cur.Chargeable = patch.Chargeable
	cur.Disabled = patch.Disabled
	cs.clients[id] = cur
	if err := cs.save(); err != nil {
		return nil, err
	}
	out := cur
	return &out, nil
}

// SetChargeable flips the billing switch of a client.
func (cs *ClientStore) SetChargeable(id string, chargeable bool) error {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	cur, ok := cs.clients[id]
	if !ok {
		return fmt.Errorf("client %q not found", id)
	}
	cur.Chargeable = chargeable
	cs.clients[id] = cur
	return cs.save()
}

// Delete removes a client from the registry. It is the caller's choice
// whether the silo/pod/log data directories are also removed.
func (cs *ClientStore) Delete(id string) error {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if _, ok := cs.clients[id]; !ok {
		return fmt.Errorf("client %q not found", id)
	}
	delete(cs.clients, id)
	return cs.save()
}

// Exists reports whether id is a known client.
func (cs *ClientStore) Exists(id string) bool {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	_, ok := cs.clients[id]
	return ok
}

func (cs *ClientStore) save() error {
	f := file{
		Settings: cs.settings,
		Clients:  make([]Client, 0, len(cs.clients)),
		Updated:  time.Now().UTC().Format(time.RFC3339),
	}
	for _, c := range cs.clients {
		f.Clients = append(f.Clients, c)
	}
	sort.Slice(f.Clients, func(i, j int) bool { return f.Clients[i].ID < f.Clients[j].ID })
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(cs.path, data)
}

// atomicWrite writes data to path atomically (temp file + rename).
func atomicWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return err
	}
	return nil
}

// Join is a small path helper for callers that need to build atp-managed
// subdirectories inside the root.
func Join(root string, elems ...string) string {
	return filepath.Join(append([]string{root}, elems...)...)
}

// SanitizeSegment ensures a path segment is safe for use in file paths.
func SanitizeSegment(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "unnamed"
	}
	repl := strings.NewReplacer("/", "_", "\\", "_", "..", "_", "\x00", "_")
	return repl.Replace(s)
}