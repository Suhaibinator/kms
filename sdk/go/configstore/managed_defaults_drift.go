package configstore

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"flag"
	"fmt"
	"io"
)

// Drift is a read-only comparison with the selected track's active pins, not
// with mutable current parameter labels. Only hashes are sent to KMS.
func runManagedDefaultsDrift[P ~string, T any](args []string, stdout, stderr io.Writer, config ManagedConfigCommandConfig[P, T], newClient managedConfigClientFactory) int {
	if stderr == nil {
		return 1
	}
	var scope managedScopeFlags
	var connection managedConnectionFlags
	set := flag.NewFlagSet("managed-config defaults drift", flag.ContinueOnError)
	set.SetOutput(stderr)
	profile := set.String("profile", "", "application defaults profile (required)")
	release := set.String("release", "", "release name (default: application's release)")
	output := set.String("output", "table", "table or json")
	addManagedScopeFlags(set, &scope)
	addManagedConnectionFlags(set, &connection)
	set.Usage = func() {
		if stdout != nil {
			fmt.Fprintln(stdout, "Usage: managed-config defaults drift --profile PROFILE [--namespace ENV/APP] [--schema-version N] [--release NAME] [--output table|json]\nCompares the selected active release to the defaults compiled into this executable.\nExit codes: 0 match, 1 drift or failure, 2 usage. No values or secrets are read.\nConnection flags: --endpoint, --insecure, --ca, --cert, --key; identity token: KMS_TOKEN.")
		}
	}
	if err := set.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if set.NArg() != 0 || !canonicalDefaultsText(*profile, false) || (*output != "table" && *output != "json") {
		writeManagedConfigError(stderr, errors.New("--profile is required, --output must be table or json, and positional arguments are not supported"))
		return 2
	}
	if stdout == nil || !validManagedApplication(config.Application) || config.Defaults.Provider == nil || config.Defaults.Encoder == nil || config.Defaults.Namespace == nil || newClient == nil {
		writeManagedConfigError(stderr, errors.New("runner configuration is incomplete"))
		return 1
	}
	root, err := config.Defaults.Provider(P(*profile))
	if err != nil || root == nil {
		writeManagedConfigError(stderr, errors.New("load defaults: provider failed"))
		return 1
	}
	raw, err := config.Defaults.Encoder(*profile, root)
	if err != nil {
		writeManagedConfigError(stderr, errors.New("encode artifact: encoder failed"))
		return 1
	}
	artifact, err := ParseDefaultsArtifact(raw)
	if err != nil || artifact.Profile != *profile {
		writeManagedConfigError(stderr, errors.New("encoded artifact is invalid"))
		return 1
	}
	ns, err := config.Defaults.Namespace(P(*profile))
	if err != nil {
		writeManagedConfigError(stderr, errors.New("namespace resolver failed"))
		return 1
	}
	ns, err = resolveManagedNamespace(ns, scope.namespace)
	if err != nil {
		writeManagedConfigError(stderr, err)
		return 2
	}
	if len(ns) <= len(config.Application) || ns[len(ns)-len(config.Application)-1:] != "/"+config.Application {
		writeManagedConfigError(stderr, errors.New("namespace must name the configured application"))
		return 2
	}
	resolveManagedConnectionFlags(set, &connection)
	clientConfig, err := defaultsApplierClientConfig(defaultsApplierFlags{managedConnectionFlags: connection}, ns)
	if err != nil {
		writeManagedConfigError(stderr, err)
		return 2
	}
	client, err := newClient(clientConfig)
	if err != nil {
		writeManagedConfigError(stderr, err)
		return 1
	}
	defer client.Close()
	verifier, ok := client.(VerifyClient)
	if !ok {
		writeManagedConfigError(stderr, errors.New("client does not support defaults verification"))
		return 1
	}
	groups := make(map[string]jsontext.Value, len(artifact.Parameters))
	for _, p := range artifact.Parameters {
		groups[p.Alias] = jsontext.Value(p.Value)
	}
	digest := artifact.SchemaSHA256
	if scope.schemaVersion != nil {
		digest = ""
	}
	result, err := VerifyDefaults(context.Background(), verifier, VerifyInput{SchemaSHA256: digest, Contract: artifact.Contract, Groups: groups}, VerifyOptions{Namespace: ns, Release: *release, Profile: *profile, SchemaVersion: scope.schemaVersion})
	if err != nil {
		writeManagedConfigError(stderr, err)
		return 1
	}
	clean := result.Passed() && result.Unverified == 0
	if *output == "json" {
		entries := make([]map[string]string, 0, len(result.Entries))
		for _, e := range result.Entries {
			entries = append(entries, map[string]string{"alias": e.Alias, "content_type": e.ContentType, "verdict": e.Verdict})
		}
		data, err := json.Marshal(map[string]any{"namespace": ns, "release": result.ReleaseName, "version": result.ReleaseVersion, "schema_version": result.SchemaVersion, "activation_revision": result.ActivationRevision, "schema_matches": result.SchemaMatches, "clean": clean, "unverified": result.Unverified, "entries": entries}, json.Deterministic(true))
		if err != nil {
			writeManagedConfigError(stderr, err)
			return 1
		}
		if _, err = fmt.Fprintln(stdout, string(data)); err != nil {
			return 1
		}
	} else if _, err = io.WriteString(stdout, result.Report()); err != nil {
		return 1
	}
	if !clean {
		return 1
	}
	return 0
}
