package atp

import (
	"bytes"
	"html"
	"strings"
	"sync"
)

// Head is the central manager for the <head> of every HTML document atp serves.
// Fragments are kept in insertion order, de-duplicated, and injected before
// </head>. Global fragments apply to every application; per-application
// fragments apply to one.
type Head struct {
	mu       sync.RWMutex
	global   []string
	perApp   map[string][]string
	title    string
	titleFor map[string]string
}

func newHead(title string) *Head {
	return &Head{title: title, perApp: map[string][]string{}, titleFor: map[string]string{}}
}

// Add registers a global head fragment (e.g. a <meta> or <link>).
func (h *Head) Add(fragment string) { h.AddFor("", fragment) }

// AddFor registers a head fragment for one application. An empty app adds a
// global fragment.
func (h *Head) AddFor(app, fragment string) {
	fragment = strings.TrimSpace(fragment)
	if fragment == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if app == "" {
		h.global = appendUnique(h.global, fragment)
		return
	}
	h.perApp[app] = appendUnique(h.perApp[app], fragment)
}

// Remove drops a global fragment. It reports whether it existed.
func (h *Head) Remove(fragment string) bool { return h.RemoveFor("", fragment) }

// RemoveFor drops a per-application (or global, when app is empty) fragment.
func (h *Head) RemoveFor(app, fragment string) bool {
	fragment = strings.TrimSpace(fragment)
	h.mu.Lock()
	defer h.mu.Unlock()
	if app == "" {
		var found bool
		h.global, found = removeString(h.global, fragment)
		return found
	}
	var found bool
	h.perApp[app], found = removeString(h.perApp[app], fragment)
	return found
}

// Title sets the default document title.
func (h *Head) Title(t string) { h.TitleFor("", t) }

// TitleFor sets the default document title for one application.
func (h *Head) TitleFor(app, t string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if app == "" {
		h.title = t
		return
	}
	h.titleFor[app] = t
}

// TitleValue returns the effective title for an app (per-app override, then the
// global default).
func (h *Head) TitleValue(app string) string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if app != "" {
		if t, ok := h.titleFor[app]; ok {
			return t
		}
	}
	return h.title
}

// Fragments returns the ordered, de-duplicated fragments for an app.
func (h *Head) Fragments(app string) []string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]string, 0, len(h.global)+len(h.perApp[app]))
	out = appendUnique(out, h.global...)
	if app != "" {
		out = appendUnique(out, h.perApp[app]...)
	}
	return out
}

// GlobalFragments returns just the global fragments.
func (h *Head) GlobalFragments() []string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return append([]string(nil), h.global...)
}

// FragmentsFor returns just the per-application fragments.
func (h *Head) FragmentsFor(app string) []string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return append([]string(nil), h.perApp[app]...)
}

// Inject inserts the managed fragments (plus any extra app-generated ones) and
// the effective <title> into an HTML document.
func (h *Head) Inject(doc []byte, app string, extra ...string) []byte {
	frags := h.Fragments(app)
	frags = appendUnique(frags, extra...)
	title := h.TitleValue(app)

	var block bytes.Buffer
	hasTitle := bytes.Contains(bytes.ToLower(doc), []byte("<title"))
	if title != "" && !hasTitle {
		block.WriteString("<title>")
		block.WriteString(html.EscapeString(title))
		block.WriteString("</title>\n")
	}
	for _, f := range frags {
		block.WriteString(f)
		block.WriteByte('\n')
	}
	if block.Len() == 0 {
		return doc
	}
	ins := block.Bytes()

	lower := bytes.ToLower(doc)
	if i := bytes.Index(lower, []byte("</head>")); i >= 0 {
		out := make([]byte, 0, len(doc)+len(ins))
		out = append(out, doc[:i]...)
		out = append(out, ins...)
		out = append(out, doc[i:]...)
		return out
	}
	if i := bytes.Index(lower, []byte(">")); i >= 0 && bytes.HasPrefix(lower, []byte("<head")) {
		out := make([]byte, 0, len(doc)+len(ins))
		out = append(out, doc[:i+1]...)
		out = append(out, '\n')
		out = append(out, ins...)
		out = append(out, doc[i+1:]...)
		return out
	}
	out := make([]byte, 0, len(doc)+len(ins))
	out = append(out, ins...)
	out = append(out, doc...)
	return out
}

func appendUnique(dst []string, items ...string) []string {
	for _, it := range items {
		if it == "" {
			continue
		}
		dup := false
		for _, have := range dst {
			if have == it {
				dup = true
				break
			}
		}
		if !dup {
			dst = append(dst, it)
		}
	}
	return dst
}

func removeString(s []string, target string) ([]string, bool) {
	for i, v := range s {
		if v == target {
			return append(s[:i:i], s[i+1:]...), true
		}
	}
	return s, false
}
