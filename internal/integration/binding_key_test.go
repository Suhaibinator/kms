package integration

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/Suhaibinator/kms/internal/core"
	"github.com/Suhaibinator/kms/internal/domain"
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
	if _, err := h.svc.RevealSecret(ctx, h.admin, ref, 1, "", ""); !errors.Is(err, domain.ErrDecryptFailed) {
		t.Fatalf("RevealSecret(missing binding key) err = %v, want ErrDecryptFailed", err)
	}
	revealed, err := h.svc.RevealSecret(ctx, h.admin, ref, 1, "", integrationBindingKeyA)
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

func TestUnbindCreatesNewVersionAndPreservesReleasePinnedSource(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	const app = "versioned-unbind"
	if _, err := h.svc.CreateApplication(ctx, h.admin, domain.Application{Name: app, ReleaseName: "runtime"}); err != nil {
		t.Fatalf("CreateApplication: %v", err)
	}
	ref := h.ensureNS("/prod/" + app + "/credential")
	if _, err := h.svc.PutSecret(ctx, h.admin, core.PutSecretInput{
		Ref: ref, Value: []byte("stable-value"), ContentType: "text/plain", Metadata: `{"owner":"ops"}`, BindingKey: integrationBindingKeyA,
	}); err != nil {
		t.Fatalf("PutSecret: %v", err)
	}
	oldRelease, err := h.svc.CreateConfigurationRelease(ctx, h.admin, domain.CreateConfigurationReleaseInput{
		Namespace: ref.NS, Name: "runtime",
		Entries: []domain.ReleaseEntrySelector{{Alias: "credential", Kind: domain.ReleaseEntrySecret, Ref: ref, Version: 1}},
	})
	if err != nil {
		t.Fatalf("CreateConfigurationRelease(old): %v", err)
	}
	_, sourceBefore, err := h.store.GetSecretVersion(ctx, ref, 1, "")
	if err != nil {
		t.Fatal(err)
	}

	transition, err := h.svc.UnbindSecret(ctx, h.admin, ref, 1, integrationBindingKeyA)
	if err != nil {
		t.Fatalf("UnbindSecret: %v", err)
	}
	if transition.CurrentVersion != 2 || transition.PreviousVersion != 1 {
		t.Fatalf("transition = %+v", transition)
	}
	_, sourceAfter, err := h.store.GetSecretVersion(ctx, ref, 1, "")
	if err != nil || !reflect.DeepEqual(sourceAfter, sourceBefore) {
		t.Fatalf("release-pinned source changed: before=%+v after=%+v err=%v", sourceBefore, sourceAfter, err)
	}
	if _, err := h.svc.GetSecret(ctx, h.admin, ref, 1, "", "", ""); !errors.Is(err, domain.ErrDecryptFailed) {
		t.Fatalf("historical bound source no longer requires its old key: %v", err)
	}
	if got, err := h.svc.GetSecret(ctx, h.admin, ref, 1, "", "", integrationBindingKeyA); err != nil || string(got.Value) != "stable-value" {
		t.Fatalf("historical source read=%q err=%v", got.Value, err)
	}
	if got, err := h.svc.GetSecret(ctx, h.admin, ref, 2, "", "", ""); err != nil || string(got.Value) != "stable-value" {
		t.Fatalf("new unbound read=%q err=%v", got.Value, err)
	}
	storedOld, err := h.svc.GetConfigurationRelease(ctx, h.admin, ref.NS, "runtime", oldRelease.Version)
	if err != nil || storedOld.Digest != oldRelease.Digest || storedOld.Entries[0].Version != 1 {
		t.Fatalf("old release changed: %+v err=%v", storedOld, err)
	}
	if validation, err := h.svc.ValidateConfigurationRelease(ctx, h.admin, ref.NS, "runtime", oldRelease.Version); err != nil || len(validation) != 0 {
		t.Fatalf("old release validation=%+v err=%v", validation, err)
	}
	newRelease, err := h.svc.CreateConfigurationRelease(ctx, h.admin, domain.CreateConfigurationReleaseInput{
		Namespace: ref.NS, Name: "runtime",
		Entries: []domain.ReleaseEntrySelector{{Alias: "credential", Kind: domain.ReleaseEntrySecret, Ref: ref, Version: 2}},
	})
	if err != nil {
		t.Fatalf("CreateConfigurationRelease(new): %v", err)
	}
	if newRelease.Digest == oldRelease.Digest || newRelease.Entries[0].Version != 2 {
		t.Fatalf("new release did not pin distinct version/digest: old=%s new=%+v", oldRelease.Digest, newRelease)
	}
}

func TestRotationCreatesOneVersionAndLeavesHistoricalCohortUnderOldKey(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	ref := h.ensureNS("/prod/app/versioned-rotation")
	for _, value := range []string{"old-1", "old-2"} {
		if _, err := h.svc.PutSecret(ctx, h.admin, core.PutSecretInput{Ref: ref, Value: []byte(value), BindingKey: integrationBindingKeyA}); err != nil {
			t.Fatal(err)
		}
	}
	_, v1Before, _ := h.store.GetSecretVersion(ctx, ref, 1, "")
	_, v2Before, _ := h.store.GetSecretVersion(ctx, ref, 2, "")
	if _, err := h.svc.RotateSecretBindingKey(ctx, h.admin, ref, 1, integrationBindingKeyA, integrationBindingKeyB); !errors.Is(err, domain.ErrAborted) {
		t.Fatalf("stale rotation guard err=%v", err)
	}
	rotated, err := h.svc.RotateSecretBindingKey(ctx, h.admin, ref, 2, integrationBindingKeyA, integrationBindingKeyB)
	if err != nil || rotated.CurrentVersion != 3 || rotated.PreviousVersion != 2 {
		t.Fatalf("rotation=%+v err=%v", rotated, err)
	}
	_, v1After, _ := h.store.GetSecretVersion(ctx, ref, 1, "")
	_, v2After, _ := h.store.GetSecretVersion(ctx, ref, 2, "")
	if !reflect.DeepEqual(v1After, v1Before) || !reflect.DeepEqual(v2After, v2Before) {
		t.Fatal("rotation changed its historical cohort")
	}
	if old, err := h.svc.PreviewSecretBindingCohort(ctx, h.admin, ref, 1, integrationBindingKeyA); err != nil || !slices.Equal(old.AffectedVersions, []uint64{1, 2}) {
		t.Fatalf("old cohort=%+v err=%v", old, err)
	}
	if current, err := h.svc.PreviewSecretBindingCohort(ctx, h.admin, ref, 3, integrationBindingKeyB); err != nil || !slices.Equal(current.AffectedVersions, []uint64{3}) {
		t.Fatalf("new cohort=%+v err=%v", current, err)
	}
	if got, err := h.svc.GetSecret(ctx, h.admin, ref, 2, "", "", integrationBindingKeyA); err != nil || string(got.Value) != "old-2" {
		t.Fatalf("old cohort read=%q err=%v", got.Value, err)
	}
	if _, err := h.svc.GetSecret(ctx, h.admin, ref, 3, "", "", integrationBindingKeyA); !errors.Is(err, domain.ErrDecryptFailed) {
		t.Fatalf("new version accepted old key: %v", err)
	}
	if got, err := h.svc.GetSecret(ctx, h.admin, ref, 3, "", "", integrationBindingKeyB); err != nil || string(got.Value) != "old-2" {
		t.Fatalf("rotated current read=%q err=%v", got.Value, err)
	}
	old, _ := h.svc.PreviewSecretBindingCohort(ctx, h.admin, ref, 1, integrationBindingKeyA)
	if _, err := h.svc.PurgeSecretBindingCohort(ctx, h.admin, ref, 1, integrationBindingKeyA, old.Revision, old.AffectedVersions); err != nil {
		t.Fatalf("purge old cohort: %v", err)
	}
	if got, err := h.svc.GetSecret(ctx, h.admin, ref, 3, "", "", integrationBindingKeyB); err != nil || string(got.Value) != "old-2" {
		t.Fatalf("old-cohort purge harmed new current: read=%q err=%v", got.Value, err)
	}
}

func TestPurgeUnboundVersionsBypassesReleasePinsAndPreservesBoundVersions(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	const app = "purge-unbound-release"
	if _, err := h.svc.CreateApplication(ctx, h.admin, domain.Application{Name: app, ReleaseName: "runtime"}); err != nil {
		t.Fatal(err)
	}
	ref := h.ensureNS("/prod/" + app + "/credential")
	for _, in := range []core.PutSecretInput{
		{Ref: ref, Value: []byte("plain-1")},
		{Ref: ref, Value: []byte("bound-2"), BindingKey: integrationBindingKeyA},
		{Ref: ref, Value: []byte("plain-3")},
	} {
		if _, err := h.svc.PutSecret(ctx, h.admin, in); err != nil {
			t.Fatal(err)
		}
	}
	release, err := h.svc.CreateConfigurationRelease(ctx, h.admin, domain.CreateConfigurationReleaseInput{
		Namespace: ref.NS, Name: "runtime",
		Entries: []domain.ReleaseEntrySelector{{Alias: "credential", Kind: domain.ReleaseEntrySecret, Ref: ref, Version: 3}},
	})
	if err != nil {
		t.Fatal(err)
	}
	zero := uint64(0)
	if _, changed, err := h.svc.ActivateConfigurationRelease(ctx, h.admin, ref.NS, "runtime", release.Version, &zero); err != nil || !changed {
		t.Fatalf("activate release changed=%v err=%v", changed, err)
	}
	if _, err := h.svc.DisableSecret(ctx, h.admin, ref, 1, false); err != nil {
		t.Fatalf("disable v1: %v", err)
	}
	if _, err := h.svc.DestroySecretVersion(ctx, h.admin, ref, 3); !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Fatalf("ordinary destroy bypassed release pin: %v", err)
	}
	preview, err := h.svc.PreviewSecretUnboundVersions(ctx, h.admin, ref)
	if err != nil || !slices.Equal(preview.AffectedVersions, []uint64{1, 3}) {
		t.Fatalf("preview=%+v err=%v", preview, err)
	}
	if _, err := h.svc.PurgeSecretUnboundVersions(ctx, h.admin, ref, preview.Revision, []uint64{1}); !errors.Is(err, domain.ErrAborted) {
		t.Fatalf("mismatched set guard err=%v", err)
	}
	purged, err := h.svc.PurgeSecretUnboundVersions(ctx, h.admin, ref, preview.Revision, preview.AffectedVersions)
	if err != nil || !slices.Equal(purged.AffectedVersions, []uint64{1, 3}) {
		t.Fatalf("purge=%+v err=%v", purged, err)
	}
	if got, err := h.svc.GetSecret(ctx, h.admin, ref, 2, "", "", integrationBindingKeyA); err != nil || string(got.Value) != "bound-2" {
		t.Fatalf("bound version after purge=%q err=%v", got.Value, err)
	}
	info, err := h.svc.GetSecretInfo(ctx, h.admin, ref)
	if err != nil || info.Labels[domain.LabelCurrent] != 3 || info.Labels[domain.LabelPrevious] != 2 {
		t.Fatalf("labels moved during purge: info=%+v err=%v", info, err)
	}
	if validation, err := h.svc.ValidateConfigurationRelease(ctx, h.admin, ref.NS, "runtime", release.Version); err != nil || len(validation) == 0 {
		t.Fatalf("purged release validation=%+v err=%v", validation, err)
	}
	created, err := h.svc.PutSecret(ctx, h.admin, core.PutSecretInput{Ref: ref, Value: []byte("new-v4")})
	if err != nil || created.Version != 4 {
		t.Fatalf("post-purge put=%+v err=%v", created, err)
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
		if _, err := h.svc.PurgeSecretBindingCohort(ctx, h.admin, ref, 2, unusable, preview.Revision, preview.AffectedVersions); !errors.Is(err, domain.ErrDecryptFailed) || err.Error() != domain.ErrDecryptFailed.Error() {
			t.Fatalf("purge with unusable key err = %v, want identical ErrDecryptFailed", err)
		}
	}
	if _, err := h.svc.PurgeSecretBindingCohort(ctx, h.admin, ref, 2, integrationBindingKeyB, preview.Revision, nil); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("purge revision-only guard err = %v, want ErrInvalidArgument", err)
	}
	if _, err := h.svc.PurgeSecretBindingCohort(ctx, h.admin, ref, 2, integrationBindingKeyB, preview.Revision, []uint64{2}); !errors.Is(err, domain.ErrAborted) {
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
	_, deniedExisting := h.svc.PurgeSecretBindingCohort(ctx, delegate, ref, 2, integrationBindingKeyB, 0, nil)
	_, deniedMissing := h.svc.PurgeSecretBindingCohort(ctx, delegate, domain.Ref{NS: ns, Key: "missing"}, 999, "short", 0, nil)
	if !errors.Is(deniedExisting, domain.ErrPermissionDenied) || !errors.Is(deniedMissing, domain.ErrPermissionDenied) || deniedExisting.Error() != deniedMissing.Error() {
		t.Fatalf("non-admin purge leaked key/cohort existence: existing=%v missing=%v", deniedExisting, deniedMissing)
	}
	if revision, err := h.store.CurrentRevision(ctx); err != nil || revision != revisionBeforeDenied {
		t.Fatalf("denied purge changed revision: %d -> %d err=%v", revisionBeforeDenied, revision, err)
	}

	purged, err := h.svc.PurgeSecretBindingCohort(ctx, h.admin, ref, 2, integrationBindingKeyB, preview.Revision, preview.AffectedVersions)
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
	if _, err := h.svc.PurgeSecretBindingCohort(ctx, h.admin, reuseRef, 0, integrationBindingKeyB, reusePreview.Revision, reusePreview.AffectedVersions); err != nil {
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
