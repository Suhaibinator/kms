package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// decodeStrictJSON parses one JSON value without rounding numbers through
// float64 and rejects duplicate object properties at every nesting level.
// Keeping this lexical check on parameter writes is important because JSON
// Schema sees an already-decoded object and cannot recover duplicate keys.
func decodeStrictJSON(raw string) (any, error) {
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	value, err := decodeStrictJSONValue(decoder)
	if err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("multiple JSON values")
		}
		return nil, err
	}
	return value, nil
}

func decodeStrictJSONValue(decoder *json.Decoder) (any, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	delim, isDelim := token.(json.Delim)
	if !isDelim {
		return token, nil
	}
	switch delim {
	case '{':
		object := make(map[string]any)
		for decoder.More() {
			nameToken, err := decoder.Token()
			if err != nil {
				return nil, err
			}
			name, ok := nameToken.(string)
			if !ok {
				return nil, errors.New("JSON object property is not a string")
			}
			if _, exists := object[name]; exists {
				return nil, fmt.Errorf("duplicate JSON property %q", name)
			}
			value, err := decodeStrictJSONValue(decoder)
			if err != nil {
				return nil, err
			}
			object[name] = value
		}
		closing, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		if closing != json.Delim('}') {
			return nil, errors.New("malformed JSON object")
		}
		return object, nil
	case '[':
		array := make([]any, 0)
		for decoder.More() {
			value, err := decodeStrictJSONValue(decoder)
			if err != nil {
				return nil, err
			}
			array = append(array, value)
		}
		closing, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		if closing != json.Delim(']') {
			return nil, errors.New("malformed JSON array")
		}
		return array, nil
	default:
		return nil, errors.New("unexpected JSON delimiter")
	}
}
