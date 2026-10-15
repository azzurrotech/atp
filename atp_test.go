package atp

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testSecret = "0123456789abcdef0123456789abcdef" // 32 bytes

func newServer(t *testing.T, opts Options) *Server {
	t.Helper()
	if opts.Root == "" {
		opts.Root = t.TempDir()
	}
	if opts.Secret == "" {
		opts.Secret = testSecret
	}
	srv, err := New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return srv
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func do(t *testing.T, h http.Handler, method, target string, body io.Reader, hdrs map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, body)
	for k, v := range hdrs {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func decode(t *testing.T, rec *httptest.ResponseRecorder, v any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), v); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
}

func TestNewRejectsShortSecret(t *testing.T) {
	if _, err := New(Options{Root: t.TempDir(), Secret: "too-short"}); err == nil {
		t.Fatal("expected error for short secret")
	}
	if _, err := New(Options{Root: t.TempDir(), Secret: testSecret}); err != nil {
		t.Fatalf("long secret should be accepted: %v", err)
	}
}

func TestServerAddrDefault(t *testing.T) {
	srv := newServer(t, Options{})
	if srv.Addr() != ":8080" {
		t.Fatalf("Addr() = %q, want :8080", srv.Addr())
	}
	if srv.Root() == "" {
		t.Fatal("Root() empty")
	}
}

func TestFileserverServesRootFiles(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "hello.txt"), "hello root")
	srv := newServer(t, Options{Root: root})
	rec := do(t, srv, "GET", "/hello.txt", nil, nil)
	if rec.Code != http.StatusOK || rec.Body.String() != "hello root" {
		t.Fatalf("got %d %q", rec.Code, rec.Body.String())
	}
}

func TestHostedAppAndHeadInjection(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "apps", "notes", "index.html"),
		"<!DOCTYPE html><html><head><title>Notes</title></head><body>hi</body></html>")
	srv := newServer(t, Options{Root: root})
	srv.Head().Add(`<meta name="theme-color" content="#0a84ff">`)
	srv.Head().AddFor("notes", `<script src="/notes/analytics.js" defer></script>`)

	rec := do(t, srv, "GET", "/notes/", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `name="theme-color"`) {
		t.Errorf("global head fragment missing: %q", body)
	}
	if !strings.Contains(body, `analytics.js`) {
		t.Errorf("per-app head fragment missing: %q", body)
	}
	if i, j := strings.Index(body, `theme-color`), strings.Index(body, `</head>`); i < 0 || j < 0 || i > j {
		t.Errorf("fragment not injected before </head>: %q", body)
	}
}

func TestAppResolution(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "apps", "notes", "index.html"), "<html></html>")
	srv := newServer(t, Options{Root: root})
	if err := srv.Apps().Register(App{
		Name: "notes", Title: "Notes", Description: "note taking", ThemeColor: "#ff0000",
	}); err != nil {
		t.Fatal(err)
	}
	app, ok := srv.Apps().Get("notes")
	if !ok || app.Title != "Notes" || app.Search == "" {
		t.Fatalf("app resolution failed: %+v ok=%v", app, ok)
	}
	if srv.appForPath("/notes/index.html") != "notes" {
		t.Fatalf("appForPath = %q", srv.appForPath("/notes/index.html"))
	}
}

func TestGeneratedClientCapabilities(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "apps", "notes", "index.html"), "<html><head></head><body></body></html>")
	srv := newServer(t, Options{Root: root})
	_ = srv.Apps().Register(App{Name: "notes", Title: "Notes"})

	rec := do(t, srv, "GET", "/notes/manifest.webmanifest", nil, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Header().Get("Content-Type"), "manifest") {
		t.Fatalf("manifest: %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	var man map[string]any
	decode(t, rec, &man)
	if man["name"] != "Notes" || man["start_url"] != "/notes/" {
		t.Fatalf("manifest content %+v", man)
	}

	rec = do(t, srv, "GET", "/notes/sw.js", nil, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "addEventListener") {
		t.Fatalf("sw.js: %d %q", rec.Code, rec.Body.String())
	}

	rec = do(t, srv, "GET", "/notes/opensearch.xml", nil, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Header().Get("Content-Type"), "opensearchdescription") {
		t.Fatalf("opensearch: %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	if !strings.Contains(rec.Body.String(), "<OpenSearchDescription") ||
		!strings.Contains(rec.Body.String(), "{searchTerms}") {
		t.Fatalf("opensearch content %q", rec.Body.String())
	}
}

func TestAppOwnFileWinsOverGeneration(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "apps", "notes", "index.html"), "<html></html>")
	writeFile(t, filepath.Join(root, "apps", "notes", "manifest.webmanifest"), `{"name":"authored"}`)
	srv := newServer(t, Options{Root: root})
	_ = srv.Apps().Register(App{Name: "notes", Title: "Notes"})

	rec := do(t, srv, "GET", "/notes/manifest.webmanifest", nil, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "authored") {
		t.Fatalf("own manifest not served: %d %q", rec.Code, rec.Body.String())
	}
}

func TestCapabilityHeadLinks(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "apps", "notes", "index.html"), "<html><head></head><body></body></html>")
	srv := newServer(t, Options{Root: root})
	_ = srv.Apps().Register(App{Name: "notes", Title: "Notes"})

	rec := do(t, srv, "GET", "/notes/", nil, nil)
	body := rec.Body.String()
	if !strings.Contains(body, `rel="manifest"`) {
		t.Errorf("manifest link missing: %q", body)
	}
	if !strings.Contains(body, `rel="search"`) {
		t.Errorf("opensearch link missing: %q", body)
	}
}

func TestRegistryRegisterMatchAndParams(t *testing.T) {
	srv := newServer(t, Options{})
	if err := srv.Handle("GET", "/api/hello", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "hi")
	}); err != nil {
		t.Fatal(err)
	}
	if err := srv.HandleFunc("GET", "/api/item/{id}", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, PathParam(r, "id"))
	}); err != nil {
		t.Fatal(err)
	}
	if err := srv.Handle("GET", "/api/nope", nil); err == nil {
		t.Error("nil handler should error")
	}
	if err := srv.Handle("GET", "no-slash", func(http.ResponseWriter, *http.Request) {}); err == nil {
		t.Error("malformed pattern should error")
	}
	if err := srv.Handle("GET", "/api/{a}/{b}", func(http.ResponseWriter, *http.Request) {}); err == nil {
		t.Error("double param pattern should error")
	}
	if err := srv.Handle("GET", "/api/hello", func(http.ResponseWriter, *http.Request) {}); err == nil {
		t.Error("duplicate route should error")
	}

	if rec := do(t, srv, "GET", "/api/hello", nil, nil); rec.Body.String() != "hi" {
		t.Fatalf("hello = %q", rec.Body.String())
	}
	if rec := do(t, srv, "GET", "/api/item/42", nil, nil); rec.Body.String() != "42" {
		t.Fatalf("param = %q", rec.Body.String())
	}
	if rec := do(t, srv, "GET", "/api/item/42/extra", nil, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("extra segment should not match, got %d", rec.Code)
	}
	if rec := do(t, srv, "POST", "/api/hello", nil, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("wrong verb should not match, got %d", rec.Code)
	}
}

func TestRegistryListingAndEnable(t *testing.T) {
	srv := newServer(t, Options{})
	if err := srv.Handle("GET", "/api/x", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "x")
	}); err != nil {
		t.Fatal(err)
	}
	routes := srv.Registry().Routes()
	found := false
	for _, rt := range routes {
		if rt.Method == "GET" && rt.Pattern == "/api/x" && rt.Enabled {
			found = true
		}
	}
	if !found {
		t.Fatalf("route not listed: %+v", routes)
	}
	if !srv.Registry().SetEnabled("GET", "/api/x", false) {
		t.Fatal("SetEnabled returned false")
	}
	if rec := do(t, srv, "GET", "/api/x", nil, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("disabled route should 404, got %d", rec.Code)
	}
	if !srv.Registry().Remove("GET", "/api/x") {
		t.Fatal("Remove returned false")
	}
}

func TestVaultCRUDAndEncryptionAtRest(t *testing.T) {
	root := t.TempDir()
	srv := newServer(t, Options{Root: root})
	v := srv.Secrets()
	if err := v.Set("notes", "stripe_key", "sk_live_secret_value", "payments"); err != nil {
		t.Fatal(err)
	}
	if got, ok := v.Get("notes", "stripe_key"); !ok || got != "sk_live_secret_value" {
		t.Fatalf("Get = %q ok=%v", got, ok)
	}
	list := v.List("notes")
	if len(list) != 1 || list[0].Name != "stripe_key" || list[0].Note != "payments" {
		t.Fatalf("List = %+v", list)
	}
	// on disk the value must not appear in plaintext
	data, err := os.ReadFile(filepath.Join(root, "secrets", "notes.json"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("sk_live_secret_value")) {
		t.Fatal("secret stored in plaintext on disk")
	}
	if !v.Delete("notes", "stripe_key") {
		t.Fatal("Delete returned false")
	}
	if _, ok := v.Get("notes", "stripe_key"); ok {
		t.Fatal("secret still present after delete")
	}
}

func TestVaultShare(t *testing.T) {
	srv := newServer(t, Options{})
	v := srv.Secrets()
	_ = v.Set("notes", "api", "value-123", "")
	tok, exp, err := v.Share("notes", "api", ShareOptions{TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if tok == "" || exp.Before(time.Now()) {
		t.Fatalf("bad share %q %v", tok, exp)
	}
	if got, ok := v.Resolve(tok); !ok || got != "value-123" {
		t.Fatalf("Resolve = %q ok=%v", got, ok)
	}
	if !v.Revoke(tok) {
		t.Fatal("Revoke false")
	}
	if _, ok := v.Resolve(tok); ok {
		t.Fatal("token usable after revoke")
	}
	// expiry
	tok2, _, err := v.Share("notes", "api", ShareOptions{TTL: time.Nanosecond})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Millisecond)
	if _, ok := v.Resolve(tok2); ok {
		t.Fatal("expired token still valid")
	}
	if _, _, err := v.Share("notes", "missing", ShareOptions{}); err == nil {
		t.Fatal("sharing a missing secret should error")
	}
}

func TestHeadManager(t *testing.T) {
	srv := newServer(t, Options{})
	h := srv.Head()
	h.Add(`<meta name="a" content="1">`)
	h.Add(`<meta name="a" content="1">`) // duplicate
	h.Add(`<meta name="b" content="2">`)
	h.AddFor("notes", `<meta name="n" content="3">`)
	h.Title("Default Title")
	h.TitleFor("notes", "Notes Title")

	frags := h.Fragments("notes")
	if len(frags) != 3 {
		t.Fatalf("fragments = %v", frags)
	}
	if frags[0] != `<meta name="a" content="1">` || frags[2] != `<meta name="n" content="3">` {
		t.Fatalf("order wrong: %v", frags)
	}
	if got := h.TitleValue("notes"); got != "Notes Title" {
		t.Fatalf("title = %q", got)
	}
	if got := h.TitleValue("other"); got != "Default Title" {
		t.Fatalf("title = %q", got)
	}
	out := string(h.Inject([]byte("<html><head></head></html>"), "notes"))
	if !strings.Contains(out, `<meta name="n" content="3">`) {
		t.Fatalf("inject missed fragment: %q", out)
	}
	if !h.RemoveFor("notes", `<meta name="n" content="3">`) {
		t.Fatal("RemoveFor false")
	}
	if len(h.Fragments("notes")) != 2 {
		t.Fatal("fragment not removed")
	}
}

func TestHeaderPolicy(t *testing.T) {
	srv := newServer(t, Options{})
	srv.Headers().Response("/api/**").Set("X-Policy", "yes")
	srv.Headers().Request("X-Client-*").Set("X-Seen", "yes")
	srv.Headers().Internal("X-Platform-View").FromPath(2)

	if err := srv.Handle("GET", "/api/echo", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, r.Header.Get("X-Seen")+"|"+r.Header.Get("X-Platform-App")+"|"+r.Header.Get("X-Platform-View"))
	}); err != nil {
		t.Fatal(err)
	}
	rec := do(t, srv, "GET", "/api/echo", nil, map[string]string{
		"X-Client-Id": "abc", "X-Platform-App": "spoofed",
	})
	if rec.Header().Get("X-Policy") != "yes" {
		t.Fatalf("response rule not applied: %q", rec.Header().Get("X-Policy"))
	}
	parts := strings.Split(rec.Body.String(), "|")
	if parts[0] != "yes" {
		t.Errorf("request set rule: %q", rec.Body.String())
	}
	if parts[1] != "api" {
		t.Errorf("X-Platform-App should be resolved from path, got %q", parts[1])
	}
	if parts[2] != "echo" {
		t.Errorf("internal fromPath: %q", parts[2])
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("baseline nosniff missing")
	}
}

func TestGzipCompression(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "apps", "notes", "index.html"),
		"<html><head></head><body>"+strings.Repeat("hello ", 200)+"</body></html>")
	srv := newServer(t, Options{Root: root})

	req := httptest.NewRequest("GET", "/notes/", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if enc := rec.Header().Get("Content-Encoding"); enc != "gzip" {
		t.Fatalf("Content-Encoding = %q", enc)
	}
	zr, err := gzip.NewReader(rec.Body)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "hello hello") {
		t.Fatalf("decompressed body wrong: %q", string(body))
	}
}

func TestSecretsHTTP(t *testing.T) {
	srv := newServer(t, Options{})
	// PUT a secret
	rec := do(t, srv, "PUT", "/secrets/notes/stripe", strings.NewReader(`{"value":"sk_123","note":"pay"}`), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT %d %q", rec.Code, rec.Body.String())
	}
	// GET value
	rec = do(t, srv, "GET", "/secrets/notes/stripe", nil, nil)
	var got map[string]any
	decode(t, rec, &got)
	if got["value"] != "sk_123" {
		t.Fatalf("GET value = %+v", got)
	}
	// LIST
	rec = do(t, srv, "GET", "/secrets/notes", nil, nil)
	if !strings.Contains(rec.Body.String(), "stripe") || strings.Contains(rec.Body.String(), "sk_123") {
		t.Fatalf("list leaked or missed: %q", rec.Body.String())
	}
	// SHARE
	rec = do(t, srv, "POST", "/secrets/notes/stripe/share", strings.NewReader(`{"ttl_seconds":60}`), nil)
	decode(t, rec, &got)
	tok, _ := got["token"].(string)
	if tok == "" {
		t.Fatalf("no share token: %q", rec.Body.String())
	}
	// RESOLVE
	rec = do(t, srv, "GET", "/secrets/notes/stripe/share/"+tok, nil, nil)
	got = nil
	decode(t, rec, &got)
	if got["value"] != "sk_123" {
		t.Fatalf("resolve = %+v", got)
	}
	// DELETE
	rec = do(t, srv, "DELETE", "/secrets/notes/stripe", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("DELETE %d", rec.Code)
	}
}

func TestHeadHTTP(t *testing.T) {
	srv := newServer(t, Options{})
	rec := do(t, srv, "POST", "/head", strings.NewReader(`{"fragment":"<meta name=\"x\">","title":"T"}`), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /head %d %q", rec.Code, rec.Body.String())
	}
	rec = do(t, srv, "GET", "/head", nil, nil)
	if !strings.Contains(rec.Body.String(), `name=\"x\"`) && !strings.Contains(rec.Body.String(), "name=") {
		t.Fatalf("GET /head %q", rec.Body.String())
	}
	rec = do(t, srv, "POST", "/head/notes", strings.NewReader(`{"fragment":"<meta name=\"app\">"}`), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /head/notes %d", rec.Code)
	}
	rec = do(t, srv, "DELETE", "/head/notes", strings.NewReader(`{"fragment":"<meta name=\"app\">"}`), nil)
	if !strings.Contains(rec.Body.String(), "true") {
		t.Fatalf("DELETE /head/notes %q", rec.Body.String())
	}
}

func TestHeadersHTTP(t *testing.T) {
	srv := newServer(t, Options{})
	rec := do(t, srv, "GET", "/headers", nil, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "X-Platform-*") {
		t.Fatalf("GET /headers %d %q", rec.Code, rec.Body.String())
	}
	rec = do(t, srv, "POST", "/headers",
		strings.NewReader(`{"direction":"response","pattern":"/api/**","name":"Cache-Control","value":"no-store"}`), nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "no-store") {
		t.Fatalf("POST /headers %d %q", rec.Code, rec.Body.String())
	}
	rec = do(t, srv, "DELETE", "/headers",
		strings.NewReader(`{"direction":"response","pattern":"/api/**"}`), nil)
	if !strings.Contains(rec.Body.String(), "true") {
		t.Fatalf("DELETE /headers %q", rec.Body.String())
	}
}

func TestManagementView(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "apps", "notes", "index.html"), "<html></html>")
	srv := newServer(t, Options{Root: root, Title: "Test Platform"})
	rec := do(t, srv, "GET", "/", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("view status %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"Test Platform", "notes", "/secrets", "/headers", "/api/functions"} {
		if !strings.Contains(body, want) {
			t.Errorf("view missing %q", want)
		}
	}
	css := do(t, srv, "GET", "/_atp/view.css", nil, nil)
	if css.Code != http.StatusOK || !strings.Contains(css.Header().Get("Content-Type"), "css") {
		t.Fatalf("css %d %q", css.Code, css.Header().Get("Content-Type"))
	}
	// headless hides the view
	hs := newServer(t, Options{Root: root, Headless: true})
	if rec := do(t, hs, "GET", "/", nil, nil); rec.Code == http.StatusOK && strings.Contains(rec.Body.String(), "Platform surfaces") {
		t.Fatalf("headless still shows management view")
	}
}

func TestPlatformCapabilityFunctions(t *testing.T) {
	srv := newServer(t, Options{})
	// issue
	rec := do(t, srv, "POST", "/api/keys",
		strings.NewReader(`{"subject":"user@example","scopes":["read:orders"],"ttl_seconds":3600}`), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("issue %d %q", rec.Code, rec.Body.String())
	}
	var issued map[string]any
	decode(t, rec, &issued)
	raw, _ := issued["token"].(string)
	if raw == "" {
		t.Fatalf("no token: %q", rec.Body.String())
	}
	// verify
	rec = do(t, srv, "POST", "/api/token/verify", strings.NewReader(`{"token":"`+raw+`"}`), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("verify %d %q", rec.Code, rec.Body.String())
	}
	// verify bad
	rec = do(t, srv, "POST", "/api/token/verify", strings.NewReader(`{"token":"garbage"}`), nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("bad verify should 401, got %d", rec.Code)
	}
	// provider authenticate with the token
	rec = do(t, srv, "POST", "/api/provider/authenticate", nil, map[string]string{"Authorization": "Bearer " + raw})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "user@example") {
		t.Fatalf("provider %d %q", rec.Code, rec.Body.String())
	}
	// firewall rules
	rec = do(t, srv, "GET", "/api/firewall/rules", nil, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "default_allow") {
		t.Fatalf("firewall list %d %q", rec.Code, rec.Body.String())
	}
	rec = do(t, srv, "POST", "/api/firewall/rules", strings.NewReader(`{"action":"deny","path_glob":"/blocked/**","enabled":true}`), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("firewall add %d %q", rec.Code, rec.Body.String())
	}
	// ratelimit
	rec = do(t, srv, "GET", "/api/ratelimit/status", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("ratelimit %d", rec.Code)
	}
	// functions listing
	rec = do(t, srv, "GET", "/api/functions", nil, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "/api/keys") {
		t.Fatalf("functions %d %q", rec.Code, rec.Body.String())
	}
	// JSCryptoKey
	rec = do(t, srv, "GET", "/api/keys/javascript?scope=notes&usage=encrypt", nil, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "algorithm") {
		t.Fatalf("js key %d %q", rec.Code, rec.Body.String())
	}
}

func TestPodMounted(t *testing.T) {
	srv := newServer(t, Options{})
	rec := do(t, srv, "GET", "/pod/health", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("pod health %d %q", rec.Code, rec.Body.String())
	}
	// create a table through the pod surface, then query it
	rec = do(t, srv, "POST", "/pod/table/contacts",
		strings.NewReader(`{"name":"Ada","email":"ada@example"}`),
		map[string]string{"Content-Type": "application/json"})
	if rec.Code != http.StatusOK && rec.Code != http.StatusCreated {
		t.Fatalf("pod upsert %d %q", rec.Code, rec.Body.String())
	}
	rec = do(t, srv, "GET", "/pod/table/contacts?format=json", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("pod query %d %q", rec.Code, rec.Body.String())
	}
}

func TestMiddlewareModeDelegates(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "apps", "notes", "index.html"), "<html><head></head><body>app</body></html>")
	srv, err := New(Options{Root: root, Secret: testSecret})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	host := http.NewServeMux()
	host.Handle("/platform/", http.StripPrefix("/platform", srv))
	host.HandleFunc("/own", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "host owns this")
	})
	mux := srv.Middleware(host)

	rec := do(t, mux, "GET", "/own", nil, nil)
	if rec.Code != http.StatusOK || rec.Body.String() != "host owns this" {
		t.Fatalf("delegation failed: %d %q", rec.Code, rec.Body.String())
	}
	// atp-owned path is handled by atp
	rec = do(t, mux, "GET", "/secrets", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("atp path not handled: %d", rec.Code)
	}
	// the app is owned by song inside atp
	rec = do(t, mux, "GET", "/notes/", nil, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "app") {
		t.Fatalf("app not served in middleware mode: %d %q", rec.Code, rec.Body.String())
	}
}

func TestStaticEncryptionAtRest(t *testing.T) {
	root := t.TempDir()
	srv := newServer(t, Options{Root: root})
	if err := srv.StaticBox().Put("notes/secret.txt", "text/plain", []byte("top secret content")); err != nil {
		t.Fatal(err)
	}
	// on disk it must not be plaintext
	found := false
	_ = filepath.WalkDir(filepath.Join(root, ".atp", "static"), func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		data, _ := os.ReadFile(p)
		if bytes.Contains(data, []byte("top secret content")) {
			found = true
		}
		return nil
	})
	if found {
		t.Fatal("static asset stored in plaintext")
	}
	// served decrypted
	rec := do(t, srv, "GET", "/_atp/static/notes/secret.txt", nil, nil)
	if rec.Code != http.StatusOK || rec.Body.String() != "top secret content" {
		t.Fatalf("serve box: %d %q", rec.Code, rec.Body.String())
	}
	if got, _, ok := srv.StaticBox().Get("notes/secret.txt"); !ok || string(got) != "top secret content" {
		t.Fatalf("Get: %q ok=%v", got, ok)
	}
}

func TestEncryptOptionOpensPod(t *testing.T) {
	srv := newServer(t, Options{Encrypt: true})
	if !srv.Encrypted() {
		t.Fatal("Encrypted() false")
	}
	if !srv.Pod().Encrypted() {
		t.Fatal("pod store not encrypted")
	}
}

// TestHeaderPolicyFluentChain locks in the exact chained form the README
// documents for header management (rules for both directions plus the internal
// namespace in one expression).
func TestHeaderPolicyFluentChain(t *testing.T) {
	srv := newServer(t, Options{})
	srv.Headers().
		Request("X-Forwarded-For").TrustProxy(true).
		Request("X-Platform-*").Strip().
		Response("/api/**").Set("X-Chain", "yes").
		Internal("X-Platform-App").FromPath(1)

	if err := srv.Handle("GET", "/api/chain", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, r.Header.Get("X-Platform-App"))
	}); err != nil {
		t.Fatal(err)
	}
	rec := do(t, srv, "GET", "/api/chain", nil, map[string]string{"X-Platform-App": "spoofed"})
	if rec.Header().Get("X-Chain") != "yes" {
		t.Fatalf("chained response rule not applied: %q", rec.Header().Get("X-Chain"))
	}
	if got := rec.Body.String(); got != "api" {
		t.Fatalf("internal FromPath(1) = %q, want \"api\" (spoof must be stripped)", got)
	}
}
