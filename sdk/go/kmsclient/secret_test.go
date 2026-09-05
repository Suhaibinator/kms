package kmsclient

import (
	"encoding/json/v2"
	"fmt"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const leak = "super-secret-plaintext"

// assertRedacted fails if s contains the plaintext or does not contain the
// redaction marker.
func assertRedacted(t *testing.T, label, s string) {
	t.Helper()
	if strings.Contains(s, leak) {
		t.Errorf("%s leaked plaintext: %q", label, s)
	}
	if !strings.Contains(s, redactedText) {
		t.Errorf("%s missing redaction marker: %q", label, s)
	}
}

func TestSecretRedaction(t *testing.T) {
	s := NewSecret([]byte(leak))

	assertRedacted(t, "String()", s.String())
	assertRedacted(t, "GoString()", s.GoString())
	assertRedacted(t, "%v", fmt.Sprintf("%v", s))
	assertRedacted(t, "%s", fmt.Sprintf("%s", s))
	assertRedacted(t, "%+v", fmt.Sprintf("%+v", s))
	assertRedacted(t, "%#v", fmt.Sprintf("%#v", s))
	assertRedacted(t, "%q", fmt.Sprintf("%q", s))
	assertRedacted(t, "pointer %v", fmt.Sprintf("%v", &s))

	j, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	assertRedacted(t, "json", string(j))
	legacyPath, err := s.MarshalJSON()
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}
	if string(legacyPath) != string(j) {
		t.Fatalf("MarshalJSON = %s, streaming marshal = %s", legacyPath, j)
	}

	// Plaintext must still be reachable explicitly.
	if got := s.StringValue(); got != leak {
		t.Errorf("StringValue = %q, want %q", got, leak)
	}
	if got := string(s.Value()); got != leak {
		t.Errorf("Value = %q, want %q", got, leak)
	}
}

func TestSecretRedactionInStruct(t *testing.T) {
	type wrapper struct {
		Name   string
		Secret Secret
	}
	w := wrapper{Name: "db", Secret: NewSecret([]byte(leak))}

	assertRedacted(t, "struct %v", fmt.Sprintf("%v", w))
	assertRedacted(t, "struct %+v", fmt.Sprintf("%+v", w))

	j, err := json.Marshal(w)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	assertRedacted(t, "struct json", string(j))
	if !strings.Contains(string(j), "db") {
		t.Errorf("non-secret field should still marshal: %s", j)
	}
}

func TestSecretCloneDeepCopiesPlaintextAndPreservesMetadata(t *testing.T) {
	original := Secret{
		BindKey:     NewBindingKey("declaration-binding-key"),
		value:       []byte("secret"),
		path:        "/prod/app/password",
		version:     17,
		contentType: "text/plain",
	}
	clone := original.Clone()

	original.value[0] = 'S'
	if got := clone.StringValue(); got != "secret" {
		t.Fatalf("clone plaintext changed with original: %q", got)
	}
	clone.value[1] = 'E'
	if got := original.StringValue(); got != "Secret" {
		t.Fatalf("original plaintext changed with clone: %q", got)
	}
	if clone.Path() != original.Path() || clone.Version() != original.Version() || clone.ContentType() != original.ContentType() {
		t.Fatalf("clone metadata = (%q, %d, %q), want (%q, %d, %q)",
			clone.Path(), clone.Version(), clone.ContentType(),
			original.Path(), original.Version(), original.ContentType())
	}
	if clone.BindKey != original.BindKey {
		t.Fatalf("clone BindKey = %q, want declaration credential preserved", clone.BindKey)
	}
	if !(Secret{BindKey: NewBindingKey("credential-only")}).IsZero() {
		t.Fatal("declaration-only Secret must remain zero for value validation")
	}
}

func TestSecretFormattingRedactsDeclarationBindingKey(t *testing.T) {
	const bindingKey = "binding-key-that-must-never-appear"
	secret := Secret{BindKey: NewBindingKey(bindingKey)}
	for name, rendered := range map[string]string{
		"String": secret.String(), "GoString": secret.GoString(), "%v": fmt.Sprintf("%v", secret),
		"%+v": fmt.Sprintf("%+v", secret), "%#v": fmt.Sprintf("%#v", secret), "%q": fmt.Sprintf("%q", secret),
	} {
		if strings.Contains(rendered, bindingKey) || !strings.Contains(rendered, redactedText) {
			t.Errorf("%s rendered declaration credential: %q", name, rendered)
		}
	}
	encoded, err := json.Marshal(secret)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), bindingKey) {
		t.Fatalf("JSON rendered declaration credential: %s", encoded)
	}
}

func TestSecretValueRedaction(t *testing.T) {
	const bindingKey = "secret-value-binding-key"
	sv := SecretValue{Key: "x", BindKey: NewBindingKey(bindingKey), Default: leak}
	// Init via default so it holds plaintext.
	c, _ := newTestClient(t, Config{})
	if err := sv.Init(c); err != nil {
		t.Fatalf("Init: %v", err)
	}

	assertRedacted(t, "SecretValue %v", fmt.Sprintf("%v", sv))
	assertRedacted(t, "SecretValue %s", fmt.Sprintf("%s", sv))
	assertRedacted(t, "SecretValue %+v", fmt.Sprintf("%+v", sv))
	assertRedacted(t, "SecretValue %#v", fmt.Sprintf("%#v", sv))
	assertRedacted(t, "SecretValue ptr %v", fmt.Sprintf("%v", &sv))

	j, err := json.Marshal(sv)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	assertRedacted(t, "SecretValue json", string(j))
	if strings.Contains(fmt.Sprintf("%#v", sv), bindingKey) || strings.Contains(string(j), bindingKey) {
		t.Fatal("SecretValue formatting exposed BindKey")
	}
	legacyPath, err := sv.MarshalJSON()
	if err != nil {
		t.Fatalf("SecretValue.MarshalJSON: %v", err)
	}
	if string(legacyPath) != string(j) {
		t.Fatalf("SecretValue.MarshalJSON = %s, streaming marshal = %s", legacyPath, j)
	}

	// And embedded in a config struct with an exported field.
	type cfg struct {
		APIKey SecretValue
	}
	cc := cfg{APIKey: sv}
	assertRedacted(t, "config %+v", fmt.Sprintf("%+v", cc))
	cj, err := json.Marshal(cc)
	if err != nil {
		t.Fatalf("json.Marshal cfg: %v", err)
	}
	assertRedacted(t, "config json", string(cj))

	if sv.Value() != leak {
		t.Errorf("Value = %q, want %q", sv.Value(), leak)
	}
}

func TestSecretValueYAMLRedaction(t *testing.T) {
	const (
		token    = "secret-value-token-must-never-appear-in-yaml"
		bindKey  = "secret-value-binding-key-must-never-appear-in-yaml"
		fallback = "secret-value-default-must-never-appear-in-yaml"
	)
	secret := SecretValue{
		Key:     "service/api-key",
		Token:   token,
		BindKey: NewBindingKey(bindKey),
		Default: fallback,
	}

	type nested struct {
		Value   SecretValue  `yaml:"value"`
		Pointer *SecretValue `yaml:"pointer"`
	}
	var interfaceValue any = secret
	var interfacePointer any = &secret

	for name, value := range map[string]any{
		"value":                  secret,
		"pointer":                &secret,
		"nested struct":          nested{Value: secret, Pointer: &secret},
		"map of values":          map[string]SecretValue{"credential": secret},
		"map of pointers":        map[string]*SecretValue{"credential": &secret},
		"interface-held value":   interfaceValue,
		"interface-held pointer": interfacePointer,
	} {
		t.Run(name, func(t *testing.T) {
			encoded, err := yaml.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			for field, sensitive := range map[string]string{
				"Token": token, "BindKey": bindKey, "Default": fallback,
			} {
				if strings.Contains(string(encoded), sensitive) {
					t.Errorf("YAML leaked %s: %s", field, encoded)
				}
			}
			if !strings.Contains(string(encoded), redactedText) {
				t.Errorf("YAML missing redaction marker: %s", encoded)
			}
		})
	}
}

func TestSecretValuePanicsBeforeInit(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on Value() before Init")
		}
	}()
	var sv SecretValue
	_ = sv.Value()
}

func TestSecretYAMLRedaction(t *testing.T) {
	const bindingKey = "binding-key-must-never-appear-in-yaml"
	declaration := Secret{BindKey: NewBindingKey(bindingKey)}
	resolved := NewSecret([]byte(leak))
	resolved.BindKey = NewBindingKey(bindingKey)
	for name, secret := range map[string]Secret{"declaration": declaration, "resolved": resolved, "clone": resolved.Clone()} {
		t.Run(name, func(t *testing.T) {
			for _, value := range []any{secret, &secret, struct {
				Credential Secret `yaml:"credential"`
			}{secret}, map[string]Secret{"credential": secret}} {
				encoded, err := yaml.Marshal(value)
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(encoded), bindingKey) {
					t.Errorf("YAML leaked binding credential: %s", encoded)
				}
				assertRedacted(t, "YAML", string(encoded))
			}
		})
	}
}
