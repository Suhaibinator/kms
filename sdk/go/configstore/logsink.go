package configstore

import (
	"context"
	"encoding/json"
	"log/slog"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// logSinkBufferLimit bounds how many startup records a LogSink retains while
// no logger is installed. Records beyond the limit are dropped and counted;
// the count is reported when the buffer is flushed.
const logSinkBufferLimit = 4096

// LogSink is a swappable *slog.Logger for SlogCallbacks. Applications often
// build their logger from the configuration that configstore is loading, so
// the logger may not exist when the initial generation is applied. A sink
// created with a nil logger buffers startup records (the initial mismatch
// report, the applied notice and its group snapshot, and any candidate
// rejected before the first generation) and replays them in order on the
// first Set. Runtime records emitted while no logger is installed are
// dropped. All methods are safe for concurrent use.
type LogSink struct {
	logger atomic.Pointer[slog.Logger]

	mu       sync.Mutex
	buffered []slog.Record
	dropped  int
}

// NewLogSink returns a sink that writes to initial, which may be nil.
func NewLogSink(initial *slog.Logger) *LogSink {
	sink := &LogSink{}
	sink.logger.Store(initial)
	return sink
}

// Set installs logger for all subsequent records. The first non-nil logger
// also receives, in order, every startup record buffered while no logger
// was installed. Setting nil removes the logger; later startup records are
// buffered again.
func (s *LogSink) Set(logger *slog.Logger) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.logger.Store(logger)
	if logger == nil || (len(s.buffered) == 0 && s.dropped == 0) {
		return
	}
	buffered, dropped := s.buffered, s.dropped
	s.buffered, s.dropped = nil, 0
	handler := logger.Handler()
	ctx := context.Background()
	for i := range buffered {
		if handler.Enabled(ctx, buffered[i].Level) {
			_ = handler.Handle(ctx, buffered[i])
		}
	}
	if dropped > 0 {
		logger.LogAttrs(ctx, slog.LevelWarn, "kms config startup log records dropped",
			slog.Int("dropped", dropped), slog.Int("limit", logSinkBufferLimit))
	}
}

// Logger returns the installed logger, or nil when none is installed.
func (s *LogSink) Logger() *slog.Logger {
	if s == nil {
		return nil
	}
	return s.logger.Load()
}

// emit logs one record through the installed logger. Without a logger a
// startup record is buffered (bounded) and a runtime record is dropped.
func (s *LogSink) emit(startup bool, level slog.Level, message string, attrs ...slog.Attr) {
	if s == nil {
		return
	}
	s.mu.Lock()
	logger := s.logger.Load()
	if logger == nil {
		defer s.mu.Unlock()
		if !startup {
			return
		}
		if len(s.buffered) >= logSinkBufferLimit {
			s.dropped++
			return
		}
		record := slog.NewRecord(time.Now(), level, message, 0)
		record.AddAttrs(attrs...)
		s.buffered = append(s.buffered, record)
		return
	}
	// Release the lock before running application handler code so a handler
	// that itself calls Set (or logs through the sink) cannot deadlock.
	s.mu.Unlock()
	logger.LogAttrs(context.Background(), level, message, attrs...)
}

// SlogOptions tunes SlogCallbacks. The zero value logs everything.
type SlogOptions struct {
	// Component is added as the "component" attribute on every record. Empty
	// defaults to "configstore".
	Component string
	// DisableStartupSnapshot suppresses the one "kms config group" record per
	// parameter group that otherwise follows the initial "kms config applied"
	// record. The snapshot contains canonical non-secret values.
	DisableStartupSnapshot bool
	// DisableReloadChanges suppresses the one "kms config field changed"
	// record per changed field that otherwise follows each "kms config
	// reloaded" record. Field records carry previous and current non-secret
	// values.
	DisableReloadChanges bool
	// DisableReloadSnapshot suppresses the one "kms config group" record per
	// parameter group after each reloaded generation. By default every applied
	// generation is dumped in full so any log line can be correlated with the
	// complete configuration in effect at that activation revision.
	DisableReloadSnapshot bool
}

// Attribute keys used by SlogCallbacks. None of them name secret material,
// so log pipelines that redact by key never hide these records.
const (
	logKeyComponent          = "component"
	logKeyPhase              = "phase"
	logKeyRelease            = "release"
	logKeyReleaseVersion     = "release_version"
	logKeyActivationRevision = "activation_revision"
	logKeyDefaultDivergent   = "default_divergent"
	logKeyFields             = "fields"
	logKeyAlias              = "alias"
	logKeyValues             = "values"
	logKeyChangedCount       = "changed_count"
	logKeyPath               = "path"
	logKeyPrevious           = "previous"
	logKeyCurrent            = "current"
	logKeyCategory           = "category"
	logKeyPaths              = "paths"
	logKeyError              = "error"
)

// SlogCallbacks returns Callbacks that log every managed-store event through
// sink using log/slog:
//
//   - OnDefaultMismatch: ERROR "kms config diverges from source defaults"
//     with component, phase, release and fields ([]FieldDifference).
//   - OnApplied at startup: INFO "kms config applied" with component, phase,
//     release, release_version, activation_revision and default_divergent,
//     followed (unless DisableStartupSnapshot) by INFO "kms config group" per
//     alias in sorted order with alias, values (json.RawMessage),
//     release_version and activation_revision. A Groups failure is logged
//     as ERROR "kms config groups unavailable".
//   - OnApplied at runtime: INFO "kms config reloaded" with component,
//     release, default_divergent and changed_count, followed (unless
//     DisableReloadChanges) by INFO "kms config field changed" per field
//     with path, previous and current, then (unless DisableReloadSnapshot) by
//     the same INFO "kms config group" records as at startup, so every
//     generation's full configuration is in the log.
//   - OnCandidateRejected: ERROR "kms config candidate rejected" with
//     component, category, release and paths.
//
// Values in these records are the report values, which the manager has
// already redacted of kmsclient.Secret content. Records emitted before the
// first generation is applied are buffered by the sink while it has no
// logger; see LogSink.
func SlogCallbacks(sink *LogSink, opts SlogOptions) Callbacks {
	if sink == nil {
		sink = NewLogSink(nil)
	}
	component := opts.Component
	if component == "" {
		component = "configstore"
	}
	componentAttr := slog.String(logKeyComponent, component)
	// Rejection reports carry no phase; anything rejected before the first
	// applied generation is a startup event for buffering purposes.
	var applied atomic.Bool

	return Callbacks{
		OnDefaultMismatch: func(report DefaultMismatchReport) {
			if report == nil {
				return
			}
			sink.emit(report.Phase() == PhaseStartup, slog.LevelError, "kms config diverges from source defaults",
				componentAttr,
				slog.String(logKeyPhase, string(report.Phase())),
				slog.String(logKeyRelease, report.Release().String()),
				slog.Any(logKeyFields, report.Fields()),
			)
		},
		OnApplied: func(report AppliedReport) {
			if report == nil {
				return
			}
			release := report.Release()
			if report.Phase() == PhaseStartup {
				applied.Store(true)
				sink.emit(true, slog.LevelInfo, "kms config applied",
					componentAttr,
					slog.String(logKeyPhase, string(report.Phase())),
					slog.String(logKeyRelease, release.String()),
					slog.Uint64(logKeyReleaseVersion, release.Version()),
					slog.Uint64(logKeyActivationRevision, release.ActivationRevision()),
					slog.Bool(logKeyDefaultDivergent, report.DefaultDivergent()),
				)
				if !opts.DisableStartupSnapshot {
					logSnapshot(sink, true, componentAttr, release, report)
				}
				return
			}
			applied.Store(true)
			changes := report.Changed()
			sink.emit(false, slog.LevelInfo, "kms config reloaded",
				componentAttr,
				slog.String(logKeyRelease, release.String()),
				slog.Bool(logKeyDefaultDivergent, report.DefaultDivergent()),
				slog.Int(logKeyChangedCount, len(changes)),
			)
			if !opts.DisableReloadChanges {
				for _, change := range changes {
					sink.emit(false, slog.LevelInfo, "kms config field changed",
						componentAttr,
						slog.String(logKeyRelease, release.String()),
						slog.String(logKeyPath, change.Path),
						slog.Any(logKeyPrevious, change.Previous),
						slog.Any(logKeyCurrent, change.Current),
					)
				}
			}
			if !opts.DisableReloadSnapshot {
				logSnapshot(sink, false, componentAttr, release, report)
			}
		},
		OnCandidateRejected: func(report CandidateRejectionReport) {
			if report == nil {
				return
			}
			sink.emit(!applied.Load(), slog.LevelError, "kms config candidate rejected",
				componentAttr,
				slog.String(logKeyCategory, string(report.Category())),
				slog.String(logKeyRelease, report.Release().String()),
				slog.Any(logKeyPaths, report.Paths()),
			)
		},
	}
}

// logSnapshot emits one "kms config group" record per parameter group of the
// applied generation. startup selects buffering when no logger is installed.
func logSnapshot(sink *LogSink, startup bool, componentAttr slog.Attr, release ReleaseIdentity, report AppliedReport) {
	groups, err := report.Groups()
	if err != nil {
		sink.emit(startup, slog.LevelError, "kms config groups unavailable",
			componentAttr,
			slog.String(logKeyRelease, release.String()),
			slog.String(logKeyError, err.Error()),
		)
		return
	}
	aliases := make([]string, 0, len(groups))
	for alias := range groups {
		aliases = append(aliases, alias)
	}
	sort.Strings(aliases)
	for _, alias := range aliases {
		values := groups[alias]
		if len(values) == 0 {
			values = json.RawMessage("null")
		}
		sink.emit(startup, slog.LevelInfo, "kms config group",
			componentAttr,
			slog.String(logKeyAlias, alias),
			slog.Any(logKeyValues, values),
			slog.Uint64(logKeyReleaseVersion, release.Version()),
			slog.Uint64(logKeyActivationRevision, release.ActivationRevision()),
		)
	}
}
