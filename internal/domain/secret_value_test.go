package domain

import "testing"

func TestIsJSONMediaType(t *testing.T) {
	cases := map[string]bool{
		"application/json":                true,
		"application/json; charset=utf-8": true,
		"APPLICATION/JSON":                true,
		"application/vnd.foo+json":        true,
		"application/ld+json; profile=x":  true,
		" application/json ;charset":      true,
		"":                                false,
		"application/octet-stream":        false,
		"text/plain":                      false,
		"application/jsonl":               false,
		"application/json-seq":            false,
		"text/json-ish":                   false,
	}
	for contentType, want := range cases {
		if got := IsJSONMediaType(contentType); got != want {
			t.Errorf("IsJSONMediaType(%q) = %v, want %v", contentType, got, want)
		}
	}
}
