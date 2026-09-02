package core

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"io"
	"time"

	"github.com/Suhaibinator/kms/internal/domain"
)

// AuditActor identifies who performed an audited operation.
type AuditActor struct {
	Identity string `json:"identity"`
	Type     string `json:"type"`
}

// AuditResource is the audited resource's denormalized address. It is captured
// at audit time, so it stays readable after the namespace is deleted.
type AuditResource struct {
	Type        string `json:"type"`
	NamespaceID int64  `json:"namespace_id"`
	Env         string `json:"env"`
	App         string `json:"app"`
	Key         string `json:"key"`
	Version     uint64 `json:"version"`
}

// AuditRecord is the canonical external shape of one audit event. It is the
// single format shared by `audit export`, `audit list -o json`, and the
// server-side archive, so an operator can concatenate all three and feed the
// result to one parser. Field order and spelling are part of that contract:
// change them only alongside the consumers.
type AuditRecord struct {
	ID int64 `json:"id"`
	// CreatedAt renders as an RFC 3339 UTC timestamp with nanosecond
	// precision (trailing zeros in the fraction are omitted).
	CreatedAt time.Time     `json:"created_at"`
	Event     string        `json:"event"`
	Decision  string        `json:"decision"`
	Actor     AuditActor    `json:"actor"`
	Resource  AuditResource `json:"resource"`
	SourceIP  string        `json:"source_ip"`
	UserAgent string        `json:"user_agent"`
	RequestID string        `json:"request_id"`
	// Metadata is the row's metadata verbatim when it holds valid JSON (the
	// same value, re-emitted compactly), and a JSON string carrying the raw
	// text otherwise.
	Metadata jsontext.Value `json:"metadata"`
}

// AuditRecordFrom projects a stored event onto the canonical record.
func AuditRecordFrom(ev domain.AuditEvent) AuditRecord {
	return AuditRecord{
		ID:        ev.ID,
		CreatedAt: ev.CreatedAt.UTC(),
		Event:     ev.EventType,
		Decision:  ev.Decision,
		Actor:     AuditActor{Identity: ev.ActorIdentity, Type: ev.ActorType},
		Resource: AuditResource{
			Type:        ev.ResourceType,
			NamespaceID: ev.ResourceNamespaceID,
			Env:         ev.ResourceEnv,
			App:         ev.ResourceApp,
			Key:         ev.ResourceKey,
			Version:     ev.ResourceVersion,
		},
		SourceIP:  ev.SourceIP,
		UserAgent: ev.UserAgent,
		RequestID: ev.RequestID,
		Metadata:  AuditMetadataValue(ev.Metadata),
	}
}

// AuditMetadataValue keeps well-formed metadata verbatim and demotes anything
// else — including the empty string — to a JSON string holding the raw text, so
// one malformed row can never make a whole export unparseable. It is exported
// because the CLI builds the same record from a wire event, which carries the
// identical raw metadata string and must render it identically.
func AuditMetadataValue(raw string) jsontext.Value {
	if value := jsontext.Value(raw); value.IsValid() {
		return value
	}
	quoted, err := json.Marshal(raw, jsontext.AllowInvalidUTF8(true))
	if err != nil {
		return jsontext.Value(`""`)
	}
	return jsontext.Value(quoted)
}

// WriteAuditJSONL writes records as JSON Lines: one compact object per record,
// each terminated by "\n", with fields in AuditRecord's declaration order and
// no HTML escaping. Invalid UTF-8 anywhere in a record is written as the
// Unicode replacement character rather than aborting the write, because a
// hostile user agent or source address must not be able to truncate an export.
func WriteAuditJSONL(w io.Writer, records []AuditRecord) error {
	enc := jsontext.NewEncoder(w,
		jsontext.EscapeForHTML(false),
		jsontext.AllowInvalidUTF8(true),
	)
	for _, record := range records {
		if err := json.MarshalEncode(enc, record); err != nil {
			return fmt.Errorf("encode audit record %d: %w", record.ID, err)
		}
	}
	return nil
}
