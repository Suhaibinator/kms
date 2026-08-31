package core

import (
	"encoding/json/jsontext"
	"errors"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// decodeStrictJSON parses exactly one JSON value without rounding numbers
// through float64. jsontext applies the v2 syntax defaults before the schema
// helper builds the precision-preserving semantic representation.
func decodeStrictJSON(raw string) (any, error) {
	if !jsontext.Value(raw).IsValid() {
		return nil, errors.New("invalid JSON document")
	}
	return jsonschema.UnmarshalJSON(strings.NewReader(raw))
}
