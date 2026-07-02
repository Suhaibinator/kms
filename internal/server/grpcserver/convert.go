package grpcserver

import (
	"time"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
	"github.com/Suhaibinator/kms/internal/domain"
)

// unixMS converts a time to unix milliseconds; the zero time maps to 0.
func unixMS(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

// unixMSToTime converts unix milliseconds to a UTC time; 0 maps to the zero time.
func unixMSToTime(ms int64) time.Time {
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}

func toProtoParameter(p domain.Parameter) *kmsv1.Parameter {
	return &kmsv1.Parameter{
		Path:            p.Path,
		Value:           p.Value,
		ContentType:     p.ContentType,
		Version:         p.Version,
		MetadataJson:    p.Metadata,
		CreatedBy:       p.CreatedBy,
		CreatedAtUnixMs: unixMS(p.CreatedAt),
		Labels:          p.Labels,
	}
}

func toProtoParameters(ps []domain.Parameter) []*kmsv1.Parameter {
	out := make([]*kmsv1.Parameter, 0, len(ps))
	for _, p := range ps {
		out = append(out, toProtoParameter(p))
	}
	return out
}

func toProtoParamVersionInfo(v domain.ParameterVersionInfo) *kmsv1.ParameterVersionInfo {
	return &kmsv1.ParameterVersionInfo{
		Version:         v.Version,
		ContentType:     v.ContentType,
		State:           v.State,
		CreatedBy:       v.CreatedBy,
		CreatedAtUnixMs: unixMS(v.CreatedAt),
		MetadataJson:    v.Metadata,
	}
}

func toProtoSecretMetadata(s domain.Secret) *kmsv1.SecretMetadata {
	versions := make([]*kmsv1.SecretVersionInfo, 0, len(s.Versions))
	for _, v := range s.Versions {
		versions = append(versions, toProtoSecretVersionInfo(v))
	}
	return &kmsv1.SecretMetadata{
		Path:            s.Path,
		ContentType:     s.ContentType,
		ClientBound:     s.ClientBound,
		HasAccessToken:  s.HasAccessToken,
		MetadataJson:    s.Metadata,
		CreatedAtUnixMs: unixMS(s.CreatedAt),
		UpdatedAtUnixMs: unixMS(s.UpdatedAt),
		Labels:          s.Labels,
		Versions:        versions,
	}
}

func toProtoSecretVersionInfo(v domain.SecretVersionInfo) *kmsv1.SecretVersionInfo {
	return &kmsv1.SecretVersionInfo{
		Version:           v.Version,
		State:             v.State,
		CreatedBy:         v.CreatedBy,
		CreatedAtUnixMs:   unixMS(v.CreatedAt),
		DestroyedAtUnixMs: unixMS(v.DestroyedAt),
		ExpiresAtUnixMs:   unixMS(v.ExpiresAt),
		MetadataJson:      v.Metadata,
	}
}

func toProtoNamespace(n domain.Namespace) *kmsv1.Namespace {
	return &kmsv1.Namespace{
		Path:            n.Path,
		Description:     n.Description,
		CreatedBy:       n.CreatedBy,
		CreatedAtUnixMs: unixMS(n.CreatedAt),
	}
}

func toProtoIdentity(id domain.Identity) *kmsv1.Identity {
	return &kmsv1.Identity{
		Name:            id.Name,
		Kind:            id.Kind,
		Disabled:        id.Disabled,
		CreatedAtUnixMs: unixMS(id.CreatedAt),
	}
}

func toProtoPolicy(p domain.Policy) *kmsv1.Policy {
	return &kmsv1.Policy{
		Name:            p.Name,
		Subject:         p.Subject,
		Allow:           toProtoRules(p.Allow),
		Deny:            toProtoRules(p.Deny),
		CreatedAtUnixMs: unixMS(p.CreatedAt),
		UpdatedAtUnixMs: unixMS(p.UpdatedAt),
	}
}

func toProtoRules(rs []domain.PolicyRule) []*kmsv1.PolicyRule {
	out := make([]*kmsv1.PolicyRule, 0, len(rs))
	for _, r := range rs {
		out = append(out, &kmsv1.PolicyRule{Operation: r.Operation, Path: r.Path})
	}
	return out
}

func fromProtoPolicy(p *kmsv1.Policy) domain.Policy {
	if p == nil {
		return domain.Policy{}
	}
	return domain.Policy{
		Name:    p.GetName(),
		Subject: p.GetSubject(),
		Allow:   fromProtoRules(p.GetAllow()),
		Deny:    fromProtoRules(p.GetDeny()),
	}
}

func fromProtoRules(rs []*kmsv1.PolicyRule) []domain.PolicyRule {
	out := make([]domain.PolicyRule, 0, len(rs))
	for _, r := range rs {
		out = append(out, domain.PolicyRule{Operation: r.GetOperation(), Path: r.GetPath()})
	}
	return out
}

func toProtoAuditEvent(e domain.AuditEvent) *kmsv1.AuditEvent {
	return &kmsv1.AuditEvent{
		Id:              e.ID,
		EventType:       e.EventType,
		ActorIdentity:   e.ActorIdentity,
		ActorType:       e.ActorType,
		ResourceType:    e.ResourceType,
		ResourcePath:    e.ResourcePath,
		ResourceVersion: e.ResourceVersion,
		Decision:        e.Decision,
		SourceIp:        e.SourceIP,
		UserAgent:       e.UserAgent,
		RequestId:       e.RequestID,
		CreatedAtUnixMs: unixMS(e.CreatedAt),
		MetadataJson:    e.Metadata,
	}
}

func toProtoSubscriber(s domain.Subscriber) *kmsv1.Subscriber {
	return &kmsv1.Subscriber{
		ClientName:          s.ClientName,
		InstanceId:          s.InstanceID,
		Identity:            s.Identity,
		Paths:               s.Paths,
		RemoteAddr:          s.RemoteAddr,
		ConnectedAtUnixMs:   unixMS(s.ConnectedAt),
		LastHeartbeatUnixMs: unixMS(s.LastHeartbeat),
		LastAckedRevision:   s.LastAckedRevision,
	}
}

// toSubscribeEvent converts a change-log entry into a wire event. Parameter
// changes carry their (non-sensitive) value inline; secret changes are always
// metadata-only — a secret value is never placed on a watch stream.
func toSubscribeEvent(e domain.ChangeLogEntry) *kmsv1.SubscribeEvent {
	if e.ResourceType == domain.ResourceSecret {
		return &kmsv1.SubscribeEvent{
			Event: &kmsv1.SubscribeEvent_SecretChange{
				SecretChange: &kmsv1.SecretMetadataChange{
					Path:       e.Path,
					ChangeType: e.ChangeType,
					Version:    e.Version,
					Label:      e.Label,
				},
			},
			Revision: e.Revision,
		}
	}
	return &kmsv1.SubscribeEvent{
		Event: &kmsv1.SubscribeEvent_Change{
			Change: &kmsv1.ParameterChange{
				Path:        e.Path,
				ChangeType:  e.ChangeType,
				Value:       e.Value,
				ContentType: e.ContentType,
				Version:     e.Version,
				Label:       e.Label,
			},
		},
		Revision: e.Revision,
	}
}
