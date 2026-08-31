package configstore

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/Suhaibinator/kms/sdk/go/kmsclient"
)

type logLine map[string]any

func newJSONLogger(level slog.Leveler) (*slog.Logger, *bytes.Buffer) {
	var buffer bytes.Buffer
	return slog.New(slog.NewJSONHandler(&buffer, &slog.HandlerOptions{Level: level})), &buffer
}

func decodeLogLines(t *testing.T, buffer *bytes.Buffer) []logLine {
	t.Helper()
	var lines []logLine
	for _, raw := range strings.Split(strings.TrimSpace(buffer.String()), "\n") {
		if raw == "" {
			continue
		}
		var line logLine
		if err := json.Unmarshal([]byte(raw), &line); err != nil {
			t.Fatalf("log line %q is not JSON: %v", raw, err)
		}
		lines = append(lines, line)
	}
	return lines
}

func messagesOf(lines []logLine) []string {
	out := make([]string, len(lines))
	for i, line := range lines {
		out[i], _ = line["msg"].(string)
	}
	return out
}

func logTestIdentity() ReleaseIdentity {
	return ReleaseIdentity{namespace: "prod/app", name: "runtime", version: 4, activationRevision: 11, digest: "d"}
}

func TestLogSinkSetAndLoggerAreAtomic(t *testing.T) {
	var sink *LogSink
	if sink.Logger() != nil {
		t.Fatal("nil sink returned a logger")
	}
	sink.Set(slog.Default()) // must not panic

	sink = NewLogSink(nil)
	if sink.Logger() != nil {
		t.Fatal("fresh sink has a logger")
	}
	logger, _ := newJSONLogger(slog.LevelInfo)
	sink.Set(logger)
	if sink.Logger() != logger {
		t.Fatal("Set did not install the logger")
	}
	sink.Set(nil)
	if sink.Logger() != nil {
		t.Fatal("Set(nil) did not clear the logger")
	}
	initial, _ := newJSONLogger(slog.LevelInfo)
	if NewLogSink(initial).Logger() != initial {
		t.Fatal("NewLogSink ignored its initial logger")
	}
}

func TestSlogCallbacksLogsDefaultMismatch(t *testing.T) {
	logger, buffer := newJSONLogger(slog.LevelInfo)
	callbacks := SlogCallbacks(NewLogSink(logger), SlogOptions{Component: "api"})
	callbacks.OnDefaultMismatch(newDefaultMismatchReport(PhaseStartup, MismatchError, logTestIdentity(), []FieldDifference{
		{Path: "limits.rate", Expected: 10, Actual: 20},
		{Path: "limits.token", Expected: kmsclient.NewSecret([]byte("plaintext-canary")), Actual: "x"},
	}))
	lines := decodeLogLines(t, buffer)
	if len(lines) != 1 {
		t.Fatalf("lines = %v", lines)
	}
	line := lines[0]
	if line["level"] != "ERROR" || line["msg"] != "kms config diverges from source defaults" ||
		line["component"] != "api" || line["phase"] != "startup" || line["release"] != "prod/app/runtime@4#11" {
		t.Fatalf("line = %v", line)
	}
	fields, ok := line["fields"].([]any)
	if !ok || len(fields) != 2 {
		t.Fatalf("fields = %#v", line["fields"])
	}
	first := fields[0].(map[string]any)
	if first["path"] != "limits.rate" || first["expected"] != float64(10) || first["actual"] != float64(20) {
		t.Fatalf("first field = %v", first)
	}
	if strings.Contains(buffer.String(), "plaintext-canary") || !strings.Contains(buffer.String(), "[REDACTED]") {
		t.Fatalf("secret leaked through mismatch log: %s", buffer.String())
	}
}

func TestSlogCallbacksLogsStartupAppliedWithSortedGroupSnapshot(t *testing.T) {
	logger, buffer := newJSONLogger(slog.LevelInfo)
	callbacks := SlogCallbacks(NewLogSink(logger), SlogOptions{})
	groups := map[string]jsontext.Value{
		"limits":   jsontext.Value(`{"rate":10}`),
		"database": jsontext.Value(`{"host":"db","port":5432}`),
		"empty":    nil,
	}
	callbacks.OnApplied(newAppliedReport(PhaseStartup, logTestIdentity(), true,
		[]FieldChange{{Path: "ignored.at.startup", Previous: 1, Current: 2}},
		func() (map[string]jsontext.Value, error) { return groups, nil }))

	lines := decodeLogLines(t, buffer)
	want := []string{"kms config applied", "kms config group", "kms config group", "kms config group"}
	if got := messagesOf(lines); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("messages = %v, want %v", got, want)
	}
	applied := lines[0]
	if applied["level"] != "INFO" || applied["component"] != "configstore" || applied["phase"] != "startup" ||
		applied["release"] != "prod/app/runtime@4#11" || applied["release_version"] != float64(4) ||
		applied["activation_revision"] != float64(11) || applied["default_divergent"] != true {
		t.Fatalf("applied line = %v", applied)
	}
	if _, present := applied["changed_count"]; present {
		t.Fatalf("startup applied line carries reload attributes: %v", applied)
	}
	aliases := []string{lines[1]["alias"].(string), lines[2]["alias"].(string), lines[3]["alias"].(string)}
	if strings.Join(aliases, ",") != "database,empty,limits" {
		t.Fatalf("group aliases = %v, want sorted", aliases)
	}
	database := lines[1]["values"].(map[string]any)
	if database["host"] != "db" || database["port"] != float64(5432) || lines[1]["release_version"] != float64(4) || lines[1]["activation_revision"] != float64(11) {
		t.Fatalf("database group line = %v", lines[1])
	}
	if lines[2]["values"] != nil {
		t.Fatalf("empty group values = %#v, want null", lines[2]["values"])
	}
	if strings.Contains(buffer.String(), "ignored.at.startup") {
		t.Fatal("startup applied log reported changes")
	}
}

func TestSlogCallbacksStartupSnapshotCanBeDisabledAndReportsGroupErrors(t *testing.T) {
	logger, buffer := newJSONLogger(slog.LevelInfo)
	callbacks := SlogCallbacks(NewLogSink(logger), SlogOptions{DisableStartupSnapshot: true})
	callbacks.OnApplied(newAppliedReport(PhaseStartup, logTestIdentity(), false, nil,
		func() (map[string]jsontext.Value, error) {
			t.Fatal("Groups called with snapshot disabled")
			return nil, nil
		}))
	if got := messagesOf(decodeLogLines(t, buffer)); strings.Join(got, "|") != "kms config applied" {
		t.Fatalf("messages = %v", got)
	}

	logger, buffer = newJSONLogger(slog.LevelInfo)
	callbacks = SlogCallbacks(NewLogSink(logger), SlogOptions{})
	callbacks.OnApplied(newAppliedReport(PhaseStartup, logTestIdentity(), false, nil,
		func() (map[string]jsontext.Value, error) { return nil, errors.New("encode failed") }))
	lines := decodeLogLines(t, buffer)
	if got := messagesOf(lines); strings.Join(got, "|") != "kms config applied|kms config groups unavailable" {
		t.Fatalf("messages = %v", got)
	}
	if lines[1]["level"] != "ERROR" || lines[1]["error"] != "encode failed" {
		t.Fatalf("groups error line = %v", lines[1])
	}
}

func TestSlogCallbacksLogsRuntimeReloadWithFieldChanges(t *testing.T) {
	logger, buffer := newJSONLogger(slog.LevelInfo)
	callbacks := SlogCallbacks(NewLogSink(logger), SlogOptions{})
	callbacks.OnApplied(newAppliedReport(PhaseRuntime, logTestIdentity(), false, []FieldChange{
		{Path: "limits.rate", Previous: 10, Current: 20},
		{Path: "database.password", Previous: nil, Current: nil},
	}, func() (map[string]jsontext.Value, error) {
		return map[string]jsontext.Value{"limits": jsontext.Value(`{"rate":20}`)}, nil
	}))

	lines := decodeLogLines(t, buffer)
	if got := messagesOf(lines); strings.Join(got, "|") != "kms config reloaded|kms config field changed|kms config field changed|kms config group" {
		t.Fatalf("messages = %v", got)
	}
	reloaded := lines[0]
	if reloaded["level"] != "INFO" || reloaded["release"] != "prod/app/runtime@4#11" || reloaded["default_divergent"] != false || reloaded["changed_count"] != float64(2) {
		t.Fatalf("reloaded line = %v", reloaded)
	}
	if lines[1]["path"] != "limits.rate" || lines[1]["previous"] != float64(10) || lines[1]["current"] != float64(20) {
		t.Fatalf("field line = %v", lines[1])
	}
	if lines[2]["path"] != "database.password" || lines[2]["previous"] != nil || lines[2]["current"] != nil {
		t.Fatalf("secret rotation line = %v", lines[2])
	}

	logger, buffer = newJSONLogger(slog.LevelInfo)
	callbacks = SlogCallbacks(NewLogSink(logger), SlogOptions{DisableReloadChanges: true})
	callbacks.OnApplied(newAppliedReport(PhaseRuntime, logTestIdentity(), true, []FieldChange{{Path: "limits.rate", Previous: 10, Current: 20}}, nil))
	lines = decodeLogLines(t, buffer)
	if got := messagesOf(lines); strings.Join(got, "|") != "kms config reloaded" || lines[0]["changed_count"] != float64(1) {
		t.Fatalf("messages = %v lines = %v", got, lines)
	}
}

func TestSlogCallbacksLogsCandidateRejected(t *testing.T) {
	logger, buffer := newJSONLogger(slog.LevelInfo)
	callbacks := SlogCallbacks(NewLogSink(logger), SlogOptions{Component: "worker"})
	callbacks.OnCandidateRejected(newCandidateRejectionReport(RejectRestartRequired, logTestIdentity(), []string{"database.endpoint", "limits.rate"}))
	lines := decodeLogLines(t, buffer)
	if len(lines) != 1 {
		t.Fatalf("lines = %v", lines)
	}
	line := lines[0]
	paths, _ := line["paths"].([]any)
	if line["level"] != "ERROR" || line["msg"] != "kms config candidate rejected" || line["component"] != "worker" ||
		line["category"] != "restart_required" || line["release"] != "prod/app/runtime@4#11" || len(paths) != 2 || paths[0] != "database.endpoint" {
		t.Fatalf("line = %v", line)
	}
}

func TestSlogCallbacksBuffersStartupRecordsUntilLoggerIsSet(t *testing.T) {
	sink := NewLogSink(nil)
	callbacks := SlogCallbacks(sink, SlogOptions{})
	callbacks.OnCandidateRejected(newCandidateRejectionReport(RejectConfigDecodeFailed, testIdentity(1, 1), []string{"limits"}))
	callbacks.OnDefaultMismatch(newDefaultMismatchReport(PhaseStartup, MismatchError, logTestIdentity(), []FieldDifference{{Path: "limits.rate", Expected: 1, Actual: 2}}))
	callbacks.OnApplied(newAppliedReport(PhaseStartup, logTestIdentity(), true, nil,
		func() (map[string]jsontext.Value, error) {
			return map[string]jsontext.Value{"limits": jsontext.Value(`{"rate":2}`)}, nil
		}))
	// Runtime records without a logger are dropped, not buffered.
	callbacks.OnDefaultMismatch(newDefaultMismatchReport(PhaseRuntime, MismatchError, testIdentity(5, 5), []FieldDifference{{Path: "limits.rate", Expected: 1, Actual: 3}}))
	callbacks.OnApplied(newAppliedReport(PhaseRuntime, testIdentity(5, 5), true, []FieldChange{{Path: "limits.rate", Previous: 2, Current: 3}}, nil))
	callbacks.OnCandidateRejected(newCandidateRejectionReport(RejectRestartRequired, testIdentity(6, 6), nil))

	logger, buffer := newJSONLogger(slog.LevelInfo)
	sink.Set(logger)
	lines := decodeLogLines(t, buffer)
	want := "kms config candidate rejected|kms config diverges from source defaults|kms config applied|kms config group"
	if got := messagesOf(lines); strings.Join(got, "|") != want {
		t.Fatalf("flushed messages = %v, want %s", got, want)
	}
	if lines[0]["category"] != "config_decode_failed" || lines[1]["phase"] != "startup" || lines[3]["alias"] != "limits" {
		t.Fatalf("flushed lines = %v", lines)
	}
	if strings.Contains(buffer.String(), "restart_required") || strings.Contains(buffer.String(), "reloaded") {
		t.Fatalf("runtime records were buffered: %s", buffer.String())
	}

	// After the flush, events go straight to the logger and nothing replays
	// on a later Set.
	buffer.Reset()
	callbacks.OnApplied(newAppliedReport(PhaseRuntime, testIdentity(7, 7), false, nil, nil))
	if got := messagesOf(decodeLogLines(t, buffer)); strings.Join(got, "|") != "kms config reloaded" {
		t.Fatalf("direct messages = %v", got)
	}
	replacement, replacementBuffer := newJSONLogger(slog.LevelInfo)
	sink.Set(replacement)
	if replacementBuffer.Len() != 0 {
		t.Fatalf("second Set replayed records: %s", replacementBuffer.String())
	}
	if sink.Logger() != replacement {
		t.Fatal("second Set did not install the replacement logger")
	}
}

func TestLogSinkBufferIsBoundedAndHonoursHandlerLevel(t *testing.T) {
	sink := NewLogSink(nil)
	callbacks := SlogCallbacks(sink, SlogOptions{})
	for i := range logSinkBufferLimit + 10 {
		callbacks.OnCandidateRejected(newCandidateRejectionReport(RejectInternal, testIdentity(uint64(i), uint64(i)), nil))
	}
	// A startup applied record arrives after the buffer is already full and
	// is dropped as well.
	callbacks.OnApplied(newAppliedReport(PhaseStartup, logTestIdentity(), false, nil, nil))

	logger, buffer := newJSONLogger(slog.LevelInfo)
	sink.Set(logger)
	lines := decodeLogLines(t, buffer)
	if len(lines) != logSinkBufferLimit+1 {
		t.Fatalf("flushed %d lines, want %d buffered + 1 drop notice", len(lines), logSinkBufferLimit+1)
	}
	last := lines[len(lines)-1]
	if last["level"] != "WARN" || last["msg"] != "kms config startup log records dropped" || last["dropped"] != float64(11) {
		t.Fatalf("drop notice = %v", last)
	}
	if lines[0]["release"] != "prod/app/runtime@0#0" || lines[logSinkBufferLimit-1]["release"] != fmt.Sprintf("prod/app/runtime@%d#%d", logSinkBufferLimit-1, logSinkBufferLimit-1) {
		t.Fatalf("buffered records lost their order: first=%v last=%v", lines[0], lines[logSinkBufferLimit-1])
	}

	// Buffered records respect the level of the logger they are flushed to.
	sink = NewLogSink(nil)
	callbacks = SlogCallbacks(sink, SlogOptions{})
	callbacks.OnApplied(newAppliedReport(PhaseStartup, logTestIdentity(), false, nil, nil))
	callbacks.OnDefaultMismatch(newDefaultMismatchReport(PhaseStartup, MismatchError, logTestIdentity(), nil))
	logger, buffer = newJSONLogger(slog.LevelError)
	sink.Set(logger)
	if got := messagesOf(decodeLogLines(t, buffer)); strings.Join(got, "|") != "kms config diverges from source defaults" {
		t.Fatalf("level-filtered flush = %v", got)
	}
}

func TestSlogCallbacksAttributeKeysNeverLookSecret(t *testing.T) {
	logger, buffer := newJSONLogger(slog.LevelDebug)
	callbacks := SlogCallbacks(NewLogSink(logger), SlogOptions{})
	callbacks.OnDefaultMismatch(newDefaultMismatchReport(PhaseStartup, MismatchError, logTestIdentity(), []FieldDifference{{Path: "a.b", Expected: 1, Actual: 2}}))
	callbacks.OnApplied(newAppliedReport(PhaseStartup, logTestIdentity(), true, nil,
		func() (map[string]jsontext.Value, error) {
			return map[string]jsontext.Value{"a": jsontext.Value(`{}`)}, nil
		}))
	callbacks.OnApplied(newAppliedReport(PhaseStartup, logTestIdentity(), true, nil,
		func() (map[string]jsontext.Value, error) { return nil, errors.New("boom") }))
	callbacks.OnApplied(newAppliedReport(PhaseRuntime, logTestIdentity(), false, []FieldChange{{Path: "a.b", Previous: 1, Current: 2}}, nil))
	callbacks.OnCandidateRejected(newCandidateRejectionReport(RejectInternal, logTestIdentity(), []string{"a"}))
	callbacks.OnDefaultMismatch(nil)
	callbacks.OnApplied(nil)
	callbacks.OnCandidateRejected(nil)

	lines := decodeLogLines(t, buffer)
	if len(lines) != 8 {
		t.Fatalf("lines = %d: %v", len(lines), messagesOf(lines))
	}
	for _, line := range lines {
		for key := range line {
			lower := strings.ToLower(key)
			for _, forbidden := range []string{"secret", "token", "password", "api_key"} {
				if strings.Contains(lower, forbidden) {
					t.Fatalf("attribute key %q looks secret in %v", key, line)
				}
			}
		}
		if line["component"] != "configstore" {
			t.Fatalf("line without component: %v", line)
		}
	}
}

func TestSlogCallbacksToleratesNilSink(t *testing.T) {
	callbacks := SlogCallbacks(nil, SlogOptions{})
	callbacks.OnApplied(newAppliedReport(PhaseStartup, logTestIdentity(), false, nil, nil))
	callbacks.OnDefaultMismatch(newDefaultMismatchReport(PhaseRuntime, MismatchError, logTestIdentity(), nil))
	callbacks.OnCandidateRejected(newCandidateRejectionReport(RejectInternal, logTestIdentity(), nil))
}

func TestSlogCallbacksLogsReloadSnapshotUnlessDisabled(t *testing.T) {
	groups := map[string]jsontext.Value{
		"limits":   jsontext.Value(`{"rate":20}`),
		"database": jsontext.Value(`{"host":"db","port":5432}`),
	}
	report := func() AppliedReport {
		return newAppliedReport(PhaseRuntime, testIdentity(5, 12), true,
			[]FieldChange{{Path: "limits.rate", Previous: 10, Current: 20}},
			func() (map[string]jsontext.Value, error) { return groups, nil })
	}

	logger, buffer := newJSONLogger(slog.LevelInfo)
	SlogCallbacks(NewLogSink(logger), SlogOptions{}).OnApplied(report())
	lines := decodeLogLines(t, buffer)
	want := []string{"kms config reloaded", "kms config field changed", "kms config group", "kms config group"}
	if got := messagesOf(lines); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("messages = %v, want %v", got, want)
	}
	// Groups are sorted by alias and carry the generation identity so a log
	// line's activation_revision resolves to a complete configuration.
	if lines[2]["alias"] != "database" || lines[3]["alias"] != "limits" ||
		lines[3]["activation_revision"] != float64(12) || lines[3]["release_version"] != float64(5) {
		t.Fatalf("group records = %v %v", lines[2], lines[3])
	}
	if values, ok := lines[3]["values"].(map[string]any); !ok || values["rate"] != float64(20) {
		t.Fatalf("group values = %v", lines[3]["values"])
	}

	logger, buffer = newJSONLogger(slog.LevelInfo)
	SlogCallbacks(NewLogSink(logger), SlogOptions{DisableReloadSnapshot: true}).OnApplied(report())
	if got := messagesOf(decodeLogLines(t, buffer)); strings.Join(got, "|") != "kms config reloaded|kms config field changed" {
		t.Fatalf("messages with reload snapshot disabled = %v", got)
	}

	logger, buffer = newJSONLogger(slog.LevelInfo)
	SlogCallbacks(NewLogSink(logger), SlogOptions{DisableReloadChanges: true}).OnApplied(report())
	if got := messagesOf(decodeLogLines(t, buffer)); strings.Join(got, "|") != "kms config reloaded|kms config group|kms config group" {
		t.Fatalf("messages with reload changes disabled = %v", got)
	}
}
