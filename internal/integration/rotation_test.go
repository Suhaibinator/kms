package integration

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/Suhaibinator/kms/internal/core"
	"github.com/Suhaibinator/kms/internal/crypto"
	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/storage"
)

// §11.4 / §25.2 — KEK rotation rewraps every secret version under a fresh key.
// All secrets (standard and client-bound) keep decrypting, key metadata reflects
// the new active / old retired keys, and the old key file can no longer unseal.
func TestKEKRotationEndToEnd(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	const stdPath = "/prod/app/standard"
	const stdValue = "standard-rotate-value"
	stdRef := h.ref(stdPath)
	if _, err := h.svc.PutSecret(ctx, h.admin, h.stdSecret(stdPath, stdValue)); err != nil {
		t.Fatalf("PutSecret standard: %v", err)
	}

	const boundPath = "/prod/app/bound"
	const boundValue = "bound-rotate-value"
	boundRef := h.ref(boundPath)
	boundRes, err := h.svc.PutSecret(ctx, h.admin, core.PutSecretInput{
		Ref: h.ensureNS(boundPath), Value: []byte(boundValue), ClientBound: true, GenerateToken: true,
	})
	if err != nil {
		t.Fatalf("PutSecret client-bound: %v", err)
	}
	boundToken := boundRes.AccessToken

	// Record the KEK id in use before rotation.
	_, verBefore, err := h.store.GetSecretVersion(ctx, stdRef, 0, "")
	if err != nil {
		t.Fatalf("GetSecretVersion before: %v", err)
	}
	oldKEKID := verBefore.KEKID

	// Rotate to fresh key material written to a new key file.
	newKeyPath := filepath.Join(h.dir, "master.key.new")
	newMaterial, err := crypto.WriteKEKMaterialFile(newKeyPath)
	if err != nil {
		t.Fatalf("write new key material: %v", err)
	}
	newID, err := crypto.NewKEKID()
	if err != nil {
		t.Fatalf("new kek id: %v", err)
	}
	secretsRewrapped, caRewrapped, err := h.svc.RotateKEK(ctx, h.admin, domain.KeyMetadata{ID: newID, Source: domain.KeySourceFile}, newMaterial)
	if err != nil {
		t.Fatalf("RotateKEK: %v", err)
	}
	if secretsRewrapped != 2 {
		t.Errorf("rewrapped %d secret versions, want 2", secretsRewrapped)
	}
	// The harness bootstraps the built-in CA, so its single active key is rewrapped
	// under the fresh KEK too (CA keys rewrap regardless of state).
	if caRewrapped != 1 {
		t.Errorf("rewrapped %d CA keys, want 1", caRewrapped)
	}

	// Both secrets still decrypt through the live (rotated) service.
	if got, err := h.svc.GetSecret(ctx, h.admin, stdRef, 0, ""); err != nil || string(got.Value) != stdValue {
		t.Errorf("standard after rotate = %q err=%v, want %q", got.Value, err, stdValue)
	}
	if got, err := h.svc.GetSecret(ctx, h.admin, boundRef, 0, "", boundToken); err != nil || string(got.Value) != boundValue {
		t.Errorf("client-bound after rotate = %q err=%v, want %q", got.Value, err, boundValue)
	}

	// The stored version now references the new KEK id.
	_, verAfter, err := h.store.GetSecretVersion(ctx, stdRef, 0, "")
	if err != nil {
		t.Fatalf("GetSecretVersion after: %v", err)
	}
	if verAfter.KEKID != newID {
		t.Errorf("version kek_id = %q, want %q (new)", verAfter.KEKID, newID)
	}
	if verAfter.KEKID == oldKEKID {
		t.Error("kek_id unchanged after rotation")
	}

	// Key metadata: new active, old retired.
	keys, err := h.svc.ListKeyMetadata(ctx, h.admin)
	if err != nil {
		t.Fatalf("ListKeyMetadata: %v", err)
	}
	states := map[string]string{}
	for _, k := range keys {
		states[k.ID] = k.State
	}
	if states[newID] != domain.KeyStateActive {
		t.Errorf("new key state = %q, want active", states[newID])
	}
	if states[oldKEKID] != domain.KeyStateRetired {
		t.Errorf("old key state = %q, want retired", states[oldKEKID])
	}

	// The old key file can no longer unseal (its material no longer matches the
	// active key's key-check); the new key file can.
	h.closeStore()

	st1, err := storage.Open(h.dbPath)
	if err != nil {
		t.Fatalf("reopen for old-key check: %v", err)
	}
	defer func() { _ = st1.Close() }()
	if _, err := crypto.Unseal(ctx, st1, crypto.UnsealOptions{KeyFilePath: h.keyPath}); err == nil {
		t.Error("old key file still unsealed after rotation; expected key-check failure")
	}

	st2, err := storage.Open(h.dbPath)
	if err != nil {
		t.Fatalf("reopen for new-key check: %v", err)
	}
	defer func() { _ = st2.Close() }()
	kr, err := crypto.Unseal(ctx, st2, crypto.UnsealOptions{KeyFilePath: newKeyPath})
	if err != nil {
		t.Fatalf("new key file failed to unseal: %v", err)
	}
	svc2 := core.New(st2, newTestLogger(h.logBuf), "test")
	svc2.SetKeyring(kr)
	if got, err := svc2.GetSecret(ctx, h.admin, stdRef, 0, ""); err != nil || string(got.Value) != stdValue {
		t.Errorf("decrypt after re-unseal with new key = %q err=%v, want %q", got.Value, err, stdValue)
	}
}
