package grpcserver

import (
	"context"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
	"github.com/Suhaibinator/kms/internal/domain"
)

type configurationReleaseServer struct {
	kmsv1.UnimplementedConfigurationReleaseServiceServer
	s *Server
}

func (h *configurationReleaseServer) CreateRelease(ctx context.Context, req *kmsv1.CreateReleaseRequest) (*kmsv1.CreateReleaseResponse, error) {
	if req.SchemaVersion == nil {
		return nil, h.s.mapErr(ctx, domain.Errorf(domain.ErrInvalidArgument, "schema_version is required"))
	}
	pr, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	entries := make([]domain.ReleaseEntrySelector, 0, len(req.GetEntries()))
	for _, e := range req.GetEntries() {
		entries = append(entries, domain.ReleaseEntrySelector{Alias: e.GetAlias(), Kind: e.GetKind(), Ref: refFromProto(e.GetRef()), Version: e.GetVersion(), Label: e.GetLabel()})
	}
	out, err := h.s.svc.CreateConfigurationRelease(ctx, pr, domain.CreateConfigurationReleaseInput{Namespace: nsRefFromProto(req.GetNamespace()), Name: req.GetName(), SchemaVersion: req.GetSchemaVersion(), Entries: entries, Metadata: req.GetMetadataJson()})
	if err != nil {
		return nil, h.s.mapErr(ctx, err)
	}
	return &kmsv1.CreateReleaseResponse{Release: toProtoConfigurationRelease(out)}, nil
}

func (h *configurationReleaseServer) ValidateRelease(ctx context.Context, req *kmsv1.ValidateReleaseRequest) (*kmsv1.ValidateReleaseResponse, error) {
	if req.SchemaVersion == nil {
		return nil, h.s.mapErr(ctx, domain.Errorf(domain.ErrInvalidArgument, "schema_version is required"))
	}
	pr, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	errs, err := h.s.svc.ValidateConfigurationRelease(ctx, pr, domain.ReleaseTrack{Namespace: nsRefFromProto(req.GetNamespace()), Name: req.GetName(), SchemaVersion: req.GetSchemaVersion()}, req.GetVersion())
	if err != nil {
		return nil, h.s.mapErr(ctx, err)
	}
	out := make([]*kmsv1.ReleaseValidationError, 0, len(errs))
	for _, e := range errs {
		out = append(out, toProtoReleaseValidationError(e))
	}
	return &kmsv1.ValidateReleaseResponse{Valid: len(out) == 0, Errors: out}, nil
}

// VerifyReleaseDefaults is the value-free defaults oracle. The wire request
// and response carry aliases, content types, caller hashes and bounded
// verdicts only; core enforces the operation, the per-identity budgets, and
// the audit record.
func (h *configurationReleaseServer) VerifyReleaseDefaults(ctx context.Context, req *kmsv1.VerifyReleaseDefaultsRequest) (*kmsv1.VerifyReleaseDefaultsResponse, error) {
	pr, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	entries := make([]domain.VerifyDefaultsEntry, 0, len(req.GetEntries()))
	for _, e := range req.GetEntries() {
		entries = append(entries, domain.VerifyDefaultsEntry{Alias: e.GetAlias(), ContentType: e.GetContentType(), SHA256: e.GetSha256()})
	}
	out, err := h.s.svc.VerifyReleaseDefaults(ctx, pr, domain.VerifyReleaseDefaultsInput{
		Namespace: nsRefFromProto(req.GetNamespace()), ReleaseName: req.GetName(),
		SchemaVersion: req.SchemaVersion, Profile: req.GetProfile(), SchemaSHA256: req.GetSchemaSha256(), Entries: entries,
	})
	if err != nil {
		return nil, h.s.mapErr(ctx, err)
	}
	return toProtoVerifyReleaseDefaults(out), nil
}

func toProtoReleaseValidationError(e domain.ReleaseValidationError) *kmsv1.ReleaseValidationError {
	return &kmsv1.ReleaseValidationError{Alias: e.Alias, Code: e.Code, SchemaPointer: e.SchemaPointer, Message: e.Message}
}

func (h *configurationReleaseServer) ActivateRelease(ctx context.Context, req *kmsv1.ActivateReleaseRequest) (*kmsv1.ActivateReleaseResponse, error) {
	if req.SchemaVersion == nil {
		return nil, h.s.mapErr(ctx, domain.Errorf(domain.ErrInvalidArgument, "schema_version is required"))
	}
	pr, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	var expected *uint64
	if req.ExpectedCurrentVersion != nil {
		v := req.GetExpectedCurrentVersion()
		expected = &v
	}
	active, changed, err := h.s.svc.ActivateConfigurationRelease(ctx, pr, domain.ReleaseTrack{Namespace: nsRefFromProto(req.GetNamespace()), Name: req.GetName(), SchemaVersion: req.GetSchemaVersion()}, req.GetVersion(), expected)
	if err != nil {
		return nil, h.s.mapErr(ctx, err)
	}
	return &kmsv1.ActivateReleaseResponse{Release: toProtoConfigurationRelease(active.Release), CurrentVersion: active.Release.Version, PreviousVersion: active.PreviousVersion, ActivationRevision: active.ActivationRevision, Changed: changed}, nil
}

func (h *configurationReleaseServer) GetRelease(ctx context.Context, req *kmsv1.GetReleaseRequest) (*kmsv1.GetReleaseResponse, error) {
	if req.SchemaVersion == nil {
		return nil, h.s.mapErr(ctx, domain.Errorf(domain.ErrInvalidArgument, "schema_version is required"))
	}
	pr, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	out, err := h.s.svc.GetConfigurationRelease(ctx, pr, domain.ReleaseTrack{Namespace: nsRefFromProto(req.GetNamespace()), Name: req.GetName(), SchemaVersion: req.GetSchemaVersion()}, req.GetVersion())
	if err != nil {
		return nil, h.s.mapErr(ctx, err)
	}
	return &kmsv1.GetReleaseResponse{Release: toProtoConfigurationRelease(out)}, nil
}
func (h *configurationReleaseServer) GetActiveRelease(ctx context.Context, req *kmsv1.GetActiveReleaseRequest) (*kmsv1.GetActiveReleaseResponse, error) {
	if req.SchemaVersion == nil {
		return nil, h.s.mapErr(ctx, domain.Errorf(domain.ErrInvalidArgument, "schema_version is required"))
	}
	pr, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	out, err := h.s.svc.GetActiveConfigurationRelease(ctx, pr, domain.ReleaseTrack{Namespace: nsRefFromProto(req.GetNamespace()), Name: req.GetName(), SchemaVersion: req.GetSchemaVersion()})
	if err != nil {
		return nil, h.s.mapErr(ctx, err)
	}
	return &kmsv1.GetActiveReleaseResponse{Release: toProtoConfigurationRelease(out.Release), ActivationRevision: out.ActivationRevision, PreviousVersion: out.PreviousVersion}, nil
}
func (h *configurationReleaseServer) ListReleases(ctx context.Context, req *kmsv1.ListReleasesRequest) (*kmsv1.ListReleasesResponse, error) {
	pr, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	rows, next, err := h.s.svc.ListConfigurationReleases(ctx, pr, domain.ReleaseFilter{Namespace: nsRefFromProto(req.GetNamespace()), Name: req.GetName(), SchemaVersion: req.SchemaVersion}, pageFrom(req.GetPageSize(), req.GetPageToken()))
	if err != nil {
		return nil, h.s.mapErr(ctx, err)
	}
	out := make([]*kmsv1.ReleaseSummary, 0, len(rows))
	for _, r := range rows {
		out = append(out, &kmsv1.ReleaseSummary{Release: toProtoConfigurationRelease(r.Release), Current: r.Current, Previous: r.Previous, ActivationRevision: r.ActivationRevision})
	}
	return &kmsv1.ListReleasesResponse{Releases: out, NextPageToken: next}, nil
}

func (h *configurationReleaseServer) WatchRelease(stream kmsv1.ConfigurationReleaseService_WatchReleaseServer) error {
	ctx := stream.Context()
	pr, err := requirePrincipal(ctx)
	if err != nil {
		return err
	}
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	regp := first.GetRegister()
	if regp == nil {
		return h.s.mapErr(ctx, domain.Errorf(domain.ErrInvalidArgument, "first watch message must register"))
	}
	if regp.SchemaVersion == nil {
		return h.s.mapErr(ctx, domain.Errorf(domain.ErrInvalidArgument, "schema_version is required"))
	}
	if regp.GetSessionId() != "" {
		return h.watchInstanceRelease(stream, pr, regp)
	}
	h.s.svc.RecordReleaseAcknowledgementOutcome("legacy_rejected")
	return h.s.mapErr(ctx, domain.Errorf(domain.ErrFailedPrecondition, "release watch upgrade required: use an updated SDK and register a release session"))
}

type configurationSchemaServer struct {
	kmsv1.UnimplementedConfigurationSchemaServiceServer
	s *Server
}

func (h *configurationSchemaServer) CreateSchema(ctx context.Context, req *kmsv1.CreateSchemaRequest) (*kmsv1.CreateSchemaResponse, error) {
	pr, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	out, err := h.s.svc.CreateConfigurationSchema(ctx, pr, req.GetApplication(), req.GetSchemaJson(), req.GetMetadataJson())
	if err != nil {
		return nil, h.s.mapErr(ctx, err)
	}
	return &kmsv1.CreateSchemaResponse{Schema: toProtoConfigurationSchema(out)}, nil
}
func (h *configurationSchemaServer) GetSchema(ctx context.Context, req *kmsv1.GetSchemaRequest) (*kmsv1.GetSchemaResponse, error) {
	pr, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	out, err := h.s.svc.GetConfigurationSchema(ctx, pr, req.GetApplication(), req.GetReleaseName(), req.GetVersion())
	if err != nil {
		return nil, h.s.mapErr(ctx, err)
	}
	return &kmsv1.GetSchemaResponse{Schema: toProtoConfigurationSchema(out)}, nil
}
func (h *configurationSchemaServer) ListSchemas(ctx context.Context, req *kmsv1.ListSchemasRequest) (*kmsv1.ListSchemasResponse, error) {
	pr, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	rows, next, err := h.s.svc.ListConfigurationSchemas(ctx, pr, req.GetApplication(), req.GetReleaseName(), pageFrom(req.GetPageSize(), req.GetPageToken()))
	if err != nil {
		return nil, h.s.mapErr(ctx, err)
	}
	out := make([]*kmsv1.ConfigurationSchema, 0, len(rows))
	for _, r := range rows {
		out = append(out, toProtoConfigurationSchema(r))
	}
	return &kmsv1.ListSchemasResponse{Schemas: out, NextPageToken: next}, nil
}

func (h *configurationReleaseServer) ResolveReleaseSchema(ctx context.Context, req *kmsv1.ResolveReleaseSchemaRequest) (*kmsv1.ResolveReleaseSchemaResponse, error) {
	pr, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	version, err := h.s.svc.ResolveReleaseSchema(ctx, pr, nsRefFromProto(req.GetNamespace()), req.GetName(), req.GetSchemaSha256())
	if err != nil {
		return nil, h.s.mapErr(ctx, err)
	}
	return &kmsv1.ResolveReleaseSchemaResponse{SchemaVersion: version}, nil
}
