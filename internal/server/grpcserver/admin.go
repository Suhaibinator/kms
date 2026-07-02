package grpcserver

import (
	"context"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
	"github.com/Suhaibinator/kms/internal/domain"
)

type adminServer struct {
	kmsv1.UnimplementedAdminServiceServer
	s *Server
}

func (h *adminServer) CreateNamespace(ctx context.Context, req *kmsv1.CreateNamespaceRequest) (*kmsv1.CreateNamespaceResponse, error) {
	pr, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	ns, err := h.s.svc.CreateNamespace(ctx, pr, req.GetPath(), req.GetDescription())
	if err != nil {
		return nil, h.s.mapErr(ctx, err)
	}
	return &kmsv1.CreateNamespaceResponse{Namespace: toProtoNamespace(ns)}, nil
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

func (h *adminServer) CreateIdentity(ctx context.Context, req *kmsv1.CreateIdentityRequest) (*kmsv1.CreateIdentityResponse, error) {
	pr, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	id, token, err := h.s.svc.CreateIdentity(ctx, pr, req.GetName(), req.GetKind())
	if err != nil {
		return nil, h.s.mapErr(ctx, err)
	}
	return &kmsv1.CreateIdentityResponse{Identity: toProtoIdentity(id), Token: token}, nil
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

func (h *adminServer) ListAuditEvents(ctx context.Context, req *kmsv1.ListAuditEventsRequest) (*kmsv1.ListAuditEventsResponse, error) {
	pr, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	filter := domain.AuditFilter{
		PathPrefix:    req.GetPathPrefix(),
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
