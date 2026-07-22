package paramstore

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
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
}

func TestSecretValueRedaction(t *testing.T) {
	sv := SecretValue{Key: "x", Default: leak}
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

func TestSecretValuePanicsBeforeInit(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on Value() before Init")
		}
	}()
	var sv SecretValue
	_ = sv.Value()
}
