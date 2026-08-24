package core

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"

	"github.com/Suhaibinator/kms/internal/domain"
)

const (
	filteredCursorVersion   = 2
	filteredScanBatchSize   = 1000
	maxFilteredScanBatches  = 4
	maxFilteredCursorLength = 8192
	filteredCursorFrameSize = 1024
	filteredCursorKeyIDSize = 255
	filteredCursorPurpose   = "filtered-pagination"
)

type filteredCursorPayload struct {
	Version int    `json:"v"`
	Kind    string `json:"kind"`
	Scope   string `json:"scope"`
	Raw     string `json:"raw"`
}

func mustNewFilteredPageKey() [32]byte {
	var key [32]byte
	if _, err := io.ReadFull(rand.Reader, key[:]); err != nil {
		panic(fmt.Sprintf("initialize filtered pagination key: %v", err))
	}
	return key
}

func filteredCursorAEAD(key [sha256.Size]byte) cipher.AEAD {
	block, err := aes.NewCipher(key[:])
	if err != nil {
		panic(fmt.Sprintf("initialize filtered pagination cipher: %v", err))
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		panic(fmt.Sprintf("initialize filtered pagination AEAD: %v", err))
	}
	return aead
}

func (s *Service) filteredCursorSealKey() (string, [sha256.Size]byte, error) {
	if keyring := s.keyring.Load(); keyring != nil {
		return keyring.ActiveSubkey(filteredCursorPurpose)
	}
	// Unit consumers that have no keyring still get opaque process-local
	// cursors. Production services are not ready until SetKeyring succeeds.
	return "", s.filteredPageKey, nil
}

func (s *Service) filteredCursorOpenKey(id string) ([sha256.Size]byte, error) {
	if id == "" {
		return s.filteredPageKey, nil
	}
	keyring := s.keyring.Load()
	if keyring == nil {
		return [sha256.Size]byte{}, fmt.Errorf("filtered cursor KEK is unavailable")
	}
	return keyring.Subkey(id, filteredCursorPurpose)
}

func (s *Service) sealFilteredCursor(kind, scope, raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	payload, err := json.Marshal(filteredCursorPayload{
		Version: filteredCursorVersion,
		Kind:    kind,
		Scope:   scope,
		Raw:     raw,
	})
	if err != nil {
		return "", err
	}
	if len(payload) > filteredCursorFrameSize-2 {
		return "", domain.Errorf(domain.ErrInvalidArgument, "pagination state is too large")
	}
	keyID, key, err := s.filteredCursorSealKey()
	if err != nil {
		return "", err
	}
	if len(keyID) > filteredCursorKeyIDSize {
		return "", fmt.Errorf("active KEK id is too long for pagination state")
	}
	aead := filteredCursorAEAD(key)
	header := make([]byte, 2+filteredCursorKeyIDSize)
	if _, err := io.ReadFull(rand.Reader, header); err != nil {
		return "", err
	}
	header[0] = filteredCursorVersion
	header[1] = byte(len(keyID))
	copy(header[2:], keyID)
	frame := make([]byte, filteredCursorFrameSize)
	if _, err := io.ReadFull(rand.Reader, frame); err != nil {
		return "", err
	}
	binary.BigEndian.PutUint16(frame[:2], uint16(len(payload)))
	copy(frame[2:], payload)
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	sealed := make([]byte, 0, len(header)+len(nonce)+len(frame)+aead.Overhead())
	sealed = append(sealed, header...)
	sealed = append(sealed, nonce...)
	sealed = aead.Seal(sealed, nonce, frame, header)
	return base64.RawURLEncoding.EncodeToString(sealed), nil
}

func (s *Service) openFilteredCursor(token, kind, scope string) (string, error) {
	if token == "" {
		return "", nil
	}
	invalid := func() (string, error) {
		return "", domain.Errorf(domain.ErrInvalidArgument, "invalid page token")
	}
	if len(token) > maxFilteredCursorLength {
		return invalid()
	}
	sealed, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return invalid()
	}
	const headerSize = 2 + filteredCursorKeyIDSize
	if len(sealed) < headerSize || sealed[0] != filteredCursorVersion || int(sealed[1]) > filteredCursorKeyIDSize {
		return invalid()
	}
	header := sealed[:headerSize]
	keyID := string(header[2 : 2+int(header[1])])
	key, err := s.filteredCursorOpenKey(keyID)
	if err != nil {
		return invalid()
	}
	aead := filteredCursorAEAD(key)
	expectedSize := headerSize + aead.NonceSize() + filteredCursorFrameSize + aead.Overhead()
	if len(sealed) != expectedSize {
		return invalid()
	}
	nonce := sealed[headerSize : headerSize+aead.NonceSize()]
	ciphertext := sealed[headerSize+aead.NonceSize():]
	plaintext, err := aead.Open(nil, nonce, ciphertext, header)
	if err != nil {
		return invalid()
	}
	if len(plaintext) != filteredCursorFrameSize {
		return invalid()
	}
	payloadSize := int(binary.BigEndian.Uint16(plaintext[:2]))
	if payloadSize == 0 || payloadSize > filteredCursorFrameSize-2 {
		return invalid()
	}
	var payload filteredCursorPayload
	if err := json.Unmarshal(plaintext[2:2+payloadSize], &payload); err != nil ||
		payload.Version != filteredCursorVersion || payload.Kind != kind ||
		payload.Scope != scope || payload.Raw == "" {
		return invalid()
	}
	return payload.Raw, nil
}

func filteredCursorScope(pr Principal, filter any) string {
	payload, _ := json.Marshal(struct {
		IdentityID int64  `json:"identity_id"`
		Identity   string `json:"identity"`
		Method     string `json:"method"`
		Filter     any    `json:"filter"`
	}{IdentityID: pr.Identity.ID, Identity: pr.Identity.Name, Method: string(pr.Method), Filter: filter})
	sum := sha256.Sum256(payload)
	return base64.RawURLEncoding.EncodeToString(sum[:])
}
