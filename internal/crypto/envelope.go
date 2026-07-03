package crypto

import (
	"errors"
	"fmt"

	"golang.org/x/crypto/hkdf"

	"crypto/sha256"

	"github.com/Suhaibinator/kms/internal/domain"
)

// Domain-separation suffixes appended to the caller AAD so the value
// ciphertext, the DEK wrap layer, and the client-bound inner layer can never
// be confused for one another.
const (
	aadSuffixValue    = "|layer:value"
	aadSuffixDEK      = "|layer:dek"
	aadSuffixDEKInner = "|layer:dek-inner"
)

// hkdfInfoClientBound fixes the HKDF context for client-key derivation.
const hkdfInfoClientBound = "kms/v1 client-bound key"

const clientKeySaltSize = 32

// ErrClientTokenRequired is returned when decrypting a client-bound version
// without a token. Transport layers must surface it generically.
var ErrClientTokenRequired = errors.New("client token required")

// BuildAAD returns the canonical associated-data string binding a secret
// version's ciphertext to its namespace-native identity. It is persisted
// alongside the ciphertext and must match byte-for-byte at decrypt time.
//
// The env, app, and key components are expected to be keyutil-validated by the
// caller (env/app are [a-z0-9-]; key segments are [a-z0-9._-] joined by '/'),
// none of which can contain the ';' or '=' delimiters, so the encoding is
// unambiguous without escaping.
func BuildAAD(env, app, key string, version uint64) string {
	return fmt.Sprintf("env=%s;app=%s;key=%s;version=%d", env, app, key, version)
}

// EncryptResult carries everything storage persists for one secret version.
type EncryptResult struct {
	Ciphertext    []byte
	EncryptedDEK  []byte // packed nonce||ct; contains the inner layer for client-bound
	KEKID         string
	WrapMode      string
	ClientKeySalt []byte // nil for standard wrapping
	Algorithm     string
	Nonce         []byte // nonce of the value encryption
	AAD           string
}

// DecryptInput carries the stored fields needed to recover a secret value.
type DecryptInput struct {
	Ciphertext    []byte
	EncryptedDEK  []byte
	Nonce         []byte
	AAD           string
	WrapMode      string
	ClientKeySalt []byte
	ClientToken   string // required iff WrapMode == client_bound
}

// Encrypt performs standard envelope encryption: fresh 32-byte DEK encrypts
// the plaintext; the KEK wraps the DEK.
func Encrypt(kek *KEK, plaintext []byte, aad string) (EncryptResult, error) {
	return encrypt(kek, plaintext, aad, "")
}

// EncryptClientBound performs double-wrapped envelope encryption: the DEK is
// first encrypted under a key derived from the client token (HKDF-SHA256 with
// a fresh random salt), then the result is wrapped under the KEK. The token
// itself is never returned or stored.
func EncryptClientBound(kek *KEK, plaintext []byte, aad, clientToken string) (EncryptResult, error) {
	if clientToken == "" {
		return EncryptResult{}, ErrClientTokenRequired
	}
	return encrypt(kek, plaintext, aad, clientToken)
}

func encrypt(kek *KEK, plaintext []byte, aad, clientToken string) (EncryptResult, error) {
	if kek == nil {
		return EncryptResult{}, errors.New("nil KEK")
	}
	dek, err := randomBytes(keySize)
	if err != nil {
		return EncryptResult{}, err
	}
	defer Zero(dek)

	nonce, ct, err := seal(dek, plaintext, []byte(aad+aadSuffixValue))
	if err != nil {
		return EncryptResult{}, err
	}

	res := EncryptResult{
		Ciphertext: ct,
		KEKID:      kek.ID,
		WrapMode:   domain.WrapModeStandard,
		Algorithm:  AlgorithmAES256GCM,
		Nonce:      nonce,
		AAD:        aad,
	}

	toWrap := dek
	if clientToken != "" {
		salt, err := randomBytes(clientKeySaltSize)
		if err != nil {
			return EncryptResult{}, err
		}
		clientKey, err := deriveClientKey(clientToken, salt)
		if err != nil {
			return EncryptResult{}, err
		}
		inner, err := sealPacked(clientKey, dek, []byte(aad+aadSuffixDEKInner))
		Zero(clientKey)
		if err != nil {
			return EncryptResult{}, err
		}
		res.WrapMode = domain.WrapModeClientBound
		res.ClientKeySalt = salt
		toWrap = inner
	}

	wrapped, err := sealPacked(kek.key, toWrap, []byte(aad+aadSuffixDEK))
	if err != nil {
		return EncryptResult{}, err
	}
	res.EncryptedDEK = wrapped
	return res, nil
}

// Decrypt reverses Encrypt/EncryptClientBound. Any failure — wrong KEK, wrong
// client token, tampered ciphertext, AAD mismatch — surfaces as
// domain.ErrDecryptFailed with no distinguishing detail.
func Decrypt(kek *KEK, in DecryptInput) ([]byte, error) {
	if kek == nil {
		return nil, errors.New("nil KEK")
	}
	inner, err := openPacked(kek.key, in.EncryptedDEK, []byte(in.AAD+aadSuffixDEK))
	if err != nil {
		return nil, domain.ErrDecryptFailed
	}
	defer Zero(inner)

	dek := inner
	switch in.WrapMode {
	case domain.WrapModeStandard:
	case domain.WrapModeClientBound:
		if in.ClientToken == "" {
			return nil, ErrClientTokenRequired
		}
		clientKey, err := deriveClientKey(in.ClientToken, in.ClientKeySalt)
		if err != nil {
			return nil, domain.ErrDecryptFailed
		}
		unwrapped, err := openPacked(clientKey, inner, []byte(in.AAD+aadSuffixDEKInner))
		Zero(clientKey)
		if err != nil {
			return nil, domain.ErrDecryptFailed
		}
		defer Zero(unwrapped)
		dek = unwrapped
	default:
		return nil, fmt.Errorf("unknown wrap mode %q: %w", in.WrapMode, domain.ErrDecryptFailed)
	}

	pt, err := open(dek, in.Nonce, in.Ciphertext, []byte(in.AAD+aadSuffixValue))
	if err != nil {
		return nil, domain.ErrDecryptFailed
	}
	return pt, nil
}

// RewrapDEK re-encrypts a stored encrypted DEK from one KEK to another
// without touching the value ciphertext or, for client-bound versions, the
// inner client layer. This keeps KEK rotation a server-side-only operation.
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

// deriveClientKey derives the 32-byte client key from the per-secret token.
func deriveClientKey(token string, salt []byte) ([]byte, error) {
	if len(salt) != clientKeySaltSize {
		return nil, fmt.Errorf("client key salt must be %d bytes", clientKeySaltSize)
	}
	key := make([]byte, keySize)
	r := hkdf.New(sha256.New, []byte(token), salt, []byte(hkdfInfoClientBound))
	if _, err := r.Read(key); err != nil {
		return nil, err
	}
	return key, nil
}
