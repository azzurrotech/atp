package atp

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// Icon is one entry of a generated web app manifest's icon list.
type Icon struct {
	Src     string `json:"src"`
	Sizes   string `json:"sizes,omitempty"`
	Type    string `json:"type,omitempty"`
	Purpose string `json:"purpose,omitempty"`
}

// App is the metadata atp uses to host an application and to generate its
// client-app plumbing (manifest, service worker, OpenSearch).
type App struct {
	Name            string   `json:"name"`
	Title           string   `json:"title,omitempty"`
	ShortName       string   `json:"short_name,omitempty"`
	Description     string   `json:"description,omitempty"`
	ThemeColor      string   `json:"theme_color,omitempty"`
	BackgroundColor string   `json:"background_color,omitempty"`
	Display         string   `json:"display,omitempty"`
	StartURL        string   `json:"start_url,omitempty"`
	Scope           string   `json:"scope,omitempty"`
	Icons           []Icon   `json:"icons,omitempty"`
	Search          string   `json:"search,omitempty"`       // {searchTerms} target for OpenSearch
	Suggestions     string   `json:"suggestions,omitempty"`  // application/x-suggestions+json target
	Wasm            string   `json:"wasm,omitempty"`         // module to preload
	Preload         []string `json:"preload,omitempty"`      // extra resource hints
	SearchTitle     string   `json:"search_title,omitempty"` // OpenSearch ShortName override
}

// Apps is the registry of hosted applications. Applications are directories
// under <root>/apps; metadata can be supplied programmatically or via an
// app-level .atp.json.
type Apps struct {
	mu     sync.RWMutex
	dir    string
	byName map[string]App
	order  []string
}

func newApps(dir string) *Apps {
	return &Apps{dir: dir, byName: map[string]App{}}
}

// Dir returns the applications directory.
func (a *Apps) Dir() string { return a.dir }

// Register adds or replaces an application's metadata.
func (a *Apps) Register(app App) error {
	if !isToken(app.Name) {
		return fmt.Errorf("atp: invalid application name %q", app.Name)
	}
	app = withDefaults(app)
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, ok := a.byName[app.Name]; !ok {
		a.order = append(a.order, app.Name)
	}
	a.byName[app.Name] = app
	return nil
}

// Get returns an application's metadata.
func (a *Apps) Get(name string) (App, bool) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	app, ok := a.byName[name]
	return app, ok
}

// List returns the applications in registration order.
func (a *Apps) List() []App {
	a.mu.RLock()
	defer a.mu.RUnlock()
	out := make([]App, 0, len(a.order))
	for _, n := range a.order {
		out = append(out, a.byName[n])
	}
	return out
}

// Names returns the registered application names.
func (a *Apps) Names() []string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return append([]string(nil), a.order...)
}

// Discover registers any application directory not already known, reading an
// app-level .atp.json when present, and creates the directory for any
// registered application that lacks one.
func (a *Apps) Discover() error {
	entries, err := os.ReadDir(a.dir)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if !isToken(name) || strings.HasPrefix(name, ".") {
			continue
		}
		app := App{Name: name, Title: name}
		if data, err := os.ReadFile(filepath.Join(a.dir, name, ".atp.json")); err == nil {
			_ = json.Unmarshal(data, &app)
			app.Name = name
		}
		if _, ok := a.Get(name); ok {
			continue
		}
		_ = a.Register(app)
	}
	// Make sure every registered app has a directory to serve from.
	for _, name := range a.Names() {
		_ = os.MkdirAll(filepath.Join(a.dir, name), 0o755)
	}
	return nil
}

// Enabled reports whether a directory for the app exists.
func (a *Apps) Enabled(name string) bool {
	st, err := os.Stat(filepath.Join(a.dir, name))
	return err == nil && st.IsDir()
}

func withDefaults(app App) App {
	if app.Title == "" {
		app.Title = app.Name
	}
	if app.ShortName == "" {
		app.ShortName = app.Title
	}
	if app.Display == "" {
		app.Display = "standalone"
	}
	if app.ThemeColor == "" {
		app.ThemeColor = "#0a84ff"
	}
	if app.BackgroundColor == "" {
		app.BackgroundColor = "#ffffff"
	}
	if app.StartURL == "" {
		app.StartURL = "/" + app.Name + "/"
	}
	if app.Scope == "" {
		app.Scope = "/" + app.Name + "/"
	}
	if app.Search == "" {
		app.Search = "/" + app.Name + "/?q={searchTerms}"
	}
	if app.SearchTitle == "" {
		app.SearchTitle = shortName(app.Title)
	}
	if len(app.Icons) == 0 {
		app.Icons = []Icon{{Src: "/" + app.Name + "/icon.svg", Sizes: "any", Type: "image/svg+xml", Purpose: "any"}}
	}
	return app
}

func shortName(s string) string {
	if len(s) <= 16 {
		return s
	}
	return strings.TrimSpace(s[:16])
}

// appForPath resolves the application a request path belongs to (silo or
// generated capability).
func (s *Server) appForPath(p string) string {
	if app, _, ok := s.capabilityMatch(p); ok {
		return app
	}
	seg := firstSegment(p)
	if _, ok := s.apps.Get(seg); ok {
		return seg
	}
	return ""
}

// capabilityMatch reports whether a path names a generated capability file for
// a known application and the application does not ship that file itself.
func (s *Server) capabilityMatch(p string) (app, kind string, ok bool) {
	trimmed := strings.TrimPrefix(p, "/")
	var tail string
	if strings.HasPrefix(trimmed, "apps/") {
		trimmed = strings.TrimPrefix(trimmed, "apps/")
	}
	i := strings.IndexByte(trimmed, '/')
	if i < 0 {
		return "", "", false
	}
	app = trimmed[:i]
	tail = trimmed[i+1:]
	switch tail {
	case "manifest.webmanifest":
		kind = "manifest"
	case "sw.js":
		kind = "sw"
	case "opensearch.xml":
		kind = "opensearch"
	default:
		return "", "", false
	}
	if _, ok := s.apps.Get(app); !ok {
		return "", "", false
	}
	// An app that ships its own file keeps it: let song serve it.
	if fileExists(filepath.Join(s.apps.Dir(), app, tail)) {
		return "", "", false
	}
	return app, kind, true
}

func (s *Server) serveCapability(w http.ResponseWriter, r *http.Request, app, kind string) {
	meta, _ := s.apps.Get(app)
	switch kind {
	case "manifest":
		w.Header().Set("Content-Type", "application/manifest+json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(manifestOf(meta))
	case "sw":
		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write([]byte(serviceWorker(meta, s.appShellFiles(app))))
	case "opensearch":
		w.Header().Set("Content-Type", "application/opensearchdescription+xml; charset=utf-8")
		_, _ = w.Write([]byte(opensearch(meta)))
	default:
		http.NotFound(w, r)
	}
}

// capabilityFragments returns the head fragments atp generates for an app:
// the manifest link, the OpenSearch autodiscovery link and resource hints.
func (s *Server) capabilityFragments(app string) []string {
	if app == "" {
		return nil
	}
	meta, ok := s.apps.Get(app)
	if !ok {
		return nil
	}
	base := "/" + app
	var frags []string
	if !fileExists(filepath.Join(s.apps.Dir(), app, "manifest.webmanifest")) {
		frags = append(frags, `<link rel="manifest" href="`+base+`/manifest.webmanifest">`)
	}
	if !fileExists(filepath.Join(s.apps.Dir(), app, "opensearch.xml")) {
		frags = append(frags, `<link rel="search" type="application/opensearchdescription+xml" title="`+
			xmlEscape(meta.SearchTitle)+`" href="`+base+`/opensearch.xml">`)
	}
	frags = append(frags, `<meta name="theme-color" content="`+meta.ThemeColor+`">`)
	for _, p := range meta.Preload {
		frags = append(frags, `<link rel="preload" href="`+p+`" as="fetch" crossorigin>`)
	}
	if meta.Wasm != "" {
		frags = append(frags, `<link rel="preload" href="`+meta.Wasm+`" as="fetch" type="application/wasm" crossorigin>`)
	}
	return frags
}

// appShellFiles lists the app's own static files (relative paths) for the
// generated service worker's precache list.
func (s *Server) appShellFiles(app string) []string {
	var out []string
	root := filepath.Join(s.apps.Dir(), app)
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if rel == "sw.js" || rel == "manifest.webmanifest" || rel == "opensearch.xml" {
			return nil
		}
		out = append(out, "/"+app+"/"+rel)
		return nil
	})
	sort.Strings(out)
	return out
}

func manifestOf(a App) map[string]any {
	m := map[string]any{
		"name":             a.Title,
		"short_name":       a.ShortName,
		"start_url":        a.StartURL,
		"scope":            a.Scope,
		"display":          a.Display,
		"theme_color":      a.ThemeColor,
		"background_color": a.BackgroundColor,
		"icons":            a.Icons,
	}
	if a.Description != "" {
		m["description"] = a.Description
	}
	return m
}

// OpenSearchDescription is the XML document atp generates per application.
type OpenSearchDescription struct {
	XMLName     xml.Name `xml:"OpenSearchDescription"`
	XMLNS       string   `xml:"xmlns,attr"`
	ShortName   string   `xml:"ShortName"`
	Description string   `xml:"Description"`
	InputEnc    string   `xml:"InputEncoding"`
	Image       struct {
		Width  int    `xml:"width,attr"`
		Height int    `xml:"height,attr"`
		Type   string `xml:"type,attr"`
		URL    string `xml:",chardata"`
	} `xml:"Image"`
	Urls []openSearchURL `xml:"Url"`
}

type openSearchURL struct {
	Type     string `xml:"type,attr"`
	Template string `xml:"template,attr"`
	Rel      string `xml:"rel,attr,omitempty"`
}

func opensearch(a App) string {
	doc := OpenSearchDescription{
		XMLNS:       "http://a9.com/-/spec/opensearch/1.1/",
		ShortName:   a.SearchTitle,
		Description: a.Title + " search",
		InputEnc:    "UTF-8",
	}
	doc.Image.Width = 16
	doc.Image.Height = 16
	doc.Image.Type = "image/svg+xml"
	doc.Image.URL = "/" + a.Name + "/icon.svg"
	doc.Urls = append(doc.Urls, openSearchURL{Type: "text/html", Template: a.Search})
	if a.Suggestions != "" {
		doc.Urls = append(doc.Urls, openSearchURL{Type: "application/x-suggestions+json", Template: a.Suggestions})
	}
	doc.Urls = append(doc.Urls, openSearchURL{
		Type:     "application/opensearchdescription+xml",
		Template: "/" + a.Name + "/opensearch.xml",
		Rel:      "self",
	})
	out, _ := xml.MarshalIndent(doc, "", "  ")
	return xml.Header + string(out) + "\n"
}

func serviceWorker(a App, shell []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "// generated by atp for application %q\n", a.Name)
	fmt.Fprintf(&b, "const CACHE = 'atp-%s-v1';\n", a.Name)
	b.WriteString("const SHELL = [\n")
	for _, f := range shell {
		fmt.Fprintf(&b, "  %q,\n", f)
	}
	b.WriteString("];\n")
	b.WriteString(`self.addEventListener('install', (e) => {
  e.waitUntil(caches.open(CACHE).then((c) => c.addAll(SHELL)).then(() => self.skipWaiting()));
});
self.addEventListener('activate', (e) => {
  e.waitUntil(self.clients.claim());
});
self.addEventListener('fetch', (e) => {
  const req = e.request;
  if (req.method !== 'GET') return;
  const url = new URL(req.url);
  if (url.origin !== self.location.origin) return;
  // functions and records are network-first
  if (url.pathname.includes('/api/') || url.pathname.startsWith('/pod/')) {
    e.respondWith(fetch(req).catch(() => caches.match(req)));
    return;
  }
  // the app shell is cache-first
  e.respondWith(caches.match(req).then((hit) => hit || fetch(req).then((res) => {
    const copy = res.clone();
    caches.open(CACHE).then((c) => c.put(req, copy));
    return res;
  }).catch(() => caches.match('/` + a.Name + `/'))));
});
`)
	return b.String()
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

func xmlEscape(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}
