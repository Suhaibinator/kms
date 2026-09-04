package kmsclient

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"unicode/utf8"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
)

var errSecretMetadataRefMismatch = errors.New("kmsclient: secret metadata resource reference mismatch")

// SecretVersionInfo describes one exact secret version without exposing its
// plaintext. Bound and HasAccessToken are independent live protection flags.
type SecretVersionInfo struct {
	Version           uint64
	State             string
	CreatedBy         string
	CreatedAtUnixMS   int64
	DestroyedAtUnixMS int64
	ExpiresAtUnixMS   int64
	MetadataJSON      string
	Bound             bool
	HasAccessToken    bool
}

// SecretMetadata describes a secret without exposing its plaintext. Bound
// summarizes the version selected by the current label; use Versions when
// making a decision about an exact pinned version.
type SecretMetadata struct {
	Path            string
	ContentType     string
	Bound           bool
	HasAccessToken  bool
	MetadataJSON    string
	CreatedAtUnixMS int64
	UpdatedAtUnixMS int64
	Labels          map[string]uint64
	Versions        []SecretVersionInfo
}

// GetSecretMetadata returns live metadata for every retained version.
func (c *Client) GetSecretMetadata(ctx context.Context, key string) (SecretMetadata, error) {
	r, err := c.resolveRef(ctx, key)
	if err != nil {
		return SecretMetadata{}, err
	}
	cctx, cancel := c.callCtx(ctx)
	defer cancel()
	resp, err := c.secrets.GetSecretMetadata(cctx, &kmsv1.GetSecretMetadataRequest{Ref: r.resourceProto()})
	if err != nil {
		return SecretMetadata{}, mapError(err)
	}
	secret := resp.GetSecret()
	if secret == nil || secret.GetRef() == nil || secret.GetRef().GetNamespace() == nil {
		return SecretMetadata{}, errors.New("kmsclient: empty secret metadata response")
	}
	if refFromProto(secret.GetRef()) != r {
		return SecretMetadata{}, errSecretMetadataRefMismatch
	}
	return secretMetadataFromProto(secret, r.display()), nil
}

func secretMetadataFromProto(secret *kmsv1.SecretMetadata, fallbackPath string) SecretMetadata {
	if secret == nil {
		return SecretMetadata{}
	}
	path := fallbackPath
	if secret.GetRef() != nil {
		path = refFromProto(secret.GetRef()).display()
	}
	out := SecretMetadata{
		Path:            path,
		ContentType:     secret.GetContentType(),
		Bound:           secret.GetBound(),
		HasAccessToken:  secret.GetHasAccessToken(),
		MetadataJSON:    secret.GetMetadataJson(),
		CreatedAtUnixMS: secret.GetCreatedAtUnixMs(),
		UpdatedAtUnixMS: secret.GetUpdatedAtUnixMs(),
		Labels:          maps.Clone(secret.GetLabels()),
		Versions:        make([]SecretVersionInfo, 0, len(secret.GetVersions())),
	}
	for _, version := range secret.GetVersions() {
		if version == nil {
			continue
		}
		out.Versions = append(out.Versions, SecretVersionInfo{
			Version:           version.GetVersion(),
			State:             version.GetState(),
			CreatedBy:         version.GetCreatedBy(),
			CreatedAtUnixMS:   version.GetCreatedAtUnixMs(),
			DestroyedAtUnixMS: version.GetDestroyedAtUnixMs(),
			ExpiresAtUnixMS:   version.GetExpiresAtUnixMs(),
			MetadataJSON:      version.GetMetadataJson(),
			Bound:             version.GetBound(),
			HasAccessToken:    version.GetHasAccessToken(),
		})
	}
	return out
}

// SecretVersionMutationResult reports an exact-version bind or unbind.
type SecretVersionMutationResult struct {
	AnchorVersion    uint64
	AffectedVersions []uint64
	Revision         uint64
}

// SecretBindingCohortResult reports the contiguous binding-key cohort found or
// changed by a preview, rotation, or purge.
type SecretBindingCohortResult struct {
	AnchorVersion    uint64
	AffectedVersions []uint64
	Revision         uint64
}

func versionMutationResult(resp *kmsv1.SecretVersionMutationResponse) SecretVersionMutationResult {
	if resp == nil {
		return SecretVersionMutationResult{}
	}
	return SecretVersionMutationResult{
		AnchorVersion:    resp.GetAnchorVersion(),
		AffectedVersions: append([]uint64(nil), resp.GetAffectedVersions()...),
		Revision:         resp.GetRevision(),
	}
}

func cohortResult(resp *kmsv1.SecretBindingCohortResponse) SecretBindingCohortResult {
	if resp == nil {
		return SecretBindingCohortResult{}
	}
	return SecretBindingCohortResult{
		AnchorVersion:    resp.GetAnchorVersion(),
		AffectedVersions: append([]uint64(nil), resp.GetAffectedVersions()...),
		Revision:         resp.GetRevision(),
	}
}

func validateCohortGuard(expected SecretBindingCohortResult) error {
	if expected.Revision == 0 {
		return errors.New("kmsclient: expected cohort revision must be positive")
	}
	if len(expected.AffectedVersions) == 0 {
		return errors.New("kmsclient: expected cohort affected versions must not be empty")
	}
	for i, version := range expected.AffectedVersions {
		if version == 0 {
			return errors.New("kmsclient: expected cohort affected versions must be positive")
		}
		if i > 0 && version <= expected.AffectedVersions[i-1] {
			return errors.New("kmsclient: expected cohort affected versions must be sorted and unique")
		}
	}
	return nil
}

// BindSecret adds binding-key protection to one exact version in place.
// version 0 selects the version labeled current.
func (c *Client) BindSecret(ctx context.Context, key string, version uint64, bindingKey string) (SecretVersionMutationResult, error) {
	r, err := c.resolveRef(ctx, key)
	if err != nil {
		return SecretVersionMutationResult{}, err
	}
	cctx, cancel := c.callCtx(ctx)
	defer cancel()
	resp, err := c.secrets.BindSecret(cctx, &kmsv1.BindSecretRequest{
		Ref: r.resourceProto(), Version: version, BindingKey: bindingKey,
	})
	if err != nil {
		return SecretVersionMutationResult{}, mapSecretError(err)
	}
	return versionMutationResult(resp), nil
}

// UnbindSecret removes binding-key protection from one exact version in place.
// version 0 selects the version labeled current.
func (c *Client) UnbindSecret(ctx context.Context, key string, version uint64, bindingKey string) (SecretVersionMutationResult, error) {
	r, err := c.resolveRef(ctx, key)
	if err != nil {
		return SecretVersionMutationResult{}, err
	}
	cctx, cancel := c.callCtx(ctx)
	defer cancel()
	resp, err := c.secrets.UnbindSecret(cctx, &kmsv1.UnbindSecretRequest{
		Ref: r.resourceProto(), Version: version, BindingKey: bindingKey,
	})
	if err != nil {
		return SecretVersionMutationResult{}, mapSecretError(err)
	}
	return versionMutationResult(resp), nil
}

// PreviewSecretBindingCohort returns the contiguous cohort around
// anchorVersion without changing it. anchorVersion 0 selects current.
func (c *Client) PreviewSecretBindingCohort(ctx context.Context, key string, anchorVersion uint64, bindingKey string) (SecretBindingCohortResult, error) {
	r, err := c.resolveRef(ctx, key)
	if err != nil {
		return SecretBindingCohortResult{}, err
	}
	cctx, cancel := c.callCtx(ctx)
	defer cancel()
	resp, err := c.secrets.PreviewSecretBindingCohort(cctx, &kmsv1.PreviewSecretBindingCohortRequest{
		Ref: r.resourceProto(), AnchorVersion: anchorVersion, BindingKey: bindingKey,
	})
	if err != nil {
		return SecretBindingCohortResult{}, mapSecretError(err)
	}
	return cohortResult(resp), nil
}

// RotateSecretBindingKey rewraps the contiguous cohort around anchorVersion.
// anchorVersion 0 selects current.
func (c *Client) RotateSecretBindingKey(ctx context.Context, key string, anchorVersion uint64, bindingKey, newBindingKey string) (SecretBindingCohortResult, error) {
	return c.rotateSecretBindingKey(ctx, key, anchorVersion, bindingKey, newBindingKey, nil)
}

// RotateSecretBindingKeyIfUnchanged performs the rotation only if the live
// cohort still matches a prior PreviewSecretBindingCohort result.
func (c *Client) RotateSecretBindingKeyIfUnchanged(ctx context.Context, key string, anchorVersion uint64, bindingKey, newBindingKey string, expected SecretBindingCohortResult) (SecretBindingCohortResult, error) {
	expected.AffectedVersions = append([]uint64(nil), expected.AffectedVersions...)
	if err := validateCohortGuard(expected); err != nil {
		return SecretBindingCohortResult{}, err
	}
	return c.rotateSecretBindingKey(ctx, key, anchorVersion, bindingKey, newBindingKey, &expected)
}

func (c *Client) rotateSecretBindingKey(ctx context.Context, key string, anchorVersion uint64, bindingKey, newBindingKey string, expected *SecretBindingCohortResult) (SecretBindingCohortResult, error) {
	if len(newBindingKey) >= 32 && utf8.ValidString(newBindingKey) && bindingKey == newBindingKey {
		return SecretBindingCohortResult{}, fmt.Errorf("%w: new binding key must differ from current binding key", ErrInvalidArgument)
	}
	r, err := c.resolveRef(ctx, key)
	if err != nil {
		return SecretBindingCohortResult{}, err
	}
	req := &kmsv1.RotateSecretBindingKeyRequest{
		Ref: r.resourceProto(), AnchorVersion: anchorVersion, BindingKey: bindingKey, NewBindingKey: newBindingKey,
	}
	if expected != nil {
		revision := expected.Revision
		req.ExpectedRevision = &revision
		req.ExpectedAffectedVersions = append([]uint64(nil), expected.AffectedVersions...)
	}
	cctx, cancel := c.callCtx(ctx)
	defer cancel()
	resp, err := c.secrets.RotateSecretBindingKey(cctx, req)
	if err != nil {
		return SecretBindingCohortResult{}, mapSecretError(err)
	}
	return cohortResult(resp), nil
}

// PurgeSecretBindingCohort irreversibly destroys the contiguous cohort around
// anchorVersion. anchorVersion 0 selects current. If the logical purge commits
// but physical database-artifact cleanup remains pending, the method returns a
// zero result and ErrPurgeCleanupPending; callers must not retry with the
// discarded binding key.
func (c *Client) PurgeSecretBindingCohort(ctx context.Context, key string, anchorVersion uint64, bindingKey string) (SecretBindingCohortResult, error) {
	return c.purgeSecretBindingCohort(ctx, key, anchorVersion, bindingKey, nil)
}

// PurgeSecretBindingCohortIfUnchanged performs the purge only if the live
// cohort still matches a prior PreviewSecretBindingCohort result. It has the
// same zero-result ErrPurgeCleanupPending contract as PurgeSecretBindingCohort.
func (c *Client) PurgeSecretBindingCohortIfUnchanged(ctx context.Context, key string, anchorVersion uint64, bindingKey string, expected SecretBindingCohortResult) (SecretBindingCohortResult, error) {
	expected.AffectedVersions = append([]uint64(nil), expected.AffectedVersions...)
	if err := validateCohortGuard(expected); err != nil {
		return SecretBindingCohortResult{}, err
	}
	return c.purgeSecretBindingCohort(ctx, key, anchorVersion, bindingKey, &expected)
}

func (c *Client) purgeSecretBindingCohort(ctx context.Context, key string, anchorVersion uint64, bindingKey string, expected *SecretBindingCohortResult) (SecretBindingCohortResult, error) {
	r, err := c.resolveRef(ctx, key)
	if err != nil {
		return SecretBindingCohortResult{}, err
	}
	req := &kmsv1.PurgeSecretBindingCohortRequest{
		Ref: r.resourceProto(), AnchorVersion: anchorVersion, BindingKey: bindingKey,
	}
	if expected != nil {
		revision := expected.Revision
		req.ExpectedRevision = &revision
		req.ExpectedAffectedVersions = append([]uint64(nil), expected.AffectedVersions...)
	}
	cctx, cancel := c.callCtx(ctx)
	defer cancel()
	resp, err := c.secrets.PurgeSecretBindingCohort(cctx, req)
	if err != nil {
		return SecretBindingCohortResult{}, mapPurgeSecretError(err)
	}
	return cohortResult(resp), nil
}
