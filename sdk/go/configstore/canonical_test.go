package configstore

import (
	"encoding/json"
	"os"
	"testing"
)

type canonicalVector struct {
	Name        string `json:"name"`
	ContentType string `json:"content_type"`
	Input       string `json:"input"`
	Canonical   string `json:"canonical"`
	SHA256      string `json:"sha256"`
	Error       bool   `json:"error"`
}

func loadCanonicalVectors(t *testing.T) []canonicalVector {
	t.Helper()
	raw, err := os.ReadFile("testdata/canonical_vectors.json")
	if err != nil {
		t.Fatalf("read vectors: %v", err)
	}
	var vectors []canonicalVector
	if err := json.Unmarshal(raw, &vectors); err != nil {
		t.Fatalf("decode vectors: %v", err)
	}
	if len(vectors) == 0 {
		t.Fatal("no vectors")
	}
	return vectors
}

func TestCanonicalParameterValueVectors(t *testing.T) {
	for _, vector := range loadCanonicalVectors(t) {
		t.Run(vector.Name, func(t *testing.T) {
			canonical, err := CanonicalParameterValue(vector.ContentType, []byte(vector.Input))
			if vector.Error {
				if err == nil {
					t.Fatalf("expected error, got canonical %q", canonical)
				}
				if _, err := ParameterHash(vector.ContentType, []byte(vector.Input)); err == nil {
					t.Fatal("ParameterHash must fail when canonicalization fails")
				}
				return
			}
			if err != nil {
				t.Fatalf("canonicalize: %v", err)
			}
			if string(canonical) != vector.Canonical {
				t.Fatalf("canonical mismatch:\n got %q\nwant %q", canonical, vector.Canonical)
			}
			hash, err := ParameterHash(vector.ContentType, []byte(vector.Input))
			if err != nil {
				t.Fatalf("hash: %v", err)
			}
			if hash != vector.SHA256 {
				t.Fatalf("hash mismatch: got %s want %s", hash, vector.SHA256)
			}
			// Canonical form is a fixed point.
			again, err := CanonicalParameterValue(vector.ContentType, canonical)
			if err != nil {
				t.Fatalf("re-canonicalize: %v", err)
			}
			if string(again) != vector.Canonical {
				t.Fatalf("canonical form is not idempotent: %q", again)
			}
		})
	}
}

func TestCanonicalParameterValueRejectsInvalidUTF8(t *testing.T) {
	if _, err := CanonicalParameterValue("json", []byte("{\"s\":\"\xff\"}")); err == nil {
		t.Fatal("expected invalid UTF-8 error")
	}
}

func TestCanonicalParameterValueDoesNotAliasInput(t *testing.T) {
	input := []byte("raw bytes")
	canonical, err := CanonicalParameterValue("string", input)
	if err != nil {
		t.Fatal(err)
	}
	input[0] = 'X'
	if string(canonical) != "raw bytes" {
		t.Fatalf("passthrough aliased caller memory: %q", canonical)
	}
}

func TestSemanticallyEqualDocumentsHashEqually(t *testing.T) {
	a, err := ParameterHash("json", []byte("{\n  \"b\": [1, 2],\n  \"a\": {\"y\": \"\\u00e9\", \"x\": null}\n}\n"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := ParameterHash("json", []byte(`{"a":{"x":null,"y":"é"},"b":[1,2]}`))
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatalf("hashes differ: %s vs %s", a, b)
	}
}
