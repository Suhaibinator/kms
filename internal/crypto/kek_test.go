package crypto

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/argon2"

	"github.com/Suhaibinator/kms/internal/domain"
)

func TestNewKEKFromMaterialValidation(t *testing.T) {
	good := make([]byte, keySize)
	if _, err := NewKEKFromMaterial("", good); err == nil {
		t.Fatal("empty id accepted")
	}
	for _, n := range []int{0, 16, 31, 33} {
		if _, err := NewKEKFromMaterial("id", make([]byte, n)); err == nil {
			t.Errorf("material len %d accepted", n)
		}
	}
	// Material is copied, not aliased: mutating the caller buffer must not
	// change the KEK.
	buf := bytes.Repeat([]byte{0x7e}, keySize)
	kek, err := NewKEKFromMaterial("id", buf)
	if err != nil {
		t.Fatalf("NewKEKFromMaterial: %v", err)
	}
	check, err := NewKeyCheck(kek)
	if err != nil {
		t.Fatalf("NewKeyCheck: %v", err)
	}
	Zero(buf) // scribble over caller's buffer
	if err := VerifyKeyCheck(kek, check); err != nil {
		t.Fatalf("KEK material was aliased to caller buffer: %v", err)
	}
}

func TestKEKDestroy(t *testing.T) {
	kek := mustKEK(t, "destroy")
	kek.Destroy()
	// After Destroy the key is zeroed; a key-check made now would be with a
	// zero key. Just confirm Destroy is nil-safe.
	var nilKEK *KEK
	nilKEK.Destroy()
}

func TestNewKEKID(t *testing.T) {
	id1, err := NewKEKID()
	if err != nil {
		t.Fatalf("NewKEKID: %v", err)
	}
	if !strings.HasPrefix(id1, "kek-") {
		t.Fatalf("id %q missing kek- prefix", id1)
	}
	id2, _ := NewKEKID()
	if id1 == id2 {
		t.Fatal("NewKEKID returned duplicate ids")
	}
}

func TestLoadKEKMaterialFromFile(t *testing.T) {
	dir := t.TempDir()
	material := bytes.Repeat([]byte{0x42}, keySize)

	write := func(name string, data []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, data, 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		return p
	}

	t.Run("raw 32 bytes", func(t *testing.T) {
		p := write("raw", material)
		got, err := LoadKEKMaterialFromFile(p)
		if err != nil || !bytes.Equal(got, material) {
			t.Fatalf("raw load = %x, %v", got, err)
		}
	})
	t.Run("hex with whitespace", func(t *testing.T) {
		p := write("hex", []byte("  "+hex.EncodeToString(material)+"\n"))
		got, err := LoadKEKMaterialFromFile(p)
		if err != nil || !bytes.Equal(got, material) {
			t.Fatalf("hex load = %x, %v", got, err)
		}
	})
	t.Run("std base64", func(t *testing.T) {
		p := write("b64", []byte(base64.StdEncoding.EncodeToString(material)))
		got, err := LoadKEKMaterialFromFile(p)
		if err != nil || !bytes.Equal(got, material) {
			t.Fatalf("b64 load = %x, %v", got, err)
		}
	})
	t.Run("url base64", func(t *testing.T) {
		p := write("b64url", []byte(base64.RawURLEncoding.EncodeToString(material)))
		got, err := LoadKEKMaterialFromFile(p)
		if err != nil || !bytes.Equal(got, material) {
			t.Fatalf("b64url load = %x, %v", got, err)
		}
	})
	t.Run("wrong length", func(t *testing.T) {
		p := write("bad", []byte("not-a-valid-key"))
		if _, err := LoadKEKMaterialFromFile(p); err == nil {
			t.Fatal("bad key material accepted")
		}
	})
	t.Run("missing file", func(t *testing.T) {
		if _, err := LoadKEKMaterialFromFile(filepath.Join(dir, "nope")); err == nil {
			t.Fatal("missing file accepted")
		}
	})
}

func TestWriteKEKMaterialFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "master.key")

	material, err := WriteKEKMaterialFile(p)
	if err != nil {
		t.Fatalf("WriteKEKMaterialFile: %v", err)
	}
	if len(material) != keySize {
		t.Fatalf("material len = %d, want %d", len(material), keySize)
	}
	info, err := os.Stat(p)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("perm = %o, want 600", perm)
	}
	// File round-trips back to the same material (stored as hex).
	loaded, err := LoadKEKMaterialFromFile(p)
	if err != nil || !bytes.Equal(loaded, material) {
		t.Fatalf("reload = %x, %v; want %x", loaded, err, material)
	}
	// Refuses to overwrite an existing file.
	if _, err := WriteKEKMaterialFile(p); err == nil {
		t.Fatal("overwrote existing key file")
	}
}

func TestArgon2ParamsMarshalRoundTrip(t *testing.T) {
	p := DefaultArgon2Params()
	if p.Version != argon2.Version {
		t.Fatalf("version = %d, want %d", p.Version, argon2.Version)
	}
	if p.KeyLen != keySize {
		t.Fatalf("keylen = %d, want %d", p.KeyLen, keySize)
	}
	if p.Threads < 1 {
		t.Fatalf("threads = %d, want >= 1", p.Threads)
	}
	got, err := UnmarshalParams(MarshalParams(p))
	if err != nil {
		t.Fatalf("UnmarshalParams: %v", err)
	}
	if got != p {
		t.Fatalf("round trip = %+v, want %+v", got, p)
	}
}

func TestUnmarshalParamsRejectsInvalid(t *testing.T) {
	bad := []string{
		`not json`,
		`{"version":1,"time":3,"memory_kib":65536,"threads":4,"key_len":32}`,  // wrong version
		`{"version":19,"time":0,"memory_kib":65536,"threads":4,"key_len":32}`, // zero time
		`{"version":19,"time":3,"memory_kib":0,"threads":4,"key_len":32}`,     // zero memory
		`{"version":19,"time":3,"memory_kib":65536,"threads":0,"key_len":32}`, // zero threads
		`{"version":19,"time":3,"memory_kib":65536,"threads":4,"key_len":16}`, // wrong key len
	}
	for _, s := range bad {
		if _, err := UnmarshalParams(s); err == nil {
			t.Errorf("UnmarshalParams(%q) = nil error", s)
		}
	}
}

func TestDeriveKEKMaterialFromPassphrase(t *testing.T) {
	params := DefaultArgon2Params()
	// Keep the test fast: derivation cost doesn't affect correctness.
	params.MemoryK = 8 * 1024
	params.Time = 1
	salt := bytes.Repeat([]byte{0x01}, 16)

	a, err := DeriveKEKMaterialFromPassphrase([]byte("correct horse"), salt, params)
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	if len(a) != keySize {
		t.Fatalf("material len = %d, want %d", len(a), keySize)
	}
	// Deterministic for identical inputs.
	b, _ := DeriveKEKMaterialFromPassphrase([]byte("correct horse"), salt, params)
	if !bytes.Equal(a, b) {
		t.Fatal("derivation not deterministic")
	}
	// Different passphrase => different key.
	c, _ := DeriveKEKMaterialFromPassphrase([]byte("wrong horse"), salt, params)
	if bytes.Equal(a, c) {
		t.Fatal("different passphrase produced same key")
	}
	// Different salt => different key.
	d, _ := DeriveKEKMaterialFromPassphrase([]byte("correct horse"), bytes.Repeat([]byte{0x02}, 16), params)
	if bytes.Equal(a, d) {
		t.Fatal("different salt produced same key")
	}
}

func TestDeriveKEKMaterialValidation(t *testing.T) {
	params := DefaultArgon2Params()
	if _, err := DeriveKEKMaterialFromPassphrase(nil, bytes.Repeat([]byte{1}, 16), params); err == nil {
		t.Fatal("empty passphrase accepted")
	}
	if _, err := DeriveKEKMaterialFromPassphrase([]byte("p"), []byte("shortsalt"), params); err == nil {
		t.Fatal("short salt accepted")
	}
	bad := params
	bad.KeyLen = 16
	if _, err := DeriveKEKMaterialFromPassphrase([]byte("p"), bytes.Repeat([]byte{1}, 16), bad); err == nil {
		t.Fatal("wrong key length accepted")
	}
}

func TestKeyCheckCanary(t *testing.T) {
	kek := mustKEK(t, "canary")
	check, err := NewKeyCheck(kek)
	if err != nil {
		t.Fatalf("NewKeyCheck: %v", err)
	}
	if err := VerifyKeyCheck(kek, check); err != nil {
		t.Fatalf("VerifyKeyCheck(correct) = %v", err)
	}

	// A different KEK (wrong master key) fails verification.
	wrong := mustKEK(t, "canary-wrong")
	if err := VerifyKeyCheck(wrong, check); !errors.Is(err, domain.ErrDecryptFailed) {
		t.Fatalf("VerifyKeyCheck(wrong) = %v, want ErrDecryptFailed", err)
	}

	// A KEK with the same key but different ID fails: the ID is bound via AAD.
	sameKeyDiffID := &KEK{ID: "different", key: bytes.Clone(kek.key)}
	if err := VerifyKeyCheck(sameKeyDiffID, check); !errors.Is(err, domain.ErrDecryptFailed) {
		t.Fatalf("VerifyKeyCheck(diff id) = %v, want ErrDecryptFailed", err)
	}

	// Tampered check blob fails.
	tampered := bytes.Clone(check)
	tampered[len(tampered)-1] ^= 0xff
	if err := VerifyKeyCheck(kek, tampered); !errors.Is(err, domain.ErrDecryptFailed) {
		t.Fatalf("VerifyKeyCheck(tampered) = %v, want ErrDecryptFailed", err)
	}
}

func TestKeyring(t *testing.T) {
	active := mustKEK(t, "active")
	ring := NewKeyring(active)

	if ring.Active().ID != active.ID {
		t.Fatalf("Active = %q, want %q", ring.Active().ID, active.ID)
	}
	got, err := ring.Get(active.ID)
	if err != nil || got.ID != active.ID {
		t.Fatalf("Get(active) = %v, %v", got, err)
	}
	if _, err := ring.Get("missing"); !errors.Is(err, domain.ErrDecryptFailed) {
		t.Fatalf("Get(missing) err = %v, want ErrDecryptFailed", err)
	}

	// Retired KEKs remain retrievable for historical records.
	retired := mustKEK(t, "retired")
	ring.Add(retired)
	if got, err := ring.Get(retired.ID); err != nil || got.ID != retired.ID {
		t.Fatalf("Get(retired) = %v, %v", got, err)
	}
	if ring.Active().ID != active.ID {
		t.Fatal("Add changed the active KEK")
	}

	// SetActive promotes a new KEK but keeps the old one reachable.
	next := mustKEK(t, "next")
	ring.SetActive(next)
	if ring.Active().ID != next.ID {
		t.Fatalf("Active = %q, want %q", ring.Active().ID, next.ID)
	}
	if _, err := ring.Get(active.ID); err != nil {
		t.Fatalf("previous active no longer retrievable: %v", err)
	}
}

func TestTokenHashDeterministicAndSized(t *testing.T) {
	h1 := TokenHash("kms_abc")
	if len(h1) != 32 {
		t.Fatalf("hash len = %d, want 32", len(h1))
	}
	if !bytes.Equal(h1, TokenHash("kms_abc")) {
		t.Fatal("TokenHash not deterministic")
	}
	if bytes.Equal(h1, TokenHash("kms_abd")) {
		t.Fatal("distinct tokens hashed equal")
	}
}

func TestGenerateToken(t *testing.T) {
	tok, hash, err := GenerateToken("kms")
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}
	if !strings.HasPrefix(tok, "kms_") {
		t.Fatalf("token %q missing prefix", tok)
	}
	if !bytes.Equal(hash, TokenHash(tok)) {
		t.Fatal("returned hash does not match TokenHash(token)")
	}
	tok2, _, _ := GenerateToken("kms")
	if tok == tok2 {
		t.Fatal("GenerateToken produced duplicate tokens")
	}
	// Prefix is honored for secret tokens too.
	stok, _, _ := GenerateToken("kmss")
	if !strings.HasPrefix(stok, "kmss_") {
		t.Fatalf("secret token %q missing prefix", stok)
	}
}
