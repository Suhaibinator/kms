package core

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
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
	svc := New(st, nil, "test")
	pr := adminPrincipal()
	_, schema, err := svc.CreateApplicationWithSchema(ctx, pr, domain.Application{Name: "app", ReleaseName: "runtime"}, `{"type":"object","properties":{"settings":{"type":"integer","minimum":0}},"required":["settings"]}`, "{}")
	if err != nil {
		t.Fatal(err)
	}
	ns := domain.NamespaceRef{Env: "prod", App: "app"}
	if _, err := st.CreateNamespace(ctx, domain.Namespace{NamespaceRef: ns, CreatedBy: "admin"}); err != nil {
		t.Fatal(err)
	}
	ref := domain.Ref{NS: ns, Key: "config/runtime"}
	if _, _, err := st.PutParameter(ctx, ref, "7", "integer", "{}", "admin"); err != nil {
		t.Fatal(err)
	}
	create := func() domain.ConfigurationRelease {
		r, err := svc.CreateConfigurationRelease(ctx, pr, domain.CreateConfigurationReleaseInput{Namespace: ns, Name: "runtime", SchemaVersion: schema.Version, Entries: []domain.ReleaseEntrySelector{{Alias: "settings", Kind: domain.ReleaseEntryParameter, Ref: ref, Label: "current"}}})
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
		Namespace: ns, Name: "runtime", SchemaVersion: schema.Version,
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
	_, err := normalizeConfigurationSchema("app", "runtime", `{"type":"definitely-not-a-real-type","const":"do-not-echo"}`, "{}")
	if err == nil || err.Error() != "invalid Draft 2020-12 JSON Schema: invalid argument" {
		t.Fatalf("unexpected schema error %q", err)
	}
}

func TestConfigurationSchemaStructuredCoordinates(t *testing.T) {
	ctx := context.Background()
	st, err := storage.Open(filepath.Join(t.TempDir(), "kms.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	svc := New(st, nil, "test")
	pr := adminPrincipal()
	if _, err := svc.CreateApplication(ctx, pr, domain.Application{Name: "payments", ReleaseName: "runtime"}); err != nil {
		t.Fatal(err)
	}
	first, err := svc.CreateConfigurationSchema(ctx, pr, "payments", `{"type":"object"}`, "{}")
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.CreateConfigurationSchema(ctx, pr, "payments", `{"type":"string"}`, "{}")
	if err != nil {
		t.Fatal(err)
	}
	got, err := svc.GetConfigurationSchema(ctx, pr, "payments", "runtime", second.Version)
	if err != nil || got.Application != "payments" || got.ReleaseName != "runtime" || got.Version != 2 {
		t.Fatalf("get schema = %+v err=%v", got, err)
	}
	rows, next, err := svc.ListConfigurationSchemas(ctx, pr, "payments", "runtime", storage.ListPage{Limit: 1})
	if err != nil || len(rows) != 1 || rows[0].Version != second.Version || next == "" {
		t.Fatalf("first schema page = %+v next=%q err=%v", rows, next, err)
	}
	rows, next, err = svc.ListConfigurationSchemas(ctx, pr, "payments", "runtime", storage.ListPage{Limit: 1, Token: next})
	if err != nil || len(rows) != 1 || rows[0].Version != first.Version || next != "" {
		t.Fatalf("second schema page = %+v next=%q err=%v", rows, next, err)
	}
	for name, call := range map[string]func() error{
		"get missing application": func() error { _, err := svc.GetConfigurationSchema(ctx, pr, "", "runtime", 1); return err },
		"get missing release":     func() error { _, err := svc.GetConfigurationSchema(ctx, pr, "payments", "", 1); return err },
		"get missing version":     func() error { _, err := svc.GetConfigurationSchema(ctx, pr, "payments", "runtime", 0); return err },
		"list release without application": func() error {
			_, _, err := svc.ListConfigurationSchemas(ctx, pr, "", "runtime", storage.ListPage{})
			return err
		},
	} {
		if err := call(); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Errorf("%s error = %v, want InvalidArgument", name, err)
		}
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

func TestReleaseCandidateValidationIsDryRunSafe(t *testing.T) {
	ctx := context.Background()
	st, err := storage.Open(filepath.Join(t.TempDir(), "kms.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	svc := New(st, nil, "test")
	pr := adminPrincipal()
	_, schema, err := svc.CreateApplicationWithSchema(ctx, pr, domain.Application{Name: "worker", ReleaseName: "runtime"}, `{"type":"object","properties":{"settings":{"type":"integer","minimum":0}},"required":["settings"]}`, "{}")
	if err != nil {
		t.Fatal(err)
	}
	ns := domain.NamespaceRef{Env: "dev", App: "worker"}
	if _, err := svc.CreateNamespace(ctx, pr, ns, "", []domain.AuthMethod{domain.AuthMethodToken}); err != nil {
		t.Fatal(err)
	}
	ref := domain.Ref{NS: ns, Key: "settings"}
	if _, _, err := svc.PutParameter(ctx, pr, ref, "7", "integer", "{}"); err != nil {
		t.Fatal(err)
	}
	rs, err := svc.releaseStore()
	if err != nil {
		t.Fatal(err)
	}
	authCtx, _, err := svc.authorize(ctx, pr, domain.OpConfigurationReleaseCreate, domain.ResourceConfigurationRelease, domain.Ref{NS: ns, Key: "runtime"})
	if err != nil {
		t.Fatal(err)
	}
	input := domain.CreateConfigurationReleaseInput{
		Namespace: ns, Name: "runtime", SchemaVersion: schema.Version, Metadata: "{}",
		Entries: []domain.ReleaseEntrySelector{{Alias: "settings", Kind: domain.ReleaseEntryParameter, Ref: ref}},
	}
	candCtx, candidate, resolution, err := svc.resolveReleaseCandidate(authCtx, pr, rs, input, true)
	if err != nil || len(resolution) != 0 {
		t.Fatalf("resolve candidate: %+v err=%v", resolution, err)
	}
	if len(candidate.Entries) != 1 || candidate.Entries[0].Version != 1 || candidate.Digest == "" || candidate.Version != 0 {
		t.Fatalf("candidate not resolved in memory: %+v", candidate)
	}

	// The stored value satisfies the schema; validation must not adopt a contract.
	validation, err := svc.validateReleaseEntries(candCtx, pr, rs, candidate, nil, false, false)
	if err != nil || len(validation) != 0 {
		t.Fatalf("stored-value validation = %+v err=%v", validation, err)
	}
	app, err := svc.GetApplication(ctx, pr, "worker")
	if err != nil || len(app.Contract) != 0 {
		t.Fatalf("dry-run validation adopted a contract: %+v err=%v", app.Contract, err)
	}

	// The override is what the schema sees: minimum is violated only by it.
	overrides := map[string]releaseCandidateValue{"settings": {value: []byte("-1"), contentType: "integer"}}
	validation, err = svc.validateReleaseEntries(candCtx, pr, rs, candidate, overrides, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(validation) != 1 || validation[0].Alias != "settings" || validation[0].Code != domain.ReleaseValidationSchema || validation[0].SchemaPointer != "/minimum" {
		t.Fatalf("override validation = %+v", validation)
	}
	for _, v := range validation {
		if strings.Contains(v.Message, "-1") || strings.Contains(v.Message, "7") {
			t.Fatalf("validation message leaks a value: %q", v.Message)
		}
	}
	// An override with a different content type is a content-type violation.
	validation, err = svc.validateReleaseEntries(candCtx, pr, rs, candidate, map[string]releaseCandidateValue{"settings": {value: []byte("x"), contentType: "string"}}, false, false)
	if err != nil || len(validation) != 1 || validation[0].Code != domain.ReleaseValidationContentType {
		t.Fatalf("content-type override validation = %+v err=%v", validation, err)
	}
	if got, _ := st.GetParameter(ctx, ref, 0, domain.LabelCurrent); got.Value != "7" || got.Version != 1 {
		t.Fatalf("dry-run wrote the parameter: %+v", got)
	}
	app, err = svc.GetApplication(ctx, pr, "worker")
	if err != nil || len(app.Contract) != 0 {
		t.Fatalf("override validation adopted a contract: %+v err=%v", app.Contract, err)
	}
	if n, err := rs.CountConfigurationReleases(ctx, ns, ""); err != nil || n != 0 {
		t.Fatalf("dry-run persisted a release: count=%d err=%v", n, err)
	}

	// Persisting through the public path still adopts the contract (adopt=true).
	if _, err := svc.CreateConfigurationRelease(ctx, pr, input); err != nil {
		t.Fatal(err)
	}
	app, err = svc.GetApplication(ctx, pr, "worker")
	if err != nil || len(app.Contract) != 1 || app.Contract[0].Alias != "settings" {
		t.Fatalf("create did not adopt the contract: %+v err=%v", app.Contract, err)
	}
}

func TestResolveReleaseCandidateCollectsPerAliasErrors(t *testing.T) {
	ctx := context.Background()
	st, err := storage.Open(filepath.Join(t.TempDir(), "kms.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	svc := New(st, nil, "test")
	pr := adminPrincipal()
	ns := domain.NamespaceRef{Env: "dev", App: "worker"}
	if _, err := svc.CreateNamespace(ctx, pr, ns, "", []domain.AuthMethod{domain.AuthMethodToken}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.PutParameter(ctx, pr, domain.Ref{NS: ns, Key: "present"}, "1", "integer", "{}"); err != nil {
		t.Fatal(err)
	}
	rs, err := svc.releaseStore()
	if err != nil {
		t.Fatal(err)
	}
	authCtx, _, err := svc.authorize(ctx, pr, domain.OpConfigurationReleaseCreate, domain.ResourceConfigurationRelease, domain.Ref{NS: ns, Key: "runtime"})
	if err != nil {
		t.Fatal(err)
	}
	input := domain.CreateConfigurationReleaseInput{Namespace: ns, Name: "runtime", Metadata: "{}", Entries: []domain.ReleaseEntrySelector{
		{Alias: "missing_a", Kind: domain.ReleaseEntryParameter, Ref: domain.Ref{NS: ns, Key: "nope"}},
		{Alias: "present", Kind: domain.ReleaseEntryParameter, Ref: domain.Ref{NS: ns, Key: "present"}},
		{Alias: "missing_b", Kind: domain.ReleaseEntrySecret, Ref: domain.Ref{NS: ns, Key: "nope"}},
	}}
	_, candidate, validation, err := svc.resolveReleaseCandidate(authCtx, pr, rs, input, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(validation) != 2 || validation[0].Alias != "missing_a" || validation[1].Alias != "missing_b" ||
		validation[0].Code != domain.ReleaseValidationNotFound || validation[1].Code != domain.ReleaseValidationNotFound {
		t.Fatalf("collected validation = %+v", validation)
	}
	if len(candidate.Entries) != 1 || candidate.Entries[0].Alias != "present" || candidate.Digest != "" {
		t.Fatalf("partial candidate = %+v", candidate)
	}
	if _, _, _, err := svc.resolveReleaseCandidate(authCtx, pr, rs, input, false); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("abort-on-first error = %v", err)
	}
	// Structural errors abort in both modes.
	bad := input
	bad.Entries = []domain.ReleaseEntrySelector{{Alias: "bad alias", Kind: domain.ReleaseEntryParameter, Ref: domain.Ref{NS: ns, Key: "present"}}}
	if _, _, _, err := svc.resolveReleaseCandidate(authCtx, pr, rs, bad, true); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("structural error in collect mode = %v", err)
	}
}

func TestRollbackConfigurationRelease(t *testing.T) {
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
	ref := domain.Ref{NS: ns, Key: "config"}
	svc := New(st, nil, "test")
	pr := adminPrincipal()
	if _, err := svc.RollbackConfigurationRelease(ctx, pr, ns, "runtime", nil); !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Fatalf("rollback without active = %v", err)
	}
	create := func(value string) domain.ConfigurationRelease {
		if _, _, err := st.PutParameter(ctx, ref, value, "integer", "{}", "admin"); err != nil {
			t.Fatal(err)
		}
		r, err := svc.CreateConfigurationRelease(ctx, pr, domain.CreateConfigurationReleaseInput{Namespace: ns, Name: "runtime", Entries: []domain.ReleaseEntrySelector{{Alias: "config", Kind: domain.ReleaseEntryParameter, Ref: ref}}})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	r1, r2 := create("1"), create("2")
	if _, _, err := svc.ActivateConfigurationRelease(ctx, pr, ns, "runtime", r1.Version, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RollbackConfigurationRelease(ctx, pr, ns, "runtime", nil); !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Fatalf("rollback without previous = %v", err)
	}
	if _, _, err := svc.ActivateConfigurationRelease(ctx, pr, ns, "runtime", r2.Version, nil); err != nil {
		t.Fatal(err)
	}
	wrong := uint64(1)
	if _, err := svc.RollbackConfigurationRelease(ctx, pr, ns, "runtime", &wrong); !errors.Is(err, domain.ErrAborted) {
		t.Fatalf("rollback CAS error = %v", err)
	}
	expected := uint64(2)
	result, err := svc.RollbackConfigurationRelease(ctx, pr, ns, "runtime", &expected)
	if err != nil || !result.Changed || result.RolledBackFrom != 2 || result.Active.Release.Version != 1 || result.Active.PreviousVersion != 2 {
		t.Fatalf("rollback = %+v err=%v", result, err)
	}
	events, _, err := st.ListAudit(ctx, domain.AuditFilter{EventType: "configuration_release.rollback"}, storage.ListPage{Limit: 10})
	if err != nil || len(events) != 1 || events[0].ResourceVersion != 1 {
		t.Fatalf("rollback audit = %+v err=%v", events, err)
	}
	// Rolling back again re-activates the newer version (previous is now v2).
	again, err := svc.RollbackConfigurationRelease(ctx, pr, ns, "runtime", nil)
	if err != nil || again.Active.Release.Version != 2 || again.RolledBackFrom != 1 {
		t.Fatalf("second rollback = %+v err=%v", again, err)
	}
}

// Divergence rides only on applied acknowledgements, is bounded, persists,
// and is visible through the admin subscriber listing.
func TestAcknowledgeConfigurationReleaseDivergence(t *testing.T) {
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
	rel, err := svc.CreateConfigurationRelease(ctx, pr, domain.CreateConfigurationReleaseInput{Namespace: ns, Name: "runtime", Entries: []domain.ReleaseEntrySelector{{Alias: "settings", Kind: domain.ReleaseEntryParameter, Ref: ref, Label: "current"}}})
	if err != nil {
		t.Fatal(err)
	}
	active, _, err := svc.ActivateConfigurationRelease(ctx, pr, ns, "runtime", rel.Version, nil)
	if err != nil {
		t.Fatal(err)
	}
	const connectionID = "divergence-connection"
	if err := svc.SetReleaseSubscriberConnected(ctx, ns, "runtime", "api", "replica-1", pr.Identity.Name, connectionID, true); err != nil {
		t.Fatal(err)
	}
	base := domain.ReleaseAcknowledgement{Namespace: ns, ReleaseName: "runtime", ReleaseVersion: rel.Version, ActivationRevision: active.ActivationRevision, ClientName: "api", InstanceID: "replica-1", ConnectionID: connectionID}

	prepared := base
	prepared.State, prepared.AppliedDivergent, prepared.DivergentFieldCount = domain.ReleaseStatePrepared, true, 1
	if err := svc.AcknowledgeConfigurationRelease(ctx, pr, prepared); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("divergent prepared ack err=%v, want invalid argument", err)
	}
	countOnly := base
	countOnly.State, countOnly.DivergentFieldCount = domain.ReleaseStateApplied, 2
	if err := svc.AcknowledgeConfigurationRelease(ctx, pr, countOnly); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("count without flag err=%v, want invalid argument", err)
	}
	huge := base
	huge.State, huge.AppliedDivergent, huge.DivergentFieldCount = domain.ReleaseStateApplied, true, 65536
	if err := svc.AcknowledgeConfigurationRelease(ctx, pr, huge); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("oversized count err=%v, want invalid argument", err)
	}

	applied := base
	applied.State, applied.AppliedDivergent, applied.DivergentFieldCount = domain.ReleaseStateApplied, true, 3
	if err := svc.AcknowledgeConfigurationRelease(ctx, pr, applied); err != nil {
		t.Fatalf("divergent applied ack: %v", err)
	}
	acks, _, _, err := svc.ListReleaseSubscribers(ctx, pr, ns, "runtime", storage.ListPage{})
	if err != nil || len(acks) != 1 {
		t.Fatalf("acks=%+v err=%v", acks, err)
	}
	if acks[0].State != domain.ReleaseStateApplied || !acks[0].AppliedDivergent || acks[0].DivergentFieldCount != 3 {
		t.Fatalf("persisted divergence = %+v", acks[0])
	}
	events, _, err := st.ListAudit(ctx, domain.AuditFilter{EventType: "configuration_release.acknowledge"}, storage.ListPage{Limit: 10})
	if err != nil || len(events) != 1 {
		t.Fatalf("audit events=%+v err=%v", events, err)
	}
	if !strings.Contains(events[0].Metadata, `"divergent":"true"`) {
		t.Fatalf("audit metadata missing divergent flag: %s", events[0].Metadata)
	}
}
