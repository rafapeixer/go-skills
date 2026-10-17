package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/sha256"
)

// newAEAD derives a per-purpose key from a master secret with the standard
// library HKDF and wraps it in AES-GCM that generates its own random nonce.
// Seal prepends the nonce to the ciphertext and Open reads it back, so no
// caller ever picks, counts or stores a nonce.
func newAEAD(master, salt []byte, purpose string) (cipher.AEAD, error) {
	key, err := hkdf.Key(sha256.New, master, salt, purpose, 32)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCMWithRandomNonce(block)
}

func seal(aead cipher.AEAD, plaintext, additionalData []byte) []byte {
	return aead.Seal(nil, nil, plaintext, additionalData)
}

func open(aead cipher.AEAD, ciphertext, additionalData []byte) ([]byte, error) {
	return aead.Open(nil, nil, ciphertext, additionalData)
}
