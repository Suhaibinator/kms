package httpserver

import (
	"time"

	"github.com/Suhaibinator/kms/internal/domain"
)

// The DTO types below define the exact JSON field names in docs/http-api.md and
// mirror frontend/lib/types.ts. Resources are addressed by a flattened
// namespace (env, app) plus a relative key; there is no `path` string on the
// wire. Timestamps are Unix milliseconds (*_unix_ms). They are separate from the
// domain types because the wire format omits internal fields.

func unixMS(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

func rawJSON(s string) string {
	if s == "" {
		return "{}"
	}
	return s
}

// nonNilLabels guarantees the JSON renders an object rather than null.
func nonNilLabels(m map[string]uint64) map[string]uint64 {
	if m == nil {
		return map[string]uint64{}
	}
	return m
}

// --- namespace reference ---------------------------------------------------

// namespaceRefDTO is the {"env","app"} object nested in identities, whoami, and
// create-identity requests. It is a pointer field so it can serialize as null
// for unbound identities.
type namespaceRefDTO struct {
	Env string `json:"env"`
	App string `json:"app"`
}

func toNamespaceRefDTO(ref *domain.NamespaceRef) *namespaceRefDTO {
	if ref == nil {
		return nil
	}
	return &namespaceRefDTO{Env: ref.Env, App: ref.App}
}

func (d *namespaceRefDTO) toDomain() *domain.NamespaceRef {
	if d == nil {
		return nil
	}
	return &domain.NamespaceRef{Env: d.Env, App: d.App}
}

// --- namespaces ------------------------------------------------------------

type namespaceDTO struct {
	Env                string   `json:"env"`
	App                string   `json:"app"`
	Description        string   `json:"description"`
	AllowedAuthMethods []string `json:"allowed_auth_methods"`
	CreatedBy          string   `json:"created_by"`
	CreatedAtUnixMS    int64    `json:"created_at_unix_ms"`
	ParameterCount     uint64   `json:"parameter_count"`
	SecretCount        uint64   `json:"secret_count"`
}

func toNamespaceDTO(n domain.Namespace) namespaceDTO {
	return namespaceDTO{
		Env:                n.Env,
		App:                n.App,
		Description:        n.Description,
		AllowedAuthMethods: authMethodsToStrings(n.AllowedAuthMethods),
		CreatedBy:          n.CreatedBy,
		CreatedAtUnixMS:    unixMS(n.CreatedAt),
		ParameterCount:     n.ParameterCount,
		SecretCount:        n.SecretCount,
	}
}

// authMethodsToStrings renders the allowed-auth-method set as a non-nil JSON
// array of strings.
func authMethodsToStrings(methods []domain.AuthMethod) []string {
	out := make([]string, 0, len(methods))
	for _, m := range methods {
		out = append(out, string(m))
	}
	return out
}

// authMethodsFromStrings casts the wire strings to domain.AuthMethod without
// validating them; core rejects unknown methods.
func authMethodsFromStrings(methods []string) []domain.AuthMethod {
	if methods == nil {
		return nil
	}
	out := make([]domain.AuthMethod, 0, len(methods))
	for _, m := range methods {
		out = append(out, domain.AuthMethod(m))
	}
	return out
}

// --- parameters ------------------------------------------------------------

type parameterDTO struct {
	Env             string            `json:"env"`
	App             string            `json:"app"`
	Key             string            `json:"key"`
	Value           string            `json:"value"`
	ContentType     string            `json:"content_type"`
	Version         uint64            `json:"version"`
	MetadataJSON    string            `json:"metadata_json"`
	CreatedBy       string            `json:"created_by"`
	CreatedAtUnixMS int64             `json:"created_at_unix_ms"`
	Labels          map[string]uint64 `json:"labels"`
}

func toParameterDTO(p domain.Parameter) parameterDTO {
	return parameterDTO{
		Env:             p.Ref.NS.Env,
		App:             p.Ref.NS.App,
		Key:             p.Ref.Key,
		Value:           p.Value,
		ContentType:     p.ContentType,
		Version:         p.Version,
		MetadataJSON:    rawJSON(p.Metadata),
		CreatedBy:       p.CreatedBy,
		CreatedAtUnixMS: unixMS(p.CreatedAt),
		Labels:          nonNilLabels(p.Labels),
	}
}

type parameterVersionDTO struct {
	Version         uint64 `json:"version"`
	ContentType     string `json:"content_type"`
	State           string `json:"state"`
	CreatedBy       string `json:"created_by"`
	CreatedAtUnixMS int64  `json:"created_at_unix_ms"`
	MetadataJSON    string `json:"metadata_json"`
}

type parameterInfoDTO struct {
	Env             string                `json:"env"`
	App             string                `json:"app"`
	Key             string                `json:"key"`
	ContentType     string                `json:"content_type"`
	MetadataJSON    string                `json:"metadata_json"`
	CreatedAtUnixMS int64                 `json:"created_at_unix_ms"`
	UpdatedAtUnixMS int64                 `json:"updated_at_unix_ms"`
	Labels          map[string]uint64     `json:"labels"`
	Versions        []parameterVersionDTO `json:"versions"`
}

func toParameterInfoDTO(p domain.ParameterInfo) parameterInfoDTO {
	versions := make([]parameterVersionDTO, 0, len(p.Versions))
	for _, v := range p.Versions {
		versions = append(versions, parameterVersionDTO{
			Version:         v.Version,
			ContentType:     v.ContentType,
			State:           v.State,
			CreatedBy:       v.CreatedBy,
			CreatedAtUnixMS: unixMS(v.CreatedAt),
			MetadataJSON:    rawJSON(v.Metadata),
		})
	}
	return parameterInfoDTO{
		Env:             p.Ref.NS.Env,
		App:             p.Ref.NS.App,
		Key:             p.Ref.Key,
		ContentType:     p.ContentType,
		MetadataJSON:    rawJSON(p.Metadata),
		CreatedAtUnixMS: unixMS(p.CreatedAt),
		UpdatedAtUnixMS: unixMS(p.UpdatedAt),
		Labels:          nonNilLabels(p.Labels),
		Versions:        versions,
	}
}

// --- secrets ---------------------------------------------------------------

type secretVersionDTO struct {
	Version           uint64 `json:"version"`
	State             string `json:"state"`
	CreatedBy         string `json:"created_by"`
	CreatedAtUnixMS   int64  `json:"created_at_unix_ms"`
	DestroyedAtUnixMS int64  `json:"destroyed_at_unix_ms"`
	ExpiresAtUnixMS   int64  `json:"expires_at_unix_ms"`
	MetadataJSON      string `json:"metadata_json"`
}

type secretMetadataDTO struct {
	Env             string             `json:"env"`
	App             string             `json:"app"`
	Key             string             `json:"key"`
	ContentType     string             `json:"content_type"`
	ClientBound     bool               `json:"client_bound"`
	HasAccessToken  bool               `json:"has_access_token"`
	MetadataJSON    string             `json:"metadata_json"`
	CreatedAtUnixMS int64              `json:"created_at_unix_ms"`
	UpdatedAtUnixMS int64              `json:"updated_at_unix_ms"`
	Labels          map[string]uint64  `json:"labels"`
	Versions        []secretVersionDTO `json:"versions"`
}

func toSecretMetadataDTO(s domain.Secret) secretMetadataDTO {
	versions := make([]secretVersionDTO, 0, len(s.Versions))
	for _, v := range s.Versions {
		versions = append(versions, secretVersionDTO{
			Version:           v.Version,
			State:             v.State,
			CreatedBy:         v.CreatedBy,
			CreatedAtUnixMS:   unixMS(v.CreatedAt),
			DestroyedAtUnixMS: unixMS(v.DestroyedAt),
			ExpiresAtUnixMS:   unixMS(v.ExpiresAt),
			MetadataJSON:      rawJSON(v.Metadata),
		})
	}
	return secretMetadataDTO{
		Env:             s.Ref.NS.Env,
		App:             s.Ref.NS.App,
		Key:             s.Ref.Key,
		ContentType:     s.ContentType,
		ClientBound:     s.ClientBound,
		HasAccessToken:  s.HasAccessToken,
		MetadataJSON:    rawJSON(s.Metadata),
		CreatedAtUnixMS: unixMS(s.CreatedAt),
		UpdatedAtUnixMS: unixMS(s.UpdatedAt),
		Labels:          nonNilLabels(s.Labels),
		Versions:        versions,
	}
}

// --- policies --------------------------------------------------------------

type policyRuleDTO struct {
	Operation string `json:"operation"`
	Env       string `json:"env"`
	App       string `json:"app"`
}

type policyDTO struct {
	Name            string          `json:"name"`
	Subject         string          `json:"subject"`
	Allow           []policyRuleDTO `json:"allow"`
	Deny            []policyRuleDTO `json:"deny"`
	CreatedAtUnixMS int64           `json:"created_at_unix_ms"`
	UpdatedAtUnixMS int64           `json:"updated_at_unix_ms"`
}

func toPolicyDTO(p domain.Policy) policyDTO {
	return policyDTO{
		Name:            p.Name,
		Subject:         p.Subject,
		Allow:           toRuleDTOs(p.Allow),
		Deny:            toRuleDTOs(p.Deny),
		CreatedAtUnixMS: unixMS(p.CreatedAt),
		UpdatedAtUnixMS: unixMS(p.UpdatedAt),
	}
}

func toRuleDTOs(rules []domain.PolicyRule) []policyRuleDTO {
	out := make([]policyRuleDTO, 0, len(rules))
	for _, r := range rules {
		out = append(out, policyRuleDTO{Operation: r.Operation, Env: r.Env, App: r.App})
	}
	return out
}

func (d policyDTO) toDomain() domain.Policy {
	return domain.Policy{
		Name:    d.Name,
		Subject: d.Subject,
		Allow:   fromRuleDTOs(d.Allow),
		Deny:    fromRuleDTOs(d.Deny),
	}
}

func fromRuleDTOs(rules []policyRuleDTO) []domain.PolicyRule {
	out := make([]domain.PolicyRule, 0, len(rules))
	for _, r := range rules {
		out = append(out, domain.PolicyRule{Operation: r.Operation, Env: r.Env, App: r.App})
	}
	return out
}

// --- identities ------------------------------------------------------------

type identityCertDTO struct {
	Serial          string `json:"serial"`
	Fingerprint     string `json:"fingerprint"`
	NotAfterUnixMS  int64  `json:"not_after_unix_ms"`
	RevokedAtUnixMS int64  `json:"revoked_at_unix_ms"`
	CreatedAtUnixMS int64  `json:"created_at_unix_ms"`
}

type identityDTO struct {
	Name            string            `json:"name"`
	Kind            string            `json:"kind"`
	Disabled        bool              `json:"disabled"`
	CreatedAtUnixMS int64             `json:"created_at_unix_ms"`
	Namespace       *namespaceRefDTO  `json:"namespace"`
	HasToken        bool              `json:"has_token"`
	Certs           []identityCertDTO `json:"certs"`
}

func toIdentityDTO(id domain.Identity) identityDTO {
	certs := make([]identityCertDTO, 0, len(id.Certs))
	for _, c := range id.Certs {
		certs = append(certs, identityCertDTO{
			Serial:          c.Serial,
			Fingerprint:     c.Fingerprint,
			NotAfterUnixMS:  unixMS(c.NotAfter),
			RevokedAtUnixMS: unixMS(c.RevokedAt),
			CreatedAtUnixMS: unixMS(c.CreatedAt),
		})
	}
	return identityDTO{
		Name:            id.Name,
		Kind:            id.Kind,
		Disabled:        id.Disabled,
		CreatedAtUnixMS: unixMS(id.CreatedAt),
		Namespace:       toNamespaceRefDTO(id.Namespace),
		HasToken:        id.HasToken,
		Certs:           certs,
	}
}

// certBundleDTO is the one-time PEM bundle returned when a client certificate
// is issued. The private key is shown exactly once and never stored.
type certBundleDTO struct {
	CertPEM        string `json:"cert_pem"`
	KeyPEM         string `json:"key_pem"`
	Serial         string `json:"serial"`
	NotAfterUnixMS int64  `json:"not_after_unix_ms"`
}

// --- audit -----------------------------------------------------------------

type auditEventDTO struct {
	ID              int64  `json:"id"`
	EventType       string `json:"event_type"`
	ActorIdentity   string `json:"actor_identity"`
	ActorType       string `json:"actor_type"`
	ResourceType    string `json:"resource_type"`
	ResourceEnv     string `json:"resource_env"`
	ResourceApp     string `json:"resource_app"`
	ResourceKey     string `json:"resource_key"`
	ResourceVersion uint64 `json:"resource_version"`
	Decision        string `json:"decision"`
	SourceIP        string `json:"source_ip"`
	UserAgent       string `json:"user_agent"`
	RequestID       string `json:"request_id"`
	CreatedAtUnixMS int64  `json:"created_at_unix_ms"`
	MetadataJSON    string `json:"metadata_json"`
}

func toAuditEventDTO(e domain.AuditEvent) auditEventDTO {
	return auditEventDTO{
		ID:              e.ID,
		EventType:       e.EventType,
		ActorIdentity:   e.ActorIdentity,
		ActorType:       e.ActorType,
		ResourceType:    e.ResourceType,
		ResourceEnv:     e.ResourceEnv,
		ResourceApp:     e.ResourceApp,
		ResourceKey:     e.ResourceKey,
		ResourceVersion: e.ResourceVersion,
		Decision:        e.Decision,
		SourceIP:        e.SourceIP,
		UserAgent:       e.UserAgent,
		RequestID:       e.RequestID,
		CreatedAtUnixMS: unixMS(e.CreatedAt),
		MetadataJSON:    rawJSON(e.Metadata),
	}
}

// --- subscribers -----------------------------------------------------------

type subscriberDTO struct {
	ClientName          string            `json:"client_name"`
	InstanceID          string            `json:"instance_id"`
	Identity            string            `json:"identity"`
	Namespaces          []namespaceRefDTO `json:"namespaces"`
	RemoteAddr          string            `json:"remote_addr"`
	ConnectedAtUnixMS   int64             `json:"connected_at_unix_ms"`
	LastHeartbeatUnixMS int64             `json:"last_heartbeat_unix_ms"`
	LastAckedRevision   uint64            `json:"last_acked_revision"`
}

func toSubscriberDTO(s domain.Subscriber) subscriberDTO {
	namespaces := make([]namespaceRefDTO, 0, len(s.Namespaces))
	for _, ns := range s.Namespaces {
		namespaces = append(namespaces, namespaceRefDTO{Env: ns.Env, App: ns.App})
	}
	return subscriberDTO{
		ClientName:          s.ClientName,
		InstanceID:          s.InstanceID,
		Identity:            s.Identity,
		Namespaces:          namespaces,
		RemoteAddr:          s.RemoteAddr,
		ConnectedAtUnixMS:   unixMS(s.ConnectedAt),
		LastHeartbeatUnixMS: unixMS(s.LastHeartbeat),
		LastAckedRevision:   s.LastAckedRevision,
	}
}

// --- configuration releases ----------------------------------------------

type resourceRefDTO struct {
	Namespace namespaceRefDTO `json:"namespace"`
	Key       string          `json:"key"`
}

func refDTO(ref domain.Ref) resourceRefDTO {
	return resourceRefDTO{Namespace: namespaceRefDTO{Env: ref.NS.Env, App: ref.NS.App}, Key: ref.Key}
}

func (r resourceRefDTO) toDomain(defaultNS domain.NamespaceRef) domain.Ref {
	ns := domain.NamespaceRef{Env: r.Namespace.Env, App: r.Namespace.App}
	if ns.Env == "" && ns.App == "" {
		ns = defaultNS
	}
	return domain.Ref{NS: ns, Key: r.Key}
}

type releaseEntryDTO struct {
	Alias           string         `json:"alias"`
	Kind            string         `json:"kind"`
	Ref             resourceRefDTO `json:"ref"`
	Version         uint64         `json:"version"`
	ContentType     string         `json:"content_type"`
	MetadataJSON    string         `json:"metadata_json"`
	ParameterDigest string         `json:"parameter_digest"`
	ClientBound     bool           `json:"client_bound"`
	HasAccessToken  bool           `json:"has_access_token"`
}

type releaseDTO struct {
	Namespace       namespaceRefDTO   `json:"namespace"`
	Name            string            `json:"name"`
	Version         uint64            `json:"version"`
	SchemaID        string            `json:"schema_id"`
	SchemaVersion   uint64            `json:"schema_version"`
	Entries         []releaseEntryDTO `json:"entries"`
	Digest          string            `json:"digest"`
	MetadataJSON    string            `json:"metadata_json"`
	CreatedBy       string            `json:"created_by"`
	CreatedAtUnixMS int64             `json:"created_at_unix_ms"`
}

func toReleaseDTO(r domain.ConfigurationRelease) releaseDTO {
	entries := make([]releaseEntryDTO, 0, len(r.Entries))
	for _, e := range r.Entries {
		entries = append(entries, releaseEntryDTO{
			Alias: e.Alias, Kind: e.Kind, Ref: refDTO(e.Ref), Version: e.Version,
			ContentType: e.ContentType, MetadataJSON: rawJSON(e.Metadata),
			ParameterDigest: e.ParameterDigest, ClientBound: e.ClientBound,
			HasAccessToken: e.HasAccessToken,
		})
	}
	return releaseDTO{
		Namespace: namespaceRefDTO{Env: r.Namespace.Env, App: r.Namespace.App},
		Name:      r.Name, Version: r.Version, SchemaID: r.SchemaID, SchemaVersion: r.SchemaVersion,
		Entries: entries, Digest: r.Digest, MetadataJSON: rawJSON(r.Metadata),
		CreatedBy: r.CreatedBy, CreatedAtUnixMS: unixMS(r.CreatedAt),
	}
}

type releaseSelectorDTO struct {
	Alias   string         `json:"alias"`
	Kind    string         `json:"kind"`
	Ref     resourceRefDTO `json:"ref"`
	Version uint64         `json:"version"`
	Label   string         `json:"label"`
}

type createReleaseDTO struct {
	Namespace     namespaceRefDTO      `json:"namespace"`
	Name          string               `json:"name"`
	SchemaID      string               `json:"schema_id"`
	SchemaVersion uint64               `json:"schema_version"`
	Entries       []releaseSelectorDTO `json:"entries"`
	MetadataJSON  string               `json:"metadata_json"`
}

func (d createReleaseDTO) toDomain() domain.CreateConfigurationReleaseInput {
	ns := domain.NamespaceRef{Env: d.Namespace.Env, App: d.Namespace.App}
	entries := make([]domain.ReleaseEntrySelector, 0, len(d.Entries))
	for _, e := range d.Entries {
		entries = append(entries, domain.ReleaseEntrySelector{
			Alias: e.Alias, Kind: e.Kind, Ref: e.Ref.toDomain(ns), Version: e.Version, Label: e.Label,
		})
	}
	return domain.CreateConfigurationReleaseInput{
		Namespace: ns, Name: d.Name, SchemaID: d.SchemaID, SchemaVersion: d.SchemaVersion,
		Entries: entries, Metadata: d.MetadataJSON,
	}
}

type releaseValidationErrorDTO struct {
	Alias         string `json:"alias"`
	Code          string `json:"code"`
	SchemaPointer string `json:"schema_pointer"`
	Message       string `json:"message"`
}

type schemaDTO struct {
	ID              string `json:"id"`
	Version         uint64 `json:"version"`
	SchemaJSON      string `json:"schema_json"`
	Digest          string `json:"digest"`
	MetadataJSON    string `json:"metadata_json"`
	CreatedBy       string `json:"created_by"`
	CreatedAtUnixMS int64  `json:"created_at_unix_ms"`
}

func toSchemaDTO(s domain.ConfigurationSchema) schemaDTO {
	return schemaDTO{ID: s.ID, Version: s.Version, SchemaJSON: s.Schema, Digest: s.Digest,
		MetadataJSON: rawJSON(s.Metadata), CreatedBy: s.CreatedBy, CreatedAtUnixMS: unixMS(s.CreatedAt)}
}

type releaseSubscriberDTO struct {
	Namespace             namespaceRefDTO `json:"namespace"`
	ReleaseName           string          `json:"release_name"`
	ClientName            string          `json:"client_name"`
	InstanceID            string          `json:"instance_id"`
	Identity              string          `json:"identity"`
	State                 string          `json:"state"`
	ReleaseVersion        uint64          `json:"release_version"`
	ActivationRevision    uint64          `json:"activation_revision"`
	RejectionCategory     string          `json:"rejection_category"`
	Diagnostic            string          `json:"diagnostic"`
	ClientTimestampUnixMS int64           `json:"client_timestamp_unix_ms"`
	ServerTimestampUnixMS int64           `json:"server_timestamp_unix_ms"`
	Connected             bool            `json:"connected"`
}

func toReleaseSubscriberDTO(s domain.ReleaseAcknowledgement) releaseSubscriberDTO {
	return releaseSubscriberDTO{
		Namespace:   namespaceRefDTO{Env: s.Namespace.Env, App: s.Namespace.App},
		ReleaseName: s.ReleaseName, ClientName: s.ClientName, InstanceID: s.InstanceID,
		Identity: s.Identity, State: s.State, ReleaseVersion: s.ReleaseVersion,
		ActivationRevision: s.ActivationRevision, RejectionCategory: s.RejectionCategory,
		Diagnostic: s.Diagnostic, ClientTimestampUnixMS: unixMS(s.ClientTimestamp),
		ServerTimestampUnixMS: unixMS(s.ServerTimestamp), Connected: s.Connected,
	}
}

// --- keys ------------------------------------------------------------------

type keyDTO struct {
	ID              string `json:"id"`
	Source          string `json:"source"`
	State           string `json:"state"`
	CreatedAtUnixMS int64  `json:"created_at_unix_ms"`
}

func toKeyDTO(k domain.KeyMetadata) keyDTO {
	return keyDTO{
		ID:              k.ID,
		Source:          k.Source,
		State:           k.State,
		CreatedAtUnixMS: unixMS(k.CreatedAt),
	}
}
