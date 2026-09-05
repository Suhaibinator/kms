package grpcserver

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
	"github.com/Suhaibinator/kms/internal/core"
	"github.com/Suhaibinator/kms/internal/domain"
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
	val, err := h.s.svc.GetSecret(ctx, pr, refFromProto(req.GetRef()), req.GetVersion(), req.GetLabel(), req.GetSecretToken(), req.GetBindingKey())
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
	return nil, status.Error(codes.FailedPrecondition, "PutSecret is incompatible with KMS v0.3; use PutSecretV03")
}

func (h *secretServer) PutSecretV03(ctx context.Context, req *kmsv1.PutSecretRequest) (*kmsv1.PutSecretResponse, error) {
	pr, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	res, err := h.s.svc.PutSecret(ctx, pr, core.PutSecretInput{
		Ref:           refFromProto(req.GetRef()),
		Value:         req.GetValue(),
		ContentType:   req.GetContentType(),
		Metadata:      req.GetMetadataJson(),
		BindingKey:    req.GetBindingKey(),
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

func (h *secretServer) BindSecret(ctx context.Context, req *kmsv1.BindSecretRequest) (*kmsv1.SecretVersionTransitionResponse, error) {
	pr, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	result, err := h.s.svc.BindSecret(ctx, pr, refFromProto(req.GetRef()), req.GetExpectedCurrentVersion(), req.GetBindingKey())
	if err != nil {
		return nil, h.s.mapErr(ctx, err)
	}
	return secretVersionTransitionResponse(result), nil
}

func (h *secretServer) UnbindSecret(ctx context.Context, req *kmsv1.UnbindSecretRequest) (*kmsv1.SecretVersionTransitionResponse, error) {
	pr, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	result, err := h.s.svc.UnbindSecret(ctx, pr, refFromProto(req.GetRef()), req.GetExpectedCurrentVersion(), req.GetBindingKey())
	if err != nil {
		return nil, h.s.mapErr(ctx, err)
	}
	return secretVersionTransitionResponse(result), nil
}

func (h *secretServer) PreviewSecretBindingCohort(ctx context.Context, req *kmsv1.PreviewSecretBindingCohortRequest) (*kmsv1.SecretBindingCohortResponse, error) {
	pr, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	result, err := h.s.svc.PreviewSecretBindingCohort(ctx, pr, refFromProto(req.GetRef()), req.GetAnchorVersion(), req.GetBindingKey())
	if err != nil {
		return nil, h.s.mapErr(ctx, err)
	}
	return secretBindingCohortResponse(result), nil
}

func (h *secretServer) RotateSecretBindingKey(ctx context.Context, req *kmsv1.RotateSecretBindingKeyRequest) (*kmsv1.SecretVersionTransitionResponse, error) {
	pr, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	result, err := h.s.svc.RotateSecretBindingKey(ctx, pr, refFromProto(req.GetRef()), req.GetExpectedCurrentVersion(), req.GetBindingKey(), req.GetNewBindingKey())
	if err != nil {
		return nil, h.s.mapErr(ctx, err)
	}
	return secretVersionTransitionResponse(result), nil
}

func (h *secretServer) PurgeSecretBindingCohort(ctx context.Context, req *kmsv1.PurgeSecretBindingCohortRequest) (*kmsv1.SecretBindingCohortResponse, error) {
	pr, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	result, err := h.s.svc.PurgeSecretBindingCohort(ctx, pr, refFromProto(req.GetRef()), req.GetAnchorVersion(), req.GetBindingKey(), req.GetExpectedRevision(), req.GetExpectedAffectedVersions())
	if err != nil {
		return nil, h.s.mapErr(ctx, err)
	}
	return secretBindingCohortResponse(result), nil
}

func (h *secretServer) PreviewSecretUnboundVersions(ctx context.Context, req *kmsv1.PreviewSecretUnboundVersionsRequest) (*kmsv1.SecretVersionSetResponse, error) {
	pr, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	result, err := h.s.svc.PreviewSecretUnboundVersions(ctx, pr, refFromProto(req.GetRef()))
	if err != nil {
		return nil, h.s.mapErr(ctx, err)
	}
	return secretVersionSetResponse(result), nil
}

func (h *secretServer) PurgeSecretUnboundVersions(ctx context.Context, req *kmsv1.PurgeSecretUnboundVersionsRequest) (*kmsv1.SecretVersionSetResponse, error) {
	pr, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	result, err := h.s.svc.PurgeSecretUnboundVersions(ctx, pr, refFromProto(req.GetRef()), req.GetExpectedRevision(), req.GetExpectedAffectedVersions())
	if err != nil {
		return nil, h.s.mapErr(ctx, err)
	}
	return secretVersionSetResponse(result), nil
}

func secretVersionTransitionResponse(result core.SecretVersionTransitionResult) *kmsv1.SecretVersionTransitionResponse {
	return &kmsv1.SecretVersionTransitionResponse{
		CurrentVersion:  result.CurrentVersion,
		PreviousVersion: result.PreviousVersion,
		Revision:        result.Revision,
	}
}

func secretVersionSetResponse(result core.SecretVersionSetResult) *kmsv1.SecretVersionSetResponse {
	return &kmsv1.SecretVersionSetResponse{
		AffectedVersions: append([]uint64(nil), result.AffectedVersions...),
		Revision:         result.Revision,
	}
}

func secretBindingCohortResponse(result core.SecretBindingCohortResult) *kmsv1.SecretBindingCohortResponse {
	return &kmsv1.SecretBindingCohortResponse{
		AnchorVersion:    result.AnchorVersion,
		AffectedVersions: append([]uint64(nil), result.AffectedVersions...),
		Revision:         result.Revision,
	}
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
	var sec domain.Secret
	if req.GetVersion() == 0 && req.GetLabel() == "" {
		sec, err = h.s.svc.GetSecretInfo(ctx, pr, refFromProto(req.GetRef()))
	} else {
		sec, err = h.s.svc.GetSecretVersionInfo(ctx, pr, refFromProto(req.GetRef()), req.GetVersion(), req.GetLabel())
	}
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
