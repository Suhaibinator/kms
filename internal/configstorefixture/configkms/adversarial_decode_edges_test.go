package configkms

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/Suhaibinator/kms/sdk/go/configstore"
	"github.com/Suhaibinator/kms/sdk/go/paramstore"
)

func TestAdversarialMalformedDuplicateAndMistypedDocumentGauntlet(t *testing.T) {
	schema := compileAdversarialFixtureSchema(t)
	fixture := startFixture(t, matchingRelease(1, 6001), fixtureDefaults, false, nil)
	validDatabase := adversarialDatabase(adversarialEndpointDefault, "20", `"3s"`)
	validRuntime := adversarialRuntimeDefault
	if !adversarialStrictSchemaAccepts(schema, validDatabase, validRuntime) {
		t.Fatal("strict schema oracle rejected the valid control documents")
	}

	type decodeCase struct {
		name     string
		group    string
		document string
		canary   string
	}
	cases := []decodeCase{
		{name: "empty database document", group: "database", document: ""},
		{name: "whitespace database document", group: "database", document: " \n\t"},
		{name: "truncated database object", group: "database", document: "{"},
		{name: "database trailing JSON value", group: "database", document: validDatabase + " true"},
		{name: "database root null", group: "database", document: "null"},
		{name: "database root array", group: "database", document: "[]"},
		{name: "database root string", group: "database", document: `"object"`},
		{name: "database missing every field", group: "database", document: `{}`},
		{name: "database missing endpoint", group: "database", document: `{"max_open":20,"timeout":"3s"}`},
		{name: "database missing max open", group: "database", document: `{"endpoint":` + adversarialEndpointDefault + `,"timeout":"3s"}`},
		{name: "database missing timeout", group: "database", document: `{"endpoint":` + adversarialEndpointDefault + `,"max_open":20}`},
		{name: "duplicate database field", group: "database", document: `{"endpoint":` + adversarialEndpointDefault + `,"max_open":20,"timeout":"3s","timeout":"4s"}`},
		{name: "duplicate nested field", group: "database", document: adversarialDatabase(`{"host":"db.internal","host":"shadow.internal","ports":[5432,5433],"labels":{"role":["primary"]},"zones":["a","b"]}`, "20", `"3s"`)},
		{name: "duplicate nested map key", group: "database", document: adversarialDatabase(`{"host":"db.internal","ports":[5432,5433],"labels":{"role":["primary"],"role":["shadow"]},"zones":["a","b"]}`, "20", `"3s"`)},
		{name: "secret plaintext embedded in parameter JSON", group: "database", document: strings.TrimSuffix(validDatabase, "}") + `,"database_password":"secret-json-plaintext-canary"}`, canary: "secret-json-plaintext-canary"},
		{name: "endpoint null", group: "database", document: adversarialDatabase("null", "20", `"3s"`)},
		{name: "endpoint array", group: "database", document: adversarialDatabase("[]", "20", `"3s"`)},
		{name: "endpoint host boolean", group: "database", document: adversarialDatabase(`{"host":true,"ports":[5432,5433],"labels":{"role":["primary"]},"zones":["a","b"]}`, "20", `"3s"`)},
		{name: "ports object", group: "database", document: adversarialDatabase(`{"host":"db.internal","ports":{},"labels":{"role":["primary"]},"zones":["a","b"]}`, "20", `"3s"`)},
		{name: "ports item string", group: "database", document: adversarialDatabase(`{"host":"db.internal","ports":["5432",5433],"labels":{"role":["primary"]},"zones":["a","b"]}`, "20", `"3s"`)},
		{name: "ports fractional item", group: "database", document: adversarialDatabase(`{"host":"db.internal","ports":[5432.5,5433],"labels":{"role":["primary"]},"zones":["a","b"]}`, "20", `"3s"`)},
		{name: "labels array", group: "database", document: adversarialDatabase(`{"host":"db.internal","ports":[5432,5433],"labels":[],"zones":["a","b"]}`, "20", `"3s"`)},
		{name: "labels value scalar", group: "database", document: adversarialDatabase(`{"host":"db.internal","ports":[5432,5433],"labels":{"role":"primary"},"zones":["a","b"]}`, "20", `"3s"`)},
		{name: "labels item number", group: "database", document: adversarialDatabase(`{"host":"db.internal","ports":[5432,5433],"labels":{"role":[1]},"zones":["a","b"]}`, "20", `"3s"`)},
		{name: "zones object", group: "database", document: adversarialDatabase(`{"host":"db.internal","ports":[5432,5433],"labels":{"role":["primary"]},"zones":{}}`, "20", `"3s"`)},
		{name: "zones empty", group: "database", document: adversarialDatabase(`{"host":"db.internal","ports":[5432,5433],"labels":{"role":["primary"]},"zones":[]}`, "20", `"3s"`)},
		{name: "zones too long", group: "database", document: adversarialDatabase(`{"host":"db.internal","ports":[5432,5433],"labels":{"role":["primary"]},"zones":["a","b","c"]}`, "20", `"3s"`)},
		{name: "max open boolean", group: "database", document: adversarialDatabase(adversarialEndpointDefault, "true", `"3s"`)},
		{name: "max open string", group: "database", document: adversarialDatabase(adversarialEndpointDefault, `"20"`, `"3s"`)},
		{name: "max open object", group: "database", document: adversarialDatabase(adversarialEndpointDefault, `{}`, `"3s"`)},
		{name: "max open below portable minimum", group: "database", document: adversarialDatabase(adversarialEndpointDefault, "-2147483649", `"3s"`)},
		{name: "timeout number", group: "database", document: adversarialDatabase(adversarialEndpointDefault, "20", "3")},
		{name: "timeout null", group: "database", document: adversarialDatabase(adversarialEndpointDefault, "20", "null")},
		{name: "timeout ISO duration", group: "database", document: adversarialDatabase(adversarialEndpointDefault, "20", `"PT3S"`)},
		{name: "timeout Go duration overflow", group: "database", document: adversarialDatabase(adversarialEndpointDefault, "20", `"2562048h"`)},
		{name: "JSON leading-zero number", group: "database", document: adversarialDatabase(adversarialEndpointDefault, "020", `"3s"`)},
		{name: "JSON NaN number", group: "database", document: adversarialDatabase(adversarialEndpointDefault, "NaN", `"3s"`)},
		{name: "runtime root null", group: "runtime", document: "null"},
		{name: "runtime root array", group: "runtime", document: "[]"},
		{name: "runtime missing every field", group: "runtime", document: `{}`},
		{name: "runtime missing payload", group: "runtime", document: `{"features":["search"],"thresholds":{"burst":100},"window":[0.25,0.75]}`},
		{name: "runtime unknown field", group: "runtime", document: strings.TrimSuffix(validRuntime, "}") + `,"unknown_runtime_canary":true}`, canary: "unknown_runtime_canary"},
		{name: "duplicate runtime field", group: "runtime", document: strings.TrimSuffix(validRuntime, "}") + `,"payload":"c2hhZG93"}`},
		{name: "features object", group: "runtime", document: adversarialRuntime(`{}`, `"Zml4dHVyZS1wYXlsb2Fk"`, `{"burst":100,"steady":25}`, `[0.25,0.75]`)},
		{name: "features item number", group: "runtime", document: adversarialRuntime(`[1]`, `"Zml4dHVyZS1wYXlsb2Fk"`, `{"burst":100,"steady":25}`, `[0.25,0.75]`)},
		{name: "payload number", group: "runtime", document: adversarialRuntime(`["search"]`, "7", `{"burst":100,"steady":25}`, `[0.25,0.75]`)},
		{name: "payload array", group: "runtime", document: adversarialRuntime(`["search"]`, "[]", `{"burst":100,"steady":25}`, `[0.25,0.75]`)},
		{name: "payload missing padding", group: "runtime", document: adversarialRuntime(`["search"]`, `"Zg"`, `{"burst":100,"steady":25}`, `[0.25,0.75]`)},
		{name: "payload embedded newline", group: "runtime", document: adversarialRuntime(`["search"]`, `"Zml4dHVy\nZS1wYXlsb2Fk"`, `{"burst":100,"steady":25}`, `[0.25,0.75]`)},
		{name: "payload nonzero padding bits", group: "runtime", document: adversarialRuntime(`["search"]`, `"Zh=="`, `{"burst":100,"steady":25}`, `[0.25,0.75]`)},
		{name: "thresholds array", group: "runtime", document: adversarialRuntime(`["search"]`, `"Zml4dHVyZS1wYXlsb2Fk"`, "[]", `[0.25,0.75]`)},
		{name: "threshold duplicate map key", group: "runtime", document: adversarialRuntime(`["search"]`, `"Zml4dHVyZS1wYXlsb2Fk"`, `{"burst":100,"burst":101}`, `[0.25,0.75]`)},
		{name: "threshold string value", group: "runtime", document: adversarialRuntime(`["search"]`, `"Zml4dHVyZS1wYXlsb2Fk"`, `{"burst":"100"}`, `[0.25,0.75]`)},
		{name: "threshold fractional value", group: "runtime", document: adversarialRuntime(`["search"]`, `"Zml4dHVyZS1wYXlsb2Fk"`, `{"burst":100.5}`, `[0.25,0.75]`)},
		{name: "threshold negative exponent fraction", group: "runtime", document: adversarialRuntime(`["search"]`, `"Zml4dHVyZS1wYXlsb2Fk"`, `{"burst":1e-1}`, `[0.25,0.75]`)},
		{name: "window null", group: "runtime", document: adversarialRuntime(`["search"]`, `"Zml4dHVyZS1wYXlsb2Fk"`, `{"burst":100}`, "null")},
		{name: "window object", group: "runtime", document: adversarialRuntime(`["search"]`, `"Zml4dHVyZS1wYXlsb2Fk"`, `{"burst":100}`, `{}`)},
		{name: "window short", group: "runtime", document: adversarialRuntime(`["search"]`, `"Zml4dHVyZS1wYXlsb2Fk"`, `{"burst":100}`, `[0.25]`)},
		{name: "window long", group: "runtime", document: adversarialRuntime(`["search"]`, `"Zml4dHVyZS1wYXlsb2Fk"`, `{"burst":100}`, `[0.25,0.75,1]`)},
		{name: "window string item", group: "runtime", document: adversarialRuntime(`["search"]`, `"Zml4dHVyZS1wYXlsb2Fk"`, `{"burst":100}`, `["0.25",0.75]`)},
		{name: "window negative overflow", group: "runtime", document: adversarialRuntime(`["search"]`, `"Zml4dHVyZS1wYXlsb2Fk"`, `{"burst":100}`, `[-1e309,0.75]`)},
	}

	for index, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if test.group != "database" && test.group != "runtime" {
				t.Fatalf("invalid test group %q", test.group)
			}
			database, runtime := validDatabase, validRuntime
			if test.group == "database" {
				database = test.document
			} else {
				runtime = test.document
			}
			if adversarialStrictSchemaAccepts(schema, database, runtime) {
				t.Fatal("generated schema unexpectedly accepted malformed/mistyped document")
			}

			version := uint64(index + 2)
			candidate := matchingRelease(version, 6000+version)
			candidate.databaseDocument = database
			candidate.runtimeDocument = runtime
			activate(t, fixture, candidate)
			ack := waitAdversarialFinalAcknowledgement(t, fixture, version)
			if ack.state != paramstore.ReleaseStateRejected || ack.category != string(configstore.RejectConfigDecodeFailed) || ack.diagnostic != "" {
				t.Fatalf("decode acknowledgement = %+v", ack)
			}
			if got := fixture.store.Current().Release().Version(); got != 1 {
				t.Fatalf("malformed release %d displaced LKG with release %d", version, got)
			}
			if test.canary != "" {
				for _, rendered := range []string{fmt.Sprint(fixture.store.Status()), fmt.Sprint(fixture.store.Stats()), fmt.Sprintf("%+v", ack)} {
					if strings.Contains(rendered, test.canary) {
						t.Fatalf("bounded rejection surface exposed untrusted field/value canary: %q", rendered)
					}
				}
			}
		})
	}

	if got := fixture.store.Stats().Rejected[configstore.RejectConfigDecodeFailed]; got < uint64(len(cases)) {
		t.Fatalf("decode rejection counter = %d, want at least %d", got, len(cases))
	}
}

// JSON Schema receives a parsed JSON data model and therefore cannot detect
// duplicate object properties by itself. Real KMS release validation performs
// strict duplicate-aware parsing before schema validation, so the adversarial
// oracle mirrors both layers rather than silently collapsing duplicate keys.
func adversarialStrictSchemaAccepts(schema interface{ Validate(any) error }, database, runtime string) bool {
	databaseValue, err := adversarialStrictJSON(database)
	if err != nil {
		return false
	}
	runtimeValue, err := adversarialStrictJSON(runtime)
	if err != nil {
		return false
	}
	return schema.Validate(map[string]any{"database": databaseValue, "runtime": runtimeValue}) == nil
}

func adversarialStrictJSON(document string) (any, error) {
	decoder := json.NewDecoder(strings.NewReader(document))
	decoder.UseNumber()
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	value, err := adversarialStrictJSONToken(decoder, token)
	if err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		if err == nil {
			err = errors.New("trailing JSON value")
		}
		return nil, err
	}
	return value, nil
}

func adversarialStrictJSONToken(decoder *json.Decoder, token json.Token) (any, error) {
	switch value := token.(type) {
	case nil, bool, string, json.Number:
		return value, nil
	case json.Delim:
		switch value {
		case '{':
			object := make(map[string]any)
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return nil, err
				}
				key, ok := keyToken.(string)
				if !ok {
					return nil, errors.New("object key is not a string")
				}
				if _, duplicate := object[key]; duplicate {
					return nil, errors.New("duplicate object key")
				}
				itemToken, err := decoder.Token()
				if err != nil {
					return nil, err
				}
				item, err := adversarialStrictJSONToken(decoder, itemToken)
				if err != nil {
					return nil, err
				}
				object[key] = item
			}
			end, err := decoder.Token()
			if err != nil || end != json.Delim('}') {
				return nil, errors.New("invalid object terminator")
			}
			return object, nil
		case '[':
			array := make([]any, 0)
			for decoder.More() {
				itemToken, err := decoder.Token()
				if err != nil {
					return nil, err
				}
				item, err := adversarialStrictJSONToken(decoder, itemToken)
				if err != nil {
					return nil, err
				}
				array = append(array, item)
			}
			end, err := decoder.Token()
			if err != nil || end != json.Delim(']') {
				return nil, errors.New("invalid array terminator")
			}
			return array, nil
		}
	}
	return nil, errors.New("invalid JSON token")
}
