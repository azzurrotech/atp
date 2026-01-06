//go:build ignore
// +build ignore

// file: atp_test.go
//
// Run the tests with:
//     go run atp_test.go
//
// The file is ignored by the regular `go build` because of the build tag above,
// so it won’t clash with the real `main()` function of the server.

package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
)

/*
	------------------------------------------------------------------
	  Tiny assertion helper (fails fast with a clear message)

-------------------------------------------------------------------
*/
func assert(cond bool, msg string, args ...any) {
	if !cond {
		log.Fatalf("FAIL: "+msg, args...)
	}
}

/*
	------------------------------------------------------------------
	  1️⃣  Happy‑path tests (unchanged)

-------------------------------------------------------------------
*/
func testCreateUser() {
	// Reset global state
	users = []User{}
	attempts = make(map[string]*attemptInfo)

	u, err := createUser("alice@example.com", "s3cr3t")
	assert(err == nil, "createUser error: %v", err)
	assert(u.Username == "alice@example.com")
	assert(u.ID != "")
	assert(u.Salt != "")
	assert(u.Hash != "")

	saltBytes, _ := base64.RawStdEncoding.DecodeString(u.Salt)
	hash := computeHash(saltBytes, "s3cr3t")
	assert(hash == u.Hash, "hash mismatch for correct password")
	assert(computeHash(saltBytes, "wrong") != u.Hash,
		"hash should differ for wrong password")
}

func testToken() {
	if len(users) == 0 {
		log.Fatal("testToken: no user available")
	}
	user := users[0]

	tok, err := makeToken(user.ID)
	assert(err == nil, "makeToken error: %v", err)
	assert(tok != "")

	assert(verifyToken(user.ID, tok), "fresh token should verify")
	bad := tok[:len(tok)-1] + "A"
	assert(!verifyToken(user.ID, bad), "tampered token must be rejected")
}

func testRateLimit() {
	ip := "192.0.2.1"
	for i := 0; i < maxAttempts; i++ {
		allowed, wait := allowAttempt(ip)
		assert(allowed, "attempt %d should be allowed, got wait=%d", i+1, wait)
	}
	allowed, wait := allowAttempt(ip)
	assert(!allowed, "extra attempt should be blocked")
	assert(wait > 0, "wait time should be >0 for blocked attempt")
}

/*
	------------------------------------------------------------------
	  2️⃣  Happy‑path HTTP handler tests (unchanged)

-------------------------------------------------------------------
*/
func testHandlersHappyPath() {
	// ---- Register -------------------------------------------------
	regBody := `{"login":"bob@example.com","password":"p@ssw0rd"}`
	req := httptest.NewRequest(http.MethodPost, "/api/auth/register", strings.NewReader(regBody))
	rec := httptest.NewRecorder()
	registerHandler(rec, req)
	assert(rec.Code == http.StatusOK,
		"registerHandler returned %d, want 200", rec.Code)

	// ---- Login ----------------------------------------------------
	loginBody := `{"login":"bob@example.com","password":"p@ssw0rd"}`
	req = httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(loginBody))
	rec = httptest.NewRecorder()
	loginHandler(rec, req)
	assert(rec.Code == http.StatusOK,
		"loginHandler returned %d, want 200", rec.Code)

	var resp loginResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		log.Fatalf("decode login response: %v", err)
	}
	assert(resp.Token != "", "login response missing token")

	// ---- Upload (protected) ---------------------------------------
	uploadPayload := "my secret context"
	req = httptest.NewRequest(http.MethodPost, "/api/context/upload", strings.NewReader(uploadPayload))
	req.Header.Set("Authorization", "Bearer "+resp.Token)
	rec = httptest.NewRecorder()
	uploadHandler(rec, req)
	assert(rec.Code == http.StatusCreated,
		"uploadHandler returned %d, want 201", rec.Code)

	// ---- Download (protected) -------------------------------------
	req = httptest.NewRequest(http.MethodGet, "/api/context/download", nil)
	req.Header.Set("Authorization", "Bearer "+resp.Token)
	rec = httptest.NewRecorder()
	downloadHandler(rec, req)
	assert(rec.Code == http.StatusOK,
		"downloadHandler returned %d, want 200", rec.Code)
	assert(strings.TrimSpace(rec.Body.String()) == uploadPayload,
		"downloaded payload differs")
}

/* ------------------------------------------------------------------
   3️⃣  Error‑condition tests
------------------------------------------------------------------- */

// ----- Malformed JSON (register & login) ----------------------------
func testMalformedJSON() {
	badJSON := `{"login":"charlie@example.com","password":}` // missing value

	// Register
	req := httptest.NewRequest(http.MethodPost, "/api/auth/register", strings.NewReader(badJSON))
	rec := httptest.NewRecorder()
	registerHandler(rec, req)
	assert(rec.Code == http.StatusBadRequest,
		"registerHandler with malformed JSON returned %d, want 400", rec.Code)

	// Login
	req = httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(badJSON))
	rec = httptest.NewRecorder()
	loginHandler(rec, req)
	assert(rec.Code == http.StatusBadRequest,
		"loginHandler with malformed JSON returned %d, want 400", rec.Code)
}

// ----- Missing Authorization header (protected endpoints) ----------
func testMissingAuthHeader() {
	// Create a user so that the upload handler has something to look up
	_, _ = createUser("dana@example.com", "pwd123")

	// Upload without Authorization
	req := httptest.NewRequest(http.MethodPost, "/api/context/upload", strings.NewReader("data"))
	rec := httptest.NewRecorder()
	uploadHandler(rec, req)
	assert(rec.Code == http.StatusUnauthorized,
		"uploadHandler without auth returned %d, want 401", rec.Code)

	// Download without Authorization
	req = httptest.NewRequest(http.MethodGet, "/api/context/download", nil)
	rec = httptest.NewRecorder()
	downloadHandler(rec, req)
	assert(rec.Code == http.StatusUnauthorized,
		"downloadHandler without auth returned %d, want 401", rec.Code)
}

/*
	------------------------------------------------------------------
	  Main – run all tests

-------------------------------------------------------------------
*/
func main() {
	fmt.Println("=== Running manual tests ===")

	// Happy‑path checks
	testCreateUser()
	fmt.Println("✔ createUser & hashing – PASS")
	testToken()
	fmt.Println("✔ token generation/verification – PASS")
	testRateLimit()
	fmt.Println("✔ rate‑limit logic – PASS")
	testHandlersHappyPath()
	fmt.Println("✔ HTTP handlers (happy path) – PASS")

	// Error‑condition checks
	testMalformedJSON()
	fmt.Println("✔ malformed‑JSON handling – PASS")
	testMissingAuthHeader()
	fmt.Println("✔ missing‑auth‑header handling – PASS")

	fmt.Println("\nAll manual tests passed!")
}
