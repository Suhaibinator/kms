package grpcserver

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
	"github.com/Suhaibinator/kms/internal/core"
	"github.com/Suhaibinator/kms/internal/storage"
)

type parameterServer struct {
	kmsv1.UnimplementedParameterServiceServer
	s *Server
}

func (h *parameterServer) GetParameter(ctx context.Context, req *kmsv1.GetParameterRequest) (*kmsv1.GetParameterResponse, error) {
	pr, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	p, err := h.s.svc.GetParameter(ctx, pr, req.GetPath(), req.GetVersion(), req.GetLabel())
	if err != nil {
		return nil, h.s.mapErr(ctx, err)
	}
	return &kmsv1.GetParameterResponse{Parameter: toProtoParameter(p)}, nil
}

func (h *parameterServer) PutParameter(ctx context.Context, req *kmsv1.PutParameterRequest) (*kmsv1.PutParameterResponse, error) {
	pr, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	version, revision, err := h.s.svc.PutParameter(ctx, pr, req.GetPath(), req.GetValue(), req.GetContentType(), req.GetMetadataJson())
	if err != nil {
		return nil, h.s.mapErr(ctx, err)
	}
	return &kmsv1.PutParameterResponse{Version: version, Revision: revision}, nil
}

func (h *parameterServer) ListParameters(ctx context.Context, req *kmsv1.ListParametersRequest) (*kmsv1.ListParametersResponse, error) {
	pr, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	params, next, err := h.s.svc.ListParameters(ctx, pr, req.GetPathPrefix(), pageFrom(req.GetPageSize(), req.GetPageToken()))
	if err != nil {
		return nil, h.s.mapErr(ctx, err)
	}
	return &kmsv1.ListParametersResponse{
		Parameters:    toProtoParameters(params),
		NextPageToken: next,
	}, nil
}

func (h *parameterServer) DeleteParameter(ctx context.Context, req *kmsv1.DeleteParameterRequest) (*kmsv1.DeleteParameterResponse, error) {
	pr, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	revision, err := h.s.svc.DeleteParameter(ctx, pr, req.GetPath())
	if err != nil {
		return nil, h.s.mapErr(ctx, err)
	}
	return &kmsv1.DeleteParameterResponse{Revision: revision}, nil
}

func (h *parameterServer) GetParameterMetadata(ctx context.Context, req *kmsv1.GetParameterMetadataRequest) (*kmsv1.GetParameterMetadataResponse, error) {
	pr, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	info, err := h.s.svc.GetParameterInfo(ctx, pr, req.GetPath())
	if err != nil {
		return nil, h.s.mapErr(ctx, err)
	}
	versions := make([]*kmsv1.ParameterVersionInfo, 0, len(info.Versions))
	for _, v := range info.Versions {
		versions = append(versions, toProtoParamVersionInfo(v))
	}
	return &kmsv1.GetParameterMetadataResponse{
		Path:            info.Path,
		ContentType:     info.ContentType,
		MetadataJson:    info.Metadata,
		CreatedAtUnixMs: unixMS(info.CreatedAt),
		UpdatedAtUnixMs: unixMS(info.UpdatedAt),
		Labels:          info.Labels,
		Versions:        versions,
	}, nil
}

// pageFrom builds a storage.ListPage from proto pagination fields.
func pageFrom(size int32, token string) storage.ListPage {
	limit := 0
	if size > 0 {
		limit = int(size)
	}
	return storage.ListPage{Limit: limit, Token: token}
}

// requirePrincipal returns the authenticated principal or an Unauthenticated
// status. The auth interceptor guarantees presence on authenticated methods;
// this is a defensive backstop.
func requirePrincipal(ctx context.Context) (core.Principal, error) {
	pr, ok := PrincipalFromContext(ctx)
	if !ok {
		return core.Principal{}, status.Error(codes.Unauthenticated, "unauthenticated")
	}
	return pr, nil
}
