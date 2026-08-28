package grpcserver

import (
	"context"
	"time"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
	"github.com/Suhaibinator/kms/internal/core"
	"github.com/Suhaibinator/kms/internal/domain"
)

type adminServer struct {
	kmsv1.UnimplementedAdminServiceServer
	s *Server
}

// --- namespaces ------------------------------------------------------------

func (h *adminServer) CreateNamespace(ctx context.Context, req *kmsv1.CreateNamespaceRequest) (*kmsv1.CreateNamespaceResponse, error) {
	pr, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	ns, err := h.s.svc.CreateNamespace(ctx, pr, nsRefFromProto(req.GetRef()), req.GetDescription(), authMethodsFromProto(req.GetAllowedAuthMethods()))
	if err != nil {
		return nil, h.s.mapErr(ctx, err)
	}
	return &kmsv1.CreateNamespaceResponse{Namespace: toProtoNamespace(ns)}, nil
}

func (h *adminServer) UpdateNamespace(ctx context.Context, req *kmsv1.UpdateNamespaceRequest) (*kmsv1.UpdateNamespaceResponse, error) {
	pr, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	ns, err := h.s.svc.UpdateNamespace(ctx, pr, nsRefFromProto(req.GetRef()), req.GetDescription(), authMethodsFromProto(req.GetAllowedAuthMethods()))
	if err != nil {
		return nil, h.s.mapErr(ctx, err)
	}
	return &kmsv1.UpdateNamespaceResponse{Namespace: toProtoNamespace(ns)}, nil
}

func (h *adminServer) DeleteNamespace(ctx context.Context, req *kmsv1.DeleteNamespaceRequest) (*kmsv1.DeleteNamespaceResponse, error) {
	pr, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if err := h.s.svc.DeleteNamespace(ctx, pr, nsRefFromProto(req.GetRef())); err != nil {
		return nil, h.s.mapErr(ctx, err)
	}
	return &kmsv1.DeleteNamespaceResponse{}, nil
}

func (h *adminServer) ListNamespaces(ctx context.Context, req *kmsv1.ListNamespacesRequest) (*kmsv1.ListNamespacesResponse, error) {
	pr, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	nss, next, err := h.s.svc.ListNamespaces(ctx, pr, pageFrom(req.GetPageSize(), req.GetPageToken()))
	if err != nil {
		return nil, h.s.mapErr(ctx, err)
	}
	out := make([]*kmsv1.Namespace, 0, len(nss))
	for _, ns := range nss {
		out = append(out, toProtoNamespace(ns))
	}
	return &kmsv1.ListNamespacesResponse{Namespaces: out, NextPageToken: next}, nil
}

func (h *adminServer) ApplyApplicationDefaults(ctx context.Context, req *kmsv1.ApplyApplicationDefaultsRequest) (*kmsv1.ApplyApplicationDefaultsResponse, error) {
	pr, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	result, err := h.s.svc.ApplyApplicationDefaults(ctx, pr, domain.DefaultsApplyInput{
		Namespace: nsRefFromProto(req.GetNamespace()), Artifact: req.GetArtifact(),
		Overwrite: req.GetOverwrite(), UpdateDefinition: req.GetUpdateDefinition(),
		Execute: req.GetExecute(), PlanDigest: req.GetPlanDigest(),
	})
	if err != nil {
		return nil, h.s.mapErr(ctx, err)
	}
	entries := make([]*kmsv1.DefaultsApplyEntry, 0, len(result.Entries))
	for _, entry := range result.Entries {
		entries = append(entries, &kmsv1.DefaultsApplyEntry{
			Alias: entry.Alias, Key: entry.Key, ContentType: entry.ContentType, Status: entry.Status,
			CurrentVersion: entry.CurrentVersion, AppliedVersion: entry.AppliedVersion, Revision: entry.Revision,
		})
	}
	return &kmsv1.ApplyApplicationDefaultsResponse{
		Profile: result.Profile, SchemaSha256: result.SchemaSHA256,
		ArtifactDigest: result.ArtifactDigest, PlanDigest: result.PlanDigest,
		Entries: entries, MissingSecrets: result.MissingSecrets, Executed: result.Executed,
		DefinitionChanged: result.DefinitionChanged, DefinitionUpdated: result.DefinitionUpdated,
	}, nil
}

// --- policies --------------------------------------------------------------

func (h *adminServer) CreatePolicy(ctx context.Context, req *kmsv1.CreatePolicyRequest) (*kmsv1.CreatePolicyResponse, error) {
	pr, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	p, err := h.s.svc.CreatePolicy(ctx, pr, fromProtoPolicy(req.GetPolicy()))
	if err != nil {
		return nil, h.s.mapErr(ctx, err)
	}
	return &kmsv1.CreatePolicyResponse{Policy: toProtoPolicy(p)}, nil
}

func (h *adminServer) UpdatePolicy(ctx context.Context, req *kmsv1.UpdatePolicyRequest) (*kmsv1.UpdatePolicyResponse, error) {
	pr, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	p, err := h.s.svc.UpdatePolicy(ctx, pr, fromProtoPolicy(req.GetPolicy()))
	if err != nil {
		return nil, h.s.mapErr(ctx, err)
	}
	return &kmsv1.UpdatePolicyResponse{Policy: toProtoPolicy(p)}, nil
}

func (h *adminServer) DeletePolicy(ctx context.Context, req *kmsv1.DeletePolicyRequest) (*kmsv1.DeletePolicyResponse, error) {
	pr, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if err := h.s.svc.DeletePolicy(ctx, pr, req.GetName()); err != nil {
		return nil, h.s.mapErr(ctx, err)
	}
	return &kmsv1.DeletePolicyResponse{}, nil
}

func (h *adminServer) ListPolicies(ctx context.Context, req *kmsv1.ListPoliciesRequest) (*kmsv1.ListPoliciesResponse, error) {
	pr, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	ps, next, err := h.s.svc.ListPolicies(ctx, pr, pageFrom(req.GetPageSize(), req.GetPageToken()))
	if err != nil {
		return nil, h.s.mapErr(ctx, err)
	}
	out := make([]*kmsv1.Policy, 0, len(ps))
	for _, p := range ps {
		out = append(out, toProtoPolicy(p))
	}
	return &kmsv1.ListPoliciesResponse{Policies: out, NextPageToken: next}, nil
}

// --- identities ------------------------------------------------------------

func (h *adminServer) CreateIdentity(ctx context.Context, req *kmsv1.CreateIdentityRequest) (*kmsv1.CreateIdentityResponse, error) {
	pr, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	in := core.CreateIdentityInput{
		Name:        req.GetName(),
		Kind:        req.GetKind(),
		AuthMethods: authMethodsFromProto(req.GetAuthMethods()),
		CertTTL:     time.Duration(req.GetCertTtlSeconds()) * time.Second,
	}
	if ns := req.GetNamespace(); ns != nil {
		bound := nsRefFromProto(ns)
		in.Namespace = &bound
	}
	res, err := h.s.svc.CreateIdentity(ctx, pr, in)
	if err != nil {
		return nil, h.s.mapErr(ctx, err)
	}
	return &kmsv1.CreateIdentityResponse{
		Identity: toProtoIdentity(res.Identity),
		Token:    res.Token,
		Cert:     certBundleToProto(res.Cert),
	}, nil
}

func (h *adminServer) ListIdentities(ctx context.Context, req *kmsv1.ListIdentitiesRequest) (*kmsv1.ListIdentitiesResponse, error) {
	pr, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	ids, next, err := h.s.svc.ListIdentities(ctx, pr, pageFrom(req.GetPageSize(), req.GetPageToken()))
	if err != nil {
		return nil, h.s.mapErr(ctx, err)
	}
	out := make([]*kmsv1.Identity, 0, len(ids))
	for _, id := range ids {
		out = append(out, toProtoIdentity(id))
	}
	return &kmsv1.ListIdentitiesResponse{Identities: out, NextPageToken: next}, nil
}

func (h *adminServer) RevokeIdentity(ctx context.Context, req *kmsv1.RevokeIdentityRequest) (*kmsv1.RevokeIdentityResponse, error) {
	pr, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if err := h.s.svc.RevokeIdentity(ctx, pr, req.GetName()); err != nil {
		return nil, h.s.mapErr(ctx, err)
	}
	return &kmsv1.RevokeIdentityResponse{}, nil
}

func (h *adminServer) RotateIdentityToken(ctx context.Context, req *kmsv1.RotateIdentityTokenRequest) (*kmsv1.RotateIdentityTokenResponse, error) {
	pr, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	token, err := h.s.svc.RotateIdentityToken(ctx, pr, req.GetName())
	if err != nil {
		return nil, h.s.mapErr(ctx, err)
	}
	return &kmsv1.RotateIdentityTokenResponse{Token: token}, nil
}

func (h *adminServer) IssueIdentityCertificate(ctx context.Context, req *kmsv1.IssueIdentityCertificateRequest) (*kmsv1.IssueIdentityCertificateResponse, error) {
	pr, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	bundle, err := h.s.svc.IssueIdentityCertificate(ctx, pr, req.GetName(), time.Duration(req.GetTtlSeconds())*time.Second)
	if err != nil {
		return nil, h.s.mapErr(ctx, err)
	}
	return &kmsv1.IssueIdentityCertificateResponse{Cert: certBundleToProto(bundle)}, nil
}

func (h *adminServer) RevokeIdentityCertificate(ctx context.Context, req *kmsv1.RevokeIdentityCertificateRequest) (*kmsv1.RevokeIdentityCertificateResponse, error) {
	pr, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if err := h.s.svc.RevokeIdentityCertificate(ctx, pr, req.GetName(), req.GetSerial()); err != nil {
		return nil, h.s.mapErr(ctx, err)
	}
	return &kmsv1.RevokeIdentityCertificateResponse{}, nil
}

// --- identity self-description & CA -----------------------------------------

// WhoAmI returns the caller's own identity and namespace binding. Any
// authenticated identity may call it (no policy check); it is the SDK's
// namespace-discovery mechanism.
func (h *adminServer) WhoAmI(ctx context.Context, _ *kmsv1.WhoAmIRequest) (*kmsv1.WhoAmIResponse, error) {
	pr, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	res, err := h.s.svc.WhoAmI(ctx, pr)
	if err != nil {
		return nil, h.s.mapErr(ctx, err)
	}
	resp := &kmsv1.WhoAmIResponse{
		Name:       res.Name,
		Kind:       res.Kind,
		AuthMethod: string(res.Method),
	}
	if res.Namespace != nil {
		resp.Namespace = nsRefToProto(*res.Namespace)
	}
	return resp, nil
}

// GetCACertificate returns the built-in CA certificate. It is public: no
// authentication is required (the interceptor exempts it), so apps can fetch
// the trust anchor before they hold any credential.
func (h *adminServer) GetCACertificate(ctx context.Context, _ *kmsv1.GetCACertificateRequest) (*kmsv1.GetCACertificateResponse, error) {
	pem, err := h.s.svc.CACertPEM()
	if err != nil {
		return nil, h.s.mapErr(ctx, err)
	}
	return &kmsv1.GetCACertificateResponse{CertPem: string(pem)}, nil
}

// --- audit / subscribers ---------------------------------------------------

func (h *adminServer) ListAuditEvents(ctx context.Context, req *kmsv1.ListAuditEventsRequest) (*kmsv1.ListAuditEventsResponse, error) {
	pr, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	filter := domain.AuditFilter{
		Env:           req.GetEnv(),
		App:           req.GetApp(),
		KeyPrefix:     req.GetKeyPrefix(),
		ActorIdentity: req.GetActorIdentity(),
		EventType:     req.GetEventType(),
		From:          unixMSToTime(req.GetFromUnixMs()),
		To:            unixMSToTime(req.GetToUnixMs()),
	}
	events, next, err := h.s.svc.ListAuditEvents(ctx, pr, filter, pageFrom(req.GetPageSize(), req.GetPageToken()))
	if err != nil {
		return nil, h.s.mapErr(ctx, err)
	}
	out := make([]*kmsv1.AuditEvent, 0, len(events))
	for _, e := range events {
		out = append(out, toProtoAuditEvent(e))
	}
	return &kmsv1.ListAuditEventsResponse{Events: out, NextPageToken: next}, nil
}

func (h *adminServer) ListSubscribers(ctx context.Context, req *kmsv1.ListSubscribersRequest) (*kmsv1.ListSubscribersResponse, error) {
	pr, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	subs, rev, err := h.s.svc.ListSubscribers(ctx, pr)
	if err != nil {
		return nil, h.s.mapErr(ctx, err)
	}
	out := make([]*kmsv1.Subscriber, 0, len(subs))
	for _, sub := range subs {
		out = append(out, toProtoSubscriber(sub))
	}
	return &kmsv1.ListSubscribersResponse{Subscribers: out, CurrentRevision: rev}, nil
}

func (h *adminServer) ListReleaseSubscribers(ctx context.Context, req *kmsv1.ListReleaseSubscribersRequest) (*kmsv1.ListReleaseSubscribersResponse, error) {
	pr, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	rows, next, rev, err := h.s.svc.ListReleaseSubscribers(ctx, pr, nsRefFromProto(req.GetNamespace()), req.GetReleaseName(), pageFrom(req.GetPageSize(), req.GetPageToken()))
	if err != nil {
		return nil, h.s.mapErr(ctx, err)
	}
	out := make([]*kmsv1.ReleaseSubscriberState, 0, len(rows))
	for _, row := range rows {
		out = append(out, toProtoReleaseSubscriber(row))
	}
	return &kmsv1.ListReleaseSubscribersResponse{Subscribers: out, NextPageToken: next, CurrentRevision: rev}, nil
}

// Health is public: it reports liveness and readiness without requiring
// credentials or a ready service, so external probes can call it at any time.
func (h *adminServer) Health(ctx context.Context, _ *kmsv1.HealthRequest) (*kmsv1.HealthResponse, error) {
	ready := h.s.svc.Ready(ctx) == nil
	rev, err := h.s.svc.CurrentRevision(ctx)
	if err != nil {
		// Store unreachable: report zero revision and not-ready, but the process
		// itself is still healthy (up and answering).
		rev = 0
		ready = false
	}
	return &kmsv1.HealthResponse{
		Healthy:         true,
		Ready:           ready,
		Version:         h.s.svc.Version(),
		CurrentRevision: rev,
	}, nil
}
