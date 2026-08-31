package crypto

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"

	"golang.org/x/crypto/argon2"

	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/fileutil"
)

// KEK is a key-encryption key. Key material is unexported; the only ways to
// obtain a KEK are NewKEKFromMaterial, LoadKEKFromFile, and
// DeriveKEKFromPassphrase.
type KEK struct {
	ID  string
	key []byte
}

// NewKEKFromMaterial builds a KEK from exactly 32 bytes of key material.
// The material is copied; the caller should Zero its own buffer.
func NewKEKFromMaterial(id string, material []byte) (*KEK, error) {
	if len(material) != keySize {
		return nil, fmt.Errorf("KEK material must be %d bytes, got %d", keySize, len(material))
	}
	if id == "" {
		return nil, errors.New("KEK id must not be empty")
	}
	k := &KEK{ID: id, key: bytes.Clone(material)}
	return k, nil
}

// Destroy zeroes the key material. The KEK is unusable afterwards.
func (k *KEK) Destroy() {
	if k != nil {
		Zero(k.key)
	}
}

// NewKEKID returns a fresh random KEK identifier.
func NewKEKID() (string, error) {
	b, err := randomBytes(6)
	if err != nil {
		return "", err
	}
	return "kek-" + hex.EncodeToString(b), nil
}

// LoadKEKMaterialFromFile reads master-key material from a file. Accepted
// encodings, tried in order:
//  1. exactly 32 raw bytes
//  2. 64 hex characters (surrounding whitespace ignored)
//  3. standard or URL-safe base64 of 32 bytes (surrounding whitespace ignored)
//
// The returned slice should be zeroed by the caller after use.
func LoadKEKMaterialFromFile(path string) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	defer Zero(raw)
	if len(raw) == keySize {
		return bytes.Clone(raw), nil
	}
	text := strings.TrimSpace(string(raw))
	if len(text) == 2*keySize {
		if b, err := hex.DecodeString(text); err == nil {
			return b, nil
		}
	}
	for _, enc := range []*base64.Encoding{
		base64.StdEncoding, base64.RawStdEncoding,
		base64.URLEncoding, base64.RawURLEncoding,
	} {
		if b, err := enc.DecodeString(text); err == nil && len(b) == keySize {
			return b, nil
		}
	}
	return nil, fmt.Errorf("key file %s: expected 32 raw bytes, 64 hex chars, or base64 of 32 bytes", path)
}

// WriteKEKMaterialFile writes fresh key material to path with 0600
// permissions, refusing to overwrite an existing file.
func WriteKEKMaterialFile(path string) ([]byte, error) {
	material, err := randomBytes(keySize)
	if err != nil {
		return nil, err
	}
	f, err := fileutil.OpenPrivateExclusive(path)
	if err != nil {
		Zero(material)
		return nil, err
	}
	_, werr := f.Write([]byte(hex.EncodeToString(material)))
	cerr := f.Close()
	if werr != nil || cerr != nil {
		Zero(material)
		return nil, errors.Join(werr, cerr)
	}
	return material, nil
}

// Argon2Params captures the argon2id cost parameters persisted in key
// metadata so derivation is reproducible across restarts and versions.
type Argon2Params struct {
	Version int    `json:"version"` // argon2 version (0x13)
	Time    uint32 `json:"time"`
	MemoryK uint32 `json:"memory_kib"`
	Threads uint8  `json:"threads"`
	KeyLen  uint32 `json:"key_len"`
}

// DefaultArgon2Params follows RFC 9106's second recommended option
// (64 MiB, t=3) — memory-hard but reasonable on small servers.
func DefaultArgon2Params() Argon2Params {
	threads := max(min(runtime.NumCPU(), 4), 1)
	return Argon2Params{
		Version: argon2.Version,
		Time:    3,
		MemoryK: 64 * 1024,
		Threads: uint8(threads),
		KeyLen:  keySize,
	}
}

// MarshalParams serializes params for the key-metadata table.
func MarshalParams(p Argon2Params) string {
	b, _ := json.Marshal(p)
	return string(b)
}

// UnmarshalParams parses stored KDF params.
func UnmarshalParams(s string) (Argon2Params, error) {
	var p Argon2Params
	if err := json.Unmarshal([]byte(s), &p); err != nil {
		return Argon2Params{}, fmt.Errorf("parsing argon2 params: %w", err)
	}
	if p.Version != argon2.Version {
		return Argon2Params{}, fmt.Errorf("unsupported argon2 version %d", p.Version)
	}
	if p.Time == 0 || p.MemoryK == 0 || p.Threads == 0 || p.KeyLen != keySize {
		return Argon2Params{}, errors.New("invalid argon2 params")
	}
	return p, nil
}

// DeriveKEKMaterialFromPassphrase stretches a human passphrase into KEK
// material with argon2id. The passphrase buffer is NOT zeroed here; the
// caller owns it and must Zero it after this returns.
func DeriveKEKMaterialFromPassphrase(passphrase, salt []byte, p Argon2Params) ([]byte, error) {
	if len(passphrase) == 0 {
		return nil, errors.New("empty passphrase")
	}
	if len(salt) < 16 {
		return nil, errors.New("argon2 salt must be at least 16 bytes")
	}
	if p.KeyLen != keySize {
		return nil, fmt.Errorf("argon2 key length must be %d", keySize)
	}
	return argon2.IDKey(passphrase, salt, p.Time, p.MemoryK, p.Threads, p.KeyLen), nil
}

// NewKDFSalt returns a fresh salt for passphrase derivation.
func NewKDFSalt() ([]byte, error) { return randomBytes(32) }

// keyCheckCanary is the fixed plaintext encrypted under a KEK at
// initialization. Decrypting it proves the presented key is the right one
// before any secret is touched.
const keyCheckCanary = "kms/v1 key-check canary"

func keyCheckAAD(kekID string) []byte {
	return []byte("kms/v1|key-check|kek:" + kekID)
}

// NewKeyCheck produces the key-check blob stored in key metadata.
func NewKeyCheck(kek *KEK) ([]byte, error) {
	return sealPacked(kek.key, []byte(keyCheckCanary), keyCheckAAD(kek.ID))
}

// VerifyKeyCheck confirms kek can decrypt the stored key-check blob.
// A mismatch returns domain.ErrDecryptFailed.
func VerifyKeyCheck(kek *KEK, stored []byte) error {
	pt, err := openPacked(kek.key, stored, keyCheckAAD(kek.ID))
	if err != nil {
		return fmt.Errorf("master key verification failed: %w", domain.ErrDecryptFailed)
	}
	ok := subtleEqual(pt, []byte(keyCheckCanary))
	Zero(pt)
	if !ok {
		return fmt.Errorf("master key verification failed: %w", domain.ErrDecryptFailed)
	}
	return nil
}

func subtleEqual(a, b []byte) bool {
	// GCM already authenticates; this is belt-and-braces, not secret-dependent.
	return bytes.Equal(a, b)
}

// Keyring holds the active KEK plus retired KEKs still needed to decrypt
// historical records. It is safe for concurrent use.
type Keyring struct {
	mu     sync.RWMutex
	active *KEK
	byID   map[string]*KEK
}

// NewKeyring builds a keyring with the given active KEK.
func NewKeyring(active *KEK) *Keyring {
	return &Keyring{active: active, byID: map[string]*KEK{active.ID: active}}
}

// Active returns the KEK used for new encryptions.
func (r *Keyring) Active() *KEK {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.active
}

// Get returns the KEK with the given id (active or retired).
func (r *Keyring) Get(id string) (*KEK, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	k, ok := r.byID[id]
	if !ok {
		return nil, fmt.Errorf("unknown KEK %q: %w", id, domain.ErrDecryptFailed)
	}
	return k, nil
}

// ActiveSubkey derives a domain-separated key from the active KEK without
// exposing the KEK material. The returned id lets callers persist which KEK
// was used so the same subkey can be recovered after rotation or restart.
func (r *Keyring) ActiveSubkey(purpose string) (string, [sha256.Size]byte, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.active == nil {
		return "", [sha256.Size]byte{}, errors.New("active KEK is unavailable")
	}
	return r.active.ID, deriveSubkey(r.active, purpose), nil
}

// Subkey derives the same domain-separated key from the identified active or
// retired KEK. It is used to open records that outlive a KEK rotation.
func (r *Keyring) Subkey(id, purpose string) ([sha256.Size]byte, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	k, ok := r.byID[id]
	if !ok {
		return [sha256.Size]byte{}, fmt.Errorf("unknown KEK %q: %w", id, domain.ErrDecryptFailed)
	}
	return deriveSubkey(k, purpose), nil
}

func deriveSubkey(k *KEK, purpose string) [sha256.Size]byte {
	mac := hmac.New(sha256.New, k.key)
	_, _ = mac.Write([]byte("kms/subkey/v1\x00"))
	_, _ = mac.Write([]byte(purpose))
	var out [sha256.Size]byte
	copy(out[:], mac.Sum(nil))
	return out
}

// Add registers a retired KEK for decryption of historical records.
func (r *Keyring) Add(k *KEK) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.byID[k.ID] = k
}

// SetActive registers k and makes it the active KEK.
func (r *Keyring) SetActive(k *KEK) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.byID[k.ID] = k
	r.active = k
}

// TokenHash returns the SHA-256 digest used to store and look up bearer
// tokens and per-secret access tokens. Tokens are high-entropy random values,
// so an unsalted hash is an appropriate verifier — and it must be
// deterministic to serve as a lookup key.
func TokenHash(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// GenerateToken mints a high-entropy bearer token with the given prefix
// (e.g. "kms" or "kmss" for secret tokens) and returns the token plus its
// storage hash. The plaintext token is shown once and never persisted.
func GenerateToken(prefix string) (token string, hash []byte, err error) {
	b, err := randomBytes(32)
	if err != nil {
		return "", nil, err
	}
	token = prefix + "_" + base64.RawURLEncoding.EncodeToString(b)
	return token, TokenHash(token), nil
}
