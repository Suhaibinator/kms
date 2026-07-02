// Package domain holds the shared domain model for the parameter store:
// entity types, operation names, and sentinel errors. It has no dependencies
// on storage, crypto, or transport packages so every layer can import it.
package domain

import "time"

// Resource kinds.
const (
	ResourceParameter = "parameter"
	ResourceSecret    = "secret"
	ResourceNamespace = "namespace"
	ResourcePolicy    = "policy"
	ResourceIdentity  = "identity"
	ResourceKey       = "key"
)

// Version states.
const (
	StateEnabled   = "enabled"
	StateDisabled  = "disabled"
	StateDestroyed = "destroyed"
)

// Wrap modes for secret versions.
const (
	WrapModeStandard    = "standard"
	WrapModeClientBound = "client_bound"
)

// Well-known labels.
const (
	LabelCurrent  = "current"
	LabelPrevious = "previous"
)

// Identity kinds.
const (
	IdentityKindClient = "client"
	IdentityKindAdmin  = "admin"
)

// Operations the policy engine distinguishes. A policy rule may also use
// wildcards: "parameter:*", "secret:*", "admin:*", or "*".
const (
	OpParameterRead   = "parameter:read"
	OpParameterWrite  = "parameter:write"
	OpParameterList   = "parameter:list"
	OpParameterDelete = "parameter:delete"

	OpSecretRead    = "secret:read"
	OpSecretWrite   = "secret:write"
	OpSecretList    = "secret:list"
	OpSecretDisable = "secret:disable"
	OpSecretDestroy = "secret:destroy"
	OpSecretPromote = "secret:promote"

	OpAdminNamespaceCreate = "admin:namespace:create"
	OpAdminPolicyWrite     = "admin:policy:write"
	OpAdminAuditRead       = "admin:audit:read"
	OpAdminKeyRotate       = "admin:key:rotate"
)

// Change types recorded in the change log and pushed over watch streams.
const (
	ChangePut     = "put"
	ChangeDelete  = "delete"
	ChangeLabel   = "label"
	ChangePromote = "promote"
	ChangeDisable = "disable"
	ChangeEnable  = "enable"
	ChangeDestroy = "destroy"
)

// Parameter is a non-sensitive configuration value at its resolved version.
type Parameter struct {
	Path        string
	Value       string
	ContentType string
	Version     uint64
	Metadata    string // JSON object
	CreatedBy   string
	CreatedAt   time.Time
	Labels      map[string]uint64
}

// ParameterInfo is parameter-level metadata plus version history (no values).
type ParameterInfo struct {
	Path        string
	ContentType string
	Metadata    string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	Labels      map[string]uint64
	Versions    []ParameterVersionInfo
}

// ParameterVersionInfo describes one immutable parameter version.
type ParameterVersionInfo struct {
	Version     uint64
	ContentType string
	State       string
	CreatedBy   string
	CreatedAt   time.Time
	Metadata    string
}

// Secret is secret-level metadata. It never carries plaintext.
type Secret struct {
	Path           string
	ContentType    string
	ClientBound    bool
	HasAccessToken bool
	Metadata       string
	CreatedAt      time.Time
	UpdatedAt      time.Time
	Labels         map[string]uint64
	Versions       []SecretVersionInfo
}

// SecretVersionInfo describes one secret version without key material.
type SecretVersionInfo struct {
	Version     uint64
	State       string
	CreatedBy   string
	CreatedAt   time.Time
	DestroyedAt time.Time // zero if not destroyed
	ExpiresAt   time.Time // zero if no expiry
	Metadata    string
}

// SecretValue is a decrypted secret returned to an authorized caller.
type SecretValue struct {
	Path        string
	Version     uint64
	Value       []byte
	ContentType string
	Metadata    string
	CreatedAt   time.Time
}

// Namespace groups parameters and secrets.
type Namespace struct {
	Path        string
	Description string
	CreatedBy   string
	CreatedAt   time.Time
}

// PolicyRule allows or denies one operation on an exact path or a prefix
// (path ending in "/*"). Operation may use the wildcard forms listed above.
type PolicyRule struct {
	Operation string `json:"operation"`
	Path      string `json:"path"`
}

// Policy binds allow/deny rules to a subject (identity name, or "*").
type Policy struct {
	Name      string       `json:"name"`
	Subject   string       `json:"subject"`
	Allow     []PolicyRule `json:"allow"`
	Deny      []PolicyRule `json:"deny"`
	CreatedAt time.Time    `json:"-"`
	UpdatedAt time.Time    `json:"-"`
}

// Identity is an authenticated principal (machine client or admin).
type Identity struct {
	ID        int64
	Name      string
	Kind      string // IdentityKindClient | IdentityKindAdmin
	Disabled  bool
	CreatedAt time.Time
}

// AuditEvent is one immutable audit record. It must never contain secret
// plaintext or token material.
type AuditEvent struct {
	ID              int64
	EventType       string
	ActorIdentity   string
	ActorType       string
	ResourceType    string
	ResourcePath    string
	ResourceVersion uint64
	Decision        string // "allow" | "deny" | "error"
	SourceIP        string
	UserAgent       string
	RequestID       string
	CreatedAt       time.Time
	Metadata        string
}

// AuditFilter narrows audit queries.
type AuditFilter struct {
	PathPrefix    string
	ActorIdentity string
	EventType     string
	From          time.Time
	To            time.Time
}

// ChangeLogEntry is one revisioned change for watch replay.
type ChangeLogEntry struct {
	Revision     uint64
	ResourceType string // ResourceParameter | ResourceSecret
	Path         string
	ChangeType   string
	Value        string // parameter value for puts; empty for secrets
	ContentType  string
	Version      uint64
	Label        string
	CreatedAt    time.Time
}

// Subscriber describes one live watch stream in the registry.
type Subscriber struct {
	ClientName        string
	InstanceID        string
	Identity          string
	Paths             []string
	RemoteAddr        string
	ConnectedAt       time.Time
	LastHeartbeat     time.Time
	LastAckedRevision uint64
}

// KeyMetadata describes one KEK generation. Key material itself never
// appears here or anywhere in the database.
type KeyMetadata struct {
	ID        string
	Source    string // "file" | "passphrase"
	KDF       string // JSON argon2id params when Source == "passphrase"
	KDFSalt   []byte
	KeyCheck  []byte // nonce || AES-GCM ciphertext of the canary
	State     string // "active" | "retired"
	CreatedAt time.Time
}

// KeyMetadata states.
const (
	KeyStateActive  = "active"
	KeyStateRetired = "retired"
)

// KeySourceFile and KeySourcePassphrase identify how a KEK was acquired.
const (
	KeySourceFile       = "file"
	KeySourcePassphrase = "passphrase"
)
