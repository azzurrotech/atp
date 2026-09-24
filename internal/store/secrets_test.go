package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testMaster = "test-master-secret-0123456789abcdef-extra-bytes"

func newTestVault(t *testing.T) (*SecretsVault, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "data")
	v, err := NewSecretsVault(root, testMaster)
	if err != nil {
		t.Fatalf("NewSecretsVault: %v", err)
	}
	return v, root
}

func TestSecretSetGetListDelete(t *testing.T) {
	v, _ := newTestVault(t)
	sec, err := v.Set("acme", "api_key", "sk_live_123", "stripe")
	if err != nil {
		t.Fatal(err)
	}
	if sec.Value != "" {
		t.Error("Set must not return the plaintext value")
	}

	got, err := v.Get("acme", "api_key")
	if err != nil {
		t.Fatal(err)
	}
	if got.Value != "sk_live_123" || got.Note != "stripe" {
		t.Errorf("round trip failed: %+v", got)
	}

	list, err := v.List("acme")
	if err != nil || len(list) != 1 || list[0].Name != "api_key" || list[0].Value != "" {
		t.Errorf("List wrong: %+v err=%v", list, err)
	}

	if err := v.Delete("acme", "api_key"); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Get("acme", "api_key"); err == nil {
		t.Error("deleted secret should not be readable")
	}
}

func TestSecretEncryptedAtRest(t *testing.T) {
	v, root := newTestVault(t)
	if _, err := v.Set("acme", "token", "super-secret-plaintext-value-xyz", ""); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, "atp", "secrets", "acme.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "super-secret-plaintext-value-xyz") {
		t.Fatal("plaintext found in the sealed file!")
	}
}

func TestSecretRotate(t *testing.T) {
	v, root := newTestVault(t)
	v.Set("acme", "a", "one", "")
	v.Set("acme", "b", "two", "")

	if err := v.Rotate(testMaster + "-rotated"); err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if _, err := v.Get("acme", "a"); err != nil {
		t.Fatal("old vault lost after rotate")
	}
	// A fresh vault with the NEW master secret must read the rows.
	v2, err := NewSecretsVault(root, testMaster+"-rotated")
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"a": "one", "b": "two"} {
		got, err := v2.Get("acme", name)
		if err != nil || got.Value != want {
			t.Errorf("after rotate %s = %q err=%v, want %q", name, got, err, want)
		}
	}
}

func TestSecretRequiresMasterOf32(t *testing.T) {
	if _, err := NewSecretsVault(t.TempDir(), "short"); err == nil {
		t.Fatal("short master secret must be rejected")
	}
}

func TestSiloSizeCountsOnDisk(t *testing.T) {
	root := filepath.Join(t.TempDir(), "data")
	v, _ := NewSecretsVault(root, testMaster) // exercises vault path
	_ = v
	// Simulate a song silo and pod namespace on disk.
	dirs := []string{
		filepath.Join(root, "song", "acme"),
		filepath.Join(root, "song", "acme", ".song"),
		filepath.Join(root, "pod", "acme", "notes"),
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite(t, filepath.Join(root, "song", "acme", "index.html"), "hello")
	mustWrite(t, filepath.Join(root, "song", "acme", "app.js"), "abcd")
	mustWrite(t, filepath.Join(root, "song", "acme", ".song", "meta.json"), "skip-me-hidden-meta")
	mustWrite(t, filepath.Join(root, "pod", "acme", "notes", "1.json"), "pod-record")

	songB, podB, err := SiloSize(root, "acme")
	if err != nil {
		t.Fatal(err)
	}
	// Hidden .song meta must not be counted for the song side.
	if songB != 9 { // "hello" + "abcd"
		t.Errorf("song bytes = %d, want 9", songB)
	}
	if podB != int64(len("pod-record")) {
		t.Errorf("pod bytes = %d, want %d", podB, len("pod-record"))
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}