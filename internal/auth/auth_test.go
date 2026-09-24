package auth

import "testing"

func TestLoginAuthenticateLogout(t *testing.T) {
	m := NewManager("admin", "s3cret")
	tok, err := m.Login("admin", "s3cret")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if tok == "" || len(tok) != 64 {
		t.Errorf("token should be 32 random bytes hex, got %q", tok)
	}
	if !m.Authenticate(tok) {
		t.Fatal("fresh token must authenticate")
	}
	m.Logout(tok)
	if m.Authenticate(tok) {
		t.Fatal("logged-out token must not authenticate")
	}
}

func TestLoginRejectsBadPassword(t *testing.T) {
	m := NewManager("admin", "s3cret")
	if _, err := m.Login("admin", "wrong"); err == nil {
		t.Fatal("wrong password must fail")
	}
	if _, err := m.Login("other", "s3cret"); err == nil {
		t.Fatal("wrong user must fail")
	}
}

func TestVerify(t *testing.T) {
	m := NewManager("op", "pass")
	if !m.Verify("op", "pass") {
		t.Fatal("correct credentials must verify")
	}
	if m.Verify("op", "nope") || m.Verify("nope", "pass") {
		t.Fatal("bad credentials must not verify")
	}
}

func TestDistinctSalts(t *testing.T) {
	m1 := NewManager("admin", "same")
	m2 := NewManager("admin", "same")
	if string(m1.hash) == string(m2.hash) {
		t.Fatal("salts must differ between instances")
	}
}