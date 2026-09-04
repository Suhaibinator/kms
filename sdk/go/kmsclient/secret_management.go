package kmsclient

import (
	"context"
	"errors"
	"fmt"
	"maps"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
)

var errSecretMetadataRefMismatch = errors.New("kmsclient: secret metadata resource reference mismatch")

// SecretVersionInfo describes one exact secret version without exposing its
// plaintext. Bound and HasAccessToken are independent, immutable protection
// properties until the version is destroyed.
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

// SecretVersionTransitionResult reports a protection transition that cloned
// the previous current version into a new current version.
type SecretVersionTransitionResult struct {
	CurrentVersion  uint64
	PreviousVersion uint64
	Revision        uint64
}

// SecretVersionSetResult reports an exact set of secret versions selected by
// a preview or destroyed by a guarded purge.
type SecretVersionSetResult struct {
	AffectedVersions []uint64
	Revision         uint64
}

// SecretBindingCohortResult reports the contiguous binding-key cohort found or
// changed by a preview or purge.
type SecretBindingCohortResult struct {
	AnchorVersion    uint64
	AffectedVersions []uint64
	Revision         uint64
}

func transitionResult(resp *kmsv1.SecretVersionTransitionResponse) SecretVersionTransitionResult {
	if resp == nil {
		return SecretVersionTransitionResult{}
	}
	return SecretVersionTransitionResult{
		CurrentVersion:  resp.GetCurrentVersion(),
		PreviousVersion: resp.GetPreviousVersion(),
		Revision:        resp.GetRevision(),
	}
}

func versionSetResult(resp *kmsv1.SecretVersionSetResponse) SecretVersionSetResult {
	if resp == nil {
		return SecretVersionSetResult{}
	}
	return SecretVersionSetResult{
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
		return fmt.Errorf("%w: expected cohort revision must be positive", ErrInvalidArgument)
	}
	if len(expected.AffectedVersions) == 0 {
		return fmt.Errorf("%w: expected cohort affected versions must not be empty", ErrInvalidArgument)
	}
	for i, version := range expected.AffectedVersions {
		if version == 0 {
			return fmt.Errorf("%w: expected cohort affected versions must be positive", ErrInvalidArgument)
		}
		if i > 0 && version <= expected.AffectedVersions[i-1] {
			return fmt.Errorf("%w: expected cohort affected versions must be sorted and unique", ErrInvalidArgument)
		}
	}
	return nil
}

func validateVersionSetGuard(expected SecretVersionSetResult) error {
	if expected.Revision == 0 {
		return fmt.Errorf("%w: expected version-set revision must be positive", ErrInvalidArgument)
	}
	if len(expected.AffectedVersions) == 0 {
		return fmt.Errorf("%w: expected version-set affected versions must not be empty", ErrInvalidArgument)
	}
	for i, version := range expected.AffectedVersions {
		if version == 0 {
			return fmt.Errorf("%w: expected version-set affected versions must be positive", ErrInvalidArgument)
		}
		if i > 0 && version <= expected.AffectedVersions[i-1] {
			return fmt.Errorf("%w: expected version-set affected versions must be sorted and unique", ErrInvalidArgument)
		}
	}
	return nil
}

// BindSecret clones the current secret into a new bound version. The operation
// aborts unless expectedCurrentVersion is still labeled current.
func (c *Client) BindSecret(ctx context.Context, key string, expectedCurrentVersion uint64, bindingKey string) (SecretVersionTransitionResult, error) {
	if expectedCurrentVersion == 0 {
		return SecretVersionTransitionResult{}, fmt.Errorf("%w: expected current version must be positive", ErrInvalidArgument)
	}
	r, err := c.resolveRef(ctx, key)
	if err != nil {
		return SecretVersionTransitionResult{}, err
	}
	cctx, cancel := c.callCtx(ctx)
	defer cancel()
	resp, err := c.secrets.BindSecret(cctx, &kmsv1.BindSecretRequest{
		Ref: r.resourceProto(), ExpectedCurrentVersion: expectedCurrentVersion, BindingKey: bindingKey,
	})
	if err != nil {
		return SecretVersionTransitionResult{}, mapSecretError(err)
	}
	return transitionResult(resp), nil
}

// UnbindSecret clones the current secret into a new unbound version. The
// operation aborts unless expectedCurrentVersion is still labeled current.
func (c *Client) UnbindSecret(ctx context.Context, key string, expectedCurrentVersion uint64, bindingKey string) (SecretVersionTransitionResult, error) {
	if expectedCurrentVersion == 0 {
		return SecretVersionTransitionResult{}, fmt.Errorf("%w: expected current version must be positive", ErrInvalidArgument)
	}
	r, err := c.resolveRef(ctx, key)
	if err != nil {
		return SecretVersionTransitionResult{}, err
	}
	cctx, cancel := c.callCtx(ctx)
	defer cancel()
	resp, err := c.secrets.UnbindSecret(cctx, &kmsv1.UnbindSecretRequest{
		Ref: r.resourceProto(), ExpectedCurrentVersion: expectedCurrentVersion, BindingKey: bindingKey,
	})
	if err != nil {
		return SecretVersionTransitionResult{}, mapSecretError(err)
	}
	return transitionResult(resp), nil
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

// RotateSecretBindingKey clones the current bound secret into a new bound
// version protected by newBindingKey. Historical versions remain unchanged.
func (c *Client) RotateSecretBindingKey(ctx context.Context, key string, expectedCurrentVersion uint64, bindingKey, newBindingKey string) (SecretVersionTransitionResult, error) {
	if expectedCurrentVersion == 0 {
		return SecretVersionTransitionResult{}, fmt.Errorf("%w: expected current version must be positive", ErrInvalidArgument)
	}
	r, err := c.resolveRef(ctx, key)
	if err != nil {
		return SecretVersionTransitionResult{}, err
	}
	req := &kmsv1.RotateSecretBindingKeyRequest{
		Ref: r.resourceProto(), ExpectedCurrentVersion: expectedCurrentVersion, BindingKey: bindingKey, NewBindingKey: newBindingKey,
	}
	cctx, cancel := c.callCtx(ctx)
	defer cancel()
	resp, err := c.secrets.RotateSecretBindingKey(cctx, req)
	if err != nil {
		return SecretVersionTransitionResult{}, mapSecretError(err)
	}
	return transitionResult(resp), nil
}

// PreviewSecretUnboundVersions returns every non-destroyed unbound version of
// one secret. Pass the exact result to PurgeSecretUnboundVersions.
func (c *Client) PreviewSecretUnboundVersions(ctx context.Context, key string) (SecretVersionSetResult, error) {
	r, err := c.resolveRef(ctx, key)
	if err != nil {
		return SecretVersionSetResult{}, err
	}
	cctx, cancel := c.callCtx(ctx)
	defer cancel()
	resp, err := c.secrets.PreviewSecretUnboundVersions(cctx, &kmsv1.PreviewSecretUnboundVersionsRequest{Ref: r.resourceProto()})
	if err != nil {
		return SecretVersionSetResult{}, mapSecretError(err)
	}
	return versionSetResult(resp), nil
}

// PurgeSecretUnboundVersions irreversibly destroys the exact set returned by
// PreviewSecretUnboundVersions. The preview guard is mandatory. If the logical
// purge commits but physical database-artifact cleanup remains pending, the
// method returns a zero result and ErrPurgeCleanupPending; callers must not
// retry the purge.
func (c *Client) PurgeSecretUnboundVersions(ctx context.Context, key string, expected SecretVersionSetResult) (SecretVersionSetResult, error) {
	expected.AffectedVersions = append([]uint64(nil), expected.AffectedVersions...)
	if err := validateVersionSetGuard(expected); err != nil {
		return SecretVersionSetResult{}, err
	}
	r, err := c.resolveRef(ctx, key)
	if err != nil {
		return SecretVersionSetResult{}, err
	}
	cctx, cancel := c.callCtx(ctx)
	defer cancel()
	resp, err := c.secrets.PurgeSecretUnboundVersions(cctx, &kmsv1.PurgeSecretUnboundVersionsRequest{
		Ref: r.resourceProto(), ExpectedRevision: expected.Revision,
		ExpectedAffectedVersions: append([]uint64(nil), expected.AffectedVersions...),
	})
	if err != nil {
		return SecretVersionSetResult{}, mapPurgeSecretError(err)
	}
	return versionSetResult(resp), nil
}

// PurgeSecretBindingCohort irreversibly destroys the contiguous cohort around
// anchorVersion. anchorVersion 0 selects current.
// If the logical purge commits but physical database-artifact cleanup remains
// pending, the method returns a zero result and ErrPurgeCleanupPending; callers
// must not retry with the discarded binding key.
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
