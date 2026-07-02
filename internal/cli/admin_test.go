package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Suhaibinator/kms/internal/core"
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
		"--from", src, "--namespace", "/prod/gradethis",
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
	val, err := svc.RevealSecret(context.Background(), pr, "/prod/gradethis/stripe-key", 0, "")
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

func readFileString(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}
