package core

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/Suhaibinator/kms/internal/crypto"
	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/keyutil"
	"github.com/Suhaibinator/kms/internal/storage"
)

// unixMSToTime converts unix milliseconds to a UTC time; 0 -> zero time.
func unixMSToTime(ms int64) time.Time {
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}

// encodeMeta serializes audit metadata deterministically; nil -> "{}".
// Values must never contain secret material — callers only pass operation
// names, labels, and version numbers.
func encodeMeta(meta map[string]string) string {
	if len(meta) == 0 {
		return "{}"
	}
	b, err := json.Marshal(meta)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// validateMetadataJSON ensures user-supplied metadata is a JSON object
// (or empty, which normalizes to "{}"). The literal JSON "null" unmarshals
// into a nil map without error, so it is rejected explicitly.
func validateMetadataJSON(s string) (string, error) {
	if s == "" {
		return "{}", nil
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil || m == nil {
		return "", domain.Errorf(domain.ErrInvalidArgument, "metadata must be a JSON object")
	}
	return s, nil
}

const maxValueBytes = 1 << 20 // 1 MiB per parameter/secret value

// validateRef validates a resource address (namespace + relative key) at the
// core boundary before any storage or crypto call touches it.
func validateRef(ref domain.Ref) error {
	if err := keyutil.ValidateNamespace(ref.NS); err != nil {
		return domain.Errorf(domain.ErrInvalidArgument, "%v", err)
	}
	if err := keyutil.ValidateKey(ref.Key); err != nil {
		return domain.Errorf(domain.ErrInvalidArgument, "%v", err)
	}
	return nil
}

// validateListScope validates a list request's namespace and optional key
// prefix. An empty prefix lists the whole namespace; a non-empty prefix must be
// a legal key.
func validateListScope(ns domain.NamespaceRef, keyPrefix string) error {
	if err := keyutil.ValidateNamespace(ns); err != nil {
		return domain.Errorf(domain.ErrInvalidArgument, "%v", err)
	}
	if keyPrefix != "" {
		if err := keyutil.ValidateKey(keyPrefix); err != nil {
			return domain.Errorf(domain.ErrInvalidArgument, "%v", err)
		}
	}
	return nil
}

// authMethodAllowed reports whether method is in the namespace's allowed set.
func authMethodAllowed(allowed []domain.AuthMethod, method domain.AuthMethod) bool {
	return slices.Contains(allowed, method)
}

// normalizeAuthMethods validates a requested auth-method set and applies the
// default (mTLS-only, the strongest posture) when the set is empty.
func normalizeAuthMethods(methods []domain.AuthMethod) ([]domain.AuthMethod, error) {
	if len(methods) == 0 {
		return []domain.AuthMethod{domain.AuthMethodMTLS}, nil
	}
	for _, m := range methods {
		if !domain.ValidAuthMethod(m) {
			return nil, domain.Errorf(domain.ErrInvalidArgument, "unknown auth method %q", m)
		}
	}
	return methods, nil
}

// --- built-in CA key envelope ----------------------------------------------

// caKeyAAD binds the CA private-key envelope to the CA key's stable identity,
// mirroring the discipline the secret layer uses (recompute the AAD from the
// row's identity rather than trusting a stored column). A row whose material was
// relocated fails authentication instead of yielding the wrong key.
func caKeyAAD(id string) string { return "kms/v1|ca-key|id=" + id }

// newCAKeyID returns a fresh CA key identifier (e.g. "ca-7f3a").
func newCAKeyID() (string, error) {
	b := make([]byte, 2)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "ca-" + hex.EncodeToString(b), nil
}

// wrapCAKey envelope-encrypts the CA private-key PEM under kek, producing the
// two blobs stored in ca_keys: the DEK-encrypted key material (nonce-prefixed
// ciphertext) and the KEK-wrapped DEK. Rotation later rewraps only the DEK.
func wrapCAKey(kek *crypto.KEK, id string, keyPEM []byte) (encryptedKey, encryptedDEK []byte, err error) {
	res, err := crypto.Encrypt(kek, keyPEM, caKeyAAD(id))
	if err != nil {
		return nil, nil, err
	}
	return packNonceCiphertext(res.Nonce, res.Ciphertext), res.EncryptedDEK, nil
}

// unwrapCAKey reverses wrapCAKey, recovering the CA private-key PEM.
func unwrapCAKey(kek *crypto.KEK, rec storage.CAKeyRecord) ([]byte, error) {
	nonce, ct, err := unpackNonceCiphertext(rec.EncryptedKey)
	if err != nil {
		return nil, err
	}
	return crypto.Decrypt(kek, crypto.DecryptInput{
		Ciphertext:   ct,
		EncryptedDEK: rec.EncryptedDEK,
		Nonce:        nonce,
		AAD:          caKeyAAD(rec.ID),
		WrapMode:     domain.WrapModeStandard,
	})
}

// packNonceCiphertext frames a GCM nonce and ciphertext as a single blob
// (1-byte nonce length, the nonce, then the ciphertext) so the CA key envelope
// fits the single ca_keys.encrypted_key column with no separate nonce field.
func packNonceCiphertext(nonce, ct []byte) []byte {
	out := make([]byte, 0, 1+len(nonce)+len(ct))
	out = append(out, byte(len(nonce)))
	out = append(out, nonce...)
	out = append(out, ct...)
	return out
}

func unpackNonceCiphertext(b []byte) (nonce, ct []byte, err error) {
	if len(b) < 1 {
		return nil, nil, fmt.Errorf("ca key material is empty: %w", domain.ErrDecryptFailed)
	}
	n := int(b[0])
	if len(b) < 1+n {
		return nil, nil, fmt.Errorf("ca key material is truncated: %w", domain.ErrDecryptFailed)
	}
	return b[1 : 1+n], b[1+n:], nil
}
