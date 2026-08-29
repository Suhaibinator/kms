package configstore

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"unicode/utf8"
)

// CanonicalParameterValue returns the canonical byte form of one parameter
// value so that semantically identical documents hash identically regardless
// of how they were written. The same function is used by the KMS server when
// it verifies release defaults and by the SDK when it hashes source-owned
// defaults, so the two sides can never disagree on formatting.
//
// For contentType "json" the document is decoded strictly (exactly one value,
// no trailing data, duplicate object keys rejected, valid UTF-8 required) and
// re-encoded compactly with object keys sorted by their UTF-8 bytes. Number
// literals are preserved verbatim (1.0 and 1 remain distinct), strings are
// emitted with the minimal JSON escaping (only ", \, and U+0000–U+001F are
// escaped; everything else, including U+2028, is raw UTF-8), and null, true
// and false are literal. Every other content type is returned byte-for-byte.
func CanonicalParameterValue(contentType string, value []byte) ([]byte, error) {
	if contentType != "json" {
		return append([]byte(nil), value...), nil
	}
	if !utf8.Valid(value) {
		return nil, errors.New("configstore: canonical json: invalid UTF-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(value))
	decoder.UseNumber()
	node, err := readCanonicalValue(decoder)
	if err != nil {
		return nil, fmt.Errorf("configstore: canonical json: %w", err)
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("configstore: canonical json: trailing data after document")
		}
		return nil, fmt.Errorf("configstore: canonical json: %w", err)
	}
	var out bytes.Buffer
	writeCanonicalValue(&out, node)
	return out.Bytes(), nil
}

// ParameterHash returns the lowercase hex SHA-256 of CanonicalParameterValue.
func ParameterHash(contentType string, value []byte) (string, error) {
	canonical, err := CanonicalParameterValue(contentType, value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
}

func readCanonicalValue(decoder *json.Decoder) (any, error) {
	token, err := decoder.Token()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return nil, errors.New("empty document")
		}
		return nil, err
	}
	delimiter, isDelimiter := token.(json.Delim)
	if !isDelimiter {
		return token, nil
	}
	switch delimiter {
	case '{':
		object := make(map[string]any)
		for decoder.More() {
			nameToken, err := decoder.Token()
			if err != nil {
				return nil, err
			}
			name, ok := nameToken.(string)
			if !ok {
				return nil, errors.New("object key is not a string")
			}
			if _, duplicate := object[name]; duplicate {
				return nil, fmt.Errorf("duplicate object key %q", name)
			}
			child, err := readCanonicalValue(decoder)
			if err != nil {
				return nil, err
			}
			object[name] = child
		}
		if _, err := decoder.Token(); err != nil {
			return nil, err
		}
		return object, nil
	case '[':
		array := make([]any, 0)
		for decoder.More() {
			child, err := readCanonicalValue(decoder)
			if err != nil {
				return nil, err
			}
			array = append(array, child)
		}
		if _, err := decoder.Token(); err != nil {
			return nil, err
		}
		return array, nil
	default:
		return nil, fmt.Errorf("unexpected delimiter %q", delimiter)
	}
}

func writeCanonicalValue(out *bytes.Buffer, node any) {
	switch value := node.(type) {
	case nil:
		out.WriteString("null")
	case bool:
		if value {
			out.WriteString("true")
		} else {
			out.WriteString("false")
		}
	case json.Number:
		out.WriteString(value.String())
	case string:
		writeCanonicalString(out, value)
	case []any:
		out.WriteByte('[')
		for i, child := range value {
			if i > 0 {
				out.WriteByte(',')
			}
			writeCanonicalValue(out, child)
		}
		out.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(value))
		for key := range value {
			keys = append(keys, key)
		}
		sort.Strings(keys) // byte order == UTF-8 code point order
		out.WriteByte('{')
		for i, key := range keys {
			if i > 0 {
				out.WriteByte(',')
			}
			writeCanonicalString(out, key)
			out.WriteByte(':')
			writeCanonicalValue(out, value[key])
		}
		out.WriteByte('}')
	default:
		// Unreachable: the strict reader only yields the cases above.
		out.WriteString("null")
	}
}

const canonicalHex = "0123456789abcdef"

// writeCanonicalString applies the minimal escaping mandated by RFC 8259 and
// nothing more, matching JSON.stringify for well-formed strings.
func writeCanonicalString(out *bytes.Buffer, value string) {
	out.WriteByte('"')
	for i := 0; i < len(value); {
		character := value[i]
		if character >= 0x20 && character != '"' && character != '\\' {
			_, size := utf8.DecodeRuneInString(value[i:])
			out.WriteString(value[i : i+size])
			i += size
			continue
		}
		switch character {
		case '"':
			out.WriteString(`\"`)
		case '\\':
			out.WriteString(`\\`)
		case '\b':
			out.WriteString(`\b`)
		case '\f':
			out.WriteString(`\f`)
		case '\n':
			out.WriteString(`\n`)
		case '\r':
			out.WriteString(`\r`)
		case '\t':
			out.WriteString(`\t`)
		default:
			out.WriteString(`\u00`)
			out.WriteByte(canonicalHex[character>>4])
			out.WriteByte(canonicalHex[character&0xF])
		}
		i++
	}
	out.WriteByte('"')
}
