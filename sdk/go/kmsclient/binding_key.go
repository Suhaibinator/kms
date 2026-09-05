package kmsclient

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"io"
	"log/slog"
)

// BindingKey holds an operator-owned credential for a bound secret. Construct
// one with NewBindingKey; the zero value means no key. Copies are immutable and
// may be shared across goroutines. There is no public plaintext accessor.
//
// Formatting and supported serializers redact the key. Private pointer-backed
// storage also avoids inline plaintext when fmt traverses an unexported field
// of an enclosing struct and cannot call the redaction methods. This is an
// accidental-disclosure guard, not protection against inspection of private
// fields through reflection, a debugger, or a memory dump. Go strings cannot be
// reliably erased; clearing a BindingKey only releases that copy's reference.
type BindingKey struct {
	value *string
}

// NewBindingKey wraps value without trimming, normalizing, or validating it.
// The server validates keys for the requested operation. An empty value returns
// the zero BindingKey. The underlying string is retained, not securely copied.
func NewBindingKey(value string) BindingKey {
	if value == "" {
		return BindingKey{}
	}
	return BindingKey{value: &value}
}

// IsSet reports whether the credential contains a non-empty key.
func (k BindingKey) IsSet() bool { return k.value != nil && *k.value != "" }

// plaintext is used only at the RPC boundary inside kmsclient.
func (k BindingKey) plaintext() string {
	if k.value == nil {
		return ""
	}
	return *k.value
}

// String redacts the credential, including its zero value.
func (k BindingKey) String() string { return redactedText }

// GoString redacts Go-syntax formatting.
func (k BindingKey) GoString() string { return redactedText }

// Format redacts every formatting verb; %q quotes the redaction marker.
func (k BindingKey) Format(f fmt.State, verb rune) {
	if verb == 'q' {
		_, _ = fmt.Fprintf(f, "%q", redactedText)
		return
	}
	_, _ = io.WriteString(f, redactedText)
}

// MarshalJSON redacts JSON v1 encoding.
func (k BindingKey) MarshalJSON() ([]byte, error) { return json.Marshal(redactedText) }

// MarshalJSONTo redacts JSON v2 streaming encoding.
func (k BindingKey) MarshalJSONTo(out *jsontext.Encoder) error {
	return json.MarshalEncode(out, redactedText)
}

// MarshalYAML redacts YAML encoding.
func (k BindingKey) MarshalYAML() (any, error) { return redactedText, nil }

// MarshalText redacts encoders that honor encoding.TextMarshaler, including
// supported TOML encoders. Serialized credentials cannot be round-tripped.
func (k BindingKey) MarshalText() ([]byte, error) { return []byte(redactedText), nil }

// LogValue redacts structured logging through slog.
func (k BindingKey) LogValue() slog.Value { return slog.StringValue(redactedText) }
