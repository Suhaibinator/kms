package domain

import (
	"encoding/json/v2"
	"testing"
)

// FindingParams must encode exactly as the untyped map it replaced did.
func TestFindingParamsMarshalLikeUntypedMap(t *testing.T) {
	typed := FindingParams{
		"alias":   TextParam(`a"b\c<é>` + "\n\x00"),
		"empty":   TextParam(""),
		"count":   NumberParam(0),
		"neg":     NumberParam(-3),
		"current": NumberParam(uint64(18446744073709551615)),
	}
	untyped := map[string]any{
		"alias":   `a"b\c<é>` + "\n\x00",
		"empty":   "",
		"count":   0,
		"neg":     -3,
		"current": uint64(18446744073709551615),
	}
	got, err := json.Marshal(typed, json.Deterministic(true))
	if err != nil {
		t.Fatal(err)
	}
	want, err := json.Marshal(untyped, json.Deterministic(true))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("typed params = %s, want %s", got, want)
	}
}
