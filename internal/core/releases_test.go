package core

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/storage"
)

func TestConfigurationReleaseCoreLifecycleAndHistoricalAck(t *testing.T) {
	ctx := context.Background()
	st, err := storage.Open(filepath.Join(t.TempDir(), "kms.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	ns := domain.NamespaceRef{Env: "prod", App: "app"}
	if _, err := st.CreateNamespace(ctx, domain.Namespace{NamespaceRef: ns, CreatedBy: "admin"}); err != nil {
		t.Fatal(err)
	}
	ref := domain.Ref{NS: ns, Key: "config/runtime"}
	if _, _, err := st.PutParameter(ctx, ref, "7", "integer", "{}", "admin"); err != nil {
		t.Fatal(err)
	}
	svc := New(st, nil, "test")
	pr := adminPrincipal()
	schema, err := svc.CreateConfigurationSchema(ctx, pr, "runtime", `{"type":"object","properties":{"settings":{"type":"integer","minimum":0}},"required":["settings"]}`, "{}")
	if err != nil {
		t.Fatal(err)
	}
	create := func() domain.ConfigurationRelease {
		r, err := svc.CreateConfigurationRelease(ctx, pr, domain.CreateConfigurationReleaseInput{Namespace: ns, Name: "runtime", SchemaID: schema.ID, SchemaVersion: schema.Version, Entries: []domain.ReleaseEntrySelector{{Alias: "settings", Kind: domain.ReleaseEntryParameter, Ref: ref, Label: "current"}}})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	r1 := create()
	if r1.Entries[0].Version != 1 || r1.Entries[0].ParameterDigest == "" || r1.Digest == "" {
		t.Fatalf("release not exactly pinned: %+v", r1)
	}
	validation, err := svc.ValidateConfigurationRelease(ctx, pr, ns, "runtime", r1.Version)
	if err != nil || len(validation) != 0 {
		t.Fatalf("validation=%+v err=%v", validation, err)
	}
	zero := uint64(0)
	a1, changed, err := svc.ActivateConfigurationRelease(ctx, pr, ns, "runtime", r1.Version, &zero)
	if err != nil || !changed {
		t.Fatalf("activate=%+v changed=%v err=%v", a1, changed, err)
	}
	if _, _, err := st.PutParameter(ctx, ref, "-1", "integer", "{}", "admin"); err != nil {
		t.Fatal(err)
	}
	badRelease, err := svc.CreateConfigurationRelease(ctx, pr, domain.CreateConfigurationReleaseInput{
		Namespace: ns, Name: "runtime", SchemaID: schema.ID, SchemaVersion: schema.Version,
		Entries: []domain.ReleaseEntrySelector{{Alias: "settings", Kind: domain.ReleaseEntryParameter, Ref: ref, Version: 2}},
	})
	if err != nil {
		t.Fatal(err)
	}
	beforeRejectedActivation, err := st.CurrentRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	expectV1ForInvalid := r1.Version
	if _, changed, err := svc.ActivateConfigurationRelease(ctx, pr, ns, "runtime", badRelease.Version, &expectV1ForInvalid); !errors.Is(err, domain.ErrFailedPrecondition) || changed {
		t.Fatalf("invalid activation changed=%v err=%v", changed, err)
	} else {
		var validationFailed *domain.ReleaseValidationFailedError
		if !errors.As(err, &validationFailed) || len(validationFailed.Violations()) == 0 {
			t.Fatalf("invalid activation error = %T %v, want structured validation failure", err, err)
		}
	}
	afterRejectedActivation, err := st.CurrentRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if afterRejectedActivation != beforeRejectedActivation {
		t.Fatalf("invalid activation advanced revision from %d to %d", beforeRejectedActivation, afterRejectedActivation)
	}
	stillActive, err := svc.GetActiveConfigurationRelease(ctx, pr, ns, "runtime")
	if err != nil || stillActive.Release.Version != r1.Version {
		t.Fatalf("active release after rejected activation = %+v err=%v", stillActive, err)
	}
	if _, err := svc.DeleteParameter(ctx, pr, ref); !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Fatalf("delete active pin err=%v", err)
	}
	if _, _, err := st.PutParameter(ctx, ref, "8", "integer", "{}", "admin"); err != nil {
		t.Fatal(err)
	}
	r2 := create()
	a2, changed, err := svc.ActivateConfigurationRelease(ctx, pr, ns, "runtime", r2.Version, nil)
	if err != nil || !changed {
		t.Fatalf("activate2=%+v changed=%v err=%v", a2, changed, err)
	}
	const connectionID = "core-test-connection"
	if err := svc.SetReleaseSubscriberConnected(ctx, ns, "runtime", "api", "replica-1", pr.Identity.Name, connectionID, true); err != nil {
		t.Fatal(err)
	}
	err = svc.AcknowledgeConfigurationRelease(ctx, pr, domain.ReleaseAcknowledgement{Namespace: ns, ReleaseName: "runtime", ReleaseVersion: r1.Version, ActivationRevision: a1.ActivationRevision, ClientName: "api", InstanceID: "replica-1", ConnectionID: connectionID, State: domain.ReleaseStateRejected, RejectionCategory: domain.ReleaseRejectSuperseded, Diagnostic: "accidental-secret-value"})
	if err != nil {
		t.Fatalf("historical superseded acknowledgement: %v", err)
	}
	if _, _, err := st.PutParameter(ctx, domain.Ref{NS: ns, Key: "unrelated"}, "1", "integer", "{}", "admin"); err != nil {
		t.Fatal(err)
	}
	acks, _, activeRevision, err := svc.ListReleaseSubscribers(ctx, pr, ns, "runtime", storage.ListPage{})
	if err != nil || len(acks) != 1 || acks[0].Diagnostic != "[redacted]" {
		t.Fatalf("redacted acknowledgements=%+v err=%v", acks, err)
	}
	if acks[0].Identity != pr.Identity.Name {
		t.Fatalf("acknowledgement identity = %q, want authenticated principal %q", acks[0].Identity, pr.Identity.Name)
	}
	if activeRevision != a2.ActivationRevision {
		t.Fatalf("subscriber current revision=%d want active release revision %d", activeRevision, a2.ActivationRevision)
	}
	err = svc.AcknowledgeConfigurationRelease(ctx, pr, domain.ReleaseAcknowledgement{Namespace: ns, ReleaseName: "runtime", ReleaseVersion: r1.Version, ActivationRevision: a1.ActivationRevision + 999, ClientName: "api", InstanceID: "replica-1", ConnectionID: connectionID, State: domain.ReleaseStateRejected, RejectionCategory: domain.ReleaseRejectSuperseded})
	if !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Fatalf("fabricated revision err=%v", err)
	}
}

func TestConfigurationReleaseValidationPinsSourceNamespaceIncarnation(t *testing.T) {
	ctx := context.Background()
	st, err := storage.Open(filepath.Join(t.TempDir(), "kms.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	source := domain.NamespaceRef{Env: "prod", App: "source"}
	target := domain.NamespaceRef{Env: "prod", App: "target"}
	for _, namespace := range []domain.NamespaceRef{source, target} {
		if _, err := st.CreateNamespace(ctx, domain.Namespace{NamespaceRef: namespace, CreatedBy: "admin"}); err != nil {
			t.Fatal(err)
		}
	}
	ref := domain.Ref{NS: source, Key: "config/runtime"}
	if _, _, err := st.PutParameter(ctx, ref, `{"enabled":true}`, "json", "{}", "admin"); err != nil {
		t.Fatal(err)
	}

	svc := New(st, nil, "test")
	principal := adminPrincipal()
	release, err := svc.CreateConfigurationRelease(ctx, principal, domain.CreateConfigurationReleaseInput{
		Namespace: target,
		Name:      "runtime",
		Entries: []domain.ReleaseEntrySelector{{
			Alias: "settings", Kind: domain.ReleaseEntryParameter, Ref: ref, Version: 1,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if release.Entries[0].ResourceNamespaceID == 0 {
		t.Fatal("release did not retain its source namespace incarnation")
	}

	if _, err := st.DeleteParameter(ctx, ref); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteNamespace(ctx, source); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateNamespace(ctx, domain.Namespace{NamespaceRef: source, CreatedBy: "admin"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.PutParameter(ctx, ref, `{"enabled":true}`, "json", "{}", "admin"); err != nil {
		t.Fatal(err)
	}

	violations, err := svc.ValidateConfigurationRelease(ctx, principal, target, "runtime", release.Version)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 1 || violations[0].Alias != "settings" || violations[0].Code != domain.ReleaseValidationNotFound {
		t.Fatalf("validation violations = %+v, want settings/not_found", violations)
	}

	before, err := st.CurrentRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	zero := uint64(0)
	if _, changed, err := svc.ActivateConfigurationRelease(ctx, principal, target, "runtime", release.Version, &zero); !errors.Is(err, domain.ErrFailedPrecondition) || changed {
		t.Fatalf("activation against recreated source changed=%v err=%v, want validation failure", changed, err)
	}
	after, err := st.CurrentRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("rejected activation advanced revision from %d to %d", before, after)
	}
	if _, err := svc.GetActiveConfigurationRelease(ctx, principal, target, "runtime"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("active release after rejected activation err=%v, want ErrNotFound", err)
	}
}

func TestConfigurationSchemaErrorsAreSanitized(t *testing.T) {
	svc := newTestService(newFakeStore())
	pr := adminPrincipal()
	_, err := svc.CreateConfigurationSchema(context.Background(), pr, "runtime", `{"type":"definitely-not-a-real-type","const":"do-not-echo"}`, "{}")
	if err == nil || err.Error() != "invalid Draft 2020-12 JSON Schema: invalid argument" {
		t.Fatalf("unexpected schema error %q", err)
	}
}

func TestValidRejectCategoryIncludesManagedConfigurationFailures(t *testing.T) {
	for _, category := range []string{
		domain.ReleaseRejectConfigContractMismatch,
		domain.ReleaseRejectConfigDecodeFailed,
		domain.ReleaseRejectConfigValidationFailed,
		domain.ReleaseRejectDefaultMismatch,
		domain.ReleaseRejectRestartRequired,
	} {
		if !validRejectCategory(category) {
			t.Errorf("validRejectCategory(%q) = false", category)
		}
	}
	if validRejectCategory("field_specific_unbounded_category") {
		t.Fatal("validRejectCategory accepted an unbounded category")
	}
}

func TestConfigurationReleaseSecretPinSurvivesLaterAttributeChanges(t *testing.T) {
	ctx := context.Background()
	st, err := storage.Open(filepath.Join(t.TempDir(), "kms.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	ns := domain.NamespaceRef{Env: "prod", App: "app"}
	if _, err := st.CreateNamespace(ctx, domain.Namespace{NamespaceRef: ns, CreatedBy: "admin"}); err != nil {
		t.Fatal(err)
	}
	if err := st.InsertKeyMetadata(ctx, domain.KeyMetadata{ID: "kek-a", Source: domain.KeySourceFile, KeyCheck: []byte("test"), State: domain.KeyStateActive}); err != nil {
		t.Fatal(err)
	}
	ref := domain.Ref{NS: ns, Key: "secret"}
	encrypt := func(version uint64) (storage.EncryptedPayload, error) {
		return storage.EncryptedPayload{
			Ciphertext: []byte{byte(version)}, EncryptedDEK: []byte("dek"), KEKID: "kek-a",
			WrapMode: domain.WrapModeStandard, Algorithm: "AES-256-GCM", Nonce: []byte("nonce"), AAD: "aad",
		}, nil
	}
	if _, _, err := st.CreateSecretVersion(ctx, storage.CreateSecretParams{
		Ref: ref, ContentType: "text/plain", Metadata: "{}", CreatedBy: "admin", Encrypt: encrypt,
	}); err != nil {
		t.Fatal(err)
	}

	svc := New(st, nil, "test")
	release, err := svc.CreateConfigurationRelease(ctx, adminPrincipal(), domain.CreateConfigurationReleaseInput{
		Namespace: ns,
		Name:      "runtime",
		Entries: []domain.ReleaseEntrySelector{{
			Alias: "credential", Kind: domain.ReleaseEntrySecret, Ref: ref, Version: 1,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	entry := release.Entries[0]
	if entry.ContentType != "text/plain" || entry.ClientBound || entry.HasAccessToken {
		t.Fatalf("release v1 secret pin = %+v", entry)
	}

	// A later immutable secret version may use a different content type and be
	// the first version protected by a per-secret access token.
	if _, _, err := st.CreateSecretVersion(ctx, storage.CreateSecretParams{
		Ref: ref, ContentType: "application/json", Metadata: "{}", CreatedBy: "admin",
		AccessTokenHash: []byte("new-token-hash"), Encrypt: encrypt,
	}); err != nil {
		t.Fatal(err)
	}
	validation, err := svc.ValidateConfigurationRelease(ctx, adminPrincipal(), ns, "runtime", release.Version)
	if err != nil {
		t.Fatal(err)
	}
	if len(validation) != 0 {
		t.Fatalf("historical release validation = %+v, want valid", validation)
	}
	stored, err := svc.GetConfigurationRelease(ctx, adminPrincipal(), ns, "runtime", release.Version)
	if err != nil {
		t.Fatal(err)
	}
	if got := stored.Entries[0]; got.ContentType != "text/plain" || got.HasAccessToken {
		t.Fatalf("stored release pin changed: %+v", got)
	}
}
