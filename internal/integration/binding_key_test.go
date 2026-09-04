package integration

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/Suhaibinator/kms/internal/core"
	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/storage"
)

const (
	integrationBindingKeyA = "integration-binding-key-A-0123456789abcdef0123456789abcdef"
	integrationBindingKeyB = "integration-binding-key-B-0123456789abcdef0123456789abcdef"
	integrationBindingKeyC = "integration-binding-key-C-0123456789abcdef0123456789abcdef"
	integrationBindingKeyD = "integration-binding-key-D-0123456789abcdef0123456789abcdef"
)

func TestBindingKeyCredentialsAndLiveMetadata(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	ref := h.ensureNS("/prod/app/credential-matrix")

	if _, err := h.svc.PutSecret(ctx, h.admin, core.PutSecretInput{
		Ref: ref, Value: []byte("invalid"), BindingKey: "too-short",
	}); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("PutSecret(short binding key) err = %v, want ErrInvalidArgument", err)
	}

	created, err := h.svc.PutSecret(ctx, h.admin, core.PutSecretInput{
		Ref: ref, Value: []byte("bound-and-token-gated"), BindingKey: integrationBindingKeyA, GenerateToken: true,
	})
	if err != nil {
		t.Fatalf("PutSecret v1: %v", err)
	}
	if created.Version != 1 || created.AccessToken == "" {
		t.Fatalf("PutSecret v1 result = %+v, want version 1 and one-time access token", created)
	}

	for _, tc := range []struct {
		name       string
		token      string
		bindingKey string
		want       error
	}{
		{name: "neither credential", want: domain.ErrPermissionDenied},
		{name: "binding key only", bindingKey: integrationBindingKeyA, want: domain.ErrPermissionDenied},
		{name: "access token only", token: created.AccessToken, want: domain.ErrDecryptFailed},
		{name: "wrong access token", token: "kmss_wrong-access-token", bindingKey: integrationBindingKeyA, want: domain.ErrPermissionDenied},
		{name: "wrong binding key", token: created.AccessToken, bindingKey: integrationBindingKeyB, want: domain.ErrDecryptFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := h.svc.GetSecret(ctx, h.admin, ref, 1, "", tc.token, tc.bindingKey); !errors.Is(err, tc.want) {
				t.Fatalf("GetSecret err = %v, want %v", err, tc.want)
			}
		})
	}
	got, err := h.svc.GetSecret(ctx, h.admin, ref, 1, "", created.AccessToken, integrationBindingKeyA)
	if err != nil || string(got.Value) != "bound-and-token-gated" {
		t.Fatalf("GetSecret(correct credentials) = %q err=%v", got.Value, err)
	}

	// Reveal bypasses only the independent access-token gate. It still cannot
	// open a bound version without the operator-owned binding key.
	if _, err := h.svc.RevealSecret(ctx, h.admin, ref, 1, "", created.AccessToken, ""); !errors.Is(err, domain.ErrDecryptFailed) {
		t.Fatalf("RevealSecret(missing binding key) err = %v, want ErrDecryptFailed", err)
	}
	revealed, err := h.svc.RevealSecret(ctx, h.admin, ref, 1, "", "deliberately-wrong-access-token", integrationBindingKeyA)
	if err != nil || string(revealed.Value) != "bound-and-token-gated" {
		t.Fatalf("RevealSecret(binding key only) = %q err=%v", revealed.Value, err)
	}

	// Protection is selected independently for every new version. The existing
	// secret-level access-token hash continues to gate both new versions.
	v2, err := h.svc.PutSecret(ctx, h.admin, core.PutSecretInput{Ref: ref, Value: []byte("unbound")})
	if err != nil || v2.Version != 2 || v2.AccessToken != "" {
		t.Fatalf("PutSecret v2 = %+v err=%v", v2, err)
	}
	v3, err := h.svc.PutSecret(ctx, h.admin, core.PutSecretInput{
		Ref: ref, Value: []byte("bound-again"), BindingKey: integrationBindingKeyB,
	})
	if err != nil || v3.Version != 3 || v3.AccessToken != "" {
		t.Fatalf("PutSecret v3 = %+v err=%v", v3, err)
	}

	info, err := h.svc.GetSecretInfo(ctx, h.admin, ref)
	if err != nil {
		t.Fatalf("GetSecretInfo: %v", err)
	}
	if !info.Bound || !info.HasAccessToken || info.Labels[domain.LabelCurrent] != 3 {
		t.Fatalf("current metadata = %+v, want bound and token-gated at v3", info)
	}
	assertSecretVersionProtection(t, info, 1, true, true)
	assertSecretVersionProtection(t, info, 2, false, true)
	assertSecretVersionProtection(t, info, 3, true, true)
	if cohort, err := h.svc.PreviewSecretBindingCohort(ctx, h.admin, ref, 1, integrationBindingKeyA); err != nil || !slices.Equal(cohort.AffectedVersions, []uint64{1}) {
		t.Fatalf("v1 cohort crossed the unbound v2 boundary: %+v err=%v", cohort, err)
	}
	if cohort, err := h.svc.PreviewSecretBindingCohort(ctx, h.admin, ref, 3, integrationBindingKeyB); err != nil || !slices.Equal(cohort.AffectedVersions, []uint64{3}) {
		t.Fatalf("v3 cohort crossed the unbound v2 boundary: %+v err=%v", cohort, err)
	}

	if _, err := h.svc.GetSecret(ctx, h.admin, ref, 2, "", "", ""); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Fatalf("unbound token-gated v2 without token err = %v, want ErrPermissionDenied", err)
	}
	if got, err := h.svc.GetSecret(ctx, h.admin, ref, 2, "", created.AccessToken, ""); err != nil || string(got.Value) != "unbound" {
		t.Fatalf("GetSecret v2 = %q err=%v", got.Value, err)
	}
	if got, err := h.svc.GetSecret(ctx, h.admin, ref, 3, "", created.AccessToken, integrationBindingKeyB); err != nil || string(got.Value) != "bound-again" {
		t.Fatalf("GetSecret v3 = %q err=%v", got.Value, err)
	}

	for _, key := range []string{integrationBindingKeyA, integrationBindingKeyB} {
		if bytes.Contains(h.scanBytes(), []byte(key)) || strings.Contains(h.logBuf.String(), key) || strings.Contains(fmt.Sprintf("%+v", info), key) {
			t.Fatalf("binding key appeared in storage, logs, or metadata")
		}
	}
}

func TestBindAndUnbindSecretVersionInPlace(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	const app = "live-binding"
	if _, err := h.svc.CreateApplication(ctx, h.admin, domain.Application{Name: app, ReleaseName: "runtime"}); err != nil {
		t.Fatalf("CreateApplication: %v", err)
	}
	ref := h.ensureNS("/prod/" + app + "/in-place-binding")
	if _, err := h.svc.PutSecret(ctx, h.admin, core.PutSecretInput{Ref: ref, Value: []byte("stable-ciphertext")}); err != nil {
		t.Fatalf("PutSecret: %v", err)
	}
	release, err := h.svc.CreateConfigurationRelease(ctx, h.admin, domain.CreateConfigurationReleaseInput{
		Namespace: ref.NS,
		Name:      "runtime",
		Entries: []domain.ReleaseEntrySelector{{
			Alias: "credential", Kind: domain.ReleaseEntrySecret, Ref: ref, Version: 1,
		}},
	})
	if err != nil {
		t.Fatalf("CreateConfigurationRelease: %v", err)
	}
	_, original, err := h.store.GetSecretVersion(ctx, ref, 1, "")
	if err != nil {
		t.Fatalf("GetSecretVersion before bind: %v", err)
	}

	bound, err := h.svc.BindSecret(ctx, h.admin, ref, 0, integrationBindingKeyA)
	if err != nil {
		t.Fatalf("BindSecret: %v", err)
	}
	if bound.AnchorVersion != 1 || !slices.Equal(bound.AffectedVersions, []uint64{1}) {
		t.Fatalf("BindSecret result = %+v", bound)
	}
	_, afterBind, err := h.store.GetSecretVersion(ctx, ref, 1, "")
	if err != nil {
		t.Fatal(err)
	}
	if !afterBind.Bound || !bytes.Equal(afterBind.Ciphertext, original.Ciphertext) || bytes.Equal(afterBind.EncryptedDEK, original.EncryptedDEK) || len(afterBind.BindingKeySalt) == 0 {
		t.Fatalf("bind did not perform an in-place DEK rewrap: before=%+v after=%+v", original, afterBind)
	}
	info, err := h.svc.GetSecretInfo(ctx, h.admin, ref)
	if err != nil || !info.Bound {
		t.Fatalf("metadata after bind = %+v err=%v", info, err)
	}
	assertSecretVersionProtection(t, info, 1, true, false)
	if stored, err := h.svc.GetConfigurationRelease(ctx, h.admin, ref.NS, "runtime", release.Version); err != nil || stored.Digest != release.Digest || len(stored.Entries) != 1 || stored.Entries[0].Version != 1 {
		t.Fatalf("bind changed immutable release pin: %+v err=%v", stored, err)
	}
	if validation, err := h.svc.ValidateConfigurationRelease(ctx, h.admin, ref.NS, "runtime", release.Version); err != nil || len(validation) != 0 {
		t.Fatalf("bound live property invalidated release: validation=%+v err=%v", validation, err)
	}

	preview, err := h.svc.PreviewSecretBindingCohort(ctx, h.admin, ref, 0, integrationBindingKeyA)
	if err != nil || preview.AnchorVersion != 1 || !slices.Equal(preview.AffectedVersions, []uint64{1}) || preview.Revision != bound.Revision {
		t.Fatalf("PreviewSecretBindingCohort = %+v err=%v", preview, err)
	}
	for _, unusable := range []string{"", "short", string([]byte{0xff, 0xfe}), integrationBindingKeyB} {
		if _, err := h.svc.UnbindSecret(ctx, h.admin, ref, 1, unusable); !errors.Is(err, domain.ErrDecryptFailed) || err.Error() != domain.ErrDecryptFailed.Error() {
			t.Fatalf("UnbindSecret(unusable key) err = %v, want identical ErrDecryptFailed", err)
		}
	}
	_, stillBound, err := h.store.GetSecretVersion(ctx, ref, 1, "")
	if err != nil || !bytes.Equal(stillBound.EncryptedDEK, afterBind.EncryptedDEK) {
		t.Fatalf("failed unbind changed wrapping: %+v err=%v", stillBound, err)
	}

	unbound, err := h.svc.UnbindSecret(ctx, h.admin, ref, 1, integrationBindingKeyA)
	if err != nil {
		t.Fatalf("UnbindSecret: %v", err)
	}
	if unbound.AnchorVersion != 1 || !slices.Equal(unbound.AffectedVersions, []uint64{1}) {
		t.Fatalf("UnbindSecret result = %+v", unbound)
	}
	_, afterUnbind, err := h.store.GetSecretVersion(ctx, ref, 1, "")
	if err != nil {
		t.Fatal(err)
	}
	if afterUnbind.Bound || !bytes.Equal(afterUnbind.Ciphertext, original.Ciphertext) || bytes.Equal(afterUnbind.EncryptedDEK, afterBind.EncryptedDEK) || len(afterUnbind.BindingKeySalt) != 0 {
		t.Fatalf("unbind did not perform an in-place DEK rewrap: %+v", afterUnbind)
	}
	info, err = h.svc.GetSecretInfo(ctx, h.admin, ref)
	if err != nil || info.Bound {
		t.Fatalf("metadata after unbind = %+v err=%v", info, err)
	}
	assertSecretVersionProtection(t, info, 1, false, false)
	if validation, err := h.svc.ValidateConfigurationRelease(ctx, h.admin, ref.NS, "runtime", release.Version); err != nil || len(validation) != 0 {
		t.Fatalf("unbound live property invalidated release: validation=%+v err=%v", validation, err)
	}
	if got, err := h.svc.GetSecret(ctx, h.admin, ref, 1, "", "", ""); err != nil || string(got.Value) != "stable-ciphertext" {
		t.Fatalf("unbound value = %q err=%v", got.Value, err)
	}
	if _, err := h.svc.BindSecret(ctx, h.admin, ref, 1, "short"); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("BindSecret(short new key) err = %v, want ErrInvalidArgument", err)
	}
}

func TestBindingCohortPreviewRotationAndCAS(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	ref := h.ensureNS("/prod/app/cohorts")
	keys := []string{
		integrationBindingKeyA,
		integrationBindingKeyB,
		integrationBindingKeyB,
		integrationBindingKeyC,
		integrationBindingKeyB,
		integrationBindingKeyB,
	}
	for i, key := range keys {
		if _, err := h.svc.PutSecret(ctx, h.admin, core.PutSecretInput{
			Ref: ref, Value: []byte(fmt.Sprintf("value-%d", i+1)), BindingKey: key,
		}); err != nil {
			t.Fatalf("PutSecret v%d: %v", i+1, err)
		}
	}

	preview, err := h.svc.PreviewSecretBindingCohort(ctx, h.admin, ref, 2, integrationBindingKeyB)
	if err != nil || preview.AnchorVersion != 2 || !slices.Equal(preview.AffectedVersions, []uint64{2, 3}) {
		t.Fatalf("preview first B cohort = %+v err=%v", preview, err)
	}
	currentPreview, err := h.svc.PreviewSecretBindingCohort(ctx, h.admin, ref, 0, integrationBindingKeyB)
	if err != nil || currentPreview.AnchorVersion != 6 || !slices.Equal(currentPreview.AffectedVersions, []uint64{5, 6}) {
		t.Fatalf("preview reused but separated B cohort = %+v err=%v", currentPreview, err)
	}
	if _, err := h.svc.PreviewSecretBindingCohort(ctx, h.admin, ref, 2, integrationBindingKeyA); !errors.Is(err, domain.ErrDecryptFailed) || err.Error() != domain.ErrDecryptFailed.Error() {
		t.Fatalf("preview with wrong anchor key err = %v, want identical ErrDecryptFailed", err)
	}
	if revision, err := h.store.CurrentRevision(ctx); err != nil || revision != currentPreview.Revision {
		t.Fatalf("preview changed revision: got %d err=%v, want %d", revision, err, currentPreview.Revision)
	}

	if _, err := h.svc.RotateSecretBindingKey(ctx, h.admin, ref, 2, integrationBindingKeyB, integrationBindingKeyD, &preview.Revision, nil); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("revision-only guard err = %v, want ErrInvalidArgument", err)
	}
	if _, err := h.svc.RotateSecretBindingKey(ctx, h.admin, ref, 2, integrationBindingKeyB, integrationBindingKeyD, nil, preview.AffectedVersions); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("versions-only guard err = %v, want ErrInvalidArgument", err)
	}
	if _, err := h.svc.RotateSecretBindingKey(ctx, h.admin, ref, 2, integrationBindingKeyB, integrationBindingKeyD, &preview.Revision, []uint64{2}); !errors.Is(err, domain.ErrAborted) {
		t.Fatalf("affected-set mismatch err = %v, want ErrAborted", err)
	}
	if _, err := h.svc.PutSecret(ctx, h.admin, h.stdSecret("/prod/app/unrelated-revision", "advance")); err != nil {
		t.Fatalf("advance revision: %v", err)
	}
	if _, err := h.svc.RotateSecretBindingKey(ctx, h.admin, ref, 2, integrationBindingKeyB, integrationBindingKeyD, &preview.Revision, preview.AffectedVersions); !errors.Is(err, domain.ErrAborted) {
		t.Fatalf("stale revision err = %v, want ErrAborted", err)
	}
	if got, err := h.svc.PreviewSecretBindingCohort(ctx, h.admin, ref, 2, integrationBindingKeyB); err != nil || !slices.Equal(got.AffectedVersions, []uint64{2, 3}) {
		t.Fatalf("failed CAS changed cohort: %+v err=%v", got, err)
	}

	before := make(map[uint64]storage.SecretVersionRecord, 2)
	for _, version := range []uint64{2, 3} {
		_, before[version], err = h.store.GetSecretVersion(ctx, ref, version, "")
		if err != nil {
			t.Fatal(err)
		}
	}
	preview, err = h.svc.PreviewSecretBindingCohort(ctx, h.admin, ref, 2, integrationBindingKeyB)
	if err != nil {
		t.Fatal(err)
	}
	rotated, err := h.svc.RotateSecretBindingKey(ctx, h.admin, ref, 2, integrationBindingKeyB, integrationBindingKeyD, &preview.Revision, preview.AffectedVersions)
	if err != nil {
		t.Fatalf("RotateSecretBindingKey: %v", err)
	}
	if rotated.AnchorVersion != 2 || !slices.Equal(rotated.AffectedVersions, []uint64{2, 3}) || rotated.Revision <= preview.Revision {
		t.Fatalf("rotation result = %+v, preview = %+v", rotated, preview)
	}
	for _, version := range rotated.AffectedVersions {
		_, after, err := h.store.GetSecretVersion(ctx, ref, version, "")
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(after.Ciphertext, before[version].Ciphertext) || after.KEKID != before[version].KEKID ||
			bytes.Equal(after.EncryptedDEK, before[version].EncryptedDEK) || bytes.Equal(after.BindingKeySalt, before[version].BindingKeySalt) {
			t.Fatalf("v%d did not preserve ciphertext/KEK while rotating DEK wrapping", version)
		}
		if _, err := h.svc.GetSecret(ctx, h.admin, ref, version, "", "", integrationBindingKeyB); !errors.Is(err, domain.ErrDecryptFailed) {
			t.Fatalf("old key still read v%d: %v", version, err)
		}
		if got, err := h.svc.GetSecret(ctx, h.admin, ref, version, "", "", integrationBindingKeyD); err != nil || string(got.Value) != fmt.Sprintf("value-%d", version) {
			t.Fatalf("new key read v%d = %q err=%v", version, got.Value, err)
		}
	}
	if got, err := h.svc.GetSecret(ctx, h.admin, ref, 5, "", "", integrationBindingKeyB); err != nil || string(got.Value) != "value-5" {
		t.Fatalf("rotation crossed separated cohort: value=%q err=%v", got.Value, err)
	}
}

func TestPurgeBindingCohortInvalidatesReleaseAndPreservesHighWater(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	const app = "purge-release"
	if _, err := h.svc.CreateApplication(ctx, h.admin, domain.Application{Name: app, ReleaseName: "runtime"}); err != nil {
		t.Fatalf("CreateApplication: %v", err)
	}
	ref := h.ensureNS("/prod/" + app + "/api-key")
	if _, err := h.svc.PutSecret(ctx, h.admin, core.PutSecretInput{Ref: ref, Value: []byte("safe-v1"), BindingKey: integrationBindingKeyA}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.PutSecret(ctx, h.admin, core.PutSecretInput{
		Ref: ref, Value: []byte("compromised-v2"), Metadata: `{"operator_note":"remove me"}`, BindingKey: integrationBindingKeyB,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.PutSecret(ctx, h.admin, core.PutSecretInput{Ref: ref, Value: []byte("compromised-v3"), BindingKey: integrationBindingKeyB}); err != nil {
		t.Fatal(err)
	}
	_, v2Before, err := h.store.GetSecretVersion(ctx, ref, 2, "")
	if err != nil {
		t.Fatal(err)
	}
	_, v3Before, err := h.store.GetSecretVersion(ctx, ref, 3, "")
	if err != nil {
		t.Fatal(err)
	}

	ns := nsRef("prod", app)
	release, err := h.svc.CreateConfigurationRelease(ctx, h.admin, domain.CreateConfigurationReleaseInput{
		Namespace: ns,
		Name:      "runtime",
		Entries: []domain.ReleaseEntrySelector{{
			Alias: "api_key", Kind: domain.ReleaseEntrySecret, Ref: ref, Version: 2,
		}},
	})
	if err != nil {
		t.Fatalf("CreateConfigurationRelease: %v", err)
	}
	zero := uint64(0)
	if _, changed, err := h.svc.ActivateConfigurationRelease(ctx, h.admin, ns, "runtime", release.Version, &zero); err != nil || !changed {
		t.Fatalf("ActivateConfigurationRelease changed=%v err=%v", changed, err)
	}
	if _, err := h.svc.DestroySecretVersion(ctx, h.admin, ref, 2); !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Fatalf("ordinary destroy bypassed immutable release pin: %v", err)
	}

	preview, err := h.svc.PreviewSecretBindingCohort(ctx, h.admin, ref, 2, integrationBindingKeyB)
	if err != nil || !slices.Equal(preview.AffectedVersions, []uint64{2, 3}) {
		t.Fatalf("preview compromised cohort = %+v err=%v", preview, err)
	}
	for _, unusable := range []string{"", "short", string([]byte{0xff, 0xfe}), integrationBindingKeyA} {
		if _, err := h.svc.PurgeSecretBindingCohort(ctx, h.admin, ref, 2, unusable, nil, nil); !errors.Is(err, domain.ErrDecryptFailed) || err.Error() != domain.ErrDecryptFailed.Error() {
			t.Fatalf("purge with unusable key err = %v, want identical ErrDecryptFailed", err)
		}
	}
	if _, err := h.svc.PurgeSecretBindingCohort(ctx, h.admin, ref, 2, integrationBindingKeyB, &preview.Revision, nil); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("purge revision-only guard err = %v, want ErrInvalidArgument", err)
	}
	if _, err := h.svc.PurgeSecretBindingCohort(ctx, h.admin, ref, 2, integrationBindingKeyB, &preview.Revision, []uint64{2}); !errors.Is(err, domain.ErrAborted) {
		t.Fatalf("purge affected-set mismatch err = %v, want ErrAborted", err)
	}
	if cohort, err := h.svc.PreviewSecretBindingCohort(ctx, h.admin, ref, 2, integrationBindingKeyB); err != nil || !slices.Equal(cohort.AffectedVersions, preview.AffectedVersions) {
		t.Fatalf("failed purge guard changed cohort: %+v err=%v", cohort, err)
	}
	delegate, _ := h.createClient("delegated-purger")
	h.grant("delegated-purge-policy", "delegated-purger", []domain.PolicyRule{
		allowRule(domain.OpSecretDestroy, "prod", app),
	}, nil)
	revisionBeforeDenied, err := h.store.CurrentRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, deniedExisting := h.svc.PurgeSecretBindingCohort(ctx, delegate, ref, 2, integrationBindingKeyB, nil, nil)
	_, deniedMissing := h.svc.PurgeSecretBindingCohort(ctx, delegate, domain.Ref{NS: ns, Key: "missing"}, 999, "short", nil, nil)
	if !errors.Is(deniedExisting, domain.ErrPermissionDenied) || !errors.Is(deniedMissing, domain.ErrPermissionDenied) || deniedExisting.Error() != deniedMissing.Error() {
		t.Fatalf("non-admin purge leaked key/cohort existence: existing=%v missing=%v", deniedExisting, deniedMissing)
	}
	if revision, err := h.store.CurrentRevision(ctx); err != nil || revision != revisionBeforeDenied {
		t.Fatalf("denied purge changed revision: %d -> %d err=%v", revisionBeforeDenied, revision, err)
	}

	purged, err := h.svc.PurgeSecretBindingCohort(ctx, h.admin, ref, 2, integrationBindingKeyB, &preview.Revision, preview.AffectedVersions)
	if err != nil {
		t.Fatalf("PurgeSecretBindingCohort: %v", err)
	}
	if purged.AnchorVersion != 2 || !slices.Equal(purged.AffectedVersions, []uint64{2, 3}) || purged.Revision <= preview.Revision {
		t.Fatalf("purge result = %+v", purged)
	}
	info, err := h.svc.GetSecretInfo(ctx, h.admin, ref)
	if err != nil {
		t.Fatal(err)
	}
	if info.Bound || info.Labels[domain.LabelCurrent] != 3 || info.Labels[domain.LabelPrevious] != 2 {
		t.Fatalf("purge moved current or retained its bound summary: %+v", info)
	}
	assertSecretVersionProtection(t, info, 1, true, false)
	for _, version := range []uint64{2, 3} {
		versionInfo := findSecretVersionInfo(t, info, version)
		if versionInfo.State != domain.StateDestroyed || versionInfo.Bound || versionInfo.HasAccessToken || versionInfo.Metadata != "" {
			t.Fatalf("v%d is not a minimal public tombstone: %+v", version, versionInfo)
		}
		_, stored, err := h.store.GetSecretVersion(ctx, ref, version, "")
		if err != nil {
			t.Fatal(err)
		}
		if stored.State != domain.StateDestroyed || len(stored.Ciphertext) != 0 || len(stored.EncryptedDEK) != 0 || len(stored.Nonce) != 0 || len(stored.BindingKeySalt) != 0 || stored.Metadata != "" {
			t.Fatalf("v%d retained recoverable payload: %+v", version, stored)
		}
	}
	if _, err := h.svc.GetSecret(ctx, h.admin, ref, 0, "", "", ""); !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Fatalf("purged current read err = %v, want ErrFailedPrecondition", err)
	}
	if got, err := h.svc.GetSecret(ctx, h.admin, ref, 1, "", "", integrationBindingKeyA); err != nil || string(got.Value) != "safe-v1" {
		t.Fatalf("purge crossed cohort boundary: v1=%q err=%v", got.Value, err)
	}

	disk := h.scanBytes()
	for name, value := range map[string][]byte{
		"v2 ciphertext":  v2Before.Ciphertext,
		"v2 wrapped DEK": v2Before.EncryptedDEK,
		"v3 ciphertext":  v3Before.Ciphertext,
		"v3 wrapped DEK": v3Before.EncryptedDEK,
		"binding key":    []byte(integrationBindingKeyB),
	} {
		if len(value) != 0 && bytes.Contains(disk, value) {
			t.Fatalf("purge left %s in the active database or WAL", name)
		}
	}

	storedRelease, err := h.svc.GetConfigurationRelease(ctx, h.admin, ns, "runtime", release.Version)
	if err != nil || storedRelease.Digest != release.Digest || len(storedRelease.Entries) != 1 || storedRelease.Entries[0].Version != 2 {
		t.Fatalf("immutable release manifest changed after purge: %+v err=%v", storedRelease, err)
	}
	active, err := h.svc.GetActiveConfigurationRelease(ctx, h.admin, ns, "runtime")
	if err != nil || active.Release.Version != release.Version {
		t.Fatalf("active release label changed after purge: %+v err=%v", active, err)
	}
	validation, err := h.svc.ValidateConfigurationRelease(ctx, h.admin, ns, "runtime", release.Version)
	if err != nil || !hasReleaseValidationError(validation, "api_key", domain.ReleaseValidationUnreadable) {
		t.Fatalf("validation after purge = %+v err=%v", validation, err)
	}
	if _, changed, err := h.svc.ActivateConfigurationRelease(ctx, h.admin, ns, "runtime", release.Version, nil); !errors.Is(err, domain.ErrFailedPrecondition) || changed {
		t.Fatalf("activation of purged pin changed=%v err=%v, want validation failure", changed, err)
	}

	// A separate path reuses the same key without creating any relationship.
	// After its entire cohort and row are removed, the retained high-water mark
	// prevents a recreated secret from satisfying an old numeric pin.
	reuseRef := h.ensureNS("/prod/" + app + "/high-water")
	for _, value := range []string{"old-v1", "old-v2"} {
		if _, err := h.svc.PutSecret(ctx, h.admin, core.PutSecretInput{Ref: reuseRef, Value: []byte(value), BindingKey: integrationBindingKeyB}); err != nil {
			t.Fatal(err)
		}
	}
	reusePreview, err := h.svc.PreviewSecretBindingCohort(ctx, h.admin, reuseRef, 0, integrationBindingKeyB)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.PurgeSecretBindingCohort(ctx, h.admin, reuseRef, 0, integrationBindingKeyB, &reusePreview.Revision, reusePreview.AffectedVersions); err != nil {
		t.Fatalf("purge reuse cohort: %v", err)
	}
	if _, err := h.svc.DeleteSecret(ctx, h.admin, reuseRef); err != nil {
		t.Fatalf("DeleteSecret after purge: %v", err)
	}
	recreated, err := h.svc.PutSecret(ctx, h.admin, core.PutSecretInput{Ref: reuseRef, Value: []byte("new-secret")})
	if err != nil || recreated.Version != 3 {
		t.Fatalf("recreated secret = %+v err=%v, want non-reused version 3", recreated, err)
	}
}

func assertSecretVersionProtection(t *testing.T, info domain.Secret, version uint64, bound, hasAccessToken bool) {
	t.Helper()
	got := findSecretVersionInfo(t, info, version)
	if got.Bound != bound || got.HasAccessToken != hasAccessToken {
		t.Fatalf("v%d protection = bound:%v access-token:%v, want bound:%v access-token:%v", version, got.Bound, got.HasAccessToken, bound, hasAccessToken)
	}
}

func findSecretVersionInfo(t *testing.T, info domain.Secret, version uint64) domain.SecretVersionInfo {
	t.Helper()
	for _, candidate := range info.Versions {
		if candidate.Version == version {
			return candidate
		}
	}
	t.Fatalf("secret metadata omitted version %d: %+v", version, info.Versions)
	return domain.SecretVersionInfo{}
}
