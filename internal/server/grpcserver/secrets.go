package grpcserver

import (
	"context"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
	"github.com/Suhaibinator/kms/internal/core"
)

type secretServer struct {
	kmsv1.UnimplementedSecretServiceServer
	s *Server
}

func (h *secretServer) GetSecret(ctx context.Context, req *kmsv1.GetSecretRequest) (*kmsv1.GetSecretResponse, error) {
	pr, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	val, err := h.s.svc.GetSecret(ctx, pr, refFromProto(req.GetRef()), req.GetVersion(), req.GetLabel())
	if err != nil {
		return nil, h.s.mapErr(ctx, err)
	}
	return &kmsv1.GetSecretResponse{
		Ref:             refToProto(val.Ref),
		Version:         val.Version,
		Value:           val.Value,
		ContentType:     val.ContentType,
		MetadataJson:    val.Metadata,
		CreatedAtUnixMs: unixMS(val.CreatedAt),
	}, nil
}

func (h *secretServer) PutSecret(ctx context.Context, req *kmsv1.PutSecretRequest) (*kmsv1.PutSecretResponse, error) {
	pr, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	res, err := h.s.svc.PutSecret(ctx, pr, core.PutSecretInput{
		Ref:           refFromProto(req.GetRef()),
		Value:         req.GetValue(),
		ContentType:   req.GetContentType(),
		Metadata:      req.GetMetadataJson(),
		ClientBound:   req.GetClientBound(),
		GenerateToken: req.GetGenerateAccessToken(),
		ExpiresAt:     req.GetExpiresAtUnixMs(),
	})
	if err != nil {
		return nil, h.s.mapErr(ctx, err)
	}
	// access_token is populated only when a token was minted.
	return &kmsv1.PutSecretResponse{
		Version:     res.Version,
		Revision:    res.Revision,
		AccessToken: res.AccessToken,
	}, nil
}

func (h *secretServer) ListSecrets(ctx context.Context, req *kmsv1.ListSecretsRequest) (*kmsv1.ListSecretsResponse, error) {
	pr, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	secrets, next, err := h.s.svc.ListSecrets(ctx, pr, nsRefFromProto(req.GetNamespace()), req.GetKeyPrefix(), pageFrom(req.GetPageSize(), req.GetPageToken()))
	if err != nil {
		return nil, h.s.mapErr(ctx, err)
	}
	out := make([]*kmsv1.SecretMetadata, 0, len(secrets))
	for _, sec := range secrets {
		out = append(out, toProtoSecretMetadata(sec))
	}
	return &kmsv1.ListSecretsResponse{Secrets: out, NextPageToken: next}, nil
}

func (h *secretServer) DeleteSecret(ctx context.Context, req *kmsv1.DeleteSecretRequest) (*kmsv1.DeleteSecretResponse, error) {
	pr, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	revision, err := h.s.svc.DeleteSecret(ctx, pr, refFromProto(req.GetRef()))
	if err != nil {
		return nil, h.s.mapErr(ctx, err)
	}
	return &kmsv1.DeleteSecretResponse{Revision: revision}, nil
}

func (h *secretServer) DisableSecret(ctx context.Context, req *kmsv1.DisableSecretRequest) (*kmsv1.DisableSecretResponse, error) {
	pr, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	revision, err := h.s.svc.DisableSecret(ctx, pr, refFromProto(req.GetRef()), req.GetVersion(), req.GetEnable())
	if err != nil {
		return nil, h.s.mapErr(ctx, err)
	}
	return &kmsv1.DisableSecretResponse{Revision: revision}, nil
}

func (h *secretServer) DestroySecretVersion(ctx context.Context, req *kmsv1.DestroySecretVersionRequest) (*kmsv1.DestroySecretVersionResponse, error) {
	pr, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	revision, err := h.s.svc.DestroySecretVersion(ctx, pr, refFromProto(req.GetRef()), req.GetVersion())
	if err != nil {
		return nil, h.s.mapErr(ctx, err)
	}
	return &kmsv1.DestroySecretVersionResponse{Revision: revision}, nil
}

func (h *secretServer) GetSecretMetadata(ctx context.Context, req *kmsv1.GetSecretMetadataRequest) (*kmsv1.GetSecretMetadataResponse, error) {
	pr, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	sec, err := h.s.svc.GetSecretInfo(ctx, pr, refFromProto(req.GetRef()))
	if err != nil {
		return nil, h.s.mapErr(ctx, err)
	}
	return &kmsv1.GetSecretMetadataResponse{Secret: toProtoSecretMetadata(sec)}, nil
}

func (h *secretServer) PromoteSecretVersion(ctx context.Context, req *kmsv1.PromoteSecretVersionRequest) (*kmsv1.PromoteSecretVersionResponse, error) {
	pr, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	current, previous, revision, err := h.s.svc.PromoteSecretVersion(ctx, pr, refFromProto(req.GetRef()), req.GetVersion())
	if err != nil {
		return nil, h.s.mapErr(ctx, err)
	}
	return &kmsv1.PromoteSecretVersionResponse{
		CurrentVersion:  current,
		PreviousVersion: previous,
		Revision:        revision,
	}, nil
}
