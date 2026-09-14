package configstore

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/Suhaibinator/kms/sdk/go/kmsclient"
)

type managedReleaseLifecycleClient interface {
	ValidateManagedRelease(context.Context, kmsclient.ManagedReleaseTarget) (kmsclient.ManagedReleaseValidation, error)
	GetManagedReleaseActivation(context.Context, kmsclient.ManagedReleaseTarget) (kmsclient.ManagedReleaseActivation, error)
	ActivateManagedRelease(context.Context, kmsclient.ManagedReleaseTarget, uint64) (kmsclient.ManagedReleaseActivation, error)
}

func runManagedReleaseLifecycle[P ~string, T any](args []string, stdout, stderr io.Writer, config ManagedConfigCommandConfig[P, T], newClient managedConfigClientFactory, activate bool) int {
	var connection managedConnectionFlags
	var scope managedScopeFlags
	var profile, name, confirmation string
	var version uint64
	var execute bool
	command := "validate"
	if activate {
		command = "activate"
	}
	set := flag.NewFlagSet("managed-config release "+command, flag.ContinueOnError)
	set.SetOutput(stderr)
	set.StringVar(&profile, "profile", "", "application defaults profile")
	set.StringVar(&name, "release", "runtime", "release name")
	set.Uint64Var(&version, "version", 0, "immutable release version (required)")
	if activate {
		set.BoolVar(&execute, "execute", false, "activate after validation and a fresh preview")
		set.StringVar(&confirmation, "confirm-production", "", "production environment name confirmation")
	}
	addManagedConnectionFlags(set, &connection)
	addManagedScopeFlags(set, &scope)
	set.Usage = func() {
		if stdout != nil {
			fmt.Fprintf(stdout, "Usage: managed-config release %s --profile PROFILE --schema-version VERSION --version VERSION [flags]\n", command)
			old := set.Output()
			set.SetOutput(stdout)
			set.PrintDefaults()
			set.SetOutput(old)
		}
	}
	if err := set.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		writeManagedConfigError(stderr, err)
		return 2
	}
	if set.NArg() != 0 || !canonicalDefaultsText(profile, false) || !canonicalDefaultsText(name, false) || version == 0 || scope.schemaVersion == nil {
		writeManagedConfigError(stderr, errors.New("--profile, --schema-version, and positive --version are required; positional arguments are not supported"))
		return 2
	}
	if stdout == nil || config.Defaults.Namespace == nil || newClient == nil || !validManagedApplication(config.Application) {
		writeManagedConfigError(stderr, errors.New("runner configuration is incomplete"))
		return 1
	}
	namespace, err := config.Defaults.Namespace(P(profile))
	if err != nil {
		writeManagedConfigError(stderr, errors.New("resolve namespace: resolver failed"))
		return 1
	}
	namespace, err = resolveManagedNamespace(namespace, scope.namespace)
	if err != nil {
		writeManagedConfigError(stderr, err)
		return 2
	}
	env, app, ok := strings.Cut(namespace, "/")
	if !ok || app != config.Application {
		writeManagedConfigError(stderr, errors.New("namespace is for another application"))
		return 2
	}
	if err := validateDefaultsProductionConfirmation(env, execute, confirmation); err != nil {
		writeManagedConfigError(stderr, err)
		return 2
	}
	resolveManagedConnectionFlags(set, &connection)
	clientConfig, err := defaultsApplierClientConfig(defaultsApplierFlags{endpoint: connection.endpoint, insecure: connection.insecure, ca: connection.ca, cert: connection.cert, key: connection.key}, namespace)
	if err != nil {
		writeManagedConfigError(stderr, err)
		return 2
	}
	base, err := newClient(clientConfig)
	if err != nil {
		writeManagedConfigError(stderr, fmt.Errorf("connect: %w", err))
		return 1
	}
	defer func() { _ = base.Close() }()
	client, ok := base.(managedReleaseLifecycleClient)
	if !ok {
		writeManagedConfigError(stderr, errors.New("client does not support release lifecycle commands"))
		return 1
	}
	target := kmsclient.ManagedReleaseTarget{Namespace: namespace, Name: name, SchemaVersion: *scope.schemaVersion, Version: version}
	ctx := context.Background()
	// Snapshot first so concurrent activation during validation invalidates execution.
	var current kmsclient.ManagedReleaseActivation
	if activate {
		current, err = client.GetManagedReleaseActivation(ctx, target)
		if errors.Is(err, kmsclient.ErrNotFound) {
			current = kmsclient.ManagedReleaseActivation{}
		}
		if err != nil && !errors.Is(err, kmsclient.ErrNotFound) {
			writeManagedConfigError(stderr, fmt.Errorf("read active release: %w", err))
			return 1
		}
	}
	validation, err := client.ValidateManagedRelease(ctx, target)
	if err != nil {
		writeManagedConfigError(stderr, fmt.Errorf("validate release: %w", err))
		return 1
	}
	if validation.Valid != (len(validation.Errors) == 0) {
		writeManagedConfigError(stderr, errors.New("invalid release validation response"))
		return 1
	}
	if _, err := fmt.Fprintf(stdout, "Release: %s %s@%d:%d\nValid: %t\n", namespace, name, *scope.schemaVersion, version, validation.Valid); err != nil {
		writeManagedConfigError(stderr, err)
		return 1
	}
	for _, item := range validation.Errors {
		if _, err := fmt.Fprintf(stdout, "Validation: alias=%s code=%s schema_pointer=%s\n", item.Alias, item.Code, item.SchemaPointer); err != nil {
			writeManagedConfigError(stderr, err)
			return 1
		}
	}
	if !validation.Valid {
		return 1
	}
	if !activate {
		return 0
	}
	if _, err := fmt.Fprintf(stdout, "Current version: %d\nActivation revision: %d\nExecute: %t\n", current.Version, current.ActivationRevision, execute); err != nil {
		writeManagedConfigError(stderr, err)
		return 1
	}
	if !execute {
		return 0
	}
	result, err := client.ActivateManagedRelease(ctx, target, current.Version)
	if err != nil {
		writeManagedConfigError(stderr, fmt.Errorf("activate release: %w", err))
		return 1
	}
	if result.Version != version || result.ActivationRevision == 0 {
		writeManagedConfigError(stderr, errors.New("invalid release activation response"))
		return 1
	}
	if _, err := fmt.Fprintf(stdout, "Activated version: %d\nActivation revision: %d\nChanged: %t\n", result.Version, result.ActivationRevision, result.Changed); err != nil {
		writeManagedConfigError(stderr, err)
		return 1
	}
	return 0
}
