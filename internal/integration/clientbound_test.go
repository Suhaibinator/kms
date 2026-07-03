package integration

import (
	"context"
	"errors"
	"testing"

	"github.com/Suhaibinator/kms/internal/core"
	"github.com/Suhaibinator/kms/internal/crypto"
	"github.com/Suhaibinator/kms/internal/domain"
)

// §25.2.4 / §25.3.9 / §25.3.10 — client-bound secret lifecycle and the security
// invariants around it.
func TestClientBoundFlow(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	const path = "/prod/payments/client-bound-key"
	const plaintext = "client-bound-plaintext-value"
	ref := h.ensureNS(path)

	// Creating a client-bound secret requires GenerateToken; the returned token
	// is the only key share.
	if _, err := h.svc.PutSecret(ctx, h.admin, core.PutSecretInput{Ref: ref, Value: []byte(plaintext), ClientBound: true}); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("client-bound create without GenerateToken err = %v, want ErrInvalidArgument", err)
	}

	res, err := h.svc.PutSecret(ctx, h.admin, core.PutSecretInput{
		Ref: ref, Value: []byte(plaintext), ClientBound: true, GenerateToken: true,
	})
	if err != nil {
		t.Fatalf("PutSecret client-bound: %v", err)
	}
	if res.AccessToken == "" {
		t.Fatal("expected an access token to be minted")
	}
	token := res.AccessToken

	// Read with the correct token succeeds.
	withToken := h.admin
	withToken.SecretToken = token
	got, err := h.svc.GetSecret(ctx, withToken, ref, 0, "")
	if err != nil {
		t.Fatalf("GetSecret with token: %v", err)
	}
	if string(got.Value) != plaintext {
		t.Errorf("value = %q, want %q", got.Value, plaintext)
	}

	// §25.3.10 at the service boundary: a wrong client token fails as a generic
	// decryption error — the SAME error as tampered ciphertext — so a caller
	// cannot tell whether the token or the ciphertext was at fault. A missing
	// token is denied at the gate (the caller knows it sent no token, so that is
	// not an oracle about the secret).
	wrong := h.admin
	wrong.SecretToken = "kmss_wrongwrongwrongwrongwrongwrong"
	if _, err := h.svc.GetSecret(ctx, wrong, ref, 0, ""); !errors.Is(err, domain.ErrDecryptFailed) {
		t.Fatalf("wrong token err = %v, want ErrDecryptFailed (indistinguishable from tampering)", err)
	}
	if _, err := h.svc.GetSecret(ctx, h.admin, ref, 0, ""); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Fatalf("missing token err = %v, want ErrPermissionDenied", err)
	}

	// §25.3 (reveal): admin break-glass cannot reveal client-bound secrets —
	// the server has no key share.
	if _, err := h.svc.RevealSecret(ctx, h.admin, ref, 0, ""); !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Errorf("RevealSecret(client-bound) err = %v, want ErrFailedPrecondition", err)
	}

	// Writing a new version requires proving possession of the current token.
	badWrite := h.admin
	badWrite.SecretToken = "kmss_nopenopenopenopenopenopenope"
	if _, err := h.svc.PutSecret(ctx, badWrite, core.PutSecretInput{Ref: ref, Value: []byte("v2"), ClientBound: true}); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Errorf("client-bound rewrite with wrong token err = %v, want ErrPermissionDenied", err)
	}
	if _, err := h.svc.PutSecret(ctx, withToken, core.PutSecretInput{Ref: ref, Value: []byte("v2-value"), ClientBound: true}); err != nil {
		t.Errorf("client-bound rewrite with correct token: %v", err)
	}

	// §25.3.9 — prove the database row plus the master key are insufficient:
	// reconstruct the KEK from the on-disk master key file and attempt to
	// decrypt the stored version directly.
	_, ver, err := h.store.GetSecretVersion(ctx, ref, 1, "")
	if err != nil {
		t.Fatalf("store.GetSecretVersion: %v", err)
	}
	if ver.WrapMode != domain.WrapModeClientBound {
		t.Fatalf("stored wrap mode = %q, want client_bound", ver.WrapMode)
	}
	material, err := crypto.LoadKEKMaterialFromFile(h.keyPath)
	if err != nil {
		t.Fatalf("load master key: %v", err)
	}
	kek, err := crypto.NewKEKFromMaterial(ver.KEKID, material)
	if err != nil {
		t.Fatalf("build KEK: %v", err)
	}
	base := crypto.DecryptInput{
		Ciphertext:    ver.Ciphertext,
		EncryptedDEK:  ver.EncryptedDEK,
		Nonce:         ver.Nonce,
		AAD:           ver.AAD,
		WrapMode:      ver.WrapMode,
		ClientKeySalt: ver.ClientKeySalt,
	}

	// Master key + ciphertext, no token: cannot decrypt.
	noToken := base
	if _, err := crypto.Decrypt(kek, noToken); err == nil {
		t.Fatal("client-bound decrypt succeeded without a token — key material leaked to the server")
	}

	// §25.3.10 at the crypto boundary: a wrong token and a tampered ciphertext
	// (with the correct token) both surface as the identical ErrDecryptFailed.
	wrongTok := base
	wrongTok.ClientToken = "kmss_definitely-not-the-real-token"
	_, errWrongTok := crypto.Decrypt(kek, wrongTok)

	tampered := base
	tampered.ClientToken = token
	tampered.Ciphertext = flipFirstByte(ver.Ciphertext)
	_, errTampered := crypto.Decrypt(kek, tampered)

	if !errors.Is(errWrongTok, domain.ErrDecryptFailed) || !errors.Is(errTampered, domain.ErrDecryptFailed) {
		t.Fatalf("wrong-token=%v tampered=%v, both want ErrDecryptFailed", errWrongTok, errTampered)
	}
	if errWrongTok.Error() != errTampered.Error() {
		t.Errorf("wrong-token and tampered errors differ (%q vs %q); they must be indistinguishable",
			errWrongTok, errTampered)
	}

	// Sanity: the correct token does recover the plaintext from the same row.
	ok := base
	ok.ClientToken = token
	pt, err := crypto.Decrypt(kek, ok)
	if err != nil || string(pt) != plaintext {
		t.Fatalf("decrypt with correct token = %q err=%v, want %q", pt, err, plaintext)
	}

	// Deleting a client-bound secret works and removes it entirely.
	if _, err := h.svc.DeleteSecret(ctx, h.admin, ref); err != nil {
		t.Fatalf("DeleteSecret: %v", err)
	}
	if _, err := h.svc.GetSecret(ctx, withToken, ref, 0, ""); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("read after delete err = %v, want ErrNotFound", err)
	}
}

// §25.2.4 — rotating a client-bound secret's token (a new version minted with a
// fresh token) invalidates the old token for the new current version.
func TestClientBoundTokenRotation(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	const path = "/prod/app/rotating-bound"
	ref := h.ensureNS(path)

	create, err := h.svc.PutSecret(ctx, h.admin, core.PutSecretInput{
		Ref: ref, Value: []byte("v1"), ClientBound: true, GenerateToken: true,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	oldToken := create.AccessToken

	// Rotate: a new version with a freshly minted token requires proving
	// possession of the current token.
	rotatePr := h.admin
	rotatePr.SecretToken = oldToken
	rotate, err := h.svc.PutSecret(ctx, rotatePr, core.PutSecretInput{
		Ref: ref, Value: []byte("v2"), ClientBound: true, GenerateToken: true,
	})
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	newToken := rotate.AccessToken
	if newToken == "" || newToken == oldToken {
		t.Fatalf("expected a distinct new token, got %q (old %q)", newToken, oldToken)
	}

	newPr := h.admin
	newPr.SecretToken = newToken
	oldPr := h.admin
	oldPr.SecretToken = oldToken

	// The new current version (v2) is readable with the new token, and NOT with
	// the old token (wrong key -> generic decryption failure, not an oracle).
	if got, err := h.svc.GetSecret(ctx, newPr, ref, 0, ""); err != nil || string(got.Value) != "v2" {
		t.Errorf("read current with new token = %q err=%v, want v2", got.Value, err)
	}
	if _, err := h.svc.GetSecret(ctx, newPr, ref, 2, ""); err != nil {
		t.Errorf("read v2 with new token err = %v, want ok", err)
	}
	if _, err := h.svc.GetSecret(ctx, oldPr, ref, 2, ""); !errors.Is(err, domain.ErrDecryptFailed) {
		t.Errorf("read v2 with old token err = %v, want ErrDecryptFailed", err)
	}

	// The prior version (v1) remains readable with its ORIGINAL token after the
	// rotation — rotation must not silently orphan history. It is not readable
	// with the new token.
	if got, err := h.svc.GetSecret(ctx, oldPr, ref, 1, ""); err != nil || string(got.Value) != "v1" {
		t.Errorf("read v1 with old token = %q err=%v, want v1 (rotation must not orphan prior versions)", got.Value, err)
	}
	if _, err := h.svc.GetSecret(ctx, newPr, ref, 1, ""); !errors.Is(err, domain.ErrDecryptFailed) {
		t.Errorf("read v1 with new token err = %v, want ErrDecryptFailed", err)
	}
}

func flipFirstByte(b []byte) []byte {
	out := make([]byte, len(b))
	copy(out, b)
	if len(out) > 0 {
		out[0] ^= 0xff
	}
	return out
}
