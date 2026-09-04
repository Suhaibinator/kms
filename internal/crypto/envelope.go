package crypto

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"unicode/utf8"

	"github.com/Suhaibinator/kms/internal/domain"
	"golang.org/x/crypto/hkdf"
)

// Domain-separation suffixes prevent ciphertext from one layer being replayed
// at another layer.
const (
	aadSuffixValue    = "|layer:value"
	aadSuffixDEK      = "|layer:dek"
	aadSuffixDEKInner = "|layer:dek-inner"
)

const (
	hkdfInfoBindingKey = "kms/v1 binding key"
	BindingKeySaltSize = 32
	MinBindingKeyBytes = 32
)

var (
	ErrBindingKeyRequired    = errors.New("binding key required")
	ErrBindingKeyTooShort    = errors.New("binding key must be at least 32 UTF-8 bytes")
	ErrBindingKeyInvalidUTF8 = errors.New("binding key must be valid UTF-8")
)

// BuildAAD returns the canonical associated-data string binding a secret
// version's ciphertext to its namespace-native identity. It is persisted
// alongside the ciphertext and must match byte-for-byte at decrypt time.
func BuildAAD(env, app, key string, version uint64) string {
	return fmt.Sprintf("env=%s;app=%s;key=%s;version=%d", env, app, key, version)
}

// ValidateBindingKey validates only the binding-key wire contract. Binding
// keys are otherwise opaque: in particular, no trimming or normalization is
// performed and their content is never included in an error.
func ValidateBindingKey(bindingKey string) error {
	if bindingKey == "" {
		return ErrBindingKeyRequired
	}
	if !utf8.ValidString(bindingKey) {
		return ErrBindingKeyInvalidUTF8
	}
	if len(bindingKey) < MinBindingKeyBytes {
		return ErrBindingKeyTooShort
	}
	return nil
}

// EncryptResult carries everything storage persists for one secret version.
type EncryptResult struct {
	Ciphertext     []byte
	EncryptedDEK   []byte // packed nonce||ct; contains the inner binding layer when bound
	KEKID          string
	WrapMode       string
	BindingKeySalt []byte // nil for standard wrapping
	Algorithm      string
	Nonce          []byte // nonce of the value encryption
	AAD            string
}

// DecryptInput carries the stored fields and caller credential needed to
// recover a secret value.
type DecryptInput struct {
	Ciphertext     []byte
	EncryptedDEK   []byte
	Nonce          []byte
	AAD            string
	WrapMode       string
	BindingKeySalt []byte
	BindingKey     string // required iff WrapMode == binding_key
}

type bindingWrapResult struct {
	EncryptedDEK   []byte
	BindingKeySalt []byte
	WrapMode       string
	KEKID          string
}

// Encrypt performs standard envelope encryption: a fresh 32-byte DEK
// encrypts the plaintext and the KEK wraps that DEK directly.
func Encrypt(kek *KEK, plaintext []byte, aad string) (EncryptResult, error) {
	return encrypt(kek, plaintext, aad, "")
}

// EncryptBindingKey performs double-wrapped envelope encryption. A key
// derived from the operator-supplied binding key wraps the DEK first, then the
// server KEK wraps that inner ciphertext. The binding key is never retained.
func EncryptBindingKey(kek *KEK, plaintext []byte, aad, bindingKey string) (EncryptResult, error) {
	if err := ValidateBindingKey(bindingKey); err != nil {
		return EncryptResult{}, err
	}
	return encrypt(kek, plaintext, aad, bindingKey)
}

func encrypt(kek *KEK, plaintext []byte, aad, bindingKey string) (EncryptResult, error) {
	if kek == nil {
		return EncryptResult{}, errors.New("nil KEK")
	}
	dek, err := randomBytes(keySize)
	if err != nil {
		return EncryptResult{}, err
	}
	defer Zero(dek)

	nonce, ciphertext, err := seal(dek, plaintext, []byte(aad+aadSuffixValue))
	if err != nil {
		return EncryptResult{}, err
	}

	result := EncryptResult{
		Ciphertext: ciphertext,
		KEKID:      kek.ID,
		WrapMode:   domain.WrapModeStandard,
		Algorithm:  AlgorithmAES256GCM,
		Nonce:      nonce,
		AAD:        aad,
	}
	if bindingKey == "" {
		result.EncryptedDEK, err = sealPacked(kek.key, dek, []byte(aad+aadSuffixDEK))
		return result, err
	}

	rewrapped, err := wrapDEKWithBindingKey(kek, dek, aad, bindingKey)
	if err != nil {
		return EncryptResult{}, err
	}
	result.EncryptedDEK = rewrapped.EncryptedDEK
	result.WrapMode = rewrapped.WrapMode
	result.BindingKeySalt = rewrapped.BindingKeySalt
	return result, nil
}

// Decrypt reverses Encrypt or EncryptBindingKey. Authentication failures are
// deliberately collapsed to domain.ErrDecryptFailed.
func Decrypt(kek *KEK, in DecryptInput) ([]byte, error) {
	if kek == nil {
		return nil, errors.New("nil KEK")
	}

	var dek []byte
	var err error
	switch in.WrapMode {
	case domain.WrapModeStandard:
		if len(in.BindingKeySalt) != 0 {
			return nil, domain.ErrDecryptFailed
		}
		dek, err = openStandardDEK(kek, in.EncryptedDEK, in.AAD)
	case domain.WrapModeBindingKey:
		if err := ValidateBindingKey(in.BindingKey); err != nil {
			return nil, err
		}
		dek, err = openBindingKeyDEK(kek, in.EncryptedDEK, in.BindingKeySalt, in.AAD, in.BindingKey)
	default:
		return nil, fmt.Errorf("unknown wrap mode %q: %w", in.WrapMode, domain.ErrDecryptFailed)
	}
	if err != nil {
		return nil, domain.ErrDecryptFailed
	}
	defer Zero(dek)

	plaintext, err := open(dek, in.Nonce, in.Ciphertext, []byte(in.AAD+aadSuffixValue))
	if err != nil {
		return nil, domain.ErrDecryptFailed
	}
	return plaintext, nil
}

// TestBindingKeyDEK cryptographically tests cohort membership. It returns no
// key material and zeroes the opened DEK before returning.
func TestBindingKeyDEK(kek *KEK, encryptedDEK, bindingKeySalt []byte, aad, bindingKey string) error {
	if kek == nil {
		return errors.New("nil KEK")
	}
	if err := ValidateBindingKey(bindingKey); err != nil {
		return err
	}
	dek, err := openBindingKeyDEK(kek, encryptedDEK, bindingKeySalt, aad, bindingKey)
	if err != nil {
		return domain.ErrDecryptFailed
	}
	Zero(dek)
	return nil
}

// RewrapDEK re-encrypts the outer DEK layer from one KEK to another without
// touching the value ciphertext or any binding-key inner layer.
func RewrapDEK(from, to *KEK, encryptedDEK []byte, aad string) ([]byte, error) {
	if from == nil || to == nil {
		return nil, errors.New("nil KEK")
	}
	inner, err := openPacked(from.key, encryptedDEK, []byte(aad+aadSuffixDEK))
	if err != nil {
		return nil, domain.ErrDecryptFailed
	}
	defer Zero(inner)
	return sealPacked(to.key, inner, []byte(aad+aadSuffixDEK))
}

func openStandardDEK(kek *KEK, encryptedDEK []byte, aad string) ([]byte, error) {
	dek, err := openPacked(kek.key, encryptedDEK, []byte(aad+aadSuffixDEK))
	if err != nil {
		return nil, err
	}
	if len(dek) != keySize {
		Zero(dek)
		return nil, errAEAD
	}
	return dek, nil
}

func openBindingKeyDEK(kek *KEK, encryptedDEK, bindingKeySalt []byte, aad, bindingKey string) ([]byte, error) {
	if len(bindingKeySalt) != BindingKeySaltSize {
		return nil, errAEAD
	}
	inner, err := openPacked(kek.key, encryptedDEK, []byte(aad+aadSuffixDEK))
	if err != nil {
		return nil, err
	}
	defer Zero(inner)

	derivedKey, err := deriveBindingKey(bindingKey, bindingKeySalt)
	if err != nil {
		return nil, err
	}
	defer Zero(derivedKey)
	dek, err := openPacked(derivedKey, inner, []byte(aad+aadSuffixDEKInner))
	if err != nil {
		return nil, err
	}
	if len(dek) != keySize {
		Zero(dek)
		return nil, errAEAD
	}
	return dek, nil
}

func wrapDEKWithBindingKey(kek *KEK, dek []byte, aad, bindingKey string) (bindingWrapResult, error) {
	if err := ValidateBindingKey(bindingKey); err != nil {
		return bindingWrapResult{}, err
	}
	salt, err := randomBytes(BindingKeySaltSize)
	if err != nil {
		return bindingWrapResult{}, err
	}
	derivedKey, err := deriveBindingKey(bindingKey, salt)
	if err != nil {
		Zero(salt)
		return bindingWrapResult{}, err
	}
	inner, err := sealPacked(derivedKey, dek, []byte(aad+aadSuffixDEKInner))
	Zero(derivedKey)
	if err != nil {
		Zero(salt)
		return bindingWrapResult{}, err
	}
	defer Zero(inner)

	outer, err := sealPacked(kek.key, inner, []byte(aad+aadSuffixDEK))
	if err != nil {
		Zero(salt)
		return bindingWrapResult{}, err
	}
	return bindingWrapResult{
		EncryptedDEK:   outer,
		BindingKeySalt: salt,
		WrapMode:       domain.WrapModeBindingKey,
		KEKID:          kek.ID,
	}, nil
}

func deriveBindingKey(bindingKey string, salt []byte) ([]byte, error) {
	if err := ValidateBindingKey(bindingKey); err != nil {
		return nil, err
	}
	if len(salt) != BindingKeySaltSize {
		return nil, fmt.Errorf("binding key salt must be %d bytes", BindingKeySaltSize)
	}

	// The conversion is required by HKDF. Zero this mutable copy promptly; the
	// caller-owned Go string itself cannot be reliably erased.
	inputKeyMaterial := []byte(bindingKey)
	defer Zero(inputKeyMaterial)
	derivedKey := make([]byte, keySize)
	r := hkdf.New(sha256.New, inputKeyMaterial, salt, []byte(hkdfInfoBindingKey))
	if _, err := io.ReadFull(r, derivedKey); err != nil {
		Zero(derivedKey)
		return nil, err
	}
	return derivedKey, nil
}
