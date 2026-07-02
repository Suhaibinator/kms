// Package storage persists all parameter-store state in SQLite.
//
// This file is the CONTRACT between the storage implementation and the rest
// of the service. The implementation lives in this package as *SQLStore and
// must satisfy Store. Storage never sees plaintext secrets or key material:
// it stores ciphertext, wrapped DEKs, and token hashes only. All multi-step
// operations documented as atomic must run in a single SQLite transaction.
//
// Times are persisted as RFC3339Nano UTC strings. Page tokens are opaque
// strings produced and consumed only by this package ("" = first page).
package storage

import (
	"context"
	"time"

	"github.com/Suhaibinator/kms/internal/domain"
)

// SecretRecord is the secret-level row.
type SecretRecord struct {
	ID              int64
	Path            string
	ClientBound     bool
	AccessTokenHash []byte // nil when no per-secret token is set
	ContentType     string
	Metadata        string
	CreatedAt       time.Time
	UpdatedAt       time.Time
	Labels          map[string]uint64
}

// SecretVersionRecord is one stored secret version including ciphertext and
// wrapping metadata. Ciphertext, EncryptedDEK, and Nonce are nil for
// destroyed versions.
type SecretVersionRecord struct {
	ID            int64
	SecretID      int64
	Version       uint64
	Ciphertext    []byte
	EncryptedDEK  []byte
	KEKID         string
	WrapMode      string // domain.WrapModeStandard | domain.WrapModeClientBound
	ClientKeySalt []byte // HKDF salt for client-bound versions, else nil
	Algorithm     string
	Nonce         []byte
	AAD           string
	State         string
	CreatedBy     string
	CreatedAt     time.Time
	DestroyedAt   time.Time
	ExpiresAt     time.Time
	Metadata      string
}

// EncryptedPayload is what the service layer produces for a new secret
// version. The storage layer persists it verbatim.
type EncryptedPayload struct {
	Ciphertext    []byte
	EncryptedDEK  []byte
	KEKID         string
	WrapMode      string
	ClientKeySalt []byte
	Algorithm     string
	Nonce         []byte
	AAD           string
}

// CreateSecretParams describes a new secret version write.
//
// Encrypt is called exactly once, inside the write transaction, with the
// version number the new version will receive (the AAD binds it). It must be
// pure computation — no I/O, no calls back into storage.
type CreateSecretParams struct {
	Path        string
	ContentType string
	Metadata    string
	CreatedBy   string
	ClientBound bool // required mode; must match the existing secret if present
	// AccessTokenHash, when non-nil, is stored on the secret row (sha256 of
	// the per-secret token). It may be set on creation or when minting a new
	// token for an existing secret.
	AccessTokenHash []byte
	ExpiresAt       time.Time // zero = never
	Encrypt         func(version uint64) (EncryptedPayload, error)
}

// ListPage bundles pagination inputs.
type ListPage struct {
	Limit int    // implementation clamps to [1, 1000]; 0 means default (100)
	Token string // "" = first page
}

// Store is everything the service layer needs from persistence.
type Store interface {
	// --- lifecycle -------------------------------------------------------

	// Ping verifies the database is reachable.
	Ping(ctx context.Context) error
	// Close closes the underlying database.
	Close() error
	// Backup writes a consistent online backup to destPath (VACUUM INTO).
	Backup(ctx context.Context, destPath string) error

	// --- key metadata ----------------------------------------------------

	InsertKeyMetadata(ctx context.Context, km domain.KeyMetadata) error
	GetKeyMetadata(ctx context.Context, id string) (domain.KeyMetadata, error)
	ListKeyMetadata(ctx context.Context) ([]domain.KeyMetadata, error)
	// ActiveKeyMetadata returns the single key with state "active".
	// domain.ErrNotFound when none exists (fresh database).
	ActiveKeyMetadata(ctx context.Context) (domain.KeyMetadata, error)
	SetKeyState(ctx context.Context, id, state string) error

	// RotateKEK atomically (single transaction): inserts newKM with state
	// "active", marks every other key "retired", and rewraps every
	// non-destroyed secret version by storing rewrap's returned encrypted DEK
	// with kek_id = newKM.ID. rewrap must be pure computation (no I/O, no
	// storage calls). Returns the number of versions rewrapped. On any error
	// the database is unchanged.
	RotateKEK(ctx context.Context, newKM domain.KeyMetadata,
		rewrap func(rec SecretVersionRecord) (newEncryptedDEK []byte, err error)) (int, error)

	// --- namespaces ------------------------------------------------------

	CreateNamespace(ctx context.Context, ns domain.Namespace) (domain.Namespace, error)
	ListNamespaces(ctx context.Context, page ListPage) ([]domain.Namespace, string, error)

	// --- parameters ------------------------------------------------------

	// PutParameter atomically upserts the parameter, appends an immutable
	// version, moves labels (current -> new, previous -> old current), and
	// appends a change-log entry. Returns the new version and revision.
	PutParameter(ctx context.Context, path, value, contentType, metadata, createdBy string) (version, revision uint64, err error)

	// GetParameter resolves version (>0) or label (default "current").
	GetParameter(ctx context.Context, path string, version uint64, label string) (domain.Parameter, error)
	GetParameterInfo(ctx context.Context, path string) (domain.ParameterInfo, error)
	// ListParameters returns each matching parameter at its "current" label,
	// ordered by path.
	ListParameters(ctx context.Context, prefix string, page ListPage) ([]domain.Parameter, string, error)
	// DeleteParameter removes the parameter and all versions, and appends a
	// change-log entry. Returns the revision.
	DeleteParameter(ctx context.Context, path string) (uint64, error)

	// --- secrets ---------------------------------------------------------

	// CreateSecretVersion atomically creates/updates the secret row, assigns
	// the next version number, invokes p.Encrypt(version), stores the
	// payload, moves labels, and appends a metadata-only change-log entry.
	// Fails with domain.ErrFailedPrecondition if p.ClientBound does not match
	// an existing secret's mode.
	CreateSecretVersion(ctx context.Context, p CreateSecretParams) (version, revision uint64, err error)

	GetSecretRecord(ctx context.Context, path string) (SecretRecord, error)
	// GetSecretVersion resolves version (>0) or label (default "current") and
	// returns both the secret row and the version row.
	GetSecretVersion(ctx context.Context, path string, version uint64, label string) (SecretRecord, SecretVersionRecord, error)
	GetSecretInfo(ctx context.Context, path string) (domain.Secret, error)
	ListSecrets(ctx context.Context, prefix string, page ListPage) ([]domain.Secret, string, error)
	DeleteSecret(ctx context.Context, path string) (uint64, error)

	// SetSecretVersionState enables/disables version (0 = all non-destroyed
	// versions). Destroyed versions are never resurrected. Appends a
	// change-log entry (disable/enable). Returns the revision.
	SetSecretVersionState(ctx context.Context, path string, version uint64, state string) (uint64, error)

	// DestroySecretVersion irreversibly nulls ciphertext, encrypted DEK, and
	// nonce, sets state destroyed + destroyed_at, appends a change-log entry.
	DestroySecretVersion(ctx context.Context, path string, version uint64) (uint64, error)

	// PromoteSecretVersion atomically points "current" at version and
	// "previous" at the old current (if different). The target version must
	// exist and be enabled. Appends a change-log entry (promote).
	PromoteSecretVersion(ctx context.Context, path string, version uint64) (current, previous, revision uint64, err error)

	// UpdateSecretAccessTokenHash replaces the per-secret token hash.
	UpdateSecretAccessTokenHash(ctx context.Context, path string, hash []byte) error

	// --- identities ------------------------------------------------------

	CreateIdentity(ctx context.Context, name, kind string, tokenHash []byte) (domain.Identity, error)
	GetIdentityByTokenHash(ctx context.Context, tokenHash []byte) (domain.Identity, error)
	GetIdentityByName(ctx context.Context, name string) (domain.Identity, error)
	ListIdentities(ctx context.Context, page ListPage) ([]domain.Identity, string, error)
	SetIdentityDisabled(ctx context.Context, name string, disabled bool) error
	UpdateIdentityTokenHash(ctx context.Context, name string, tokenHash []byte) error

	// --- policies --------------------------------------------------------

	CreatePolicy(ctx context.Context, p domain.Policy) (domain.Policy, error)
	UpdatePolicy(ctx context.Context, p domain.Policy) (domain.Policy, error)
	DeletePolicy(ctx context.Context, name string) error
	ListPolicies(ctx context.Context, page ListPage) ([]domain.Policy, string, error)
	// PoliciesForSubject returns all policies whose subject is the given
	// identity name or "*".
	PoliciesForSubject(ctx context.Context, subject string) ([]domain.Policy, error)

	// --- audit -----------------------------------------------------------

	AppendAudit(ctx context.Context, ev domain.AuditEvent) error
	ListAudit(ctx context.Context, f domain.AuditFilter, page ListPage) ([]domain.AuditEvent, string, error)

	// --- change log / revisions ------------------------------------------

	// CurrentRevision returns the latest assigned revision (0 on fresh DB).
	CurrentRevision(ctx context.Context) (uint64, error)
	// OldestRetainedRevision returns the smallest revision still in the
	// change log, or 0 when the log is empty.
	OldestRetainedRevision(ctx context.Context) (uint64, error)
	// ListChangesSince returns entries with revision > since, ascending,
	// up to limit.
	ListChangesSince(ctx context.Context, since uint64, limit int) ([]domain.ChangeLogEntry, error)
	// PruneChangeLog deletes entries older than keepDuration AND beyond
	// maxRows most recent, whichever retains fewer rows. Revisions are never
	// reused after pruning.
	PruneChangeLog(ctx context.Context, keepDuration time.Duration, maxRows int) (int, error)
	// SnapshotParameters returns, in one consistent read transaction, the
	// current revision and the "current" value of every parameter matching
	// any of the given patterns (see pathutil.Match).
	SnapshotParameters(ctx context.Context, patterns []string) ([]domain.Parameter, uint64, error)
}
