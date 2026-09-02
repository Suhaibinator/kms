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

// namespacesFromProto maps a slice of wire namespace refs to the domain type.
func namespacesFromProto(refs []*kmsv1.NamespaceRef) []domain.NamespaceRef {
	out := make([]domain.NamespaceRef, 0, len(refs))
	for _, r := range refs {
		out = append(out, nsRefFromProto(r))
	}
	return out
}

// namespacesToProto renders a slice of namespace refs on the wire.
func namespacesToProto(nss []domain.NamespaceRef) []*kmsv1.NamespaceRef {
	out := make([]*kmsv1.NamespaceRef, 0, len(nss))
	for _, ns := range nss {
		out = append(out, nsRefToProto(ns))
	}
	return out
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
			Operation: r.GetOperation(),
			Env:       r.GetEnv(),
			App:       r.GetApp(),
		})
	}
	return out
}

// --- audit -----------------------------------------------------------------

func toProtoAuditEvent(e domain.AuditEvent) *kmsv1.AuditEvent {
	return &kmsv1.AuditEvent{
		Id:                  e.ID,
		EventType:           e.EventType,
		ActorIdentity:       e.ActorIdentity,
		ActorType:           e.ActorType,
		ResourceType:        e.ResourceType,
		ResourceEnv:         e.ResourceEnv,
		ResourceApp:         e.ResourceApp,
		ResourceKey:         e.ResourceKey,
		ResourceVersion:     e.ResourceVersion,
		ResourceNamespaceId: e.ResourceNamespaceID,
		Decision:            e.Decision,
		SourceIp:            e.SourceIP,
		UserAgent:           e.UserAgent,
		RequestId:           e.RequestID,
		CreatedAtUnixMs:     unixMS(e.CreatedAt),
		MetadataJson:        e.Metadata,
	}
}

// --- subscribers -----------------------------------------------------------

func toProtoSubscriber(s domain.Subscriber) *kmsv1.Subscriber {
	return &kmsv1.Subscriber{
		ClientName:          s.ClientName,
		InstanceId:          s.InstanceID,
		Identity:            s.Identity,
		Namespaces:          namespacesToProto(s.Namespaces),
		RemoteAddr:          s.RemoteAddr,
		ConnectedAtUnixMs:   unixMS(s.ConnectedAt),
		LastHeartbeatUnixMs: unixMS(s.LastHeartbeat),
		LastAckedRevision:   s.LastAckedRevision,
	}
}

// --- configuration releases ----------------------------------------------

func toProtoConfigurationReleaseEntry(e domain.ConfigurationReleaseEntry) *kmsv1.ConfigurationReleaseEntry {
	return &kmsv1.ConfigurationReleaseEntry{Alias: e.Alias, Kind: e.Kind, Ref: refToProto(e.Ref), Version: e.Version, ContentType: e.ContentType, MetadataJson: e.Metadata, ParameterDigest: e.ParameterDigest, ClientBound: e.ClientBound, HasAccessToken: e.HasAccessToken}
}

func toProtoConfigurationRelease(r domain.ConfigurationRelease) *kmsv1.ConfigurationRelease {
	entries := make([]*kmsv1.ConfigurationReleaseEntry, 0, len(r.Entries))
	for _, e := range r.Entries {
		entries = append(entries, toProtoConfigurationReleaseEntry(e))
	}
	return &kmsv1.ConfigurationRelease{Namespace: nsRefToProto(r.Namespace), Name: r.Name, Version: r.Version, SchemaId: r.SchemaID, SchemaVersion: r.SchemaVersion, Entries: entries, Digest: r.Digest, MetadataJson: r.Metadata, CreatedBy: r.CreatedBy, CreatedAtUnixMs: unixMS(r.CreatedAt)}
}

func toProtoConfigurationSchema(s domain.ConfigurationSchema) *kmsv1.ConfigurationSchema {
	return &kmsv1.ConfigurationSchema{Id: s.ID, Version: s.Version, SchemaJson: s.Schema, Digest: s.Digest, MetadataJson: s.Metadata, CreatedBy: s.CreatedBy, CreatedAtUnixMs: unixMS(s.CreatedAt)}
}

func toProtoReleaseSubscriber(a domain.ReleaseAcknowledgement) *kmsv1.ReleaseSubscriberState {
	return &kmsv1.ReleaseSubscriberState{Namespace: nsRefToProto(a.Namespace), ReleaseName: a.ReleaseName, ClientName: a.ClientName, InstanceId: a.InstanceID, Identity: a.Identity, State: a.State, ReleaseVersion: a.ReleaseVersion, ActivationRevision: a.ActivationRevision, RejectionCategory: a.RejectionCategory, Diagnostic: a.Diagnostic, ClientTimestampUnixMs: unixMS(a.ClientTimestamp), ServerTimestampUnixMs: unixMS(a.ServerTimestamp), Connected: a.Connected, AppliedDivergent: a.AppliedDivergent, DivergentFieldCount: a.DivergentFieldCount}
}

// toProtoVerifyReleaseDefaults renders the value-free verification result.
// Counts are clamped through uint32 conversion; the request is bounded to 256
// entries so they can never overflow in practice.
func toProtoVerifyReleaseDefaults(r domain.VerifyReleaseDefaultsResult) *kmsv1.VerifyReleaseDefaultsResponse {
	entries := make([]*kmsv1.VerifyEntryVerdict, 0, len(r.Entries))
	for _, e := range r.Entries {
		entries = append(entries, &kmsv1.VerifyEntryVerdict{Alias: e.Alias, Verdict: e.Verdict})
	}
	return &kmsv1.VerifyReleaseDefaultsResponse{
		Name:                        r.ReleaseName,
		Version:                     r.ReleaseVersion,
		ActivationRevision:          r.ActivationRevision,
		SchemaMatches:               r.SchemaMatches,
		Entries:                     entries,
		MatchCount:                  uint32(r.Summary.Match),
		DiffersCount:                uint32(r.Summary.Differs),
		MissingInReleaseCount:       uint32(r.Summary.MissingInRelease),
		UnknownAliasCount:           uint32(r.Summary.UnknownAlias),
		SecretAliasCount:            uint32(r.Summary.SecretAlias),
		UnsupportedContentTypeCount: uint32(r.Summary.UnsupportedContentType),
		UnverifiedCount:             uint32(r.Summary.Unverified),
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
