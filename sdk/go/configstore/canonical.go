package configstore

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
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
// re-encoded compactly with object keys sorted by their UTF-8 bytes; container
// nesting deeper than 1000 levels below the root is rejected. Number
// literals are preserved verbatim (1.0 and 1 remain distinct), strings are
// emitted with the minimal JSON escaping (only ", \, and U+0000–U+001F are
// escaped; everything else, including U+2028, is raw UTF-8), and null, true
// and false are literal. Every other content type is returned byte-for-byte.
func CanonicalParameterValue(contentType string, value []byte) ([]byte, error) {
	if contentType != "json" {
		return append([]byte(nil), value...), nil
	}
	node, err := parseJSONReader(bytes.NewBuffer(value))
	if err != nil {
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

func writeCanonicalValue(out *bytes.Buffer, node jsonNode) {
	switch node.kind {
	case nodeNull:
		out.WriteString("null")
	case nodeBool:
		if node.boolean {
			out.WriteString("true")
		} else {
			out.WriteString("false")
		}
	case nodeNumber:
		out.WriteString(node.text)
	case nodeString:
		writeCanonicalString(out, node.text)
	case nodeArray:
		out.WriteByte('[')
		for i, child := range node.elements {
			if i > 0 {
				out.WriteByte(',')
			}
			writeCanonicalValue(out, child)
		}
		out.WriteByte(']')
	case nodeObject:
		properties := append([]jsonProperty(nil), node.properties...)
		sort.Slice(properties, func(i, j int) bool { return properties[i].name < properties[j].name })
		out.WriteByte('{')
		for i, property := range properties {
			if i > 0 {
				out.WriteByte(',')
			}
			writeCanonicalString(out, property.name)
			out.WriteByte(':')
			writeCanonicalValue(out, property.value)
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
