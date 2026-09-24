package web

// This file renders atp's management web UI. The UI is plain HTML + vanilla
// ES6 JavaScript — no frameworks, no external assets — and talks to the same
// JSON API the admin UI uses. It does not duplicate any feature of song, pod
// or shepherd: every file/table/key the pages touch goes through the embedded
// services' own endpoints (scoped per client).

import (
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"strings"
)

//go:embed templates/*.html
var uiFS embed.FS

var uiFuncs = template.FuncMap{
	"json": func(v any) (template.JS, error) {
		b, err := json.Marshal(v)
		return template.JS(b), err
	},
}

// uiPages holds one template set per page. Each set contains the shared shell
// (base.html) plus exactly one page file, so the "content" block that page
// defines is unambiguous. A single parse of all files would collapse every
// page's "content" block into the last one parsed.
var uiPages = mustBuildPages()

func mustBuildPages() map[string]*template.Template {
	baseSrc, err := uiFS.ReadFile("templates/base.html")
	if err != nil {
		panic(fmt.Sprintf("read templates/base.html: %v", err))
	}
	entries, err := uiFS.ReadDir("templates")
	if err != nil {
		panic(err)
	}
	pages := map[string]*template.Template{}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".html") || name == "base.html" {
			continue
		}
		page := strings.TrimSuffix(name, ".html")
		src, err := uiFS.ReadFile("templates/" + name)
		if err != nil {
			panic(fmt.Sprintf("read templates/%s: %v", name, err))
		}
		t := template.New(name).Funcs(uiFuncs)
		if _, err := t.Parse(string(baseSrc)); err != nil {
			panic(fmt.Sprintf("parse base for %s: %v", name, err))
		}
		if _, err := t.Parse(string(src)); err != nil {
			panic(fmt.Sprintf("parse %s: %v", name, err))
		}
		pages[page] = t
	}
	return pages
}

// renderPage executes the shared shell (base.html) with a page's "content"
// block. The set is selected per page; see uiPages above.
func (s *ATPService) renderPage(w http.ResponseWriter, r *http.Request, page string, data map[string]any) {
	if s.cfg.DisableUI {
		http.Error(w, "web UI is disabled", http.StatusNotFound)
		return
	}
	tpl := uiPages[page]
	if tpl == nil {
		http.Error(w, "unknown page: "+page, http.StatusInternalServerError)
		return
	}
	if data == nil {
		data = map[string]any{}
	}
	data["Version"] = Version
	data["Path"] = r.URL.Path
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tpl.ExecuteTemplate(w, "templates/base.html", data); err != nil {
		http.Error(w, "render error: "+err.Error(), http.StatusInternalServerError)
	}
}

// ---- top-level pages --------------------------------------------------------

func (s *ATPService) handleClientsPage(w http.ResponseWriter, r *http.Request) {
	s.renderPage(w, r, "clients", map[string]any{
		"Title": "ATP — Clients", "Active": "clients", "UserName": s.cfg.AdminUser,
	})
}

func (s *ATPService) handleClientPage(w http.ResponseWriter, r *http.Request) {
	id := pathValue(r, "id")
	c, err := s.clients.Get(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	s.renderPage(w, r, "client", map[string]any{
		"Title": "ATP — " + c.Name, "Active": "client",
		"ClientID": c.ID, "Client": c, "UserName": s.cfg.AdminUser,
	})
}

func (s *ATPService) handleClientFilesPage(w http.ResponseWriter, r *http.Request) {
	id := pathValue(r, "id")
	c, err := s.clients.Get(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	s.renderPage(w, r, "files", map[string]any{
		"Title": "Files — " + c.Name, "Active": "files",
		"ClientID": c.ID, "Client": c, "UserName": s.cfg.AdminUser,
	})
}

func (s *ATPService) handleClientTablesPage(w http.ResponseWriter, r *http.Request) {
	id := pathValue(r, "id")
	c, err := s.clients.Get(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	s.renderPage(w, r, "tables", map[string]any{
		"Title": "Tables — " + c.Name, "Active": "tables",
		"ClientID": c.ID, "Client": c, "UserName": s.cfg.AdminUser,
	})
}

func (s *ATPService) handleClientSecurityPage(w http.ResponseWriter, r *http.Request) {
	id := pathValue(r, "id")
	c, err := s.clients.Get(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	s.renderPage(w, r, "security", map[string]any{
		"Title": "Security — " + c.Name, "Active": "security",
		"ClientID": c.ID, "Client": c, "UserName": s.cfg.AdminUser,
	})
}

func (s *ATPService) handleClientUsagePage(w http.ResponseWriter, r *http.Request) {
	id := pathValue(r, "id")
	c, err := s.clients.Get(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	s.renderPage(w, r, "usage", map[string]any{
		"Title": "Usage — " + c.Name, "Active": "usage",
		"ClientID": c.ID, "Client": c, "UserName": s.cfg.AdminUser,
	})
}

func (s *ATPService) handleClientSecretsPage(w http.ResponseWriter, r *http.Request) {
	id := pathValue(r, "id")
	c, err := s.clients.Get(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	s.renderPage(w, r, "secrets", map[string]any{
		"Title": "Secrets — " + c.Name, "Active": "secrets",
		"ClientID": c.ID, "Client": c, "UserName": s.cfg.AdminUser,
	})
}

// clientNav builds the shared per-client navigation block (used by the base
// template when .ClientID is present).