package grpcserver

import (
	"time"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
	"github.com/Suhaibinator/kms/internal/core"
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

// --- refs and selectors ----------------------------------------------------

// nsRefToProto renders a namespace reference on the wire.
func nsRefToProto(ns domain.NamespaceRef) *kmsv1.NamespaceRef {
	return &kmsv1.NamespaceRef{Env: ns.Env, App: ns.App}
}

// nsRefFromProto reads a namespace reference off the wire. A nil message yields
// the zero NamespaceRef, which the core layer rejects with InvalidArgument.
func nsRefFromProto(n *kmsv1.NamespaceRef) domain.NamespaceRef {
	if n == nil {
		return domain.NamespaceRef{}
	}
	return domain.NamespaceRef{Env: n.GetEnv(), App: n.GetApp()}
}

// refToProto renders a resource reference (namespace + relative key).
func refToProto(ref domain.Ref) *kmsv1.ResourceRef {
	return &kmsv1.ResourceRef{Namespace: nsRefToProto(ref.NS), Key: ref.Key}
}

// refFromProto reads a resource reference off the wire. A nil message yields the
// zero Ref, which the core layer rejects with InvalidArgument.
func refFromProto(r *kmsv1.ResourceRef) domain.Ref {
	if r == nil {
		return domain.Ref{}
	}
	return domain.Ref{NS: nsRefFromProto(r.GetNamespace()), Key: r.GetKey()}
}

// selectorFromProto reads a watch selector off the wire.
func selectorFromProto(s *kmsv1.WatchSelector) domain.WatchSelector {
	if s == nil {
		return domain.WatchSelector{}
	}
	return domain.WatchSelector{NS: nsRefFromProto(s.GetNamespace()), KeyPattern: s.GetKeyPattern()}
}

// selectorsFromProto maps a slice of wire selectors to the domain type.
func selectorsFromProto(sels []*kmsv1.WatchSelector) []domain.WatchSelector {
	out := make([]domain.WatchSelector, 0, len(sels))
	for _, s := range sels {
		out = append(out, selectorFromProto(s))
	}
	return out
}

// selectorToProto renders a watch selector on the wire.
func selectorToProto(s domain.WatchSelector) *kmsv1.WatchSelector {
	return &kmsv1.WatchSelector{Namespace: nsRefToProto(s.NS), KeyPattern: s.KeyPattern}
}

// --- auth methods ----------------------------------------------------------

// authMethodsFromProto casts the wire's []string auth methods to the named
// domain type. Validation of unknown values happens in the core layer.
func authMethodsFromProto(methods []string) []domain.AuthMethod {
	if methods == nil {
		return nil
	}
	out := make([]domain.AuthMethod, len(methods))
	for i, m := range methods {
		out[i] = domain.AuthMethod(m)
	}
	return out
}

// authMethodsToProto casts the named domain auth methods back to []string.
func authMethodsToProto(methods []domain.AuthMethod) []string {
	if methods == nil {
		return nil
	}
	out := make([]string, len(methods))
	for i, m := range methods {
		out[i] = string(m)
	}
	return out
}

// --- parameters ------------------------------------------------------------

func toProtoParameter(p domain.Parameter) *kmsv1.Parameter {
	return &kmsv1.Parameter{
		Ref:             refToProto(p.Ref),
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

// --- secrets ---------------------------------------------------------------

func toProtoSecretMetadata(s domain.Secret) *kmsv1.SecretMetadata {
	versions := make([]*kmsv1.SecretVersionInfo, 0, len(s.Versions))
	for _, v := range s.Versions {
		versions = append(versions, toProtoSecretVersionInfo(v))
	}
	return &kmsv1.SecretMetadata{
		Ref:             refToProto(s.Ref),
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

// --- namespaces ------------------------------------------------------------

func toProtoNamespace(n domain.Namespace) *kmsv1.Namespace {
	return &kmsv1.Namespace{
		Ref:                nsRefToProto(n.NamespaceRef),
		Description:        n.Description,
		AllowedAuthMethods: authMethodsToProto(n.AllowedAuthMethods),
		CreatedBy:          n.CreatedBy,
		CreatedAtUnixMs:    unixMS(n.CreatedAt),
		ParameterCount:     n.ParameterCount,
		SecretCount:        n.SecretCount,
	}
}

// --- identities ------------------------------------------------------------

func toProtoIdentity(id domain.Identity) *kmsv1.Identity {
	out := &kmsv1.Identity{
		Name:            id.Name,
		Kind:            id.Kind,
		Disabled:        id.Disabled,
		CreatedAtUnixMs: unixMS(id.CreatedAt),
		HasToken:        id.HasToken,
	}
	if id.Namespace != nil {
		out.Namespace = nsRefToProto(*id.Namespace)
	}
	if len(id.Certs) > 0 {
		out.Certs = make([]*kmsv1.IdentityCertInfo, 0, len(id.Certs))
		for _, c := range id.Certs {
			out.Certs = append(out.Certs, toProtoIdentityCert(c))
		}
	}
	return out
}

func toProtoIdentityCert(c domain.IdentityCert) *kmsv1.IdentityCertInfo {
	return &kmsv1.IdentityCertInfo{
		Serial:          c.Serial,
		Fingerprint:     c.Fingerprint,
		NotAfterUnixMs:  unixMS(c.NotAfter),
		RevokedAtUnixMs: unixMS(c.RevokedAt),
		CreatedAtUnixMs: unixMS(c.CreatedAt),
	}
}

// certBundleToProto renders a freshly issued certificate bundle (private key
// returned exactly once at issuance). A nil bundle yields nil.
func certBundleToProto(b *core.CertBundle) *kmsv1.CertBundle {
	if b == nil {
		return nil
	}
	return &kmsv1.CertBundle{
		CertPem:        b.CertPEM,
		KeyPem:         b.KeyPEM,
		Serial:         b.Serial,
		NotAfterUnixMs: unixMS(b.NotAfter),
	}
}

// --- policies --------------------------------------------------------------

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
		out = append(out, &kmsv1.PolicyRule{
			Operation: r.Operation,
			Env:       r.Env,
			App:       r.App,
			Key:       r.KeyPattern,
		})
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
		out = append(out, domain.PolicyRule{
			Operation:  r.GetOperation(),
			Env:        r.GetEnv(),
			App:        r.GetApp(),
			KeyPattern: r.GetKey(),
		})
	}
	return out
}

// --- audit -----------------------------------------------------------------

func toProtoAuditEvent(e domain.AuditEvent) *kmsv1.AuditEvent {
	return &kmsv1.AuditEvent{
		Id:              e.ID,
		EventType:       e.EventType,
		ActorIdentity:   e.ActorIdentity,
		ActorType:       e.ActorType,
		ResourceType:    e.ResourceType,
		ResourceEnv:     e.ResourceEnv,
		ResourceApp:     e.ResourceApp,
		ResourceKey:     e.ResourceKey,
		ResourceVersion: e.ResourceVersion,
		Decision:        e.Decision,
		SourceIp:        e.SourceIP,
		UserAgent:       e.UserAgent,
		RequestId:       e.RequestID,
		CreatedAtUnixMs: unixMS(e.CreatedAt),
		MetadataJson:    e.Metadata,
	}
}

// --- subscribers -----------------------------------------------------------

func toProtoSubscriber(s domain.Subscriber) *kmsv1.Subscriber {
	sels := make([]*kmsv1.WatchSelector, 0, len(s.Selectors))
	for _, sel := range s.Selectors {
		sels = append(sels, selectorToProto(sel))
	}
	return &kmsv1.Subscriber{
		ClientName:          s.ClientName,
		InstanceId:          s.InstanceID,
		Identity:            s.Identity,
		Selectors:           sels,
		RemoteAddr:          s.RemoteAddr,
		ConnectedAtUnixMs:   unixMS(s.ConnectedAt),
		LastHeartbeatUnixMs: unixMS(s.LastHeartbeat),
		LastAckedRevision:   s.LastAckedRevision,
	}
}

// --- watch events ----------------------------------------------------------

// toSubscribeEvent converts a change-log entry into a wire event. Parameter
// changes carry their (non-sensitive) value inline; secret changes are always
// metadata-only — a secret value is never placed on a watch stream.
func toSubscribeEvent(e domain.ChangeLogEntry) *kmsv1.SubscribeEvent {
	if e.ResourceType == domain.ResourceSecret {
		return &kmsv1.SubscribeEvent{
			Event: &kmsv1.SubscribeEvent_SecretChange{
				SecretChange: &kmsv1.SecretMetadataChange{
					Ref:        refToProto(e.Ref),
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
				Ref:         refToProto(e.Ref),
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
