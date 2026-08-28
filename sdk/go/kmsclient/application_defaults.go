package kmsclient

import (
	"context"
	"fmt"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
)

// ApplicationDefaultsApplyOptions describes one preview or execution of a
// generated, parameter-only application defaults artifact.
type ApplicationDefaultsApplyOptions struct {
	Namespace        string
	Artifact         []byte
	Overwrite        bool
	UpdateDefinition bool
	Execute          bool
	PlanDigest       string
}

// ApplicationDefaultsApplyEntry reports the disposition of one parameter
// default. Status is create, unchanged, update, or blocked.
type ApplicationDefaultsApplyEntry struct {
	Alias          string
	Key            string
	ContentType    string
	Status         string
	CurrentVersion uint64
	AppliedVersion uint64
	Revision       uint64
}

// ApplicationDefaultsApplyResult is the validated server response for an
// application defaults preview or execution.
type ApplicationDefaultsApplyResult struct {
	Profile           string
	SchemaSHA256      string
	ArtifactDigest    string
	PlanDigest        string
	Entries           []ApplicationDefaultsApplyEntry
	MissingSecrets    []string
	Executed          bool
	DefinitionChanged bool
	DefinitionUpdated bool
}

// ApplyApplicationDefaults previews or atomically applies a generated
// parameter-only defaults artifact. Execute requests must carry the exact plan
// digest returned by a fresh preview. Existing differing values remain blocked
// unless Overwrite is true; identical values are an idempotent no-op.
func (c *Client) ApplyApplicationDefaults(
	ctx context.Context,
	options ApplicationDefaultsApplyOptions,
) (ApplicationDefaultsApplyResult, error) {
	namespace, err := parseNamespace(options.Namespace)
	if err != nil {
		return ApplicationDefaultsApplyResult{}, err
	}
	if len(options.Artifact) == 0 {
		return ApplicationDefaultsApplyResult{}, fmt.Errorf("kmsclient: defaults artifact is required")
	}
	cctx, cancel := c.callCtx(ctx, "")
	defer cancel()
	response, err := c.admin.ApplyApplicationDefaults(cctx, &kmsv1.ApplyApplicationDefaultsRequest{
		Namespace:        namespace.proto(),
		Artifact:         options.Artifact,
		Overwrite:        options.Overwrite,
		UpdateDefinition: options.UpdateDefinition,
		Execute:          options.Execute,
		PlanDigest:       options.PlanDigest,
	})
	if err != nil {
		return ApplicationDefaultsApplyResult{}, mapError(err)
	}
	if response == nil || response.GetPlanDigest() == "" {
		return ApplicationDefaultsApplyResult{}, fmt.Errorf("kmsclient: invalid defaults response")
	}
	if response.GetExecuted() != options.Execute {
		return ApplicationDefaultsApplyResult{}, fmt.Errorf("kmsclient: defaults response execution state mismatch")
	}
	if response.GetDefinitionUpdated() != (options.Execute && response.GetDefinitionChanged()) {
		return ApplicationDefaultsApplyResult{}, fmt.Errorf("kmsclient: defaults response definition state mismatch")
	}

	result := ApplicationDefaultsApplyResult{
		Profile:           response.GetProfile(),
		SchemaSHA256:      response.GetSchemaSha256(),
		ArtifactDigest:    response.GetArtifactDigest(),
		PlanDigest:        response.GetPlanDigest(),
		Entries:           make([]ApplicationDefaultsApplyEntry, 0, len(response.GetEntries())),
		MissingSecrets:    append([]string(nil), response.GetMissingSecrets()...),
		Executed:          response.GetExecuted(),
		DefinitionChanged: response.GetDefinitionChanged(),
		DefinitionUpdated: response.GetDefinitionUpdated(),
	}
	for index, entry := range response.GetEntries() {
		if entry == nil {
			return ApplicationDefaultsApplyResult{}, fmt.Errorf("kmsclient: defaults response entry %d is empty", index)
		}
		switch entry.GetStatus() {
		case "create", "unchanged", "update", "blocked":
		default:
			return ApplicationDefaultsApplyResult{}, fmt.Errorf("kmsclient: defaults response entry %d has invalid status", index)
		}
		if entry.GetAlias() == "" || entry.GetKey() == "" {
			return ApplicationDefaultsApplyResult{}, fmt.Errorf("kmsclient: defaults response entry %d is incomplete", index)
		}
		result.Entries = append(result.Entries, ApplicationDefaultsApplyEntry{
			Alias: entry.GetAlias(), Key: entry.GetKey(), ContentType: entry.GetContentType(),
			Status: entry.GetStatus(), CurrentVersion: entry.GetCurrentVersion(),
			AppliedVersion: entry.GetAppliedVersion(), Revision: entry.GetRevision(),
		})
	}
	return result, nil
}
