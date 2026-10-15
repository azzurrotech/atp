package atp

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"errors"
)

// deriveKey turns the master secret into a 32-byte AES-256 key, bound to a
// purpose label so that the vault, the static overlay and pod never share key
// material even though they share one configured secret.
func deriveKey(secret, purpose string) []byte {
	h := sha256.New()
	h.Write([]byte("azzurrotech/atp\x00"))
	h.Write([]byte(purpose))
	h.Write([]byte{0})
	h.Write([]byte(secret))
	return h.Sum(nil)
}

// seal encrypts plain with AES-256-GCM. The returned slice is nonce||ciphertext.
func seal(key, plain []byte) ([]byte, error) {
	g, err := aead(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, g.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return g.Seal(nonce, nonce, plain, nil), nil
}

// open reverses seal.
func open(key, sealed []byte) ([]byte, error) {
	g, err := aead(key)
	if err != nil {
		return nil, err
	}
	ns := g.NonceSize()
	if len(sealed) < ns {
		return nil, errors.New("atp: ciphertext too short")
	}
	return g.Open(nil, sealed[:ns], sealed[ns:], nil)
}

func aead(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
