package configgen

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func documentedSchema(t *testing.T, defaultsFunc string) map[string]any {
	t.Helper()
	artifacts, err := Generate(context.Background(), Options{
		Dir: repoRoot(t), Package: "./internal/configgen/testdata/documented", Type: "Config",
		BindingPackage: "documentedgenerated", DefaultsFunc: defaultsFunc,
	})
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := json.Unmarshal(artifacts.Schema, &schema); err != nil {
		t.Fatal(err)
	}
	return schema
}

func property(t *testing.T, schema map[string]any, path ...string) map[string]any {
	t.Helper()
	current := schema
	for _, key := range path {
		properties, ok := current["properties"].(map[string]any)
		if !ok {
			t.Fatalf("no properties under %v", path)
		}
		next, ok := properties[key].(map[string]any)
		if !ok {
			t.Fatalf("missing property %q under %v", key, path)
		}
		current = next
	}
	return current
}

func TestSchemaCarriesDocCommentsAndLiteralDefaults(t *testing.T) {
	schema := documentedSchema(t, "")
	if got := schema["description"]; got != "Config is the documented fixture root." {
		t.Fatalf("root description = %v", got)
	}
	greeting := property(t, schema, "runtime", "greeting")
	if greeting["description"] != "Greeting is shown on the landing page." || greeting["default"] != "hello" {
		t.Fatalf("greeting = %v", greeting)
	}
	if limit := property(t, schema, "runtime", "request_limit"); limit["default"] != float64(100) {
		t.Fatalf("request_limit default = %v (constants resolve through identifiers)", limit["default"])
	}
	attempts := property(t, schema, "runtime", "retry", "attempts")
	if attempts["description"] != "Attempts is the number of tries before giving up." || attempts["default"] != float64(3) {
		t.Fatalf("nested attempts = %v", attempts)
	}
	backoff := property(t, schema, "runtime", "retry", "backoff")
	if backoff["description"] != "Backoff is the initial delay between tries." || backoff["default"] != "250ms" {
		t.Fatalf("trailing comment / duration default = %v", backoff)
	}
	retry := property(t, schema, "runtime", "retry")
	if retry["description"] != "Retry tunes outbound retries." {
		t.Fatalf("retry description = %v", retry["description"])
	}
	if _, ok := retry["default"].(map[string]any); !ok {
		t.Fatalf("struct default should be an object, got %v", retry["default"])
	}
	maxIdle := property(t, schema, "runtime", "max_idle")
	if maxIdle["description"] != "MaxIdle is optional; nil means unlimited." {
		t.Fatalf("nullable wrapper keeps the description: %v", maxIdle)
	}
	if _, has := maxIdle["default"]; has {
		t.Fatalf("a default taken from a local variable must be omitted, got %v", maxIdle["default"])
	}
	if verbose := property(t, schema, "runtime", "verbose"); verbose["default"] != true {
		t.Fatalf("new(true) should default to true, got %v", verbose["default"])
	}
	if burst := property(t, schema, "runtime", "burst"); burst["default"] != float64(0) {
		t.Fatalf("new(int) should default to the zero value, got %v", burst["default"])
	}
	if got, _ := json.Marshal(property(t, schema, "runtime", "fallback")["default"]); string(got) != `{"attempts":5,"backoff":"1s"}` {
		t.Fatalf("helper returning a literal should be inlined, got %s", got)
	}
	computed := property(t, schema, "runtime", "computed")
	if _, has := computed["default"]; has {
		t.Fatalf("helper using a local must yield no object default, got %v", computed["default"])
	}
	if backoff := property(t, schema, "runtime", "computed", "backoff"); backoff["default"] != "1s" {
		t.Fatalf("the evaluable part of an inlined helper still contributes, got %v", backoff["default"])
	}
	policies := property(t, schema, "runtime", "policies")
	policiesInner := policies["anyOf"].([]any)[0].(map[string]any)
	policyField := policiesInner["additionalProperties"].(map[string]any)["properties"].(map[string]any)["attempts"].(map[string]any)
	if policyField["description"] != "Attempts is the number of tries before giving up." {
		t.Fatalf("map value struct fields should be described, got %v", policyField)
	}
	if _, has := policyField["default"]; has {
		t.Fatal("map value struct fields carry no defaults")
	}
	history := property(t, schema, "runtime", "history")
	historyInner := history["anyOf"].([]any)[0].(map[string]any)
	historyField := historyInner["items"].(map[string]any)["properties"].(map[string]any)["backoff"].(map[string]any)
	if historyField["description"] != "Backoff is the initial delay between tries." {
		t.Fatalf("list item struct fields should be described, got %v", historyField)
	}
	tags := property(t, schema, "runtime", "tags")
	if got, _ := json.Marshal(tags["default"]); string(got) != `["blue","canary"]` {
		t.Fatalf("tags default = %s", got)
	}
	server := property(t, schema, "server")
	if got, _ := json.Marshal(server["default"]); string(got) != `{"listen_address":"127.0.0.1:8080"}` {
		t.Fatalf("complete group default = %s", got)
	}
	if _, has := property(t, schema, "runtime")["default"]; has {
		t.Fatal("a group with an unknown field default must not claim a complete default")
	}
	if _, has := schema["properties"].(map[string]any)["api_key"]; has {
		t.Fatal("secrets never enter the schema")
	}
}

func TestSchemaDefaultsCanBeDisabledOrRequired(t *testing.T) {
	schema := documentedSchema(t, "-")
	if _, has := property(t, schema, "runtime", "greeting")["default"]; has {
		t.Fatal("-defaults - must suppress defaults")
	}
	if property(t, schema, "runtime", "greeting")["description"] == nil {
		t.Fatal("descriptions do not depend on defaults")
	}
	_, err := Generate(context.Background(), Options{
		Dir: repoRoot(t), Package: "./internal/configgen/testdata/documented", Type: "Config",
		BindingPackage: "documentedgenerated", DefaultsFunc: "Missing",
	})
	if err == nil || !strings.Contains(err.Error(), "defaults function Missing was not found") {
		t.Fatalf("explicit missing defaults function must fail, got %v", err)
	}
}

func TestUndocumentedRootStaysMinimal(t *testing.T) {
	artifacts, err := Generate(context.Background(), validOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(artifacts.Schema), `"description"`) || strings.Contains(string(artifacts.Schema), `"default"`) {
		t.Fatal("a root without comments or a defaults function must not gain annotations")
	}
}

func TestSchemaReadsDocCommentsFromInlinedPackages(t *testing.T) {
	artifacts, err := Generate(context.Background(), Options{
		Dir: repoRoot(t), Package: "./internal/configgen/testdata/composed", Type: "Config", BindingPackage: "composedgenerated",
	})
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := json.Unmarshal(artifacts.Schema, &schema); err != nil {
		t.Fatal(err)
	}
	if got := property(t, schema, "database", "endpoint")["description"]; got != "Endpoint is the database address shared by every service." {
		t.Fatalf("inlined package field description = %v", got)
	}
	if got := property(t, schema, "runtime", "burst")["description"]; got != "Burst is the number of requests allowed above the steady rate." {
		t.Fatalf("nested inlined field description = %v", got)
	}
}
