package crypto

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/Suhaibinator/kms/internal/domain"
)

const (
	testBindingKeyA = "0123456789abcdef0123456789abcdef"
	testBindingKeyB = "abcdef0123456789abcdef0123456789"
)

func decryptInput(result EncryptResult, bindingKey string) DecryptInput {
	return DecryptInput{
		Ciphertext:     result.Ciphertext,
		EncryptedDEK:   result.EncryptedDEK,
		Nonce:          result.Nonce,
		AAD:            result.AAD,
		WrapMode:       result.WrapMode,
		BindingKeySalt: result.BindingKeySalt,
		BindingKey:     bindingKey,
	}
}

func TestBuildAADFormatAndIdentityBinding(t *testing.T) {
	got := BuildAAD("prod", "gradethis", "billing/stripe-key", 3)
	want := "env=prod;app=gradethis;key=billing/stripe-key;version=3"
	if got != want {
		t.Fatalf("BuildAAD = %q, want %q", got, want)
	}
	base := BuildAAD("prod", "app", "x", 1)
	for name, other := range map[string]string{
		"environment": BuildAAD("stage", "app", "x", 1),
		"application": BuildAAD("prod", "other", "x", 1),
		"key":         BuildAAD("prod", "app", "y", 1),
		"version":     BuildAAD("prod", "app", "x", 2),
	} {
		if base == other {
			t.Fatalf("AAD is not sensitive to %s", name)
		}
	}
}

func TestEncryptDecryptStandardRoundTrip(t *testing.T) {
	kek := mustKEK(t, "kek-standard")
	plaintext := []byte("super-secret-password")
	result, err := Encrypt(kek, plaintext, BuildAAD("prod", "app", "db", 1))
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if result.WrapMode != domain.WrapModeStandard {
		t.Fatalf("WrapMode = %q, want %q", result.WrapMode, domain.WrapModeStandard)
	}
	if result.BindingKeySalt != nil {
		t.Fatal("standard wrap persisted a binding-key salt")
	}
	if result.KEKID != kek.ID || result.Algorithm != AlgorithmAES256GCM {
		t.Fatalf("unexpected metadata: KEKID=%q algorithm=%q", result.KEKID, result.Algorithm)
	}
	if bytes.Contains(result.Ciphertext, plaintext) {
		t.Fatal("plaintext leaked into ciphertext")
	}
	got, err := Decrypt(kek, decryptInput(result, ""))
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Fatalf("Decrypt = %q, want %q", got, plaintext)
	}
}

func TestEncryptBindingKeyRoundTrip(t *testing.T) {
	kek := mustKEK(t, "kek-bound")
	plaintext := []byte("bound-secret")
	result, err := EncryptBindingKey(kek, plaintext, BuildAAD("prod", "app", "api", 1), testBindingKeyA)
	if err != nil {
		t.Fatalf("EncryptBindingKey: %v", err)
	}
	if result.WrapMode != domain.WrapModeBindingKey {
		t.Fatalf("WrapMode = %q, want %q", result.WrapMode, domain.WrapModeBindingKey)
	}
	if len(result.BindingKeySalt) != BindingKeySaltSize {
		t.Fatalf("BindingKeySalt len = %d, want %d", len(result.BindingKeySalt), BindingKeySaltSize)
	}
	if result.KEKID != kek.ID {
		t.Fatalf("KEKID = %q, want %q", result.KEKID, kek.ID)
	}
	got, err := Decrypt(kek, decryptInput(result, testBindingKeyA))
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Fatalf("Decrypt = %q, want %q", got, plaintext)
	}
}

func TestValidateBindingKeyOpaqueUTF8Bytes(t *testing.T) {
	for name, tc := range map[string]struct {
		key  string
		want error
	}{
		"missing":       {key: "", want: ErrBindingKeyRequired},
		"31 bytes":      {key: strings.Repeat("x", 31), want: ErrBindingKeyTooShort},
		"32 bytes":      {key: strings.Repeat("x", 32)},
		"1024 bytes":    {key: strings.Repeat("x", 1024)},
		"1025 bytes":    {key: strings.Repeat("x", 1025), want: ErrBindingKeyTooLong},
		"16 unicode":    {key: strings.Repeat("é", 16)}, // 32 UTF-8 bytes
		"opaque spaces": {key: strings.Repeat(" ", 32)},
		"not trimmed":   {key: strings.Repeat("x", 31) + " "},
		"invalid UTF-8": {key: string([]byte{0xff, 0xfe}) + strings.Repeat("x", 32), want: ErrBindingKeyInvalidUTF8},
	} {
		t.Run(name, func(t *testing.T) {
			err := ValidateBindingKey(tc.key)
			if !errors.Is(err, tc.want) || (tc.want == nil && err != nil) {
				t.Fatalf("ValidateBindingKey() error = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestBindingKeyDecryptRejectsMissingShortAndWrongKeys(t *testing.T) {
	kek := mustKEK(t, "kek-credentials")
	result, err := EncryptBindingKey(kek, []byte("v"), BuildAAD("prod", "app", "api", 1), testBindingKeyA)
	if err != nil {
		t.Fatalf("EncryptBindingKey: %v", err)
	}
	for name, tc := range map[string]struct {
		key  string
		want error
	}{
		"missing": {key: "", want: ErrBindingKeyRequired},
		"short":   {key: "wrong", want: ErrBindingKeyTooShort},
		"wrong":   {key: testBindingKeyB, want: domain.ErrDecryptFailed},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Decrypt(kek, decryptInput(result, tc.key))
			if !errors.Is(err, tc.want) {
				t.Fatalf("Decrypt error = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestBindingKeyCannotBeBypassedWithKEK(t *testing.T) {
	kek := mustKEK(t, "kek-no-bypass")
	result, err := EncryptBindingKey(kek, []byte("v"), BuildAAD("prod", "app", "api", 1), testBindingKeyA)
	if err != nil {
		t.Fatalf("EncryptBindingKey: %v", err)
	}
	in := decryptInput(result, "")
	in.WrapMode = domain.WrapModeStandard
	in.BindingKeySalt = nil
	if _, err := Decrypt(kek, in); !errors.Is(err, domain.ErrDecryptFailed) {
		t.Fatalf("Decrypt forced standard error = %v, want ErrDecryptFailed", err)
	}
}

func TestBindingKeyEncryptionUsesFreshIndependentSalts(t *testing.T) {
	kek := mustKEK(t, "kek-salts")
	aad := BuildAAD("prod", "app", "api", 1)
	first, err := EncryptBindingKey(kek, []byte("same"), aad, testBindingKeyA)
	if err != nil {
		t.Fatalf("first EncryptBindingKey: %v", err)
	}
	second, err := EncryptBindingKey(kek, []byte("same"), aad, testBindingKeyA)
	if err != nil {
		t.Fatalf("second EncryptBindingKey: %v", err)
	}
	if bytes.Equal(first.BindingKeySalt, second.BindingKeySalt) {
		t.Fatal("binding-key salt reused")
	}
	if bytes.Equal(first.Ciphertext, second.Ciphertext) || bytes.Equal(first.EncryptedDEK, second.EncryptedDEK) {
		t.Fatal("encryption reused ciphertext or wrapped DEK")
	}
}

func TestBindingKeyDerivation(t *testing.T) {
	salt := bytes.Repeat([]byte{0x09}, BindingKeySaltSize)
	first, err := deriveBindingKey(testBindingKeyA, salt)
	if err != nil {
		t.Fatalf("deriveBindingKey: %v", err)
	}
	defer Zero(first)
	second, err := deriveBindingKey(testBindingKeyA, salt)
	if err != nil {
		t.Fatalf("deriveBindingKey second: %v", err)
	}
	defer Zero(second)
	if len(first) != keySize || !bytes.Equal(first, second) {
		t.Fatal("derivation has wrong size or is nondeterministic")
	}
	otherSalt, _ := deriveBindingKey(testBindingKeyA, bytes.Repeat([]byte{0x0a}, BindingKeySaltSize))
	defer Zero(otherSalt)
	otherKey, _ := deriveBindingKey(testBindingKeyB, salt)
	defer Zero(otherKey)
	if bytes.Equal(first, otherSalt) || bytes.Equal(first, otherKey) {
		t.Fatal("different salt or binding key produced the same derived key")
	}
	if _, err := deriveBindingKey(testBindingKeyA, []byte("short")); err == nil {
		t.Fatal("deriveBindingKey accepted a short salt")
	}
}

func TestRewrapDEKOuterKEKPreservesBindingLayer(t *testing.T) {
	from := mustKEK(t, "kek-old")
	to := mustKEK(t, "kek-new")
	aad := BuildAAD("prod", "app", "api", 1)
	result, err := EncryptBindingKey(from, []byte("v"), aad, testBindingKeyA)
	if err != nil {
		t.Fatal(err)
	}
	wrapped, err := RewrapDEK(from, to, result.EncryptedDEK, aad)
	if err != nil {
		t.Fatalf("RewrapDEK: %v", err)
	}
	in := decryptInput(result, testBindingKeyA)
	in.EncryptedDEK = wrapped
	got, err := Decrypt(to, in)
	if err != nil || string(got) != "v" {
		t.Fatalf("Decrypt after outer rewrap = %q, %v", got, err)
	}
	if _, err := Decrypt(from, in); !errors.Is(err, domain.ErrDecryptFailed) {
		t.Fatalf("old KEK opens rewrapped DEK: %v", err)
	}
}

func TestDecryptFailsClosedOnTamperAndConfusion(t *testing.T) {
	kek := mustKEK(t, "kek-tamper")
	aad := BuildAAD("prod", "app", "api", 1)
	result, err := Encrypt(kek, []byte("value"), aad)
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*DecryptInput){
		"ciphertext":  func(in *DecryptInput) { in.Ciphertext = flipFirst(in.Ciphertext) },
		"wrapped DEK": func(in *DecryptInput) { in.EncryptedDEK = flipFirst(in.EncryptedDEK) },
		"nonce":       func(in *DecryptInput) { in.Nonce = flipFirst(in.Nonce) },
		"AAD":         func(in *DecryptInput) { in.AAD += "-wrong" },
		"cross layer": func(in *DecryptInput) { in.EncryptedDEK = in.Ciphertext },
	} {
		t.Run(name, func(t *testing.T) {
			in := decryptInput(result, "")
			mutate(&in)
			if _, err := Decrypt(kek, in); !errors.Is(err, domain.ErrDecryptFailed) {
				t.Fatalf("Decrypt error = %v, want ErrDecryptFailed", err)
			}
		})
	}
	in := decryptInput(result, "")
	in.WrapMode = "unknown"
	if _, err := Decrypt(kek, in); !errors.Is(err, domain.ErrDecryptFailed) {
		t.Fatalf("unknown wrap mode error = %v, want ErrDecryptFailed", err)
	}
}

func TestZeroOverwritesMutableKeyMaterial(t *testing.T) {
	material := bytes.Repeat([]byte{0xa5}, keySize)
	Zero(material)
	if !bytes.Equal(material, make([]byte, keySize)) {
		t.Fatal("Zero did not overwrite the entire buffer")
	}
}

func TestDomainSeparationSuffixesDistinct(t *testing.T) {
	suffixes := []string{aadSuffixValue, aadSuffixDEK, aadSuffixDEKInner}
	for i := range suffixes {
		if !strings.HasPrefix(suffixes[i], "|layer:") {
			t.Fatalf("suffix %q lacks domain prefix", suffixes[i])
		}
		for j := i + 1; j < len(suffixes); j++ {
			if suffixes[i] == suffixes[j] {
				t.Fatalf("suffixes %q and %q are equal", suffixes[i], suffixes[j])
			}
		}
	}
}

func flipFirst(in []byte) []byte {
	out := bytes.Clone(in)
	if len(out) > 0 {
		out[0] ^= 0x80
	}
	return out
}
