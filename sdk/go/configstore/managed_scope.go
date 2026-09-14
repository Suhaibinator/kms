package configstore

import (
	"errors"
	"flag"
	"fmt"
	"strconv"
	"strings"
)

// A nil schema version selects the generated artifact's digest, whereas zero
// explicitly selects the schema-free track.
type managedScopeFlags struct {
	namespace     string
	schemaVersion *uint64
}

func addManagedVersionFlag(set *flag.FlagSet, name, usage string, value **uint64) {
	set.Func(name, usage, func(raw string) error {
		n, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			return fmt.Errorf("--%s requires an unsigned schema/version number", name)
		}
		*value = &n
		return nil
	})
}

func addManagedScopeFlags(set *flag.FlagSet, scope *managedScopeFlags) {
	set.StringVar(&scope.namespace, "namespace", "", "override the environment namespace (ENV/APP); application must match")
	addManagedVersionFlag(set, "schema-version", "select an explicit schema track instead of the generated digest", &scope.schemaVersion)
}

func resolveManagedNamespace(defaultNamespace, override string) (string, error) {
	env, app, ok := strings.Cut(defaultNamespace, "/")
	if !ok || !validManagedApplication(env) || !validManagedApplication(app) {
		return "", errors.New("resolver returned an invalid namespace")
	}
	if override == "" {
		return defaultNamespace, nil
	}
	targetEnv, targetApp, ok := strings.Cut(override, "/")
	if !ok || !validManagedApplication(targetEnv) || !validManagedApplication(targetApp) {
		return "", errors.New("--namespace must be a canonical ENV/APP")
	}
	if targetApp != app {
		return "", errors.New("--namespace must name the same application as the profile namespace")
	}
	return override, nil
}
