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
	"github.com/Suhaibinator/kms/sdk/go/configstore"
)

// verifyFixture is a real-SQLite application with an active release that pins
// several parameters and one secret, so every verdict can be exercised.
type verifyFixture struct {
	st      *storage.SQLStore
	svc     *Service
	admin   Principal
	ns      domain.NamespaceRef
	release domain.ActiveConfigurationRelease
	schema  domain.ConfigurationSchema
}

const (
	verifyPrettyJSON = "{\n  \"b\": 1,\n  \"a\": 2\n}"
	verifyCanonical  = `{"a":2,"b":1}`
)

func newVerifyFixture(t *testing.T) *verifyFixture {
	t.Helper()
	ctx := context.Background()
	st, err := storage.Open(filepath.Join(t.TempDir(), "kms.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ns := domain.NamespaceRef{Env: "prod", App: "app"}
	if _, err := st.CreateNamespace(ctx, domain.Namespace{NamespaceRef: ns, AllowedAuthMethods: []domain.AuthMethod{domain.AuthMethodToken}, CreatedBy: "admin"}); err != nil {
		t.Fatal(err)
	}
	put := func(key, value, contentType string) {
		t.Helper()
		if _, _, err := st.PutParameter(ctx, domain.Ref{NS: ns, Key: key}, value, contentType, "{}", "admin"); err != nil {
			t.Fatal(err)
		}
	}
	put("config/json", verifyPrettyJSON, "json")
	put("config/text", "hello", "string")
	put("config/num", "7", "integer")
	put("config/quiet", "unmentioned", "string")
	kek, err := crypto.NewKEKFromMaterial("kek-test", make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	keyCheck, err := crypto.NewKeyCheck(kek)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.InsertKeyMetadata(ctx, domain.KeyMetadata{ID: "kek-test", Source: domain.KeySourceFile, KeyCheck: keyCheck, State: domain.KeyStateActive, CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	svc := New(st, nil, "test")
	svc.SetKeyring(crypto.NewKeyring(kek))
	admin := adminPrincipal()
	if _, err := svc.PutSecret(ctx, admin, PutSecretInput{Ref: domain.Ref{NS: ns, Key: "db-password"}, Value: []byte("s3cr3t"), ContentType: "text/plain"}); err != nil {
		t.Fatal(err)
	}
	schema, err := svc.CreateConfigurationSchema(ctx, admin, "app/runtime", `{
		"type": "object",
		"properties": {"json_cfg": {"type": "object"}, "text_cfg": {"type": "string"}, "num_cfg": {"type": "integer"}, "quiet_cfg": {"type": "string"}}
	}`, "{}")
	if err != nil {
		t.Fatal(err)
	}
	rel, err := svc.CreateConfigurationRelease(ctx, admin, domain.CreateConfigurationReleaseInput{
		Namespace: ns, Name: "runtime", SchemaID: schema.ID, SchemaVersion: schema.Version,
		Entries: []domain.ReleaseEntrySelector{
			{Alias: "json_cfg", Kind: domain.ReleaseEntryParameter, Ref: domain.Ref{NS: ns, Key: "config/json"}},
			{Alias: "text_cfg", Kind: domain.ReleaseEntryParameter, Ref: domain.Ref{NS: ns, Key: "config/text"}},
			{Alias: "num_cfg", Kind: domain.ReleaseEntryParameter, Ref: domain.Ref{NS: ns, Key: "config/num"}},
			{Alias: "quiet_cfg", Kind: domain.ReleaseEntryParameter, Ref: domain.Ref{NS: ns, Key: "config/quiet"}},
			{Alias: "db_password", Kind: domain.ReleaseEntrySecret, Ref: domain.Ref{NS: ns, Key: "db-password"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	active, _, err := svc.ActivateConfigurationRelease(ctx, admin, ns, "runtime", rel.Version, nil)
	if err != nil {
		t.Fatal(err)
	}
	// The first release adopted the contract; add a contract-only alias so a
	// request can observe missing_in_release.
	app, err := st.GetApplication(ctx, ns.App)
	if err != nil {
		t.Fatal(err)
	}
	app.Contract = append(app.Contract, domain.ApplicationContractField{Alias: "future_cfg", Kind: domain.ReleaseEntryParameter, ContentType: "string"})
	if _, err := st.UpdateApplication(ctx, app); err != nil {
		t.Fatal(err)
	}
	return &verifyFixture{st: st, svc: svc, admin: admin, ns: ns, release: active, schema: schema}
}

func mustHash(t *testing.T, contentType, value string) string {
	t.Helper()
	h, err := configstore.ParameterHash(contentType, []byte(value))
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func (f *verifyFixture) verifyAudit(t *testing.T) []domain.AuditEvent {
	t.Helper()
	events, _, err := f.st.ListAudit(context.Background(), domain.AuditFilter{EventType: "configuration_release.verify_defaults"}, storage.ListPage{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	return events
}

func TestVerifyReleaseDefaultsVerdicts(t *testing.T) {
	f := newVerifyFixture(t)
	ctx := context.Background()
	wrong := strings.Repeat("0", 64)
	in := domain.VerifyReleaseDefaultsInput{
		Namespace: f.ns, Profile: "dev", SchemaSHA256: f.schema.Digest,
		Entries: []domain.VerifyDefaultsEntry{
			// Stored pretty-printed with keys out of order; the caller hashes
			// the canonical form and still matches.
			{Alias: "json_cfg", ContentType: "json", SHA256: mustHash(t, "json", verifyCanonical)},
			{Alias: "text_cfg", ContentType: "string", SHA256: wrong},
			{Alias: "num_cfg", ContentType: "string", SHA256: mustHash(t, "string", "7")},
			{Alias: "db_password", ContentType: "text/plain", SHA256: mustHash(t, "text/plain", "s3cr3t")},
			{Alias: "future_cfg", ContentType: "string", SHA256: wrong},
			{Alias: "nope", ContentType: "string", SHA256: wrong},
		},
	}
	out, err := f.svc.VerifyReleaseDefaults(ctx, f.admin, in)
	if err != nil {
		t.Fatalf("VerifyReleaseDefaults: %v", err)
	}
	if out.ReleaseName != "runtime" || out.ReleaseVersion != f.release.Release.Version || out.ActivationRevision != f.release.ActivationRevision {
		t.Fatalf("release identity = %+v", out)
	}
	if !out.SchemaMatches {
		t.Fatal("schema digest from the registry should match")
	}
	want := map[string]string{
		"json_cfg":    domain.VerifyVerdictMatch,
		"text_cfg":    domain.VerifyVerdictDiffers,
		"num_cfg":     domain.VerifyVerdictDiffers, // content-type mismatch
		"db_password": domain.VerifyVerdictSecretAlias,
		"future_cfg":  domain.VerifyVerdictMissingInRelease,
		"nope":        domain.VerifyVerdictUnknownAlias,
	}
	if len(out.Entries) != len(want) {
		t.Fatalf("entries = %+v", out.Entries)
	}
	for i, e := range out.Entries {
		if e.Alias != in.Entries[i].Alias {
			t.Fatalf("verdict order changed: %+v", out.Entries)
		}
		if e.Verdict != want[e.Alias] {
			t.Errorf("%s verdict = %s, want %s", e.Alias, e.Verdict, want[e.Alias])
		}
	}
	if out.Summary != (domain.VerifyDefaultsSummary{Match: 1, Differs: 2, MissingInRelease: 1, UnknownAlias: 1, SecretAlias: 1, Unverified: 1}) {
		t.Fatalf("summary = %+v", out.Summary)
	}

	// A wrong schema digest is reported, not rejected; an omitted digest is
	// simply not checked.
	in.SchemaSHA256 = wrong
	if out, err := f.svc.VerifyReleaseDefaults(ctx, f.admin, in); err != nil || out.SchemaMatches {
		t.Fatalf("wrong schema: matches=%v err=%v", out.SchemaMatches, err)
	}
	in.SchemaSHA256 = ""
	if out, err := f.svc.VerifyReleaseDefaults(ctx, f.admin, in); err != nil || out.SchemaMatches {
		t.Fatalf("omitted schema: matches=%v err=%v", out.SchemaMatches, err)
	}

	// Audit carries counts only.
	events := f.verifyAudit(t)
	if len(events) != 3 {
		t.Fatalf("audit events = %d, want 3", len(events))
	}
	first := events[len(events)-1]
	if first.Decision != "allow" || first.ResourceKey != "runtime" || first.ResourceVersion != f.release.Release.Version {
		t.Fatalf("audit event = %+v", first)
	}
	for _, want := range []string{`"entry_count":"6"`, `"match_count":"1"`, `"differs_count":"2"`, `"missing_count":"1"`, `"unknown_alias_count":"1"`, `"secret_alias_count":"1"`, `"unsupported_count":"0"`, `"unverified_count":"1"`, `"schema_matches":"true"`, `"limited":"false"`} {
		if !strings.Contains(first.Metadata, want) {
			t.Errorf("audit metadata missing %s: %s", want, first.Metadata)
		}
	}
	for _, ev := range events {
		for _, forbidden := range []string{"json_cfg", "text_cfg", "db_password", "nope", "future_cfg", "dev", f.schema.Digest, verifyCanonical, "s3cr3t", wrong} {
			if strings.Contains(ev.Metadata, forbidden) {
				t.Errorf("audit metadata leaked %q: %s", forbidden, ev.Metadata)
			}
		}
	}
}

func TestVerifyReleaseDefaultsCrossNamespaceEntry(t *testing.T) {
	f := newVerifyFixture(t)
	ctx := context.Background()
	shared := domain.NamespaceRef{Env: "shared", App: "platform"}
	if _, err := f.st.CreateNamespace(ctx, domain.Namespace{NamespaceRef: shared, CreatedBy: "admin"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.st.PutParameter(ctx, domain.Ref{NS: shared, Key: "flags"}, `{"x":true}`, "json", "{}", "admin"); err != nil {
		t.Fatal(err)
	}
	other := domain.NamespaceRef{Env: "stage", App: "xapp"}
	if _, err := f.st.CreateNamespace(ctx, domain.Namespace{NamespaceRef: other, CreatedBy: "admin"}); err != nil {
		t.Fatal(err)
	}
	rel, err := f.svc.CreateConfigurationRelease(ctx, f.admin, domain.CreateConfigurationReleaseInput{
		Namespace: other, Name: "runtime",
		Entries: []domain.ReleaseEntrySelector{{Alias: "flags", Kind: domain.ReleaseEntryParameter, Ref: domain.Ref{NS: shared, Key: "flags"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.svc.ActivateConfigurationRelease(ctx, f.admin, other, "runtime", rel.Version, nil); err != nil {
		t.Fatal(err)
	}
	out, err := f.svc.VerifyReleaseDefaults(ctx, f.admin, domain.VerifyReleaseDefaultsInput{
		Namespace: other,
		Entries:   []domain.VerifyDefaultsEntry{{Alias: "flags", ContentType: "json", SHA256: mustHash(t, "json", `{ "x" : true }`)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Summary.Match != 1 || out.Entries[0].Verdict != domain.VerifyVerdictMatch {
		t.Fatalf("cross-namespace entry = %+v", out)
	}
}

func TestVerifyReleaseDefaultsValidation(t *testing.T) {
	f := newVerifyFixture(t)
	ctx := context.Background()
	good := mustHash(t, "string", "hello")
	// A client with no policy: validation must fail before authorization, so
	// invalid input is reported as InvalidArgument rather than PermissionDenied.
	unprivileged := clientPrincipal("nobody")
	entry := func(alias, sha string) []domain.VerifyDefaultsEntry {
		return []domain.VerifyDefaultsEntry{{Alias: alias, ContentType: "string", SHA256: sha}}
	}
	tooMany := make([]domain.VerifyDefaultsEntry, 257)
	for i := range tooMany {
		tooMany[i] = domain.VerifyDefaultsEntry{Alias: "a" + strings.Repeat("b", i%50) + string(rune('a'+i%26)) + itoa(i), ContentType: "string", SHA256: good}
	}
	cases := []struct {
		name string
		in   domain.VerifyReleaseDefaultsInput
	}{
		{"uppercase hex", domain.VerifyReleaseDefaultsInput{Namespace: f.ns, Entries: entry("text_cfg", strings.ToUpper(good))}},
		{"short hex", domain.VerifyReleaseDefaultsInput{Namespace: f.ns, Entries: entry("text_cfg", good[:63])}},
		{"non hex", domain.VerifyReleaseDefaultsInput{Namespace: f.ns, Entries: entry("text_cfg", strings.Repeat("z", 64))}},
		{"empty alias", domain.VerifyReleaseDefaultsInput{Namespace: f.ns, Entries: entry("", good)}},
		{"long alias", domain.VerifyReleaseDefaultsInput{Namespace: f.ns, Entries: entry(strings.Repeat("a", 65), good)}},
		{"duplicate alias", domain.VerifyReleaseDefaultsInput{Namespace: f.ns, Entries: append(entry("text_cfg", good), entry("text_cfg", good)...)}},
		{"too many entries", domain.VerifyReleaseDefaultsInput{Namespace: f.ns, Entries: tooMany}},
		{"bad schema sha", domain.VerifyReleaseDefaultsInput{Namespace: f.ns, SchemaSHA256: strings.ToUpper(good), Entries: entry("text_cfg", good)}},
		{"long profile", domain.VerifyReleaseDefaultsInput{Namespace: f.ns, Profile: strings.Repeat("p", 65), Entries: entry("text_cfg", good)}},
		{"bad namespace", domain.VerifyReleaseDefaultsInput{Namespace: domain.NamespaceRef{Env: "prod"}, Entries: entry("text_cfg", good)}},
		{"bad release name", domain.VerifyReleaseDefaultsInput{Namespace: f.ns, ReleaseName: "bad name!", Entries: entry("text_cfg", good)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := f.svc.VerifyReleaseDefaults(ctx, unprivileged, tc.in)
			if !errors.Is(err, domain.ErrInvalidArgument) {
				t.Fatalf("err = %v, want invalid argument", err)
			}
		})
	}
	if len(f.verifyAudit(t)) != 0 {
		t.Fatal("rejected input must not produce a verify audit event")
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func TestVerifyReleaseDefaultsNotFound(t *testing.T) {
	f := newVerifyFixture(t)
	ctx := context.Background()
	good := mustHash(t, "string", "hello")
	// A release name that has never been activated.
	_, err := f.svc.VerifyReleaseDefaults(ctx, f.admin, domain.VerifyReleaseDefaultsInput{Namespace: f.ns, ReleaseName: "other", Entries: []domain.VerifyDefaultsEntry{{Alias: "text_cfg", ContentType: "string", SHA256: good}}})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v, want not found", err)
	}
	events := f.verifyAudit(t)
	if len(events) != 1 || events[0].Decision != "error" || events[0].ResourceKey != "other" {
		t.Fatalf("audit = %+v, want one error decision", events)
	}
	// Legacy namespace-less lookups must not be reachable either: an unknown
	// namespace is NotFound as well.
	_, err = f.svc.VerifyReleaseDefaults(ctx, f.admin, domain.VerifyReleaseDefaultsInput{Namespace: domain.NamespaceRef{Env: "prod", App: "ghost"}, Entries: []domain.VerifyDefaultsEntry{{Alias: "text_cfg", ContentType: "string", SHA256: good}}})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("ghost namespace err = %v, want not found", err)
	}
}

func TestVerifyReleaseDefaultsAuthorization(t *testing.T) {
	f := newVerifyFixture(t)
	ctx := context.Background()
	in := domain.VerifyReleaseDefaultsInput{Namespace: f.ns, Entries: []domain.VerifyDefaultsEntry{{Alias: "text_cfg", ContentType: "string", SHA256: mustHash(t, "string", "hello")}}}

	// Unbound client without a rule: denied and audited.
	ci := clientPrincipal("ci")
	if _, err := f.svc.VerifyReleaseDefaults(ctx, ci, in); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Fatalf("no policy err = %v, want permission denied", err)
	}
	// Bound to the home namespace: still denied (no implicit grant).
	bound := boundClientPrincipal("svc", f.ns)
	if _, err := f.svc.VerifyReleaseDefaults(ctx, bound, in); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Fatalf("home-bound err = %v, want permission denied", err)
	}
	denials, _, err := f.st.ListAudit(ctx, domain.AuditFilter{EventType: "authz.denial"}, storage.ListPage{Limit: 10})
	if err != nil || len(denials) != 2 {
		t.Fatalf("authz.denial events = %+v err=%v", denials, err)
	}
	for _, d := range denials {
		if !strings.Contains(d.Metadata, domain.OpConfigurationReleaseVerifyDefaults) || d.Decision != "deny" {
			t.Fatalf("denial = %+v", d)
		}
	}
	if len(f.verifyAudit(t)) != 0 {
		t.Fatal("denied calls must not produce a verify audit event")
	}

	// An explicit allow rule for the operation is enough.
	if _, err := f.st.CreatePolicy(ctx, domain.Policy{Name: "ci-verify", Subject: "ci", Allow: []domain.PolicyRule{{Operation: domain.OpConfigurationReleaseVerifyDefaults, Env: f.ns.Env, App: f.ns.App}}}); err != nil {
		t.Fatal(err)
	}
	out, err := f.svc.VerifyReleaseDefaults(ctx, ci, in)
	if err != nil || out.Summary.Match != 1 {
		t.Fatalf("allowed client: out=%+v err=%v", out, err)
	}
	// ...but only for that namespace.
	other := in
	other.Namespace = domain.NamespaceRef{Env: "stage", App: "app"}
	if _, err := f.st.CreateNamespace(ctx, domain.Namespace{NamespaceRef: other.Namespace, AllowedAuthMethods: []domain.AuthMethod{domain.AuthMethodToken}}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.VerifyReleaseDefaults(ctx, ci, other); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Fatalf("other namespace err = %v, want permission denied", err)
	}
}

func TestVerifyReleaseDefaultsBudgets(t *testing.T) {
	f := newVerifyFixture(t)
	ctx := context.Background()
	wrong := strings.Repeat("1", 64)
	in := domain.VerifyReleaseDefaultsInput{Namespace: f.ns, Entries: []domain.VerifyDefaultsEntry{
		{Alias: "text_cfg", ContentType: "string", SHA256: wrong},
		{Alias: "num_cfg", ContentType: "integer", SHA256: wrong},
	}}

	t.Run("mismatch budget", func(t *testing.T) {
		// Admins are limited too. Three non-match verdicts per hour; the first
		// call spends two, the second needs two more and is refused as a whole.
		f.svc.SetVerifyDefaultsLimits(VerifyDefaultsLimits{RequestsPerHour: 1000, Burst: 1000, MismatchBudgetPerHour: 3})
		if out, err := f.svc.VerifyReleaseDefaults(ctx, f.admin, in); err != nil || out.Summary.Differs != 2 {
			t.Fatalf("first: out=%+v err=%v", out, err)
		}
		out, err := f.svc.VerifyReleaseDefaults(ctx, f.admin, in)
		if !errors.Is(err, domain.ErrResourceExhausted) {
			t.Fatalf("second err = %v, want resource exhausted", err)
		}
		if len(out.Entries) != 0 || out.ReleaseName != "" {
			t.Fatalf("limited response leaked verdicts: %+v", out)
		}
		// A single remaining token still serves a one-mismatch request, and
		// matches never cost anything.
		one := domain.VerifyReleaseDefaultsInput{Namespace: f.ns, Entries: []domain.VerifyDefaultsEntry{
			{Alias: "text_cfg", ContentType: "string", SHA256: mustHash(t, "string", "hello")},
			{Alias: "num_cfg", ContentType: "integer", SHA256: wrong},
		}}
		if out, err := f.svc.VerifyReleaseDefaults(ctx, f.admin, one); err != nil || out.Summary.Match != 1 || out.Summary.Differs != 1 {
			t.Fatalf("one-mismatch: out=%+v err=%v", out, err)
		}
		allMatch := domain.VerifyReleaseDefaultsInput{Namespace: f.ns, Entries: []domain.VerifyDefaultsEntry{
			{Alias: "text_cfg", ContentType: "string", SHA256: mustHash(t, "string", "hello")},
		}}
		if out, err := f.svc.VerifyReleaseDefaults(ctx, f.admin, allMatch); err != nil || out.Summary.Match != 1 {
			t.Fatalf("all-match with empty mismatch budget: out=%+v err=%v", out, err)
		}
		// Budgets are per identity: another identity has its own bucket.
		if _, err := f.st.CreatePolicy(ctx, domain.Policy{Name: "ci-verify", Subject: "ci", Allow: []domain.PolicyRule{{Operation: domain.OpConfigurationReleaseVerifyDefaults, Env: "*", App: "*"}}}); err != nil {
			t.Fatal(err)
		}
		if out, err := f.svc.VerifyReleaseDefaults(ctx, clientPrincipal("ci"), in); err != nil || out.Summary.Differs != 2 {
			t.Fatalf("other identity: out=%+v err=%v", out, err)
		}
	})

	t.Run("request budget", func(t *testing.T) {
		f.svc.SetVerifyDefaultsLimits(VerifyDefaultsLimits{RequestsPerHour: 1, Burst: 2, MismatchBudgetPerHour: 1000})
		for i := 0; i < 2; i++ {
			if _, err := f.svc.VerifyReleaseDefaults(ctx, f.admin, in); err != nil {
				t.Fatalf("burst call %d: %v", i+1, err)
			}
		}
		out, err := f.svc.VerifyReleaseDefaults(ctx, f.admin, in)
		if !errors.Is(err, domain.ErrResourceExhausted) || len(out.Entries) != 0 {
			t.Fatalf("third call: out=%+v err=%v, want resource exhausted", out, err)
		}
	})

	limited := 0
	for _, ev := range f.verifyAudit(t) {
		if !strings.Contains(ev.Metadata, `"limited":"true"`) {
			continue
		}
		limited++
		if ev.Decision != "deny" {
			t.Fatalf("limited audit decision = %q, want deny: %+v", ev.Decision, ev)
		}
	}
	if limited != 2 {
		t.Fatalf("limited audit events = %d, want 2", limited)
	}
}

// unsupported_content_type is reachable only when a pinned json parameter
// cannot be canonicalized. Activation refuses malformed json, so drive the
// per-entry verdict directly against a lenient fake store.
func TestVerifyDefaultsEntryUnsupportedContentType(t *testing.T) {
	store := newFakeStore()
	s := newTestService(store)
	ctx := context.Background()
	ns := domain.NamespaceRef{Env: "prod", App: "app"}
	ref := domain.Ref{NS: ns, Key: "broken"}
	const raw = `{"unterminated":`
	if _, _, err := store.PutParameter(ctx, ref, raw, "json", "{}", "admin"); err != nil {
		t.Fatal(err)
	}
	entries := map[string]domain.ConfigurationReleaseEntry{
		"broken": {Alias: "broken", Kind: domain.ReleaseEntryParameter, Ref: ref, Version: 1, ContentType: "json", ParameterDigest: sha256Hex([]byte(raw))},
	}
	verdict, err := s.verifyDefaultsEntry(ctx, domain.VerifyDefaultsEntry{Alias: "broken", ContentType: "json", SHA256: strings.Repeat("0", 64)}, entries, nil)
	if err != nil || verdict != domain.VerifyVerdictUnsupportedContentType {
		t.Fatalf("verdict = %q err=%v", verdict, err)
	}
	// A pin whose stored bytes drifted from the release digest is differs, and
	// a pin whose parameter vanished is missing_in_release.
	entries["broken"] = domain.ConfigurationReleaseEntry{Alias: "broken", Kind: domain.ReleaseEntryParameter, Ref: ref, Version: 1, ContentType: "json", ParameterDigest: "stale"}
	if verdict, err := s.verifyDefaultsEntry(ctx, domain.VerifyDefaultsEntry{Alias: "broken", ContentType: "json", SHA256: strings.Repeat("0", 64)}, entries, nil); err != nil || verdict != domain.VerifyVerdictDiffers {
		t.Fatalf("digest drift verdict = %q err=%v", verdict, err)
	}
	entries["gone"] = domain.ConfigurationReleaseEntry{Alias: "gone", Kind: domain.ReleaseEntryParameter, Ref: domain.Ref{NS: ns, Key: "gone"}, Version: 1, ContentType: "string"}
	if verdict, err := s.verifyDefaultsEntry(ctx, domain.VerifyDefaultsEntry{Alias: "gone", ContentType: "string", SHA256: strings.Repeat("0", 64)}, entries, nil); err != nil || verdict != domain.VerifyVerdictMissingInRelease {
		t.Fatalf("vanished parameter verdict = %q err=%v", verdict, err)
	}
}
