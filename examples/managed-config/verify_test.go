package main

import (
	"testing"

	appconfig "github.com/Suhaibinator/kms/examples/managed-config/config"
	"github.com/Suhaibinator/kms/examples/managed-config/configkms"
	"github.com/Suhaibinator/kms/sdk/go/kmsverify"
)

// TestReleaseMatchesSourceDefaults is the CI tripwire for configuration
// drift: it hashes the source-owned defaults for the selected profile and
// asks a real KMS whether the active release still matches them. Without
// KMS_VERIFY_ENDPOINT the test skips, so ordinary `go test ./...` runs need no
// server; a release pipeline sets the KMS_VERIFY_* variables and
// KMS_VERIFY_REQUIRED=1 so a missing endpoint fails instead of skipping.
//
// This example has a single "default" profile that lives in the
// dev/managed-config namespace (see cmd/apply-defaults). An application with
// several environments maps each profile to its own namespace here, or sets
// KMS_VERIFY_NAMESPACE per CI job.
func TestReleaseMatchesSourceDefaults(t *testing.T) {
	kmsverify.Run(t, kmsverify.Spec[appconfig.Config]{
		Defaults: func(profile string) (*appconfig.Config, error) {
			if profile == "" {
				profile = "default"
			}
			return appconfig.ManagedReleaseDefaults(profile)
		},
		Verify: configkms.VerifyReleaseDefaults,
		Namespace: func(string) (string, error) {
			return "dev/managed-config", nil
		},
	})
}
