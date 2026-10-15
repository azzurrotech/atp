package atp

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// StaticBox is atp's encrypted static overlay: named assets are sealed with
// AES-256-GCM into <root>/.atp/static and decrypted transparently when served
// from /_atp/static/…. It lets an application keep static content encrypted at
// rest without changing song.
type StaticBox struct {
	mu  sync.Mutex
	dir string
	key []byte
}

type boxEntry struct {
	Name    string    `json:"name"`
	Type    string    `json:"type,omitempty"`
	Updated time.Time `json:"updated"`
	Sealed  []byte    `json:"sealed"`
}

func newStaticBox(dir string, key []byte) *StaticBox {
	_ = os.MkdirAll(dir, 0o700)
	return &StaticBox{dir: dir, key: key}
}

// Dir returns the overlay's directory.
func (b *StaticBox) Dir() string { return b.dir }

func (b *StaticBox) entryPath(name string) (string, error) {
	clean := strings.Trim(strings.TrimSpace(name), "/")
	if clean == "" || strings.Contains(clean, "..") {
		return "", fmt.Errorf("atp: invalid static asset name %q", name)
	}
	return filepath.Join(b.dir, filepath.FromSlash(clean)+".json"), nil
}

// Put seals content under a logical name (e.g. "notes/style.css").
func (b *StaticBox) Put(name, contentType string, content []byte) error {
	if contentType == "" {
		contentType = contentTypeFor(name)
	}
	p, err := b.entryPath(name)
	if err != nil {
		return err
	}
	sealed, err := seal(b.key, content)
	if err != nil {
		return err
	}
	e := boxEntry{Name: name, Type: contentType, Updated: time.Now().UTC(), Sealed: sealed}
	data, err := json.Marshal(e)
	if err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	return os.WriteFile(p, data, 0o600)
}

// Get returns the decrypted content and content type of a stored asset.
func (b *StaticBox) Get(name string) ([]byte, string, bool) {
	p, err := b.entryPath(name)
	if err != nil {
		return nil, "", false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, "", false
	}
	var e boxEntry
	if err := json.Unmarshal(data, &e); err != nil {
		return nil, "", false
	}
	plain, err := open(b.key, e.Sealed)
	if err != nil {
		return nil, "", false
	}
	return plain, e.Type, true
}

// List returns the stored asset names.
func (b *StaticBox) List() []string {
	var out []string
	_ = filepath.WalkDir(b.dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".json") {
			return nil
		}
		rel, err := filepath.Rel(b.dir, p)
		if err != nil {
			return nil
		}
		out = append(out, filepath.ToSlash(strings.TrimSuffix(rel, ".json")))
		return nil
	})
	sort.Strings(out)
	return out
}

// Delete removes a stored asset. It reports whether it existed.
func (b *StaticBox) Delete(name string) bool {
	p, err := b.entryPath(name)
	if err != nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := os.Remove(p); err != nil {
		return false
	}
	return true
}

// serveStaticBox serves /_atp/static/… from the overlay.
func (s *Server) serveStaticBox(w http.ResponseWriter, r *http.Request) bool {
	name := strings.TrimPrefix(r.URL.Path, "/_atp/static/")
	if name == r.URL.Path {
		return false
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return true
	}
	content, ctype, ok := s.box.Get(name)
	if !ok {
		http.NotFound(w, r)
		return true
	}
	if ctype == "" {
		ctype = http.DetectContentType(content)
	}
	w.Header().Set("Content-Type", ctype)
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(content)
	}
	return true
}

// contentTypeFor returns a best-effort MIME type from a file extension.
func contentTypeFor(name string) string {
	switch ext(name) {
	case ".html", ".htm":
		return "text/html; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".js", ".mjs":
		return "application/javascript; charset=utf-8"
	case ".json":
		return "application/json; charset=utf-8"
	case ".xml":
		return "application/xml; charset=utf-8"
	case ".svg":
		return "image/svg+xml"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".avif":
		return "image/avif"
	case ".ico":
		return "image/x-icon"
	case ".wasm":
		return "application/wasm"
	case ".txt", ".md":
		return "text/plain; charset=utf-8"
	case ".vtt":
		return "text/vtt; charset=utf-8"
	case ".mp4":
		return "video/mp4"
	case ".webm":
		return "video/webm"
	case ".mp3":
		return "audio/mpeg"
	case ".ogg":
		return "audio/ogg"
	case ".woff2":
		return "font/woff2"
	case ".woff":
		return "font/woff"
	}
	return "application/octet-stream"
}
