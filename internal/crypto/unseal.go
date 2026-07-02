package crypto

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"time"

	"github.com/Suhaibinator/kms/internal/domain"
)

// KeyMetadataStore is the slice of storage the unseal flow needs. Satisfied
// by storage.Store.
type KeyMetadataStore interface {
	ActiveKeyMetadata(ctx context.Context) (domain.KeyMetadata, error)
	InsertKeyMetadata(ctx context.Context, km domain.KeyMetadata) error
}

// PassphrasePrompter reads a passphrase without echo. confirm asks the human
// to enter it twice (first initialization). Implementations live in the CLI
// layer; crypto never touches stdin itself.
type PassphrasePrompter func(confirm bool) ([]byte, error)

// UnsealOptions describes where master-key material may come from, in the
// order mandated by the plan: key file first, then passphrase (pre-supplied
// via environment, or prompted on a TTY).
type UnsealOptions struct {
	// KeyFilePath is encryption.kek_file. Empty disables file mode.
	KeyFilePath string
	// CreateKeyFileIfMissing generates fresh key material at KeyFilePath
	// (0600, hex) when initializing a fresh database and the file is absent.
	CreateKeyFileIfMissing bool
	// Passphrase, when non-empty, is used instead of prompting (e.g. from
	// KMS_MASTER_PASSPHRASE for unattended passphrase-mode restarts). The
	// buffer is zeroed by Unseal.
	Passphrase []byte
	// Prompt reads a passphrase interactively; nil when stdin is not a TTY.
	Prompt PassphrasePrompter
}

// ErrNoKeySource is returned when neither a key file nor any way to obtain a
// passphrase is available.
var ErrNoKeySource = errors.New(
	"no master key source: configure encryption.kek_file, set KMS_MASTER_PASSPHRASE, or run on a TTY for an interactive prompt")

// Unseal acquires and verifies the master key, initializing key metadata on
// first run. It returns a ready keyring. Every failure path is fail-fast with
// an actionable error; a wrong key or passphrase is detected via the stored
// key-check value before any secret is touched.
func Unseal(ctx context.Context, store KeyMetadataStore, opts UnsealOptions) (*Keyring, error) {
	defer Zero(opts.Passphrase)

	km, err := store.ActiveKeyMetadata(ctx)
	switch {
	case errors.Is(err, domain.ErrNotFound):
		return initialize(ctx, store, opts)
	case err != nil:
		return nil, fmt.Errorf("loading key metadata: %w", err)
	}
	return unlock(km, opts)
}

func initialize(ctx context.Context, store KeyMetadataStore, opts UnsealOptions) (*Keyring, error) {
	id, err := NewKEKID()
	if err != nil {
		return nil, err
	}
	km := domain.KeyMetadata{
		ID:        id,
		State:     domain.KeyStateActive,
		CreatedAt: time.Now().UTC(),
	}

	var material []byte
	switch {
	case opts.KeyFilePath != "" && fileExists(opts.KeyFilePath):
		material, err = LoadKEKMaterialFromFile(opts.KeyFilePath)
		if err != nil {
			return nil, err
		}
		km.Source = domain.KeySourceFile

	case opts.KeyFilePath != "" && opts.CreateKeyFileIfMissing:
		material, err = WriteKEKMaterialFile(opts.KeyFilePath)
		if err != nil {
			return nil, fmt.Errorf("creating master key file: %w", err)
		}
		km.Source = domain.KeySourceFile

	default:
		passphrase, err := obtainPassphrase(opts, true)
		if err != nil {
			return nil, err
		}
		params := DefaultArgon2Params()
		salt, err := NewKDFSalt()
		if err != nil {
			Zero(passphrase)
			return nil, err
		}
		material, err = DeriveKEKMaterialFromPassphrase(passphrase, salt, params)
		Zero(passphrase)
		if err != nil {
			return nil, err
		}
		km.Source = domain.KeySourcePassphrase
		km.KDF = MarshalParams(params)
		km.KDFSalt = salt
	}
	defer Zero(material)

	kek, err := NewKEKFromMaterial(km.ID, material)
	if err != nil {
		return nil, err
	}
	if km.KeyCheck, err = NewKeyCheck(kek); err != nil {
		return nil, err
	}
	if err := store.InsertKeyMetadata(ctx, km); err != nil {
		kek.Destroy()
		return nil, fmt.Errorf("persisting key metadata: %w", err)
	}
	return NewKeyring(kek), nil
}

func unlock(km domain.KeyMetadata, opts UnsealOptions) (*Keyring, error) {
	var material []byte
	var err error

	switch km.Source {
	case domain.KeySourceFile:
		if opts.KeyFilePath == "" {
			return nil, errors.New(
				"this database was initialized with a file-based master key, but encryption.kek_file is not configured")
		}
		material, err = LoadKEKMaterialFromFile(opts.KeyFilePath)
		if err != nil {
			return nil, fmt.Errorf(
				"this database was initialized with a file-based master key and the key file is unavailable (%w); restore the key file — a passphrase cannot replace it", err)
		}

	case domain.KeySourcePassphrase:
		passphrase, perr := obtainPassphrase(opts, false)
		if perr != nil {
			return nil, perr
		}
		params, perr := UnmarshalParams(km.KDF)
		if perr != nil {
			Zero(passphrase)
			return nil, perr
		}
		material, err = DeriveKEKMaterialFromPassphrase(passphrase, km.KDFSalt, params)
		Zero(passphrase)
		if err != nil {
			return nil, err
		}

	default:
		return nil, fmt.Errorf("unknown master key source %q in key metadata", km.Source)
	}
	defer Zero(material)

	kek, err := NewKEKFromMaterial(km.ID, material)
	if err != nil {
		return nil, err
	}
	if err := VerifyKeyCheck(kek, km.KeyCheck); err != nil {
		kek.Destroy()
		if km.Source == domain.KeySourcePassphrase {
			return nil, fmt.Errorf("wrong master passphrase: %w", err)
		}
		return nil, fmt.Errorf("master key file does not match this database: %w", err)
	}
	return NewKeyring(kek), nil
}

func obtainPassphrase(opts UnsealOptions, confirm bool) ([]byte, error) {
	if len(opts.Passphrase) > 0 {
		p := make([]byte, len(opts.Passphrase))
		copy(p, opts.Passphrase)
		return p, nil
	}
	if opts.Prompt == nil {
		return nil, ErrNoKeySource
	}
	return opts.Prompt(confirm)
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return !errors.Is(err, fs.ErrNotExist)
}
