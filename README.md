# atp — AzzurroTech Platform

**MIT License © Azzurro Technology Inc.** · module `azzurrotech/atp` · **Go standard library
only**, plus the three first-party modules **song**, **pod** and **shepherd**.

atp is a **modified Go fileserver**. On top of ordinary static file serving it adds the
machinery to **create and manage secrets and serverless functions**, so that an application
made of plain **HTML, CSS and JavaScript** plus **custom Go serverless functions** can be
built and deployed as **one application** on **one server**. It is the same idea as a
self-hosted Supabase: a single process that gives an identity-free web application a place to
live, a place to keep its secrets, a place to run its server-side code, and a set of managed
interfaces over the platform's own services.

Everything atp does is available **from other Go projects**: atp is imported, not just run,
and Go functions are registered against specific HTTP verbs and endpoints through a small
public API — so the platform is **expandable** without forking it.

---

## What atp is

- **A modified fileserver.** Requests are served from a root directory exactly as an
  `http.FileServer` would, except that atp intercepts the paths it owns: hosted applications,
  registered functions, the secrets vault, the head and header managers, and the management
  view. Everything else is served verbatim.
- **A secret manager.** Named secrets are stored per application, encrypted at rest, read
  back on demand by registered Go functions, and shared with an expiring, revocable link.
- **A serverless function host.** Go functions are registered to an HTTP verb and an
  endpoint (`GET /api/hello`, `POST /hooks/stripe`, …). They run in-process, get the platform
  services injected, and are served as part of the hosted application.
- **A single application deployment platform.** An application is a directory of HTML, CSS
  and JavaScript, a set of registered Go functions, and a set of secrets — deployed together
  and served together.
- **A managed view over the whole stack.** Every feature of **song** (static hosting,
  templates, Web Components), **pod** (the file-backed XML database) and **shepherd**
  (identity-free IAM, firewall, rate limiting, gateway) is exposed through the atp view, so
  one server presents all of them through one address.
- **A client-application builder.** For each hosted application atp **generates the
  web-platform plumbing** — an installable Progressive Web App (manifest + service worker +
  offline shell), an OpenSearch search provider, media delivery, WebAssembly hosting, and the
  privacy, performance and security header policy — so the author writes HTML, CSS and
  JavaScript and gets a modern client app without hand-writing the boilerplate. See
  [Client application capabilities](#client-application-capabilities).
- **Identity-free by construction.** No accounts, no user store. Access to hosted resources
  needs no login at all, and where a capability is wanted it is a shepherd token that carries
  *what may be done*, never *who* is doing it.

---

## Design constraints (non-negotiable)

These are the rules a change to atp must respect:

1. **Dependencies are exactly `song`, `pod` and `shepherd`.** Nothing else may be imported.
2. **Everything else is the Go standard library** plus **baseline HTML, CSS and JavaScript**
   on the client. No frameworks, no bundlers, no CDNs, no third-party modules anywhere.
3. **No added security is required to access resources hosted within the server.** A hosted
   application, its files and its registered functions are reachable without authentication.
   Any capability layer is *opt-in* for the application author, built from shepherd, and never
   imposed on the fileserver itself.
4. **Static and dynamic content can be encrypted at rest.** Static files (song) and record
   values (pod) are encrypted transparently; the on-disk form never holds plaintext.
5. **One process, one binary.** atp embeds song, pod and shepherd in-process — no sidecars, no
   proxy hops, no separate services to run.
6. **The three submodules stay dependency-free.** `atp/go.mod` therefore adds no third-party
   `require` lines of its own.
7. **The web platform is the substrate.** atp is positioned as a stack for **Go web
   development** ([go.dev/solutions/webdev](https://go.dev/solutions/webdev)): the server is
   `net/http`, templates are `html/template`/`text/template`, data is `encoding/json` /
   `encoding/xml` / the `database/sql` driver, and crypto is `crypto/*`. On the client it
   automates the platform capabilities an application needs — **Progressive Web Apps**,
   **OpenSearch**, **media**, **WebAssembly**, **privacy**, **performance** and **security**
   — rather than requiring a framework to approximate them.

---

## How it fits together

| Module | Role | What atp adds on top |
|---|---|---|
| **song** | siloed static hosting, Go template processing, Web Components | served through the atp view; the head manager injects into every hosted document |
| **pod** | HTML-form → XML file database, uploads, optional encryption, link sharing | served through the atp view; secrets and dynamic content are encrypted at rest |
| **shepherd** | identity-free capability tokens, firewall, rate limiting, gateway | served through the atp view; backs the optional access layer for hosted apps |
| **atp** | the modified fileserver, the function registry, the secrets vault, head/header management, deployment | ties the three together into one address |

song, pod and shepherd are **git submodules of atp** and are compiled **into** it. atp does
not proxy them; it mounts their handlers, so a single `go build` produces a single binary.

```text
                 ┌────────────────────────── atp (one process) ──────────────────────────┐
   client ──────▶│  fileserver  →  function registry  →  head manager  →  header policy   │
                 │      │                 │                                                 │
                 │      ├── song     (static + templates + components, encrypted files)   │
                 │      ├── pod      (form → XML database, encrypted values, shares)      │
                 │      └── shepherd (capabilities, firewall, rate limit, gateway)        │
                 │                                                                            │
                 │  secrets vault   ·   function registry   ·   management view           │
                 └────────────────────────────────────────────────────────────────────────────┘
```

---

## The modified fileserver

atp is one `http.Handler`. A request is resolved in this order:

1. **Registered function?** If a Go function was registered for this *method + endpoint*, it
   runs. This is the serverless path and it wins over static content.
2. **Platform path?** `/secrets/…`, `/head/…`, `/headers/…`, `/song/…`, `/pod/…`,
   `/shepherd/…` are served by the corresponding manager or embedded module.
3. **Hosted application?** The first path segment names an application; the rest resolves
   inside that application's directory (index document, template route, encrypted file).
4. **Static file.** Anything left is served from the server root as an ordinary file.
5. **404.**

Because atp is a *modified* fileserver, steps 3 and 4 behave exactly like `http.FileServer`
(index resolution, content types, range requests, conditional requests); steps 1 and 2 are
the additions.

---

## Registering Go functions — the endpoint & function hosting interface

This is how atp is **expanded**: any Go program that imports atp can attach its own
serverless functions to a verb and an endpoint. There is no code generation and no plugin
build.

```go
package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"

	"azzurrotech/atp"
)

func main() {
	srv, err := atp.New(atp.Options{
		Root:   "./data",              // hosted apps + stores + vault
		Addr:   ":8080",               // ignored in middleware mode
		Secret: os.Getenv("ATP_SECRET"), // >= 32 bytes: vault key + token signing
	})
	if err != nil {
		log.Fatal(err)
	}

	// A serverless function bound to one HTTP verb and one endpoint.
	srv.Handle("GET", "/api/time", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"now": nowUnix()})
	})

	// The handler receives the platform, so it can read secrets and reach the
	// embedded services directly.
	srv.Handle("POST", "/api/charge", func(w http.ResponseWriter, r *http.Request) {
		key, ok := srv.Secrets().Get("stripe_key")
		if !ok {
			http.Error(w, "not configured", http.StatusPreconditionFailed)
			return
		}
		_ = key
		w.WriteHeader(http.StatusAccepted)
	})

	log.Fatal(http.ListenAndServe(srv.Addr(), srv))
}
```

- **Verb + endpoint.** `srv.Handle(method, pattern, fn)` and `srv.HandleFunc(method, pattern, fn)`
  bind a function to exactly one method and one path. `method` accepts a single verb or `*`
  for any verb; `pattern` supports a trailing `{name}` segment for path parameters.
- **`http.Handler` shaped, standard library only.** A registered function is an ordinary
  `http.HandlerFunc`, so it composes with everything in `net/http`.
- **Platform access.** A host program keeps the `*atp.Server` and uses it: `srv.Secrets()`,
  `srv.Head()`, `srv.Headers()`, `srv.Song()`, `srv.Pod()`, `srv.Shepherd()`.
- **Registered at runtime, too.** The management view exposes the registry over HTTP, so
  functions can also be listed, grouped and enabled/disabled on a running server. New *Go*
  code is added by importing atp and registering it; the registry is the seam that makes that
  a one-line change.

`atp.Handle` returns an error for a malformed pattern or a duplicate `method+pattern`, so a
mistake is caught at start-up rather than turning into a silent shadow.

---

## Using atp from another Go project

atp has two modes, and they are not exclusive.

**Server mode** — `go build` produces one binary that serves the whole platform:

```sh
go build -o atp .
./atp --addr :8080 --root ./data --secret "$ATP_SECRET"
```

**Middleware mode** — import atp and mount it inside a Go server you already have. atp serves
the platform paths and hosted applications and delegates everything else to your handler:

```go
srv, err := atp.New(atp.Options{Root: "./data", Secret: os.Getenv("ATP_SECRET")})
if err != nil {
	log.Fatal(err)
}

mux := http.NewServeMux()
mux.Handle("/platform/", http.StripPrefix("/platform", srv)) // atp under a prefix
mux.Handle("/", myOwnHandler)                                // your existing server
log.Fatal(http.ListenAndServe(":8080", mux))
```

`srv.Middleware(next http.Handler) http.Handler` is the one-liner version: atp handles what it
owns and calls `next` for everything else, so a host server can adopt atp without reorganising
its routes.

---

## Where the middleware files live, and how they are used

atp does **not** re-implement song, pod or shepherd. It imports each module's **middleware**
entry point and mounts it **in-process**, which is why one binary serves all three. These are
the exact files atp uses, with the constructor and the method it calls:

| Module | Middleware file | Entry point | How atp uses it |
|---|---|---|---|
| **song** | `song/pkg/song/song.go` | `song.New(song.Config{Root, Funcs, Components})` → `(*Song).Middleware(next)` (or `.Handler()`) | serves hosted applications, silos, templates and `/_song/…`; delegates every path it does not own to `next` (atp's file server) |
| **pod** | `pod/handler.go` | `pod.NewHandler(pod.HandlerOptions{Store, Mount, UI})` → `*Handler`, an `http.Handler` | mounted under `/pod/…` (and per-app `…/api/pod`), giving the form→XML database surface |
| **pod** (store) | `pod/store.go` | `pod.Open(base)` → `*Store` | the shared store + index the pod handler and the vault's records are built on |
| **pod** (auth) | `pod/auth.go` | `pod.NewAuth(pod.AuthOptions{…})` → `(*Auth).Wrap(next)` | the opt-in request gate/policy around pod for an app that wants one |
| **shepherd** | `shepherd/middleware/middleware.go` | `middleware.New(middleware.Config{…})` → `*Shepherd` | the capability engine exposed at `/api/…` |
| **shepherd** (wrappers) | `shepherd/middleware/middleware.go` | `.Firewall`, `.RateLimit`, `.Authenticate`, `.Optional`, `.Authorize`, `.Gate`, `.Enhance`, `.MagicLinkHandler` | the opt-in request pipeline wrapped around apps and functions |
| **shepherd** (gateway) | `shepherd/middleware/gateway.go` | `.Gateway(middleware.GatewayOptions{Mount, Upstreams})` | the enriching, signing `/gw/…` proxy |
| **shepherd** (debounce) | `shepherd/middleware/debounce.go` | `.Debounce(window, maxInflight)` | duplicate/replay suppression |
| **shepherd** (provider) | `shepherd/provider/provider.go` (the `Provider` interface, `Principal`, `New(m, opts…)`, `FromCapability(m)`) + `shepherd/middleware/middleware.go` (`.Provider()`) | `(*Shepherd).Provider()` → `provider.Provider`; `provider.FromCapability(m)`; `provider.New(m, WithDeny…, WithLimiter…)` | the identity-free provider that pod's `AuthOptions.Authenticate` accepts (`Authenticate(r) (Principal, error)` + `Wrap(h) http.Handler`) |

### How atp wires them together

atp builds the three middleware **once at start-up** and composes them into its own
`ServeHTTP`. The order is fixed:

```go
// build once, at start-up
songMW := songSvc.Middleware(fileSrv)               // song/pkg/song/song.go
podMW  := pod.NewHandler(pod.HandlerOptions{        // pod/handler.go
    Store: podStore,                                // pod.Open(root)  — pod/store.go
    Mount: "/pod",
})
shep   := shepherdSvc                               // shepherd/middleware/middleware.go

// serve: registered functions > platform managers > pod > song/apps > fileserver
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
    switch {
    case s.registry.Match(r):                    s.registry.Serve(w, r) // Go function (verb + endpoint)
    case s.ownsPath(r.URL.Path):                 s.managers.Serve(w, r) // secrets / head / headers
    case strings.HasPrefix(r.URL.Path, "/pod/"): podMW.ServeHTTP(w, r) // pod middleware
    default:                                     songMW.ServeHTTP(w, r) // song middleware (apps + files)
    }
}
```

- **song is the outermost of the embedded three.** It owns the application and static space
  and hands everything it does not own to the `http.FileServer` passed as `next` — exactly the
  composition its own README documents.
- **pod is consulted only for its mount**, the same "middleware next to a `http.FileServer`"
  shape pod describes for itself.
- **shepherd is opt-in and wraps the whole chain** when an application enables the capability
  layer: `shep.Firewall(...)` → `shep.RateLimit(...)` → `shep.Authenticate(...)` →
  `shep.Authorize(scopes...)` → the handler selected above. With the layer off, **no shepherd
  middleware is in the path** and hosted resources stay open, as the access model requires.
- **The reference wirings are the modules' own examples**: `song/examples/middleware/main.go`
  (song embedded in a host server), the FileServer composition in `pod/README.md`, and
  `shepherd/examples/middleware-demo/main.go` (gated inline API + gateway + magic link).
- **atp never imports the server binaries** (`song/main.go`, `pod/cmd/pod/main.go`,
  `shepherd/main.go`, `shepherd/server.go`). Only the middleware files above are used, which
  is what lets one process host all three.

---

## Hosted applications: HTML, CSS and JavaScript

An application is a directory under the root. Its front end is **nothing but baseline web
platform pieces** — semantic HTML, plain CSS and vanilla JavaScript (ES modules) using
`fetch`, `localStorage` and standard cookies. There is no framework and no build step.

```text
data/apps/notes/
├── index.html          served at /notes/
├── style.css           served at /notes/style.css
├── app.js              served at /notes/app.js
└── (functions live in Go, not here)
```

The application consumes the platform from the browser:

```html
<script type="module">
  // a registered Go function: GET /notes/api/list
  const rows = await (await fetch('/notes/api/list')).json();
  // the secrets/records/capability helpers are all plain fetch() calls
</script>
```

- **Static content** is served by song from the application's silo, with template processing
  and Web Components available when the author wants them.
- **Dynamic content** is served by registered Go functions and by pod records; both may be
  encrypted at rest.
- **Templates** are processed server-side with `text/template` / `html/template`; **Web
  Components** registered in the system can be used from any hosted HTML file.

---

## Client application capabilities

An application's source stays **HTML, CSS and JavaScript**. Everything else a modern client
app expects is **generated and served by atp** from the application's metadata, so an app is
easily and automatically built and the author never hand-writes the plumbing. Each subsection
below names the web-platform documentation atp implements.

### Progressive web apps

Reference: [Progressive web apps](https://developer.mozilla.org/en-US/docs/Web/Progressive_web_apps).

- **Web app manifest, generated.** atp serves `/apps/{app}/manifest.webmanifest` from the
  app's metadata — `name`, `short_name`, `description`, `start_url`, `scope`, `display`,
  `theme_color`, `background_color`, `icons`, `shortcuts`, `share_target` — and injects
  `<link rel="manifest" href="…">` through the head manager.
- **Service worker, generated.** atp serves `/apps/{app}/sw.js`: it precaches the application
  shell (the app's HTML, CSS and JS) and serves it cache-first, while sending functions and
  pod records network-first with an offline fallback. An app that ships its own `sw.js` keeps
  it — atp serves theirs and skips generation.
- **Installable, standalone, offline.** Manifest + service worker make the app installable;
  `display: standalone` gives it its own window, and the precache keeps it working offline.
- **Icons.** One source icon per app is emitted at the sizes the manifest lists.
- **Runtime platform APIs** the app opts into from its own JS — Notifications, Badging, Web
  Share, Background Sync, IndexedDB — are plain platform calls; atp serves the files and the
  manifest that make them usable.

### OpenSearch

Reference: [OpenSearch description format](https://developer.mozilla.org/en-US/docs/Web/XML/Guides/OpenSearch).

- **Description document, generated.** atp serves `/apps/{app}/opensearch.xml` with
  `Content-Type: application/opensearchdescription+xml`, carrying `<ShortName>` (16
  characters or fewer), `<Description>`, `<InputEncoding>`, `<Image>`, a
  `<Url type="text/html" template="…?q={searchTerms}">`, an optional
  `application/x-suggestions+json` suggestions URL, and a `rel="self"` auto-update URL.
- **Autodiscovery, injected.** The head manager injects
  `<link rel="search" type="application/opensearchdescription+xml" title="…" href="…">` into
  every app document, so a browser registers the app's search engine on first visit.
- **The search endpoint is yours.** `{searchTerms}` targets a registered Go function (or a
  pod query); atp only describes it.

### Media

Reference: [Media](https://developer.mozilla.org/en-US/docs/Web/Media).

- **Correct delivery.** Media is served with the right `Content-Type`, `Accept-Ranges` and
  byte-range support, which `<video>` and `<audio>` need for seeking and streaming.
- **Tracks and responsive images.** WebVTT caption/subtitle/description tracks are served
  alongside, and image variants are served for `<picture>` / `srcset` (AVIF/WebP/JPEG).
- **Capture to storage.** Browser capture (`getUserMedia` + `MediaRecorder`) uploads straight
  into pod's file store, so a recording becomes a record with an attached blob.
- **Encrypted at rest.** Media blobs can be encrypted like any other pod upload.

### WebAssembly

Reference: [WebAssembly](https://developer.mozilla.org/en-US/docs/WebAssembly).

- **Served correctly.** `.wasm` is served as `application/wasm`, which
  `WebAssembly.instantiateStreaming()` requires, with the same caching policy as other assets.
- **Go on both sides.** Because atp is a Go server, an application can be Go server-side
  (registered functions) **and** Go client-side (the same code compiled to `wasm`), with atp
  serving the module and its JavaScript glue.
- **Workers and threads.** The header manager sets the `script-src` / `worker-src` (and, for
  shared-memory threads, `Cross-Origin-Opener-Policy` / `Cross-Origin-Embedder-Policy`)
  directives a wasm worker needs.
- **Resource hints.** The head manager can emit
  `<link rel="preload" as="fetch" type="application/wasm" crossorigin>` for a module the app
  needs early.

### Privacy

Reference: [Privacy](https://developer.mozilla.org/en-US/docs/Web/Privacy).

- **No identity, no accounts.** Hosting a resource needs no login and stores no user, so
  there is nothing to profile; the optional capability layer is identity-free by design.
- **No third party, by construction.** The dependency rule forbids third-party scripts, fonts
  and CDNs, so an atp app has no third-party surveillance surface and makes no cross-site
  requests.
- **Secrets never reach the browser.** They live server-side in the vault, are read only by Go
  functions, and are never rendered into HTML or JavaScript.
- **Privacy headers as defaults.** The header manager sets `Referrer-Policy`,
  `Permissions-Policy`, `Cross-Origin-*` and cookie-free defaults an author can tighten. Data
  stays on your server; there is no external service.
- **Scoped storage.** App state (IndexedDB, Cache, `localStorage`) is bounded to the app's own
  origin.

### Performance

Reference: [Performance](https://developer.mozilla.org/en-US/docs/Web/Performance).

- **Cache policy per path class.** The header manager issues `ETag` / `Last-Modified`
  conditional requests, `Cache-Control: immutable` for content-addressed assets, and
  revalidation for HTML, so repeat visits are cheap.
- **No framework, no bundler.** The client is HTML, CSS and ES modules, so there is no runtime
  to download and no build step; server-side templates mean the first paint needs no client
  round-trip.
- **Resource hints.** The head manager injects `preload`, `prefetch`, `preconnect` and
  `modulepreload` hints per app.
- **Transport.** atp is an ordinary `net/http` server, so it serves HTTP/2 (and HTTP/3 behind
  a fronting proxy) and compresses responses through the header manager's `Content-Encoding`
  policy.
- **Fast data.** pod's in-memory index and record cache make queries cheap; song's static
  reads are direct filesystem reads.

### Security

Reference: [Security](https://developer.mozilla.org/en-US/docs/Web/Security).

- **Safe defaults, centrally set.** The header manager applies `Content-Security-Policy`,
  `X-Content-Type-Options: nosniff`, `Strict-Transport-Security` (when TLS is terminated in
  front), `Referrer-Policy`, `Permissions-Policy`, `COOP`/`COEP` and frame controls; an
  application can tighten or relax them per path.
- **Injection points are safe.** Templates render with `html/template` (contextual escaping),
  stored values are escaped on output, and song and pod confine paths to the application root.
- **Capability security when wanted.** shepherd supplies scoped, revocable, rate-limited,
  firewall-bounded access for an app that opts in — never forced on a hosted resource.
- **The access rule still holds:** no *access* security is added to hosted resources; these are
  safe defaults for the content an app chooses to serve.

### Go for web development

Reference: [Go for Web Development](https://go.dev/solutions/webdev).

atp is the Go team's case made concrete: a single dependency-free binary, an `net/http`
server, `html/template` for server-side rendering, `encoding/json` / `encoding/xml` for data,
the pod `database/sql` driver, `crypto/*` for the vault and tokens, and `embed` for assets —
deployable to any host or locally and scaling on goroutines. An application's serverless
functions are ordinary Go, so client and server share one language and one standard library.

### What the author writes vs. what atp generates

| The author provides | atp generates / serves automatically |
|---|---|
| `index.html`, CSS, JS | web app manifest, service worker, `<link rel="manifest">`, icon sizes |
| app name, icons, colours | OpenSearch description document + `<link rel="search">` |
| a `search` function | the `{searchTerms}` template and suggestion URL |
| media files | MIME + range delivery, caption tracks, image variants |
| a `.wasm` module (optional) | `application/wasm` delivery, preload hint, worker/COEP headers |
| nothing for privacy | no third-party requests, privacy headers, server-side secrets |
| nothing for performance | cache + compression policy, resource hints, SSR |
| nothing for security | CSP, nosniff, HSTS, permissions, COOP/COEP defaults |

---

## Central head management

The `<head>` of hosted components is managed **centrally** rather than duplicated in every
document. atp keeps an ordered head registry and injects it into the `<head>` of every HTML
document it serves.

```go
srv.Head().Add(`<meta name="theme-color" content="#0a84ff">`)          // all applications
srv.Head().AddFor("notes", `<script src="/notes/analytics.js" defer></script>`) // one app
srv.Head().Title("Notes — AzzurroTech")                                 // default <title>
```

- **Global and per-application fragments.** Ordered, de-duplicated, and injected before
  `</head>`.
- **The platform injects its own fragments here.** This is the single insertion point for the
  generated client-app plumbing described in
  [Client application capabilities](#client-application-capabilities): the web app
  `<link rel="manifest">`, the OpenSearch `<link rel="search">`, media and icon hints,
  the `preload`/`prefetch`/`preconnect`/`modulepreload` resource hints, and the
  `<link rel="preload" as="fetch" type="application/wasm">` hint for a WebAssembly module.
- **Over HTTP.** `GET/POST/DELETE /head` and `/head/{app}` manage the same registry on a
  running server, so the head can be adjusted without a redeploy.
- **Works with song.** Because song already injects the Web Components runtime only into
  documents that use a registered tag, the head manager and the component runtime share the
  same insertion point and do not fight over it.

---

## Request header management (internal and external)

Headers are managed in one place, for both directions and both audiences:

- **Inbound (external, client → atp).** Policy rules decide which request headers are
  trusted, rewritten, added or stripped before a handler sees them — for example accepting
  `X-Forwarded-For` only from a proxy you control, or dropping client-supplied
  `X-Platform-*` spoofing attempts.
- **Outbound (external, atp → client).** Response headers are set centrally. The default
  policy is the **safe baseline** the client-capability sections rely on:
  `Content-Security-Policy`, `X-Content-Type-Options: nosniff`, `Strict-Transport-Security`
  (when TLS terminates in front), `Referrer-Policy`, `Permissions-Policy`, `COOP`/`COEP`,
  frame controls, `Cache-Control`/`ETag` per path class, `Content-Encoding` for compression,
  and CORS — with per-application overrides.
- **Internal (atp ↔ song/pod/shepherd, atp ↔ registered functions).** An internal header
  namespace carries the request context between the fileserver, the registry and the embedded
  modules — the resolved application, the effective principal, the correlation id — without
  ever leaking to the client.

```go
srv.Headers().
	Request(`X-Forwarded-For`).TrustProxy(true).
	Request(`X-Platform-*`).Strip().
	Response(`/apps/**`).Set("Cache-Control", "public, max-age=300").
	Internal("X-Platform-App").FromPath(1)   // set once, visible to functions
```

The gateway that shepherd already provides (`X-Shepherd-Subject`, `X-Shepherd-Scopes`,
`X-Shepherd-Signature`, …) is reused for the *external upstream* case, so a backend behind the
gateway receives a request that carries its own proof.

---

## Secrets management and sharing

Secrets are **named values owned by an application**, encrypted at rest with AES-256-GCM (key
derived from `ATP_SECRET`) and readable by that application's registered Go functions.

```go
srv.Secrets().Set("notes", "stripe_key", "sk_live_…", "payments key")
srv.Secrets().Get("notes", "stripe_key")             // (string, bool)
srv.Secrets().List("notes")                          // names + notes; never values
link, _ := srv.Secrets().Share("notes", "stripe_key", atp.ShareOptions{TTL: time.Hour})
```

- **Creation and management.** Set (create/rotate), read, list and delete by name. Listing
  never returns a value; reading one requires an explicit call.
- **Sharing assistance.** A secret can be shared as an opaque, expiring, revocable link —
  the same link-first model pod uses for records and files, so nothing relies on filesystem
  symlinks. `DELETE /secrets/{app}/{name}/share/{token}` revokes early.
- **Encrypted at rest.** The on-disk file never contains plaintext; the value is decrypted
  only for the holder of `ATP_SECRET`.
- **Over HTTP**, under the management view: `/secrets/{app}` and `/secrets/{app}/{name}`.
- **Injected where needed.** A registered function reads a secret through `srv.Secrets()`;
  the value is never embedded in served HTML or JavaScript.

---

## Exposing song, pod and shepherd through the atp view

All three modules are mounted, so their whole HTTP surface is reachable through atp's one
address. The prefixes below are the atp view onto each module.

### song — static hosting, templates, components

| atp path | song feature |
|---|---|
| `/{app}/…` | serve the application's silo (index resolution, content types) |
| `POST /{app}/{path}` | upsert a static resource |
| `/{app}/**.tmpl`, `/{app}/**.html` | server-side Go template processing |
| `/_song/components.js` | the baseline Web Components runtime |
| `/_song/components` | the registered tag definitions as JSON |

### pod — the form-driven XML database

Mounted under `/pod` (and available per application under `/{app}/api/pod`):

| atp path | pod feature |
|---|---|
| `/pod/table/{path}` | `GET` query · `POST`/`PUT` upsert (the HTML form action) |
| `/pod/record/{path}/{id}` | `GET` one record (pre-filled form card) · `PUT`/`POST`/`DELETE` |
| `/pod/schema/{path}` | the effective schema |
| `/pod/files/{table}/{id}/{name}` | download · replace · delete an uploaded blob |
| `/pod/share`, `/pod/s/{token}` | create · resolve a link-based share |
| `/pod/sql`, `/pod/tables` | SQL surface and table listing |
| `/pod/normalize`, `/pod/reindex` | run the normalizer / indexer now |

### shepherd — identity-free security

| atp path | shepherd feature |
|---|---|
| `/api/keys`, `/api/keys/block`, `/api/keys/magic` | issue capabilities, blocks, magic links |
| `/api/keys/javascript` | derive a browser WebCrypto key |
| `/api/token/verify`, `/api/revoke`, `/api/revoke/block` | verify, revoke a token or a block |
| `/api/provider/authenticate` | identity-free provider call (pod-compatible principal) |
| `/api/firewall/rules` | ordered allow/deny rules |
| `/api/ratelimit/status`, `/api/ratelimit/reset` | the token-bucket limiter |
| `/api/upstreams`, `/gw/…` | the enriching, signing gateway |

Because the modules are compiled in, every one of these is a function call inside the process,
not a network hop.

---

## Encrypted static and dynamic content

atp is built so that **both halves of an application can be encrypted at rest**:

- **Static** — song serves silos; with encryption enabled, file contents are sealed and
  decrypted transparently on read.
- **Dynamic** — pod encrypts record *values* and uploaded blobs; registered functions and the
  SQL surface read them back transparently. Metadata (ids, timestamps, schema, filenames) stays
  plaintext so listing, paging and the form card keep working.
- **Secrets** — the vault is always encrypted at rest.

Encryption is **at rest only**; terminate TLS in front for the wire. Like pod, atp can start
with encryption off for local work and turn it on without rewriting the data.

---

## Access model — no added security for hosted resources

**A resource hosted in the server is reachable without logging in.** Hosted applications,
their files, their templates and their registered functions are served to anyone who asks.
That is deliberate: atp is for publishing, not for gating.

Where an application *wants* capabilities — a private share, a scoped API, a rate limit, a
firewall rule — it opts in and gets them from **shepherd**: identity-free tokens that carry
scopes and roles, verified statelessly, with blocks, magic links, revocation, a firewall, a
rate limiter and a signing gateway. atp never forces a login onto a hosted resource and never
adds a session layer of its own for them. There are no accounts and no user table anywhere in
the platform.

---

## Running locally

```sh
go build -o atp .
./atp --addr :8080 --root ./data --secret '0123456789abcdef0123456789abcdef'
# open http://localhost:8080  (the management view)
# a hosted application:       http://localhost:8080/notes/
# a registered function:      http://localhost:8080/notes/api/list
```

| Flag | Env | Default | Meaning |
|---|---|---|---|
| `--addr` | `ATP_ADDR` | `:8080` | listen address |
| `--root` | `ATP_ROOT` | `./data` | root for hosted apps, stores and the vault |
| `--secret` | `ATP_SECRET` | — | master secret ≥ 32 bytes (vault key + token signing) |
| `--encrypt` | `ATP_ENCRYPT` | `false` | encrypt static and dynamic content at rest |
| `--headless` | `ATP_HEADLESS` | `false` | serve apps/functions without the management view |
| `--version` / `--help` | — | — | print and exit |

Try a whole application locally:

```sh
./atp --root ./data --secret "$(head -c32 /dev/urandom | base64)" --encrypt
# drop HTML/CSS/JS in ./data/apps/notes/, register a Go function, open /notes/
```

---

## Deploying

atp is **one static binary with no external dependencies and no database**, so it deploys the
same way everywhere.

**As a binary on a host**

```sh
CGO_ENABLED=0 go build -o atp .          # or: GOOS=linux GOARCH=amd64 go build -o atp .
scp atp you@host:/usr/local/bin/atp
ssh you@host 'ATP_SECRET=… atp --addr :8080 --root /srv/atp/data'
```

**As a systemd unit**

```ini
[Service]
Environment=ATP_SECRET=replace-me-with-32-plus-bytes
ExecStart=/usr/local/bin/atp --addr :8080 --root /srv/atp/data
Restart=on-failure
```

**As a container** — the embedded modules are submodules, so copy them alongside `go.mod`:

```dockerfile
FROM golang:1.22-alpine
WORKDIR /app
COPY go.mod ./
COPY song ./song
COPY pod ./pod
COPY shepherd ./shepherd
COPY . .
RUN CGO_ENABLED=0 go build -o atp-server .
EXPOSE 8080
CMD ["/app/atp-server", "--addr", ":8080", "--root", "/app/data"]
```

**Behind TLS.** Run atp on plain HTTP behind Caddy, nginx or a cloud load balancer that
terminates TLS and forwards to `:8080`; when you do, trust the proxy's client-IP header through
the header manager.

---

## Layout

```text
README.md            this document
LICENSE              MIT
.gitmodules          song, pod, shepherd
song/                submodule — static hosting, templates, Web Components
pod/                 submodule — form → XML database, uploads, encryption, shares
shepherd/            submodule — identity-free IAM, firewall, rate limit, gateway
```

atp is the `atp` submodule of the `stenella` superproject and itself superprojects `song`,
`pod` and `shepherd` through `replace` directives in `go.mod`, so all four modules build
together and stay standard-library only.

---

## Development

```sh
go build ./...   # the platform and its embedded modules
go vet ./...
go test ./...
```

The behaviour this document specifies is delivered in stages: the launch stage ships this
specification, the three embedded submodules and the repository scaffolding; the Go packages
(`atp.New`, the function registry, the secrets vault, the head and header managers, the
management view) are the contract they implement. Where this document names song, pod or
shepherd behaviour, those details are already implemented and tested inside the submodules.

The reference wirings for [how the middleware files are used](#where-the-middleware-files-live-and-how-they-are-used)
are the modules' own examples: `song/examples/middleware/`, `pod/README.md` and
`shepherd/examples/middleware-demo/`.

---

## References

The web-platform capabilities atp automates are documented upstream:

- **Progressive web apps** — <https://developer.mozilla.org/en-US/docs/Web/Progressive_web_apps>
- **OpenSearch description format** — <https://developer.mozilla.org/en-US/docs/Web/XML/Guides/OpenSearch>
- **Media** — <https://developer.mozilla.org/en-US/docs/Web/Media>
- **WebAssembly** — <https://developer.mozilla.org/en-US/docs/WebAssembly>
- **Privacy** — <https://developer.mozilla.org/en-US/docs/Web/Privacy>
- **Performance** — <https://developer.mozilla.org/en-US/docs/Web/Performance>
- **Security** — <https://developer.mozilla.org/en-US/docs/Web/Security>
- **Go for Web Development** — <https://go.dev/solutions/webdev>

The embedded modules document their own middleware in `song/pkg/song/README.md`,
`pod/README.md`, `shepherd/middleware/README.md` and `shepherd/provider/`.
