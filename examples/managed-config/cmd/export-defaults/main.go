// Command export-defaults emits the application's non-secret KMS baseline.
package main

import (
	"os"

	appconfig "github.com/Suhaibinator/kms/examples/managed-config/config"
	"github.com/Suhaibinator/kms/examples/managed-config/configkms"
	"github.com/Suhaibinator/kms/sdk/go/configstore"
)

func main() {
	os.Exit(configstore.RunDefaultsExporter(
		os.Args[1:], os.Stdout, os.Stderr,
		appconfig.ManagedReleaseDefaults,
		configkms.EncodeDefaultsArtifact,
	))
}
