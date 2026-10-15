package atp

import (
	"html/template"
	"net/http"
)

// viewCSS is the stylesheet for the management view, served at /_atp/view.css.
const viewCSS = `:root{color-scheme:light dark;--bg:#f7f8fa;--fg:#1c1e21;--card:#fff;--line:#e3e6ea;--accent:#0a84ff}
@media (prefers-color-scheme:dark){:root{--bg:#111315;--fg:#e7e9ec;--card:#1b1e21;--line:#2b3035}}
*{box-sizing:border-box}
body{margin:0;font:16px/1.5 system-ui,-apple-system,Segoe UI,Roboto,sans-serif;background:var(--bg);color:var(--fg)}
header{padding:1.5rem 2rem;border-bottom:1px solid var(--line)}
h1{margin:0;font-size:1.4rem}
main{padding:1.5rem 2rem;display:grid;gap:1.25rem;grid-template-columns:repeat(auto-fit,minmax(320px,1fr))}
section{background:var(--card);border:1px solid var(--line);border-radius:10px;padding:1rem 1.25rem}
h2{margin:0 0 .75rem;font-size:1rem;text-transform:uppercase;letter-spacing:.04em;color:var(--accent)}
ul{margin:0;padding-left:1.1rem}
li{margin:.15rem 0}
code{background:rgba(127,127,127,.15);padding:.1rem .35rem;border-radius:4px;font-size:.9em}
a{color:var(--accent)}
.muted{opacity:.7;font-size:.9rem}
`

var viewTmpl = template.Must(template.New("view").Parse(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}}</title>
<link rel="stylesheet" href="/_atp/view.css">
</head>
<body>
<header>
<h1>{{.Title}}</h1>
<p class="muted">atp {{.Version}} · root <code>{{.Root}}</code>{{if .Encrypted}} · encryption at rest <strong>on</strong>{{end}}</p>
</header>
<main>
<section>
<h2>Applications</h2>
{{if .Apps}}<ul>{{range .Apps}}<li><a href="/{{.Name}}/">{{.Title}}</a> <span class="muted">({{.Name}})</span></li>{{end}}</ul>
{{else}}<p class="muted">No hosted applications yet. Add a directory under <code>apps/</code>.</p>{{end}}
</section>
<section>
<h2>Registered functions</h2>
{{if .Functions}}<ul>{{range .Functions}}<li><code>{{.Method}} {{.Pattern}}</code>{{if not .Enabled}} <span class="muted">(disabled)</span>{{end}}</li>{{end}}</ul>
{{else}}<p class="muted">None.</p>{{end}}
</section>
<section>
<h2>Secrets</h2>
{{if .SecretApps}}<ul>{{range .SecretApps}}<li><a href="/secrets/{{.}}">{{.}}</a></li>{{end}}</ul>
{{else}}<p class="muted">No stored secrets.</p>{{end}}
</section>
<section>
<h2>Static silos (song)</h2>
{{if .Silos}}<ul>{{range .Silos}}<li><a href="/{{.}}/">{{.}}</a></li>{{end}}</ul>
{{else}}<p class="muted">None.</p>{{end}}
</section>
<section>
<h2>Encrypted assets</h2>
{{if .Box}}<ul>{{range .Box}}<li><a href="/_atp/static/{{.}}">{{.}}</a></li>{{end}}</ul>
{{else}}<p class="muted">None.</p>{{end}}
</section>
<section>
<h2>Gateway upstreams</h2>
{{if .Upstreams}}<ul>{{range .Upstreams}}<li><code>{{.Prefix}}</code> → {{.Target}}</li>{{end}}</ul>
{{else}}<p class="muted">No upstreams configured.</p>{{end}}
</section>
<section>
<h2>Platform surfaces</h2>
<ul>
<li><a href="/secrets">/secrets</a> — the secrets vault</li>
<li><a href="/head">/head</a> — the head manager</li>
<li><a href="/headers">/headers</a> — the header policy</li>
<li><a href="/api/functions">/api/functions</a> — the function registry</li>
<li><a href="/pod/">/pod/</a> — the pod XML database</li>
<li><a href="/gw/">/gw/</a> — the shepherd gateway</li>
<li><code>/api/…</code> — shepherd capability functions</li>
</ul>
</section>
</main>
</body>
</html>
`))

func (s *Server) renderView(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	silos, _ := s.song.Store().ListSilos()
	data := struct {
		Title      string
		Version    string
		Root       string
		Encrypted  bool
		Apps       []App
		Functions  []Route
		SecretApps []string
		Silos      []string
		Box        []string
		Upstreams  any
	}{
		Title:      orDefault(s.head.TitleValue(""), "atp — AzzurroTech Platform"),
		Version:    Version,
		Root:       s.root,
		Encrypted:  s.encrypted,
		Apps:       s.apps.List(),
		Functions:  s.registry.Routes(),
		SecretApps: s.vault.Apps(),
		Silos:      silos,
		Box:        s.box.List(),
		Upstreams:  s.opts.Upstreams,
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = viewTmpl.Execute(w, data)
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
