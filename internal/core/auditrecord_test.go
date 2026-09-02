package core

import (
	"bytes"
	"encoding/json/jsontext"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Suhaibinator/kms/internal/domain"
)

// goldenAuditEvent is fully populated so every field of the canonical record
// has a distinguishable value in the golden bytes below.
func goldenAuditEvent() domain.AuditEvent {
	return domain.AuditEvent{
		ID:                  42,
		EventType:           "secret.read",
		ActorIdentity:       "gradethis-be",
		ActorType:           "client",
		ResourceType:        "secret",
		ResourceNamespaceID: 7,
		ResourceEnv:         "prod",
		ResourceApp:         "gradethis",
		ResourceKey:         "stripe-api-key",
		ResourceVersion:     2,
		Decision:            "allow",
		SourceIP:            "10.0.0.5",
		UserAgent:           "kms-cli/1.0",
		RequestID:           "r-123",
		CreatedAt:           time.Date(2026, 7, 1, 12, 34, 56, 123456789, time.UTC),
		Metadata:            `{"label":"current"}`,
	}
}

// TestWriteAuditJSONLGolden pins the exact bytes of the shared export format.
// `audit export`, `audit list -o json`, and the server-side archive all emit
// it, so a change here breaks every consumer and must be deliberate.
func TestWriteAuditJSONLGolden(t *testing.T) {
	records := []AuditRecord{
		AuditRecordFrom(goldenAuditEvent()),
		// Unparseable metadata is demoted to a JSON string; every other field
		// is empty, so this line also pins the zero-value rendering.
		AuditRecordFrom(domain.AuditEvent{ID: 43, EventType: "auth.failure", Decision: "deny", Metadata: "not json"}),
		// Empty metadata is not valid JSON either and takes the same path.
		AuditRecordFrom(domain.AuditEvent{ID: 44}),
	}

	want := strings.Join([]string{
		`{"id":42,"created_at":"2026-07-01T12:34:56.123456789Z","event":"secret.read","decision":"allow",` +
			`"actor":{"identity":"gradethis-be","type":"client"},` +
			`"resource":{"type":"secret","namespace_id":7,"env":"prod","app":"gradethis","key":"stripe-api-key","version":2},` +
			`"source_ip":"10.0.0.5","user_agent":"kms-cli/1.0","request_id":"r-123","metadata":{"label":"current"}}`,
		`{"id":43,"created_at":"0001-01-01T00:00:00Z","event":"auth.failure","decision":"deny",` +
			`"actor":{"identity":"","type":""},` +
			`"resource":{"type":"","namespace_id":0,"env":"","app":"","key":"","version":0},` +
			`"source_ip":"","user_agent":"","request_id":"","metadata":"not json"}`,
		`{"id":44,"created_at":"0001-01-01T00:00:00Z","event":"","decision":"",` +
			`"actor":{"identity":"","type":""},` +
			`"resource":{"type":"","namespace_id":0,"env":"","app":"","key":"","version":0},` +
			`"source_ip":"","user_agent":"","request_id":"","metadata":""}`,
		"",
	}, "\n")

	var buf bytes.Buffer
	if err := WriteAuditJSONL(&buf, records); err != nil {
		t.Fatalf("WriteAuditJSONL: %v", err)
	}
	if buf.String() != want {
		t.Fatalf("JSONL mismatch\n got: %q\nwant: %q", buf.String(), want)
	}
}

func TestAuditRecordFromProjectsEveryField(t *testing.T) {
	got := AuditRecordFrom(goldenAuditEvent())
	want := AuditRecord{
		ID:        42,
		CreatedAt: time.Date(2026, 7, 1, 12, 34, 56, 123456789, time.UTC),
		Event:     "secret.read",
		Decision:  "allow",
		Actor:     AuditActor{Identity: "gradethis-be", Type: "client"},
		Resource: AuditResource{
			Type: "secret", NamespaceID: 7, Env: "prod", App: "gradethis",
			Key: "stripe-api-key", Version: 2,
		},
		SourceIP:  "10.0.0.5",
		UserAgent: "kms-cli/1.0",
		RequestID: "r-123",
		Metadata:  jsontext.Value(`{"label":"current"}`),
	}
	if !got.CreatedAt.Equal(want.CreatedAt) {
		t.Errorf("CreatedAt = %v, want %v", got.CreatedAt, want.CreatedAt)
	}
	if string(got.Metadata) != string(want.Metadata) {
		t.Errorf("Metadata = %q, want %q", got.Metadata, want.Metadata)
	}
	if got.ID != want.ID || got.Event != want.Event || got.Decision != want.Decision {
		t.Errorf("id/event/decision = %d/%q/%q, want %d/%q/%q", got.ID, got.Event, got.Decision, want.ID, want.Event, want.Decision)
	}
	if got.Actor != want.Actor {
		t.Errorf("actor = %+v, want %+v", got.Actor, want.Actor)
	}
	if got.Resource != want.Resource {
		t.Errorf("resource = %+v, want %+v", got.Resource, want.Resource)
	}
	if got.SourceIP != want.SourceIP || got.UserAgent != want.UserAgent || got.RequestID != want.RequestID {
		t.Errorf("source_ip/user_agent/request_id = %q/%q/%q, want %q/%q/%q",
			got.SourceIP, got.UserAgent, got.RequestID, want.SourceIP, want.UserAgent, want.RequestID)
	}
}

// TestAuditRecordFromNormalizesToUTC keeps the wall-clock instant stable no
// matter which zone the event carries; the format has exactly one spelling per
// instant.
func TestAuditRecordFromNormalizesToUTC(t *testing.T) {
	zone := time.FixedZone("UTC+5", 5*60*60)
	ev := domain.AuditEvent{ID: 1, CreatedAt: time.Date(2026, 7, 1, 17, 34, 56, 0, zone), Metadata: "{}"}

	var buf bytes.Buffer
	if err := WriteAuditJSONL(&buf, []AuditRecord{AuditRecordFrom(ev)}); err != nil {
		t.Fatalf("WriteAuditJSONL: %v", err)
	}
	if !strings.Contains(buf.String(), `"created_at":"2026-07-01T12:34:56Z"`) {
		t.Fatalf("timestamp not normalized to UTC: %s", buf.String())
	}
}

// TestWriteAuditJSONLDoesNotEscapeHTML keeps the payload byte-comparable with
// the metadata as stored; escaping would silently rewrite it.
func TestWriteAuditJSONLDoesNotEscapeHTML(t *testing.T) {
	ev := domain.AuditEvent{ID: 1, UserAgent: "<script>&'\"", Metadata: `{"note":"a<b&c"}`}

	var buf bytes.Buffer
	if err := WriteAuditJSONL(&buf, []AuditRecord{AuditRecordFrom(ev)}); err != nil {
		t.Fatalf("WriteAuditJSONL: %v", err)
	}
	for _, want := range []string{`"user_agent":"<script>&'\""`, `"metadata":{"note":"a<b&c"}`} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("output missing %s: %s", want, buf.String())
		}
	}
}

// TestWriteAuditJSONLReplacesInvalidUTF8: an attacker-controlled header must
// not be able to truncate an export mid-line.
func TestWriteAuditJSONLReplacesInvalidUTF8(t *testing.T) {
	ev := domain.AuditEvent{ID: 1, UserAgent: "bad\xffagent", Metadata: "raw\xffmeta"}

	var buf bytes.Buffer
	if err := WriteAuditJSONL(&buf, []AuditRecord{AuditRecordFrom(ev)}); err != nil {
		t.Fatalf("WriteAuditJSONL: %v", err)
	}
	line := buf.String()
	if !strings.HasSuffix(line, "}\n") {
		t.Fatalf("record was truncated: %q", line)
	}
	if !strings.Contains(line, `"user_agent":"bad�agent"`) || !strings.Contains(line, `"metadata":"raw�meta"`) {
		t.Fatalf("invalid UTF-8 not replaced: %q", line)
	}
}

// TestWriteAuditJSONLCompactsMetadata: the row's metadata is the same JSON
// value, re-emitted on one line so a record is always exactly one line.
func TestWriteAuditJSONLCompactsMetadata(t *testing.T) {
	ev := domain.AuditEvent{ID: 1, Metadata: "{\n  \"a\" : [1, 2]\n}"}

	var buf bytes.Buffer
	if err := WriteAuditJSONL(&buf, []AuditRecord{AuditRecordFrom(ev)}); err != nil {
		t.Fatalf("WriteAuditJSONL: %v", err)
	}
	if got := strings.Count(buf.String(), "\n"); got != 1 {
		t.Fatalf("record spans %d lines: %q", got, buf.String())
	}
	if !strings.Contains(buf.String(), `"metadata":{"a":[1,2]}`) {
		t.Fatalf("metadata not compacted: %s", buf.String())
	}
}

func TestWriteAuditJSONLEmptyInputWritesNothing(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteAuditJSONL(&buf, nil); err != nil {
		t.Fatalf("WriteAuditJSONL: %v", err)
	}
	if buf.Len() != 0 {
		t.Fatalf("wrote %q for no records", buf.String())
	}
}

type failingWriter struct{ err error }

func (w failingWriter) Write([]byte) (int, error) { return 0, w.err }

func TestWriteAuditJSONLPropagatesWriteError(t *testing.T) {
	sentinel := errors.New("disk full")
	err := WriteAuditJSONL(failingWriter{err: sentinel}, []AuditRecord{AuditRecordFrom(domain.AuditEvent{ID: 9})})
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want it to wrap %v", err, sentinel)
	}
	if !strings.Contains(err.Error(), "audit record 9") {
		t.Fatalf("err = %v, want it to name the failing record", err)
	}
}
