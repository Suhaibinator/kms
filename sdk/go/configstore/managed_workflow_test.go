package configstore

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/Suhaibinator/kms/sdk/go/kmsclient"
)

type driftCommandClient struct {
	fakeDefaultsApplyClient
	request  kmsclient.VerifyReleaseDefaultsOptions
	response kmsclient.VerifyReleaseDefaultsResult
}

func (c *driftCommandClient) VerifyReleaseDefaults(_ context.Context, in kmsclient.VerifyReleaseDefaultsOptions) (kmsclient.VerifyReleaseDefaultsResult, error) {
	c.request = in
	return c.response, nil
}

func TestManagedDriftUsesEmbeddedHashesAndExplicitTrack(t *testing.T) {
	for _, verdict := range []string{kmsclient.VerifyVerdictMatch, kmsclient.VerifyVerdictDiffers} {
		t.Run(verdict, func(t *testing.T) {
			c := &driftCommandClient{response: kmsclient.VerifyReleaseDefaultsResult{ReleaseName: "runtime", ReleaseVersion: 1, SchemaVersion: 5, SchemaMatches: true, Entries: []kmsclient.VerifyDefaultsVerdict{{Alias: "runtime", Verdict: verdict}}}}
			var out, errOut bytes.Buffer
			code := runManagedConfigCommand([]string{"defaults", "drift", "--profile", "dev", "--namespace", "prod-linkie/gradethis", "--schema-version", "5", "--insecure", "--output", "json"}, &out, &errOut, managedCommandTestConfig(), func(kmsclient.Config) (managedConfigClient, error) { return c, nil })
			want := 0
			if verdict != kmsclient.VerifyVerdictMatch {
				want = 1
			}
			if code != want {
				t.Fatalf("code=%d stderr=%s", code, &errOut)
			}
			if c.request.Namespace != "prod-linkie/gradethis" || c.request.SchemaVersion == nil || *c.request.SchemaVersion != 5 || c.request.SchemaSHA256 != "" {
				t.Fatalf("request=%+v", c.request)
			}
			if len(c.request.Entries) != 1 || len(c.request.Entries[0].SHA256) != 64 {
				t.Fatalf("expected parameter-only hashes: %+v", c.request.Entries)
			}
			if len(c.calls)+len(c.releaseCalls)+len(c.schemaCalls) != 0 {
				t.Fatal("drift mutated server")
			}
			if !c.closed || !strings.Contains(out.String(), `"verdict":"`+verdict+`"`) {
				t.Fatalf("output=%s closed=%t", &out, c.closed)
			}
		})
	}
}

func TestManagedDefaultsExplicitSchemaAndNamespace(t *testing.T) {
	c := &fakeDefaultsApplyClient{results: []kmsclient.ApplicationDefaultsApplyResult{{Profile: "dev", PlanDigest: "p"}, {Profile: "dev", PlanDigest: "p", Executed: true}}}
	var out, errOut bytes.Buffer
	code := runManagedConfigCommand([]string{"defaults", "apply", "--profile", "dev", "--namespace", "prod-linkie/gradethis", "--schema-version", "7", "--overwrite", "--execute", "--confirm-production", "prod-linkie"}, &out, &errOut, managedCommandTestConfig(), func(cfg kmsclient.Config) (managedConfigClient, error) {
		if cfg.Namespace != "prod-linkie/gradethis" || cfg.Insecure {
			t.Fatalf("config namespace/TLS=%s/%t", cfg.Namespace, cfg.Insecure)
		}
		return c, nil
	})
	if code != 0 || len(c.calls) != 2 {
		t.Fatalf("code=%d err=%s calls=%d", code, &errOut, len(c.calls))
	}
	for _, call := range c.calls {
		if call.Namespace != "prod-linkie/gradethis" || call.SchemaVersion == nil || *call.SchemaVersion != 7 || call.UpdateDefinition {
			t.Fatalf("call=%+v", call)
		}
	}
}

func TestManagedScopeErrorsDoNotConnect(t *testing.T) {
	for _, args := range [][]string{
		{"defaults", "apply", "--profile", "dev", "--schema-version", "7", "--update-definition"},
		{"defaults", "apply", "--profile", "dev", "--namespace", "prod-linkie/other"},
		{"defaults", "apply", "--profile", "dev", "--namespace", "prod-linkie/gradethis", "--execute"},
		{"defaults", "apply", "--profile", "dev", "--namespace", "prod-linkie/gradethis", "--execute", "--confirm-production", "prod"},
		{"defaults", "drift", "--profile", "dev", "--schema-version", "-1"},
		{"release", "create", "--profile", "dev", "--namespace", "prod-linkie/other"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var out, errOut bytes.Buffer
			code := runManagedConfigCommand(args, &out, &errOut, managedCommandTestConfig(), func(kmsclient.Config) (managedConfigClient, error) { t.Fatal("unexpected connection"); return nil, nil })
			if code != 2 {
				t.Fatalf("code=%d err=%s", code, &errOut)
			}
		})
	}
}

func TestManagedReleaseForwardsSourceAndTargetSchema(t *testing.T) {
	c := &fakeDefaultsApplyClient{releaseResults: []kmsclient.CreateApplicationReleaseResult{{Profile: "dev", PlanDigest: strings.Repeat("a", 64), Valid: true, ReleaseName: "runtime", SchemaVersion: 7}}}
	var out, errOut bytes.Buffer
	code := runManagedConfigCommand([]string{"release", "create", "--profile", "dev", "--namespace", "prod-linkie/gradethis", "--schema-version", "7", "--from-schema", "5"}, &out, &errOut, managedCommandTestConfig(), func(kmsclient.Config) (managedConfigClient, error) { return c, nil })
	if code != 0 || len(c.releaseCalls) != 1 {
		t.Fatalf("code=%d stderr=%s", code, &errOut)
	}
	call := c.releaseCalls[0]
	if call.Namespace != "prod-linkie/gradethis" || call.SchemaVersion == nil || *call.SchemaVersion != 7 || call.SourceSchemaVersion == nil || *call.SourceSchemaVersion != 5 || call.Execute {
		t.Fatalf("call=%+v", call)
	}
}
