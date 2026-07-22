package paramstore

import (
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"time"
)

// ReleaseEntryMetadata describes one immutable resource pin in a configuration
// release. It contains no parameter value, secret plaintext, or access token.
type ReleaseEntryMetadata struct {
	Alias           string `json:"alias"`
	Kind            string `json:"kind"`
	Path            string `json:"path"`
	Version         uint64 `json:"version"`
	ContentType     string `json:"content_type,omitempty"`
	MetadataJSON    string `json:"metadata_json,omitempty"`
	ParameterDigest string `json:"parameter_digest,omitempty"`
	ClientBound     bool   `json:"client_bound,omitempty"`
	HasAccessToken  bool   `json:"has_access_token,omitempty"`
}

// ReleaseParameter is a resolved, version-pinned non-secret value. Its
// metadata is copied from the immutable release manifest.
type ReleaseParameter struct {
	value string
	entry ReleaseEntryMetadata
}

// Value returns the parameter document exactly as stored.
func (p ReleaseParameter) Value() string { return p.value }

// StringValue is an alias for Value.
func (p ReleaseParameter) StringValue() string { return p.value }

// Entry returns the resource pin and non-sensitive metadata.
func (p ReleaseParameter) Entry() ReleaseEntryMetadata { return p.entry }

// ReleaseManifest is an immutable, unresolved configuration release. It
// contains only release identity and non-sensitive entry metadata, never
// parameter values, secret plaintext, or access tokens. Its entry map is
// private and accessors return copies so validation callbacks cannot alter the
// candidate that the loader will resolve.
type ReleaseManifest struct {
	namespace          string
	name               string
	version            uint64
	activationRevision uint64
	schemaID           string
	schemaVersion      uint64
	digest             string
	metadataJSON       string
	entries            map[string]ReleaseEntryMetadata
}

func (m ReleaseManifest) Namespace() string          { return m.namespace }
func (m ReleaseManifest) Name() string               { return m.name }
func (m ReleaseManifest) Version() uint64            { return m.version }
func (m ReleaseManifest) ActivationRevision() uint64 { return m.activationRevision }
func (m ReleaseManifest) SchemaID() string           { return m.schemaID }
func (m ReleaseManifest) SchemaVersion() uint64      { return m.schemaVersion }
func (m ReleaseManifest) Digest() string             { return m.digest }
func (m ReleaseManifest) MetadataJSON() string       { return m.metadataJSON }

// Entries returns an alias-keyed copy of every unresolved release entry.
func (m ReleaseManifest) Entries() map[string]ReleaseEntryMetadata {
	return maps.Clone(m.entries)
}

// Entry returns metadata for one stable alias.
func (m ReleaseManifest) Entry(alias string) (ReleaseEntryMetadata, bool) {
	entry, ok := m.entries[alias]
	return entry, ok
}

// String intentionally contains only release identity, never resolved values.
func (m ReleaseManifest) String() string {
	return fmt.Sprintf("ReleaseManifest{%s/%s version=%d revision=%d digest=%s entries=%d}",
		m.namespace, m.name, m.version, m.activationRevision, m.digest, len(m.entries))
}

// GoString uses the same safe representation as String.
func (m ReleaseManifest) GoString() string { return m.String() }

// Format prevents formatting from reflecting private implementation fields.
func (m ReleaseManifest) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, m.String()) }

// MarshalJSON emits only release identity and non-sensitive entry metadata.
func (m ReleaseManifest) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Namespace          string                          `json:"namespace"`
		Name               string                          `json:"name"`
		Version            uint64                          `json:"version"`
		ActivationRevision uint64                          `json:"activation_revision"`
		SchemaID           string                          `json:"schema_id,omitempty"`
		SchemaVersion      uint64                          `json:"schema_version,omitempty"`
		Digest             string                          `json:"digest"`
		Entries            map[string]ReleaseEntryMetadata `json:"entries"`
	}{
		Namespace:          m.namespace,
		Name:               m.name,
		Version:            m.version,
		ActivationRevision: m.activationRevision,
		SchemaID:           m.schemaID,
		SchemaVersion:      m.schemaVersion,
		Digest:             m.digest,
		Entries:            m.Entries(),
	})
}

// ReleaseSnapshot is a completely resolved configuration release candidate.
// Its maps are private and accessors return copies, so application code cannot
// alter the candidate seen by another preparation step.
type ReleaseSnapshot struct {
	namespace          string
	name               string
	version            uint64
	activationRevision uint64
	schemaID           string
	schemaVersion      uint64
	digest             string
	metadataJSON       string
	entries            map[string]ReleaseEntryMetadata
	parameters         map[string]ReleaseParameter
	secrets            map[string]Secret
}

func (s ReleaseSnapshot) Namespace() string          { return s.namespace }
func (s ReleaseSnapshot) Name() string               { return s.name }
func (s ReleaseSnapshot) Version() uint64            { return s.version }
func (s ReleaseSnapshot) ActivationRevision() uint64 { return s.activationRevision }
func (s ReleaseSnapshot) SchemaID() string           { return s.schemaID }
func (s ReleaseSnapshot) SchemaVersion() uint64      { return s.schemaVersion }
func (s ReleaseSnapshot) Digest() string             { return s.digest }
func (s ReleaseSnapshot) MetadataJSON() string       { return s.metadataJSON }

// Entries returns an alias-keyed copy of every release entry.
func (s ReleaseSnapshot) Entries() map[string]ReleaseEntryMetadata {
	return maps.Clone(s.entries)
}

// Entry returns metadata for one stable alias.
func (s ReleaseSnapshot) Entry(alias string) (ReleaseEntryMetadata, bool) {
	e, ok := s.entries[alias]
	return e, ok
}

// Parameters returns an alias-keyed copy of all resolved parameter documents.
func (s ReleaseSnapshot) Parameters() map[string]ReleaseParameter {
	return maps.Clone(s.parameters)
}

// Parameter returns one resolved parameter document.
func (s ReleaseSnapshot) Parameter(alias string) (ReleaseParameter, bool) {
	p, ok := s.parameters[alias]
	return p, ok
}

// Secrets returns an alias-keyed copy of the resolved secret values. Every
// Secret preserves the SDK's redacting formatting behavior; plaintext remains
// available only through Secret.Value/StringValue.
func (s ReleaseSnapshot) Secrets() map[string]Secret {
	out := make(map[string]Secret, len(s.secrets))
	for alias, secret := range s.secrets {
		out[alias] = cloneSecret(secret)
	}
	return out
}

// Secret returns one resolved secret without exposing it through formatting.
func (s ReleaseSnapshot) Secret(alias string) (Secret, bool) {
	secret, ok := s.secrets[alias]
	return cloneSecret(secret), ok
}

func cloneSecret(s Secret) Secret {
	return s.Clone()
}

// String intentionally contains only release identity, never resolved values.
func (s ReleaseSnapshot) String() string {
	return fmt.Sprintf("ReleaseSnapshot{%s/%s version=%d revision=%d digest=%s entries=%d}",
		s.namespace, s.name, s.version, s.activationRevision, s.digest, len(s.entries))
}

// GoString uses the same redacted representation as String.
func (s ReleaseSnapshot) GoString() string { return s.String() }

// Format prevents %+v and %#v from reflecting private secret-bearing fields.
func (s ReleaseSnapshot) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, s.String()) }

// MarshalJSON emits release identity and entry metadata only. Resolved values
// are deliberately excluded so snapshots are safe to attach to diagnostics.
func (s ReleaseSnapshot) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Namespace          string                          `json:"namespace"`
		Name               string                          `json:"name"`
		Version            uint64                          `json:"version"`
		ActivationRevision uint64                          `json:"activation_revision"`
		SchemaID           string                          `json:"schema_id,omitempty"`
		SchemaVersion      uint64                          `json:"schema_version,omitempty"`
		Digest             string                          `json:"digest"`
		Entries            map[string]ReleaseEntryMetadata `json:"entries"`
	}{
		Namespace:          s.namespace,
		Name:               s.name,
		Version:            s.version,
		ActivationRevision: s.activationRevision,
		SchemaID:           s.schemaID,
		SchemaVersion:      s.schemaVersion,
		Digest:             s.digest,
		Entries:            s.Entries(),
	})
}

// ReleaseLoaderStatus is a redacted point-in-time view of loader progress.
type ReleaseLoaderStatus struct {
	State                  string
	ObservedVersion        uint64
	ObservedRevision       uint64
	AppliedVersion         uint64
	AppliedRevision        uint64
	LastFailureCategory    string
	LastFailureAt          time.Time
	LastResolutionDuration time.Duration
	Reconnects             uint64
}

// ReleaseLoaderStats contains bounded counters suitable for metrics export.
// It intentionally contains no aliases, paths, diagnostics, or secret metadata.
type ReleaseLoaderStats struct {
	Candidates uint64
	Applied    uint64
	Rejected   map[string]uint64
	Reconnects uint64
}
