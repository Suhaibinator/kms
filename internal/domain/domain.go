// Package domain holds the shared domain model for the parameter store:
// entity types, operation names, and sentinel errors. It has no dependencies
// on storage, crypto, or transport packages so every layer can import it.
package domain

import (
	"strings"
	"time"
)

// Resource kinds.
const (
	ResourceParameter   = "parameter"
	ResourceSecret      = "secret"
	ResourceApplication = "application"
	ResourceNamespace   = "namespace"
	ResourcePolicy      = "policy"
	ResourceIdentity    = "identity"
	ResourceKey         = "key"
)

// Version states.
const (
	StateEnabled   = "enabled"
	StateDisabled  = "disabled"
	StateDestroyed = "destroyed"
)

// Wrap modes for secret versions.
const (
	WrapModeStandard   = "standard"
	WrapModeBindingKey = "binding_key"
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

// AuthMethod identifies how a caller proves its identity. A namespace
// registers which methods admit a client into it (see Namespace).
type AuthMethod string

// Authentication methods.
const (
	AuthMethodMTLS  AuthMethod = "mtls"
	AuthMethodToken AuthMethod = "token"
)

// ValidAuthMethod reports whether m is a known authentication method.
func ValidAuthMethod(m AuthMethod) bool {
	return m == AuthMethodMTLS || m == AuthMethodToken
}

// Operations the policy engine distinguishes. A policy rule may also use
// wildcards: "parameter:*", "secret:*", "configuration-release:*",
// "admin:*", or "*".
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

	OpConfigurationReleaseCreate   = "configuration-release:create"
	OpConfigurationReleaseRead     = "configuration-release:read"
	OpConfigurationReleaseValidate = "configuration-release:validate"
	OpConfigurationReleaseActivate = "configuration-release:activate"
	OpConfigurationReleaseList     = "configuration-release:list"
	OpConfigurationReleaseWatch    = "configuration-release:watch"
	// OpConfigurationReleaseVerifyDefaults authorizes value-free comparison of
	// source-owned default hashes with the active release. It is deliberately
	// outside the implicit home-namespace grant so a CI identity can hold it
	// without any read access.
	OpConfigurationReleaseVerifyDefaults = "configuration-release:verify-defaults"

	OpAdminNamespaceCreate = "admin:namespace:create"
	OpAdminNamespaceUpdate = "admin:namespace:update"
	OpAdminNamespaceDelete = "admin:namespace:delete"
	OpAdminIdentityCert    = "admin:identity:cert"
	OpAdminPolicyWrite     = "admin:policy:write"
	OpAdminAuditRead       = "admin:audit:read"
	OpAdminKeyRotate       = "admin:key:rotate"
)

// Change types recorded in the change log and pushed over watch streams.
const (
	ChangePut                = "put"
	ChangeDelete             = "delete"
	ChangeLabel              = "label"
	ChangePromote            = "promote"
	ChangeDisable            = "disable"
	ChangeEnable             = "enable"
	ChangeDestroy            = "destroy"
	ChangeActivate           = "activate"
	ChangeBind               = "bind"
	ChangeUnbind             = "unbind"
	ChangeRotateBindingKey   = "rotate_binding_key"
	ChangePurgeBindingCohort = "purge_binding_cohort"
)

// NamespaceRef is a fixed (env, app) pair — the first-class grouping every
// parameter and secret belongs to. Empty Env or App is invalid.
type NamespaceRef struct {
	Env string
	App string
}

// String renders the display form "env/app" (logs, breadcrumbs, CLI).
func (n NamespaceRef) String() string { return n.Env + "/" + n.App }

// Ref names one parameter or secret: a namespace plus a relative key. The key
// may contain interior slashes (e.g. "billing/stripe-key") that are part of
// the name, never namespace structure.
type Ref struct {
	NS  NamespaceRef
	Key string
}

// String renders the display form "/env/app/key" (audit rendering, logs, and
// the SDK/CLI absolute-path escape hatch). The server never parses this back.
func (r Ref) String() string {
	var b strings.Builder
	b.Grow(len(r.NS.Env) + len(r.NS.App) + len(r.Key) + 3)
	b.WriteByte('/')
	b.WriteString(r.NS.Env)
	b.WriteByte('/')
	b.WriteString(r.NS.App)
	b.WriteByte('/')
	b.WriteString(r.Key)
	return b.String()
}

// Parameter is a non-sensitive configuration value at its resolved version.
type Parameter struct {
	Ref         Ref
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
	Ref         Ref
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
	Ref            Ref
	ContentType    string
	Bound          bool
	HasAccessToken bool
	Metadata       string
	CreatedAt      time.Time
	UpdatedAt      time.Time
	Labels         map[string]uint64
	Versions       []SecretVersionInfo
}

// SecretVersionInfo describes one secret version without key material.
type SecretVersionInfo struct {
	Version        uint64
	State          string
	Bound          bool
	HasAccessToken bool
	CreatedBy      string
	CreatedAt      time.Time
	DestroyedAt    time.Time // zero if not destroyed
	ExpiresAt      time.Time // zero if no expiry
	Metadata       string
}

// SecretValue is a decrypted secret returned to an authorized caller.
type SecretValue struct {
	Ref         Ref
	Version     uint64
	Value       []byte
	ContentType string
	Metadata    string
	CreatedAt   time.Time
}

// Namespace groups parameters and secrets under a fixed (env, app) pair and
// declares which authentication methods admit a client into it.
type Namespace struct {
	ID int64
	NamespaceRef
	Description        string
	AllowedAuthMethods []AuthMethod
	CreatedBy          string
	CreatedAt          time.Time
	// ParameterCount and SecretCount are populated by list queries; point reads
	// leave them zero.
	ParameterCount uint64
	SecretCount    uint64
}

// Application is the environment-independent configuration owner. Every
// namespace whose App component matches Name is one deployment environment of
// this application. Contract is the canonical release shape shared by all of
// those environments; values and resource versions remain namespace-local.
type Application struct {
	ID               int64
	Name             string
	Description      string
	ReleaseName      string
	SchemaVersion    uint64
	Contract         []ApplicationContractField
	CreatedBy        string
	CreatedAt        time.Time
	UpdatedAt        time.Time
	ArchivedAt       time.Time
	ArchivedBy       string
	EnvironmentCount uint64
}

// ApplicationContractField describes one stable alias required in every
// configuration release for an application. Parameter fields also pin their
// content type. Secret fields leave ContentType empty.
type ApplicationContractField struct {
	Alias       string `json:"alias"`
	Kind        string `json:"kind"`
	ContentType string `json:"content_type,omitempty"`
}

// PolicyRule allows or denies one operation on a whole namespace whose Env/App
// match (exact or "*"). Authorization is per-namespace: there is no key field,
// so a grant covers every key in the matched namespace. Operation may use the
// wildcard forms above.
type PolicyRule struct {
	Operation string `json:"operation"`
	Env       string `json:"env"`
	App       string `json:"app"`
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
	// Namespace is the identity's home namespace binding, or nil when unbound
	// (admin/tooling). The SDK discovers it via WhoAmI at connect time.
	Namespace *NamespaceRef
	// HasToken reports whether the identity carries a bearer-token credential.
	// A cert-only identity has none.
	HasToken bool
	// Certs is the identity's issued client certificates. It is populated on the
	// admin-facing views (GetIdentityByName, ListIdentities) and left nil on the
	// hot authentication path.
	Certs []IdentityCert
}

// IdentityCert summarizes one issued client certificate. Private keys are
// returned once at issuance and never stored, so they never appear here.
type IdentityCert struct {
	Serial      string
	Fingerprint string
	NotAfter    time.Time
	RevokedAt   time.Time // zero = valid
	CreatedAt   time.Time
}

// AuditEvent is one immutable audit record. It must never contain secret
// plaintext or token material. Resource env/app/key are denormalized text so
// history stays readable after a namespace is deleted.
type AuditEvent struct {
	ID            int64
	EventType     string
	ActorIdentity string
	ActorType     string
	ResourceType  string
	// ResourceNamespaceID is the immutable namespace incarnation captured when
	// the event is written. It is denormalized (no foreign key) so deleted
	// history remains available to admins without letting a recreated env/app
	// inherit the prior incarnation's delegated visibility.
	ResourceNamespaceID int64
	ResourceEnv         string
	ResourceApp         string
	ResourceKey         string
	ResourceVersion     uint64
	Decision            string // "allow" | "deny" | "error"
	SourceIP            string
	UserAgent           string
	RequestID           string
	CreatedAt           time.Time
	Metadata            string
}

// AuditFilter narrows audit queries. Env/App match exactly (empty = any);
// KeyPrefix is an opaque browsing filter on the resource key (LIKE 'prefix%'),
// never a security boundary.
type AuditFilter struct {
	Env           string
	App           string
	KeyPrefix     string
	ActorIdentity string
	EventType     string
	// Decision matches the recorded outcome exactly: "allow", "deny", or
	// "error". Empty matches any outcome.
	Decision string
	From     time.Time
	To       time.Time
}

// ValidAuditDecision reports whether s is an outcome AuditFilter.Decision may
// select on: "allow", "deny", "error", or "" for any. Transports validate the
// caller-supplied spelling with it so a typo narrows nothing silently.
func ValidAuditDecision(s string) bool {
	switch s {
	case "", "allow", "deny", "error":
		return true
	default:
		return false
	}
}

// ChangeLogEntry is one revisioned change for watch replay. Ref carries the
// denormalized env/app/key of the changed resource.
type ChangeLogEntry struct {
	Revision     uint64
	ResourceType string // ResourceParameter | ResourceSecret
	// NamespaceID is the immutable namespace incarnation that produced this
	// event. Watch replay/live matching must use it in addition to Ref so a
	// deleted namespace's history cannot flow into a recreated name.
	NamespaceID int64
	Ref         Ref
	ChangeType  string
	Value       string // parameter value for puts; empty for secrets
	ContentType string
	Version     uint64
	// AffectedVersions records the exact sorted version set for one atomic
	// multi-version secret mutation. It is empty for ordinary changes.
	AffectedVersions []uint64
	Label            string
	CreatedAt        time.Time
}

// Subscriber describes one live watch stream in the registry. Namespaces are
// the (env, app) namespaces the stream is subscribed to; a subscriber receives
// every change in each of them.
type Subscriber struct {
	ClientName        string
	InstanceID        string
	Identity          string
	Namespaces        []NamespaceRef
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
