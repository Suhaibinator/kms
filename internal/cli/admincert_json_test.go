package cli

import (
	"context"
	"encoding/json/v2"
	"strings"
	"testing"
	"time"
)

// TestAdminCertListJSON drives the offline list against real storage: every
// column the table shows is present in the document, timestamps are RFC 3339,
// and stdout carries nothing else.
func TestAdminCertListJSON(t *testing.T) {
	db, keyFile := initKMS(t, "ops")
	outDir := t.TempDir()

	issue := newTestCLI()
	if code := issue.Run([]string{"admin-cert", "issue", "ops",
		"--sqlite-path", db, "--kek-file", keyFile, "--out", outDir}); code != 0 {
		t.Fatalf("issue exit=%d stderr=%s", code, issue.stderr())
	}
	store := openTestStore(t, db)
	cert := identityCerts(t, store, "ops")[0]

	list := newTestCLI()
	if code := list.Run([]string{"-o", "json", "admin-cert", "list", "ops", "--sqlite-path", db}); code != 0 {
		t.Fatalf("list exit=%d stderr=%s", code, list.stderr())
	}
	var doc struct {
		Items []struct {
			Serial      string  `json:"serial"`
			Fingerprint string  `json:"fingerprint"`
			State       string  `json:"state"`
			ExpiresAt   *string `json:"expires_at"`
			IssuedAt    *string `json:"issued_at"`
		} `json:"items"`
		NextPageToken string `json:"next_page_token"`
	}
	if err := json.Unmarshal([]byte(list.stdout()), &doc); err != nil {
		t.Fatalf("stdout is not one JSON document: %v\n%s", err, list.stdout())
	}
	if len(doc.Items) != 1 {
		t.Fatalf("items = %+v, want exactly one", doc.Items)
	}
	item := doc.Items[0]
	if item.Serial != cert.Serial || item.Fingerprint != cert.Fingerprint || item.State != "valid" {
		t.Fatalf("item = %+v, want the recorded certificate %+v", item, cert)
	}
	if item.ExpiresAt == nil || item.IssuedAt == nil {
		t.Fatalf("timestamps = %+v, want both set", item)
	}
	if _, err := time.Parse(time.RFC3339Nano, *item.ExpiresAt); err != nil {
		t.Fatalf("expires_at = %q: %v", *item.ExpiresAt, err)
	}
	if doc.NextPageToken != "" {
		t.Fatalf("next_page_token = %q, want it omitted", doc.NextPageToken)
	}

	revoke := newTestCLI()
	if code := revoke.Run([]string{"admin-cert", "revoke", "ops", "--sqlite-path", db,
		"--serial", cert.Serial, "--yes", "--output", "json"}); code != 0 {
		t.Fatalf("revoke exit=%d stderr=%s", code, revoke.stderr())
	}
	if got := revoke.stdout(); got != "{\n  \"name\": \"ops\",\n  \"serial\": \""+cert.Serial+"\",\n  \"revoked\": true\n}\n" {
		t.Fatalf("revoke stdout = %q", got)
	}
	after := newTestCLI()
	if code := after.Run([]string{"-o", "json", "admin-cert", "list", "ops", "--sqlite-path", db}); code != 0 {
		t.Fatalf("list exit=%d stderr=%s", code, after.stderr())
	}
	if !strings.Contains(after.stdout(), `"state": "revoked"`) {
		t.Fatalf("revoked certificate not shown as revoked:\n%s", after.stdout())
	}
}

// TestAdminCertListJSONEmptyIsAnArray pins the never-null rule for a list with
// no rows at all.
func TestAdminCertListJSONEmptyIsAnArray(t *testing.T) {
	db, _ := initKMS(t, "ops")
	c := newTestCLI()
	if code := c.Run([]string{"-o", "json", "admin-cert", "list", "ops", "--sqlite-path", db}); code != 0 {
		t.Fatalf("list exit=%d stderr=%s", code, c.stderr())
	}
	if got := c.stdout(); got != "{\n  \"items\": []\n}\n" {
		t.Fatalf("stdout = %q", got)
	}
}

// TestAdminCertIssueJSONNamesFilesWithoutPEM: the one-time private key is
// written to its owner-only file and named by path, never embedded in the
// document, and the browser/CLI guidance moves to stderr.
func TestAdminCertIssueJSONNamesFilesWithoutPEM(t *testing.T) {
	db, keyFile := initKMS(t, "ops")
	outDir := t.TempDir()

	c := newTestCLI()
	code := c.Run([]string{"-o", "json", "admin-cert", "issue", "ops",
		"--sqlite-path", db, "--kek-file", keyFile, "--out", outDir})
	if code != 0 {
		t.Fatalf("issue exit=%d stderr=%s", code, c.stderr())
	}
	if strings.Contains(c.stdout(), "PRIVATE KEY") || strings.Contains(c.stdout(), "BEGIN CERTIFICATE") {
		t.Fatalf("JSON document carried PEM material:\n%s", c.stdout())
	}
	var doc struct {
		Name   string `json:"name"`
		Serial string `json:"serial"`
		Cert   struct {
			CertFile  string  `json:"cert_file"`
			KeyFile   string  `json:"key_file"`
			Serial    string  `json:"serial"`
			ExpiresAt *string `json:"expires_at"`
		} `json:"cert"`
	}
	if err := json.Unmarshal([]byte(c.stdout()), &doc); err != nil {
		t.Fatalf("stdout is not one JSON document: %v\n%s", err, c.stdout())
	}
	recorded := identityCerts(t, openTestStore(t, db), "ops")
	if len(recorded) != 1 || doc.Serial != recorded[0].Serial || doc.Cert.Serial != recorded[0].Serial {
		t.Fatalf("document = %+v, recorded = %+v", doc, recorded)
	}
	if doc.Name != "ops" || doc.Cert.ExpiresAt == nil {
		t.Fatalf("document = %+v", doc)
	}
	if got := readFileString(t, doc.Cert.KeyFile); !strings.Contains(got, "PRIVATE KEY") {
		t.Fatalf("key_file %s = %q", doc.Cert.KeyFile, got)
	}
	if !strings.Contains(c.stderr(), "openssl pkcs12 -export") {
		t.Fatalf("stderr missing the browser guidance: %s", c.stderr())
	}
	if !strings.Contains(c.stderr(), `Issued admin client certificate for identity "ops"`) {
		t.Fatalf("stderr missing the status line: %s", c.stderr())
	}
}

// TestAdminCertRevokeRequiresConfirmation: an offline revocation takes effect
// on the next request of a running server, so a script has to say --yes.
func TestAdminCertRevokeRequiresConfirmation(t *testing.T) {
	db, keyFile := initKMS(t, "ops")
	outDir := t.TempDir()
	issue := newTestCLI()
	if code := issue.Run([]string{"admin-cert", "issue", "ops",
		"--sqlite-path", db, "--kek-file", keyFile, "--out", outDir}); code != 0 {
		t.Fatalf("issue exit=%d stderr=%s", code, issue.stderr())
	}
	store := openTestStore(t, db)
	serial := identityCerts(t, store, "ops")[0].Serial

	refused := newTestCLI()
	if code := refused.Run([]string{"admin-cert", "revoke", "ops", "--sqlite-path", db, "--serial", serial}); code != exitUsage {
		t.Fatalf("exit=%d, want %d; stderr=%s", code, exitUsage, refused.stderr())
	}
	if !strings.Contains(refused.stderr(), "refusing to revoke admin certificate "+serial+" of identity ops without --yes") {
		t.Fatalf("stderr = %s", refused.stderr())
	}
	if refused.stdout() != "" {
		t.Fatalf("refused revocation wrote stdout: %q", refused.stdout())
	}
	rec, err := store.GetIdentityCertBySerial(context.Background(), serial)
	if err != nil {
		t.Fatalf("lookup certificate: %v", err)
	}
	if !rec.Cert.RevokedAt.IsZero() {
		t.Fatalf("a refused confirmation still revoked %s", serial)
	}

	// The interactive path: retyping the identity proceeds, a typo aborts.
	mistyped := newTestCLI()
	ttyStdin(t, mistyped, "nope\n")
	if code := mistyped.Run([]string{"admin-cert", "revoke", "ops", "--sqlite-path", db, "--serial", serial}); code != exitUsage {
		t.Fatalf("mistyped exit=%d, want %d; stderr=%s", code, exitUsage, mistyped.stderr())
	}
	if !strings.Contains(mistyped.stderr(), "does not match") {
		t.Fatalf("mistyped stderr = %s", mistyped.stderr())
	}
	rec, err = store.GetIdentityCertBySerial(context.Background(), serial)
	if err != nil {
		t.Fatalf("lookup certificate: %v", err)
	}
	if !rec.Cert.RevokedAt.IsZero() {
		t.Fatalf("a mistyped confirmation still revoked %s", serial)
	}

	typed := newTestCLI()
	ttyStdin(t, typed, "ops\n")
	if code := typed.Run([]string{"admin-cert", "revoke", "ops", "--sqlite-path", db, "--serial", serial}); code != 0 {
		t.Fatalf("typed exit=%d stderr=%s", code, typed.stderr())
	}
	if !strings.Contains(typed.stderr(), `Type "ops" to confirm`) {
		t.Fatalf("typed stderr missing the prompt: %s", typed.stderr())
	}
	rec, err = store.GetIdentityCertBySerial(context.Background(), serial)
	if err != nil {
		t.Fatalf("lookup certificate: %v", err)
	}
	if rec.Cert.RevokedAt.IsZero() {
		t.Fatalf("a confirmed revocation did not revoke %s", serial)
	}
}
