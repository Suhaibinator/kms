package httpserver

import (
	"time"

	"github.com/Suhaibinator/kms/internal/domain"
)

// The DTO types below define the exact JSON field names in docs/http-api.md.
// They are separate from the domain types because the wire format uses Unix
// milliseconds for timestamps and omits internal fields.

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

// --- parameters ------------------------------------------------------------

type parameterDTO struct {
	Path            string            `json:"path"`
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
		Path:            p.Path,
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
	Path            string                `json:"path"`
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
		Path:            p.Path,
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
	Path            string             `json:"path"`
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
		Path:            s.Path,
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

// --- namespaces ------------------------------------------------------------

type namespaceDTO struct {
	Path            string `json:"path"`
	Description     string `json:"description"`
	CreatedBy       string `json:"created_by"`
	CreatedAtUnixMS int64  `json:"created_at_unix_ms"`
}

func toNamespaceDTO(n domain.Namespace) namespaceDTO {
	return namespaceDTO{
		Path:            n.Path,
		Description:     n.Description,
		CreatedBy:       n.CreatedBy,
		CreatedAtUnixMS: unixMS(n.CreatedAt),
	}
}

// --- policies --------------------------------------------------------------

type policyRuleDTO struct {
	Operation string `json:"operation"`
	Path      string `json:"path"`
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
		out = append(out, policyRuleDTO{Operation: r.Operation, Path: r.Path})
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
		out = append(out, domain.PolicyRule{Operation: r.Operation, Path: r.Path})
	}
	return out
}

// --- identities ------------------------------------------------------------

type identityDTO struct {
	Name            string `json:"name"`
	Kind            string `json:"kind"`
	Disabled        bool   `json:"disabled"`
	CreatedAtUnixMS int64  `json:"created_at_unix_ms"`
}

func toIdentityDTO(id domain.Identity) identityDTO {
	return identityDTO{
		Name:            id.Name,
		Kind:            id.Kind,
		Disabled:        id.Disabled,
		CreatedAtUnixMS: unixMS(id.CreatedAt),
	}
}

// --- audit -----------------------------------------------------------------

type auditEventDTO struct {
	ID              int64  `json:"id"`
	EventType       string `json:"event_type"`
	ActorIdentity   string `json:"actor_identity"`
	ActorType       string `json:"actor_type"`
	ResourceType    string `json:"resource_type"`
	ResourcePath    string `json:"resource_path"`
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
		ResourcePath:    e.ResourcePath,
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
	ClientName          string   `json:"client_name"`
	InstanceID          string   `json:"instance_id"`
	Identity            string   `json:"identity"`
	Paths               []string `json:"paths"`
	RemoteAddr          string   `json:"remote_addr"`
	ConnectedAtUnixMS   int64    `json:"connected_at_unix_ms"`
	LastHeartbeatUnixMS int64    `json:"last_heartbeat_unix_ms"`
	LastAckedRevision   uint64   `json:"last_acked_revision"`
}

func toSubscriberDTO(s domain.Subscriber) subscriberDTO {
	paths := s.Paths
	if paths == nil {
		paths = []string{}
	}
	return subscriberDTO{
		ClientName:          s.ClientName,
		InstanceID:          s.InstanceID,
		Identity:            s.Identity,
		Paths:               paths,
		RemoteAddr:          s.RemoteAddr,
		ConnectedAtUnixMS:   unixMS(s.ConnectedAt),
		LastHeartbeatUnixMS: unixMS(s.LastHeartbeat),
		LastAckedRevision:   s.LastAckedRevision,
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

// nonNilLabels guarantees the JSON renders an object rather than null.
func nonNilLabels(m map[string]uint64) map[string]uint64 {
	if m == nil {
		return map[string]uint64{}
	}
	return m
}
