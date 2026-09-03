package kmsclient

import (
	"context"
	"fmt"
	"strings"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
)

const (
	ApplicationReleaseSourceGeneratedDefault      = "generated_default"
	ApplicationReleaseSourceCarriedActiveSecret   = "carried_active_secret"
	ApplicationReleaseSourceResolvedCurrentSecret = "resolved_current_secret"
)

// CreateApplicationReleaseOptions describes one preview or execution of an
// immutable application release assembled from a generated defaults artifact.
// Execute requests must carry the plan digest returned by a fresh preview.
type CreateApplicationReleaseOptions struct {
	Namespace    string
	Artifact     []byte
	MetadataJSON string
	Execute      bool
	PlanDigest   string
}

// ApplicationReleasePlanEntry describes one value-free resource pin selected
// for the proposed release. It never contains parameter values, secret
// plaintext, access tokens, or resource metadata.
type ApplicationReleasePlanEntry struct {
	Alias       string
	Kind        string
	Path        string
	FromVersion uint64
	ToVersion   uint64
	Source      string
}

// ApplicationReleaseValidationError is a sanitized validation result returned
// by KMS. Message is bounded server text and must never contain resource values.
type ApplicationReleaseValidationError struct {
	Alias         string
	Code          string
	SchemaPointer string
	Message       string
}

// CreateApplicationReleaseResult is the validated, value-free response for a
// release preview or execution. Release is present when KMS returns the created
// or already-identical immutable manifest; it is never activated by this API.
type CreateApplicationReleaseResult struct {
	Profile            string
	PlanDigest         string
	Valid              bool
	Executed           bool
	Created            bool
	ReleaseName        string
	SchemaVersion      uint64
	BaseReleaseVersion uint64
	Entries            []ApplicationReleasePlanEntry
	MissingSecrets     []string
	Validation         []ApplicationReleaseValidationError
	Release            *ReleaseManifest
}

// CreateApplicationRelease previews or creates an immutable application
// release from a generated defaults artifact. It does not activate the release.
func (c *Client) CreateApplicationRelease(
	ctx context.Context,
	options CreateApplicationReleaseOptions,
) (CreateApplicationReleaseResult, error) {
	namespace, err := parseNamespace(options.Namespace)
	if err != nil {
		return CreateApplicationReleaseResult{}, err
	}
	if len(options.Artifact) == 0 {
		return CreateApplicationReleaseResult{}, fmt.Errorf("kmsclient: defaults artifact is required")
	}
	if options.Execute {
		if !validCanonicalSHA256Hex(options.PlanDigest) {
			return CreateApplicationReleaseResult{}, fmt.Errorf("kmsclient: execute requires a valid preview plan digest")
		}
	} else if options.PlanDigest != "" {
		return CreateApplicationReleaseResult{}, fmt.Errorf("kmsclient: preview must not include a plan digest")
	}

	cctx, cancel := c.callCtx(ctx, "")
	defer cancel()
	response, err := c.admin.CreateApplicationRelease(cctx, &kmsv1.CreateApplicationReleaseRequest{
		Namespace:    namespace.proto(),
		Artifact:     options.Artifact,
		MetadataJson: options.MetadataJSON,
		Execute:      options.Execute,
		PlanDigest:   options.PlanDigest,
	})
	if err != nil {
		return CreateApplicationReleaseResult{}, mapError(err)
	}
	if response == nil || response.GetProfile() == "" || response.GetReleaseName() == "" || !validCanonicalSHA256Hex(response.GetPlanDigest()) {
		return CreateApplicationReleaseResult{}, fmt.Errorf("kmsclient: invalid application release response")
	}
	if response.GetExecuted() != options.Execute || response.GetCreated() && !response.GetExecuted() {
		return CreateApplicationReleaseResult{}, fmt.Errorf("kmsclient: application release response execution state mismatch")
	}
	if options.Execute && response.GetPlanDigest() != options.PlanDigest {
		return CreateApplicationReleaseResult{}, fmt.Errorf("kmsclient: application release response plan digest mismatch")
	}
	if response.GetExecuted() && !response.GetValid() {
		return CreateApplicationReleaseResult{}, fmt.Errorf("kmsclient: application release response executed an invalid plan")
	}
	if response.GetExecuted() && response.GetRelease() == nil {
		return CreateApplicationReleaseResult{}, fmt.Errorf("kmsclient: application release response omitted the executed release")
	}
	if !response.GetExecuted() && response.GetRelease() != nil {
		return CreateApplicationReleaseResult{}, fmt.Errorf("kmsclient: application release preview unexpectedly included a release")
	}

	result := CreateApplicationReleaseResult{
		Profile: response.GetProfile(), PlanDigest: response.GetPlanDigest(), Valid: response.GetValid(),
		Executed: response.GetExecuted(), Created: response.GetCreated(), ReleaseName: response.GetReleaseName(),
		SchemaVersion: response.GetSchemaVersion(), BaseReleaseVersion: response.GetBaseReleaseVersion(),
		Entries:        make([]ApplicationReleasePlanEntry, 0, len(response.GetEntries())),
		MissingSecrets: append([]string(nil), response.GetMissingSecrets()...),
		Validation:     make([]ApplicationReleaseValidationError, 0, len(response.GetValidation())),
	}

	aliases := make(map[string]struct{}, len(response.GetEntries()))
	for index, entry := range response.GetEntries() {
		if entry == nil || entry.GetAlias() == "" || entry.GetRef() == nil || entry.GetRef().GetNamespace() == nil || entry.GetRef().GetKey() == "" {
			return CreateApplicationReleaseResult{}, fmt.Errorf("kmsclient: application release response entry %d is incomplete", index)
		}
		if entry.GetKind() != "parameter" && entry.GetKind() != "secret" {
			return CreateApplicationReleaseResult{}, fmt.Errorf("kmsclient: application release response entry %d has invalid kind", index)
		}
		switch entry.GetSource() {
		case ApplicationReleaseSourceGeneratedDefault:
			if entry.GetKind() != "parameter" {
				return CreateApplicationReleaseResult{}, fmt.Errorf("kmsclient: application release response entry %d has an invalid source for its kind", index)
			}
		case ApplicationReleaseSourceCarriedActiveSecret, ApplicationReleaseSourceResolvedCurrentSecret:
			if entry.GetKind() != "secret" {
				return CreateApplicationReleaseResult{}, fmt.Errorf("kmsclient: application release response entry %d has an invalid source for its kind", index)
			}
		default:
			return CreateApplicationReleaseResult{}, fmt.Errorf("kmsclient: application release response entry %d has invalid source", index)
		}
		if _, duplicate := aliases[entry.GetAlias()]; duplicate {
			return CreateApplicationReleaseResult{}, fmt.Errorf("kmsclient: application release response entry %d duplicates an alias", index)
		}
		aliases[entry.GetAlias()] = struct{}{}
		ref := refFromProto(entry.GetRef())
		path := ref.display()
		if _, err := splitDisplayPath(path); err != nil {
			return CreateApplicationReleaseResult{}, fmt.Errorf("kmsclient: application release response entry %d has invalid resource", index)
		}
		result.Entries = append(result.Entries, ApplicationReleasePlanEntry{
			Alias: entry.GetAlias(), Kind: entry.GetKind(), Path: path,
			FromVersion: entry.GetFromVersion(), ToVersion: entry.GetToVersion(), Source: entry.GetSource(),
		})
	}
	for index, alias := range result.MissingSecrets {
		if alias == "" {
			return CreateApplicationReleaseResult{}, fmt.Errorf("kmsclient: application release response missing secret %d is empty", index)
		}
	}
	for index, validation := range response.GetValidation() {
		if validation == nil || validation.GetCode() == "" {
			return CreateApplicationReleaseResult{}, fmt.Errorf("kmsclient: application release response validation %d is incomplete", index)
		}
		result.Validation = append(result.Validation, ApplicationReleaseValidationError{
			Alias: validation.GetAlias(), Code: validation.GetCode(),
			SchemaPointer: validation.GetSchemaPointer(), Message: validation.GetMessage(),
		})
	}

	if release := response.GetRelease(); release != nil {
		manifest, err := validateCreatedApplicationRelease(release, namespace, result.ReleaseName, result.SchemaVersion)
		if err != nil {
			return CreateApplicationReleaseResult{}, err
		}
		result.Release = &manifest
	}
	return result, nil
}

func validateCreatedApplicationRelease(
	release *kmsv1.ConfigurationRelease,
	namespace namespaceRef,
	releaseName string,
	schemaVersion uint64,
) (ReleaseManifest, error) {
	if release.GetNamespace() == nil || release.GetNamespace().GetEnv() != namespace.env || release.GetNamespace().GetApp() != namespace.app ||
		release.GetName() != releaseName || release.GetVersion() == 0 || release.GetSchemaVersion() != schemaVersion || !validCanonicalSHA256Hex(release.GetDigest()) {
		return ReleaseManifest{}, fmt.Errorf("kmsclient: application release response contains an invalid release")
	}
	calculatedDigest, err := deterministicReleaseDigest(release)
	if err != nil || calculatedDigest != release.GetDigest() {
		return ReleaseManifest{}, fmt.Errorf("kmsclient: application release response contains an invalid release digest")
	}
	manifest, err := newReleaseManifest(release, releaseCandidate{})
	if err != nil {
		return ReleaseManifest{}, fmt.Errorf("kmsclient: application release response contains an invalid release")
	}
	return manifest, nil
}

func validCanonicalSHA256Hex(value string) bool {
	return validSHA256Hex(value) && value == strings.ToLower(value)
}
