package httpserver

import (
	"net/http"
	"strings"
	"testing"
)

const diffQuery = "/api/v1/releases/diff?env=prod&app=gradethis&name=runtime&schema_version=1"

// rowsByAlias indexes a diff response's rows.
func rowsByAlias(t *testing.T, body map[string]any) map[string]map[string]any {
	t.Helper()
	rows, _ := body["rows"].([]any)
	out := map[string]map[string]any{}
	for _, raw := range rows {
		row, _ := raw.(map[string]any)
		out[row["alias"].(string)] = row
	}
	return out
}

func TestReleaseDiffShape(t *testing.T) {
	e := newReleaseTestEnv(t)
	e.seedConsoleApp("prod")
	if r := e.ship("prod", "rate_limits", "7", false); r["status"] != "activated" {
		t.Fatalf("ship v1 = %v", r)
	}
	if r := e.ship("prod", "rate_limits", "12", false); r["status"] != "activated" {
		t.Fatalf("ship v2 = %v", r)
	}

	w := e.admin(http.MethodGet, diffQuery+"&from=previous&to=current", nil)
	mustStatus(t, w, http.StatusOK)
	body := decodeBody(t, w)

	from, _ := body["from"].(map[string]any)
	to, _ := body["to"].(map[string]any)
	if from["version"] != float64(1) || to["version"] != float64(2) {
		t.Fatalf("sides = %v -> %v", from["version"], to["version"])
	}
	if from["previous"] != true || from["current"] != false || to["current"] != true || to["previous"] != false {
		t.Fatalf("labels from=%v to=%v", from, to)
	}
	if to["activation_revision"] == float64(0) || to["previous_version"] != float64(1) || from["activation_revision"] != float64(0) {
		t.Fatalf("activation facts from=%v to=%v", from, to)
	}
	for _, key := range []string{"digest", "created_by", "created_at_unix_ms", "name", "schema_version", "namespace"} {
		if _, ok := to[key]; !ok {
			t.Fatalf("to side is missing %s: %v", key, to)
		}
	}
	if body["identical"] != false || body["schema_changed"] != false || body["cross_environment"] != false {
		t.Fatalf("flags = %v", body)
	}
	if body["values_included"] != true || body["value_cap_bytes"] != float64(262144) {
		t.Fatalf("value fields = %v", body)
	}
	counts, _ := body["counts"].(map[string]any)
	if counts["changed"] != float64(1) || counts["unchanged"] != float64(2) || counts["added"] != float64(0) || counts["removed"] != float64(0) || counts["secrets_changed"] != float64(0) || counts["attention"] != float64(0) {
		t.Fatalf("counts = %v", counts)
	}

	rows := rowsByAlias(t, body)
	if len(rows) != 3 {
		t.Fatalf("rows = %v", rows)
	}
	rate := rows["rate_limits"]
	if rate["change"] != "changed" || rate["kind"] != "parameter" {
		t.Fatalf("rate_limits row = %v", rate)
	}
	if reasons, _ := rate["reasons"].([]any); len(reasons) != 1 || reasons[0] != "value" {
		t.Fatalf("rate_limits reasons = %v", rate["reasons"])
	}
	rateFrom, _ := rate["from"].(map[string]any)
	rateTo, _ := rate["to"].(map[string]any)
	if rateFrom["value"] != "7" || rateTo["value"] != "12" || rateFrom["value_state"] != "present" || rateTo["value_state"] != "present" {
		t.Fatalf("rate_limits values = %v -> %v", rateFrom, rateTo)
	}
	if rateFrom["value_bytes"] != float64(1) || rateTo["value_bytes"] != float64(2) {
		t.Fatalf("rate_limits value_bytes = %v -> %v", rateFrom["value_bytes"], rateTo["value_bytes"])
	}
	if rateTo["created_by"] != "admin" || rateTo["created_at_unix_ms"] == float64(0) || rateTo["content_type"] != "integer" {
		t.Fatalf("rate_limits to pin = %v", rateTo)
	}
	if ref, _ := rateTo["ref"].(map[string]any); ref["key"] != "rate_limits" {
		t.Fatalf("rate_limits ref = %v", rateTo["ref"])
	}
	if _, ok := rateTo["secret_state"]; ok {
		t.Fatalf("parameter pin carries secret fields: %v", rateTo)
	}

	db := rows["database"]
	dbTo, _ := db["to"].(map[string]any)
	if db["change"] != "unchanged" || dbTo["value_state"] != "omitted_unchanged" {
		t.Fatalf("database row = %v", db)
	}
	if _, ok := dbTo["value"]; ok {
		t.Fatalf("unchanged row carries a value: %v", dbTo)
	}

	secret := rows["db_password"]
	secretTo, _ := secret["to"].(map[string]any)
	if secret["change"] != "unchanged" || secret["kind"] != "secret" || secretTo["value_state"] != "secret" {
		t.Fatalf("db_password row = %v", secret)
	}
	if _, ok := secretTo["value"]; ok {
		t.Fatalf("secret pin carries a value: %v", secretTo)
	}
	if strings.Contains(w.Body.String(), "hunter2") {
		t.Fatal("response leaks the secret value")
	}
}

func TestReleaseDiffSecretMetadataAndNumericSides(t *testing.T) {
	e := newReleaseTestEnv(t)
	e.seedConsoleApp("prod")
	if r := e.ship("prod", "rate_limits", "7", false); r["status"] != "activated" {
		t.Fatalf("ship v1 = %v", r)
	}
	// A second secret version and a release pinning it, so the secret row changes.
	e.putSecret("prod", "db_password", "hunter2-rotated")
	w := e.admin(http.MethodPost, "/api/v1/applications/ship", map[string]any{
		"schema_version": 1, "application": "gradethis", "environment": "prod",
		"changes": []map[string]any{{"alias": "db_password", "version": 2}},
	})
	mustStatus(t, w, http.StatusOK)
	if r := decodeBody(t, w); r["status"] != "activated" {
		t.Fatalf("ship v2 = %v", r)
	}

	w = e.admin(http.MethodGet, diffQuery+"&from=1&to=2", nil)
	mustStatus(t, w, http.StatusOK)
	body := decodeBody(t, w)
	counts, _ := body["counts"].(map[string]any)
	if counts["changed"] != float64(1) || counts["secrets_changed"] != float64(1) {
		t.Fatalf("counts = %v", counts)
	}
	row := rowsByAlias(t, body)["db_password"]
	if reasons, _ := row["reasons"].([]any); len(reasons) != 1 || reasons[0] != "pin" {
		t.Fatalf("db_password reasons = %v", row["reasons"])
	}
	for _, side := range []string{"from", "to"} {
		pin, _ := row[side].(map[string]any)
		if pin["value_state"] != "secret" || pin["secret_state"] != "enabled" || pin["created_by"] != "admin" {
			t.Fatalf("%s pin = %v", side, pin)
		}
		if _, ok := pin["bound"]; !ok {
			t.Fatalf("%s pin lacks bound: %v", side, pin)
		}
		if _, ok := pin["value"]; ok {
			t.Fatalf("%s secret pin carries a value: %v", side, pin)
		}
	}
	if from, _ := row["from"].(map[string]any); from["version"] != float64(1) {
		t.Fatalf("from pin version = %v", from["version"])
	}
	if strings.Contains(w.Body.String(), "hunter2") {
		t.Fatal("response leaks a secret value")
	}

	// Swapped sides flip the classification symmetrically.
	w = e.admin(http.MethodGet, diffQuery+"&from=2&to=1", nil)
	mustStatus(t, w, http.StatusOK)
	swapped := decodeBody(t, w)
	if to, _ := swapped["to"].(map[string]any); to["version"] != float64(1) || to["previous"] != true {
		t.Fatalf("swapped to side = %v", to)
	}
}

func TestReleaseDiffValuesOff(t *testing.T) {
	e := newReleaseTestEnv(t)
	e.seedConsoleApp("prod")
	for _, value := range []string{"7", "12"} {
		if r := e.ship("prod", "rate_limits", value, false); r["status"] != "activated" {
			t.Fatalf("ship %s = %v", value, r)
		}
	}
	w := e.admin(http.MethodGet, diffQuery+"&from=previous&to=current&values=0", nil)
	mustStatus(t, w, http.StatusOK)
	body := decodeBody(t, w)
	if body["values_included"] != false {
		t.Fatalf("values_included = %v", body["values_included"])
	}
	row := rowsByAlias(t, body)["rate_limits"]
	if row["change"] != "changed" {
		t.Fatalf("row = %v", row)
	}
	to, _ := row["to"].(map[string]any)
	if to["value_state"] != "omitted_request" {
		t.Fatalf("to pin = %v", to)
	}
	if _, ok := to["value"]; ok {
		t.Fatalf("values=0 still carries a value: %v", to)
	}
	if strings.Contains(w.Body.String(), `"12"`) || strings.Contains(w.Body.String(), `"7"`) {
		t.Fatalf("values=0 body contains a value: %s", w.Body.String())
	}

	w = e.admin(http.MethodGet, diffQuery+"&from=previous&to=current&values=maybe", nil)
	mustStatus(t, w, http.StatusBadRequest)
}

func TestReleaseDiffOverCapValueIsOmitted(t *testing.T) {
	e := newReleaseTestEnv(t)
	e.seedConsoleApp("prod")
	if r := e.ship("prod", "rate_limits", "7", false); r["status"] != "activated" {
		t.Fatalf("ship v1 = %v", r)
	}
	// A 300 KiB JSON value: above the 256 KiB per-side cap, below the
	// 1 MiB parameter limit.
	big := `{"blob":"` + strings.Repeat("x", 300<<10) + `"}`
	if r := e.ship("prod", "database", big, false); r["status"] != "activated" {
		t.Fatalf("ship v2 = %v", r)
	}
	w := e.admin(http.MethodGet, diffQuery+"&from=1&to=2", nil)
	mustStatus(t, w, http.StatusOK)
	body := decodeBody(t, w)
	row := rowsByAlias(t, body)["database"]
	from, _ := row["from"].(map[string]any)
	to, _ := row["to"].(map[string]any)
	if from["value_state"] != "present" || from["value"] == nil {
		t.Fatalf("from pin = %v", from)
	}
	if to["value_state"] != "omitted_size" || to["value_bytes"] != float64(len(big)) {
		t.Fatalf("to pin state=%v bytes=%v", to["value_state"], to["value_bytes"])
	}
	if _, ok := to["value"]; ok {
		t.Fatal("over-cap pin carries the value")
	}
	if to["created_by"] != "admin" {
		t.Fatalf("over-cap pin lost authorship: %v", to)
	}
	if len(w.Body.Bytes()) > 64<<10 {
		t.Fatalf("response is %d bytes; the value was not omitted", len(w.Body.Bytes()))
	}
}

func TestReleaseDiffCrossEnvironment(t *testing.T) {
	e := newReleaseTestEnv(t)
	e.seedConsoleApp("prod", "staging")
	// Staging's secret is on its second version, so the two environments pin
	// different versions of what is, by construction, a different secret.
	e.putSecret("staging", "db_password", "hunter2-staging")
	if r := e.ship("prod", "rate_limits", "7", false); r["status"] != "activated" {
		t.Fatalf("prod ship = %v", r)
	}
	if r := e.ship("staging", "rate_limits", "12", false); r["status"] != "activated" {
		t.Fatalf("staging ship = %v", r)
	}
	w := e.admin(http.MethodGet, diffQuery+"&from=current&to=current&to_env=staging", nil)
	mustStatus(t, w, http.StatusOK)
	body := decodeBody(t, w)
	if body["cross_environment"] != true {
		t.Fatalf("cross_environment = %v", body["cross_environment"])
	}
	if to, _ := body["to"].(map[string]any); to["namespace"].(map[string]any)["env"] != "staging" {
		t.Fatalf("to side = %v", to)
	}
	rows := rowsByAlias(t, body)
	// Same seeded value on both sides: independent version histories are not a change.
	if rows["database"]["change"] != "unchanged" {
		t.Fatalf("database = %v", rows["database"])
	}
	if rows["rate_limits"]["change"] != "changed" {
		t.Fatalf("rate_limits = %v", rows["rate_limits"])
	}
	// Secrets are per environment; a differing pinned version is reported as a repin.
	if reasons, _ := rows["db_password"]["reasons"].([]any); len(reasons) != 1 || reasons[0] != "pin" {
		t.Fatalf("db_password reasons = %v", rows["db_password"]["reasons"])
	}
	if strings.Contains(w.Body.String(), "hunter2") {
		t.Fatal("response leaks a secret value")
	}

	// The same track and version on both sides is rejected.
	w = e.admin(http.MethodGet, diffQuery+"&from=current&to=1", nil)
	mustStatus(t, w, http.StatusBadRequest)
}

func TestReleaseDiffErrors(t *testing.T) {
	e := newReleaseTestEnv(t)
	e.seedConsoleApp("prod")

	// Nothing shipped yet: current has no active version.
	w := e.admin(http.MethodGet, diffQuery+"&from=previous&to=current", nil)
	mustStatus(t, w, http.StatusNotFound)

	if r := e.ship("prod", "rate_limits", "7", false); r["status"] != "activated" {
		t.Fatalf("ship v1 = %v", r)
	}
	// One activation: there is no previous label yet.
	w = e.admin(http.MethodGet, diffQuery+"&from=previous&to=current", nil)
	mustStatus(t, w, http.StatusPreconditionFailed)
	if code := errCode(t, w); code != "failed_precondition" {
		t.Fatalf("code = %s", code)
	}
	if msg := decodeBody(t, w)["error"].(map[string]any)["message"].(string); !strings.Contains(msg, "no previous release") {
		t.Fatalf("message = %q", msg)
	}

	// A missing version names its side.
	w = e.admin(http.MethodGet, diffQuery+"&from=1&to=9", nil)
	mustStatus(t, w, http.StatusNotFound)
	if msg := decodeBody(t, w)["error"].(map[string]any)["message"].(string); !strings.Contains(msg, "to release runtime@1:9 not found") {
		t.Fatalf("message = %q", msg)
	}
	w = e.admin(http.MethodGet, diffQuery+"&from=9&to=1", nil)
	mustStatus(t, w, http.StatusNotFound)
	if msg := decodeBody(t, w)["error"].(map[string]any)["message"].(string); !strings.Contains(msg, "from release runtime@1:9 not found") {
		t.Fatalf("message = %q", msg)
	}

	// Selector validation.
	for _, q := range []string{"&from=1", "&to=1", "&from=0&to=1", "&from=latest&to=1", "&from=1&to=1"} {
		w = e.admin(http.MethodGet, diffQuery+q, nil)
		mustStatus(t, w, http.StatusBadRequest)
		if code := errCode(t, w); code != "invalid_argument" {
			t.Fatalf("%s: code = %s", q, code)
		}
	}
	w = e.admin(http.MethodGet, "/api/v1/releases/diff?env=prod&app=gradethis&name=runtime&from=1&to=2", nil)
	mustStatus(t, w, http.StatusBadRequest)

	// Unauthenticated.
	w = e.do(http.MethodGet, diffQuery+"&from=1&to=2", nil, nil)
	mustStatus(t, w, http.StatusUnauthorized)
}
