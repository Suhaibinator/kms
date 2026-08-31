package httpserver

import (
	"context"
	"encoding/base64"
	"net/http"
	"strings"
	"testing"

	"github.com/Suhaibinator/kms/internal/core"
	"github.com/Suhaibinator/kms/internal/domain"
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
	w := e.admin(http.MethodPost, "/api/v1/configuration-schemas", map[string]any{"id": "runtime", "schema_json": consoleSchemaJSON})
	mustStatus(e.t, w, http.StatusCreated)
	w = e.admin(http.MethodPost, "/api/v1/applications", map[string]any{
		"name": "gradethis", "description": "Grading API", "release_name": "runtime", "schema_id": "runtime", "schema_version": 1,
		"contract": []map[string]any{
			{"alias": "database", "kind": "parameter", "content_type": "json"},
			{"alias": "rate_limits", "kind": "parameter", "content_type": "integer"},
			{"alias": "db_password", "kind": "secret"},
		},
	})
	mustStatus(e.t, w, http.StatusOK)
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
		"application": "gradethis", "environment": env, "dry_run": dryRun,
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
	active, err := e.svc.GetActiveConfigurationRelease(ctx, pr, ns, "runtime")
	if err != nil {
		e.t.Fatalf("active release for ack: %v", err)
	}
	conn := "conn-" + instance
	if err := e.svc.SetReleaseSubscriberConnected(ctx, ns, "runtime", "api", instance, pr.Identity.Name, conn, true); err != nil {
		e.t.Fatal(err)
	}
	if err := e.svc.AcknowledgeConfigurationRelease(ctx, pr, domain.ReleaseAcknowledgement{
		Namespace: ns, ReleaseName: "runtime", ReleaseVersion: active.Release.Version, ActivationRevision: active.ActivationRevision,
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
		"application": "gradethis", "environment": "dev", "expected_active_version": 0,
		"changes": []map[string]any{{"alias": "rate_limits", "value": "3"}},
	})
	mustStatus(t, w, http.StatusConflict)
	if errCode(t, w) != "aborted" {
		t.Fatalf("stale expected_active_version code = %s", errCode(t, w))
	}
	w = e.admin(http.MethodPost, "/api/v1/applications/ship", map[string]any{
		"application": "gradethis", "environment": "dev", "changes": []map[string]any{{"alias": "unknown", "value": "3"}},
	})
	mustStatus(t, w, http.StatusBadRequest)
	w = e.admin(http.MethodPost, "/api/v1/applications/ship", map[string]any{
		"application": "gradethis", "environment": "dev", "changes": []map[string]any{{"alias": "db_password", "value": "leak"}},
	})
	mustStatus(t, w, http.StatusBadRequest)
	// Pin-only changes (secrets included) are accepted.
	w = e.admin(http.MethodPost, "/api/v1/applications/ship", map[string]any{
		"application": "gradethis", "environment": "dev", "dry_run": true, "changes": []map[string]any{{"alias": "db_password", "version": 1}},
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
		"application": "gradethis", "source_env": "dev", "target_env": "prod", "copy_values": true, "description": "Production",
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
	w = e.admin(http.MethodPost, "/api/v1/applications/environments/clone", map[string]any{"application": "gradethis", "source_env": "dev", "target_env": "dev"})
	mustStatus(t, w, http.StatusBadRequest)
}
