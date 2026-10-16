package kmsclient

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"io"
)

// redactedText is what every secret-bearing type prints instead of plaintext.
const redactedText = "[REDACTED]"

// Secret holds secret plaintext together with non-sensitive metadata. It is
// deliberately hard to leak: its formatting, JSON, and YAML implementations
// all emit "[REDACTED]", so it is safe to pass to fmt.Printf, structured loggers,
// json.Marshal, or yaml.Marshal. Plaintext is only accessible through
// the explicit Value / StringValue accessors.
//
// Secret is a value type; copies redact identically.
type Secret struct {
	// BindKey is a declaration-only credential used by generated configuration
	// stores. Secrets fetched from KMS always leave it empty.
	BindKey BindingKey

	value       []byte
	path        string
	version     uint64
	contentType string
}

// NewSecret wraps plaintext in a Secret. It is mainly useful in tests and
// tooling; normal code obtains Secrets from Client.GetSecret. A Secret built
// here was not resolved from KMS, so IsSet reports true only when value is
// non-empty.
func NewSecret(value []byte) Secret { return Secret{value: value} }

// Value returns the raw secret plaintext. This is the only way to read it.
func (s Secret) Value() []byte { return s.value }

// StringValue returns the secret plaintext as a string.
func (s Secret) StringValue() string { return string(s.value) }

// Path returns the store path the secret was read from, if known.
func (s Secret) Path() string { return s.path }

// Version returns the secret version that was read, if known.
func (s Secret) Version() uint64 { return s.version }

// ContentType returns the declared content type of the secret, if known.
func (s Secret) ContentType() string { return s.contentType }

// Equal reports whether plaintext, path, version, content type, and binding key
// contents are equal. Nil and non-nil empty plaintext buffers are distinct;
// buffer capacity and allocation identity do not affect equality. Zero values
// compare equal. Equal does not guarantee constant-time comparison.
func (s Secret) Equal(other Secret) bool {
	return s.path == other.path &&
		s.version == other.version &&
		s.contentType == other.contentType &&
		s.BindKey.Equal(other.BindKey) &&
		(s.value == nil) == (other.value == nil) &&
		bytes.Equal(s.value, other.value)
}

// Clone returns an independent copy of the Secret. The plaintext buffer is
// deep-copied while declaration credentials and immutable metadata are
// preserved. Nil and non-nil empty buffers remain distinct, so the clone is
// Equal to its source. Generated stores strip BindKey before retaining resolved values.
func (s Secret) Clone() Secret {
	s.value = bytes.Clone(s.value)
	return s
}

// IsZero reports whether the Secret carries no plaintext bytes. KMS stores
// empty (zero-byte) secrets, so a Secret resolved from the store can be zero:
// use IsSet, not IsZero, to check that a required secret was resolved.
func (s Secret) IsZero() bool { return len(s.value) == 0 }

// IsSet reports whether the Secret holds a value: it was resolved from KMS (as
// by Client.GetSecret or a managed configuration release, which record the
// store path and version read), or it carries plaintext bytes. A resolved empty
// secret is set even though IsZero is also true; this is the check for a
// required secret.
//
// A Secret built with NewSecret (local configuration, tests, tooling) has no
// store provenance, so it is set only when non-empty: there,
// NewSecret([]byte(os.Getenv("X"))) with X unset reads as missing, exactly as
// IsZero did. The zero Secret, including a declaration that carries only a
// BindKey, is never set.
func (s Secret) IsSet() bool { return s.resolved() || len(s.value) > 0 }

// resolved reports whether the Secret records the store path and version it
// was read from.
func (s Secret) resolved() bool { return s.path != "" && s.version != 0 }

// String implements fmt.Stringer and always redacts.
func (s Secret) String() string { return redactedText }

// GoString implements fmt.GoStringer (used by the %#v verb) and always redacts.
func (s Secret) GoString() string { return redactedText }

// Format implements fmt.Formatter so that every verb (%v, %s, %+v, %#v, %q, ...)
// redacts the plaintext. %q wraps the redaction in quotes for valid output.
func (s Secret) Format(f fmt.State, verb rune) {
	switch verb {
	case 'q':
		_, _ = fmt.Fprintf(f, "%q", redactedText)
	default:
		_, _ = io.WriteString(f, redactedText)
	}
}

// MarshalJSON always emits the redaction string, so a Secret embedded in a
// JSON-marshaled struct never leaks plaintext.
func (s Secret) MarshalJSON() ([]byte, error) {
	return json.Marshal(redactedText)
}

// MarshalJSONTo is the streaming JSON v2 equivalent of MarshalJSON.
func (s Secret) MarshalJSONTo(out *jsontext.Encoder) error {
	return json.MarshalEncode(out, redactedText)
}

// MarshalYAML redacts both plaintext and declaration credentials in YAML exports.
func (s Secret) MarshalYAML() (any, error) {
	return redactedText, nil
}
