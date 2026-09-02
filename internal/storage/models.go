package storage

import (
	"encoding/json/v2"
	"time"

	"github.com/Suhaibinator/kms/internal/domain"
)

// This file defines the GORM models. They map one-to-one onto the tables in
// migrations/0001_initial.sql. Timestamps are stored as fixed-width RFC3339
// UTC text (see fmtTime) so lexicographic ordering matches chronological
// ordering in SQL. Nullable time/blob columns use pointer/[]byte fields so a
// zero value round-trips to SQL NULL.

// keyMetadataModel -> key_metadata.
type keyMetadataModel struct {
	ID        string `gorm:"column:id;primaryKey"`
	Source    string `gorm:"column:source;not null"`
	KDF       string `gorm:"column:kdf;not null;default:''"`
	KDFSalt   []byte `gorm:"column:kdf_salt"`
	KeyCheck  []byte `gorm:"column:key_check;not null"`
	State     string `gorm:"column:state;not null;default:active"`
	CreatedAt string `gorm:"column:created_at;not null"`
}

func (keyMetadataModel) TableName() string { return "key_metadata" }

// applicationModel -> applications. ContractJSON is a canonical JSON array of
// domain.ApplicationContractField values.
type applicationModel struct {
	ID            int64  `gorm:"column:id;primaryKey;autoIncrement"`
	Name          string `gorm:"column:name;not null;uniqueIndex"`
	Description   string `gorm:"column:description;not null;default:''"`
	ReleaseName   string `gorm:"column:release_name;not null;default:runtime"`
	SchemaID      string `gorm:"column:schema_id;not null;default:''"`
	SchemaVersion int64  `gorm:"column:schema_version;not null;default:0"`
	ContractJSON  string `gorm:"column:contract_json;not null;default:'[]'"`
	CreatedBy     string `gorm:"column:created_by;not null;default:''"`
	CreatedAt     string `gorm:"column:created_at;not null"`
	UpdatedAt     string `gorm:"column:updated_at;not null"`
}

func (applicationModel) TableName() string { return "applications" }

// namespaceModel -> namespaces. AllowedAuthMethods is a JSON array of strings.
type namespaceModel struct {
	ID                 int64            `gorm:"column:id;primaryKey;autoIncrement"`
	Env                string           `gorm:"column:env;not null;uniqueIndex:idx_ns_env_app,priority:1"`
	App                string           `gorm:"column:app;not null;uniqueIndex:idx_ns_env_app,priority:2"`
	Application        applicationModel `gorm:"foreignKey:App;references:Name"`
	Description        string           `gorm:"column:description;not null;default:''"`
	AllowedAuthMethods string           `gorm:"column:allowed_auth_methods;not null;default:[\"mtls\"]"`
	CreatedBy          string           `gorm:"column:created_by;not null;default:''"`
	CreatedAt          string           `gorm:"column:created_at;not null"`
}

func (namespaceModel) TableName() string { return "namespaces" }

// parameterModel -> parameters.
type parameterModel struct {
	ID           int64          `gorm:"column:id;primaryKey;autoIncrement"`
	NamespaceID  int64          `gorm:"column:namespace_id;not null;uniqueIndex:idx_param_ns_name,priority:1"`
	Namespace    namespaceModel `gorm:"foreignKey:NamespaceID;references:ID"`
	Name         string         `gorm:"column:name;not null;uniqueIndex:idx_param_ns_name,priority:2"`
	ContentType  string         `gorm:"column:content_type;not null;default:string"`
	MetadataJSON string         `gorm:"column:metadata_json;not null;default:{}"`
	CreatedAt    string         `gorm:"column:created_at;not null"`
	UpdatedAt    string         `gorm:"column:updated_at;not null"`
}

func (parameterModel) TableName() string { return "parameters" }

// parameterVersionModel -> parameter_versions.
type parameterVersionModel struct {
	ID            int64          `gorm:"column:id;primaryKey;autoIncrement"`
	ParameterID   int64          `gorm:"column:parameter_id;not null;uniqueIndex:idx_param_ver,priority:1"`
	Parameter     parameterModel `gorm:"foreignKey:ParameterID;references:ID;constraint:OnDelete:CASCADE"`
	VersionNumber int64          `gorm:"column:version_number;not null;uniqueIndex:idx_param_ver,priority:2"`
	Value         string         `gorm:"column:value;not null"`
	ContentType   string         `gorm:"column:content_type;not null;default:string"`
	State         string         `gorm:"column:state;not null;default:enabled"`
	CreatedBy     string         `gorm:"column:created_by;not null;default:''"`
	CreatedAt     string         `gorm:"column:created_at;not null"`
	MetadataJSON  string         `gorm:"column:metadata_json;not null;default:{}"`
}

func (parameterVersionModel) TableName() string { return "parameter_versions" }

// parameterLabelModel -> parameter_labels.
type parameterLabelModel struct {
	ParameterID   int64          `gorm:"column:parameter_id;not null;primaryKey;autoIncrement:false"`
	Parameter     parameterModel `gorm:"foreignKey:ParameterID;references:ID;constraint:OnDelete:CASCADE"`
	Label         string         `gorm:"column:label;not null;primaryKey"`
	VersionNumber int64          `gorm:"column:version_number;not null"`
}

func (parameterLabelModel) TableName() string { return "parameter_labels" }

// secretModel -> secrets.
type secretModel struct {
	ID              int64          `gorm:"column:id;primaryKey;autoIncrement"`
	NamespaceID     int64          `gorm:"column:namespace_id;not null;uniqueIndex:idx_secret_ns_name,priority:1"`
	Namespace       namespaceModel `gorm:"foreignKey:NamespaceID;references:ID"`
	Name            string         `gorm:"column:name;not null;uniqueIndex:idx_secret_ns_name,priority:2"`
	ClientBound     int64          `gorm:"column:client_bound;not null;default:0"`
	AccessTokenHash []byte         `gorm:"column:access_token_hash"`
	ContentType     string         `gorm:"column:content_type;not null;default:application/octet-stream"`
	MetadataJSON    string         `gorm:"column:metadata_json;not null;default:{}"`
	CreatedAt       string         `gorm:"column:created_at;not null"`
	UpdatedAt       string         `gorm:"column:updated_at;not null"`
}

func (secretModel) TableName() string { return "secrets" }

// secretVersionModel -> secret_versions.
type secretVersionModel struct {
	ID             int64       `gorm:"column:id;primaryKey;autoIncrement"`
	SecretID       int64       `gorm:"column:secret_id;not null;uniqueIndex:idx_secret_ver,priority:1"`
	Secret         secretModel `gorm:"foreignKey:SecretID;references:ID;constraint:OnDelete:CASCADE"`
	VersionNumber  int64       `gorm:"column:version_number;not null;uniqueIndex:idx_secret_ver,priority:2"`
	ContentType    string      `gorm:"column:content_type;not null;default:application/octet-stream"`
	ClientBound    int64       `gorm:"column:client_bound;not null;default:0"`
	HasAccessToken int64       `gorm:"column:has_access_token;not null;default:0"`
	Ciphertext     []byte      `gorm:"column:ciphertext"`
	EncryptedDEK   []byte      `gorm:"column:encrypted_dek"`
	KEKID          string      `gorm:"column:kek_id;not null"`
	WrapMode       string      `gorm:"column:wrap_mode;not null;default:standard"`
	ClientKeySalt  []byte      `gorm:"column:client_key_salt"`
	Algorithm      string      `gorm:"column:algorithm;not null;default:AES-256-GCM"`
	Nonce          []byte      `gorm:"column:nonce"`
	AAD            string      `gorm:"column:aad;not null"`
	State          string      `gorm:"column:state;not null;default:enabled"`
	CreatedBy      string      `gorm:"column:created_by;not null;default:''"`
	CreatedAt      string      `gorm:"column:created_at;not null"`
	DestroyedAt    *string     `gorm:"column:destroyed_at"`
	ExpiresAt      *string     `gorm:"column:expires_at"`
	MetadataJSON   string      `gorm:"column:metadata_json;not null;default:{}"`
}

func (secretVersionModel) TableName() string { return "secret_versions" }

// secretLabelModel -> secret_labels.
type secretLabelModel struct {
	SecretID      int64       `gorm:"column:secret_id;not null;primaryKey;autoIncrement:false"`
	Secret        secretModel `gorm:"foreignKey:SecretID;references:ID;constraint:OnDelete:CASCADE"`
	Label         string      `gorm:"column:label;not null;primaryKey"`
	VersionNumber int64       `gorm:"column:version_number;not null"`
}

func (secretLabelModel) TableName() string { return "secret_labels" }

// identityModel -> identities. TokenHash is nullable (cert-only identities have
// none); NamespaceID is nullable (unbound admin/tooling identities).
type identityModel struct {
	ID           int64           `gorm:"column:id;primaryKey;autoIncrement"`
	Name         string          `gorm:"column:name;not null;unique"`
	Kind         string          `gorm:"column:kind;not null"`
	TokenHash    []byte          `gorm:"column:token_hash;index:idx_identities_token_hash"`
	NamespaceID  *int64          `gorm:"column:namespace_id"`
	Namespace    *namespaceModel `gorm:"foreignKey:NamespaceID;references:ID"`
	Disabled     int64           `gorm:"column:disabled;not null;default:0"`
	CreatedAt    string          `gorm:"column:created_at;not null"`
	MetadataJSON string          `gorm:"column:metadata_json;not null;default:{}"`
}

func (identityModel) TableName() string { return "identities" }

// caKeyModel -> ca_keys. Private key material is envelope-encrypted under the
// active KEK; the public cert is served unauthenticated.
type caKeyModel struct {
	ID           string `gorm:"column:id;primaryKey"`
	CertPEM      string `gorm:"column:cert_pem;not null"`
	EncryptedKey []byte `gorm:"column:encrypted_key;not null"`
	EncryptedDEK []byte `gorm:"column:encrypted_dek;not null"`
	KEKID        string `gorm:"column:kek_id;not null"`
	State        string `gorm:"column:state;not null;default:active"`
	CreatedAt    string `gorm:"column:created_at;not null"`
}

func (caKeyModel) TableName() string { return "ca_keys" }

// identityCertModel -> identity_certs. Never holds private keys.
type identityCertModel struct {
	Serial      string        `gorm:"column:serial;primaryKey"`
	IdentityID  int64         `gorm:"column:identity_id;not null;index:idx_identity_certs_identity"`
	Identity    identityModel `gorm:"foreignKey:IdentityID;references:ID;constraint:OnDelete:CASCADE"`
	Fingerprint string        `gorm:"column:fingerprint;not null"`
	NotAfter    string        `gorm:"column:not_after;not null"`
	RevokedAt   *string       `gorm:"column:revoked_at"`
	CreatedAt   string        `gorm:"column:created_at;not null"`
}

func (identityCertModel) TableName() string { return "identity_certs" }

// policyModel -> policies.
type policyModel struct {
	ID        int64  `gorm:"column:id;primaryKey;autoIncrement"`
	Name      string `gorm:"column:name;not null;unique"`
	Subject   string `gorm:"column:subject;not null;index:idx_policies_subject"`
	RulesJSON string `gorm:"column:rules_json;not null"`
	CreatedAt string `gorm:"column:created_at;not null"`
	UpdatedAt string `gorm:"column:updated_at;not null"`
}

func (policyModel) TableName() string { return "policies" }

// auditEventModel -> audit_events. Resource env/app/key are denormalized text.
type auditEventModel struct {
	// id carries the decision index's second column: audit listing pages by
	// descending id, so the index has to order rows within one decision.
	ID                  int64  `gorm:"column:id;primaryKey;autoIncrement;index:idx_audit_decision,priority:2"`
	EventType           string `gorm:"column:event_type;not null"`
	ActorIdentity       string `gorm:"column:actor_identity;not null;default:'';index:idx_audit_actor"`
	ActorType           string `gorm:"column:actor_type;not null;default:''"`
	ResourceType        string `gorm:"column:resource_type;not null;default:''"`
	ResourceNamespaceID int64  `gorm:"column:resource_namespace_id;not null;default:0;index:idx_audit_namespace_id"`
	ResourceEnv         string `gorm:"column:resource_env;not null;default:'';index:idx_audit_ns,priority:1"`
	ResourceApp         string `gorm:"column:resource_app;not null;default:'';index:idx_audit_ns,priority:2"`
	ResourceKey         string `gorm:"column:resource_key;not null;default:''"`
	ResourceVersion     int64  `gorm:"column:resource_version;not null;default:0"`
	Decision            string `gorm:"column:decision;not null;default:'';index:idx_audit_decision,priority:1"`
	SourceIP            string `gorm:"column:source_ip;not null;default:''"`
	UserAgent           string `gorm:"column:user_agent;not null;default:''"`
	RequestID           string `gorm:"column:request_id;not null;default:''"`
	CreatedAt           string `gorm:"column:created_at;not null;index:idx_audit_created"`
	MetadataJSON        string `gorm:"column:metadata_json;not null;default:{}"`
}

func (auditEventModel) TableName() string { return "audit_events" }

// changeLogModel -> change_log. The table itself is created via raw DDL (see
// changeLogDDL) to guarantee INTEGER PRIMARY KEY AUTOINCREMENT; this struct is
// used only for queries.
type changeLogModel struct {
	Revision      int64   `gorm:"column:revision;primaryKey;autoIncrement"`
	ResourceType  string  `gorm:"column:resource_type;not null"`
	NamespaceID   int64   `gorm:"column:namespace_id;not null;default:0;index:idx_change_log_namespace_revision,priority:1"`
	Env           string  `gorm:"column:env;not null"`
	App           string  `gorm:"column:app;not null"`
	Key           string  `gorm:"column:key;not null"`
	ChangeType    string  `gorm:"column:change_type;not null"`
	Value         *string `gorm:"column:value"`
	ContentType   string  `gorm:"column:content_type;not null;default:''"`
	VersionNumber int64   `gorm:"column:version_number;not null;default:0"`
	Label         string  `gorm:"column:label;not null;default:''"`
	CreatedAt     string  `gorm:"column:created_at;not null"`
}

func (changeLogModel) TableName() string { return "change_log" }

type configurationReleaseModel struct {
	ID            int64          `gorm:"column:id;primaryKey;autoIncrement"`
	NamespaceID   int64          `gorm:"column:namespace_id;not null;uniqueIndex:idx_release_ns_name_ver,priority:1"`
	Namespace     namespaceModel `gorm:"foreignKey:NamespaceID;references:ID"`
	Name          string         `gorm:"column:name;not null;uniqueIndex:idx_release_ns_name_ver,priority:2"`
	VersionNumber int64          `gorm:"column:version_number;not null;uniqueIndex:idx_release_ns_name_ver,priority:3"`
	SchemaID      string         `gorm:"column:schema_id;not null;default:''"`
	SchemaVersion int64          `gorm:"column:schema_version;not null;default:0"`
	Digest        string         `gorm:"column:digest;not null"`
	MetadataJSON  string         `gorm:"column:metadata_json;not null;default:{}"`
	CreatedBy     string         `gorm:"column:created_by;not null;default:''"`
	CreatedAt     string         `gorm:"column:created_at;not null"`
}

func (configurationReleaseModel) TableName() string { return "configuration_releases" }

type configurationReleaseEntryModel struct {
	ID        int64                     `gorm:"column:id;primaryKey;autoIncrement"`
	ReleaseID int64                     `gorm:"column:release_id;not null;uniqueIndex:idx_release_entry_alias,priority:1;index:idx_release_entry_ref,priority:1"`
	Release   configurationReleaseModel `gorm:"foreignKey:ReleaseID;references:ID;constraint:OnDelete:CASCADE"`
	Alias     string                    `gorm:"column:alias;not null;uniqueIndex:idx_release_entry_alias,priority:2"`
	Kind      string                    `gorm:"column:kind;not null;index:idx_release_entry_ref,priority:2;index:idx_release_entry_resource,priority:2"`
	// ResourceNamespaceID is deliberately denormalized without a foreign key:
	// immutable/inactive release history may outlive deletion of its source
	// namespace, but must never resolve a recreated env/app name instead.
	ResourceNamespaceID int64  `gorm:"column:resource_namespace_id;not null;default:0;index:idx_release_entry_resource,priority:1"`
	ResourceEnv         string `gorm:"column:resource_env;not null;index:idx_release_entry_ref,priority:3"`
	ResourceApp         string `gorm:"column:resource_app;not null;index:idx_release_entry_ref,priority:4"`
	ResourceKey         string `gorm:"column:resource_key;not null;index:idx_release_entry_ref,priority:5;index:idx_release_entry_resource,priority:3"`
	ResourceVersion     int64  `gorm:"column:resource_version;not null;index:idx_release_entry_ref,priority:6;index:idx_release_entry_resource,priority:4"`
	ContentType         string `gorm:"column:content_type;not null;default:''"`
	MetadataJSON        string `gorm:"column:metadata_json;not null;default:{}"`
	ParameterDigest     string `gorm:"column:parameter_digest;not null;default:''"`
	ClientBound         int64  `gorm:"column:client_bound;not null;default:0"`
	HasAccessToken      int64  `gorm:"column:has_access_token;not null;default:0"`
}

func (configurationReleaseEntryModel) TableName() string { return "configuration_release_entries" }

type configurationReleaseLabelModel struct {
	NamespaceID        int64          `gorm:"column:namespace_id;not null;primaryKey;autoIncrement:false"`
	Namespace          namespaceModel `gorm:"foreignKey:NamespaceID;references:ID"`
	ReleaseName        string         `gorm:"column:release_name;not null;primaryKey"`
	Label              string         `gorm:"column:label;not null;primaryKey"`
	VersionNumber      int64          `gorm:"column:version_number;not null"`
	ActivationRevision int64          `gorm:"column:activation_revision;not null;default:0"`
}

func (configurationReleaseLabelModel) TableName() string { return "configuration_release_labels" }

// configurationReleaseActivationModel preserves the authoritative identity of
// an activation after its replay row ages out of the global changelog. This
// lets idempotently retried lifecycle acknowledgements remain verifiable for
// the longer release-retention window without keeping ordinary watch history
// forever.
type configurationReleaseActivationModel struct {
	Revision      int64          `gorm:"column:revision;primaryKey;autoIncrement:false"`
	NamespaceID   int64          `gorm:"column:namespace_id;not null;index:idx_release_activation_lookup,priority:1"`
	Namespace     namespaceModel `gorm:"foreignKey:NamespaceID;references:ID"`
	ReleaseName   string         `gorm:"column:release_name;not null;index:idx_release_activation_lookup,priority:2"`
	VersionNumber int64          `gorm:"column:version_number;not null;index:idx_release_activation_lookup,priority:3"`
	ActivatedAt   string         `gorm:"column:activated_at;not null;index:idx_release_activation_time"`
}

func (configurationReleaseActivationModel) TableName() string {
	return "configuration_release_activations"
}

type configurationSchemaModel struct {
	ID            string `gorm:"column:id;not null;primaryKey"`
	VersionNumber int64  `gorm:"column:version_number;not null;primaryKey;autoIncrement:false"`
	SchemaJSON    string `gorm:"column:schema_json;not null"`
	Digest        string `gorm:"column:digest;not null"`
	MetadataJSON  string `gorm:"column:metadata_json;not null;default:{}"`
	CreatedBy     string `gorm:"column:created_by;not null;default:''"`
	CreatedAt     string `gorm:"column:created_at;not null"`
}

func (configurationSchemaModel) TableName() string { return "configuration_schemas" }

type releaseSubscriberStateModel struct {
	NamespaceID        int64          `gorm:"column:namespace_id;not null;primaryKey;autoIncrement:false;index:idx_release_subscriber_page,priority:1"`
	Namespace          namespaceModel `gorm:"foreignKey:NamespaceID;references:ID"`
	ReleaseName        string         `gorm:"column:release_name;not null;primaryKey;index:idx_release_subscriber_page,priority:2"`
	ClientName         string         `gorm:"column:client_name;not null;primaryKey"`
	InstanceID         string         `gorm:"column:instance_id;not null;primaryKey"`
	Identity           string         `gorm:"column:identity;not null;default:'';primaryKey"`
	State              string         `gorm:"column:state;not null;primaryKey"`
	ReleaseVersion     int64          `gorm:"column:release_version;not null"`
	ActivationRevision int64          `gorm:"column:activation_revision;not null"`
	RejectionCategory  string         `gorm:"column:rejection_category;not null;default:''"`
	Diagnostic         string         `gorm:"column:diagnostic;not null;default:''"`
	ClientTimestamp    string         `gorm:"column:client_timestamp;not null"`
	ServerTimestamp    string         `gorm:"column:server_timestamp;not null;index:idx_release_subscriber_server_time;index:idx_release_subscriber_page,priority:3"`
	Connected          int64          `gorm:"column:connected;not null;default:0"`
	DisconnectedAt     *string        `gorm:"column:disconnected_at;index:idx_release_subscriber_disconnected"`
	// AppliedDivergent / DivergentFieldCount (schema v7) record that an applied
	// generation differs from the application's source-owned defaults. They are
	// added by an explicit ALTER TABLE in migrate because this table is never
	// AutoMigrate'd once it exists.
	AppliedDivergent    int64 `gorm:"column:applied_divergent;not null;default:0"`
	DivergentFieldCount int64 `gorm:"column:divergent_field_count;not null;default:0"`
}

func (releaseSubscriberStateModel) TableName() string { return "release_subscriber_states" }

type releaseSubscriberConnectionModel struct {
	NamespaceID     int64          `gorm:"column:namespace_id;not null;primaryKey;autoIncrement:false;index:idx_release_connection_page,priority:1"`
	Namespace       namespaceModel `gorm:"foreignKey:NamespaceID;references:ID"`
	ReleaseName     string         `gorm:"column:release_name;not null;primaryKey;index:idx_release_connection_page,priority:2"`
	ClientName      string         `gorm:"column:client_name;not null;primaryKey"`
	InstanceID      string         `gorm:"column:instance_id;not null;primaryKey"`
	Identity        string         `gorm:"column:identity;not null;default:'';primaryKey"`
	ConnectionID    string         `gorm:"column:connection_id;not null;default:''"`
	Connected       int64          `gorm:"column:connected;not null;default:0"`
	ConnectedAt     string         `gorm:"column:connected_at;not null"`
	DisconnectedAt  *string        `gorm:"column:disconnected_at;index:idx_release_connection_disconnected"`
	ServerTimestamp string         `gorm:"column:server_timestamp;not null;index:idx_release_connection_server_time;index:idx_release_connection_page,priority:3"`
}

func (releaseSubscriberConnectionModel) TableName() string {
	return "release_subscriber_connections"
}

// schemaMigrationModel -> schema_migrations. Tracks the applied schema version.
type schemaMigrationModel struct {
	Version   int    `gorm:"column:version;primaryKey;autoIncrement:false"`
	AppliedAt string `gorm:"column:applied_at;not null"`
}

func (schemaMigrationModel) TableName() string { return "schema_migrations" }

// autoMigrateModels are migrated by GORM. change_log is intentionally absent:
// it is created by raw DDL to guarantee AUTOINCREMENT.
var autoMigrateModels = []any{
	&keyMetadataModel{},
	&applicationModel{},
	&namespaceModel{},
	&parameterModel{},
	&parameterVersionModel{},
	&parameterLabelModel{},
	&secretModel{},
	&secretVersionModel{},
	&secretLabelModel{},
	&identityModel{},
	&caKeyModel{},
	&identityCertModel{},
	&policyModel{},
	&auditEventModel{},
	&configurationReleaseModel{},
	&configurationReleaseEntryModel{},
	&configurationReleaseLabelModel{},
	&configurationReleaseActivationModel{},
	&configurationSchemaModel{},
	&releaseSubscriberStateModel{},
	&releaseSubscriberConnectionModel{},
}

// ---- model <-> domain conversions ----------------------------------------

func toKeyMetadata(m keyMetadataModel) domain.KeyMetadata {
	return domain.KeyMetadata{
		ID:        m.ID,
		Source:    m.Source,
		KDF:       m.KDF,
		KDFSalt:   m.KDFSalt,
		KeyCheck:  m.KeyCheck,
		State:     m.State,
		CreatedAt: parseTime(m.CreatedAt),
	}
}

// toIdentity maps the base identity row. Namespace and Certs are filled by the
// caller (they require additional lookups and are omitted on the hot auth path).
func toIdentity(m identityModel) domain.Identity {
	return domain.Identity{
		ID:        m.ID,
		Name:      m.Name,
		Kind:      m.Kind,
		Disabled:  i2b(m.Disabled),
		CreatedAt: parseTime(m.CreatedAt),
		HasToken:  len(m.TokenHash) > 0,
	}
}

func toNamespace(m namespaceModel) domain.Namespace {
	return domain.Namespace{
		ID:  m.ID,
		Env: m.Env, App: m.App,
		Description:        m.Description,
		AllowedAuthMethods: parseAuthMethods(m.AllowedAuthMethods),
		CreatedBy:          m.CreatedBy,
		CreatedAt:          parseTime(m.CreatedAt),
	}
}

func toApplication(m applicationModel) domain.Application {
	var contract []domain.ApplicationContractField
	if err := json.Unmarshal([]byte(m.ContractJSON), &contract); err != nil {
		contract = nil
	}
	return domain.Application{
		ID:            m.ID,
		Name:          m.Name,
		Description:   m.Description,
		ReleaseName:   m.ReleaseName,
		SchemaID:      m.SchemaID,
		SchemaVersion: uint64(m.SchemaVersion),
		Contract:      contract,
		CreatedBy:     m.CreatedBy,
		CreatedAt:     parseTime(m.CreatedAt),
		UpdatedAt:     parseTime(m.UpdatedAt),
	}
}

func toIdentityCert(m identityCertModel) domain.IdentityCert {
	return domain.IdentityCert{
		Serial:      m.Serial,
		Fingerprint: m.Fingerprint,
		NotAfter:    parseTime(m.NotAfter),
		RevokedAt:   parseTimePtr(m.RevokedAt),
		CreatedAt:   parseTime(m.CreatedAt),
	}
}

func toCAKeyRecord(m caKeyModel) CAKeyRecord {
	return CAKeyRecord{
		ID:           m.ID,
		CertPEM:      m.CertPEM,
		EncryptedKey: m.EncryptedKey,
		EncryptedDEK: m.EncryptedDEK,
		KEKID:        m.KEKID,
		State:        m.State,
		CreatedAt:    parseTime(m.CreatedAt),
	}
}

func toSecretRecord(sec secretModel, ref domain.Ref, labels map[string]uint64) SecretRecord {
	return SecretRecord{
		ID:              sec.ID,
		Ref:             ref,
		ClientBound:     i2b(sec.ClientBound),
		AccessTokenHash: sec.AccessTokenHash,
		ContentType:     sec.ContentType,
		Metadata:        sec.MetadataJSON,
		CreatedAt:       parseTime(sec.CreatedAt),
		UpdatedAt:       parseTime(sec.UpdatedAt),
		Labels:          labels,
	}
}

func toSecretVersionRecord(v secretVersionModel) SecretVersionRecord {
	return SecretVersionRecord{
		ID:             v.ID,
		SecretID:       v.SecretID,
		Version:        uint64(v.VersionNumber),
		ContentType:    v.ContentType,
		ClientBound:    i2b(v.ClientBound),
		HasAccessToken: i2b(v.HasAccessToken),
		Ciphertext:     v.Ciphertext,
		EncryptedDEK:   v.EncryptedDEK,
		KEKID:          v.KEKID,
		WrapMode:       v.WrapMode,
		ClientKeySalt:  v.ClientKeySalt,
		Algorithm:      v.Algorithm,
		Nonce:          v.Nonce,
		AAD:            v.AAD,
		State:          v.State,
		CreatedBy:      v.CreatedBy,
		CreatedAt:      parseTime(v.CreatedAt),
		DestroyedAt:    parseTimePtr(v.DestroyedAt),
		ExpiresAt:      parseTimePtr(v.ExpiresAt),
		Metadata:       v.MetadataJSON,
	}
}

func toChangeEntry(m changeLogModel) domain.ChangeLogEntry {
	value := ""
	if m.Value != nil {
		value = *m.Value
	}
	return domain.ChangeLogEntry{
		Revision:     uint64(m.Revision),
		ResourceType: m.ResourceType,
		NamespaceID:  m.NamespaceID,
		Ref:          domain.Ref{NS: domain.NamespaceRef{Env: m.Env, App: m.App}, Key: m.Key},
		ChangeType:   m.ChangeType,
		Value:        value,
		ContentType:  m.ContentType,
		Version:      uint64(m.VersionNumber),
		Label:        m.Label,
		CreatedAt:    parseTime(m.CreatedAt),
	}
}

func toAuditEvent(m auditEventModel) domain.AuditEvent {
	return domain.AuditEvent{
		ID:                  m.ID,
		EventType:           m.EventType,
		ActorIdentity:       m.ActorIdentity,
		ActorType:           m.ActorType,
		ResourceType:        m.ResourceType,
		ResourceNamespaceID: m.ResourceNamespaceID,
		ResourceEnv:         m.ResourceEnv,
		ResourceApp:         m.ResourceApp,
		ResourceKey:         m.ResourceKey,
		ResourceVersion:     uint64(m.ResourceVersion),
		Decision:            m.Decision,
		SourceIP:            m.SourceIP,
		UserAgent:           m.UserAgent,
		RequestID:           m.RequestID,
		CreatedAt:           parseTime(m.CreatedAt),
		Metadata:            m.MetadataJSON,
	}
}

// marshalAuthMethods renders a set of auth methods as the stored JSON array.
// An empty set defaults to ["mtls"] — the strongest posture.
func marshalAuthMethods(methods []domain.AuthMethod) string {
	if len(methods) == 0 {
		return `["mtls"]`
	}
	b, err := json.Marshal(methods)
	if err != nil {
		// AuthMethod is a plain string; marshalling cannot fail in practice.
		return `["mtls"]`
	}
	return string(b)
}

// parseAuthMethods decodes the stored JSON array. Unparseable or empty text
// yields nil so callers see an explicit empty set rather than a bogus method.
func parseAuthMethods(s string) []domain.AuthMethod {
	if s == "" {
		return nil
	}
	var methods []domain.AuthMethod
	if err := json.Unmarshal([]byte(s), &methods); err != nil {
		return nil
	}
	return methods
}

// zeroOr returns v if non-empty, else def.
func zeroOr(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func b2i(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

func i2b(i int64) bool { return i != 0 }

// nowUTC returns the current time truncated to what the DB representation can
// hold, so returned domain values match subsequently-read ones exactly.
func nowUTC() time.Time { return parseTime(fmtTime(time.Now())) }
