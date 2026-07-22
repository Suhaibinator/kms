package integration

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/Suhaibinator/kms/internal/core"
	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/storage"
	"github.com/Suhaibinator/kms/internal/watch"
)

// TestConfigurationReleaseLifecycle exercises the release feature through the
// real service, storage, encryption, audit, and change-log stack. In
// particular, it proves that movable resource labels are resolved to immutable
// pins before persistence and that a release activation is isolated from the
// legacy namespace watch stream.
func TestConfigurationReleaseLifecycle(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	ns := nsRef("prod", "release-integration")
	parameterRef := h.ensureNS("/prod/release-integration/workers")
	secretRef := h.ref("/prod/release-integration/database-password")

	parameterV1, _, err := h.svc.PutParameter(ctx, h.admin, parameterRef, "4", "integer", `{"unit":"workers"}`)
	if err != nil {
		t.Fatalf("PutParameter v1: %v", err)
	}
	const secretV1Plaintext = "release-secret-v1-canary-e429"
	secretV1, err := h.svc.PutSecret(ctx, h.admin, core.PutSecretInput{
		Ref: secretRef, Value: []byte(secretV1Plaintext), ContentType: "text/plain",
		Metadata: `{"owner":"database"}`, GenerateToken: true,
	})
	if err != nil {
		t.Fatalf("PutSecret v1: %v", err)
	}
	if secretV1.AccessToken == "" {
		t.Fatal("PutSecret v1 returned no access token")
	}

	schemaV1, err := h.svc.CreateConfigurationSchema(ctx, h.admin, "release-integration/runtime", `{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object",
		"properties":{"workers":{"type":"integer","minimum":1}},
		"required":["workers"],
		"additionalProperties":false
	}`, `{"owner":"integration"}`)
	if err != nil {
		t.Fatalf("CreateConfigurationSchema v1: %v", err)
	}

	releaseV1, err := h.svc.CreateConfigurationRelease(ctx, h.admin, domain.CreateConfigurationReleaseInput{
		Namespace: ns, Name: "runtime", SchemaID: schemaV1.ID, SchemaVersion: schemaV1.Version,
		Metadata: `{"purpose":"runtime"}`,
		Entries: []domain.ReleaseEntrySelector{
			{Alias: "workers", Kind: domain.ReleaseEntryParameter, Ref: parameterRef, Label: domain.LabelCurrent},
			{Alias: "database_password", Kind: domain.ReleaseEntrySecret, Ref: secretRef, Label: domain.LabelCurrent},
		},
	})
	if err != nil {
		t.Fatalf("CreateConfigurationRelease v1: %v", err)
	}
	assertReleasePins(t, releaseV1, parameterV1, secretV1.Version, secretV1Plaintext, secretV1.AccessToken)
	validation, err := h.svc.ValidateConfigurationRelease(ctx, h.admin, ns, "runtime", releaseV1.Version)
	if err != nil {
		t.Fatalf("ValidateConfigurationRelease v1: %v", err)
	}
	if len(validation) != 0 {
		t.Fatalf("ValidateConfigurationRelease v1 errors = %+v, want none", validation)
	}

	parameterV2, _, err := h.svc.PutParameter(ctx, h.admin, parameterRef, "9", "integer", `{"unit":"workers"}`)
	if err != nil {
		t.Fatalf("PutParameter v2: %v", err)
	}
	const secretV2Plaintext = "release-secret-v2-canary-c77b"
	secretV2, err := h.svc.PutSecret(ctx, h.admin, core.PutSecretInput{
		Ref: secretRef, Value: []byte(secretV2Plaintext), ContentType: "text/plain",
		Metadata: `{"owner":"database"}`,
	})
	if err != nil {
		t.Fatalf("PutSecret v2: %v", err)
	}
	if secretV2.AccessToken != "" {
		t.Fatal("ordinary rotation unexpectedly minted a new access token")
	}

	// The second immutable schema version intentionally conflicts with the
	// integer parameter. Validation must return a structured, value-free schema
	// failure without preventing the immutable release from being inspected.
	schemaV2, err := h.svc.CreateConfigurationSchema(ctx, h.admin, schemaV1.ID, `{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object",
		"properties":{"workers":{"type":"string"}},
		"required":["workers"],
		"additionalProperties":false
	}`, `{"owner":"integration"}`)
	if err != nil {
		t.Fatalf("CreateConfigurationSchema v2: %v", err)
	}
	if schemaV2.Version != schemaV1.Version+1 {
		t.Fatalf("schema v2 version = %d, want %d", schemaV2.Version, schemaV1.Version+1)
	}
	releaseV2, err := h.svc.CreateConfigurationRelease(ctx, h.admin, domain.CreateConfigurationReleaseInput{
		Namespace: ns, Name: "runtime", SchemaID: schemaV2.ID, SchemaVersion: schemaV2.Version,
		Metadata: `{"purpose":"runtime"}`,
		Entries: []domain.ReleaseEntrySelector{
			// Empty version/label defaults to current and must still persist the
			// exact resource version selected at creation time.
			{Alias: "database_password", Kind: domain.ReleaseEntrySecret, Ref: secretRef},
			{Alias: "workers", Kind: domain.ReleaseEntryParameter, Ref: parameterRef},
		},
	})
	if err != nil {
		t.Fatalf("CreateConfigurationRelease v2: %v", err)
	}
	assertReleasePins(t, releaseV2, parameterV2, secretV2.Version, secretV2Plaintext, secretV1.AccessToken)
	validation, err = h.svc.ValidateConfigurationRelease(ctx, h.admin, ns, "runtime", releaseV2.Version)
	if err != nil {
		t.Fatalf("ValidateConfigurationRelease v2: %v", err)
	}
	if !hasReleaseValidationError(validation, "workers", domain.ReleaseValidationSchema) {
		t.Fatalf("ValidateConfigurationRelease v2 errors = %+v, want workers schema violation", validation)
	}
	validationText := fmt.Sprintf("%+v", validation)
	for _, sensitive := range []string{secretV1Plaintext, secretV2Plaintext, secretV1.AccessToken} {
		if strings.Contains(validationText, sensitive) {
			t.Fatalf("release validation leaked sensitive value %q", sensitive)
		}
	}
	releaseV3, err := h.svc.CreateConfigurationRelease(ctx, h.admin, domain.CreateConfigurationReleaseInput{
		Namespace: ns, Name: "runtime", SchemaID: schemaV1.ID, SchemaVersion: schemaV1.Version,
		Metadata: `{"purpose":"runtime"}`,
		Entries: []domain.ReleaseEntrySelector{
			{Alias: "database_password", Kind: domain.ReleaseEntrySecret, Ref: secretRef, Version: secretV2.Version},
			{Alias: "workers", Kind: domain.ReleaseEntryParameter, Ref: parameterRef, Version: parameterV2},
		},
	})
	if err != nil {
		t.Fatalf("CreateConfigurationRelease v3: %v", err)
	}
	validation, err = h.svc.ValidateConfigurationRelease(ctx, h.admin, ns, "runtime", releaseV3.Version)
	if err != nil || len(validation) != 0 {
		t.Fatalf("ValidateConfigurationRelease v3 errors=%+v err=%v", validation, err)
	}

	beforeActivation, err := h.store.CurrentRevision(ctx)
	if err != nil {
		t.Fatalf("CurrentRevision before activation: %v", err)
	}
	zero := uint64(0)
	activeV1, changed, err := h.svc.ActivateConfigurationRelease(ctx, h.admin, ns, "runtime", releaseV1.Version, &zero)
	if err != nil || !changed {
		t.Fatalf("ActivateConfigurationRelease v1 changed=%v err=%v", changed, err)
	}
	if activeV1.Release.Version != releaseV1.Version || activeV1.PreviousVersion != 0 {
		t.Fatalf("active v1 = %+v", activeV1)
	}

	// A stale compare-and-swap must neither move current nor consume a global
	// changelog revision.
	if _, _, err := h.svc.ActivateConfigurationRelease(ctx, h.admin, ns, "runtime", releaseV3.Version, &zero); !errors.Is(err, domain.ErrAborted) {
		t.Fatalf("stale CAS activation err = %v, want ErrAborted", err)
	}
	afterConflict, err := h.store.CurrentRevision(ctx)
	if err != nil {
		t.Fatalf("CurrentRevision after conflict: %v", err)
	}
	if afterConflict != activeV1.ActivationRevision {
		t.Fatalf("CAS conflict advanced revision from %d to %d", activeV1.ActivationRevision, afterConflict)
	}

	expectV1 := releaseV1.Version
	if _, changed, err := h.svc.ActivateConfigurationRelease(ctx, h.admin, ns, "runtime", releaseV2.Version, &expectV1); !errors.Is(err, domain.ErrFailedPrecondition) || changed {
		t.Fatalf("schema-invalid activation changed=%v err=%v, want validation failure", changed, err)
	}
	afterValidationFailure, err := h.store.CurrentRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if afterValidationFailure != activeV1.ActivationRevision {
		t.Fatalf("validation failure advanced revision from %d to %d", activeV1.ActivationRevision, afterValidationFailure)
	}

	activeV2, changed, err := h.svc.ActivateConfigurationRelease(ctx, h.admin, ns, "runtime", releaseV3.Version, &expectV1)
	if err != nil || !changed {
		t.Fatalf("ActivateConfigurationRelease v3 changed=%v err=%v", changed, err)
	}
	if activeV2.PreviousVersion != releaseV1.Version {
		t.Fatalf("active v3 previous = %d, want %d", activeV2.PreviousVersion, releaseV1.Version)
	}
	rolledBack, changed, err := h.svc.ActivateConfigurationRelease(ctx, h.admin, ns, "runtime", releaseV1.Version, nil)
	if err != nil || !changed {
		t.Fatalf("rollback to v1 changed=%v err=%v", changed, err)
	}
	if rolledBack.Release.Version != releaseV1.Version || rolledBack.PreviousVersion != releaseV3.Version {
		t.Fatalf("rollback active = %+v, want current v%d previous v%d", rolledBack, releaseV1.Version, releaseV3.Version)
	}

	activationChanges, err := h.store.ListChangesSince(ctx, beforeActivation, 100)
	if err != nil {
		t.Fatalf("ListChangesSince activation: %v", err)
	}
	if len(activationChanges) != 3 {
		t.Fatalf("activation change count = %d (%+v), want exactly 3", len(activationChanges), activationChanges)
	}
	wantVersions := []uint64{releaseV1.Version, releaseV3.Version, releaseV1.Version}
	for i, change := range activationChanges {
		if change.ResourceType != domain.ResourceConfigurationRelease || change.ChangeType != "activate" || change.Ref.NS != ns || change.Ref.Key != "runtime" {
			t.Fatalf("activation change %d = %+v", i, change)
		}
		if change.Version != wantVersions[i] {
			t.Fatalf("activation change %d version = %d, want %d", i, change.Version, wantVersions[i])
		}
		if change.Value != "" {
			t.Fatalf("activation change %d exposed a value", i)
		}
		if i > 0 && change.Revision <= activationChanges[i-1].Revision {
			t.Fatalf("activation revisions are not strictly increasing: %+v", activationChanges)
		}
	}

	// The historical namespace-wide watch stream must advance past release
	// revisions without surfacing release events to existing consumers.
	legacyHub := watch.NewHub(h.store, nil, watch.Options{})
	namespace, err := h.store.GetNamespace(ctx, ns)
	if err != nil {
		t.Fatalf("GetNamespace for legacy watcher: %v", err)
	}
	legacySub, err := legacyHub.Subscribe(ctx, watch.Registration{
		ClientName: "legacy", InstanceID: "replica-1", Identity: "legacy",
		Namespaces: []domain.NamespaceRef{ns}, NamespaceIDs: map[domain.NamespaceRef]int64{ns: namespace.ID},
		LastSeenRevision: activeV1.ActivationRevision,
	})
	if err != nil {
		t.Fatalf("legacy Subscribe: %v", err)
	}
	defer legacySub.Close()
	legacyBacklog := legacySub.Backlog()
	if legacyBacklog.IsSnapshot || len(legacyBacklog.Replay) != 0 {
		t.Fatalf("legacy watcher backlog = %+v, want empty replay across release-only revisions", legacyBacklog)
	}
	if legacyBacklog.Revision != rolledBack.ActivationRevision {
		t.Fatalf("legacy watcher revision = %d, want %d", legacyBacklog.Revision, rolledBack.ActivationRevision)
	}

	// Current and previous releases remain resolvable, so destructive operations
	// against pins from either release are rejected with an identifying error.
	assertReferenceBlocked(t, func() error { _, err := h.svc.DeleteParameter(ctx, h.admin, parameterRef); return err }, "workers")
	assertReferenceBlocked(t, func() error {
		_, err := h.svc.DestroySecretVersion(ctx, h.admin, secretRef, secretV1.Version)
		return err
	}, "database_password")
	assertReferenceBlocked(t, func() error {
		_, err := h.svc.DestroySecretVersion(ctx, h.admin, secretRef, secretV2.Version)
		return err
	}, "database_password")
	assertReferenceBlocked(t, func() error { _, err := h.svc.DeleteSecret(ctx, h.admin, secretRef); return err }, "database_password")

	for _, audit := range []struct {
		typeName string
		decision string
	}{
		{"configuration_schema.create", "allow"},
		{"configuration_release.create", "allow"},
		{"configuration_release.validate", "allow"},
		{"configuration_release.validate", "error"},
		{"configuration_release.activate", "allow"},
		{"configuration_release.cas_conflict", "deny"},
		{"configuration_release.reference_blocked", "deny"},
	} {
		if !hasAuditEvent(t, h, audit.typeName, audit.decision) {
			t.Errorf("missing audit event %q decision %q", audit.typeName, audit.decision)
		}
	}
	events, _, err := h.svc.ListAuditEvents(ctx, h.admin, domain.AuditFilter{}, storage.ListPage{Limit: 1000})
	if err != nil {
		t.Fatalf("ListAuditEvents: %v", err)
	}
	auditText := fmt.Sprintf("%+v", events)
	releaseText := fmt.Sprintf("%+v%+v", releaseV1, releaseV2)
	logText := h.logBuf.String()
	for _, sensitive := range []string{secretV1Plaintext, secretV2Plaintext, secretV1.AccessToken} {
		for surface, text := range map[string]string{"release": releaseText, "audit": auditText, "log": logText} {
			if strings.Contains(text, sensitive) {
				t.Fatalf("%s surface leaked sensitive value %q", surface, sensitive)
			}
		}
		if strings.Contains(string(h.scanBytes()), sensitive) {
			t.Fatalf("database surface leaked sensitive value %q", sensitive)
		}
	}
}

func assertReleasePins(t *testing.T, release domain.ConfigurationRelease, parameterVersion, secretVersion uint64, sensitive ...string) {
	t.Helper()
	if release.Version == 0 || release.Digest == "" {
		t.Fatalf("release identity is incomplete: %+v", release)
	}
	if len(release.Entries) != 2 {
		t.Fatalf("release entries = %+v, want 2", release.Entries)
	}
	entries := make(map[string]domain.ConfigurationReleaseEntry, len(release.Entries))
	for _, entry := range release.Entries {
		entries[entry.Alias] = entry
	}
	parameter := entries["workers"]
	if parameter.Kind != domain.ReleaseEntryParameter || parameter.Version != parameterVersion || parameter.ParameterDigest == "" {
		t.Fatalf("parameter release pin = %+v, want version %d with digest", parameter, parameterVersion)
	}
	secret := entries["database_password"]
	if secret.Kind != domain.ReleaseEntrySecret || secret.Version != secretVersion || secret.ParameterDigest != "" || !secret.HasAccessToken {
		t.Fatalf("secret release pin = %+v, want metadata-only version %d", secret, secretVersion)
	}
	text := fmt.Sprintf("%+v", release)
	for _, value := range sensitive {
		if value != "" && strings.Contains(text, value) {
			t.Fatalf("release leaked sensitive value %q", value)
		}
	}
}

func hasReleaseValidationError(errors []domain.ReleaseValidationError, alias, code string) bool {
	for _, validation := range errors {
		if validation.Alias == alias && validation.Code == code {
			return true
		}
	}
	return false
}

func assertReferenceBlocked(t *testing.T, operation func() error, alias string) {
	t.Helper()
	err := operation()
	if !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Fatalf("destructive operation err = %v, want ErrFailedPrecondition", err)
	}
	if !strings.Contains(err.Error(), "runtime") || !strings.Contains(err.Error(), alias) {
		t.Fatalf("destructive conflict %q does not identify release runtime and alias %q", err, alias)
	}
}
