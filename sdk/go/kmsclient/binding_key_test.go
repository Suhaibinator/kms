package kmsclient_test

import (
	"bytes"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/Suhaibinator/kms/sdk/go/configstore"
	"github.com/Suhaibinator/kms/sdk/go/kmsclient"
	"github.com/pelletier/go-toml/v2"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"gopkg.in/yaml.v3"
)

const bindingCanary = "binding-key-canary-DO-NOT-DISCLOSE-123456789"

func TestBindingKeyZeroAndCopies(t *testing.T) {
	var zero kmsclient.BindingKey
	if zero.IsSet() || kmsclient.NewBindingKey("").IsSet() {
		t.Fatal("empty credentials must be unset")
	}
	key := kmsclient.NewBindingKey(bindingCanary)
	copyOfKey := key
	key = kmsclient.BindingKey{}
	if key.IsSet() || !copyOfKey.IsSet() {
		t.Fatal("clearing one credential must not change a retained copy")
	}
	if !kmsclient.NewBindingKey(" ").IsSet() {
		t.Fatal("constructor must not trim credentials")
	}
}

// All tests are outside kmsclient so they exercise the same method visibility
// and reflection behavior as consumers, including non-addressable map values.
func TestBindingKeyRedaction(t *testing.T) {
	key := kmsclient.NewBindingKey(bindingCanary)
	pointer := &key
	var interfaceValue any = key
	var interfacePointer any = pointer
	type nested struct {
		Key     kmsclient.BindingKey
		Pointer *kmsclient.BindingKey
		Any     any
	}
	// Removing parent methods simulates generic inspection which traverses
	// exported fields without honoring the parent's redaction interfaces.
	type secretFields kmsclient.Secret
	type secretValueFields kmsclient.SecretValue
	shapes := map[string]any{
		"value":              key,
		"pointer":            pointer,
		"double pointer":     &pointer,
		"interface value":    interfaceValue,
		"interface pointer":  interfacePointer,
		"struct":             nested{Key: key, Pointer: pointer, Any: key},
		"map values":         map[string]kmsclient.BindingKey{"key": key},
		"map pointers":       map[string]*kmsclient.BindingKey{"key": pointer},
		"slice":              []kmsclient.BindingKey{key, key},
		"pointer slice":      []*kmsclient.BindingKey{pointer},
		"array":              [1]kmsclient.BindingKey{key},
		"interfaces":         []any{key, pointer, map[string]any{"key": key}},
		"secret":             kmsclient.Secret{BindKey: key},
		"secret value":       kmsclient.SecretValue{BindKey: key},
		"secret fields":      secretFields{BindKey: key},
		"declaration fields": secretValueFields{BindKey: key},
		"loader keys": kmsclient.ReleaseLoaderConfig{
			BindingKeys: map[string]kmsclient.BindingKey{"key": key},
		}.BindingKeys,
		"manager keys": configstore.Options{
			BindingKeys: map[string]kmsclient.BindingKey{"key": key},
		}.BindingKeys,
		"zero": kmsclient.BindingKey{},
	}
	for name, value := range shapes {
		t.Run(name, func(t *testing.T) {
			for _, format := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x", "%X", "%d", "%f", "%20.5v"} {
				assertBindingRedacted(t, fmt.Sprintf(format, value), false)
			}
			// TOML requires a document root; use the same wrapper for all
			// encoders to cover keys nested in interface-valued maps as well.
			document := map[string]any{"credential": value}
			for encoder, marshal := range map[string]func(any) ([]byte, error){
				"JSON v1": json.Marshal,
				"JSON v2": func(v any) ([]byte, error) { return jsonv2.Marshal(v) },
				"YAML":    yaml.Marshal,
				"TOML":    toml.Marshal,
			} {
				t.Run(encoder, func(t *testing.T) {
					encoded, err := marshal(document)
					if err != nil {
						t.Fatal(err)
					}
					assertBindingRedacted(t, string(encoded), true)
				})
			}
			testBindingLogs(t, value, name != "double pointer")
		})
	}
}

func TestBindingKeyUnexportedEnclosingFields(t *testing.T) {
	key := kmsclient.NewBindingKey(bindingCanary)
	// fmt skips methods on unexported fields. Inline string storage would
	// disclose plaintext here even with a value-receiver Formatter.
	for name, value := range map[string]any{
		"value":   struct{ key kmsclient.BindingKey }{key},
		"pointer": struct{ key *kmsclient.BindingKey }{&key},
		"map": struct {
			keys map[string]kmsclient.BindingKey
		}{map[string]kmsclient.BindingKey{"key": key}},
		"slice":       struct{ keys []kmsclient.BindingKey }{[]kmsclient.BindingKey{key}},
		"interface":   struct{ key any }{key},
		"secret":      struct{ secret kmsclient.Secret }{kmsclient.Secret{BindKey: key}},
		"declaration": struct{ secret kmsclient.SecretValue }{kmsclient.SecretValue{BindKey: key}},
		"loader": struct{ config kmsclient.ReleaseLoaderConfig }{kmsclient.ReleaseLoaderConfig{
			BindingKeys: map[string]kmsclient.BindingKey{"key": key},
		}},
		"manager": struct{ options configstore.Options }{configstore.Options{
			BindingKeys: map[string]kmsclient.BindingKey{"key": key},
		}},
	} {
		t.Run(name, func(t *testing.T) {
			for _, format := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x", "%X"} {
				assertBindingRedacted(t, fmt.Sprintf(format, value), false)
			}
			testBindingLogs(t, value, false)
		})
	}
}

func TestBindingKeyNilPointer(t *testing.T) {
	var key *kmsclient.BindingKey
	for _, format := range []string{"%v", "%+v", "%#v", "%s", "%q"} {
		assertBindingRedacted(t, fmt.Sprintf(format, key), false)
	}
	for _, marshal := range []func(any) ([]byte, error){json.Marshal, yaml.Marshal, toml.Marshal} {
		encoded, err := marshal(struct{ Credential *kmsclient.BindingKey }{key})
		if err != nil {
			t.Fatal(err)
		}
		assertBindingRedacted(t, string(encoded), false)
	}
	testBindingLogs(t, key, false)
}

func testBindingLogs(t *testing.T, value any, wantMarker bool) {
	t.Helper()
	for _, jsonHandler := range []bool{false, true} {
		var output bytes.Buffer
		var handler slog.Handler = slog.NewTextHandler(&output, nil)
		if jsonHandler {
			handler = slog.NewJSONHandler(&output, nil)
		}
		slog.New(handler).Info("credential test", slog.Any("credential", value))
		assertBindingRedacted(t, output.String(), wantMarker)
	}
	for _, field := range []zap.Field{zap.Any("credential", value), zap.Reflect("credential", value)} {
		var output bytes.Buffer
		encoder := zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig())
		logger := zap.New(zapcore.NewCore(encoder, zapcore.AddSync(&output), zap.DebugLevel))
		logger.Info("credential test", field)
		assertBindingRedacted(t, output.String(), wantMarker)
	}
}

func assertBindingRedacted(t *testing.T, output string, wantMarker bool) {
	t.Helper()
	if strings.Contains(output, bindingCanary) {
		t.Fatalf("credential disclosed: %s", output)
	}
	if wantMarker && !strings.Contains(output, "[REDACTED]") {
		t.Fatalf("redaction marker missing: %s", output)
	}
}
