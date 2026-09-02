package envinject

import (
	"encoding/json/jsontext"
	"io"
	"strings"
	"unicode/utf8"
)

// hexDigits indexes the lowercase hex alphabet used by \uXXXX escapes.
const hexDigits = "0123456789abcdef"

// WriteDotenv writes "NAME=value" lines, quoting each value only when it needs
// it. The output is meant to be read back by a dotenv parser.
func WriteDotenv(w io.Writer, vars []Var) error {
	var buf []byte
	for _, v := range vars {
		buf = append(buf, v.Name...)
		buf = append(buf, '=')
		buf = append(buf, DotenvQuote(v.Value)...)
		buf = append(buf, '\n')
	}
	_, err := w.Write(buf)
	return err
}

// WriteExport writes "export NAME='value'" lines for a POSIX shell to source.
func WriteExport(w io.Writer, vars []Var) error {
	var buf []byte
	for _, v := range vars {
		buf = append(buf, "export "...)
		buf = append(buf, v.Name...)
		buf = append(buf, '=')
		buf = append(buf, ShellQuote(v.Value)...)
		buf = append(buf, '\n')
	}
	_, err := w.Write(buf)
	return err
}

// WriteJSON writes a single JSON object of name/value pairs in the order given,
// indented two spaces and terminated with a newline.
func WriteJSON(w io.Writer, vars []Var) error {
	// AllowInvalidUTF8 keeps a stray byte from failing the whole write; it is
	// mangled to U+FFFD, exactly as the dotenv and YAML writers do. Resolve
	// never produces one, since such a value is base64-encoded instead.
	enc := jsontext.NewEncoder(w, jsontext.WithIndent("  "), jsontext.AllowInvalidUTF8(true))
	if err := enc.WriteToken(jsontext.BeginObject); err != nil {
		return err
	}
	for _, v := range vars {
		if err := enc.WriteToken(jsontext.String(v.Name)); err != nil {
			return err
		}
		if err := enc.WriteToken(jsontext.String(v.Value)); err != nil {
			return err
		}
	}
	return enc.WriteToken(jsontext.EndObject)
}

// WriteYAML writes "NAME: value" lines with every value quoted as a JSON
// string, which is also a valid YAML double-quoted scalar.
func WriteYAML(w io.Writer, vars []Var) error {
	var buf []byte
	for _, v := range vars {
		buf = append(buf, v.Name...)
		buf = append(buf, ':', ' ')
		buf = appendQuoted(buf, v.Value)
		buf = append(buf, '\n')
	}
	_, err := w.Write(buf)
	return err
}

// ShellQuote returns s single-quoted for a POSIX shell. A single quote cannot
// appear inside single quotes, so each one is written as '\'': close,
// escape, reopen. The empty string yields ''.
func ShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// DotenvQuote returns s ready for the right-hand side of a dotenv assignment.
// Values made only of unambiguous characters are left bare, which also makes
// them safe for "set -a; . file"; anything else is written as a double-quoted
// string with JSON escapes, which common dotenv parsers read back exactly.
func DotenvQuote(s string) string {
	if isBareDotenv(s) {
		return s
	}
	return string(appendQuoted(nil, s))
}

// isBareDotenv reports whether s can be written without quotes.
func isBareDotenv(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case strings.IndexByte("_./:@%+=-", c) >= 0:
		default:
			return false
		}
	}
	return true
}

// appendQuoted appends s to dst as a double-quoted JSON string, escaping the
// backslash, the double quote, and every control character. The C1 controls
// (U+0080-U+009F) and the line and paragraph separators are escaped as well:
// JSON tolerates them raw, but a YAML reader treats several of them as line
// breaks and would fold them into spaces. Bytes that are not valid UTF-8
// become U+FFFD, since neither JSON nor YAML can carry them.
func appendQuoted(dst []byte, s string) []byte {
	dst = append(dst, '"')
	for i := 0; i < len(s); {
		c := s[i]
		if c < utf8.RuneSelf {
			switch {
			case c == '\\' || c == '"':
				dst = append(dst, '\\', c)
			case c == '\n':
				dst = append(dst, '\\', 'n')
			case c == '\r':
				dst = append(dst, '\\', 'r')
			case c == '\t':
				dst = append(dst, '\\', 't')
			case c < 0x20, c == 0x7f:
				dst = appendEscape(dst, rune(c))
			default:
				dst = append(dst, c)
			}
			i++
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == utf8.RuneError && size == 1:
			dst = append(dst, "\ufffd"...)
		case r <= 0x9f, r == '\u2028', r == '\u2029':
			dst = appendEscape(dst, r)
		default:
			dst = append(dst, s[i:i+size]...)
		}
		i += size
	}
	return append(dst, '"')
}

// appendEscape appends r as a \uXXXX escape. It is only called for characters
// in the basic multilingual plane, which need no surrogate pair.
func appendEscape(dst []byte, r rune) []byte {
	return append(dst, '\\', 'u',
		hexDigits[(r>>12)&0xf], hexDigits[(r>>8)&0xf], hexDigits[(r>>4)&0xf], hexDigits[r&0xf])
}
