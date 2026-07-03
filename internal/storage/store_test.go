package storage

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Suhaibinator/kms/internal/domain"
)

// ---- helpers --------------------------------------------------------------

func newStore(t *testing.T) *SQLStore {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "kms.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// encryptStub returns a deterministic Encrypt function whose payload embeds the
// version number, and records the version it was invoked with.
func encryptStub(gotVersion *uint64) func(uint64) (EncryptedPayload, error) {
	return func(v uint64) (EncryptedPayload, error) {
		if gotVersion != nil {
			*gotVersion = v
		}
		s := strconv.FormatUint(v, 10)
		return EncryptedPayload{
			Ciphertext:   []byte("ct-" + s),
			EncryptedDEK: []byte("dek-" + s),
			KEKID:        "kek-a",
			WrapMode:     domain.WrapModeStandard,
			Algorithm:    "AES-256-GCM",
			Nonce:        []byte("nonce-" + s),
			AAD:          "aad-" + s,
		}, nil
	}
}

func putSecret(t *testing.T, st *SQLStore, path string, clientBound bool) (uint64, uint64) {
	t.Helper()
	v, r, err := st.CreateSecretVersion(context.Background(), CreateSecretParams{
		Path:        path,
		CreatedBy:   "tester",
		ClientBound: clientBound,
		Encrypt:     encryptStub(nil),
	})
	if err != nil {
		t.Fatalf("CreateSecretVersion(%s): %v", path, err)
	}
	return v, r
}

func mustErrIs(t *testing.T, err, target error, ctx string) {
	t.Helper()
	if !errors.Is(err, target) {
		t.Fatalf("%s: got err %v, want errors.Is(_, %v)", ctx, err, target)
	}
}

// ---- schema / lifecycle ---------------------------------------------------

func TestSchemaDDL(t *testing.T) {
	st := newStore(t)
	var rows []struct {
		Name string
		SQL  string
	}
	if err := st.db.Raw("SELECT name, sql FROM sqlite_master WHERE type='table' AND sql IS NOT NULL").Scan(&rows).Error; err != nil {
		t.Fatal(err)
	}
	ddl := map[string]string{}
	for _, r := range rows {
		ddl[r.Name] = r.SQL
	}
	for _, tbl := range []string{
		"key_metadata", "namespaces", "parameters", "parameter_versions", "parameter_labels",
		"secrets", "secret_versions", "secret_labels", "identities", "policies",
		"audit_events", "change_log", "schema_migrations",
	} {
		if _, ok := ddl[tbl]; !ok {
			t.Errorf("missing table %s", tbl)
		}
	}
	if !strings.Contains(strings.ToUpper(ddl["change_log"]), "AUTOINCREMENT") {
		t.Errorf("change_log missing AUTOINCREMENT:\n%s", ddl["change_log"])
	}
	if !strings.Contains(strings.ToUpper(ddl["parameter_versions"]), "ON DELETE CASCADE") {
		t.Errorf("parameter_versions missing FK cascade:\n%s", ddl["parameter_versions"])
	}
	if !strings.Contains(strings.ToUpper(ddl["secret_versions"]), "ON DELETE CASCADE") {
		t.Errorf("secret_versions missing FK cascade:\n%s", ddl["secret_versions"])
	}
	t.Logf("change_log DDL:\n%s", ddl["change_log"])
}

func TestPragmasApplied(t *testing.T) {
	st := newStore(t)
	check := func(pragma, want string) {
		var got string
		if err := st.db.Raw("PRAGMA " + pragma).Scan(&got).Error; err != nil {
			t.Fatal(err)
		}
		if !strings.EqualFold(got, want) {
			t.Errorf("PRAGMA %s = %q, want %q", pragma, got, want)
		}
	}
	check("journal_mode", "wal")
	check("foreign_keys", "1")
	check("busy_timeout", "5000")
}

func TestPingAndClose(t *testing.T) {
	st := newStore(t)
	if err := st.Ping(context.Background()); err != nil {
		t.Fatalf("Ping: %v", err)
	}
}

func TestReopenAndVersionGuard(t *testing.T) {
	p := filepath.Join(t.TempDir(), "k.db")
	st, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.PutParameter(context.Background(), "/a", "v1", "", "", "u"); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	// Reopen must succeed and preserve data.
	st2, err := Open(p)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	got, err := st2.GetParameter(context.Background(), "/a", 0, "")
	if err != nil || got.Value != "v1" {
		t.Fatalf("after reopen got %+v err %v", got, err)
	}
	// Stamp a newer-than-supported version and confirm Open refuses.
	if err := st2.db.Exec("UPDATE schema_migrations SET version = ?", schemaVersion+5).Error; err != nil {
		t.Fatal(err)
	}
	_ = st2.Close()
	if _, err := Open(p); err == nil {
		t.Fatal("expected Open to refuse a newer schema version")
	}
}

// ---- parameters -----------------------------------------------------------

func TestPutGetParameter(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	v, r, err := st.PutParameter(ctx, "/app/db/url", "postgres://a", "", "", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if v != 1 {
		t.Fatalf("first version = %d, want 1", v)
	}
	if r == 0 {
		t.Fatal("revision should be non-zero")
	}

	got, err := st.GetParameter(ctx, "/app/db/url", 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Value != "postgres://a" || got.Version != 1 || got.ContentType != "string" {
		t.Fatalf("got %+v", got)
	}
	if got.Metadata != "{}" {
		t.Fatalf("default metadata = %q, want {}", got.Metadata)
	}
	if got.Labels[domain.LabelCurrent] != 1 {
		t.Fatalf("current label = %d, want 1", got.Labels[domain.LabelCurrent])
	}
	if _, ok := got.Labels[domain.LabelPrevious]; ok {
		t.Fatal("first version must not have a previous label")
	}

	// Second put moves current->2, previous->1.
	v2, _, err := st.PutParameter(ctx, "/app/db/url", "postgres://b", "text/plain", `{"k":"x"}`, "bob")
	if err != nil {
		t.Fatal(err)
	}
	if v2 != 2 {
		t.Fatalf("second version = %d, want 2", v2)
	}
	cur, err := st.GetParameter(ctx, "/app/db/url", 0, domain.LabelCurrent)
	if err != nil {
		t.Fatal(err)
	}
	if cur.Value != "postgres://b" || cur.Version != 2 || cur.ContentType != "text/plain" || cur.Metadata != `{"k":"x"}` {
		t.Fatalf("current = %+v", cur)
	}
	if cur.Labels[domain.LabelCurrent] != 2 || cur.Labels[domain.LabelPrevious] != 1 {
		t.Fatalf("labels = %v", cur.Labels)
	}
	// Explicit old version still resolves.
	old, err := st.GetParameter(ctx, "/app/db/url", 1, "")
	if err != nil || old.Value != "postgres://a" {
		t.Fatalf("v1 = %+v err %v", old, err)
	}
	// Previous label resolves to v1.
	prev, err := st.GetParameter(ctx, "/app/db/url", 0, domain.LabelPrevious)
	if err != nil || prev.Version != 1 {
		t.Fatalf("previous = %+v err %v", prev, err)
	}
}

func TestGetParameterNotFound(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	_, err := st.GetParameter(ctx, "/missing", 0, "")
	mustErrIs(t, err, domain.ErrNotFound, "missing parameter")

	if _, _, err := st.PutParameter(ctx, "/exists", "v", "", "", "u"); err != nil {
		t.Fatal(err)
	}
	_, err = st.GetParameter(ctx, "/exists", 99, "")
	mustErrIs(t, err, domain.ErrNotFound, "missing version")
	_, err = st.GetParameter(ctx, "/exists", 0, "nope")
	mustErrIs(t, err, domain.ErrNotFound, "missing label")
}

func TestParameterInfo(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	if _, _, err := st.PutParameter(ctx, "/p", "a", "", "", "u1"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.PutParameter(ctx, "/p", "b", "", "", "u2"); err != nil {
		t.Fatal(err)
	}
	info, err := st.GetParameterInfo(ctx, "/p")
	if err != nil {
		t.Fatal(err)
	}
	if len(info.Versions) != 2 {
		t.Fatalf("versions = %d, want 2", len(info.Versions))
	}
	if info.Versions[0].Version != 1 || info.Versions[1].Version != 2 {
		t.Fatalf("version order = %+v", info.Versions)
	}
	if info.Labels[domain.LabelCurrent] != 2 {
		t.Fatalf("labels = %v", info.Labels)
	}
}

func TestListParametersOrderingAndPagination(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	paths := []string{"/a", "/c", "/b", "/d/e", "/d/f"}
	for _, p := range paths {
		if _, _, err := st.PutParameter(ctx, p, "v", "", "", "u"); err != nil {
			t.Fatal(err)
		}
	}
	// Page through with limit 2 and assert sorted, complete, non-overlapping.
	var seen []string
	token := ""
	for {
		list, next, err := st.ListParameters(ctx, "", ListPage{Limit: 2, Token: token})
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range list {
			seen = append(seen, p.Path)
		}
		if next == "" {
			break
		}
		token = next
	}
	want := []string{"/a", "/b", "/c", "/d/e", "/d/f"}
	if strings.Join(seen, ",") != strings.Join(want, ",") {
		t.Fatalf("paginated = %v, want %v", seen, want)
	}
}

func TestListParametersPrefixEscaping(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	// Underscore is a valid path char; prefix "/a_b" must not match "/aXb".
	for _, p := range []string{"/a_b", "/a_b/child", "/aXb", "/aXb/child", "/a_bc"} {
		if _, _, err := st.PutParameter(ctx, p, "v", "", "", "u"); err != nil {
			t.Fatal(err)
		}
	}
	list, _, err := st.ListParameters(ctx, "/a_b", ListPage{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, p := range list {
		got[p.Path] = true
	}
	if !got["/a_b"] || !got["/a_b/child"] {
		t.Fatalf("prefix /a_b should include itself and children: %v", got)
	}
	if got["/aXb"] || got["/aXb/child"] {
		t.Fatalf("prefix /a_b must not match /aXb via LIKE wildcard: %v", got)
	}
	if got["/a_bc"] {
		t.Fatalf("prefix /a_b must not match sibling /a_bc: %v", got)
	}
}

func TestDeleteParameterCascades(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	if _, _, err := st.PutParameter(ctx, "/p", "a", "", "", "u"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.PutParameter(ctx, "/p", "b", "", "", "u"); err != nil {
		t.Fatal(err)
	}
	var pid int64
	if err := st.db.Raw("SELECT id FROM parameters WHERE path='/p'").Scan(&pid).Error; err != nil {
		t.Fatal(err)
	}
	rev, err := st.DeleteParameter(ctx, "/p")
	if err != nil {
		t.Fatal(err)
	}
	if rev == 0 {
		t.Fatal("delete revision should be non-zero")
	}
	var nVers, nLabels int64
	st.db.Raw("SELECT COUNT(*) FROM parameter_versions WHERE parameter_id=?", pid).Scan(&nVers)
	st.db.Raw("SELECT COUNT(*) FROM parameter_labels WHERE parameter_id=?", pid).Scan(&nLabels)
	if nVers != 0 || nLabels != 0 {
		t.Fatalf("cascade left rows: versions=%d labels=%d", nVers, nLabels)
	}
	_, err = st.GetParameter(ctx, "/p", 0, "")
	mustErrIs(t, err, domain.ErrNotFound, "after delete")

	// Delete of a missing parameter is ErrNotFound.
	_, err = st.DeleteParameter(ctx, "/nope")
	mustErrIs(t, err, domain.ErrNotFound, "delete missing")
}

// ---- secrets --------------------------------------------------------------

func TestCreateSecretVersion(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	var gotVer uint64
	v, r, err := st.CreateSecretVersion(ctx, CreateSecretParams{
		Path:        "/svc/key",
		ContentType: "application/json",
		Metadata:    `{"team":"x"}`,
		CreatedBy:   "alice",
		ClientBound: false,
		Encrypt:     encryptStub(&gotVer),
	})
	if err != nil {
		t.Fatal(err)
	}
	if v != 1 || r == 0 {
		t.Fatalf("v=%d r=%d", v, r)
	}
	if gotVer != 1 {
		t.Fatalf("Encrypt got version %d, want 1", gotVer)
	}
	rec, ver, err := st.GetSecretVersion(ctx, "/svc/key", 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if rec.ContentType != "application/json" || rec.Metadata != `{"team":"x"}` || rec.ClientBound {
		t.Fatalf("secret rec = %+v", rec)
	}
	if string(ver.Ciphertext) != "ct-1" || string(ver.EncryptedDEK) != "dek-1" || ver.AAD != "aad-1" {
		t.Fatalf("version payload = %+v", ver)
	}
	if ver.State != domain.StateEnabled || ver.KEKID != "kek-a" {
		t.Fatalf("version meta = %+v", ver)
	}
	if rec.Labels[domain.LabelCurrent] != 1 {
		t.Fatalf("labels = %v", rec.Labels)
	}

	// change-log entry for a secret carries no value.
	changes, err := st.ListChangesSince(ctx, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	last := changes[len(changes)-1]
	if last.ResourceType != domain.ResourceSecret || last.Value != "" || last.ChangeType != domain.ChangePut {
		t.Fatalf("secret change entry = %+v", last)
	}
}

func TestCreateSecretVersionModeMismatch(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	putSecret(t, st, "/s", false)
	_, _, err := st.CreateSecretVersion(ctx, CreateSecretParams{
		Path: "/s", ClientBound: true, Encrypt: encryptStub(nil),
	})
	mustErrIs(t, err, domain.ErrFailedPrecondition, "mode mismatch")
	// Nothing new should be written.
	info, err := st.GetSecretInfo(ctx, "/s")
	if err != nil {
		t.Fatal(err)
	}
	if len(info.Versions) != 1 {
		t.Fatalf("versions after failed create = %d, want 1", len(info.Versions))
	}
}

func TestCreateSecretVersionKeepsTokenHash(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	hash := []byte("tokenhash")
	if _, _, err := st.CreateSecretVersion(ctx, CreateSecretParams{
		Path: "/s", AccessTokenHash: hash, Encrypt: encryptStub(nil),
	}); err != nil {
		t.Fatal(err)
	}
	// New version without a hash must keep the existing one.
	if _, _, err := st.CreateSecretVersion(ctx, CreateSecretParams{
		Path: "/s", Encrypt: encryptStub(nil),
	}); err != nil {
		t.Fatal(err)
	}
	rec, err := st.GetSecretRecord(ctx, "/s")
	if err != nil {
		t.Fatal(err)
	}
	if string(rec.AccessTokenHash) != "tokenhash" {
		t.Fatalf("token hash = %q, want kept", rec.AccessTokenHash)
	}
}

func TestSecretEncryptErrorAborts(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	boom := errors.New("boom")
	_, _, err := st.CreateSecretVersion(ctx, CreateSecretParams{
		Path: "/s", Encrypt: func(uint64) (EncryptedPayload, error) { return EncryptedPayload{}, boom },
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}
	// Secret row must not have been created.
	_, err = st.GetSecretRecord(ctx, "/s")
	mustErrIs(t, err, domain.ErrNotFound, "no secret after encrypt error")
}

func TestSecretVersionNumberIncludesDestroyed(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	putSecret(t, st, "/s", false) // v1
	putSecret(t, st, "/s", false) // v2
	if _, err := st.DestroySecretVersion(ctx, "/s", 2); err != nil {
		t.Fatal(err)
	}
	v, _ := putSecret(t, st, "/s", false) // must be v3, not reuse 2
	if v != 3 {
		t.Fatalf("next version after destroy = %d, want 3", v)
	}
}

func TestGetSecretVersionDoesNotFilterState(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	putSecret(t, st, "/s", false) // v1
	putSecret(t, st, "/s", false) // v2
	if _, err := st.SetSecretVersionState(ctx, "/s", 1, domain.StateDisabled); err != nil {
		t.Fatal(err)
	}
	// disabled version still returned, with its state.
	_, ver, err := st.GetSecretVersion(ctx, "/s", 1, "")
	if err != nil {
		t.Fatal(err)
	}
	if ver.State != domain.StateDisabled {
		t.Fatalf("state = %q, want disabled", ver.State)
	}
}

func TestSetSecretVersionState(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	putSecret(t, st, "/s", false)
	putSecret(t, st, "/s", false)
	putSecret(t, st, "/s", false)

	// invalid state.
	_, err := st.SetSecretVersionState(ctx, "/s", 1, "bogus")
	mustErrIs(t, err, domain.ErrInvalidArgument, "bad state")

	// disable all (version 0).
	if _, err := st.SetSecretVersionState(ctx, "/s", 0, domain.StateDisabled); err != nil {
		t.Fatal(err)
	}
	info, _ := st.GetSecretInfo(ctx, "/s")
	for _, v := range info.Versions {
		if v.State != domain.StateDisabled {
			t.Fatalf("version %d state = %q, want disabled", v.Version, v.State)
		}
	}
	// re-enable v2 only.
	if _, err := st.SetSecretVersionState(ctx, "/s", 2, domain.StateEnabled); err != nil {
		t.Fatal(err)
	}
	_, ver, _ := st.GetSecretVersion(ctx, "/s", 2, "")
	if ver.State != domain.StateEnabled {
		t.Fatalf("v2 state = %q, want enabled", ver.State)
	}

	// destroyed version cannot be resurrected via state change.
	if _, err := st.DestroySecretVersion(ctx, "/s", 3); err != nil {
		t.Fatal(err)
	}
	_, err = st.SetSecretVersionState(ctx, "/s", 3, domain.StateEnabled)
	mustErrIs(t, err, domain.ErrFailedPrecondition, "resurrect destroyed")

	// "disable all" must skip destroyed versions.
	if _, err := st.SetSecretVersionState(ctx, "/s", 0, domain.StateDisabled); err != nil {
		t.Fatal(err)
	}
	_, ver3, _ := st.GetSecretVersion(ctx, "/s", 3, "")
	if ver3.State != domain.StateDestroyed {
		t.Fatalf("v3 state = %q, want still destroyed", ver3.State)
	}
}

func TestDestroySecretVersion(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	putSecret(t, st, "/s", false)
	rev, err := st.DestroySecretVersion(ctx, "/s", 1)
	if err != nil {
		t.Fatal(err)
	}
	if rev == 0 {
		t.Fatal("destroy revision should be non-zero")
	}
	_, ver, err := st.GetSecretVersion(ctx, "/s", 1, "")
	if err != nil {
		t.Fatal(err)
	}
	if ver.Ciphertext != nil || ver.EncryptedDEK != nil || ver.Nonce != nil {
		t.Fatalf("destroyed version still has material: %+v", ver)
	}
	if ver.State != domain.StateDestroyed || ver.DestroyedAt.IsZero() {
		t.Fatalf("destroyed meta = state %q destroyedAt %v", ver.State, ver.DestroyedAt)
	}
	// re-destroy is a failed precondition.
	_, err = st.DestroySecretVersion(ctx, "/s", 1)
	mustErrIs(t, err, domain.ErrFailedPrecondition, "re-destroy")
	// version 0 is invalid.
	_, err = st.DestroySecretVersion(ctx, "/s", 0)
	mustErrIs(t, err, domain.ErrInvalidArgument, "destroy v0")
}

func TestPromoteSecretVersion(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	putSecret(t, st, "/s", false) // v1
	putSecret(t, st, "/s", false) // v2 current, previous 1
	putSecret(t, st, "/s", false) // v3 current, previous 2

	cur, prev, rev, err := st.PromoteSecretVersion(ctx, "/s", 1)
	if err != nil {
		t.Fatal(err)
	}
	if cur != 1 || prev != 3 || rev == 0 {
		t.Fatalf("promote returned cur=%d prev=%d rev=%d", cur, prev, rev)
	}
	rec, _, _ := st.GetSecretVersion(ctx, "/s", 0, domain.LabelCurrent)
	if rec.Labels[domain.LabelCurrent] != 1 || rec.Labels[domain.LabelPrevious] != 3 {
		t.Fatalf("labels after promote = %v", rec.Labels)
	}

	// Promote requires an enabled, existing target.
	if _, err := st.SetSecretVersionState(ctx, "/s", 2, domain.StateDisabled); err != nil {
		t.Fatal(err)
	}
	_, _, _, err = st.PromoteSecretVersion(ctx, "/s", 2)
	mustErrIs(t, err, domain.ErrFailedPrecondition, "promote disabled")
	_, _, _, err = st.PromoteSecretVersion(ctx, "/s", 99)
	mustErrIs(t, err, domain.ErrNotFound, "promote missing")
}

func TestSecretInfoAndList(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	if _, _, err := st.CreateSecretVersion(ctx, CreateSecretParams{
		Path: "/a/one", AccessTokenHash: []byte("h"), Encrypt: encryptStub(nil),
	}); err != nil {
		t.Fatal(err)
	}
	putSecret(t, st, "/a/two", true)
	putSecret(t, st, "/b/three", false)

	info, err := st.GetSecretInfo(ctx, "/a/one")
	if err != nil {
		t.Fatal(err)
	}
	if !info.HasAccessToken {
		t.Fatal("HasAccessToken should be true")
	}

	list, _, err := st.ListSecrets(ctx, "/a", ListPage{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].Path != "/a/one" || list[1].Path != "/a/two" {
		t.Fatalf("list /a = %+v", list)
	}
	if !list[1].ClientBound {
		t.Fatal("/a/two should be client-bound")
	}
}

func TestDeleteSecret(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	putSecret(t, st, "/s", false)
	putSecret(t, st, "/s", false)
	var sid int64
	st.db.Raw("SELECT id FROM secrets WHERE path='/s'").Scan(&sid)
	if _, err := st.DeleteSecret(ctx, "/s"); err != nil {
		t.Fatal(err)
	}
	var nVers, nLabels int64
	st.db.Raw("SELECT COUNT(*) FROM secret_versions WHERE secret_id=?", sid).Scan(&nVers)
	st.db.Raw("SELECT COUNT(*) FROM secret_labels WHERE secret_id=?", sid).Scan(&nLabels)
	if nVers != 0 || nLabels != 0 {
		t.Fatalf("cascade left rows versions=%d labels=%d", nVers, nLabels)
	}
	_, err := st.GetSecretRecord(ctx, "/s")
	mustErrIs(t, err, domain.ErrNotFound, "after delete")
}

func TestUpdateSecretAccessTokenHash(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	putSecret(t, st, "/s", false)
	if err := st.UpdateSecretAccessTokenHash(ctx, "/s", []byte("newhash")); err != nil {
		t.Fatal(err)
	}
	rec, _ := st.GetSecretRecord(ctx, "/s")
	if string(rec.AccessTokenHash) != "newhash" {
		t.Fatalf("hash = %q", rec.AccessTokenHash)
	}
	// clearing to nil.
	if err := st.UpdateSecretAccessTokenHash(ctx, "/s", nil); err != nil {
		t.Fatal(err)
	}
	rec, _ = st.GetSecretRecord(ctx, "/s")
	if rec.AccessTokenHash != nil {
		t.Fatalf("hash = %v, want nil", rec.AccessTokenHash)
	}
	err := st.UpdateSecretAccessTokenHash(ctx, "/missing", []byte("x"))
	mustErrIs(t, err, domain.ErrNotFound, "update missing secret")
}

// ---- key metadata / rotation ---------------------------------------------

func TestKeyMetadata(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	km := domain.KeyMetadata{ID: "kek-a", Source: domain.KeySourceFile, KeyCheck: []byte("kc"), State: domain.KeyStateActive}
	if err := st.InsertKeyMetadata(ctx, km); err != nil {
		t.Fatal(err)
	}
	// duplicate.
	err := st.InsertKeyMetadata(ctx, km)
	mustErrIs(t, err, domain.ErrAlreadyExists, "duplicate key")

	got, err := st.GetKeyMetadata(ctx, "kek-a")
	if err != nil || got.Source != domain.KeySourceFile || string(got.KeyCheck) != "kc" {
		t.Fatalf("got %+v err %v", got, err)
	}
	active, err := st.ActiveKeyMetadata(ctx)
	if err != nil || active.ID != "kek-a" {
		t.Fatalf("active = %+v err %v", active, err)
	}
	if err := st.SetKeyState(ctx, "kek-a", domain.KeyStateRetired); err != nil {
		t.Fatal(err)
	}
	_, err = st.ActiveKeyMetadata(ctx)
	mustErrIs(t, err, domain.ErrNotFound, "no active key")

	err = st.SetKeyState(ctx, "missing", domain.KeyStateActive)
	mustErrIs(t, err, domain.ErrNotFound, "set state missing")
}

func TestActiveKeyMetadataFreshDB(t *testing.T) {
	st := newStore(t)
	_, err := st.ActiveKeyMetadata(context.Background())
	mustErrIs(t, err, domain.ErrNotFound, "fresh db active key")
}

func TestRotateKEK(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	if err := st.InsertKeyMetadata(ctx, domain.KeyMetadata{ID: "kek-a", Source: "file", KeyCheck: []byte("a"), State: domain.KeyStateActive}); err != nil {
		t.Fatal(err)
	}
	putSecret(t, st, "/s1", false) // v1
	putSecret(t, st, "/s1", false) // v2
	putSecret(t, st, "/s2", false) // v1
	// destroy one; it must be skipped by rotation.
	if _, err := st.DestroySecretVersion(ctx, "/s1", 1); err != nil {
		t.Fatal(err)
	}

	count, err := st.RotateKEK(ctx, domain.KeyMetadata{ID: "kek-b", Source: "file", KeyCheck: []byte("b")},
		func(rec SecretVersionRecord) ([]byte, error) {
			if rec.KEKID != "kek-a" {
				return nil, fmt.Errorf("unexpected kek %s", rec.KEKID)
			}
			return append([]byte("rw-"), rec.EncryptedDEK...), nil
		})
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 { // s1/v2 and s2/v1; s1/v1 destroyed
		t.Fatalf("rewrapped %d, want 2", count)
	}
	// new key active, old retired.
	active, _ := st.ActiveKeyMetadata(ctx)
	if active.ID != "kek-b" {
		t.Fatalf("active = %s, want kek-b", active.ID)
	}
	old, _ := st.GetKeyMetadata(ctx, "kek-a")
	if old.State != domain.KeyStateRetired {
		t.Fatalf("kek-a state = %s, want retired", old.State)
	}
	// non-destroyed versions rewrapped to kek-b; destroyed untouched.
	_, v2, _ := st.GetSecretVersion(ctx, "/s1", 2, "")
	if v2.KEKID != "kek-b" || !strings.HasPrefix(string(v2.EncryptedDEK), "rw-") {
		t.Fatalf("s1/v2 not rewrapped: %+v", v2)
	}
	_, v1, _ := st.GetSecretVersion(ctx, "/s1", 1, "")
	if v1.State != domain.StateDestroyed || v1.EncryptedDEK != nil {
		t.Fatalf("destroyed version altered: %+v", v1)
	}
}

func TestRotateKEKRollback(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	if err := st.InsertKeyMetadata(ctx, domain.KeyMetadata{ID: "kek-a", Source: "file", KeyCheck: []byte("a"), State: domain.KeyStateActive}); err != nil {
		t.Fatal(err)
	}
	putSecret(t, st, "/s1", false)
	putSecret(t, st, "/s2", false)

	// Fail on the second version so the first has already been rewrapped: the
	// whole transaction (including that successful rewrap and the key swap) must
	// roll back.
	var calls int
	_, err := st.RotateKEK(ctx, domain.KeyMetadata{ID: "kek-b", KeyCheck: []byte("b")},
		func(rec SecretVersionRecord) ([]byte, error) {
			calls++
			if calls >= 2 {
				return nil, errors.New("rewrap failed partway")
			}
			return []byte("rw"), nil
		})
	if err == nil {
		t.Fatal("expected rotation error")
	}
	if calls < 2 {
		t.Fatalf("rewrap called %d times, expected to fail on the second", calls)
	}
	// Everything unchanged: kek-b absent, kek-a still active, versions on kek-a.
	if _, err := st.GetKeyMetadata(ctx, "kek-b"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("kek-b should not exist: %v", err)
	}
	active, _ := st.ActiveKeyMetadata(ctx)
	if active.ID != "kek-a" {
		t.Fatalf("active = %s, want kek-a (rollback)", active.ID)
	}
	_, v, _ := st.GetSecretVersion(ctx, "/s1", 1, "")
	if v.KEKID != "kek-a" {
		t.Fatalf("version kek = %s, want kek-a (rollback)", v.KEKID)
	}
}

// ---- namespaces / identities / policies -----------------------------------

func TestNamespaces(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	ns, err := st.CreateNamespace(ctx, domain.Namespace{Path: "/team/a", Description: "A", CreatedBy: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	if ns.CreatedAt.IsZero() {
		t.Fatal("CreatedAt should be set")
	}
	_, err = st.CreateNamespace(ctx, domain.Namespace{Path: "/team/a"})
	mustErrIs(t, err, domain.ErrAlreadyExists, "dup namespace")

	if _, err := st.CreateNamespace(ctx, domain.Namespace{Path: "/team/b"}); err != nil {
		t.Fatal(err)
	}
	list, _, err := st.ListNamespaces(ctx, ListPage{Limit: 100})
	if err != nil || len(list) != 2 || list[0].Path != "/team/a" {
		t.Fatalf("list = %+v err %v", list, err)
	}
}

func TestIdentities(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	id, err := st.CreateIdentity(ctx, "svc-1", domain.IdentityKindClient, []byte("h1"))
	if err != nil {
		t.Fatal(err)
	}
	if id.ID == 0 || id.Disabled {
		t.Fatalf("identity = %+v", id)
	}
	_, err = st.CreateIdentity(ctx, "svc-1", domain.IdentityKindClient, []byte("h1"))
	mustErrIs(t, err, domain.ErrAlreadyExists, "dup identity")

	byName, err := st.GetIdentityByName(ctx, "svc-1")
	if err != nil || byName.Name != "svc-1" {
		t.Fatalf("byName = %+v err %v", byName, err)
	}
	byHash, err := st.GetIdentityByTokenHash(ctx, []byte("h1"))
	if err != nil || byHash.Name != "svc-1" {
		t.Fatalf("byHash = %+v err %v", byHash, err)
	}
	_, err = st.GetIdentityByTokenHash(ctx, []byte("nope"))
	mustErrIs(t, err, domain.ErrNotFound, "missing hash")

	if err := st.SetIdentityDisabled(ctx, "svc-1", true); err != nil {
		t.Fatal(err)
	}
	byName, _ = st.GetIdentityByName(ctx, "svc-1")
	if !byName.Disabled {
		t.Fatal("should be disabled")
	}
	if err := st.UpdateIdentityTokenHash(ctx, "svc-1", []byte("h2")); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetIdentityByTokenHash(ctx, []byte("h2")); err != nil {
		t.Fatalf("new hash lookup: %v", err)
	}
	err = st.SetIdentityDisabled(ctx, "missing", true)
	mustErrIs(t, err, domain.ErrNotFound, "disable missing")
}

func TestPolicies(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	p := domain.Policy{
		Name:    "p1",
		Subject: "svc-1",
		Allow:   []domain.PolicyRule{{Operation: domain.OpSecretRead, Path: "/a/*"}},
		Deny:    []domain.PolicyRule{{Operation: domain.OpSecretWrite, Path: "/a/secret"}},
	}
	created, err := st.CreatePolicy(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	if len(created.Allow) != 1 || created.Allow[0].Path != "/a/*" {
		t.Fatalf("created = %+v", created)
	}
	_, err = st.CreatePolicy(ctx, p)
	mustErrIs(t, err, domain.ErrAlreadyExists, "dup policy")

	p.Deny = nil
	p.Allow = []domain.PolicyRule{{Operation: "*", Path: "/*"}}
	updated, err := st.UpdatePolicy(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	if len(updated.Deny) != 0 || updated.Allow[0].Operation != "*" {
		t.Fatalf("updated = %+v", updated)
	}

	if _, err := st.CreatePolicy(ctx, domain.Policy{Name: "wild", Subject: "*", Allow: []domain.PolicyRule{{Operation: domain.OpParameterRead, Path: "/*"}}}); err != nil {
		t.Fatal(err)
	}
	forSubj, err := st.PoliciesForSubject(ctx, "svc-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(forSubj) != 2 { // p1 (svc-1) + wild (*)
		t.Fatalf("policies for svc-1 = %d, want 2", len(forSubj))
	}

	if err := st.DeletePolicy(ctx, "p1"); err != nil {
		t.Fatal(err)
	}
	err = st.DeletePolicy(ctx, "p1")
	mustErrIs(t, err, domain.ErrNotFound, "delete missing policy")
}

// ---- audit ----------------------------------------------------------------

func TestAuditAppendListAndFilters(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	base := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	events := []domain.AuditEvent{
		{EventType: "read", ActorIdentity: "alice", ResourcePath: "/a/x", Decision: "allow", CreatedAt: base},
		{EventType: "write", ActorIdentity: "bob", ResourcePath: "/a/y", Decision: "allow", CreatedAt: base.Add(time.Second)},
		{EventType: "read", ActorIdentity: "alice", ResourcePath: "/b/z", Decision: "deny", CreatedAt: base.Add(2 * time.Second)},
	}
	for _, e := range events {
		if err := st.AppendAudit(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	// newest first.
	all, _, err := st.ListAudit(ctx, domain.AuditFilter{}, ListPage{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 || all[0].ResourcePath != "/b/z" {
		t.Fatalf("audit order = %+v", all)
	}
	// prefix filter.
	byPrefix, _, _ := st.ListAudit(ctx, domain.AuditFilter{PathPrefix: "/a"}, ListPage{Limit: 100})
	if len(byPrefix) != 2 {
		t.Fatalf("prefix /a = %d, want 2", len(byPrefix))
	}
	// actor + event type filters.
	byActor, _, _ := st.ListAudit(ctx, domain.AuditFilter{ActorIdentity: "alice", EventType: "read"}, ListPage{Limit: 100})
	if len(byActor) != 2 {
		t.Fatalf("alice+read = %d, want 2", len(byActor))
	}
}

func TestAuditTimeRangeFractionalSeconds(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	base := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	// e1 at .000, e2 at .500 — RFC3339Nano would trim e1's fraction and misorder
	// a string range comparison; fixed-width format keeps it correct.
	if err := st.AppendAudit(ctx, domain.AuditEvent{EventType: "x", ResourcePath: "/p", CreatedAt: base}); err != nil {
		t.Fatal(err)
	}
	if err := st.AppendAudit(ctx, domain.AuditEvent{EventType: "x", ResourcePath: "/p", CreatedAt: base.Add(500 * time.Millisecond)}); err != nil {
		t.Fatal(err)
	}

	from := base.Add(250 * time.Millisecond)
	got, _, err := st.ListAudit(ctx, domain.AuditFilter{From: from}, ListPage{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("From=.250 returned %d events, want 1 (only the .500 event)", len(got))
	}
	if !got[0].CreatedAt.Equal(base.Add(500 * time.Millisecond)) {
		t.Fatalf("returned event at %v, want .500", got[0].CreatedAt)
	}
}

func TestAuditPagination(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		if err := st.AppendAudit(ctx, domain.AuditEvent{EventType: "e", ResourcePath: "/p"}); err != nil {
			t.Fatal(err)
		}
	}
	var seen int
	token := ""
	for {
		list, next, err := st.ListAudit(ctx, domain.AuditFilter{}, ListPage{Limit: 2, Token: token})
		if err != nil {
			t.Fatal(err)
		}
		seen += len(list)
		if next == "" {
			break
		}
		token = next
	}
	if seen != 5 {
		t.Fatalf("paginated audit = %d, want 5", seen)
	}
}

// ---- change log / revisions ----------------------------------------------

func TestRevisionMonotonicAfterPruneAll(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		if _, _, err := st.PutParameter(ctx, "/p", strconv.Itoa(i), "", "", "u"); err != nil {
			t.Fatal(err)
		}
	}
	maxBefore, err := st.CurrentRevision(ctx)
	if err != nil || maxBefore == 0 {
		t.Fatalf("CurrentRevision = %d err %v", maxBefore, err)
	}
	// Prune every row.
	deleted, err := st.PruneChangeLog(ctx, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if deleted == 0 {
		t.Fatal("expected to delete all change_log rows")
	}
	oldest, _ := st.OldestRetainedRevision(ctx)
	if oldest != 0 {
		t.Fatalf("OldestRetainedRevision after prune-all = %d, want 0", oldest)
	}
	// CurrentRevision survives pruning (reads sqlite_sequence).
	afterPrune, _ := st.CurrentRevision(ctx)
	if afterPrune != maxBefore {
		t.Fatalf("CurrentRevision changed after prune: %d != %d", afterPrune, maxBefore)
	}
	// New writes get strictly greater revisions — never reused.
	_, newRev, err := st.PutParameter(ctx, "/q", "v", "", "", "u")
	if err != nil {
		t.Fatal(err)
	}
	if newRev <= maxBefore {
		t.Fatalf("new revision %d not strictly greater than old max %d", newRev, maxBefore)
	}
}

func TestListChangesSince(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	_, r1, _ := st.PutParameter(ctx, "/a", "1", "", "", "u")
	_, r2, _ := st.PutParameter(ctx, "/b", "2", "", "", "u")
	changes, err := st.ListChangesSince(ctx, r1, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 || changes[0].Revision != r2 || changes[0].Path != "/b" {
		t.Fatalf("changes since r1 = %+v", changes)
	}
	if changes[0].Value != "2" {
		t.Fatalf("parameter change value = %q, want 2", changes[0].Value)
	}
}

func TestPruneChangeLogRetentionMath(t *testing.T) {
	insert := func(st *SQLStore, n int, age time.Duration) {
		now := time.Now()
		for i := 0; i < n; i++ {
			if err := st.db.Create(&changeLogModel{
				ResourceType: "parameter", Path: "/p", ChangeType: "put",
				CreatedAt: fmtTime(now.Add(-age)),
			}).Error; err != nil {
				t.Fatal(err)
			}
		}
	}

	t.Run("duration dominates", func(t *testing.T) {
		st := newStore(t)
		insert(st, 3, 2*time.Hour) // old
		insert(st, 3, 0)           // recent
		deleted, err := st.PruneChangeLog(context.Background(), time.Hour, 1000)
		if err != nil {
			t.Fatal(err)
		}
		if deleted != 3 {
			t.Fatalf("deleted %d, want 3 (the old ones)", deleted)
		}
	})

	t.Run("maxRows dominates", func(t *testing.T) {
		st := newStore(t)
		insert(st, 6, 0)
		deleted, err := st.PruneChangeLog(context.Background(), 1000*time.Hour, 2)
		if err != nil {
			t.Fatal(err)
		}
		if deleted != 4 {
			t.Fatalf("deleted %d, want 4 (keep 2 newest)", deleted)
		}
	})

	t.Run("intersection", func(t *testing.T) {
		st := newStore(t)
		insert(st, 3, 2*time.Hour) // old
		insert(st, 3, 0)           // recent
		// duration keeps the 3 recent; maxRows keeps 2 newest => keep 2.
		deleted, err := st.PruneChangeLog(context.Background(), time.Hour, 2)
		if err != nil {
			t.Fatal(err)
		}
		if deleted != 4 {
			t.Fatalf("deleted %d, want 4 (keep 2)", deleted)
		}
	})

	t.Run("maxRows zero deletes all", func(t *testing.T) {
		st := newStore(t)
		insert(st, 5, 0)
		deleted, err := st.PruneChangeLog(context.Background(), 1000*time.Hour, 0)
		if err != nil {
			t.Fatal(err)
		}
		if deleted != 5 {
			t.Fatalf("deleted %d, want 5", deleted)
		}
	})
}

func TestSnapshotParameters(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	if _, _, err := st.PutParameter(ctx, "/prod/a", "1", "", "", "u"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.PutParameter(ctx, "/prod/a", "2", "", "", "u"); err != nil { // current is v2
		t.Fatal(err)
	}
	if _, _, err := st.PutParameter(ctx, "/prod/b", "x", "", "", "u"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.PutParameter(ctx, "/stage/c", "y", "", "", "u"); err != nil {
		t.Fatal(err)
	}

	// exact + prefix patterns.
	params, rev, err := st.SnapshotParameters(ctx, []string{"/prod/*"})
	if err != nil {
		t.Fatal(err)
	}
	if rev == 0 {
		t.Fatal("snapshot revision should be non-zero")
	}
	if len(params) != 2 {
		t.Fatalf("prod/* returned %d, want 2", len(params))
	}
	// current value only.
	byPath := map[string]domain.Parameter{}
	for _, p := range params {
		byPath[p.Path] = p
	}
	if byPath["/prod/a"].Value != "2" || byPath["/prod/a"].Version != 2 {
		t.Fatalf("/prod/a snapshot = %+v", byPath["/prod/a"])
	}

	// exact pattern.
	exact, _, _ := st.SnapshotParameters(ctx, []string{"/stage/c"})
	if len(exact) != 1 || exact[0].Path != "/stage/c" {
		t.Fatalf("exact = %+v", exact)
	}

	// match-all.
	all, _, _ := st.SnapshotParameters(ctx, []string{"/*"})
	if len(all) != 3 {
		t.Fatalf("all = %d, want 3", len(all))
	}

	// no patterns => nothing (but a valid revision).
	none, rev2, _ := st.SnapshotParameters(ctx, nil)
	if len(none) != 0 || rev2 == 0 {
		t.Fatalf("empty patterns = %d rev %d", len(none), rev2)
	}
}

// TestSnapshotParametersSetBased exercises the set-based snapshot query against
// the exact contract it must preserve: multiple patterns, label resolution,
// the full labels map, omission of parameters lacking a "current" label or a
// version row for it, empty results, and LIKE-metacharacter escaping in prefix
// patterns.
func TestSnapshotParametersSetBased(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	// /prod/a has two versions -> current v2, previous v1.
	if _, _, err := st.PutParameter(ctx, "/prod/a", "1", "text/plain", `{"k":"1"}`, "alice"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.PutParameter(ctx, "/prod/a", "2", "text/plain", `{"k":"2"}`, "bob"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.PutParameter(ctx, "/prod/b", "x", "", "", "u"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.PutParameter(ctx, "/stage/c", "y", "", "", "u"); err != nil {
		t.Fatal(err)
	}

	byPath := func(ps []domain.Parameter) map[string]domain.Parameter {
		m := map[string]domain.Parameter{}
		for _, p := range ps {
			m[p.Path] = p
		}
		return m
	}

	// Multiple patterns: exact path OR a prefix. Union, de-duplicated, by path.
	multi, rev, err := st.SnapshotParameters(ctx, []string{"/prod/a", "/stage/*"})
	if err != nil {
		t.Fatal(err)
	}
	if rev == 0 {
		t.Fatal("revision should be non-zero")
	}
	if wantRev, _ := st.CurrentRevision(ctx); rev != wantRev {
		t.Fatalf("snapshot rev %d != CurrentRevision %d", rev, wantRev)
	}
	if len(multi) != 2 {
		t.Fatalf("multi patterns returned %d, want 2 (%+v)", len(multi), multi)
	}
	m := byPath(multi)
	// Ordering must be by path ascending.
	if multi[0].Path != "/prod/a" || multi[1].Path != "/stage/c" {
		t.Fatalf("multi order = %q,%q", multi[0].Path, multi[1].Path)
	}
	// The current-labeled version is returned, with the version's fields.
	a := m["/prod/a"]
	if a.Value != "2" || a.Version != 2 || a.ContentType != "text/plain" ||
		a.Metadata != `{"k":"2"}` || a.CreatedBy != "bob" {
		t.Fatalf("/prod/a = %+v", a)
	}
	// The full labels map (current + previous), not just "current".
	if a.Labels[domain.LabelCurrent] != 2 || a.Labels[domain.LabelPrevious] != 1 {
		t.Fatalf("/prod/a labels = %+v", a.Labels)
	}

	// Exact-path pattern.
	exact, _, err := st.SnapshotParameters(ctx, []string{"/prod/b"})
	if err != nil {
		t.Fatal(err)
	}
	if len(exact) != 1 || exact[0].Path != "/prod/b" || exact[0].Value != "x" {
		t.Fatalf("exact = %+v", exact)
	}

	// Patterns matching nothing: empty slice, still a valid revision.
	empty, rev3, err := st.SnapshotParameters(ctx, []string{"/nope/*", "/also-nope"})
	if err != nil {
		t.Fatal(err)
	}
	if len(empty) != 0 || rev3 == 0 {
		t.Fatalf("no-match = %d rev %d", len(empty), rev3)
	}

	// A parameter whose "current" label was removed (only a non-current label
	// remains) must be omitted, even though its path matches the pattern.
	if _, _, err := st.PutParameter(ctx, "/prod/orphan", "o1", "", "", "u"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.PutParameter(ctx, "/prod/orphan", "o2", "", "", "u"); err != nil { // current=2, previous=1
		t.Fatal(err)
	}
	var op parameterModel
	if err := st.db.Where("path = ?", "/prod/orphan").First(&op).Error; err != nil {
		t.Fatal(err)
	}
	if err := st.db.Where("parameter_id = ? AND label = ?", op.ID, domain.LabelCurrent).
		Delete(&parameterLabelModel{}).Error; err != nil {
		t.Fatal(err)
	}
	afterOrphan := byPath(mustSnapshot(t, st, ctx, []string{"/prod/*"}))
	if _, ok := afterOrphan["/prod/orphan"]; ok {
		t.Fatalf("param with no current label should be omitted, got %+v", afterOrphan["/prod/orphan"])
	}
	if _, ok := afterOrphan["/prod/a"]; !ok {
		t.Fatal("/prod/a should still be present")
	}

	// A "current" label pointing at a missing version row must also be omitted.
	if _, _, err := st.PutParameter(ctx, "/prod/ghost", "g1", "", "", "u"); err != nil {
		t.Fatal(err)
	}
	var gp parameterModel
	if err := st.db.Where("path = ?", "/prod/ghost").First(&gp).Error; err != nil {
		t.Fatal(err)
	}
	if err := st.db.Where("parameter_id = ? AND version_number = ?", gp.ID, 1).
		Delete(&parameterVersionModel{}).Error; err != nil {
		t.Fatal(err)
	}
	afterGhost := byPath(mustSnapshot(t, st, ctx, []string{"/prod/*"}))
	if _, ok := afterGhost["/prod/ghost"]; ok {
		t.Fatal("param whose current version row is gone should be omitted")
	}
}

// TestSnapshotParametersLikeEscape verifies prefix patterns escape LIKE
// metacharacters so "/a_b/*" and "/p%q/*" match only the literal path, never a
// wildcard expansion (an unescaped "_" or "%" would match "/aXb" / "/pZZq").
func TestSnapshotParametersLikeEscape(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	if _, _, err := st.PutParameter(ctx, "/a_b/x", "match-underscore", "", "", "u"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.PutParameter(ctx, "/aXb/x", "wildcard-underscore", "", "", "u"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.PutParameter(ctx, "/p%q/x", "match-percent", "", "", "u"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.PutParameter(ctx, "/pZZq/x", "wildcard-percent", "", "", "u"); err != nil {
		t.Fatal(err)
	}

	under := mustSnapshot(t, st, ctx, []string{"/a_b/*"})
	if len(under) != 1 || under[0].Path != "/a_b/x" {
		t.Fatalf("/a_b/* should match only /a_b/x, got %+v", under)
	}

	pct := mustSnapshot(t, st, ctx, []string{"/p%q/*"})
	if len(pct) != 1 || pct[0].Path != "/p%q/x" {
		t.Fatalf("/p%%q/* should match only /p%%q/x, got %+v", pct)
	}
}

func mustSnapshot(t *testing.T, st *SQLStore, ctx context.Context, patterns []string) []domain.Parameter {
	t.Helper()
	ps, _, err := st.SnapshotParameters(ctx, patterns)
	if err != nil {
		t.Fatalf("SnapshotParameters(%v): %v", patterns, err)
	}
	return ps
}

// ---- backup / time round-trip / pagination tokens -------------------------

func TestBackup(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	if _, _, err := st.PutParameter(ctx, "/a", "v", "", "", "u"); err != nil {
		t.Fatal(err)
	}

	dest := filepath.Join(t.TempDir(), "backup.db")
	if err := st.Backup(ctx, dest); err != nil {
		t.Fatal(err)
	}
	// backup must be a usable database with the data.
	bk, err := Open(dest)
	if err != nil {
		t.Fatalf("open backup: %v", err)
	}
	defer func() { _ = bk.Close() }()
	got, err := bk.GetParameter(ctx, "/a", 0, "")
	if err != nil || got.Value != "v" {
		t.Fatalf("backup data = %+v err %v", got, err)
	}
	// existing destination is refused.
	err = st.Backup(ctx, dest)
	mustErrIs(t, err, domain.ErrAlreadyExists, "backup over existing")
}

func TestTimeRoundTripNanoseconds(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	ts := time.Date(2026, 7, 1, 12, 34, 56, 123456789, time.UTC)
	ns, err := st.CreateNamespace(ctx, domain.Namespace{Path: "/n", CreatedAt: ts})
	if err != nil {
		t.Fatal(err)
	}
	if !ns.CreatedAt.Equal(ts) {
		t.Fatalf("returned CreatedAt %v != %v", ns.CreatedAt, ts)
	}
	list, _, _ := st.ListNamespaces(ctx, ListPage{Limit: 10})
	if !list[0].CreatedAt.Equal(ts) {
		t.Fatalf("stored CreatedAt %v != %v", list[0].CreatedAt, ts)
	}
}

func TestPageTokenOpaqueInvalid(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	_, _, err := st.ListParameters(ctx, "", ListPage{Limit: 10, Token: "!!!not-base64!!!"})
	mustErrIs(t, err, domain.ErrInvalidArgument, "bad page token")
	_, _, err = st.ListAudit(ctx, domain.AuditFilter{}, ListPage{Limit: 10, Token: "!!!bad!!!"})
	mustErrIs(t, err, domain.ErrInvalidArgument, "bad audit token")
}

func TestLimitClamping(t *testing.T) {
	if clampLimit(0) != 100 {
		t.Fatal("default should be 100")
	}
	if clampLimit(-5) != 100 {
		t.Fatal("negative should default to 100")
	}
	if clampLimit(5000) != 1000 {
		t.Fatal("over-max should clamp to 1000")
	}
	if clampLimit(50) != 50 {
		t.Fatal("in-range should pass through")
	}
}

// ---- concurrency ----------------------------------------------------------

func TestConcurrentPutParameterNoLostUpdates(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	const workers, iters = 8, 40
	var wg sync.WaitGroup
	errCh := make(chan error, workers*iters)
	verCh := make(chan uint64, workers*iters)

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				v, _, err := st.PutParameter(ctx, "/hot", fmt.Sprintf("w%d-i%d", w, i), "", "", "u")
				if err != nil {
					errCh <- err
					continue
				}
				verCh <- v
			}
		}(w)
	}
	wg.Wait()
	close(errCh)
	close(verCh)

	for err := range errCh {
		t.Fatalf("concurrent put error (busy handling failed?): %v", err)
	}
	seen := make(map[uint64]bool)
	var max uint64
	count := 0
	for v := range verCh {
		if seen[v] {
			t.Fatalf("duplicate version %d assigned (lost update)", v)
		}
		seen[v] = true
		if v > max {
			max = v
		}
		count++
	}
	if count != workers*iters {
		t.Fatalf("successful puts = %d, want %d", count, workers*iters)
	}
	// Versions are exactly 1..count with no gaps.
	if max != uint64(count) {
		t.Fatalf("max version %d != count %d (gap or reuse)", max, count)
	}
	info, err := st.GetParameterInfo(ctx, "/hot")
	if err != nil {
		t.Fatal(err)
	}
	if len(info.Versions) != count {
		t.Fatalf("stored versions %d != %d", len(info.Versions), count)
	}
	if info.Labels[domain.LabelCurrent] != uint64(count) {
		t.Fatalf("current label = %d, want %d", info.Labels[domain.LabelCurrent], count)
	}
}
