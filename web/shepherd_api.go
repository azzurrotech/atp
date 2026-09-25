package web

// This file implements atp's hosting of the full shepherd surface. shepherd's
// own standalone server is a `package main` program (not importable), so atp
// — as the designated orchestrator — binds the importable middleware.Shepherd
// API to HTTP routes of its own. None of the security logic is duplicated:
// every handler below is a thin, honest binding over the real shepherd
// primitives (token.Manager, firewall, rate limiter, gateway).

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"azzurrotech/shepherd/firewall"
	"azzurrotech/shepherd/middleware"
	"azzurrotech/shepherd/token"

	"azzurrotech/song/pkg/static"
)

// ---- gateway ------------------------------------------------------------------

// rebuildGateway reconstructs the shepherd gateway handler from the current
// upstream table. Called on startup and whenever upstreams change.
func (s *ATPService) rebuildGateway() {
	up := make([]middleware.Upstream, 0, len(s.up))
	for _, u := range s.up {
		up = append(up, u)
	}
	s.gw = s.sec.Gateway(middleware.GatewayOptions{Mount: "/gw", Upstreams: up})
}

// handleGateway forwards into the shepherd reverse-proxy gateway at /gw/.
// Access to the gateway through atp requires an admin session; individual
// upstreams may still be marked Public to skip capability auth inside.
func (s *ATPService) handleGateway(w http.ResponseWriter, r *http.Request) {
	s.gw.ServeHTTP(w, r)
}

// handleShepherdUpstreams lists (GET) or registers (POST) gateway upstreams.
func (s *ATPService) handleShepherdUpstreams(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.upMu.RLock()
		out := make([]middleware.Upstream, 0, len(s.up))
		for _, u := range s.up {
			out = append(out, u)
		}
		s.upMu.RUnlock()
		writeJSON(w, http.StatusOK, map[string]any{"upstreams": out, "mount": "/gw"})
	case http.MethodPost:
		var u middleware.Upstream
		if err := readJSON(r, &u); err != nil {
			writeErrJSON(w, http.StatusBadRequest, "invalid body: "+err.Error())
			return
		}
		if u.Name == "" || u.Prefix == "" || u.Target == "" {
			writeErrJSON(w, http.StatusBadRequest, "name, prefix and target are required")
			return
		}
		if strings.ContainsAny(u.Prefix, "/ \t") {
			writeErrJSON(w, http.StatusBadRequest, "prefix must be a single path segment")
			return
		}
		if _, err := url.Parse(u.Target); err != nil {
			writeErrJSON(w, http.StatusBadRequest, "invalid target url: "+err.Error())
			return
		}
		s.upMu.Lock()
		s.up[u.Prefix] = u
		s.upMu.Unlock()
		s.rebuildGateway()
		writeJSON(w, http.StatusCreated, map[string]any{"registered": u})
	default:
		methodNotAllowed(w, "GET, POST")
	}
}

func (s *ATPService) handleShepherdUpstreamDelete(w http.ResponseWriter, r *http.Request) {
	prefix := pathValue(r, "prefix")
	s.upMu.Lock()
	_, ok := s.up[prefix]
	delete(s.up, prefix)
	s.upMu.Unlock()
	s.rebuildGateway()
	if !ok {
		writeErrJSON(w, http.StatusNotFound, "upstream not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": prefix})
}

// ---- firewall -----------------------------------------------------------------

// handleShepherdFirewallRules lists (GET) and creates (POST) firewall rules.
func (s *ATPService) handleShepherdFirewallRules(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{
			"default_allow": s.fw.DefaultAllow(),
			"rules":         s.fw.Rules(),
		})
	case http.MethodPost:
		var rule firewall.Rule
		if err := readJSON(r, &rule); err != nil {
			writeErrJSON(w, http.StatusBadRequest, "invalid body: "+err.Error())
			return
		}
		if rule.Action != "allow" && rule.Action != "deny" {
			writeErrJSON(w, http.StatusBadRequest, "action must be \"allow\" or \"deny\"")
			return
		}
		id, err := s.fw.Add(rule)
		if err != nil {
			writeErrJSON(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"id": id})
	default:
		methodNotAllowed(w, "GET, POST")
	}
}

func (s *ATPService) handleShepherdFirewallRuleDelete(w http.ResponseWriter, r *http.Request) {
	id := pathValue(r, "id")
	if !s.fw.Remove(id) {
		writeErrJSON(w, http.StatusNotFound, "rule not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": id})
}

// ---- rate limiter ---------------------------------------------------------------

func (s *ATPService) handleShepherdRateStatus(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Query().Get("key")
	if key == "" {
		writeErrJSON(w, http.StatusBadRequest, "missing key query parameter")
		return
	}
	st, ok := s.sec.RateLimitStatus(key)
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"key": key, "tracked": false})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"key": key, "tracked": true, "status": st})
}

func (s *ATPService) handleShepherdRateReset(w http.ResponseWriter, r *http.Request) {
	s.sec.RateLimitReset()
	writeJSON(w, http.StatusOK, map[string]any{"reset": true})
}

// ---- token issuance / revocation (admin, unscoped) -------------------------------

func (s *ATPService) handleShepherdIssue(w http.ResponseWriter, r *http.Request) {
	var req issueRequest
	if err := readJSON(r, &req); err != nil {
		writeErrJSON(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	ttl, err := parseTTL(req.TTL)
	if err != nil {
		writeErrJSON(w, http.StatusBadRequest, "invalid ttl: "+err.Error())
		return
	}
	raw, claims, err := s.sec.Issue(token.IssueOptions{
		Subject:  req.Subject,
		Scopes:   req.Scopes,
		Roles:    req.Roles,
		Audience: req.Audience,
		TTL:      ttl,
		Block:    req.Block,
		Meta:     req.Meta,
	})
	if err != nil {
		writeErrJSON(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"token": raw, "claims": claims, "issuer": s.sec.Issuer(),
		"expires_in": claims.ExpiresAt - claims.IssuedAt,
	})
}

func (s *ATPService) handleShepherdIssueBlock(w http.ResponseWriter, r *http.Request) {
	var req issueRequest
	if err := readJSON(r, &req); err != nil {
		writeErrJSON(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	if req.Count <= 0 {
		req.Count = 1
	}
	if req.Count > 10000 {
		writeErrJSON(w, http.StatusBadRequest, "count must be <= 10000")
		return
	}
	ttl, err := parseTTL(req.TTL)
	if err != nil {
		writeErrJSON(w, http.StatusBadRequest, "invalid ttl: "+err.Error())
		return
	}
	keys, err := s.sec.Manager().IssueBlock(token.IssueOptions{
		Subject:  req.Subject,
		Scopes:   req.Scopes,
		Roles:    req.Roles,
		Audience: req.Audience,
		TTL:      ttl,
		Meta:     req.Meta,
	}, req.Block, req.Count)
	if err != nil {
		writeErrJSON(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"block": keys[0].Claims.Block, "count": len(keys), "keys": keys,
	})
}

func (s *ATPService) handleShepherdMagicLink(w http.ResponseWriter, r *http.Request) {
	var req issueRequest
	if err := readJSON(r, &req); err != nil {
		writeErrJSON(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	if req.Next == "" {
		req.Next = "/"
	}
	ttl, err := parseTTL(req.TTL)
	if err != nil {
		writeErrJSON(w, http.StatusBadRequest, "invalid ttl: "+err.Error())
		return
	}
	raw, claims, err := s.sec.Manager().IssueMagicLink(token.IssueOptions{
		Subject:  req.Subject,
		Scopes:   req.Scopes,
		Roles:    req.Roles,
		Audience: req.Audience,
		TTL:      ttl,
		Next:     req.Next,
		Meta:     req.Meta,
	})
	if err != nil {
		writeErrJSON(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"url":        "/api/shepherd/verify?t=" + url.QueryEscape(raw) + "&next=" + url.QueryEscape(req.Next),
		"token":      raw,
		"claims":     claims,
		"expires_in": claims.ExpiresAt - claims.IssuedAt,
	})
}

func (s *ATPService) handleShepherdRevoke(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token string `json:"token"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErrJSON(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	claims, err := s.sec.Manager().Verify(req.Token)
	if err != nil {
		writeErrJSON(w, http.StatusBadRequest, "cannot revoke: "+err.Error())
		return
	}
	s.sec.RevokeToken(claims.ID, claims.ExpiresAt)
	writeJSON(w, http.StatusOK, map[string]any{"revoked": claims.ID})
}

func (s *ATPService) handleShepherdRevokeBlock(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Block string `json:"block"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErrJSON(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	if req.Block == "" {
		writeErrJSON(w, http.StatusBadRequest, "block is required")
		return
	}
	s.sec.RevokeBlock(req.Block, time.Now().Add(365*24*time.Hour).Unix())
	writeJSON(w, http.StatusOK, map[string]any{"revoked_block": req.Block})
}

func (s *ATPService) handleShepherdJSKey(w http.ResponseWriter, r *http.Request) {
	scope := r.URL.Query().Get("scope")
	k, err := s.sec.JSCryptoKey(scope, r.URL.Query().Get("usage"))
	if err != nil {
		writeErrJSON(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, k)
}

// ---- public verification ---------------------------------------------------------

// handleShepherdVerifyToken validates a capability token presented via the
// Authorization header or the shepherd capability cookie. Public by design —
// verification of a presented credential requires no credential itself.
func (s *ATPService) handleShepherdVerifyToken(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, "GET")
		return
	}
	raw := s.sec.TokenFromRequest(r)
	if raw == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"valid": false, "error": "missing token"})
		return
	}
	claims, err := s.sec.Manager().Verify(raw)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"valid": false, "error": err.Error()})
		return
	}
	if s.sec.IsDenied(claims.ID) || (claims.Block != "" && s.sec.IsDenied(claims.Block)) {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"valid": false, "error": "token revoked"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"valid": true, "claims": claims})
}

// handleShepherdMagicRedeem redeems a magic link (?t=...) like shepherd's own
// /verify endpoint: it converts the one-shot link into a session capability
// cookie and redirects to the token's Next target. Public by design.
func (s *ATPService) handleShepherdMagicRedeem(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, "GET")
		return
	}
	tok := r.URL.Query().Get("t")
	if tok == "" {
		writeErrJSON(w, http.StatusBadRequest, "missing magic link token")
		return
	}
	claims, err := s.sec.VerifyMagicToken(tok)
	if err != nil {
		writeErrJSON(w, http.StatusUnauthorized, "invalid magic link: "+err.Error())
		return
	}
	sess, sessClaims, err := s.sec.Issue(token.IssueOptions{
		Subject:  claims.Subject,
		Scopes:   claims.Scopes,
		Roles:    claims.Roles,
		Audience: claims.Audience,
		Block:    claims.Block,
		Meta:     claims.Meta,
	})
	if err != nil {
		writeErrJSON(w, http.StatusInternalServerError, err.Error())
		return
	}
	secure := r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
	http.SetCookie(w, &http.Cookie{
		Name:     middleware.CookieName,
		Value:    sess,
		Path:     "/",
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(sessClaims.ExpiresAt - sessClaims.IssuedAt),
	})
	target := "/"
	if safeRedirect(claims.Next, r.Host) {
		target = claims.Next
	}
	http.Redirect(w, r, target, http.StatusFound)
}

// safeRedirect only allows local, protocol-relative or same-host redirect
// targets (mirrors shepherd's own guard).
func safeRedirect(to, reqHost string) bool {
	if to == "" || strings.HasPrefix(to, "//") {
		return false
	}
	if strings.HasPrefix(to, "/") {
		return true
	}
	u, err := url.Parse(to)
	if err != nil {
		return false
	}
	if u.Host == "" {
		return true
	}
	return strings.EqualFold(u.Host, reqHost)
}

func methodNotAllowed(w http.ResponseWriter, allow string) {
	w.Header().Set("Allow", allow)
	writeErrJSON(w, http.StatusMethodNotAllowed, "method not allowed")
}

// ---- demo seed -------------------------------------------------------------------

// seedDemo provisions a working client that exercises every integrated
// service, so a fresh instance has a dashboard, a hosted site, a database
// table, a secret and a capability key to poke at.
func (s *ATPService) seedDemo() error {
	if _, err := s.clients.Create("demo", "Demo Client", "Seeded demo client — exercises song, pod, shepherd through atp"); err != nil && !strings.Contains(err.Error(), "already exists") {
		return err
	}
	st := s.song.Store()
	if err := st.CreateSilo("demo"); err != nil && !errors.Is(err, static.ErrExists) {
		return fmt.Errorf("silo: %w", err)
	}
	files := map[string]string{
		"index.html": `<!doctype html><html><head><meta charset="utf-8">
<title>Demo client — served by song through atp</title>
<link rel="stylesheet" href="style.css"></head>
<body><main>
<h1>Hello from the demo client silo</h1>
<p>This site is <strong>hosted by song</strong> and served through atp at
<a href="/c/demo/">/c/demo/</a>. Its traffic and size are counted against the
client's usage and billed at the configured rate.</p>
<button id="go">Call the demo pod table</button>
<pre id="out"></pre>
</main><script src="app.js"></script></body></html>`,
		"style.css": "body{font-family:system-ui,sans-serif;max-width:46em;margin:3em auto;padding:0 1em;color:#1a1d23;line-height:1.6}button{padding:.5em 1em}pre{background:#f4f5f7;padding:.75em;border-radius:6px}",
		"app.js": `document.getElementById('go') && document.getElementById('go').addEventListener('click', async () => {
  const res = await fetch('/api/pod/table/demo/notes', {headers:{'Accept':'application/json'}});
  document.getElementById('out').textContent = JSON.stringify(await res.json(), null, 2);
});`,
	}
	for rel, content := range files {
		if _, err := st.Create("demo", rel, content, false); err != nil && !errors.Is(err, static.ErrExists) {
			return fmt.Errorf("%s: %w", rel, err)
		}
	}

	// A pod table + one record, created through the real pod HTTP handler.
	seedBody := url.Values{"title": {"first note"}, "body": {"seeded through atp"}}
	req := &http.Request{
		Method: http.MethodPost,
		URL:    &url.URL{Path: "/api/pod/table/demo/notes"},
		Header: http.Header{"Content-Type": []string{"application/x-www-form-urlencoded"}},
		Body:   io.NopCloser(strings.NewReader(seedBody.Encode())),
	}
	resp := &recorder{}
	s.pod.ServeHTTP(resp, req)
	if resp.status >= 400 {
		return fmt.Errorf("pod seed: %s (status %d)", resp.body.String(), resp.status)
	}

	// One vault secret and one capability token scoped to the client.
	if _, err := s.vault.Set("demo", "example-api-key", "sk-demo-"+randTokenHex(8), "seeded example secret for external APIs"); err != nil {
		return err
	}
	if _, _, err := s.sec.Issue(token.IssueOptions{Subject: "demo-anon", Scopes: []string{"client:demo"}, Meta: map[string]string{"note": "seeded"}}); err != nil {
		return err
	}
	return nil
}

// recorder is the smallest possible http.ResponseWriter for in-process calls
// (used by the seed routine; net/http/httptest would also work but this keeps
// the seeded path dependency-free of test helpers).
type recorder struct {
	status int
	body   strings.Builder
}

func (r *recorder) Header() http.Header { return http.Header{} }
func (r *recorder) WriteHeader(status int) {
	if r.status == 0 {
		r.status = status
	}
}
func (r *recorder) Write(b []byte) (int, error) { return r.body.Write(b) }
