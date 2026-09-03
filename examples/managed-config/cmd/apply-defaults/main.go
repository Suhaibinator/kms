// Command apply-defaults uploads this application's generated schema,
// previews/applies its source-owned parameter defaults, or creates an inactive
// immutable release for review in KMS.
package main

import (
	"os"

	appconfig "github.com/Suhaibinator/kms/examples/managed-config/config"
	"github.com/Suhaibinator/kms/examples/managed-config/configkms"
	"github.com/Suhaibinator/kms/sdk/go/configstore"
)

func main() {
	os.Exit(configstore.RunManagedConfigCommand(
		os.Args[1:], os.Stdout, os.Stderr,
		configstore.ManagedConfigCommandConfig[string, appconfig.Config]{
			Application: appconfig.ApplicationName,
			Schema:      configkms.GeneratedSchema,
			Defaults: configstore.DefaultsApplierConfig[string, appconfig.Config]{
				Provider: appconfig.ManagedReleaseDefaults,
				Encoder:  configkms.EncodeDefaultsArtifact,
				Namespace: func(string) (string, error) {
					return "dev/managed-config", nil
				},
			},
		},
	))
}
