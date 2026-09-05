// Package crypto implements the encryption subsystem: AES-256-GCM envelope
// encryption with per-version DEKs, KEK management (file- or
// passphrase-derived), opt-in binding-key double wrapping, key-check
// canaries, and token generation/hashing.
//
// Design invariants:
//   - Only authenticated encryption (AES-256-GCM), fresh random nonce per
//     sealing operation, 32-byte keys everywhere.
//   - Every ciphertext is bound to a caller-supplied associated-data string
//     plus an internal domain-separation suffix, so a blob copied between
//     records or between layers fails authentication.
//   - Key material lives only in process memory; this package never persists
//     anything itself.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"runtime"
)

// AlgorithmAES256GCM is the algorithm identifier persisted with ciphertexts.
const AlgorithmAES256GCM = "AES-256-GCM"

const (
	keySize   = 32
	nonceSize = 12
)

var errAEAD = errors.New("authenticated decryption failed")

// seal encrypts plaintext under key bound to aad, returning nonce and
// ciphertext (with the GCM tag appended by the cipher).
func seal(key, plaintext, aad []byte) (nonce, ciphertext []byte, err error) {
	aead, err := newGCM(key)
	if err != nil {
		return nil, nil, err
	}
	nonce = make([]byte, nonceSize)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, nil, fmt.Errorf("generating nonce: %w", err)
	}
	return nonce, aead.Seal(nil, nonce, plaintext, aad), nil
}

// open decrypts ciphertext produced by seal. It returns errAEAD on any
// authentication failure without further detail.
func open(key, nonce, ciphertext, aad []byte) ([]byte, error) {
	aead, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	if len(nonce) != nonceSize {
		return nil, errAEAD
	}
	pt, err := aead.Open(nil, nonce, ciphertext, aad)
	if err != nil {
		return nil, errAEAD
	}
	return pt, nil
}

// sealPacked is seal with nonce||ciphertext packed into one blob.
func sealPacked(key, plaintext, aad []byte) ([]byte, error) {
	nonce, ct, err := seal(key, plaintext, aad)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, len(nonce)+len(ct))
	out = append(out, nonce...)
	return append(out, ct...), nil
}

// openPacked reverses sealPacked.
func openPacked(key, packed, aad []byte) ([]byte, error) {
	if len(packed) < nonceSize {
		return nil, errAEAD
	}
	return open(key, packed[:nonceSize], packed[nonceSize:], aad)
}

func newGCM(key []byte) (cipher.AEAD, error) {
	if len(key) != keySize {
		return nil, fmt.Errorf("key must be %d bytes, got %d", keySize, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// randomBytes returns n cryptographically secure random bytes.
func randomBytes(n int) ([]byte, error) {
	b := make([]byte, n)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		return nil, fmt.Errorf("reading random bytes: %w", err)
	}
	return b, nil
}

// Zero overwrites b with zeros. Use it to discard key material and
// passphrases promptly. (Go can't guarantee no copies exist, but this
// removes the primary buffer.)
func Zero(b []byte) {
	clear(b)
	// KeepAlive prevents the compiler from proving the final overwrite dead
	// before the backing storage's last observable use.
	runtime.KeepAlive(b)
}
