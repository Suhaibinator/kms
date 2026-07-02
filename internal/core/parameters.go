package core

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/pathutil"
	"github.com/Suhaibinator/kms/internal/policy"
	"github.com/Suhaibinator/kms/internal/storage"
)

var validParameterContentTypes = map[string]bool{
	"string": true, "integer": true, "float": true,
	"boolean": true, "json": true, "binary": true,
}

// validateParameterValue checks that value parses as its declared type, so a
// typo can't push an unparseable value to every subscribed application.
func validateParameterValue(value, contentType string) error {
	var err error
	switch contentType {
	case "integer":
		_, err = strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	case "float":
		_, err = strconv.ParseFloat(strings.TrimSpace(value), 64)
	case "boolean":
		_, err = strconv.ParseBool(strings.TrimSpace(value))
	case "json":
		if !json.Valid([]byte(value)) {
			err = errors.New("invalid JSON")
		}
	case "binary":
		_, err = base64.StdEncoding.DecodeString(value)
	}
	if err != nil {
		return domain.Errorf(domain.ErrInvalidArgument, "value does not parse as %s", contentType)
	}
	return nil
}

// GetParameter resolves a parameter at a version (>0) or label ("" = current).
func (s *Service) GetParameter(ctx context.Context, pr Principal, path string, version uint64, label string) (domain.Parameter, error) {
	path, err := pathutil.Normalize(path)
	if err != nil {
		return domain.Parameter{}, domain.Errorf(domain.ErrInvalidArgument, "%v", err)
	}
	if err := s.authorize(ctx, pr, domain.OpParameterRead, domain.ResourceParameter, path); err != nil {
		return domain.Parameter{}, err
	}
	return s.store.GetParameter(ctx, path, version, label)
}

// PutParameter writes a new immutable version and moves the current label.
func (s *Service) PutParameter(ctx context.Context, pr Principal, path, value, contentType, metadata string) (version, revision uint64, err error) {
	path, err = pathutil.Normalize(path)
	if err != nil {
		return 0, 0, domain.Errorf(domain.ErrInvalidArgument, "%v", err)
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
	if err := s.authorize(ctx, pr, domain.OpParameterWrite, domain.ResourceParameter, path); err != nil {
		return 0, 0, err
	}
	version, revision, err = s.store.PutParameter(ctx, path, value, contentType, metadata, pr.Identity.Name)
	if err != nil {
		return 0, 0, err
	}
	s.auditOp(ctx, pr, "parameter.write", domain.ResourceParameter, path, version, "allow", nil)
	s.getHub().Wake()
	return version, revision, nil
}

// ListParameters lists current-labeled parameters under a prefix, filtered to
// what the principal may read.
func (s *Service) ListParameters(ctx context.Context, pr Principal, prefix string, page storage.ListPage) ([]domain.Parameter, string, error) {
	prefix, err := normalizeListPrefix(prefix)
	if err != nil {
		return nil, "", err
	}
	// Parameter list responses carry values inline, so an item is only
	// exposed when the caller may read it (parameter:list authorizes the
	// enumeration itself, not value disclosure).
	filter, err := s.listFilter(ctx, pr, domain.ResourceParameter, domain.OpParameterList, prefix, domain.OpParameterRead)
	if err != nil {
		return nil, "", err
	}
	params, next, err := s.store.ListParameters(ctx, prefix, page)
	if err != nil {
		return nil, "", err
	}
	out := params[:0]
	for _, p := range params {
		if filter(p.Path) {
			out = append(out, p)
		}
	}
	return out, next, nil
}

// GetParameterInfo returns parameter metadata and version history.
func (s *Service) GetParameterInfo(ctx context.Context, pr Principal, path string) (domain.ParameterInfo, error) {
	path, err := pathutil.Normalize(path)
	if err != nil {
		return domain.ParameterInfo{}, domain.Errorf(domain.ErrInvalidArgument, "%v", err)
	}
	if err := s.authorize(ctx, pr, domain.OpParameterRead, domain.ResourceParameter, path); err != nil {
		return domain.ParameterInfo{}, err
	}
	return s.store.GetParameterInfo(ctx, path)
}

// DeleteParameter removes a parameter and all its versions.
func (s *Service) DeleteParameter(ctx context.Context, pr Principal, path string) (uint64, error) {
	path, err := pathutil.Normalize(path)
	if err != nil {
		return 0, domain.Errorf(domain.ErrInvalidArgument, "%v", err)
	}
	if err := s.authorize(ctx, pr, domain.OpParameterDelete, domain.ResourceParameter, path); err != nil {
		return 0, err
	}
	revision, err := s.store.DeleteParameter(ctx, path)
	if err != nil {
		return 0, err
	}
	s.auditOp(ctx, pr, "parameter.delete", domain.ResourceParameter, path, 0, "allow", nil)
	s.getHub().Wake()
	return revision, nil
}

// normalizeListPrefix accepts "", "/", or a canonical path as a list root.
func normalizeListPrefix(prefix string) (string, error) {
	if prefix == "" || prefix == "/" {
		return "", nil
	}
	p, err := pathutil.Normalize(prefix)
	if err != nil {
		return "", domain.Errorf(domain.ErrInvalidArgument, "%v", err)
	}
	return p, nil
}

// listFilter authorizes a list over the prefix subtree and returns a per-item
// predicate. Enumerating the namespace requires the list operation (listOp);
// each individual item is then included only if the caller is authorized for
// one of itemOps on that item's path. Admins pass everything through.
//
// Separating the two levels is what closes the "list reveals what read denies"
// gap: parameter listings return values inline, so they pass only
// parameter:read as the item op — holding parameter:list lets a caller
// enumerate a namespace but never see the value of a path whose read is denied.
// Secret listings return metadata only, so they pass secret:list and
// secret:read (metadata visibility tracks either).
func (s *Service) listFilter(ctx context.Context, pr Principal, resourceType, listOp, prefix string, itemOps ...string) (func(string) bool, error) {
	if pr.IsAdmin() {
		return func(string) bool { return true }, nil
	}
	policies, err := s.store.PoliciesForSubject(ctx, pr.Identity.Name)
	if err != nil {
		return nil, domain.Errorf(domain.ErrPermissionDenied, "authorization unavailable")
	}
	if !policy.MayListUnder(policies, listOp, prefix) {
		s.auditOp(ctx, pr, "authz.denial", resourceType, prefix, 0, "deny",
			map[string]string{"operation": listOp})
		return nil, domain.Errorf(domain.ErrPermissionDenied, "access denied")
	}
	return func(path string) bool {
		for _, op := range itemOps {
			if policy.Evaluate(policies, op, path) {
				return true
			}
		}
		return false
	}, nil
}
