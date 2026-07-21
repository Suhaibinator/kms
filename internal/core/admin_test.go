package core

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Suhaibinator/kms/internal/crypto"
	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/storage"
)

func TestRequireAdminGatesPolicyWrites(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	s := newTestService(store)

	valid := domain.Policy{Name: "p", Subject: "app",
		Allow: []domain.PolicyRule{{Operation: domain.OpSecretRead, Env: "prod", App: "app"}}}

	if _, err := s.CreatePolicy(ctx, clientPrincipal("app"), valid); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Fatalf("client CreatePolicy err = %v, want ErrPermissionDenied", err)
	}
	if !store.hasAudit("policy.write", "deny") {
		t.Error("policy write denial not audited")
	}

	out, err := s.CreatePolicy(ctx, adminPrincipal(), valid)
	if err != nil {
		t.Fatalf("admin CreatePolicy: %v", err)
	}
	if out.Allow[0].App != "app" || out.Allow[0].Env != "prod" {
		t.Fatalf("stored rule = %+v", out.Allow[0])
	}
	if len(store.policies) != 1 {
		t.Fatalf("policies stored = %d, want 1", len(store.policies))
	}
}

func TestCreatePolicyValidatesRules(t *testing.T) {
	ctx := context.Background()
	s := newTestService(newFakeStore())
	bad := domain.Policy{Name: "p", Subject: "app",
		Allow: []domain.PolicyRule{{Operation: "secret:teleport", Env: "prod", App: "app"}}}
	if _, err := s.CreatePolicy(ctx, adminPrincipal(), bad); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("err = %v, want ErrInvalidArgument", err)
	}
}

func TestNamespaceCRUD(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	s := newTestService(store)

	// Create with an explicit auth-method set.
	ns, err := s.CreateNamespace(ctx, adminPrincipal(), mkns("prod", "gradethis"), "prod ns",
		[]domain.AuthMethod{domain.AuthMethodMTLS, domain.AuthMethodToken})
	if err != nil {
		t.Fatalf("CreateNamespace: %v", err)
	}
	if len(ns.AllowedAuthMethods) != 2 {
		t.Fatalf("methods = %v, want 2", ns.AllowedAuthMethods)
	}

	// Default auth methods when none supplied is mTLS-only.
	def, err := s.CreateNamespace(ctx, adminPrincipal(), mkns("prod", "other"), "", nil)
	if err != nil {
		t.Fatalf("CreateNamespace(default): %v", err)
	}
	if len(def.AllowedAuthMethods) != 1 || def.AllowedAuthMethods[0] != domain.AuthMethodMTLS {
		t.Fatalf("default methods = %v, want [mtls]", def.AllowedAuthMethods)
	}

	// Update replaces description + methods.
	upd, err := s.UpdateNamespace(ctx, adminPrincipal(), mkns("prod", "gradethis"), "updated",
		[]domain.AuthMethod{domain.AuthMethodToken})
	if err != nil {
		t.Fatalf("UpdateNamespace: %v", err)
	}
	if upd.Description != "updated" || len(upd.AllowedAuthMethods) != 1 {
		t.Fatalf("update = %+v", upd)
	}
	if !store.hasAudit("namespace.update", "allow") {
		t.Error("namespace.update not audited")
	}

	// Delete an empty namespace succeeds.
	if err := s.DeleteNamespace(ctx, adminPrincipal(), mkns("prod", "other")); err != nil {
		t.Fatalf("DeleteNamespace: %v", err)
	}
	if !store.hasAudit("namespace.delete", "allow") {
		t.Error("namespace.delete not audited")
	}
}

func TestDeleteNamespaceGuardsNonEmpty(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	s := newTestService(store)
	if _, err := s.CreateNamespace(ctx, adminPrincipal(), tns, "", []domain.AuthMethod{domain.AuthMethodToken}); err != nil {
		t.Fatalf("CreateNamespace: %v", err)
	}
	if _, _, err := store.PutParameter(ctx, tref("x"), "1", "integer", "{}", "root"); err != nil {
		t.Fatalf("seed param: %v", err)
	}
	if err := s.DeleteNamespace(ctx, adminPrincipal(), tns); !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Fatalf("delete non-empty err = %v, want ErrFailedPrecondition", err)
	}
}

func TestCreateNamespaceAuthorization(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	s := newTestService(store)

	// Client without the dedicated operation is denied.
	if _, err := s.CreateNamespace(ctx, clientPrincipal("app"), mkns("team", "x"), "", nil); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Fatalf("client err = %v, want ErrPermissionDenied", err)
	}

	// Granting admin:namespace:create lets the client create the namespace.
	store.addPolicy(domain.Policy{Name: "ns", Subject: "app",
		Allow: []domain.PolicyRule{{Operation: domain.OpAdminNamespaceCreate, Env: "team", App: "x"}}})
	if _, err := s.CreateNamespace(ctx, clientPrincipal("app"), mkns("team", "x"), "", nil); err != nil {
		t.Fatalf("authorized CreateNamespace: %v", err)
	}
}

func TestDelegatedNamespaceAdminObeysMethodGate(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	store.addNamespace(tns, domain.AuthMethodMTLS)
	store.addPolicy(domain.Policy{Name: "delegated", Subject: "app", Allow: []domain.PolicyRule{{
		Operation: domain.OpAdminNamespaceUpdate, Env: tns.Env, App: tns.App,
	}}})
	s := newTestService(store)
	pr := boundClientPrincipal("app", tns) // token-authenticated

	if _, err := s.UpdateNamespace(ctx, pr, tns, "bypass", []domain.AuthMethod{domain.AuthMethodToken}); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Fatalf("token delegated update err = %v, want ErrPermissionDenied", err)
	}
	got, _ := store.GetNamespace(ctx, tns)
	if got.Description == "bypass" {
		t.Fatal("disallowed token caller changed an mTLS-only namespace")
	}

	pr.Method = domain.AuthMethodMTLS
	if _, err := s.UpdateNamespace(ctx, pr, tns, "allowed", []domain.AuthMethod{domain.AuthMethodMTLS}); err != nil {
		t.Fatalf("mTLS delegated update: %v", err)
	}
}

func TestCreateIdentity(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	s := newTestService(store)
	withCA(t, s)

	// Non-admin denied.
	if _, err := s.CreateIdentity(ctx, clientPrincipal("app"), CreateIdentityInput{Name: "svc", Kind: domain.IdentityKindClient}); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Fatalf("client err = %v, want ErrPermissionDenied", err)
	}

	// Invalid name and kind rejected.
	if _, err := s.CreateIdentity(ctx, adminPrincipal(), CreateIdentityInput{Name: "bad name!", Kind: domain.IdentityKindClient}); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("bad name err = %v, want ErrInvalidArgument", err)
	}
	if _, err := s.CreateIdentity(ctx, adminPrincipal(), CreateIdentityInput{Name: "svc", Kind: "robot"}); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("bad kind err = %v, want ErrInvalidArgument", err)
	}

	// Token identity: usable token returned once, no cert.
	res, err := s.CreateIdentity(ctx, adminPrincipal(), CreateIdentityInput{
		Name: "svc", Kind: domain.IdentityKindClient, AuthMethods: []domain.AuthMethod{domain.AuthMethodToken},
	})
	if err != nil {
		t.Fatalf("CreateIdentity: %v", err)
	}
	if res.Identity.Name != "svc" {
		t.Fatalf("identity name = %q", res.Identity.Name)
	}
	if !strings.HasPrefix(res.Token, "kms_") {
		t.Fatalf("token %q missing kms_ prefix", res.Token)
	}
	if res.Cert != nil {
		t.Fatal("token-only identity should not receive a cert")
	}
	if _, err := s.Authenticate(ctx, res.Token, "ip", "ua"); err != nil {
		t.Fatalf("Authenticate with new token: %v", err)
	}

	// Both methods: token and cert.
	both, err := s.CreateIdentity(ctx, adminPrincipal(), CreateIdentityInput{
		Name: "dual", Kind: domain.IdentityKindClient,
		AuthMethods: []domain.AuthMethod{domain.AuthMethodToken, domain.AuthMethodMTLS},
	})
	if err != nil {
		t.Fatalf("CreateIdentity(both): %v", err)
	}
	if both.Token == "" || both.Cert == nil {
		t.Fatalf("both-method identity missing token or cert: token=%q cert=%v", both.Token, both.Cert)
	}
	if !both.Identity.HasToken || len(both.Identity.Certs) != 1 {
		t.Fatalf("identity view = %+v, want HasToken + 1 cert", both.Identity)
	}
}

func TestCreateIdentityBoundNamespace(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	s := newTestService(store)
	withCA(t, s)
	store.addNamespace(tns, domain.AuthMethodMTLS)

	res, err := s.CreateIdentity(ctx, adminPrincipal(), CreateIdentityInput{
		Name: "svc", Kind: domain.IdentityKindClient, Namespace: &tns,
		AuthMethods: []domain.AuthMethod{domain.AuthMethodMTLS},
	})
	if err != nil {
		t.Fatalf("CreateIdentity: %v", err)
	}
	if res.Identity.Namespace == nil || *res.Identity.Namespace != tns {
		t.Fatalf("bound namespace = %v, want %v", res.Identity.Namespace, tns)
	}
}

func TestIssueAndRevokeCertificate(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	s := newTestService(store)
	withCA(t, s)
	store.addIdentity("svc", domain.IdentityKindClient, "")

	bundle, err := s.IssueIdentityCertificate(ctx, adminPrincipal(), "svc", 0)
	if err != nil {
		t.Fatalf("IssueIdentityCertificate: %v", err)
	}
	if bundle.KeyPEM == "" || bundle.Serial == "" {
		t.Fatalf("incomplete bundle: %+v", bundle)
	}
	if !store.hasAudit("identity.cert.issue", "allow") {
		t.Error("cert issue not audited")
	}

	// Revoking a serial that belongs to a different identity is a not-found.
	store.addIdentity("other", domain.IdentityKindClient, "")
	if err := s.RevokeIdentityCertificate(ctx, adminPrincipal(), "other", bundle.Serial); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("cross-identity revoke err = %v, want ErrNotFound", err)
	}

	if err := s.RevokeIdentityCertificate(ctx, adminPrincipal(), "svc", bundle.Serial); err != nil {
		t.Fatalf("RevokeIdentityCertificate: %v", err)
	}
	if !store.hasAudit("identity.cert.revoke", "allow") {
		t.Error("cert revoke not audited")
	}
}

func TestRotateIdentityToken(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	s := newTestService(store)
	store.addIdentity("svc", domain.IdentityKindClient, "kms_old")

	if _, err := s.RotateIdentityToken(ctx, clientPrincipal("svc"), "svc"); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Fatalf("client err = %v, want ErrPermissionDenied", err)
	}

	newTok, err := s.RotateIdentityToken(ctx, adminPrincipal(), "svc")
	if err != nil {
		t.Fatalf("RotateIdentityToken: %v", err)
	}
	if _, err := s.Authenticate(ctx, newTok, "ip", "ua"); err != nil {
		t.Fatalf("Authenticate with rotated token: %v", err)
	}
}

func TestRotateIdentityTokenRejectsCertOnly(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	s := newTestService(store)
	store.addIdentity("svc", domain.IdentityKindClient, "") // cert-only, no token

	if _, err := s.RotateIdentityToken(ctx, adminPrincipal(), "svc"); !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Fatalf("rotate cert-only err = %v, want ErrFailedPrecondition", err)
	}
}

func TestRevokeIdentity(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	s := newTestService(store)
	store.addIdentity("svc", domain.IdentityKindClient, "kms_tok")

	if err := s.RevokeIdentity(ctx, clientPrincipal("svc"), "svc"); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Fatalf("client err = %v, want ErrPermissionDenied", err)
	}
	if err := s.RevokeIdentity(ctx, adminPrincipal(), "svc"); err != nil {
		t.Fatalf("RevokeIdentity: %v", err)
	}
	if id := store.identitiesByName["svc"]; !id.Disabled {
		t.Fatal("identity not marked disabled")
	}
}

func TestListKeyMetadataStripsMaterial(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	store.keys = []domain.KeyMetadata{{
		ID: "kek-1", Source: domain.KeySourceFile, State: domain.KeyStateActive,
		KeyCheck: []byte("verifier"), KDFSalt: []byte("salt"),
	}}
	s := newTestService(store)

	if _, err := s.ListKeyMetadata(ctx, clientPrincipal("app")); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Fatalf("client err = %v, want ErrPermissionDenied", err)
	}

	keys, err := s.ListKeyMetadata(ctx, adminPrincipal())
	if err != nil {
		t.Fatalf("ListKeyMetadata: %v", err)
	}
	if len(keys) != 1 {
		t.Fatalf("keys = %d, want 1", len(keys))
	}
	if keys[0].KeyCheck != nil || keys[0].KDFSalt != nil {
		t.Fatal("key verifier/salt material was not stripped")
	}
}

func TestListAuditAuthorization(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	s := newTestService(store)

	if _, _, err := s.ListAuditEvents(ctx, clientPrincipal("app"), domain.AuditFilter{}, storage.ListPage{}); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Fatalf("client err = %v, want ErrPermissionDenied", err)
	}
	store.addPolicy(domain.Policy{Name: "a", Subject: "app",
		Allow: []domain.PolicyRule{{Operation: domain.OpAdminAuditRead, Env: "*", App: "*"}}})
	if _, _, err := s.ListAuditEvents(ctx, clientPrincipal("app"), domain.AuditFilter{}, storage.ListPage{}); err != nil {
		t.Fatalf("authorized ListAuditEvents: %v", err)
	}
	if _, _, err := s.ListAuditEvents(ctx, adminPrincipal(), domain.AuditFilter{}, storage.ListPage{}); err != nil {
		t.Fatalf("admin ListAuditEvents: %v", err)
	}
}

func TestListAuditEventsFiltersRowsByAuthenticationMethod(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	s := newTestService(store)
	tokenNS := mkns("stage", "token")
	mtlsNS := mkns("stage", "mtls")
	deletedNS := mkns("stage", "deleted")
	recreatedNS := mkns("stage", "recreated")
	deniedNS := mkns("stage", "denied")
	store.addNamespace(tokenNS, domain.AuthMethodToken)
	store.addNamespace(mtlsNS, domain.AuthMethodMTLS)
	store.addNamespace(recreatedNS, domain.AuthMethodToken)
	store.addNamespace(deniedNS, domain.AuthMethodToken)
	tokenCreated := store.namespaces[tokenNS.String()].CreatedAt
	mtlsCreated := store.namespaces[mtlsNS.String()].CreatedAt
	recreatedAt := store.namespaces[recreatedNS.String()].CreatedAt
	store.addPolicy(domain.Policy{Name: "audit-stage", Subject: "auditor", Allow: []domain.PolicyRule{{
		Operation: domain.OpAdminAuditRead, Env: "stage", App: "*",
	}}, Deny: []domain.PolicyRule{{Operation: domain.OpAdminAuditRead, Env: deniedNS.Env, App: deniedNS.App}}})
	store.audits = []domain.AuditEvent{
		{ID: 7, EventType: "parameter.read", ResourceType: domain.ResourceParameter, ResourceNamespaceID: store.namespaces[tokenNS.String()].ID, ResourceEnv: tokenNS.Env, ResourceApp: tokenNS.App, CreatedAt: tokenCreated.Add(time.Second)},
		{ID: 6, EventType: "parameter.read", ResourceType: domain.ResourceParameter, ResourceNamespaceID: store.namespaces[mtlsNS.String()].ID, ResourceEnv: mtlsNS.Env, ResourceApp: mtlsNS.App, CreatedAt: mtlsCreated.Add(time.Second)},
		{ID: 5, EventType: "parameter.read", ResourceType: domain.ResourceParameter, ResourceNamespaceID: store.namespaces[deniedNS.String()].ID, ResourceEnv: deniedNS.Env, ResourceApp: deniedNS.App, CreatedAt: time.Now()},
		// Half-specified namespaced rows cannot be assigned a policy boundary.
		{ID: 4, EventType: "parameter.read", ResourceType: domain.ResourceParameter, ResourceEnv: tokenNS.Env},
		{ID: 3, EventType: "namespace.delete", ResourceType: domain.ResourceNamespace, ResourceEnv: deletedNS.Env, ResourceApp: deletedNS.App, CreatedAt: time.Now()},
		// Fully blank rows for namespace-bound resource types are malformed, not global.
		{ID: 2, EventType: "parameter.read", ResourceType: domain.ResourceParameter},
		// A row older than a current namespace with the same name belongs to a
		// prior deleted incarnation and must not inherit the new method policy,
		// even if restored timestamps are inconsistent.
		{ID: 1, EventType: "parameter.read", ResourceType: domain.ResourceParameter, ResourceNamespaceID: store.namespaces[recreatedNS.String()].ID + 1000, ResourceEnv: recreatedNS.Env, ResourceApp: recreatedNS.App, CreatedAt: recreatedAt.Add(time.Minute)},
	}
	filter := domain.AuditFilter{Env: "stage"} // intentionally partial

	tokenRows, _, err := s.ListAuditEvents(ctx, clientPrincipal("auditor"), filter, storage.ListPage{})
	if err != nil {
		t.Fatalf("ListAuditEvents(token): %v", err)
	}
	if len(tokenRows) != 1 || tokenRows[0].ResourceApp != tokenNS.App {
		t.Fatalf("token audit rows = %+v, want only %s", tokenRows, tokenNS)
	}

	mtlsPrincipal := clientPrincipal("auditor")
	mtlsPrincipal.Method = domain.AuthMethodMTLS
	mtlsRows, _, err := s.ListAuditEvents(ctx, mtlsPrincipal, filter, storage.ListPage{})
	if err != nil {
		t.Fatalf("ListAuditEvents(mTLS): %v", err)
	}
	if len(mtlsRows) != 1 || mtlsRows[0].ResourceApp != mtlsNS.App {
		t.Fatalf("mTLS audit rows = %+v, want only %s", mtlsRows, mtlsNS)
	}

	adminRows, _, err := s.ListAuditEvents(ctx, adminPrincipal(), filter, storage.ListPage{})
	if err != nil {
		t.Fatalf("ListAuditEvents(admin): %v", err)
	}
	if len(adminRows) != 7 {
		t.Fatalf("admin audit rows = %+v, want current and deleted namespace history", adminRows)
	}

	if _, _, err := s.ListAuditEvents(ctx, clientPrincipal("auditor"),
		domain.AuditFilter{Env: mtlsNS.Env, App: mtlsNS.App}, storage.ListPage{}); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Fatalf("fully scoped token audit query err = %v, want ErrPermissionDenied", err)
	}
}

func TestListAuditEventsBroadFilterPreservesGlobalRows(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	s := newTestService(store)
	tokenNS := mkns("prod", "token")
	store.addNamespace(tokenNS, domain.AuthMethodToken)
	tokenCreated := store.namespaces[tokenNS.String()].CreatedAt
	store.addPolicy(domain.Policy{Name: "audit-all", Subject: "auditor", Allow: []domain.PolicyRule{{
		Operation: domain.OpAdminAuditRead, Env: "*", App: "*",
	}}})
	store.audits = []domain.AuditEvent{
		// A malformed row with a namespace incarnation but blank env/app must not
		// inherit the otherwise-global policy resource classification.
		{ID: 3, EventType: "policy.update", ResourceType: domain.ResourcePolicy, ResourceNamespaceID: 999},
		{ID: 2, EventType: "parameter.read", ResourceType: domain.ResourceParameter, ResourceNamespaceID: store.namespaces[tokenNS.String()].ID, ResourceEnv: tokenNS.Env, ResourceApp: tokenNS.App, CreatedAt: tokenCreated.Add(time.Second)},
		{ID: 1, EventType: "auth.failure"},
	}

	rows, _, err := s.ListAuditEvents(ctx, clientPrincipal("auditor"), domain.AuditFilter{}, storage.ListPage{})
	if err != nil {
		t.Fatalf("ListAuditEvents(broad): %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("broad token audit rows = %+v, want allowed namespace and global row", rows)
	}
}

func TestListAuditEventsGlobalRowsUseCurrentPolicySnapshot(t *testing.T) {
	store := newFakeStore()
	s := newTestService(store)
	allow := []domain.Policy{{Name: "allow", Subject: "auditor", Allow: []domain.PolicyRule{{
		Operation: domain.OpAdminAuditRead, Env: "*", App: "*",
	}}}}
	deny := []domain.Policy{{Name: "deny", Subject: "auditor", Deny: []domain.PolicyRule{{
		Operation: domain.OpAdminAuditRead, Env: "*", App: "*",
	}}}}
	calls := 0
	store.onPoliciesForSubject = func(string) ([]domain.Policy, error) {
		calls++
		if calls == 1 {
			return allow, nil
		}
		return deny, nil
	}
	store.audits = []domain.AuditEvent{{ID: 1, EventType: "auth.failure"}}
	rows, _, err := s.ListAuditEvents(context.Background(), clientPrincipal("auditor"), domain.AuditFilter{}, storage.ListPage{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("global rows after policy revocation = %+v, want none", rows)
	}
}

func TestListAuditEventsFilteredPaginationIsCompleteAndScopeBound(t *testing.T) {
	ctx := context.Background()
	st, err := storage.Open(filepath.Join(t.TempDir(), "audit-pages.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	tokenNS := mkns("prod", "token")
	mtlsNS := mkns("prod", "mtls")
	tokenRow, err := st.CreateNamespace(ctx, domain.Namespace{NamespaceRef: tokenNS, AllowedAuthMethods: []domain.AuthMethod{domain.AuthMethodToken}})
	if err != nil {
		t.Fatal(err)
	}
	mtlsRow, err := st.CreateNamespace(ctx, domain.Namespace{NamespaceRef: mtlsNS, AllowedAuthMethods: []domain.AuthMethod{domain.AuthMethodMTLS}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreatePolicy(ctx, domain.Policy{Name: "audit", Subject: "auditor", Allow: []domain.PolicyRule{{Operation: domain.OpAdminAuditRead, Env: "*", App: "*"}}}); err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC()
	for i, ns := range []domain.Namespace{tokenRow, mtlsRow, tokenRow, mtlsRow} {
		if err := st.AppendAudit(ctx, domain.AuditEvent{
			EventType: "parameter.read", ResourceType: domain.ResourceParameter,
			ResourceNamespaceID: ns.ID, ResourceEnv: ns.Env, ResourceApp: ns.App,
			CreatedAt: at.Add(time.Duration(i) * time.Second),
		}); err != nil {
			t.Fatal(err)
		}
	}
	s := New(st, nil, "test")
	pr := clientPrincipal("auditor")
	page1, next, err := s.ListAuditEvents(ctx, pr, domain.AuditFilter{}, storage.ListPage{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(page1) != 1 || page1[0].ID != 3 || next == "" {
		t.Fatalf("first filtered audit page = %+v next=%q, want visible ID 3 and continuation", page1, next)
	}
	if next == storage.AuditPageToken(page1[0].ID) {
		t.Fatal("delegated audit cursor exposed the raw storage cursor")
	}
	page2, final, err := s.ListAuditEvents(ctx, pr, domain.AuditFilter{}, storage.ListPage{Limit: 1, Token: next})
	if err != nil {
		t.Fatal(err)
	}
	if len(page2) != 1 || page2[0].ID != 1 || final != "" {
		t.Fatalf("second filtered audit page = %+v next=%q, want visible ID 1 and exhaustion", page2, final)
	}
	if _, _, err := s.ListAuditEvents(ctx, pr, domain.AuditFilter{EventType: "other"}, storage.ListPage{Token: next}); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("cursor reused across filters err = %v, want ErrInvalidArgument", err)
	}
}

func TestFilteredListingsHaveBoundedRawScanWork(t *testing.T) {
	t.Run("namespaces", func(t *testing.T) {
		store := newFakeStore()
		store.addPolicy(domain.Policy{Name: "read", Subject: "app", Allow: []domain.PolicyRule{{Operation: domain.OpParameterRead, Env: "*", App: "*"}}})
		calls := 0
		store.onListNamespaces = func(storage.ListPage) ([]domain.Namespace, string, error) {
			calls++
			return []domain.Namespace{{NamespaceRef: mkns("prod", fmt.Sprintf("hidden-%d", calls)), AllowedAuthMethods: []domain.AuthMethod{domain.AuthMethodMTLS}}}, fmt.Sprintf("raw-%d", calls), nil
		}
		rows, next, err := newTestService(store).ListNamespaces(context.Background(), clientPrincipal("app"), storage.ListPage{Limit: 1})
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 0 || next == "" || calls != maxFilteredScanBatches {
			t.Fatalf("rows=%v next=%q calls=%d, want empty continuation after %d bounded calls", rows, next, calls, maxFilteredScanBatches)
		}
	})

	t.Run("audit", func(t *testing.T) {
		store := newFakeStore()
		hidden := mkns("prod", "mtls")
		store.addNamespace(hidden, domain.AuthMethodMTLS)
		store.addPolicy(domain.Policy{Name: "audit", Subject: "auditor", Allow: []domain.PolicyRule{{Operation: domain.OpAdminAuditRead, Env: "*", App: "*"}}})
		calls := 0
		store.onListAudit = func(domain.AuditFilter, storage.ListPage) ([]domain.AuditEvent, string, error) {
			calls++
			return []domain.AuditEvent{{ID: int64(calls), EventType: "parameter.read", ResourceType: domain.ResourceParameter, ResourceNamespaceID: store.namespaces[hidden.String()].ID, ResourceEnv: hidden.Env, ResourceApp: hidden.App}}, fmt.Sprintf("raw-%d", calls), nil
		}
		rows, next, err := newTestService(store).ListAuditEvents(context.Background(), clientPrincipal("auditor"), domain.AuditFilter{}, storage.ListPage{Limit: 1})
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 0 || next == "" || calls != maxFilteredScanBatches {
			t.Fatalf("rows=%v next=%q calls=%d, want empty continuation after %d bounded calls", rows, next, calls, maxFilteredScanBatches)
		}
	})
}

func TestRotateKEKRewrapsSecretsAndCA(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	s := newTestService(store)
	withCA(t, s)

	caPEM, err := s.CACertPEM()
	if err != nil {
		t.Fatalf("CACertPEM: %v", err)
	}

	putSecret(t, s, PutSecretInput{Ref: tref("a"), Value: []byte("alpha"), ContentType: "text/plain"})
	putSecret(t, s, PutSecretInput{Ref: tref("b"), Value: []byte("bravo"), ContentType: "text/plain"})

	// Non-admin cannot rotate.
	if _, _, err := s.RotateKEK(ctx, clientPrincipal("app"),
		domain.KeyMetadata{ID: "kek-rotated"}, bytes.Repeat([]byte{0x9}, 32)); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Fatalf("client RotateKEK err = %v, want ErrPermissionDenied", err)
	}

	secrets, ca, err := s.RotateKEK(ctx, adminPrincipal(),
		domain.KeyMetadata{ID: "kek-rotated"}, bytes.Repeat([]byte{0x9}, 32))
	if err != nil {
		t.Fatalf("RotateKEK: %v", err)
	}
	if secrets != 2 {
		t.Fatalf("secrets rewrapped = %d, want 2", secrets)
	}
	if ca != 1 {
		t.Fatalf("ca rewrapped = %d, want 1", ca)
	}

	// Secrets still decrypt under the rotated KEK.
	for _, tc := range []struct{ key, want string }{{"a", "alpha"}, {"b", "bravo"}} {
		val, err := s.GetSecret(ctx, adminPrincipal(), tref(tc.key), 0, "")
		if err != nil {
			t.Fatalf("GetSecret(%s) after rotate: %v", tc.key, err)
		}
		if string(val.Value) != tc.want {
			t.Fatalf("GetSecret(%s) = %q, want %q", tc.key, val.Value, tc.want)
		}
	}

	// The rewrapped CA key loads under a keyring holding only the new KEK,
	// yielding the same CA certificate.
	newKEK, err := crypto.NewKEKFromMaterial("kek-rotated", bytes.Repeat([]byte{0x9}, 32))
	if err != nil {
		t.Fatalf("NewKEKFromMaterial: %v", err)
	}
	s2 := newTestService(store)
	s2.SetKeyring(crypto.NewKeyring(newKEK))
	if err := s2.BootstrapCA(ctx); err != nil {
		t.Fatalf("BootstrapCA after rotate: %v", err)
	}
	reloaded, err := s2.CACertPEM()
	if err != nil {
		t.Fatalf("CACertPEM after rotate: %v", err)
	}
	if !bytes.Equal(caPEM, reloaded) {
		t.Fatal("CA certificate changed after KEK rotation")
	}
}
