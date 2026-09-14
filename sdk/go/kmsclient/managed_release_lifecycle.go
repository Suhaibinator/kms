package kmsclient

import (
	"context"
	"fmt"
	"strings"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
)

// ManagedReleaseTarget identifies an immutable release on an explicit schema track.
// SchemaVersion may be zero for the schema-free track.
type ManagedReleaseTarget struct {
	Namespace     string
	Name          string
	SchemaVersion uint64
	Version       uint64
}

// ManagedReleaseValidation contains value-free schema validation results.
type ManagedReleaseValidation struct {
	Valid  bool
	Errors []ApplicationReleaseValidationError
}

// ManagedReleaseActivation describes the active immutable release without resolving entries.
type ManagedReleaseActivation struct {
	Version            uint64
	PreviousVersion    uint64
	ActivationRevision uint64
	Changed            bool
}

func (target ManagedReleaseTarget) namespace(requireVersion bool) (namespaceRef, error) {
	ns, err := parseNamespace(target.Namespace)
	if err != nil {
		return namespaceRef{}, err
	}
	if target.Name == "" || strings.TrimSpace(target.Name) != target.Name || strings.ContainsAny(target.Name, "/\\\x00\r\n") || requireVersion && target.Version == 0 {
		return namespaceRef{}, fmt.Errorf("kmsclient: release name and positive version are required")
	}
	return ns, nil
}

// ValidateManagedRelease validates pinned resources server-side; it never resolves values.
func (c *Client) ValidateManagedRelease(ctx context.Context, target ManagedReleaseTarget) (ManagedReleaseValidation, error) {
	ns, err := target.namespace(true)
	if err != nil {
		return ManagedReleaseValidation{}, err
	}
	ctx, cancel := c.callCtx(ctx)
	defer cancel()
	response, err := c.releases.ValidateRelease(ctx, &kmsv1.ValidateReleaseRequest{Namespace: ns.proto(), Name: target.Name, Version: target.Version, SchemaVersion: &target.SchemaVersion})
	if err != nil {
		return ManagedReleaseValidation{}, mapError(err)
	}
	if response == nil || response.GetValid() != (len(response.GetErrors()) == 0) {
		return ManagedReleaseValidation{}, fmt.Errorf("kmsclient: invalid release validation response")
	}
	result := ManagedReleaseValidation{Valid: response.GetValid()}
	for _, item := range response.GetErrors() {
		if item == nil || item.GetCode() == "" {
			return ManagedReleaseValidation{}, fmt.Errorf("kmsclient: invalid release validation error")
		}
		result.Errors = append(result.Errors, ApplicationReleaseValidationError{Alias: item.GetAlias(), Code: item.GetCode(), SchemaPointer: item.GetSchemaPointer(), Message: item.GetMessage()})
	}
	return result, nil
}

// GetManagedReleaseActivation reads active manifest metadata without resolving any entries.
// A track with no active release returns ErrNotFound.
func (c *Client) GetManagedReleaseActivation(ctx context.Context, target ManagedReleaseTarget) (ManagedReleaseActivation, error) {
	ns, err := target.namespace(false)
	if err != nil {
		return ManagedReleaseActivation{}, err
	}
	ctx, cancel := c.callCtx(ctx)
	defer cancel()
	response, err := c.releases.GetActiveRelease(ctx, &kmsv1.GetActiveReleaseRequest{Namespace: ns.proto(), Name: target.Name, SchemaVersion: &target.SchemaVersion})
	if err != nil {
		return ManagedReleaseActivation{}, mapError(err)
	}
	if response == nil || response.GetRelease() == nil || response.GetActivationRevision() == 0 {
		return ManagedReleaseActivation{}, fmt.Errorf("kmsclient: invalid active release response")
	}
	if _, err := validateCreatedApplicationRelease(response.GetRelease(), ns, target.Name, target.SchemaVersion); err != nil {
		return ManagedReleaseActivation{}, err
	}
	return ManagedReleaseActivation{Version: response.GetRelease().GetVersion(), PreviousVersion: response.GetPreviousVersion(), ActivationRevision: response.GetActivationRevision()}, nil
}

// ActivateManagedRelease activates using the server's current-version CAS guard.
// expectedCurrentVersion zero explicitly requires no active release on the track.
func (c *Client) ActivateManagedRelease(ctx context.Context, target ManagedReleaseTarget, expectedCurrentVersion uint64) (ManagedReleaseActivation, error) {
	ns, err := target.namespace(true)
	if err != nil {
		return ManagedReleaseActivation{}, err
	}
	ctx, cancel := c.callCtx(ctx)
	defer cancel()
	response, err := c.releases.ActivateRelease(ctx, &kmsv1.ActivateReleaseRequest{Namespace: ns.proto(), Name: target.Name, Version: target.Version, SchemaVersion: &target.SchemaVersion, ExpectedCurrentVersion: &expectedCurrentVersion})
	if err != nil {
		return ManagedReleaseActivation{}, mapError(err)
	}
	if response == nil || response.GetRelease() == nil || response.GetCurrentVersion() != target.Version || response.GetRelease().GetVersion() != target.Version || response.GetActivationRevision() == 0 || response.GetChanged() != (expectedCurrentVersion != target.Version) || response.GetChanged() && response.GetPreviousVersion() != expectedCurrentVersion {
		return ManagedReleaseActivation{}, fmt.Errorf("kmsclient: invalid release activation response")
	}
	if _, err := validateCreatedApplicationRelease(response.GetRelease(), ns, target.Name, target.SchemaVersion); err != nil {
		return ManagedReleaseActivation{}, err
	}
	return ManagedReleaseActivation{Version: response.GetCurrentVersion(), PreviousVersion: response.GetPreviousVersion(), ActivationRevision: response.GetActivationRevision(), Changed: response.GetChanged()}, nil
}
