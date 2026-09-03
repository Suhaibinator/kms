// Package storage persists all parameter-store state in SQLite.
//
// This file is the CONTRACT between the storage implementation and the rest
// of the service. The implementation lives in this package as *SQLStore and
// must satisfy Store. Storage never sees plaintext secrets or key material:
// it stores ciphertext, wrapped DEKs, and token hashes only. All multi-step
// operations documented as atomic must run in a single SQLite transaction.
//
// Resources are addressed by domain.Ref (namespace + relative key), never by a
// parsed path string. A parameter or secret lives inside a namespace, which
// must already exist (the namespace_id foreign key requires it); operations
// against a missing namespace return domain.ErrNotFound naming it.
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
	Ref             domain.Ref
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
	ID       int64
	SecretID int64
	Version  uint64
	// ContentType and the protection flags are immutable attributes of this
	// exact version. SecretRecord carries the latest secret-level view for
	// listing and token-hash rotation, but must not be used to interpret or
	// authorize a historical version.
	ContentType    string
	ClientBound    bool
	HasAccessToken bool
	Ciphertext     []byte
	EncryptedDEK   []byte
	KEKID          string
	WrapMode       string // domain.WrapModeStandard | domain.WrapModeClientBound
	ClientKeySalt  []byte // HKDF salt for client-bound versions, else nil
	Algorithm      string
	Nonce          []byte
	AAD            string
	State          string
	CreatedBy      string
	CreatedAt      time.Time
	DestroyedAt    time.Time
	ExpiresAt      time.Time
	Metadata       string
}

// EncryptedPayload is what the service layer produces for a new secret
// version. The storage layer persists it verbatim. AAD is produced in the
// service layer (it binds env/app/key/version) and stored opaque here.
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

// SecretWriteExpectation is the secret state observed by the service before it
// prepares a write. Storage compares it inside the write transaction so an
// absent secret cannot silently become an update and a client-bound write
// cannot commit after its validated token has been rotated.
type SecretWriteExpectation struct {
	Exists bool
	// ID is the immutable row identity observed by the service. Comparing it
	// prevents a delete-and-recreate cycle at the same ref (ABA) from being
	// mistaken for the original secret, including unprotected secrets whose
	// access-token hashes are both nil.
	ID              int64
	AccessTokenHash []byte
}

// CreateSecretParams describes a new secret version write.
//
// Encrypt is called exactly once, inside the write transaction, with the
// version number the new version will receive (the AAD binds it). It must be
// pure computation — no I/O, no calls back into storage.
type CreateSecretParams struct {
	Ref         domain.Ref
	ContentType string
	Metadata    string
	CreatedBy   string
	ClientBound bool // required mode; must match the existing secret if present
	// AccessTokenHash, when non-nil, is stored on the secret row (sha256 of
	// the per-secret token). It may be set on creation or when minting a new
	// token for an existing secret.
	AccessTokenHash []byte
	// Expected, when non-nil, is checked atomically before any secret state is
	// changed or Encrypt is called. A mismatch returns domain.ErrAborted.
	Expected  *SecretWriteExpectation
	ExpiresAt time.Time // zero = never
	Encrypt   func(version uint64) (EncryptedPayload, error)
}

// CreateIdentityParams describes a new identity. TokenHash may be nil for a
// cert-only identity; Namespace may be nil for an unbound admin/tooling
// identity. A non-nil Namespace must already exist.
type CreateIdentityParams struct {
	Name      string
	Kind      string // domain.IdentityKindClient | domain.IdentityKindAdmin
	TokenHash []byte
	Namespace *domain.NamespaceRef
	// Cert, when non-nil, is inserted in the same transaction as the identity.
	// This makes first-certificate issuance atomic and retry-safe.
	Cert *domain.IdentityCert
}

// CAKeyRecord is one built-in CA key. Private key material is envelope-encrypted
// under a KEK; the storage layer never sees plaintext. CertPEM is public.
type CAKeyRecord struct {
	ID           string
	CertPEM      string
	EncryptedKey []byte
	EncryptedDEK []byte
	KEKID        string
	State        string // domain.KeyStateActive | domain.KeyStateRetired
	CreatedAt    time.Time
}

// IdentityCertRecord is an issued certificate joined to its owning identity —
// everything the auth interceptor needs to accept or reject a presented client
// certificate (revocation, expiry, and identity-disabled are all checked here).
type IdentityCertRecord struct {
	Cert             domain.IdentityCert
	IdentityName     string
	IdentityDisabled bool
}

// ListPage bundles pagination inputs.
type ListPage struct {
	Limit int    // implementation clamps to [1, 1000]; 0 means default (100)
	Token string // "" = first page
}

// ApplicationStore is the application-first management surface. It remains a
// separate capability so small test stores and data-plane-only integrations do
// not need to implement admin dashboard aggregation.
type ApplicationStore interface {
	EnsureApplication(ctx context.Context, name, createdBy string) (domain.Application, error)
	CreateApplication(ctx context.Context, app domain.Application) (domain.Application, error)
	CreateApplicationWithSchema(ctx context.Context, app domain.Application, schema domain.ConfigurationSchema) (domain.Application, domain.ConfigurationSchema, error)
	GetApplication(ctx context.Context, name string) (domain.Application, error)
	AdoptApplicationContract(ctx context.Context, name string, contract []domain.ApplicationContractField) (domain.Application, error)
	UpdateApplication(ctx context.Context, app domain.Application) (domain.Application, error)
	DeleteApplication(ctx context.Context, name string) error
	ArchiveApplication(ctx context.Context, name, actor string) (domain.Application, error)
	UnarchiveApplication(ctx context.Context, name string) (domain.Application, error)
	ListApplications(ctx context.Context, page ListPage, archived ApplicationArchiveFilter) ([]domain.Application, string, error)
	ListApplicationNamespaces(ctx context.Context, app string) ([]domain.Namespace, error)
}

type ApplicationArchiveFilter string

const (
	ApplicationsActiveOnly   ApplicationArchiveFilter = "exclude"
	ApplicationsIncludeAll   ApplicationArchiveFilter = "include"
	ApplicationsArchivedOnly ApplicationArchiveFilter = "only"
)

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
	// "active", marks every other key "retired", rewraps every non-destroyed
	// secret version via rewrapSecret, and rewraps every CA key (active and
	// retired) via rewrapCA, storing each returned encrypted DEK with kek_id =
	// newKM.ID. Both callbacks must be pure computation (no I/O, no storage
	// calls). Returns the number of secret versions and CA keys rewrapped. On
	// any error the database is unchanged.
	RotateKEK(ctx context.Context, newKM domain.KeyMetadata,
		rewrapSecret func(rec SecretVersionRecord) (newEncryptedDEK []byte, err error),
		rewrapCA func(rec CAKeyRecord) (newEncryptedDEK []byte, err error)) (secretsRewrapped, caRewrapped int, err error)

	// --- namespaces ------------------------------------------------------

	CreateNamespace(ctx context.Context, ns domain.Namespace) (domain.Namespace, error)
	GetNamespace(ctx context.Context, ref domain.NamespaceRef) (domain.Namespace, error)
	// UpdateNamespace replaces the description and the allowed auth-method set
	// (full replace). domain.ErrNotFound when the namespace is absent.
	UpdateNamespace(ctx context.Context, ref domain.NamespaceRef, description string, methods []domain.AuthMethod) (domain.Namespace, error)
	// DeleteNamespace removes an empty namespace. Verified in the same
	// transaction: any remaining parameter, secret, or bound identity yields
	// domain.ErrFailedPrecondition.
	DeleteNamespace(ctx context.Context, ref domain.NamespaceRef) error
	// ListNamespaces returns namespaces ordered by (env, app), each carrying its
	// parameter and secret counts.
	ListNamespaces(ctx context.Context, page ListPage) ([]domain.Namespace, string, error)

	// --- parameters ------------------------------------------------------

	// PutParameter atomically upserts the parameter in its namespace, appends an
	// immutable version, moves labels (current -> new, previous -> old current),
	// and appends a change-log entry. Returns the new version and revision.
	PutParameter(ctx context.Context, ref domain.Ref, value, contentType, metadata, createdBy string) (version, revision uint64, err error)

	// GetParameter resolves version (>0) or label (default "current").
	GetParameter(ctx context.Context, ref domain.Ref, version uint64, label string) (domain.Parameter, error)
	GetParameterInfo(ctx context.Context, ref domain.Ref) (domain.ParameterInfo, error)
	// ListParameters returns each parameter in ns matching keyPrefix at its
	// "current" label, ordered by key.
	ListParameters(ctx context.Context, ns domain.NamespaceRef, keyPrefix string, page ListPage) ([]domain.Parameter, string, error)
	// DeleteParameter removes the parameter and all versions, and appends a
	// change-log entry. Returns the revision.
	DeleteParameter(ctx context.Context, ref domain.Ref) (uint64, error)

	// --- secrets ---------------------------------------------------------

	// CreateSecretVersion atomically creates/updates the secret row, assigns
	// the next version number, invokes p.Encrypt(version), stores the
	// payload, moves labels, and appends a metadata-only change-log entry.
	// Fails with domain.ErrFailedPrecondition if p.ClientBound does not match
	// an existing secret's mode, or domain.ErrAborted if p.Expected no longer
	// matches the secret row.
	CreateSecretVersion(ctx context.Context, p CreateSecretParams) (version, revision uint64, err error)

	GetSecretRecord(ctx context.Context, ref domain.Ref) (SecretRecord, error)
	// GetSecretVersion resolves version (>0) or label (default "current") and
	// returns both the secret row and the version row.
	GetSecretVersion(ctx context.Context, ref domain.Ref, version uint64, label string) (SecretRecord, SecretVersionRecord, error)
	GetSecretInfo(ctx context.Context, ref domain.Ref) (domain.Secret, error)
	ListSecrets(ctx context.Context, ns domain.NamespaceRef, keyPrefix string, page ListPage) ([]domain.Secret, string, error)
	DeleteSecret(ctx context.Context, ref domain.Ref) (uint64, error)

	// SetSecretVersionState enables/disables version (0 = all non-destroyed
	// versions). Destroyed versions are never resurrected. Appends a
	// change-log entry (disable/enable). Returns the revision.
	SetSecretVersionState(ctx context.Context, ref domain.Ref, version uint64, state string) (uint64, error)

	// DestroySecretVersion irreversibly nulls ciphertext, encrypted DEK, and
	// nonce, sets state destroyed + destroyed_at, appends a change-log entry.
	DestroySecretVersion(ctx context.Context, ref domain.Ref, version uint64) (uint64, error)

	// PromoteSecretVersion atomically points "current" at version and
	// "previous" at the old current (if different). The target version must
	// exist and be enabled. Appends a change-log entry (promote).
	PromoteSecretVersion(ctx context.Context, ref domain.Ref, version uint64) (current, previous, revision uint64, err error)

	// UpdateSecretAccessTokenHash replaces the per-secret token hash.
	UpdateSecretAccessTokenHash(ctx context.Context, ref domain.Ref, hash []byte) error

	// --- identities ------------------------------------------------------

	CreateIdentity(ctx context.Context, params CreateIdentityParams) (domain.Identity, error)
	// GetIdentityByTokenHash returns the identity whose token hash matches. It
	// only matches identities that carry a token hash (cert-only identities are
	// never returned); certs are omitted (hot auth path).
	GetIdentityByTokenHash(ctx context.Context, tokenHash []byte) (domain.Identity, error)
	// GetIdentityByName returns the identity including its issued certificate
	// summaries and namespace binding (admin-facing view).
	GetIdentityByName(ctx context.Context, name string) (domain.Identity, error)
	ListIdentities(ctx context.Context, page ListPage) ([]domain.Identity, string, error)
	SetIdentityDisabled(ctx context.Context, name string, disabled bool) error
	// UpdateIdentityTokenHash replaces an identity's token hash (nil clears it).
	UpdateIdentityTokenHash(ctx context.Context, name string, tokenHash []byte) error

	// --- built-in CA / client certificates -------------------------------

	// InsertCAKey stores a CA key as active and retires every other CA key in
	// the same transaction (retire-on-rotate).
	InsertCAKey(ctx context.Context, ca CAKeyRecord) error
	// ActiveCAKey returns the single active CA key; domain.ErrNotFound before
	// the CA has been generated.
	ActiveCAKey(ctx context.Context) (CAKeyRecord, error)

	// InsertIdentityCert records a certificate issued to identityName (which
	// must exist).
	InsertIdentityCert(ctx context.Context, identityName string, cert domain.IdentityCert) error
	// ListIdentityCerts returns every certificate issued to identityName.
	ListIdentityCerts(ctx context.Context, identityName string) ([]domain.IdentityCert, error)
	// GetIdentityCertBySerial returns the certificate joined to its owning
	// identity's name and disabled flag. domain.ErrNotFound for unknown serials.
	GetIdentityCertBySerial(ctx context.Context, serial string) (IdentityCertRecord, error)
	// RevokeIdentityCert marks a certificate revoked (idempotent).
	RevokeIdentityCert(ctx context.Context, serial string) error

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
	// current revision and the "current" value of every parameter in the given
	// namespaces (WHERE namespace_id IN (...)). Authorization is namespace-level,
	// so the snapshot is the whole authorized namespace.
	SnapshotParameters(ctx context.Context, namespaces []domain.NamespaceRef) ([]domain.Parameter, uint64, error)
}
