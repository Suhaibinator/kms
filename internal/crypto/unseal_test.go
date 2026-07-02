package crypto

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Suhaibinator/kms/internal/domain"
)

// fakeKMStore is a minimal in-memory KeyMetadataStore for the unseal flow.
type fakeKMStore struct {
	active    *domain.KeyMetadata
	activeErr error
	insertErr error
	inserts   int
}

func (f *fakeKMStore) ActiveKeyMetadata(context.Context) (domain.KeyMetadata, error) {
	if f.activeErr != nil {
		return domain.KeyMetadata{}, f.activeErr
	}
	if f.active == nil {
		return domain.KeyMetadata{}, domain.ErrNotFound
	}
	return *f.active, nil
}

func (f *fakeKMStore) InsertKeyMetadata(_ context.Context, km domain.KeyMetadata) error {
	if f.insertErr != nil {
		return f.insertErr
	}
	f.inserts++
	stored := km
	f.active = &stored
	return nil
}

func TestUnsealInitializeAndUnlockFileMode(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	keyFile := filepath.Join(dir, "master.key")
	store := &fakeKMStore{}

	// First run: fresh DB, no key file yet — create it.
	ring, err := Unseal(ctx, store, UnsealOptions{
		KeyFilePath:            keyFile,
		CreateKeyFileIfMissing: true,
	})
	if err != nil {
		t.Fatalf("Unseal(initialize): %v", err)
	}
	if ring.Active() == nil {
		t.Fatal("nil active KEK after initialize")
	}
	if store.active == nil || store.active.Source != domain.KeySourceFile {
		t.Fatalf("stored key source = %+v, want file", store.active)
	}
	if _, err := os.Stat(keyFile); err != nil {
		t.Fatalf("key file not created: %v", err)
	}

	// A value encrypted under the fresh keyring round-trips.
	aad := BuildAAD(domain.ResourceSecret, "/x", 1)
	res, err := Encrypt(ring.Active(), []byte("v"), aad)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	// Second run: existing DB + existing key file — unlock path.
	ring2, err := Unseal(ctx, store, UnsealOptions{KeyFilePath: keyFile})
	if err != nil {
		t.Fatalf("Unseal(unlock): %v", err)
	}
	got, err := Decrypt(ring2.Active(), stdInput(res))
	if err != nil {
		t.Fatalf("Decrypt with re-unsealed keyring: %v", err)
	}
	if string(got) != "v" {
		t.Fatalf("Decrypt = %q, want v", got)
	}
	if store.inserts != 1 {
		t.Fatalf("inserts = %d, want 1 (unlock must not re-insert)", store.inserts)
	}
}

func TestUnsealFileModeExistingFile(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	keyFile := filepath.Join(dir, "master.key")
	if _, err := WriteKEKMaterialFile(keyFile); err != nil {
		t.Fatalf("seed key file: %v", err)
	}
	store := &fakeKMStore{}

	ring, err := Unseal(ctx, store, UnsealOptions{KeyFilePath: keyFile})
	if err != nil {
		t.Fatalf("Unseal: %v", err)
	}
	if ring.Active() == nil {
		t.Fatal("nil active KEK")
	}
	if store.active.Source != domain.KeySourceFile {
		t.Fatalf("source = %q, want file", store.active.Source)
	}
}

func TestUnsealWrongKeyFileFailsVerification(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	keyFile := filepath.Join(dir, "master.key")
	store := &fakeKMStore{}
	if _, err := Unseal(ctx, store, UnsealOptions{KeyFilePath: keyFile, CreateKeyFileIfMissing: true}); err != nil {
		t.Fatalf("initialize: %v", err)
	}

	// Replace the key file with different material.
	wrongFile := filepath.Join(dir, "wrong.key")
	if _, err := WriteKEKMaterialFile(wrongFile); err != nil {
		t.Fatalf("write wrong file: %v", err)
	}
	_, err := Unseal(ctx, store, UnsealOptions{KeyFilePath: wrongFile})
	if !errors.Is(err, domain.ErrDecryptFailed) {
		t.Fatalf("Unseal(wrong key file) err = %v, want ErrDecryptFailed", err)
	}
}

func TestUnsealFileModeMissingFileOnDisk(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	keyFile := filepath.Join(dir, "master.key")
	store := &fakeKMStore{}
	if _, err := Unseal(ctx, store, UnsealOptions{KeyFilePath: keyFile, CreateKeyFileIfMissing: true}); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	// The DB is initialized (file mode), but the operator lost the key file.
	if err := os.Remove(keyFile); err != nil {
		t.Fatalf("remove key file: %v", err)
	}
	_, err := Unseal(ctx, store, UnsealOptions{KeyFilePath: keyFile})
	if err == nil {
		t.Fatal("Unseal with missing key file = nil error")
	}
	if !strings.Contains(err.Error(), "restore the key file") {
		t.Fatalf("error should tell the operator to restore the key file: %v", err)
	}
}

func TestUnsealFileModeMissingConfig(t *testing.T) {
	ctx := context.Background()
	// DB says the key came from a file, but no key file is configured now.
	store := &fakeKMStore{active: &domain.KeyMetadata{
		ID: "kek-x", Source: domain.KeySourceFile, State: domain.KeyStateActive,
	}}
	if _, err := Unseal(ctx, store, UnsealOptions{}); err == nil {
		t.Fatal("Unseal without configured key file = nil error")
	}
}

func TestUnsealPassphraseInitAndUnlock(t *testing.T) {
	ctx := context.Background()
	store := &fakeKMStore{}
	pass := []byte("correct horse battery staple")

	ring, err := Unseal(ctx, store, UnsealOptions{Passphrase: bytes.Clone(pass)})
	if err != nil {
		t.Fatalf("Unseal(passphrase init): %v", err)
	}
	if ring.Active() == nil {
		t.Fatal("nil active KEK")
	}
	if store.active.Source != domain.KeySourcePassphrase {
		t.Fatalf("source = %q, want passphrase", store.active.Source)
	}
	if store.active.KDF == "" || len(store.active.KDFSalt) == 0 {
		t.Fatal("passphrase metadata missing KDF params/salt")
	}

	// Correct passphrase unlocks.
	if _, err := Unseal(ctx, store, UnsealOptions{Passphrase: bytes.Clone(pass)}); err != nil {
		t.Fatalf("Unseal(correct passphrase): %v", err)
	}
	// Wrong passphrase is rejected via the key-check.
	_, err = Unseal(ctx, store, UnsealOptions{Passphrase: []byte("wrong passphrase entirely")})
	if !errors.Is(err, domain.ErrDecryptFailed) {
		t.Fatalf("Unseal(wrong passphrase) err = %v, want ErrDecryptFailed", err)
	}
}

func TestUnsealZeroesPassphraseBuffer(t *testing.T) {
	ctx := context.Background()
	store := &fakeKMStore{}
	pass := []byte("zero-me-after-use")
	if _, err := Unseal(ctx, store, UnsealOptions{Passphrase: pass}); err != nil {
		t.Fatalf("Unseal: %v", err)
	}
	for i, b := range pass {
		if b != 0 {
			t.Fatalf("passphrase buffer byte %d = %d, want 0 (not zeroed)", i, b)
		}
	}
}

func TestUnsealPromptUsedWhenNoOtherSource(t *testing.T) {
	ctx := context.Background()
	store := &fakeKMStore{}
	calls := 0
	opts := UnsealOptions{
		Prompt: func(confirm bool) ([]byte, error) {
			calls++
			if !confirm {
				t.Error("initialization prompt should ask for confirmation")
			}
			return []byte("prompted-pass"), nil
		},
	}
	if _, err := Unseal(ctx, store, opts); err != nil {
		t.Fatalf("Unseal(prompt): %v", err)
	}
	if calls != 1 {
		t.Fatalf("prompt called %d times, want 1", calls)
	}
}

func TestUnsealNoKeySource(t *testing.T) {
	ctx := context.Background()
	store := &fakeKMStore{}
	_, err := Unseal(ctx, store, UnsealOptions{})
	if !errors.Is(err, ErrNoKeySource) {
		t.Fatalf("Unseal(no source) err = %v, want ErrNoKeySource", err)
	}
}

func TestUnsealStoreError(t *testing.T) {
	ctx := context.Background()
	sentinel := errors.New("db down")
	store := &fakeKMStore{activeErr: sentinel}
	_, err := Unseal(ctx, store, UnsealOptions{})
	if !errors.Is(err, sentinel) {
		t.Fatalf("Unseal err = %v, want wrapped %v", err, sentinel)
	}
}
