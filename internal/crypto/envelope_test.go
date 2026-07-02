package crypto

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/Suhaibinator/kms/internal/domain"
)

func stdInput(res EncryptResult) DecryptInput {
	return DecryptInput{
		Ciphertext:    res.Ciphertext,
		EncryptedDEK:  res.EncryptedDEK,
		Nonce:         res.Nonce,
		AAD:           res.AAD,
		WrapMode:      res.WrapMode,
		ClientKeySalt: res.ClientKeySalt,
	}
}

func TestBuildAADFormat(t *testing.T) {
	got := BuildAAD(domain.ResourceSecret, "/prod/x", 3)
	want := "kms/v1|type:secret|path:/prod/x|version:3"
	if got != want {
		t.Fatalf("BuildAAD = %q, want %q", got, want)
	}
	// Distinct identity => distinct AAD.
	if BuildAAD(domain.ResourceSecret, "/prod/x", 3) == BuildAAD(domain.ResourceSecret, "/prod/x", 4) {
		t.Fatal("AAD not sensitive to version")
	}
	if BuildAAD(domain.ResourceSecret, "/a", 1) == BuildAAD(domain.ResourceParameter, "/a", 1) {
		t.Fatal("AAD not sensitive to resource type")
	}
}

func TestEncryptDecryptStandardRoundTrip(t *testing.T) {
	kek := mustKEK(t, "kek-1")
	aad := BuildAAD(domain.ResourceSecret, "/prod/db", 1)
	plaintext := []byte("super-secret-password")

	res, err := Encrypt(kek, plaintext, aad)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if res.WrapMode != domain.WrapModeStandard {
		t.Fatalf("WrapMode = %q, want standard", res.WrapMode)
	}
	if res.ClientKeySalt != nil {
		t.Fatal("standard wrap must not set ClientKeySalt")
	}
	if res.Algorithm != AlgorithmAES256GCM {
		t.Fatalf("Algorithm = %q", res.Algorithm)
	}
	if res.KEKID != kek.ID {
		t.Fatalf("KEKID = %q, want %q", res.KEKID, kek.ID)
	}
	if bytes.Contains(res.Ciphertext, plaintext) {
		t.Fatal("plaintext leaked into ciphertext")
	}

	got, err := Decrypt(kek, stdInput(res))
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Fatalf("Decrypt = %q, want %q", got, plaintext)
	}
}

func TestEncryptEmptyPlaintextRoundTrips(t *testing.T) {
	kek := mustKEK(t, "kek-empty")
	aad := BuildAAD(domain.ResourceSecret, "/e", 1)
	res, err := Encrypt(kek, []byte{}, aad)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	got, err := Decrypt(kek, stdInput(res))
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("Decrypt = %q, want empty", got)
	}
}

func TestEncryptNilKEK(t *testing.T) {
	if _, err := Encrypt(nil, []byte("x"), "aad"); err == nil {
		t.Fatal("Encrypt(nil KEK) = nil error")
	}
	if _, err := Decrypt(nil, DecryptInput{}); err == nil {
		t.Fatal("Decrypt(nil KEK) = nil error")
	}
}

func TestDecryptFailsClosedOnTamper(t *testing.T) {
	kek := mustKEK(t, "kek-tamper")
	aad := BuildAAD(domain.ResourceSecret, "/prod/db", 7)
	res, err := Encrypt(kek, []byte("value"), aad)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	cases := map[string]func(in *DecryptInput){
		"ciphertext bit flip": func(in *DecryptInput) {
			in.Ciphertext = bytes.Clone(in.Ciphertext)
			in.Ciphertext[0] ^= 0x80
		},
		"encrypted DEK bit flip": func(in *DecryptInput) {
			in.EncryptedDEK = bytes.Clone(in.EncryptedDEK)
			in.EncryptedDEK[len(in.EncryptedDEK)-1] ^= 0x01
		},
		"nonce bit flip": func(in *DecryptInput) {
			in.Nonce = bytes.Clone(in.Nonce)
			in.Nonce[0] ^= 0x01
		},
		"aad mismatch": func(in *DecryptInput) {
			in.AAD = BuildAAD(domain.ResourceSecret, "/prod/db", 8) // wrong version
		},
		"truncated ciphertext": func(in *DecryptInput) {
			in.Ciphertext = in.Ciphertext[:len(in.Ciphertext)-1]
		},
		"empty encrypted DEK": func(in *DecryptInput) {
			in.EncryptedDEK = nil
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			in := stdInput(res)
			mutate(&in)
			_, err := Decrypt(kek, in)
			if !errors.Is(err, domain.ErrDecryptFailed) {
				t.Fatalf("Decrypt(%s) err = %v, want ErrDecryptFailed", name, err)
			}
		})
	}
}

func TestDecryptWrongKEK(t *testing.T) {
	kek := mustKEK(t, "kek-a")
	other := mustKEK(t, "kek-b")
	aad := BuildAAD(domain.ResourceSecret, "/x", 1)
	res, err := Encrypt(kek, []byte("v"), aad)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if _, err := Decrypt(other, stdInput(res)); !errors.Is(err, domain.ErrDecryptFailed) {
		t.Fatalf("Decrypt(wrong KEK) err = %v, want ErrDecryptFailed", err)
	}
}

func TestDecryptUnknownWrapMode(t *testing.T) {
	kek := mustKEK(t, "kek-mode")
	aad := BuildAAD(domain.ResourceSecret, "/x", 1)
	res, err := Encrypt(kek, []byte("v"), aad)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	in := stdInput(res)
	in.WrapMode = "bogus-mode"
	_, err = Decrypt(kek, in)
	if !errors.Is(err, domain.ErrDecryptFailed) {
		t.Fatalf("Decrypt(unknown mode) err = %v, want ErrDecryptFailed", err)
	}
}

// A wrapped-DEK blob copied from another record (different AAD) must fail:
// this is the cross-record substitution defense.
func TestDecryptRejectsCrossRecordDEK(t *testing.T) {
	kek := mustKEK(t, "kek-cross")
	aadA := BuildAAD(domain.ResourceSecret, "/a", 1)
	aadB := BuildAAD(domain.ResourceSecret, "/b", 1)
	resA, err := Encrypt(kek, []byte("secret-a"), aadA)
	if err != nil {
		t.Fatalf("Encrypt A: %v", err)
	}
	resB, err := Encrypt(kek, []byte("secret-b"), aadB)
	if err != nil {
		t.Fatalf("Encrypt B: %v", err)
	}
	// Splice B's encrypted DEK into A's record.
	in := stdInput(resA)
	in.EncryptedDEK = resB.EncryptedDEK
	if _, err := Decrypt(kek, in); !errors.Is(err, domain.ErrDecryptFailed) {
		t.Fatalf("Decrypt(spliced DEK) err = %v, want ErrDecryptFailed", err)
	}
}

func TestClientBoundRoundTrip(t *testing.T) {
	kek := mustKEK(t, "kek-cb")
	aad := BuildAAD(domain.ResourceSecret, "/prod/cb", 1)
	token := "kmss_client-access-token-value"
	plaintext := []byte("client-bound-secret")

	res, err := EncryptClientBound(kek, plaintext, aad, token)
	if err != nil {
		t.Fatalf("EncryptClientBound: %v", err)
	}
	if res.WrapMode != domain.WrapModeClientBound {
		t.Fatalf("WrapMode = %q, want client_bound", res.WrapMode)
	}
	if len(res.ClientKeySalt) != clientKeySaltSize {
		t.Fatalf("ClientKeySalt len = %d, want %d", len(res.ClientKeySalt), clientKeySaltSize)
	}

	in := stdInput(res)
	in.ClientToken = token
	got, err := Decrypt(kek, in)
	if err != nil {
		t.Fatalf("Decrypt(client-bound): %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Fatalf("Decrypt = %q, want %q", got, plaintext)
	}
}

func TestClientBoundRequiresToken(t *testing.T) {
	kek := mustKEK(t, "kek-cb2")
	aad := BuildAAD(domain.ResourceSecret, "/x", 1)
	if _, err := EncryptClientBound(kek, []byte("v"), aad, ""); !errors.Is(err, ErrClientTokenRequired) {
		t.Fatalf("EncryptClientBound(empty token) err = %v, want ErrClientTokenRequired", err)
	}

	res, err := EncryptClientBound(kek, []byte("v"), aad, "tok")
	if err != nil {
		t.Fatalf("EncryptClientBound: %v", err)
	}
	// Decrypt without supplying the token.
	if _, err := Decrypt(kek, stdInput(res)); !errors.Is(err, ErrClientTokenRequired) {
		t.Fatalf("Decrypt(client-bound, no token) err = %v, want ErrClientTokenRequired", err)
	}
}

func TestClientBoundWrongToken(t *testing.T) {
	kek := mustKEK(t, "kek-cb3")
	aad := BuildAAD(domain.ResourceSecret, "/x", 1)
	res, err := EncryptClientBound(kek, []byte("v"), aad, "correct-token")
	if err != nil {
		t.Fatalf("EncryptClientBound: %v", err)
	}
	in := stdInput(res)
	in.ClientToken = "wrong-token"
	if _, err := Decrypt(kek, in); !errors.Is(err, domain.ErrDecryptFailed) {
		t.Fatalf("Decrypt(wrong token) err = %v, want ErrDecryptFailed", err)
	}
}

// The database plus the master key alone must not decrypt a client-bound
// secret: even with the correct KEK, a missing/incorrect token fails.
func TestClientBoundUndecryptableWithoutToken(t *testing.T) {
	kek := mustKEK(t, "kek-cb4")
	aad := BuildAAD(domain.ResourceSecret, "/x", 1)
	res, err := EncryptClientBound(kek, []byte("v"), aad, "the-only-key-share")
	if err != nil {
		t.Fatalf("EncryptClientBound: %v", err)
	}
	// Attacker holds ciphertext, encrypted DEK, salt, AAD, and the real KEK —
	// but not the token. Treat it as standard to try to skip the inner layer.
	in := stdInput(res)
	in.WrapMode = domain.WrapModeStandard
	if _, err := Decrypt(kek, in); err == nil {
		t.Fatal("client-bound secret decrypted without token by forcing standard mode")
	}
}

func TestClientKeyDerivation(t *testing.T) {
	salt := bytes.Repeat([]byte{0x09}, clientKeySaltSize)
	k1, err := deriveClientKey("token", salt)
	if err != nil {
		t.Fatalf("deriveClientKey: %v", err)
	}
	if len(k1) != keySize {
		t.Fatalf("derived key len = %d, want %d", len(k1), keySize)
	}
	// Deterministic for identical inputs.
	k2, _ := deriveClientKey("token", salt)
	if !bytes.Equal(k1, k2) {
		t.Fatal("deriveClientKey not deterministic")
	}
	// Different salt => different key.
	salt2 := bytes.Repeat([]byte{0x0a}, clientKeySaltSize)
	k3, _ := deriveClientKey("token", salt2)
	if bytes.Equal(k1, k3) {
		t.Fatal("different salt produced same key")
	}
	// Different token => different key.
	k4, _ := deriveClientKey("other", salt)
	if bytes.Equal(k1, k4) {
		t.Fatal("different token produced same key")
	}
	// Bad salt length rejected.
	if _, err := deriveClientKey("token", []byte("short")); err == nil {
		t.Fatal("deriveClientKey(short salt) = nil error")
	}
}

func TestRewrapDEKStandard(t *testing.T) {
	from := mustKEK(t, "kek-old")
	to := mustKEK(t, "kek-new")
	aad := BuildAAD(domain.ResourceSecret, "/rotate", 1)
	res, err := Encrypt(from, []byte("rotate-me"), aad)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	newDEK, err := RewrapDEK(from, to, res.EncryptedDEK, aad)
	if err != nil {
		t.Fatalf("RewrapDEK: %v", err)
	}
	if bytes.Equal(newDEK, res.EncryptedDEK) {
		t.Fatal("rewrapped DEK identical to original")
	}

	// New KEK decrypts using the rewrapped DEK and the UNCHANGED value ciphertext.
	in := stdInput(res)
	in.EncryptedDEK = newDEK
	got, err := Decrypt(to, in)
	if err != nil {
		t.Fatalf("Decrypt(rewrapped): %v", err)
	}
	if string(got) != "rotate-me" {
		t.Fatalf("Decrypt = %q, want rotate-me", got)
	}
	// Old KEK no longer opens the rewrapped DEK.
	if _, err := Decrypt(from, in); !errors.Is(err, domain.ErrDecryptFailed) {
		t.Fatalf("Decrypt(old KEK, rewrapped) err = %v, want ErrDecryptFailed", err)
	}
}

func TestRewrapDEKClientBoundPreservesInnerLayer(t *testing.T) {
	from := mustKEK(t, "kek-cbo")
	to := mustKEK(t, "kek-cbn")
	aad := BuildAAD(domain.ResourceSecret, "/rotate/cb", 1)
	token := "client-token"
	res, err := EncryptClientBound(from, []byte("cb-value"), aad, token)
	if err != nil {
		t.Fatalf("EncryptClientBound: %v", err)
	}

	newDEK, err := RewrapDEK(from, to, res.EncryptedDEK, aad)
	if err != nil {
		t.Fatalf("RewrapDEK: %v", err)
	}
	in := stdInput(res)
	in.EncryptedDEK = newDEK
	in.ClientToken = token // inner layer still needs the token after KEK rotation
	got, err := Decrypt(to, in)
	if err != nil {
		t.Fatalf("Decrypt(rewrapped client-bound): %v", err)
	}
	if string(got) != "cb-value" {
		t.Fatalf("Decrypt = %q, want cb-value", got)
	}
}

func TestRewrapDEKRejectsWrongSource(t *testing.T) {
	from := mustKEK(t, "kek-rw-a")
	wrong := mustKEK(t, "kek-rw-b")
	to := mustKEK(t, "kek-rw-c")
	aad := BuildAAD(domain.ResourceSecret, "/x", 1)
	res, err := Encrypt(from, []byte("v"), aad)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if _, err := RewrapDEK(wrong, to, res.EncryptedDEK, aad); !errors.Is(err, domain.ErrDecryptFailed) {
		t.Fatalf("RewrapDEK(wrong source) err = %v, want ErrDecryptFailed", err)
	}
	if _, err := RewrapDEK(nil, to, res.EncryptedDEK, aad); err == nil {
		t.Fatal("RewrapDEK(nil from) = nil error")
	}
	if _, err := RewrapDEK(from, nil, res.EncryptedDEK, aad); err == nil {
		t.Fatal("RewrapDEK(nil to) = nil error")
	}
}

// Domain-separation suffixes must differ so a DEK-layer blob can't be replayed
// as a value-layer blob or vice versa.
func TestDomainSeparationSuffixesDistinct(t *testing.T) {
	suffixes := []string{aadSuffixValue, aadSuffixDEK, aadSuffixDEKInner}
	for i := range suffixes {
		for j := i + 1; j < len(suffixes); j++ {
			if suffixes[i] == suffixes[j] {
				t.Fatalf("suffixes %d and %d are equal (%q)", i, j, suffixes[i])
			}
		}
		if !strings.HasPrefix(suffixes[i], "|layer:") {
			t.Fatalf("suffix %q missing |layer: prefix", suffixes[i])
		}
	}
}

// Encrypting identical plaintext twice must yield different nonce, ciphertext,
// and wrapped DEK — a fresh DEK and nonce per sealing operation.
func TestEncryptUniquePerCall(t *testing.T) {
	kek := mustKEK(t, "kek-uniq")
	aad := BuildAAD(domain.ResourceSecret, "/x", 1)
	a, err := Encrypt(kek, []byte("same-plaintext"), aad)
	if err != nil {
		t.Fatalf("Encrypt a: %v", err)
	}
	b, err := Encrypt(kek, []byte("same-plaintext"), aad)
	if err != nil {
		t.Fatalf("Encrypt b: %v", err)
	}
	if bytes.Equal(a.Nonce, b.Nonce) {
		t.Error("nonce reused across encryptions")
	}
	if bytes.Equal(a.Ciphertext, b.Ciphertext) {
		t.Error("identical ciphertext for identical plaintext (DEK/nonce reuse)")
	}
	if bytes.Equal(a.EncryptedDEK, b.EncryptedDEK) {
		t.Error("identical wrapped DEK across encryptions")
	}
}

// Cross-layer confusion: a blob from one layer must not authenticate in
// another (value ciphertext as wrapped DEK, and vice versa).
func TestDecryptRejectsCrossLayerConfusion(t *testing.T) {
	kek := mustKEK(t, "kek-layer")
	aad := BuildAAD(domain.ResourceSecret, "/x", 1)
	res, err := Encrypt(kek, []byte("v"), aad)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	valueAsDEK := stdInput(res)
	valueAsDEK.EncryptedDEK = res.Ciphertext
	if _, err := Decrypt(kek, valueAsDEK); !errors.Is(err, domain.ErrDecryptFailed) {
		t.Fatalf("value-as-DEK err = %v, want ErrDecryptFailed", err)
	}

	dekAsValue := stdInput(res)
	dekAsValue.Ciphertext = res.EncryptedDEK
	if _, err := Decrypt(kek, dekAsValue); !errors.Is(err, domain.ErrDecryptFailed) {
		t.Fatalf("DEK-as-value err = %v, want ErrDecryptFailed", err)
	}
}

// A tampered client-key salt derives the wrong client key and must fail.
func TestClientBoundWrongSaltFails(t *testing.T) {
	kek := mustKEK(t, "kek-salt")
	aad := BuildAAD(domain.ResourceSecret, "/x", 1)
	res, err := EncryptClientBound(kek, []byte("v"), aad, "token")
	if err != nil {
		t.Fatalf("EncryptClientBound: %v", err)
	}
	in := stdInput(res)
	in.ClientToken = "token"
	in.ClientKeySalt = bytes.Clone(res.ClientKeySalt)
	in.ClientKeySalt[0] ^= 0xff
	if _, err := Decrypt(kek, in); !errors.Is(err, domain.ErrDecryptFailed) {
		t.Fatalf("wrong salt err = %v, want ErrDecryptFailed", err)
	}
}
