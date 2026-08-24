package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Suhaibinator/kms/internal/core"
	"github.com/Suhaibinator/kms/internal/crypto"
	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/storage"
)

// TestInitCheckImportEndToEnd drives the administrative CLI against real
// storage and crypto: it initializes a database with a key file, verifies the
// key, imports secrets, and confirms an imported value decrypts correctly.
func TestInitCheckImportEndToEnd(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "kms.db")
	keyFile := filepath.Join(dir, "master.key")

	// init: create db + key file + bootstrap admin.
	c := newTestCLI()
	if code := c.cmdInit([]string{"--db", db, "--master-key-file", keyFile, "--admin", "root"}); code != 0 {
		t.Fatalf("init exit=%d stderr=%s", code, c.stderr())
	}
	if !strings.Contains(c.stdout(), "token:") {
		t.Fatalf("init should print an admin token: %s", c.stdout())
	}
	if !fileExists(keyFile) {
		t.Fatalf("init should have created the key file")
	}

	// check: database + key verification.
	c2 := newTestCLI()
	if code := c2.cmdCheck([]string{"--db", db, "--key-file", keyFile}); code != 0 {
		t.Fatalf("check exit=%d stderr=%s", code, c2.stderr())
	}
	if !strings.Contains(c2.stdout(), "Master key OK") {
		t.Fatalf("check stdout = %s", c2.stdout())
	}

	// import (real run): write secrets and a token report.
	src := filepath.Join(dir, "export.json")
	writeFile(t, src, `{"STRIPE_KEY":"sk_live_x","TWILIO_SID":"AC123"}`)
	report := filepath.Join(dir, "report.txt")
	c3 := newTestCLI()
	code := c3.cmdImport([]string{
		"--from", src, "--namespace", "prod/gradethis",
		"--db", db, "--master-key-file", keyFile, "--report", report,
	})
	if code != 0 {
		t.Fatalf("import exit=%d stderr=%s", code, c3.stderr())
	}
	body := readFileString(t, report)
	if !strings.Contains(body, "STRIPE_KEY -> /prod/gradethis/stripe-key -> kmss_") {
		t.Fatalf("report missing token mapping: %s", body)
	}
	if !strings.Contains(body, "WARNING") {
		t.Fatalf("report missing one-time token warning: %s", body)
	}

	// Verify the imported secret decrypts to the original plaintext.
	store, err := storage.Open(db)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer func() { _ = store.Close() }()
	keyring, err := c3.unseal(context.Background(), store, keyFile, false)
	if err != nil {
		t.Fatalf("unseal: %v", err)
	}
	svc := core.New(store, c3.quietLogger(), "test")
	svc.SetKeyring(keyring)

	pr := core.Principal{Identity: domain.Identity{Name: "admin", Kind: domain.IdentityKindAdmin}}
	stripeRef := domain.Ref{NS: domain.NamespaceRef{Env: "prod", App: "gradethis"}, Key: "stripe-key"}
	val, err := svc.RevealSecret(context.Background(), pr, stripeRef, 0, "")
	if err != nil {
		t.Fatalf("reveal imported secret: %v", err)
	}
	if string(val.Value) != "sk_live_x" {
		t.Fatalf("imported secret value = %q, want sk_live_x", val.Value)
	}
}

func TestCheckUninitializedDatabase(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "fresh.db")
	c := newTestCLI()
	// A freshly opened (migrated) but un-keyed database reports that it needs init.
	if code := c.cmdCheck([]string{"--db", db}); code != 0 {
		t.Fatalf("check exit=%d stderr=%s", code, c.stderr())
	}
	if !strings.Contains(c.stdout(), "not yet initialized") {
		t.Fatalf("check stdout = %s", c.stdout())
	}
}

func TestRotateAdminDirectDatabaseRecovery(t *testing.T) {
	db := filepath.Join(t.TempDir(), "kms.db")
	ctx := context.Background()
	oldToken := "kms_old_admin_token"
	store, err := storage.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	created, err := store.CreateIdentity(ctx, storage.CreateIdentityParams{
		Name: "admin", Kind: domain.IdentityKindAdmin, TokenHash: crypto.TokenHash(oldToken),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	c := newTestCLI()
	if code := c.Run([]string{"rotate-admin", "--db", db, "--name", "admin"}); code != 0 {
		t.Fatalf("rotate-admin exit=%d stderr=%s", code, c.stderr())
	}
	if !strings.Contains(c.stdout(), `Rotated admin identity "admin".`) {
		t.Fatalf("rotate-admin stdout = %s", c.stdout())
	}
	newToken := tokenFromCLIOutput(t, c.stdout())
	if newToken == oldToken || !strings.HasPrefix(newToken, "kms_") {
		t.Fatalf("replacement token = %q", newToken)
	}

	store, err = storage.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	if _, err := store.GetIdentityByTokenHash(ctx, crypto.TokenHash(oldToken)); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("old token lookup error = %v, want not found", err)
	}
	rotated, err := store.GetIdentityByTokenHash(ctx, crypto.TokenHash(newToken))
	if err != nil {
		t.Fatalf("new token lookup: %v", err)
	}
	if rotated.ID != created.ID || rotated.Name != created.Name || rotated.Kind != domain.IdentityKindAdmin {
		t.Fatalf("rotated identity = %+v, created = %+v", rotated, created)
	}

	svc := core.New(store, c.quietLogger(), "test")
	if _, err := svc.Authenticate(ctx, oldToken, "", ""); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatalf("old token authentication error = %v, want unauthenticated", err)
	}
	if authenticated, err := svc.Authenticate(ctx, newToken, "", ""); err != nil || authenticated.ID != created.ID {
		t.Fatalf("new token authentication = %+v, %v", authenticated, err)
	}

	events, _, err := store.ListAudit(ctx, domain.AuditFilter{ActorIdentity: "cli", EventType: "identity.write"}, storage.ListPage{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].ResourceType != domain.ResourceIdentity || events[0].ResourceKey != "admin" || events[0].Decision != "allow" || !strings.Contains(events[0].Metadata, `"action":"rotate-token"`) {
		t.Fatalf("rotation audit events = %+v", events)
	}
}

func TestRotateAdminRejectsInvalidTargetsWithoutMutation(t *testing.T) {
	tests := []struct {
		name      string
		target    string
		kind      string
		disabled  bool
		hasToken  bool
		args      func(db string) []string
		wantError string
	}{
		{name: "missing name", target: "admin", kind: domain.IdentityKindAdmin, hasToken: true, args: func(db string) []string { return []string{"--db", db} }, wantError: "--name is required"},
		{name: "unknown identity", target: "admin", kind: domain.IdentityKindAdmin, hasToken: true, args: func(db string) []string { return []string{"--db", db, "--name", "missing"} }, wantError: "identity missing"},
		{name: "client identity", target: "client", kind: domain.IdentityKindClient, hasToken: true, args: func(db string) []string { return []string{"--db", db, "--name", "client"} }, wantError: "is not an admin"},
		{name: "disabled admin", target: "admin", kind: domain.IdentityKindAdmin, disabled: true, hasToken: true, args: func(db string) []string { return []string{"--db", db, "--name", "admin"} }, wantError: "is disabled"},
		{name: "admin without token", target: "admin", kind: domain.IdentityKindAdmin, args: func(db string) []string { return []string{"--db", db, "--name", "admin"} }, wantError: "has no token to rotate"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db := filepath.Join(t.TempDir(), "kms.db")
			ctx := context.Background()
			oldToken := "kms_preserved_" + strings.ReplaceAll(test.name, " ", "_")
			var oldHash []byte
			if test.hasToken {
				oldHash = crypto.TokenHash(oldToken)
			}
			store, err := storage.Open(db)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.CreateIdentity(ctx, storage.CreateIdentityParams{Name: test.target, Kind: test.kind, TokenHash: oldHash}); err != nil {
				t.Fatal(err)
			}
			if test.disabled {
				if err := store.SetIdentityDisabled(ctx, test.target, true); err != nil {
					t.Fatal(err)
				}
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}

			c := newTestCLI()
			if code := c.cmdRotateAdmin(test.args(db)); code != 1 {
				t.Fatalf("rotate-admin exit=%d, want 1; stdout=%s stderr=%s", code, c.stdout(), c.stderr())
			}
			if !strings.Contains(c.stderr(), test.wantError) {
				t.Fatalf("stderr = %q, want %q", c.stderr(), test.wantError)
			}
			if c.stdout() != "" {
				t.Fatalf("failed rotation disclosed output: %q", c.stdout())
			}

			store, err = storage.Open(db)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = store.Close() }()
			identity, err := store.GetIdentityByName(ctx, test.target)
			if err != nil {
				t.Fatal(err)
			}
			if identity.Disabled != test.disabled || identity.HasToken != test.hasToken {
				t.Fatalf("identity changed after refusal: %+v", identity)
			}
			if test.hasToken {
				preserved, err := store.GetIdentityByTokenHash(ctx, oldHash)
				if err != nil || preserved.Name != test.target {
					t.Fatalf("old token was not preserved: %+v, %v", preserved, err)
				}
			}
			events, _, err := store.ListAudit(ctx, domain.AuditFilter{EventType: "identity.write"}, storage.ListPage{Limit: 10})
			if err != nil || len(events) != 0 {
				t.Fatalf("refused rotation audit events = %+v, %v", events, err)
			}
		})
	}
}

func tokenFromCLIOutput(t *testing.T, output string) string {
	t.Helper()
	for _, line := range strings.Split(output, "\n") {
		if token, ok := strings.CutPrefix(strings.TrimSpace(line), "token: "); ok && token != "" {
			return token
		}
	}
	t.Fatalf("output contains no token: %s", output)
	return ""
}

func readFileString(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}
