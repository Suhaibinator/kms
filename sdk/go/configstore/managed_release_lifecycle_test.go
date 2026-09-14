package configstore

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Suhaibinator/kms/sdk/go/kmsclient"
)

type lifecycleTestClient struct {
	fakeDefaultsApplyClient
	valid           bool
	activeErr       error
	activateErr     error
	expected        uint64
	activationCalls int
	target          kmsclient.ManagedReleaseTarget
}

func (c *lifecycleTestClient) ValidateManagedRelease(_ context.Context, t kmsclient.ManagedReleaseTarget) (kmsclient.ManagedReleaseValidation, error) {
	c.target = t
	result := kmsclient.ManagedReleaseValidation{Valid: c.valid}
	if !c.valid {
		result.Errors = []kmsclient.ApplicationReleaseValidationError{{Code: "schema_violation", Message: "NEVER_PRINT_RESOURCE_VALUES"}}
	}
	return result, nil
}
func (c *lifecycleTestClient) GetManagedReleaseActivation(context.Context, kmsclient.ManagedReleaseTarget) (kmsclient.ManagedReleaseActivation, error) {
	return kmsclient.ManagedReleaseActivation{Version: 3, ActivationRevision: 25}, c.activeErr
}
func (c *lifecycleTestClient) ActivateManagedRelease(_ context.Context, t kmsclient.ManagedReleaseTarget, v uint64) (kmsclient.ManagedReleaseActivation, error) {
	c.activationCalls++
	c.expected = v
	return kmsclient.ManagedReleaseActivation{Version: t.Version, ActivationRevision: 26, Changed: true}, c.activateErr
}

func TestManagedReleaseLifecycle(t *testing.T) {
	for _, tc := range []struct {
		name                     string
		activate, execute, valid bool
		namespace, confirm       string
		activeErr, activateErr   error
		want                     int
		calls                    int
	}{
		{name: "validate", valid: true, want: 0},
		{name: "preview", activate: true, valid: true, want: 0},
		{name: "execute", activate: true, execute: true, valid: true, want: 0, calls: 1},
		{name: "invalid", activate: true, execute: true, want: 1},
		{name: "production requires confirmation", activate: true, execute: true, valid: true, namespace: "prod-linkie/gradethis", want: 2},
		{name: "production confirmed", activate: true, execute: true, valid: true, namespace: "prod-linkie/gradethis", confirm: "prod-linkie", want: 0, calls: 1},
		{name: "conflict", activate: true, execute: true, valid: true, activateErr: errors.New("version conflict"), want: 1, calls: 1},
		{name: "read fails", activate: true, execute: true, valid: true, activeErr: errors.New("denied"), want: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &lifecycleTestClient{valid: tc.valid, activeErr: tc.activeErr, activateErr: tc.activateErr}
			args := []string{"--profile", "dev", "--schema-version", "7", "--version", "4", "--insecure"}
			if tc.execute {
				args = append(args, "--execute")
			}
			if tc.namespace != "" {
				args = append(args, "--namespace", tc.namespace)
			}
			if tc.confirm != "" {
				args = append(args, "--confirm-production", tc.confirm)
			}
			var out, errs bytes.Buffer
			code := runManagedReleaseLifecycle(args, &out, &errs, managedCommandTestConfig(), func(kmsclient.Config) (managedConfigClient, error) { return client, nil }, tc.activate)
			if code != tc.want || client.activationCalls != tc.calls {
				t.Fatalf("code=%d calls=%d errors=%s", code, client.activationCalls, errs.String())
			}
			if strings.Contains(out.String(), "NEVER_PRINT") {
				t.Fatal("leaked validation message")
			}
			if tc.calls > 0 && client.expected != 3 {
				t.Fatal("missing CAS guard")
			}
		})
	}
}
