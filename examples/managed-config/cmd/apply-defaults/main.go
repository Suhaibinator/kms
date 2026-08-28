// Command apply-defaults previews or applies this application's source-owned
// parameter defaults to its KMS namespace.
package main

import (
	"os"

	appconfig "github.com/Suhaibinator/kms/examples/managed-config/config"
	"github.com/Suhaibinator/kms/examples/managed-config/configkms"
	"github.com/Suhaibinator/kms/sdk/go/configstore"
)

func main() {
	os.Exit(configstore.RunDefaultsApplier(
		os.Args[1:], os.Stdout, os.Stderr,
		configstore.DefaultsApplierConfig[string, appconfig.Config]{
			Provider: appconfig.ManagedReleaseDefaults,
			Encoder:  configkms.EncodeDefaultsArtifact,
			Namespace: func(string) (string, error) {
				return "dev/managed-config", nil
			},
		},
	))
}
