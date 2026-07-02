package crypto

import (
	"bytes"
	"crypto/sha256"
	"testing"
)

// mustKEK builds a KEK whose 32-byte material is derived from the full id, so
// distinct ids always yield distinct key material (same-length ids must not
// collide).
func mustKEK(t *testing.T, id string) *KEK {
	t.Helper()
	sum := sha256.Sum256([]byte("kek-material:" + id))
	k, err := NewKEKFromMaterial(id, sum[:])
	if err != nil {
		t.Fatalf("NewKEKFromMaterial(%q): %v", id, err)
	}
	return k
}

func TestSealOpenRoundTrip(t *testing.T) {
	key := bytes.Repeat([]byte{0x5a}, keySize)
	plaintext := []byte("the crow flies at midnight")
	aad := []byte("aad|type:secret")

	nonce, ct, err := seal(key, plaintext, aad)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if len(nonce) != nonceSize {
		t.Fatalf("nonce size = %d, want %d", len(nonce), nonceSize)
	}
	if bytes.Equal(ct, plaintext) {
		t.Fatal("ciphertext equals plaintext")
	}

	got, err := open(key, nonce, ct, aad)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Fatalf("open = %q, want %q", got, plaintext)
	}
}

func TestOpenRejectsTamper(t *testing.T) {
	key := bytes.Repeat([]byte{0x11}, keySize)
	aad := []byte("aad")
	nonce, ct, err := seal(key, []byte("payload"), aad)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}

	t.Run("flipped ciphertext byte", func(t *testing.T) {
		bad := bytes.Clone(ct)
		bad[0] ^= 0xff
		if _, err := open(key, nonce, bad, aad); err != errAEAD {
			t.Fatalf("open(tampered ct) err = %v, want errAEAD", err)
		}
	})
	t.Run("flipped tag byte", func(t *testing.T) {
		bad := bytes.Clone(ct)
		bad[len(bad)-1] ^= 0x01
		if _, err := open(key, nonce, bad, aad); err != errAEAD {
			t.Fatalf("open(tampered tag) err = %v, want errAEAD", err)
		}
	})
	t.Run("wrong aad", func(t *testing.T) {
		if _, err := open(key, nonce, ct, []byte("other")); err != errAEAD {
			t.Fatalf("open(wrong aad) err = %v, want errAEAD", err)
		}
	})
	t.Run("wrong key", func(t *testing.T) {
		other := bytes.Repeat([]byte{0x22}, keySize)
		if _, err := open(other, nonce, ct, aad); err != errAEAD {
			t.Fatalf("open(wrong key) err = %v, want errAEAD", err)
		}
	})
	t.Run("truncated nonce", func(t *testing.T) {
		if _, err := open(key, nonce[:nonceSize-1], ct, aad); err != errAEAD {
			t.Fatalf("open(short nonce) err = %v, want errAEAD", err)
		}
	})
	t.Run("oversized nonce", func(t *testing.T) {
		if _, err := open(key, append(bytes.Clone(nonce), 0x00), ct, aad); err != errAEAD {
			t.Fatalf("open(long nonce) err = %v, want errAEAD", err)
		}
	})
}

func TestSealFreshNoncePerCall(t *testing.T) {
	key := bytes.Repeat([]byte{0x33}, keySize)
	seen := make(map[string]bool)
	for i := 0; i < 256; i++ {
		nonce, _, err := seal(key, []byte("x"), nil)
		if err != nil {
			t.Fatalf("seal #%d: %v", i, err)
		}
		if seen[string(nonce)] {
			t.Fatalf("nonce reuse detected at iteration %d", i)
		}
		seen[string(nonce)] = true
	}
}

func TestSealPackedRoundTripAndBounds(t *testing.T) {
	key := bytes.Repeat([]byte{0x44}, keySize)
	aad := []byte("packed-aad")
	packed, err := sealPacked(key, []byte("hello"), aad)
	if err != nil {
		t.Fatalf("sealPacked: %v", err)
	}
	if len(packed) <= nonceSize {
		t.Fatalf("packed length %d not greater than nonce size", len(packed))
	}
	got, err := openPacked(key, packed, aad)
	if err != nil {
		t.Fatalf("openPacked: %v", err)
	}
	if string(got) != "hello" {
		t.Fatalf("openPacked = %q, want hello", got)
	}
	if _, err := openPacked(key, packed[:nonceSize-1], aad); err != errAEAD {
		t.Fatalf("openPacked(too short) err = %v, want errAEAD", err)
	}
}

func TestNewGCMKeySizeValidation(t *testing.T) {
	for _, n := range []int{0, 16, 24, 31, 33, 64} {
		if _, err := newGCM(make([]byte, n)); err == nil {
			t.Errorf("newGCM(%d-byte key) = nil error, want error", n)
		}
	}
	if _, err := newGCM(make([]byte, keySize)); err != nil {
		t.Errorf("newGCM(%d-byte key) unexpected error: %v", keySize, err)
	}
}

func TestRandomBytesLengthAndVariance(t *testing.T) {
	a, err := randomBytes(32)
	if err != nil {
		t.Fatalf("randomBytes: %v", err)
	}
	if len(a) != 32 {
		t.Fatalf("len = %d, want 32", len(a))
	}
	b, _ := randomBytes(32)
	if bytes.Equal(a, b) {
		t.Fatal("two randomBytes calls returned identical output")
	}
}

func TestZero(t *testing.T) {
	b := []byte{1, 2, 3, 4, 5}
	Zero(b)
	for i, v := range b {
		if v != 0 {
			t.Fatalf("byte %d = %d, want 0", i, v)
		}
	}
	Zero(nil) // must not panic
}
