package core

import (
	"context"
	"encoding/base64"
	"encoding/json/v2"
	"errors"
	"strconv"
	"strings"

	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/storage"
)

var validParameterContentTypes = map[string]bool{
	"string": true, "integer": true, "float": true,
	"boolean": true, "json": true, "binary": true,
}

// validateParameterValue checks that value parses as its declared type, so a
// typo can't push an unparseable value to every subscribed application.
func validateParameterValue(value, contentType string) error {
	_, err := parseParameterValue(value, contentType)
	if err != nil {
		return domain.Errorf(domain.ErrInvalidArgument, "value does not parse as %s", contentType)
	}
	return nil
}

// parseParameterValue is the backward-compatible content-type conversion used
// when writing parameters. Managed release construction applies its stricter
// JSON duplicate-property check in parameterSchemaValue. Binary values remain
// represented by their base64 text after the encoding has been validated,
// because JSON Schema has no byte-string type.
func parseParameterValue(value, contentType string) (any, error) {
	switch contentType {
	case "string":
		return value, nil
	case "integer":
		return strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	case "float":
		return strconv.ParseFloat(strings.TrimSpace(value), 64)
	case "boolean":
		return strconv.ParseBool(strings.TrimSpace(value))
	case "json":
		var decoded any
		if err := json.Unmarshal([]byte(value), &decoded); err != nil {
			return nil, err
		}
		return decoded, nil
	case "binary":
		if _, err := base64.StdEncoding.DecodeString(value); err != nil {
			return nil, err
		}
		return value, nil
	default:
		return nil, errors.New("unsupported content type")
	}
}

// GetParameter resolves a parameter at a version (>0) or label ("" = current).
func (s *Service) GetParameter(ctx context.Context, pr Principal, ref domain.Ref, version uint64, label string) (domain.Parameter, error) {
	if err := validateRef(ref); err != nil {
		return domain.Parameter{}, err
	}
	ctx, _, err := s.authorize(ctx, pr, domain.OpParameterRead, domain.ResourceParameter, ref)
	if err != nil {
		return domain.Parameter{}, err
	}
	return s.store.GetParameter(ctx, ref, version, label)
}

// PutParameter writes a new immutable version and moves the current label.
func (s *Service) PutParameter(ctx context.Context, pr Principal, ref domain.Ref, value, contentType, metadata string) (version, revision uint64, err error) {
	if err := validateRef(ref); err != nil {
		return 0, 0, err
	}
	if len(value) > maxValueBytes {
		return 0, 0, domain.Errorf(domain.ErrInvalidArgument, "value exceeds %d bytes", maxValueBytes)
	}
	if contentType == "" {
		contentType = "string"
	}
	if !validParameterContentTypes[contentType] {
		return 0, 0, domain.Errorf(domain.ErrInvalidArgument, "unknown content type %q", contentType)
	}
	if err := validateParameterValue(value, contentType); err != nil {
		return 0, 0, err
	}
	if metadata, err = validateMetadataJSON(metadata); err != nil {
		return 0, 0, err
	}
	ctx, namespace, err := s.authorize(ctx, pr, domain.OpParameterWrite, domain.ResourceParameter, ref)
	if err != nil {
		return 0, 0, err
	}
	version, revision, err = s.store.PutParameter(ctx, ref, value, contentType, metadata, pr.Identity.Name)
	if err != nil {
		return 0, 0, err
	}
	s.auditRefWithNamespaceID(ctx, pr, "parameter.write", domain.ResourceParameter, ref, namespace.ID, version, "allow", nil)
	s.getHub().Wake()
	return version, revision, nil
}

// ListParameters lists current-labeled parameters in a namespace under a key
// prefix, filtered to what the principal may read.
func (s *Service) ListParameters(ctx context.Context, pr Principal, ns domain.NamespaceRef, keyPrefix string, page storage.ListPage) ([]domain.Parameter, string, error) {
	if err := validateListScope(ns, keyPrefix); err != nil {
		return nil, "", err
	}
	// Parameter list responses carry values inline, so an item is only exposed
	// when the caller may read it (parameter:list authorizes the enumeration
	// itself, not value disclosure).
	ctx, _, filter, err := s.listFilter(ctx, pr, domain.ResourceParameter, domain.OpParameterList, ns, domain.OpParameterRead)
	if err != nil {
		return nil, "", err
	}
	params, next, err := s.store.ListParameters(ctx, ns, keyPrefix, page)
	if err != nil {
		return nil, "", err
	}
	out := params[:0]
	for _, p := range params {
		if filter(p.Ref) {
			out = append(out, p)
		}
	}
	return out, next, nil
}

// GetParameterInfo returns parameter metadata and version history.
func (s *Service) GetParameterInfo(ctx context.Context, pr Principal, ref domain.Ref) (domain.ParameterInfo, error) {
	if err := validateRef(ref); err != nil {
		return domain.ParameterInfo{}, err
	}
	ctx, _, err := s.authorize(ctx, pr, domain.OpParameterRead, domain.ResourceParameter, ref)
	if err != nil {
		return domain.ParameterInfo{}, err
	}
	return s.store.GetParameterInfo(ctx, ref)
}

// DeleteParameter removes a parameter and all its versions.
func (s *Service) DeleteParameter(ctx context.Context, pr Principal, ref domain.Ref) (uint64, error) {
	if err := validateRef(ref); err != nil {
		return 0, err
	}
	ctx, namespace, err := s.authorize(ctx, pr, domain.OpParameterDelete, domain.ResourceParameter, ref)
	if err != nil {
		return 0, err
	}
	revision, err := s.store.DeleteParameter(ctx, ref)
	if err != nil {
		if errors.Is(err, domain.ErrFailedPrecondition) {
			s.auditProtectedReleaseReference(ctx, pr, ref, namespace.ID, domain.ReleaseEntryParameter, 0, "delete")
		}
		return 0, err
	}
	s.auditRefWithNamespaceID(ctx, pr, "parameter.delete", domain.ResourceParameter, ref, namespace.ID, 0, "allow", nil)
	s.getHub().Wake()
	return revision, nil
}
