package core

import (
	"context"
	"errors"
	"testing"

	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/storage"
)

func cloneItem(t *testing.T, r domain.CloneEnvironmentResult, alias string) domain.CloneEnvironmentItem {
	t.Helper()
	for _, item := range r.Items {
		if item.Alias == alias {
			return item
		}
	}
	t.Fatalf("clone result has no item %s: %+v", alias, r.Items)
	return domain.CloneEnvironmentItem{}
}

func TestCloneApplicationEnvironment(t *testing.T) {
	ctx := context.Background()
	svc, st := newConsoleTestService(t)
	pr := adminPrincipal()
	seedConsoleApp(t, svc, pr)

	result, err := svc.CloneApplicationEnvironment(ctx, pr, domain.CloneEnvironmentInput{Application: "gradethis", SourceEnv: "dev", TargetEnv: "prod", CopyValues: true, Description: "Production"})
	if err != nil {
		t.Fatal(err)
	}
	if !result.NamespaceCreated || result.Namespace.Env != "prod" || result.Namespace.Description != "Production" || len(result.Namespace.AllowedAuthMethods) != 1 || result.Namespace.AllowedAuthMethods[0] != domain.AuthMethodToken {
		t.Fatalf("namespace = %+v created=%v", result.Namespace, result.NamespaceCreated)
	}
	if db := cloneItem(t, result, "database"); db.Action != domain.CloneItemCopied || db.SourceVersion != 1 || db.TargetVersion != 1 || db.Key != "database" {
		t.Fatalf("database item = %+v", db)
	}
	if rl := cloneItem(t, result, "rate_limits"); rl.Action != domain.CloneItemCopied {
		t.Fatalf("rate_limits item = %+v", rl)
	}
	if pw := cloneItem(t, result, "db_password"); pw.Action != domain.CloneItemNeedsValue || pw.Kind != domain.ReleaseEntrySecret || pw.SourceVersion != 1 {
		t.Fatalf("secret item = %+v", pw)
	}
	if len(result.NeedsValue) != 1 || result.NeedsValue[0] != "db_password" {
		t.Fatalf("needs_value = %v", result.NeedsValue)
	}
	prod := domain.NamespaceRef{Env: "prod", App: "gradethis"}
	if p, err := st.GetParameter(ctx, domain.Ref{NS: prod, Key: "database"}, 0, domain.LabelCurrent); err != nil || p.Value != `{"host":"db.internal"}` || p.ContentType != "json" {
		t.Fatalf("copied parameter = %+v err=%v", p, err)
	}
	if _, _, err := st.GetSecretVersion(ctx, domain.Ref{NS: prod, Key: "db_password"}, 0, domain.LabelCurrent); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("secret must never be copied: err=%v", err)
	}

	// A second clone attaches the existing namespace and never overwrites.
	again, err := svc.CloneApplicationEnvironment(ctx, pr, domain.CloneEnvironmentInput{Application: "gradethis", SourceEnv: "dev", TargetEnv: "prod", CopyValues: true})
	if err != nil {
		t.Fatal(err)
	}
	if again.NamespaceCreated || cloneItem(t, again, "database").Action != domain.CloneItemExists || cloneItem(t, again, "database").TargetVersion != 1 {
		t.Fatalf("second clone = %+v", again)
	}
	if p, _ := st.GetParameter(ctx, domain.Ref{NS: prod, Key: "database"}, 0, domain.LabelCurrent); p.Version != 1 {
		t.Fatalf("existing key was overwritten: v%d", p.Version)
	}

	// Without copy_values parameters become needs_value; a source gap is reported.
	if _, err := st.CreateNamespace(ctx, domain.Namespace{Env: "qa", App: "gradethis", CreatedBy: "admin", AllowedAuthMethods: []domain.AuthMethod{domain.AuthMethodToken}}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.PutParameter(ctx, domain.Ref{NS: domain.NamespaceRef{Env: "qa", App: "gradethis"}, Key: "database"}, `{}`, "json", "{}", "admin"); err != nil {
		t.Fatal(err)
	}
	sparse, err := svc.CloneApplicationEnvironment(ctx, pr, domain.CloneEnvironmentInput{Application: "gradethis", SourceEnv: "qa", TargetEnv: "staging", CopyValues: false})
	if err != nil {
		t.Fatal(err)
	}
	if cloneItem(t, sparse, "database").Action != domain.CloneItemNeedsValue || cloneItem(t, sparse, "rate_limits").Action != domain.CloneItemMissingInSource || cloneItem(t, sparse, "db_password").Action != domain.CloneItemNeedsValue {
		t.Fatalf("sparse clone = %+v", sparse.Items)
	}
	if len(sparse.NeedsValue) != 2 {
		t.Fatalf("needs_value = %v", sparse.NeedsValue)
	}

	for _, in := range []domain.CloneEnvironmentInput{
		{Application: "gradethis", SourceEnv: "dev", TargetEnv: "dev"},
		{Application: "gradethis", SourceEnv: "Bad Env", TargetEnv: "x"},
	} {
		if _, err := svc.CloneApplicationEnvironment(ctx, pr, in); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Fatalf("clone %+v error = %v", in, err)
		}
	}
	if _, err := svc.CloneApplicationEnvironment(ctx, pr, domain.CloneEnvironmentInput{Application: "gradethis", SourceEnv: "nowhere", TargetEnv: "x"}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing source error = %v", err)
	}
	if _, err := svc.CloneApplicationEnvironment(ctx, clientPrincipal("c"), domain.CloneEnvironmentInput{Application: "gradethis", SourceEnv: "dev", TargetEnv: "x"}); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Fatalf("non-admin error = %v", err)
	}
	events, _, _ := st.ListAudit(ctx, domain.AuditFilter{EventType: "application.environment_clone"}, storage.ListPage{Limit: 10})
	allowed, denied := 0, 0
	for _, ev := range events {
		switch ev.Decision {
		case "allow":
			allowed++
		case "deny":
			denied++
		}
	}
	if allowed != 3 || denied != 1 {
		t.Fatalf("clone audit events allow=%d deny=%d", allowed, denied)
	}
}

func TestCloneApplicationEnvironmentWithoutContractCopiesEverything(t *testing.T) {
	ctx := context.Background()
	svc, _ := newConsoleTestService(t)
	pr := adminPrincipal()
	if _, err := svc.CreateApplication(ctx, pr, domain.Application{Name: "legacy", ReleaseName: "runtime"}); err != nil {
		t.Fatal(err)
	}
	dev := domain.NamespaceRef{Env: "dev", App: "legacy"}
	if _, err := svc.CreateNamespace(ctx, pr, dev, "", []domain.AuthMethod{domain.AuthMethodMTLS}); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"a", "b"} {
		if _, _, err := svc.PutParameter(ctx, pr, domain.Ref{NS: dev, Key: key}, key, "string", "{}"); err != nil {
			t.Fatal(err)
		}
	}
	result, err := svc.CloneApplicationEnvironment(ctx, pr, domain.CloneEnvironmentInput{Application: "legacy", SourceEnv: "dev", TargetEnv: "prod", CopyValues: true, AuthMethods: []domain.AuthMethod{domain.AuthMethodToken}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 2 || result.Items[0].Alias != "a" || result.Items[0].Action != domain.CloneItemCopied || result.Items[1].Key != "b" {
		t.Fatalf("items = %+v", result.Items)
	}
	if len(result.Namespace.AllowedAuthMethods) != 1 || result.Namespace.AllowedAuthMethods[0] != domain.AuthMethodToken {
		t.Fatalf("explicit auth methods not applied: %+v", result.Namespace)
	}
}
