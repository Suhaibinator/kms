package core

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Suhaibinator/kms/internal/crypto"
	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/storage"
)

// newConsoleTestService is a ready service over the real schema-v2 store with
// a keyring attached so secrets, releases and applications all work.
func newConsoleTestService(t *testing.T) (*Service, *storage.SQLStore) {
	t.Helper()
	st, err := storage.Open(filepath.Join(t.TempDir(), "kms.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	kek, err := crypto.NewKEKFromMaterial("kek-test", make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	keyCheck, err := crypto.NewKeyCheck(kek)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.InsertKeyMetadata(context.Background(), domain.KeyMetadata{ID: "kek-test", Source: domain.KeySourceFile, KeyCheck: keyCheck, State: domain.KeyStateActive, CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	svc := New(st, nil, "test")
	svc.SetKeyring(crypto.NewKeyring(kek))
	return svc, st
}

const consoleSchema = `{"type":"object","properties":{"database":{"type":"object"},"rate_limits":{"type":"integer","minimum":0}},"required":["database","rate_limits"],"additionalProperties":false}`

// seedConsoleApp creates application gradethis (contract database/json,
// rate_limits/integer, db_password/secret; schema pinned) with a dev
// environment holding one version of each resource.
func seedConsoleApp(t *testing.T, svc *Service, pr Principal, envs ...string) domain.Application {
	t.Helper()
	ctx := context.Background()
	schema, err := svc.CreateConfigurationSchema(ctx, pr, "runtime", consoleSchema, "{}")
	if err != nil {
		t.Fatal(err)
	}
	app, err := svc.CreateApplication(ctx, pr, domain.Application{Name: "gradethis", ReleaseName: "runtime", SchemaID: schema.ID, SchemaVersion: schema.Version, Contract: []domain.ApplicationContractField{
		{Alias: "database", Kind: domain.ReleaseEntryParameter, ContentType: "json"},
		{Alias: "rate_limits", Kind: domain.ReleaseEntryParameter, ContentType: "integer"},
		{Alias: "db_password", Kind: domain.ReleaseEntrySecret},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(envs) == 0 {
		envs = []string{"dev"}
	}
	for _, env := range envs {
		seedConsoleEnv(t, svc, pr, env)
	}
	return app
}

func seedConsoleEnv(t *testing.T, svc *Service, pr Principal, env string) {
	t.Helper()
	ctx := context.Background()
	ns := domain.NamespaceRef{Env: env, App: "gradethis"}
	if _, err := svc.CreateNamespace(ctx, pr, ns, env+" environment", []domain.AuthMethod{domain.AuthMethodToken}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.PutParameter(ctx, pr, domain.Ref{NS: ns, Key: "database"}, `{"host":"db.internal"}`, "json", "{}"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.PutParameter(ctx, pr, domain.Ref{NS: ns, Key: "rate_limits"}, "5", "integer", "{}"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.PutSecret(ctx, pr, PutSecretInput{Ref: domain.Ref{NS: ns, Key: "db_password"}, Value: []byte("hunter2"), ContentType: "text/plain", Metadata: "{}"}); err != nil {
		t.Fatal(err)
	}
}

func str(v string) *string { return &v }

func previewEntry(t *testing.T, r domain.ShipResult, alias string) domain.ShipPreviewEntry {
	t.Helper()
	for _, e := range r.Preview.Entries {
		if e.Alias == alias {
			return e
		}
	}
	t.Fatalf("preview has no entry %s: %+v", alias, r.Preview.Entries)
	return domain.ShipPreviewEntry{}
}

func TestShipApplicationChangeDryRunNeverWrites(t *testing.T) {
	ctx := context.Background()
	svc, st := newConsoleTestService(t)
	pr := adminPrincipal()
	seedConsoleApp(t, svc, pr)
	ns := domain.NamespaceRef{Env: "dev", App: "gradethis"}
	rs, _ := svc.releaseStore()

	result, err := svc.ShipApplicationChange(ctx, pr, domain.ShipInput{Application: "gradethis", Environment: "dev", DryRun: true, Changes: []domain.ShipChange{{Alias: "rate_limits", Value: str("7")}}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != domain.ShipStatusPreview || len(result.Preview.Validation) != 0 || result.Preview.BaseVersion != 0 || result.Preview.ReleaseName != "runtime" || result.Preview.SchemaID != "runtime" {
		t.Fatalf("preview = %+v", result)
	}
	if e := previewEntry(t, result, "rate_limits"); e.Change != domain.ShipEntryEdited || e.ToVersion != 2 || e.FromVersion != 0 || e.Key != "rate_limits" {
		t.Fatalf("edited entry = %+v", e)
	}
	if e := previewEntry(t, result, "database"); e.Change != domain.ShipEntryIncluded || e.ToVersion != 1 {
		t.Fatalf("included entry = %+v", e)
	}
	if e := previewEntry(t, result, "db_password"); e.Change != domain.ShipEntryIncluded || e.Kind != domain.ReleaseEntrySecret || e.ToVersion != 1 {
		t.Fatalf("secret entry = %+v", e)
	}
	// Schema sees the unsaved value.
	bad, err := svc.ShipApplicationChange(ctx, pr, domain.ShipInput{Application: "gradethis", Environment: "dev", DryRun: true, Changes: []domain.ShipChange{{Alias: "rate_limits", Value: str("-1")}}})
	if err != nil {
		t.Fatal(err)
	}
	if bad.Status != domain.ShipStatusPreview || len(bad.Preview.Validation) != 1 || bad.Preview.Validation[0].Alias != "rate_limits" || bad.Preview.Validation[0].Code != domain.ReleaseValidationSchema {
		t.Fatalf("invalid preview = %+v", bad.Preview.Validation)
	}
	// Nothing was written by either dry run.
	if p, err := st.GetParameter(ctx, domain.Ref{NS: ns, Key: "rate_limits"}, 0, domain.LabelCurrent); err != nil || p.Version != 1 || p.Value != "5" {
		t.Fatalf("dry run wrote a parameter: %+v err=%v", p, err)
	}
	if n, _ := rs.CountConfigurationReleases(ctx, ns, ""); n != 0 {
		t.Fatalf("dry run created %d releases", n)
	}
	app, _ := svc.GetApplication(ctx, pr, "gradethis")
	if len(app.Contract) != 3 {
		t.Fatalf("contract changed: %+v", app.Contract)
	}
	events, _, err := st.ListAudit(ctx, domain.AuditFilter{EventType: "application.ship"}, storage.ListPage{Limit: 10})
	if err != nil || len(events) != 0 {
		t.Fatalf("dry run audited: %d events err=%v", len(events), err)
	}
}

func TestShipApplicationChangeExecuteAndPinOptIn(t *testing.T) {
	ctx := context.Background()
	svc, st := newConsoleTestService(t)
	pr := adminPrincipal()
	seedConsoleApp(t, svc, pr)
	ns := domain.NamespaceRef{Env: "dev", App: "gradethis"}
	rs, _ := svc.releaseStore()

	result, err := svc.ShipApplicationChange(ctx, pr, domain.ShipInput{Application: "gradethis", Environment: "dev", Changes: []domain.ShipChange{{Alias: "rate_limits", Value: str("7")}}, Metadata: `{"ticket":"KMS-1"}`})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != domain.ShipStatusActivated || result.Release == nil || result.Release.Version != 1 || result.Activation == nil || !result.Activation.Changed || result.Activation.ActivationRevision == 0 {
		t.Fatalf("ship = %+v", result)
	}
	if len(result.Parameters) != 1 || result.Parameters[0].Alias != "rate_limits" || result.Parameters[0].Version != 2 || result.Parameters[0].Key != "rate_limits" {
		t.Fatalf("parameters = %+v", result.Parameters)
	}
	if e := previewEntry(t, result, "rate_limits"); e.ToVersion != 2 {
		t.Fatalf("preview not updated with the written version: %+v", e)
	}
	if !strings.Contains(result.Release.Metadata, `"source":"console.ship"`) || !strings.Contains(result.Release.Metadata, `"ticket":"KMS-1"`) {
		t.Fatalf("release metadata = %s", result.Release.Metadata)
	}
	active, err := rs.GetActiveConfigurationRelease(ctx, ns, "runtime")
	if err != nil || active.Release.Version != 1 {
		t.Fatalf("active = %+v err=%v", active, err)
	}
	for _, entry := range active.Release.Entries {
		if entry.Alias == "rate_limits" && entry.Version != 2 {
			t.Fatalf("active pins rate_limits@%d, want 2", entry.Version)
		}
	}

	// An unreleased newer version is reported, not silently included.
	if _, _, err := svc.PutParameter(ctx, pr, domain.Ref{NS: ns, Key: "rate_limits"}, "9", "integer", "{}"); err != nil {
		t.Fatal(err)
	}
	preview, err := svc.ShipApplicationChange(ctx, pr, domain.ShipInput{Application: "gradethis", Environment: "dev", DryRun: true, Changes: []domain.ShipChange{{Alias: "database", Value: str(`{"host":"db2"}`)}}})
	if err != nil {
		t.Fatal(err)
	}
	if preview.Preview.BaseVersion != 1 {
		t.Fatalf("base version = %d", preview.Preview.BaseVersion)
	}
	if e := previewEntry(t, preview, "rate_limits"); e.Change != domain.ShipEntryIncluded || e.FromVersion != 2 || e.ToVersion != 2 {
		t.Fatalf("unreleased alias must keep the active pin: %+v", e)
	}
	if len(preview.Preview.Warnings) != 1 || preview.Preview.Warnings[0].Code != domain.FindingUnreleasedChanges || preview.Preview.Warnings[0].Params["current"] != uint64(3) || preview.Preview.Warnings[0].Params["pinned"] != uint64(2) {
		t.Fatalf("warnings = %+v", preview.Preview.Warnings)
	}
	// Opt in by pinning the newer version explicitly, guarded by CAS.
	expected := uint64(1)
	shipped, err := svc.ShipApplicationChange(ctx, pr, domain.ShipInput{Application: "gradethis", Environment: "dev", ExpectedActiveVersion: &expected, Changes: []domain.ShipChange{
		{Alias: "database", Value: str(`{"host":"db2"}`)}, {Alias: "rate_limits", Version: 3},
	}})
	if err != nil || shipped.Status != domain.ShipStatusActivated || shipped.Release.Version != 2 || shipped.Activation.PreviousVersion != 1 {
		t.Fatalf("opt-in ship = %+v err=%v", shipped, err)
	}
	if e := previewEntry(t, shipped, "rate_limits"); e.Change != domain.ShipEntryPinned || e.FromVersion != 2 || e.ToVersion != 3 {
		t.Fatalf("pinned entry = %+v", e)
	}
	active, _ = rs.GetActiveConfigurationRelease(ctx, ns, "runtime")
	for _, entry := range active.Release.Entries {
		if entry.Alias == "rate_limits" && entry.Version != 3 {
			t.Fatalf("active pins rate_limits@%d, want 3", entry.Version)
		}
	}
	stale := uint64(1)
	if _, err := svc.ShipApplicationChange(ctx, pr, domain.ShipInput{Application: "gradethis", Environment: "dev", ExpectedActiveVersion: &stale, Changes: []domain.ShipChange{{Alias: "rate_limits", Value: str("1")}}}); !errors.Is(err, domain.ErrAborted) {
		t.Fatalf("stale expected_active_version error = %v", err)
	}
	events, _, err := st.ListAudit(ctx, domain.AuditFilter{EventType: "application.ship"}, storage.ListPage{Limit: 10})
	if err != nil || len(events) != 2 {
		t.Fatalf("ship audit events = %d err=%v", len(events), err)
	}
	for _, ev := range events {
		if ev.Decision != "allow" || !strings.Contains(ev.Metadata, `"activated":"true"`) || strings.Contains(ev.Metadata, "db2") {
			t.Fatalf("audit event = %+v", ev)
		}
	}
}

func TestShipApplicationChangeRejectedWritesNothing(t *testing.T) {
	ctx := context.Background()
	svc, st := newConsoleTestService(t)
	pr := adminPrincipal()
	seedConsoleApp(t, svc, pr)
	ns := domain.NamespaceRef{Env: "dev", App: "gradethis"}
	rs, _ := svc.releaseStore()

	result, err := svc.ShipApplicationChange(ctx, pr, domain.ShipInput{Application: "gradethis", Environment: "dev", Changes: []domain.ShipChange{{Alias: "rate_limits", Value: str("-3")}}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != domain.ShipStatusRejected || result.Error == nil || len(result.Error.ValidationErrors) != 1 || result.Release != nil || len(result.Parameters) != 0 {
		t.Fatalf("rejected ship = %+v", result)
	}
	if p, _ := st.GetParameter(ctx, domain.Ref{NS: ns, Key: "rate_limits"}, 0, domain.LabelCurrent); p.Version != 1 {
		t.Fatalf("rejected ship wrote parameter v%d", p.Version)
	}
	if n, _ := rs.CountConfigurationReleases(ctx, ns, ""); n != 0 {
		t.Fatalf("rejected ship created %d releases", n)
	}
	events, _, _ := st.ListAudit(ctx, domain.AuditFilter{EventType: "application.ship"}, storage.ListPage{Limit: 10})
	if len(events) != 1 || events[0].Decision != "deny" {
		t.Fatalf("rejected ship audit = %+v", events)
	}
}

func TestShipApplicationChangePreflight(t *testing.T) {
	ctx := context.Background()
	svc, _ := newConsoleTestService(t)
	pr := adminPrincipal()
	seedConsoleApp(t, svc, pr)
	ship := func(changes ...domain.ShipChange) error {
		_, err := svc.ShipApplicationChange(ctx, pr, domain.ShipInput{Application: "gradethis", Environment: "dev", DryRun: true, Changes: changes})
		return err
	}
	cases := map[string]struct {
		changes []domain.ShipChange
		want    error
	}{
		"no changes":            {nil, domain.ErrInvalidArgument},
		"unknown alias":         {[]domain.ShipChange{{Alias: "nope", Value: str("1")}}, domain.ErrInvalidArgument},
		"secret value":          {[]domain.ShipChange{{Alias: "db_password", Value: str("x")}}, domain.ErrInvalidArgument},
		"value and version":     {[]domain.ShipChange{{Alias: "rate_limits", Value: str("1"), Version: 1}}, domain.ErrInvalidArgument},
		"neither":               {[]domain.ShipChange{{Alias: "rate_limits"}}, domain.ErrInvalidArgument},
		"content type mismatch": {[]domain.ShipChange{{Alias: "rate_limits", Value: str("1"), ContentType: "string"}}, domain.ErrInvalidArgument},
		"malformed value":       {[]domain.ShipChange{{Alias: "rate_limits", Value: str("abc")}}, domain.ErrInvalidArgument},
		"duplicate alias":       {[]domain.ShipChange{{Alias: "rate_limits", Value: str("1")}, {Alias: "rate_limits", Version: 1}}, domain.ErrInvalidArgument},
		"secret pin ok":         {[]domain.ShipChange{{Alias: "db_password", Version: 1}}, nil},
	}
	for name, c := range cases {
		err := ship(c.changes...)
		if c.want == nil && err != nil {
			t.Errorf("%s: unexpected error %v", name, err)
		}
		if c.want != nil && !errors.Is(err, c.want) {
			t.Errorf("%s: error = %v, want %v", name, err, c.want)
		}
	}
	if _, err := svc.ShipApplicationChange(ctx, pr, domain.ShipInput{Application: "gradethis", Environment: "staging", DryRun: true, Changes: []domain.ShipChange{{Alias: "rate_limits", Value: str("1")}}}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing namespace error = %v", err)
	}
	if _, err := svc.ShipApplicationChange(ctx, clientPrincipal("client"), domain.ShipInput{Application: "gradethis", Environment: "dev", DryRun: true, Changes: []domain.ShipChange{{Alias: "rate_limits", Value: str("1")}}}); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Fatalf("non-admin error = %v", err)
	}
	if _, err := svc.CreateApplication(ctx, pr, domain.Application{Name: "empty", ReleaseName: "runtime"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ShipApplicationChange(ctx, pr, domain.ShipInput{Application: "empty", Environment: "dev", DryRun: true, Changes: []domain.ShipChange{{Alias: "x", Value: str("1")}}}); !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Fatalf("empty contract error = %v", err)
	}
}

func TestShipApplicationChangeFirstReleaseWithMissingAlias(t *testing.T) {
	ctx := context.Background()
	svc, _ := newConsoleTestService(t)
	pr := adminPrincipal()
	seedConsoleApp(t, svc, pr)
	ns := domain.NamespaceRef{Env: "staging", App: "gradethis"}
	if _, err := svc.CreateNamespace(ctx, pr, ns, "", []domain.AuthMethod{domain.AuthMethodToken}); err != nil {
		t.Fatal(err)
	}
	// Nothing exists in staging: edited aliases become new keys, the rest are missing.
	result, err := svc.ShipApplicationChange(ctx, pr, domain.ShipInput{Application: "gradethis", Environment: "staging", DryRun: true, Changes: []domain.ShipChange{{Alias: "rate_limits", Value: str("1")}}})
	if err != nil {
		t.Fatal(err)
	}
	if e := previewEntry(t, result, "rate_limits"); e.Change != domain.ShipEntryEdited || e.ToVersion != 1 || e.Key != "rate_limits" {
		t.Fatalf("new key entry = %+v", e)
	}
	if e := previewEntry(t, result, "database"); e.Change != domain.ShipEntryMissing {
		t.Fatalf("missing entry = %+v", e)
	}
	codes := map[string]string{}
	for _, v := range result.Preview.Validation {
		codes[v.Alias] = v.Code
	}
	if codes["database"] != domain.ReleaseValidationNotFound || codes["db_password"] != domain.ReleaseValidationNotFound || len(codes) != 2 {
		t.Fatalf("validation = %+v", result.Preview.Validation)
	}
}
