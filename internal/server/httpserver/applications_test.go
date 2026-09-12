package httpserver

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Suhaibinator/kms/internal/core"
	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/storage"
)

const consoleSchemaJSON = `{"type":"object","properties":{"database":{"type":"object"},"rate_limits":{"type":"integer","minimum":0}},"required":["database","rate_limits"],"additionalProperties":false}`

func consoleAdmin() core.Principal {
	return core.Principal{Identity: domain.Identity{Name: "admin", Kind: domain.IdentityKindAdmin}, Method: domain.AuthMethodToken}
}

// seedConsoleApp registers the runtime schema and application gradethis
// (contract database/json, rate_limits/integer, db_password/secret) and fills
// each environment with one version of every resource.
func (e *testEnv) seedConsoleApp(envs ...string) {
	e.t.Helper()
	w := e.admin(http.MethodPost, "/api/v1/applications", map[string]any{
		"name": "gradethis", "description": "Grading API", "release_name": "runtime",
		"schema": map[string]any{"schema_json": consoleSchemaJSON},
		"contract": []map[string]any{
			{"alias": "database", "kind": "parameter", "content_type": "json"},
			{"alias": "rate_limits", "kind": "parameter", "content_type": "integer"},
			{"alias": "db_password", "kind": "secret"},
		},
	})
	mustStatus(e.t, w, http.StatusCreated)
	for _, env := range envs {
		e.seedConsoleEnv(env)
	}
}

func (e *testEnv) seedConsoleEnv(env string) {
	e.t.Helper()
	e.createNS(env, "gradethis", "token")
	e.putParam(env, "database", `{"host":"db.internal","pool":8}`, "json")
	e.putParam(env, "rate_limits", "5", "integer")
	e.putSecret(env, "db_password", "hunter2")
}

func (e *testEnv) putParam(env, key, value, contentType string) {
	e.t.Helper()
	w := e.admin(http.MethodPut, "/api/v1/parameters", map[string]any{"env": env, "app": "gradethis", "key": key, "value": value, "content_type": contentType, "metadata_json": "{}"})
	mustStatus(e.t, w, http.StatusOK)
}

func (e *testEnv) putSecret(env, key, value string) {
	e.t.Helper()
	w := e.admin(http.MethodPost, "/api/v1/secrets", map[string]any{"env": env, "app": "gradethis", "key": key, "value_base64": base64.StdEncoding.EncodeToString([]byte(value)), "content_type": "text/plain", "metadata_json": "{}"})
	mustStatus(e.t, w, http.StatusOK)
}

// ship executes (or previews) one value change through the ship endpoint.
func (e *testEnv) ship(env, alias, value string, dryRun bool) map[string]any {
	e.t.Helper()
	w := e.admin(http.MethodPost, "/api/v1/applications/ship", map[string]any{
		"schema_version": 1, "application": "gradethis", "environment": env, "dry_run": dryRun,
		"changes": []map[string]any{{"alias": alias, "value": value}},
	})
	mustStatus(e.t, w, http.StatusOK)
	return decodeBody(e.t, w)
}

// ackInstance registers a connected subscriber instance and records one
// lifecycle acknowledgement for the active release in env.
func (e *testEnv) ackInstance(env, instance, state, category string) {
	e.t.Helper()
	ctx := context.Background()
	ns := domain.NamespaceRef{Env: env, App: "gradethis"}
	pr := consoleAdmin()
	track := domain.ReleaseTrack{Namespace: ns, Name: "runtime", SchemaVersion: 1}
	active, err := e.svc.GetActiveConfigurationRelease(ctx, pr, track)
	if err != nil {
		e.t.Fatalf("active release for ack: %v", err)
	}
	conn := "conn-" + instance
	if err := e.svc.SetReleaseSubscriberConnected(ctx, track, "api", instance, pr.Identity.Name, conn, true); err != nil {
		e.t.Fatal(err)
	}
	if err := e.svc.AcknowledgeConfigurationRelease(ctx, pr, domain.ReleaseAcknowledgement{
		Namespace: ns, ReleaseName: "runtime", SchemaVersion: 1, ReleaseVersion: active.Release.Version, ActivationRevision: active.ActivationRevision,
		ClientName: "api", InstanceID: instance, ConnectionID: conn, State: state, RejectionCategory: category,
	}); err != nil {
		e.t.Fatal(err)
	}
}

func envOverview(t *testing.T, body map[string]any, env string) map[string]any {
	t.Helper()
	for _, item := range body["environments"].([]any) {
		m := item.(map[string]any)
		if m["namespace"].(map[string]any)["env"] == env {
			return m
		}
	}
	t.Fatalf("overview has no environment %s: %v", env, body["environments"])
	return nil
}

func findingCodesOf(v any) []string {
	out := []string{}
	for _, f := range v.([]any) {
		out = append(out, f.(map[string]any)["code"].(string))
	}
	return out
}

func TestGetApplicationHTTP(t *testing.T) {
	e := newReleaseTestEnv(t)
	e.seedConsoleApp()
	w := e.admin(http.MethodGet, "/api/v1/applications/get?name=gradethis", nil)
	mustStatus(t, w, http.StatusOK)
	if app := decodeBody(t, w)["application"].(map[string]any); app["name"] != "gradethis" || len(app["contract"].([]any)) != 3 {
		t.Fatalf("application = %v", app)
	}
	w = e.admin(http.MethodGet, "/api/v1/applications/get?name=nope", nil)
	mustStatus(t, w, http.StatusNotFound)
}

func TestApplicationOverviewRecoversAfterRestartHTTP(t *testing.T) {
	e := newReleaseTestEnv(t)
	e.seedConsoleApp("prod")
	e.ship("prod", "rate_limits", "7", false)
	e.ackInstance("prod", "replica", domain.ReleaseStateRejected, "restart_required")
	read := func() map[string]any {
		w := e.admin(http.MethodGet, "/api/v1/applications/overview?name=gradethis", nil)
		mustStatus(t, w, http.StatusOK)
		return envOverview(t, decodeBody(t, w), "prod")
	}
	if before := read(); before["status"] != "degraded" {
		t.Fatalf("before restart = %v", before)
	}
	ctx := context.Background()
	track := domain.ReleaseTrack{Namespace: domain.NamespaceRef{Env: "prod", App: "gradethis"}, Name: "runtime", SchemaVersion: 1}
	pr := consoleAdmin()
	if err := e.svc.SetReleaseSubscriberConnected(ctx, track, "api", "replica", pr.Identity.Name, "conn-replica", false); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.SetReleaseSubscriberConnected(ctx, track, "api", "replica", pr.Identity.Name, "restarted-replica", true); err != nil {
		t.Fatal(err)
	}
	active, err := e.svc.GetActiveConfigurationRelease(ctx, pr, track)
	if err != nil {
		t.Fatal(err)
	}
	// Cross the timestamp precision boundary before recording recovery.
	time.Sleep(2 * time.Millisecond)
	if err := e.svc.AcknowledgeConfigurationRelease(ctx, pr, domain.ReleaseAcknowledgement{
		Namespace: track.Namespace, ReleaseName: track.Name, SchemaVersion: track.SchemaVersion,
		ReleaseVersion: active.Release.Version, ActivationRevision: active.ActivationRevision,
		ClientName: "api", InstanceID: "replica", ConnectionID: "restarted-replica", State: domain.ReleaseStateApplied,
	}); err != nil {
		t.Fatal(err)
	}
	after := read()
	rollout := after["rollout"].(map[string]any)
	if after["status"] != "ready" || rollout["connected"] != float64(1) || rollout["applied_current"] != float64(1) || rollout["rejected"] != float64(0) || len(rollout["rejected_instances"].([]any)) != 0 {
		t.Fatalf("after restart = %v", after)
	}
	for _, code := range findingCodesOf(after["findings"]) {
		if code == "instance_rejected" {
			t.Fatalf("obsolete rejection finding: %v", after)
		}
	}
}

func TestApplicationArchiveHTTP(t *testing.T) {
	e := newReleaseTestEnv(t)
	w := e.admin(http.MethodPost, "/api/v1/applications", map[string]any{
		"name": "payments", "release_name": "runtime",
		"schema": map[string]any{"schema_json": `{"type":"object"}`},
	})
	mustStatus(t, w, http.StatusCreated)
	body := decodeBody(t, w)
	if body["application"].(map[string]any)["schema_version"].(float64) != 1 || body["schema"].(map[string]any)["application"] != "payments" {
		t.Fatalf("atomic create response = %v", body)
	}
	w = e.admin(http.MethodPost, "/api/v1/applications/archive", map[string]any{"name": "payments"})
	mustStatus(t, w, http.StatusOK)
	archived := decodeBody(t, w)["application"].(map[string]any)
	if archived["archived_at_unix_ms"].(float64) == 0 || archived["archived_by"] != "admin" {
		t.Fatalf("archived application = %v", archived)
	}
	w = e.admin(http.MethodGet, "/api/v1/applications", nil)
	mustStatus(t, w, http.StatusOK)
	if applications := decodeBody(t, w)["applications"].([]any); len(applications) != 0 {
		t.Fatalf("default list includes archived application: %v", applications)
	}
	w = e.admin(http.MethodGet, "/api/v1/applications?archived=only", nil)
	mustStatus(t, w, http.StatusOK)
	if applications := decodeBody(t, w)["applications"].([]any); len(applications) != 1 || applications[0].(map[string]any)["name"] != "payments" {
		t.Fatalf("archived list = %v", applications)
	}
	w = e.admin(http.MethodPost, "/api/v1/configuration-schemas", map[string]any{"application": "payments", "schema_json": `{"type":"string"}`})
	mustStatus(t, w, http.StatusPreconditionFailed)
	w = e.admin(http.MethodPost, "/api/v1/namespaces", map[string]any{"env": "prod", "app": "payments"})
	mustStatus(t, w, http.StatusPreconditionFailed)
	w = e.admin(http.MethodPost, "/api/v1/applications/unarchive", map[string]any{"name": "payments"})
	mustStatus(t, w, http.StatusOK)
	w = e.admin(http.MethodPost, "/api/v1/namespaces", map[string]any{"env": "prod", "app": "payments"})
	mustStatus(t, w, http.StatusOK)
	w = e.admin(http.MethodPost, "/api/v1/applications/archive", map[string]any{"name": "payments"})
	mustStatus(t, w, http.StatusPreconditionFailed)
}

func TestApplicationOverviewHTTP(t *testing.T) {
	e := newReleaseTestEnv(t)
	e.seedConsoleApp("dev", "prod")

	w := e.admin(http.MethodGet, "/api/v1/applications/overview?name=gradethis", nil)
	mustStatus(t, w, http.StatusOK)
	body := decodeBody(t, w)
	if body["status"] != "setup" || len(body["environments"].([]any)) != 2 || len(body["rows"].([]any)) != 3 || body["schema_json"] == "" {
		t.Fatalf("initial overview = %v", body)
	}
	dev := envOverview(t, body, "dev")
	if dev["status"] != "unreleased" || dev["values_state"] != "complete" || dev["release_state"] != "none" || dev["rollout_state"] != "no_subscribers" || dev["production"] != false {
		t.Fatalf("dev = %v", dev)
	}
	if prod := envOverview(t, body, "prod"); prod["production"] != true {
		t.Fatalf("prod = %v", prod)
	}
	values := dev["values"].([]any)
	if len(values) != 3 {
		t.Fatalf("values = %v", values)
	}
	for _, v := range values {
		m := v.(map[string]any)
		if m["present"] != true || m["key"] != m["alias"] || m["current_version"].(float64) != 1 {
			t.Fatalf("value = %v", m)
		}
		if m["pinned_version"].(float64) != 0 {
			t.Fatalf("no active release but pinned_version nonzero: %v", m)
		}
	}
	if _, ok := dev["release"].(map[string]any)["active"]; ok {
		t.Fatalf("no active release expected: %v", dev["release"])
	}
	if codes := findingCodesOf(dev["findings"]); strings.Join(codes, ",") != "no_active_release" {
		t.Fatalf("dev findings = %v", codes)
	}
	appCodes := findingCodesOf(body["findings"])
	if strings.Join(appCodes, ",") != "insecure_listener" {
		t.Fatalf("app findings = %v", appCodes)
	}

	// Ship dev, then it is ready and prod keeps the app in attention.
	shipped := e.ship("dev", "rate_limits", "7", false)
	if shipped["status"] != "activated" {
		t.Fatalf("ship = %v", shipped)
	}
	e.ackInstance("dev", "i1", domain.ReleaseStateApplied, "")
	w = e.admin(http.MethodGet, "/api/v1/applications/overview?name=gradethis", nil)
	mustStatus(t, w, http.StatusOK)
	body = decodeBody(t, w)
	if body["status"] != "attention" {
		t.Fatalf("status after dev ship = %v", body["status"])
	}
	dev = envOverview(t, body, "dev")
	if dev["status"] != "ready" || dev["release_state"] != "active" || dev["rollout_state"] != "applied" {
		t.Fatalf("dev after ship = %v", dev)
	}
	active := dev["release"].(map[string]any)["active"].(map[string]any)
	if active["version"].(float64) != 1 || active["is_rolled_back"] != false || active["activation_revision"].(float64) == 0 || len(active["entries"].([]any)) != 3 {
		t.Fatalf("active = %v", active)
	}
	if rel := dev["release"].(map[string]any); rel["latest_version"].(float64) != 1 || rel["release_count"].(float64) != 1 {
		t.Fatalf("release = %v", rel)
	}
	rollout := dev["rollout"].(map[string]any)
	if rollout["total"].(float64) != 1 || rollout["applied_current"].(float64) != 1 || len(rollout["other_release_names"].([]any)) != 0 || len(rollout["rejected_instances"].([]any)) != 0 {
		t.Fatalf("rollout = %v", rollout)
	}
	for _, v := range dev["values"].([]any) {
		m := v.(map[string]any)
		if m["alias"] == "rate_limits" && (m["pinned_version"].(float64) != 2 || m["current_version"].(float64) != 2) {
			t.Fatalf("rate_limits value = %v", m)
		}
	}

	// A newer parameter version is drift with value-free params.
	e.putParam("dev", "rate_limits", "9", "integer")
	w = e.admin(http.MethodGet, "/api/v1/applications/overview?name=gradethis&env=dev", nil)
	mustStatus(t, w, http.StatusOK)
	body = decodeBody(t, w)
	if len(body["environments"].([]any)) != 1 {
		t.Fatalf("env filter returned %d environments", len(body["environments"].([]any)))
	}
	dev = envOverview(t, body, "dev")
	if dev["status"] != "drift" {
		t.Fatalf("dev drift = %v", dev["status"])
	}
	var drift map[string]any
	for _, f := range dev["findings"].([]any) {
		if f.(map[string]any)["code"] == "unreleased_changes" {
			drift = f.(map[string]any)
		}
	}
	if drift == nil || drift["scope"].(map[string]any)["alias"] != "rate_limits" || drift["params"].(map[string]any)["current"].(float64) != 3 || drift["params"].(map[string]any)["pinned"].(float64) != 2 {
		t.Fatalf("unreleased_changes = %v", drift)
	}
	if strings.Contains(w.Body.String(), "db.internal") && !strings.Contains(w.Body.String(), `"rows"`) {
		t.Fatal("overview leaked a value outside rows")
	}

	// Fleet form.
	w = e.admin(http.MethodGet, "/api/v1/applications/overview", nil)
	mustStatus(t, w, http.StatusOK)
	fleet := decodeBody(t, w)["applications"].([]any)
	if len(fleet) != 1 {
		t.Fatalf("fleet = %v", fleet)
	}
	app := fleet[0].(map[string]any)
	if app["status"] != "attention" || app["application"].(map[string]any)["name"] != "gradethis" {
		t.Fatalf("fleet app = %v", app)
	}
	statuses := map[string]string{}
	for _, env := range app["environments"].([]any) {
		m := env.(map[string]any)
		statuses[m["env"].(string)] = m["status"].(string)
		if m["env"] == "prod" && m["production"] != true {
			t.Fatalf("fleet prod flag = %v", m)
		}
	}
	if statuses["dev"] != "drift" || statuses["prod"] != "unreleased" {
		t.Fatalf("fleet statuses = %v", statuses)
	}
	if _, ok := app["environments"].([]any)[0].(map[string]any)["values"]; ok {
		t.Fatal("fleet form must not carry values")
	}

	// Multi-environment selection: repeated and comma-joined, order preserved.
	for _, query := range []string{"env=prod&env=dev", "env=prod,dev", "env=prod,,dev&env=prod"} {
		w = e.admin(http.MethodGet, "/api/v1/applications/overview?name=gradethis&"+query, nil)
		mustStatus(t, w, http.StatusOK)
		envs := decodeBody(t, w)["environments"].([]any)
		if len(envs) != 2 || envs[0].(map[string]any)["namespace"].(map[string]any)["env"] != "prod" || envs[1].(map[string]any)["namespace"].(map[string]any)["env"] != "dev" {
			t.Fatalf("%s selected %d environments: %v", query, len(envs), envs)
		}
	}
	w = e.admin(http.MethodGet, "/api/v1/applications/overview?name=gradethis&env=dev,nope", nil)
	mustStatus(t, w, http.StatusNotFound)
	w = e.admin(http.MethodGet, "/api/v1/applications/overview?name=gradethis&env=Bad%20Env", nil)
	mustStatus(t, w, http.StatusBadRequest)

	w = e.admin(http.MethodGet, "/api/v1/applications/overview?name=missing", nil)
	mustStatus(t, w, http.StatusNotFound)
	w = e.admin(http.MethodGet, "/api/v1/applications/overview?name=gradethis&env=nope", nil)
	mustStatus(t, w, http.StatusNotFound)
	w = e.do(http.MethodGet, "/api/v1/applications/overview?name=gradethis", nil, nil)
	mustStatus(t, w, http.StatusUnauthorized)
}

func TestShipApplicationHTTP(t *testing.T) {
	e := newReleaseTestEnv(t)
	e.seedConsoleApp("dev")

	preview := e.ship("dev", "rate_limits", "12", true)
	if preview["status"] != "preview" || preview["preview"].(map[string]any)["validation"].(map[string]any)["valid"] != true || len(preview["parameters"].([]any)) != 0 {
		t.Fatalf("preview = %v", preview)
	}
	if _, ok := preview["release"]; ok {
		t.Fatalf("preview must not carry a release: %v", preview)
	}
	entries := preview["preview"].(map[string]any)["entries"].([]any)
	if len(entries) != 3 {
		t.Fatalf("entries = %v", entries)
	}
	for _, item := range entries {
		m := item.(map[string]any)
		switch m["alias"] {
		case "rate_limits":
			if m["change"] != "edited" || m["to_version"].(float64) != 2 || m["key"] != "rate_limits" {
				t.Fatalf("edited entry = %v", m)
			}
			if m["from_version"].(float64) != 0 {
				t.Fatalf("first release has nonzero from_version: %v", m)
			}
		default:
			if m["change"] != "included" || m["to_version"].(float64) != 1 {
				t.Fatalf("included entry = %v", m)
			}
		}
	}
	w := e.admin(http.MethodGet, "/api/v1/parameters/get?env=dev&app=gradethis&key=rate_limits", nil)
	mustStatus(t, w, http.StatusOK)
	if decodeBody(t, w)["parameter"].(map[string]any)["version"].(float64) != 1 {
		t.Fatal("dry run wrote a parameter version")
	}

	invalid := e.ship("dev", "rate_limits", "-1", true)
	validation := invalid["preview"].(map[string]any)["validation"].(map[string]any)
	if validation["valid"] != false || len(validation["errors"].([]any)) != 1 {
		t.Fatalf("invalid preview = %v", validation)
	}

	rejected := e.ship("dev", "rate_limits", "-1", false)
	if rejected["status"] != "rejected" || rejected["error"].(map[string]any)["code"] != "failed_precondition" || len(rejected["error"].(map[string]any)["validation_errors"].([]any)) != 1 {
		t.Fatalf("rejected = %v", rejected)
	}

	shipped := e.ship("dev", "rate_limits", "12", false)
	if shipped["status"] != "activated" || shipped["release"].(map[string]any)["version"].(float64) != 1 || shipped["activation"].(map[string]any)["changed"] != true {
		t.Fatalf("shipped = %v", shipped)
	}
	params := shipped["parameters"].([]any)
	if len(params) != 1 || params[0].(map[string]any)["version"].(float64) != 2 || params[0].(map[string]any)["alias"] != "rate_limits" {
		t.Fatalf("parameters = %v", params)
	}

	w = e.admin(http.MethodPost, "/api/v1/applications/ship", map[string]any{
		"schema_version": 1, "application": "gradethis", "environment": "dev", "expected_active_version": 0,
		"changes": []map[string]any{{"alias": "rate_limits", "value": "3"}},
	})
	mustStatus(t, w, http.StatusConflict)
	if errCode(t, w) != "aborted" {
		t.Fatalf("stale expected_active_version code = %s", errCode(t, w))
	}
	w = e.admin(http.MethodPost, "/api/v1/applications/ship", map[string]any{
		"schema_version": 1, "application": "gradethis", "environment": "dev", "changes": []map[string]any{{"alias": "unknown", "value": "3"}},
	})
	mustStatus(t, w, http.StatusBadRequest)
	w = e.admin(http.MethodPost, "/api/v1/applications/ship", map[string]any{
		"schema_version": 1, "application": "gradethis", "environment": "dev", "changes": []map[string]any{{"alias": "db_password", "value": "leak"}},
	})
	mustStatus(t, w, http.StatusBadRequest)
	// Pin-only changes (secrets included) are accepted.
	w = e.admin(http.MethodPost, "/api/v1/applications/ship", map[string]any{
		"schema_version": 1, "application": "gradethis", "environment": "dev", "dry_run": true, "changes": []map[string]any{{"alias": "db_password", "version": 1}},
	})
	mustStatus(t, w, http.StatusOK)
	if decodeBody(t, w)["status"] != "preview" {
		t.Fatalf("secret pin preview = %s", w.Body.String())
	}
}

func TestCloneEnvironmentHTTP(t *testing.T) {
	e := newReleaseTestEnv(t)
	e.seedConsoleApp("dev")
	w := e.admin(http.MethodPost, "/api/v1/applications/environments/clone", map[string]any{
		"schema_version": 1, "application": "gradethis", "source_env": "dev", "target_env": "prod", "copy_values": true, "description": "Production",
	})
	mustStatus(t, w, http.StatusOK)
	body := decodeBody(t, w)
	if body["namespace_created"] != true || body["namespace"].(map[string]any)["env"] != "prod" || body["namespace"].(map[string]any)["description"] != "Production" {
		t.Fatalf("clone = %v", body)
	}
	actions := map[string]string{}
	for _, item := range body["items"].([]any) {
		m := item.(map[string]any)
		actions[m["alias"].(string)] = m["action"].(string)
	}
	if actions["database"] != "copied" || actions["rate_limits"] != "copied" || actions["db_password"] != "needs_value" {
		t.Fatalf("actions = %v", actions)
	}
	if needs := body["needs_value"].([]any); len(needs) != 1 || needs[0] != "db_password" {
		t.Fatalf("needs_value = %v", needs)
	}
	w = e.admin(http.MethodPost, "/api/v1/applications/environments/clone", map[string]any{"schema_version": 1, "application": "gradethis", "source_env": "dev", "target_env": "dev"})
	mustStatus(t, w, http.StatusBadRequest)
}

func TestApplicationManagementHTTPRequiresExplicitSchemaVersion(t *testing.T) {
	e := newReleaseTestEnv(t)
	e.seedConsoleApp("dev")
	ctx := context.Background()
	pr := consoleAdmin()
	ns := domain.NamespaceRef{Env: "dev", App: "gradethis"}
	e.ship("dev", "rate_limits", "7", false)
	newer, err := e.svc.CreateConfigurationSchema(ctx, pr, ns.App, `{"type":"object","x-kms-contract":[{"alias":"extra","kind":"parameter","content_type":"string"}]}`, "{}")
	if err != nil {
		t.Fatal(err)
	}
	w := e.admin(http.MethodPost, "/api/v1/applications/ship", map[string]any{
		"application": ns.App, "environment": ns.Env, "schema_version": newer.Version,
		"changes": []map[string]any{{"alias": "extra", "value": "initial"}},
	})
	mustStatus(t, w, http.StatusOK)
	beforeRevision, err := e.svc.CurrentRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	beforeReleases, _, err := e.svc.ListConfigurationReleases(ctx, pr, domain.ReleaseFilter{Namespace: ns}, storage.ListPage{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	beforeActive := make(map[uint64]domain.ActiveConfigurationRelease)
	for _, schemaVersion := range []uint64{1, newer.Version} {
		active, err := e.svc.GetActiveConfigurationRelease(ctx, pr, domain.ReleaseTrack{Namespace: ns, Name: "runtime", SchemaVersion: schemaVersion})
		if err != nil {
			t.Fatal(err)
		}
		beforeActive[schemaVersion] = active
	}
	for _, selector := range []string{"omitted", "null"} {
		t.Run(selector, func(t *testing.T) {
			for _, endpoint := range []string{"ship", "environments/clone"} {
				body := map[string]any{"application": ns.App}
				if selector == "null" {
					body["schema_version"] = nil
				}
				if endpoint == "ship" {
					body["environment"] = ns.Env
					body["changes"] = []map[string]any{{"alias": "extra", "value": "must not write"}}
				} else {
					body["source_env"], body["target_env"], body["copy_values"] = ns.Env, "prod", true
				}
				w := e.admin(http.MethodPost, "/api/v1/applications/"+endpoint, body)
				mustStatus(t, w, http.StatusBadRequest)
				if errCode(t, w) != "invalid_argument" || !strings.Contains(w.Body.String(), "schema_version") {
					t.Fatalf("%s without selector: %s", endpoint, w.Body.String())
				}
			}
		})
	}
	if after, err := e.svc.CurrentRevision(ctx); err != nil || after != beforeRevision {
		t.Fatalf("missing/null selectors wrote revisions: before=%d after=%d err=%v", beforeRevision, after, err)
	}
	if after, _, err := e.svc.ListConfigurationReleases(ctx, pr, domain.ReleaseFilter{Namespace: ns}, storage.ListPage{Limit: 100}); err != nil || !reflect.DeepEqual(beforeReleases, after) {
		t.Fatalf("missing/null selectors changed releases: %+v %v", after, err)
	}
	if parameter, err := e.svc.GetParameter(ctx, pr, domain.Ref{NS: ns, Key: "extra"}, 0, domain.LabelCurrent); err != nil || parameter.Version != 1 || parameter.Value != "initial" {
		t.Fatalf("missing/null selectors changed resource: %+v %v", parameter, err)
	}
	w = e.admin(http.MethodGet, "/api/v1/namespaces/get?env=prod&app=gradethis", nil)
	mustStatus(t, w, http.StatusNotFound)
	for schemaVersion, before := range beforeActive {
		active, err := e.svc.GetActiveConfigurationRelease(ctx, pr, domain.ReleaseTrack{Namespace: ns, Name: "runtime", SchemaVersion: schemaVersion})
		if err != nil || !reflect.DeepEqual(before, active) {
			t.Fatalf("missing/null selectors changed activation for schema %d: %+v %v", schemaVersion, active, err)
		}
	}

	// Explicit older selectors are still passed through to each management flow.
	if shipped := e.ship("dev", "rate_limits", "8", false); shipped["release"].(map[string]any)["version"] != float64(2) {
		t.Fatalf("older schema ship: %v", shipped)
	}
	w = e.admin(http.MethodPost, "/api/v1/applications/environments/clone", map[string]any{
		"application": ns.App, "source_env": ns.Env, "target_env": "prod", "schema_version": 1, "copy_values": true,
	})
	mustStatus(t, w, http.StatusOK)
	if items := decodeBody(t, w)["items"].([]any); len(items) != 3 {
		t.Fatalf("older schema clone did not select its contract: %v", items)
	}
}

func TestApplicationManagementHTTPExplicitSchemaZero(t *testing.T) {
	e := newReleaseTestEnv(t)
	ctx := context.Background()
	pr := consoleAdmin()
	app, err := e.svc.CreateApplication(ctx, pr, domain.Application{Name: "legacy", ReleaseName: "runtime", Contract: []domain.ApplicationContractField{{Alias: "setting", Kind: domain.ReleaseEntryParameter, ContentType: "string"}}})
	if err != nil {
		t.Fatal(err)
	}
	ns := domain.NamespaceRef{Env: "dev", App: app.Name}
	if _, err := e.svc.CreateNamespace(ctx, pr, ns, "", []domain.AuthMethod{domain.AuthMethodToken}); err != nil {
		t.Fatal(err)
	}
	newer, err := e.svc.CreateConfigurationSchema(ctx, pr, app.Name, `{"type":"object","x-kms-contract":[]}`, "{}")
	if err != nil {
		t.Fatal(err)
	}
	w := e.admin(http.MethodPost, "/api/v1/applications/ship", map[string]any{
		"application": app.Name, "environment": ns.Env, "schema_version": 0,
		"changes": []map[string]any{{"alias": "setting", "value": "schema-free"}},
	})
	mustStatus(t, w, http.StatusOK)
	if body := decodeBody(t, w); body["status"] != "activated" || body["preview"].(map[string]any)["schema_version"] != float64(0) {
		t.Fatalf("schema-free ship: %v", body)
	}
	w = e.admin(http.MethodPost, "/api/v1/applications/environments/clone", map[string]any{
		"application": app.Name, "source_env": ns.Env, "target_env": "prod", "schema_version": 0, "copy_values": true,
	})
	mustStatus(t, w, http.StatusOK)
	if items := decodeBody(t, w)["items"].([]any); len(items) != 1 || items[0].(map[string]any)["alias"] != "setting" || items[0].(map[string]any)["action"] != "copied" {
		t.Fatalf("schema-free clone: %v", items)
	}
	if _, err := e.svc.GetActiveConfigurationRelease(ctx, pr, domain.ReleaseTrack{Namespace: ns, Name: app.ReleaseName, SchemaVersion: newer.Version}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("schema-free management activated newer schema: %v", err)
	}
}

func TestApplicationPatchPreservesOmittedContract(t *testing.T) {
	for _, schemaVersion := range []uint64{0, 1} {
		t.Run(fmt.Sprint(schemaVersion), func(t *testing.T) {
			e := newReleaseTestEnv(t)
			contract := []map[string]any{{"alias": "amount", "kind": "parameter", "content_type": "integer"}}
			create := map[string]any{"name": "payments", "release_name": "runtime", "contract": contract}
			if schemaVersion != 0 {
				create["schema"] = map[string]any{"schema_json": `{"type":"object"}`}
			}
			mustStatus(t, e.admin(http.MethodPost, "/api/v1/applications", create), http.StatusCreated)
			before, err := e.svc.GetApplication(context.Background(), consoleAdmin(), "payments")
			if err != nil {
				t.Fatal(err)
			}
			for _, explicitNull := range []bool{false, true} {
				patch := map[string]any{"name": "payments", "release_name": "runtime", "schema_version": schemaVersion, "description": "updated"}
				if explicitNull {
					patch["contract"] = nil
				}
				response := e.admin(http.MethodPatch, "/api/v1/applications", patch)
				mustStatus(t, response, http.StatusOK)
				updated, err := e.svc.GetApplication(context.Background(), consoleAdmin(), "payments")
				if err != nil {
					t.Fatal(err)
				}
				if updated.Description != "updated" || !reflect.DeepEqual(updated.Contract, before.Contract) {
					t.Fatalf("omitted contract changed definition (null=%t): %+v", explicitNull, updated)
				}
				if got := decodeBody(t, response)["application"].(map[string]any)["contract"].([]any); len(got) != 1 {
					t.Fatalf("PATCH response contract = %v", got)
				}
			}
			// Omission preserves the definition; explicitly clearing or replacing an
			// established contract still fails atomically, including descriptive edits.
			for _, replacement := range []any{[]any{}, []map[string]any{{"alias": "other", "kind": "parameter", "content_type": "integer"}}} {
				response := e.admin(http.MethodPatch, "/api/v1/applications", map[string]any{
					"name": "payments", "release_name": "runtime", "schema_version": schemaVersion,
					"description": "must not persist", "contract": replacement,
				})
				if errCode(t, response) != "failed_precondition" {
					t.Fatalf("replacement status %d: %s", response.Code, response.Body.String())
				}
				updated, err := e.svc.GetApplication(context.Background(), consoleAdmin(), "payments")
				if err != nil || updated.Description != "updated" || !reflect.DeepEqual(updated.Contract, before.Contract) {
					t.Fatalf("rejected replacement altered application: %+v err=%v", updated, err)
				}
			}
		})
	}
}
