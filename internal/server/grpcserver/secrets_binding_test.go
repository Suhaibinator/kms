package grpcserver

import (
	"context"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/storage"
)

const (
	grpcBindingKeyA = "binding-key-a-0123456789-0123456789"
	grpcBindingKeyB = "binding-key-b-0123456789-0123456789"
	grpcBindingKeyC = "binding-key-c-0123456789-0123456789"
)

func TestSecretBindingTransitionsCreateVersions(t *testing.T) {
	env := newTestEnv(t, true)
	env.store.addNamespace(domain.NamespaceRef{Env: "prod", App: "svc"})
	client := env.secret()
	secretRef := pRef("prod", "svc", "credentials")
	expiresAt := time.Now().Add(time.Hour).UnixMilli()
	put, err := client.PutSecretV03(adminCtx(), &kmsv1.PutSecretRequest{Ref: secretRef, Value: []byte("value"), ContentType: "text/plain", MetadataJson: `{"epoch":1}`, ExpiresAtUnixMs: expiresAt})
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	bound, err := client.BindSecret(adminCtx(), &kmsv1.BindSecretRequest{Ref: secretRef, ExpectedCurrentVersion: put.GetVersion(), BindingKey: grpcBindingKeyA})
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	if bound.GetCurrentVersion() != 2 || bound.GetPreviousVersion() != 1 || bound.GetRevision() <= put.GetRevision() {
		t.Fatalf("bind response = %+v", bound)
	}
	if _, err := client.BindSecret(adminCtx(), &kmsv1.BindSecretRequest{Ref: secretRef, ExpectedCurrentVersion: 1, BindingKey: grpcBindingKeyB}); codeOf(err) != codes.Aborted {
		t.Fatalf("stale bind guard = %v, want Aborted", err)
	}
	rotated, err := client.RotateSecretBindingKey(adminCtx(), &kmsv1.RotateSecretBindingKeyRequest{Ref: secretRef, ExpectedCurrentVersion: 2, BindingKey: grpcBindingKeyA, NewBindingKey: grpcBindingKeyB})
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if rotated.GetCurrentVersion() != 3 || rotated.GetPreviousVersion() != 2 {
		t.Fatalf("rotate response = %+v", rotated)
	}
	if _, err := client.GetSecret(adminCtx(), &kmsv1.GetSecretRequest{Ref: secretRef, Version: 2, BindingKey: grpcBindingKeyA}); err != nil {
		t.Fatalf("historical source no longer opens with old key: %v", err)
	}
	if _, err := client.GetSecret(adminCtx(), &kmsv1.GetSecretRequest{Ref: secretRef, Version: 3, BindingKey: grpcBindingKeyB}); err != nil {
		t.Fatalf("rotated current does not open with new key: %v", err)
	}
	unbound, err := client.UnbindSecret(adminCtx(), &kmsv1.UnbindSecretRequest{Ref: secretRef, ExpectedCurrentVersion: 3, BindingKey: grpcBindingKeyB})
	if err != nil {
		t.Fatalf("unbind: %v", err)
	}
	if unbound.GetCurrentVersion() != 4 || unbound.GetPreviousVersion() != 3 {
		t.Fatalf("unbind response = %+v", unbound)
	}
	got, err := client.GetSecret(adminCtx(), &kmsv1.GetSecretRequest{Ref: secretRef, Version: 4})
	if err != nil || string(got.GetValue()) != "value" || got.GetContentType() != "text/plain" || got.GetMetadataJson() != `{"epoch":1}` {
		t.Fatalf("unbound clone = %+v, %v", got, err)
	}
	env.store.mu.Lock()
	row := env.store.secrets["/prod/svc/credentials"]
	v1, v2, v3, v4 := row.versions[1], row.versions[2], row.versions[3], row.versions[4]
	audits := slices.Clone(env.store.audit)
	changes := slices.Clone(env.store.changelog)
	env.store.mu.Unlock()
	if v1.Bound || !v2.Bound || !v3.Bound || v4.Bound {
		t.Fatalf("version binding modes = %v %v %v %v", v1.Bound, v2.Bound, v3.Bound, v4.Bound)
	}
	if v4.ExpiresAt.UnixMilli() != expiresAt || v4.State != domain.StateEnabled || v4.ContentType != "text/plain" || v4.Metadata != `{"epoch":1}` {
		t.Fatalf("transition did not preserve source properties: %+v", v4)
	}
	if reflect.DeepEqual(v3.Ciphertext, v4.Ciphertext) || v3.AAD == v4.AAD || reflect.DeepEqual(v3.Nonce, v4.Nonce) {
		t.Fatal("transition reused cryptographic material")
	}
	wantAffected := map[string]struct {
		versions   []uint64
		metadata   string
		changeType string
	}{
		"secret.bind":               {[]uint64{1, 2}, `{"affected_versions":[1,2]}`, domain.ChangeBind},
		"secret.binding_key.rotate": {[]uint64{2, 3}, `{"affected_versions":[2,3]}`, domain.ChangeRotateBindingKey},
		"secret.unbind":             {[]uint64{3, 4}, `{"affected_versions":[3,4]}`, domain.ChangeUnbind},
	}
	for eventType, want := range wantAffected {
		found := false
		for _, audit := range audits {
			if audit.EventType == eventType {
				if strings.Contains(audit.Metadata, grpcBindingKeyA) || strings.Contains(audit.Metadata, grpcBindingKeyB) {
					t.Fatalf("audit leaked binding key: %+v", audit)
				}
				if audit.Decision == "allow" && audit.Metadata == want.metadata {
					found = true
				}
			}
		}
		if !found {
			t.Fatalf("missing %s audit", eventType)
		}
		found = false
		for _, change := range changes {
			if change.ChangeType == want.changeType && slices.Equal(change.AffectedVersions, want.versions) {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("missing %s change with affected versions %v: %+v", want.changeType, want.versions, changes)
		}
	}
}

func TestSecretBindingTransitionAuditFailureRollsBack(t *testing.T) {
	env := newTestEnv(t, true)
	env.store.addNamespace(domain.NamespaceRef{Env: "prod", App: "svc"})
	client := env.secret()
	secretRef := pRef("prod", "svc", "audit-rollback")
	if _, err := client.PutSecretV03(adminCtx(), &kmsv1.PutSecretRequest{Ref: secretRef, Value: []byte("value")}); err != nil {
		t.Fatalf("put: %v", err)
	}
	env.store.mu.Lock()
	before := cloneMemSecretVersion(env.store.secrets["/prod/svc/audit-rollback"].versions[1])
	beforeRevision := env.store.revision
	env.store.auditErr = context.Canceled
	env.store.mu.Unlock()
	_, err := client.BindSecret(adminCtx(), &kmsv1.BindSecretRequest{Ref: secretRef, ExpectedCurrentVersion: 1, BindingKey: grpcBindingKeyA})
	if codeOf(err) != codes.FailedPrecondition || status.Convert(err).Message() != "audit unavailable: failed precondition" {
		t.Fatalf("bind audit failure = %v", err)
	}
	env.store.mu.Lock()
	after := cloneMemSecretVersion(env.store.secrets["/prod/svc/audit-rollback"].versions[1])
	_, created := env.store.secrets["/prod/svc/audit-rollback"].versions[2]
	afterRevision := env.store.revision
	env.store.mu.Unlock()
	if !reflect.DeepEqual(after, before) || created || afterRevision != beforeRevision {
		t.Fatal("audit failure mutated state")
	}
}

func TestSecretBindingCohortPurgeRequiresPreviewAndAuditsAffectedVersions(t *testing.T) {
	env := newTestEnv(t, true)
	env.store.addNamespace(domain.NamespaceRef{Env: "prod", App: "svc"})
	client := env.secret()
	secretRef := pRef("prod", "svc", "purge-guard")
	if _, err := client.PutSecretV03(adminCtx(), &kmsv1.PutSecretRequest{Ref: secretRef, Value: []byte("value"), BindingKey: grpcBindingKeyA}); err != nil {
		t.Fatalf("put: %v", err)
	}
	preview, err := client.PreviewSecretBindingCohort(adminCtx(), &kmsv1.PreviewSecretBindingCohortRequest{
		Ref: secretRef, AnchorVersion: 1, BindingKey: grpcBindingKeyA,
	})
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if _, err := client.PurgeSecretBindingCohort(adminCtx(), &kmsv1.PurgeSecretBindingCohortRequest{
		Ref: secretRef, AnchorVersion: 1, BindingKey: grpcBindingKeyA,
	}); codeOf(err) != codes.InvalidArgument {
		t.Fatalf("unguarded purge = %v, want InvalidArgument", err)
	}
	purged, err := client.PurgeSecretBindingCohort(adminCtx(), &kmsv1.PurgeSecretBindingCohortRequest{
		Ref: secretRef, AnchorVersion: 1, BindingKey: grpcBindingKeyA,
		ExpectedRevision: preview.GetRevision(), ExpectedAffectedVersions: preview.GetAffectedVersions(),
	})
	if err != nil || !slices.Equal(purged.GetAffectedVersions(), []uint64{1}) {
		t.Fatalf("purge = %+v, %v", purged, err)
	}
	env.store.mu.Lock()
	audits := slices.Clone(env.store.audit)
	env.store.mu.Unlock()
	found := false
	for _, audit := range audits {
		if audit.EventType == "secret.binding_cohort.purge" && audit.Decision == "allow" {
			found = true
			if audit.ResourceVersion != 1 || audit.Metadata != `{"affected_versions":[1]}` {
				t.Fatalf("bound purge audit = %+v", audit)
			}
		}
	}
	if !found {
		t.Fatal("missing bound purge allow audit")
	}
}

func TestUnboundVersionPreviewAndPurge(t *testing.T) {
	env := newTestEnv(t, true)
	env.store.addNamespace(domain.NamespaceRef{Env: "prod", App: "svc"})
	client := env.secret()
	secretRef := pRef("prod", "svc", "unbound")
	if _, err := client.PutSecretV03(adminCtx(), &kmsv1.PutSecretRequest{Ref: secretRef, Value: []byte("v1")}); err != nil {
		t.Fatalf("put v1: %v", err)
	}
	if _, err := client.PutSecretV03(adminCtx(), &kmsv1.PutSecretRequest{Ref: secretRef, Value: []byte("v2"), BindingKey: grpcBindingKeyA}); err != nil {
		t.Fatalf("put v2: %v", err)
	}
	if _, err := client.PutSecretV03(adminCtx(), &kmsv1.PutSecretRequest{Ref: secretRef, Value: []byte("v3")}); err != nil {
		t.Fatalf("put v3: %v", err)
	}
	preview, err := client.PreviewSecretUnboundVersions(adminCtx(), &kmsv1.PreviewSecretUnboundVersionsRequest{Ref: secretRef})
	if err != nil || !slices.Equal(preview.GetAffectedVersions(), []uint64{1, 3}) || preview.GetRevision() == 0 {
		t.Fatalf("preview = %+v, %v", preview, err)
	}
	env.store.addPolicy(domain.Policy{Name: "delegated-destroy", Subject: "client", Allow: []domain.PolicyRule{{Operation: domain.OpSecretDestroy, Env: "prod", App: "svc"}}})
	if _, err := client.PreviewSecretUnboundVersions(clientCtx(), &kmsv1.PreviewSecretUnboundVersionsRequest{Ref: secretRef}); codeOf(err) != codes.PermissionDenied {
		t.Fatalf("non-admin preview = %v, want PermissionDenied", err)
	}
	if _, err := client.PurgeSecretUnboundVersions(adminCtx(), &kmsv1.PurgeSecretUnboundVersionsRequest{Ref: secretRef, ExpectedRevision: preview.GetRevision(), ExpectedAffectedVersions: []uint64{1}}); codeOf(err) != codes.Aborted {
		t.Fatalf("mismatched set purge = %v, want Aborted", err)
	}
	purged, err := client.PurgeSecretUnboundVersions(adminCtx(), &kmsv1.PurgeSecretUnboundVersionsRequest{Ref: secretRef, ExpectedRevision: preview.GetRevision(), ExpectedAffectedVersions: preview.GetAffectedVersions()})
	if err != nil || !slices.Equal(purged.GetAffectedVersions(), []uint64{1, 3}) || purged.GetRevision() <= preview.GetRevision() {
		t.Fatalf("purge = %+v, %v", purged, err)
	}
	if _, err := client.GetSecret(adminCtx(), &kmsv1.GetSecretRequest{Ref: secretRef, Version: 2, BindingKey: grpcBindingKeyA}); err != nil {
		t.Fatalf("bound version was affected: %v", err)
	}
	env.store.mu.Lock()
	v1, v2, v3 := env.store.secrets["/prod/svc/unbound"].versions[1], env.store.secrets["/prod/svc/unbound"].versions[2], env.store.secrets["/prod/svc/unbound"].versions[3]
	audits := slices.Clone(env.store.audit)
	changes := slices.Clone(env.store.changelog)
	env.store.mu.Unlock()
	if v1.State != domain.StateDestroyed || v3.State != domain.StateDestroyed || v2.State == domain.StateDestroyed || len(v1.Ciphertext) != 0 || len(v3.Ciphertext) != 0 {
		t.Fatalf("unexpected tombstones: v1=%+v v2=%+v v3=%+v", v1, v2, v3)
	}
	foundAudit := false
	for _, audit := range audits {
		if audit.EventType == "secret.unbound_versions.purge" && audit.Decision == "allow" && audit.Metadata == `{"affected_versions":[1,3]}` {
			foundAudit = true
			if audit.ResourceVersion != 1 {
				t.Fatalf("unbound purge audit = %+v", audit)
			}
		}
	}
	if !foundAudit {
		t.Fatal("missing unbound purge allow audit")
	}
	foundChange := false
	for _, change := range changes {
		if change.ChangeType == domain.ChangePurgeUnbound {
			foundChange = true
			if change.Version != 1 || !slices.Equal(change.AffectedVersions, []uint64{1, 3}) {
				t.Fatalf("unbound purge change = %+v", change)
			}
		}
	}
	if !foundChange {
		t.Fatal("missing unbound purge change")
	}
}

func TestBindingCredentialFailuresRemainIndistinguishable(t *testing.T) {
	env := newTestEnv(t, true)
	env.store.addNamespace(domain.NamespaceRef{Env: "prod", App: "svc"})
	client := env.secret()
	secretRef := pRef("prod", "svc", "credential-errors")
	if _, err := client.PutSecretV03(adminCtx(), &kmsv1.PutSecretRequest{Ref: secretRef, Value: []byte("value"), BindingKey: grpcBindingKeyA}); err != nil {
		t.Fatalf("put: %v", err)
	}
	guard, err := client.PreviewSecretBindingCohort(adminCtx(), &kmsv1.PreviewSecretBindingCohortRequest{Ref: secretRef, AnchorVersion: 1, BindingKey: grpcBindingKeyA})
	if err != nil {
		t.Fatalf("preview guard: %v", err)
	}
	operations := []struct {
		name string
		call func(string) error
	}{
		{"unbind", func(key string) error {
			_, err := client.UnbindSecret(adminCtx(), &kmsv1.UnbindSecretRequest{Ref: secretRef, ExpectedCurrentVersion: 1, BindingKey: key})
			return err
		}},
		{"preview", func(key string) error {
			_, err := client.PreviewSecretBindingCohort(adminCtx(), &kmsv1.PreviewSecretBindingCohortRequest{Ref: secretRef, AnchorVersion: 1, BindingKey: key})
			return err
		}},
		{"rotate", func(key string) error {
			_, err := client.RotateSecretBindingKey(adminCtx(), &kmsv1.RotateSecretBindingKeyRequest{Ref: secretRef, ExpectedCurrentVersion: 1, BindingKey: key, NewBindingKey: grpcBindingKeyC})
			return err
		}},
		{"purge", func(key string) error {
			_, err := client.PurgeSecretBindingCohort(adminCtx(), &kmsv1.PurgeSecretBindingCohortRequest{
				Ref: secretRef, AnchorVersion: 1, BindingKey: key,
				ExpectedRevision: guard.GetRevision(), ExpectedAffectedVersions: guard.GetAffectedVersions(),
			})
			return err
		}},
	}
	for _, operation := range operations {
		t.Run(operation.name, func(t *testing.T) {
			for _, credential := range []string{"", "short", grpcBindingKeyB} {
				err := operation.call(credential)
				if codeOf(err) != codes.Internal || status.Convert(err).Message() != "internal error" {
					t.Fatalf("credential %q error = %v", credential, err)
				}
			}
		})
	}
}

func TestOversizedBindingKeyIsRejectedAndRedactedAcrossGRPC(t *testing.T) {
	env := newTestEnv(t, true)
	env.store.addNamespace(domain.NamespaceRef{Env: "prod", App: "svc"})
	client := env.secret()
	secretRef := pRef("prod", "svc", "oversized-binding-key")
	if _, err := client.PutSecretV03(adminCtx(), &kmsv1.PutSecretRequest{Ref: secretRef, Value: []byte("value"), BindingKey: grpcBindingKeyA}); err != nil {
		t.Fatalf("put: %v", err)
	}
	oversized := strings.Repeat("grpc-binding-size-canary-", 45)
	_, err := client.PreviewSecretBindingCohort(adminCtx(), &kmsv1.PreviewSecretBindingCohortRequest{
		Ref: secretRef, AnchorVersion: 1, BindingKey: oversized,
	})
	if codeOf(err) != codes.InvalidArgument {
		t.Fatalf("oversized binding key error = %v, want InvalidArgument", err)
	}
	if strings.Contains(status.Convert(err).Message(), oversized) || strings.Contains(status.Convert(err).Message(), "size-canary") {
		t.Fatalf("gRPC error reflected binding key: %v", err)
	}
}

func TestPurgeCleanupPendingMapsAcrossGRPC(t *testing.T) {
	env := newTestEnv(t, true)
	env.store.addNamespace(domain.NamespaceRef{Env: "prod", App: "svc"})
	client := env.secret()
	secretRef := pRef("prod", "svc", "cleanup-pending")
	if _, err := client.PutSecretV03(adminCtx(), &kmsv1.PutSecretRequest{Ref: secretRef, Value: []byte("value")}); err != nil {
		t.Fatalf("put: %v", err)
	}
	preview, err := client.PreviewSecretUnboundVersions(adminCtx(), &kmsv1.PreviewSecretUnboundVersionsRequest{Ref: secretRef})
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	env.store.setPurgeResultErr(storage.ErrPurgeCleanupPending)
	response, err := client.PurgeSecretUnboundVersions(adminCtx(), &kmsv1.PurgeSecretUnboundVersionsRequest{Ref: secretRef, ExpectedRevision: preview.GetRevision(), ExpectedAffectedVersions: preview.GetAffectedVersions()})
	if response != nil || codeOf(err) != codes.Unavailable || status.Convert(err).Message() != storage.ErrPurgeCleanupPending.Error() {
		t.Fatalf("cleanup pending = %+v, %v", response, err)
	}
}

func TestSecretMetadataSelectors(t *testing.T) {
	env := newTestEnv(t, true)
	env.store.addNamespace(domain.NamespaceRef{Env: "prod", App: "svc"})
	client := env.secret()
	ref := pRef("prod", "svc", "metadata")
	for _, key := range []string{"", grpcBindingKeyA} {
		if _, err := client.PutSecretV03(adminCtx(), &kmsv1.PutSecretRequest{Ref: ref, Value: []byte("value"), BindingKey: key}); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name        string
		version     uint64
		label       string
		wantVersion uint64
		wantBound   bool
		wantCode    codes.Code
	}{
		{name: "exact", version: 1, wantVersion: 1},
		{name: "current", label: "current", wantVersion: 2, wantBound: true},
		{name: "previous", label: "previous", wantVersion: 1},
		{name: "missing label", label: "missing", wantCode: codes.NotFound},
		{name: "missing version", version: 99, wantCode: codes.NotFound},
		{name: "conflicting selectors", version: 1, label: "current", wantCode: codes.InvalidArgument},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := client.GetSecretMetadata(adminCtx(), &kmsv1.GetSecretMetadataRequest{Ref: ref, Version: tc.version, Label: tc.label})
			if status.Code(err) != tc.wantCode {
				t.Fatalf("error = %v, want %s", err, tc.wantCode)
			}
			if err != nil {
				return
			}
			info := resp.GetSecret()
			if len(info.GetVersions()) != 1 || info.GetVersions()[0].GetVersion() != tc.wantVersion || info.GetBound() != tc.wantBound || info.GetVersions()[0].GetBound() != tc.wantBound {
				t.Fatalf("incorrect selected metadata: %v", info)
			}
			wantLabels := map[string]uint64(nil)
			if tc.label != "" {
				wantLabels = map[string]uint64{tc.label: tc.wantVersion}
			}
			if !reflect.DeepEqual(info.GetLabels(), wantLabels) {
				t.Fatalf("labels = %v, want %v", info.GetLabels(), wantLabels)
			}
		})
	}
	full, err := client.GetSecretMetadata(adminCtx(), &kmsv1.GetSecretMetadataRequest{Ref: ref})
	if err != nil || len(full.GetSecret().GetVersions()) != 2 || len(full.GetSecret().GetLabels()) != 2 {
		t.Fatalf("full history changed: %v, %v", full, err)
	}
	if _, err := client.GetSecretMetadata(context.Background(), &kmsv1.GetSecretMetadataRequest{Ref: ref, Label: "current"}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("unauthenticated label read: %v", err)
	}
}
