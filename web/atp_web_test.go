package web

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testSecret = "web-test-master-secret-0123456789abcdef-aaaaaaaaa"

func newTestService(t *testing.T) (*ATPService, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "data")
	s, err := NewATPService(Options{
		Root:          root,
		Secret:        testSecret,
		AdminUser:     "admin",
		AdminPassword: "s3cret",
	})
	if err != nil {
		t.Fatalf("NewATPService: %v", err)
	}
	return s, root
}

func doJSON(t *testing.T, h http.Handler, method, path, body string, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rdr)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func login(t *testing.T, h http.Handler) *http.Cookie {
	t.Helper()
	rec := doJSON(t, h, "POST", "/login", `{"user":"admin","password":"s3cret"}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("login: %d %s", rec.Code, rec.Body.String())
	}
	cookies := rec.Result().Cookies()
	for _, c := range cookies {
		if c.Name == sessionCookie {
			return c
		}
	}
	t.Fatal("no session cookie set")
	return nil
}

func mustDecode(t *testing.T, rec *httptest.ResponseRecorder, v any) {
	t.Helper()
	if rec.Code >= 400 {
		t.Fatalf("HTTP %d: %s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), v); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
}

func TestHealth(t *testing.T) {
	s, _ := newTestService(t)
	rec := doJSON(t, s.Handler(), "GET", "/health", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("health: %d", rec.Code)
	}
	var out map[string]any
	mustDecode(t, rec, &out)
	if out["status"] != "healthy" {
		t.Errorf("health status: %v", out["status"])
	}
}

func TestLoginAndAdminGate(t *testing.T) {
	s, _ := newTestService(t)

	// Admin pages redirect unauthenticated browsers to /login.
	req := httptest.NewRequest("GET", "/clients", nil)
	req.Header.Set("Accept", "text/html")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/login" {
		t.Fatalf("unauthenticated page: %d %s", rec.Code, rec.Header().Get("Location"))
	}

	// API rejects unauthenticated with 401.
	rec = doJSON(t, s.Handler(), "GET", "/api/summary", "", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated API: %d", rec.Code)
	}

	// Wrong password.
	rec = doJSON(t, s.Handler(), "POST", "/login", `{"user":"admin","password":"nope"}`, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("bad login: %d", rec.Code)
	}

	// Good login then access.
	cookie := login(t, s.Handler())
	rec = doJSON(t, s.Handler(), "GET", "/api/summary", "", cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("authenticated summary: %d", rec.Code)
	}

	// Dashboard HTML renders.
	req = httptest.NewRequest("GET", "/", nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Admin overview") {
		t.Fatalf("dashboard: %d", rec.Code)
	}
}

func TestClientLifecycleAndSiloProvision(t *testing.T) {
	s, _ := newTestService(t)
	cookie := login(t, s.Handler())

	rec := doJSON(t, s.Handler(), "POST", "/api/clients", `{"id":"acme","name":"Acme Inc","notes":"n"}`, cookie)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create client: %d %s", rec.Code, rec.Body.String())
	}

	// The song silo must have been provisioned.
	silos, err := s.song.Store().ListSilos()
	if err != nil || len(silos) != 1 || silos[0] != "acme" {
		t.Fatalf("silos after create: %v err=%v", silos, err)
	}

	// Reserved id cannot be created.
	rec = doJSON(t, s.Handler(), "POST", "/api/clients", `{"id":"api"}`, cookie)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("reserved id: %d", rec.Code)
	}

	// Update flags.
	rec = doJSON(t, s.Handler(), "PUT", "/api/clients/acme", `{"chargeable":false}`, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("update: %d %s", rec.Code, rec.Body.String())
	}
	var upd map[string]any
	mustDecode(t, rec, &upd)
	c := upd["client"].(map[string]any)
	if c["chargeable"] != false {
		t.Errorf("chargeable should be false: %v", c["chargeable"])
	}

	// Partial update (name only) must not flip chargeable back on.
	rec = doJSON(t, s.Handler(), "PUT", "/api/clients/acme", `{"name":"Acme Corp"}`, cookie)
	mustDecode(t, rec, &upd)
	c = upd["client"].(map[string]any)
	if c["chargeable"] != false || c["name"] != "Acme Corp" {
		t.Errorf("partial update touched extras: %v %v", c["chargeable"], c["name"])
	}
}

func TestPublicClientWebApp(t *testing.T) {
	s, _ := newTestService(t)
	cookie := login(t, s.Handler())
	doJSON(t, s.Handler(), "POST", "/api/clients", `{"id":"acme"}`, cookie)

	// Put a file through the client-scoped song API.
	rec := doJSON(t, s.Handler(), "POST", "/c/acme/api/song/silos/acme/file",
		`{"path":"index.html","content":"<h1>hi acme</h1>","overwrite":true}`, cookie)
	if rec.Code != http.StatusCreated && rec.Code != http.StatusOK {
		t.Fatalf("create file: %d %s", rec.Code, rec.Body.String())
	}

	// It is served publicly with no auth at /c/acme/.
	rec = doJSON(t, s.Handler(), "GET", "/c/acme/", "", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "hi acme") {
		t.Fatalf("public site: %d %s", rec.Code, rec.Body.String())
	}

	// Scoped listing works; another client's silo is refused.
	rec = doJSON(t, s.Handler(), "GET", "/c/acme/api/song/silos/acme/files", "", cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("scoped list: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, s.Handler(), "GET", "/c/acme/api/song/silos/mallory/files", "", cookie)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-silo access must be forbidden, got %d", rec.Code)
	}

	// Non-existent client 404s.
	rec = doJSON(t, s.Handler(), "GET", "/c/nope/", "", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown client site: %d", rec.Code)
	}
}

func TestPodScopedDatabase(t *testing.T) {
	s, _ := newTestService(t)
	cookie := login(t, s.Handler())
	doJSON(t, s.Handler(), "POST", "/api/clients", `{"id":"acme"}`, cookie)

	// Create a table for the client through the admin pass-through.
	rec := doJSON(t, s.Handler(), "POST", "/api/pod/tables",
		`{"table":"acme/contacts","columns":[{"name":"name","type":"text"}]}`, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("create table: %d %s", rec.Code, rec.Body.String())
	}

	// Insert a record via the scoped proxy (this also counts usage).
	rec = doJSON(t, s.Handler(), "POST", "/c/acme/api/pod/table/acme/contacts",
		`{"name":"Alice"}`, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("insert: %d %s", rec.Code, rec.Body.String())
	}
	var ins map[string]any
	mustDecode(t, rec, &ins)
	if ins["created"] != true {
		t.Errorf("insert created flag: %v", ins["created"])
	}

	// Query it back.
	rec = doJSON(t, s.Handler(), "GET", "/c/acme/api/pod/table/acme/contacts?q=Alice", "", cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("query: %d %s", rec.Code, rec.Body.String())
	}
	var q map[string]any
	mustDecode(t, rec, &q)
	if q["count"].(float64) != 1 {
		t.Errorf("query count: %v", q["count"])
	}

	// Cross-namespace access is forbidden.
	rec = doJSON(t, s.Handler(), "GET", "/c/acme/api/pod/table/mallory/contacts", "", cookie)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-namespace: %d", rec.Code)
	}

	// Client-level table listing shows the table.
	rec = doJSON(t, s.Handler(), "GET", "/api/clients/acme/tables", "", cookie)
	var tl struct {
		Tables []struct {
			Name  string `json:"name"`
			Count int    `json:"count"`
		} `json:"tables"`
	}
	mustDecode(t, rec, &tl)
	if len(tl.Tables) != 1 || tl.Tables[0].Name != "acme/contacts" || tl.Tables[0].Count != 1 {
		t.Fatalf("client tables: %+v", tl.Tables)
	}
}

func TestSecretsVaultEndToEnd(t *testing.T) {
	s, root := newTestService(t)
	cookie := login(t, s.Handler())
	doJSON(t, s.Handler(), "POST", "/api/clients", `{"id":"acme"}`, cookie)

	rec := doJSON(t, s.Handler(), "POST", "/api/clients/acme/secrets",
		`{"name":"api_key","value":"sk_very_secret_999","note":"stripe"}`, cookie)
	if rec.Code != http.StatusCreated {
		t.Fatalf("set secret: %d %s", rec.Code, rec.Body.String())
	}

	// Secret is not in plaintext on disk.
	data, err := os.ReadFile(filepath.Join(root, "atp", "secrets", "acme.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "sk_very_secret_999") {
		t.Fatal("secret leaked to disk in plaintext")
	}

	// List hides values.
	var list struct {
		Secrets []map[string]any `json:"secrets"`
	}
	rec = doJSON(t, s.Handler(), "GET", "/api/clients/acme/secrets", "", cookie)
	mustDecode(t, rec, &list)
	if len(list.Secrets) != 1 || list.Secrets[0]["value"] != nil {
		t.Fatalf("list should hide values: %+v", list.Secrets)
	}

	// Get decrypts for the admin.
	var got map[string]any
	rec = doJSON(t, s.Handler(), "GET", "/api/clients/acme/secrets/api_key", "", cookie)
	mustDecode(t, rec, &got)
	if got["value"] != "sk_very_secret_999" {
		t.Fatalf("decrypted value: %v", got["value"])
	}
}

func TestShepherdKeysScopingAndVerify(t *testing.T) {
	s, _ := newTestService(t)
	cookie := login(t, s.Handler())
	doJSON(t, s.Handler(), "POST", "/api/clients", `{"id":"acme"}`, cookie)

	rec := doJSON(t, s.Handler(), "POST", "/api/clients/acme/keys", `{"subject":"u1","ttl":"1h"}`, cookie)
	if rec.Code != http.StatusCreated {
		t.Fatalf("issue key: %d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Token  string   `json:"token"`
		Claims struct {
			Scopes []string `json:"scp"`
		} `json:"claims"`
	}
	mustDecode(t, rec, &out)
	if len(out.Claims.Scopes) != 1 || out.Claims.Scopes[0] != "client:acme" {
		t.Fatalf("token scopes: %v", out.Claims.Scopes)
	}

	// Public verify endpoint accepts the token via header.
	req := httptest.NewRequest("GET", "/api/shepherd/token/verify", nil)
	req.Header.Set("Authorization", "Bearer "+out.Token)
	rec2 := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec2, req)
	if rec2.Code != http.StatusOK {
		t.Fatalf("verify: %d %s", rec2.Code, rec2.Body.String())
	}

	// Client verify endpoint insists the scope matches the client.
	rec = doJSON(t, s.Handler(), "GET", "/api/clients/acme/verify?t="+out.Token, "", cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("client verify: %d %s", rec.Code, rec.Body.String())
	}

	// Revoking makes verification fail.
	rec = doJSON(t, s.Handler(), "POST", "/api/clients/acme/revoke", `{"token":"`+out.Token+`"}`, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("revoke: %d %s", rec.Code, rec.Body.String())
	}
	req = httptest.NewRequest("GET", "/api/shepherd/token/verify", nil)
	req.Header.Set("Authorization", "Bearer "+out.Token)
	rec3 := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec3, req)
	if rec3.Code != http.StatusUnauthorized {
		t.Fatalf("verify after revoke: %d", rec3.Code)
	}

	// Block issuance and magic link.
	rec = doJSON(t, s.Handler(), "POST", "/api/clients/acme/keys/block", `{"count":3,"ttl":"1h"}`, cookie)
	if rec.Code != http.StatusCreated {
		t.Fatalf("block: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, s.Handler(), "POST", "/api/clients/acme/keys/magic", `{"next":"/c/acme/"}`, cookie)
	if rec.Code != http.StatusCreated {
		t.Fatalf("magic: %d %s", rec.Code, rec.Body.String())
	}
	var ml struct {
		URL string `json:"url"`
	}
	mustDecode(t, rec, &ml)
	if !strings.Contains(ml.URL, "/api/shepherd/verify?t=") {
		t.Fatalf("magic url: %s", ml.URL)
	}
}

func TestUsageLoggingAndBilling(t *testing.T) {
	s, _ := newTestService(t)
	cookie := login(t, s.Handler())
	doJSON(t, s.Handler(), "POST", "/api/clients", `{"id":"acme"}`, cookie)

	// A few requests against the client's surface (public site + scoped API).
	var body bytes.Buffer
	body.WriteString(`{"path":"index.html","content":"<h1>x</h1>","overwrite":true}`)
	doJSON(t, s.Handler(), "POST", "/c/acme/api/song/silos/acme/file", body.String(), cookie)
	doJSON(t, s.Handler(), "GET", "/c/acme/", "", cookie)

	// Usage must have been recorded against the client.
	rec := doJSON(t, s.Handler(), "GET", "/api/clients/acme/usage?limit=10", "", cookie)
	var logs struct {
		Records []map[string]any `json:"records"`
	}
	mustDecode(t, rec, &logs)
	if len(logs.Records) < 2 {
		t.Fatalf("usage records: %d", len(logs.Records))
	}
	for _, r := range logs.Records {
		if r["client"] != "acme" {
			t.Errorf("record attributed wrong: %v", r["client"])
		}
	}

	// A silo-size sample on the current hour then billing.
	s.usage.SampleSize("acme", 1024*1024*1024) // 1 GiB
	s.usage.Flush()
	rec = doJSON(t, s.Handler(), "GET", "/api/clients/acme/billing", "", cookie)
	var bill map[string]any
	mustDecode(t, rec, &bill)
	if bill["chargeable"] != true {
		t.Errorf("billing chargeable: %v", bill["chargeable"])
	}
	if bill["total_requests"].(float64) < 2 {
		t.Errorf("billing requests: %v", bill["total_requests"])
	}
	if bill["sampled_hours"].(float64) < 1 {
		t.Errorf("sampled hours: %v", bill["sampled_hours"])
	}
}

func TestFirewallAndUpstreams(t *testing.T) {
	s, _ := newTestService(t)
	cookie := login(t, s.Handler())

	rec := doJSON(t, s.Handler(), "POST", "/api/shepherd/firewall/rules",
		`{"name":"block-orders","action":"deny","path_glob":"/orders/**"}`, cookie)
	if rec.Code != http.StatusCreated && rec.Code != http.StatusOK {
		t.Fatalf("add rule: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, s.Handler(), "GET", "/api/shepherd/firewall/rules", "", cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("list rules: %d", rec.Code)
	}
	var fw struct {
		Rules []map[string]any `json:"rules"`
	}
	mustDecode(t, rec, &fw)
	if len(fw.Rules) != 1 {
		t.Fatalf("rules: %+v", fw.Rules)
	}
	id := fw.Rules[0]["id"].(string)
	if fw.Rules[0]["path_glob"] != "/orders/**" {
		t.Errorf("rule path: %v", fw.Rules[0]["path_glob"])
	}

	rec = doJSON(t, s.Handler(), "DELETE", "/api/shepherd/firewall/rules/"+id, "", cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete rule: %d %s", rec.Code, rec.Body.String())
	}

	// Register an upstream and confirm the gateway rebuilds.
	rec = doJSON(t, s.Handler(), "POST", "/api/shepherd/upstreams",
		`{"name":"orders","prefix":"orders","target":"http://localhost:9000"}`, cookie)
	if rec.Code != http.StatusCreated {
		t.Fatalf("add upstream: %d %s", rec.Code, rec.Body.String())
	}
	if s.gw == nil {
		t.Fatal("gateway not rebuilt")
	}
	rec = doJSON(t, s.Handler(), "GET", "/api/shepherd/upstreams", "", cookie)
	var up struct {
		Upstreams []map[string]any `json:"upstreams"`
	}
	mustDecode(t, rec, &up)
	if len(up.Upstreams) != 1 || up.Upstreams[0]["prefix"] != "orders" {
		t.Fatalf("upstreams: %+v", up.Upstreams)
	}
}

func TestMiddlewareMode(t *testing.T) {
	s, _ := newTestService(t)
	hostCalled := false
	host := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hostCalled = true
		fmt.Fprintf(w, "host: %s", r.URL.Path)
	})
	mw := s.Middleware(host)

	// atp-owned path is handled internally.
	rec := doJSON(t, mw, "GET", "/health", "", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "healthy") {
		t.Fatalf("middleware health: %d", rec.Code)
	}
	if hostCalled {
		t.Fatal("health should not reach the host")
	}

	// Foreign path goes to the host.
	rec = doJSON(t, mw, "GET", "/my-app/page", "", nil)
	if !hostCalled || rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "host: /my-app/page") {
		t.Fatalf("middleware foreign path: err=%v body=%s", rec.Body.String(), rec.Body.String())
	}
}

func TestSeedDemo(t *testing.T) {
	root := filepath.Join(t.TempDir(), "data")
	s, err := NewATPService(Options{Root: root, Secret: testSecret, Seed: true, AdminUser: "admin", AdminPassword: "s3cret"})
	if err != nil {
		t.Fatal(err)
	}
	if !s.clients.Exists("demo") {
		t.Fatal("seed did not create the demo client")
	}
	// Demo silo has files.
	files, err := s.song.Store().List("demo", "")
	if err != nil || len(files) == 0 {
		t.Fatalf("demo silo files: %v err=%v", files, err)
	}
	// Demo pod table exists with a record.
	tables := s.clientTableList("demo")
	if len(tables) == 0 {
		t.Fatal("demo pod tables missing")
	}
	// Demo magic link flow: issuing a link and redeeming redirects + sets cookie.
	cookie := login(t, s.Handler())
	rec := doJSON(t, s.Handler(), "POST", "/api/clients/demo/keys/magic", `{"next":"/c/demo/"}`, cookie)
	var ml struct {
		Token string `json:"token"`
	}
	mustDecode(t, rec, &ml)
	req := httptest.NewRequest("GET", "/api/shepherd/verify?t="+ml.Token, nil)
	rec2 := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec2, req)
	if rec2.Code != http.StatusFound || rec2.Header().Get("Location") != "/c/demo/" {
		t.Fatalf("magic redeem: %d loc=%s", rec2.Code, rec2.Header().Get("Location"))
	}
	if !strings.Contains(rec2.Header().Get("Set-Cookie"), "shepherd_cap") {
		t.Fatalf("magic redeem must set shepherd capability cookie: %s", rec2.Header().Get("Set-Cookie"))
	}
}

func TestSeedIdempotent(t *testing.T) {
	root := filepath.Join(t.TempDir(), "data")
	if _, err := NewATPService(Options{Root: root, Secret: testSecret, Seed: true}); err != nil {
		t.Fatal(err)
	}
	s2, err := NewATPService(Options{Root: root, Secret: testSecret, Seed: true})
	if err != nil {
		t.Fatal(err)
	}
	if !s2.clients.Exists("demo") {
		t.Fatal("re-seed must be idempotent (client exists)")
	}
}

func TestShortSecretRejected(t *testing.T) {
	if _, err := NewATPService(Options{Root: t.TempDir(), Secret: "short"}); err == nil {
		t.Fatal("short master secret must be rejected")
	}
}