package cli

import (
	"errors"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// observedLogger returns a logger recording every entry at debug level and up.
func observedLogger() (*zap.Logger, *observer.ObservedLogs) {
	core, logs := observer.New(zapcore.DebugLevel)
	return zap.New(core), logs
}

// entryFor returns the single logged entry whose message is msg, failing when
// there is not exactly one.
func entryFor(t *testing.T, logs *observer.ObservedLogs, msg string) observer.LoggedEntry {
	t.Helper()
	found := logs.FilterMessage(msg).All()
	if len(found) != 1 {
		t.Fatalf("want exactly 1 entry %q, got %d; all entries: %v", msg, len(found), messages(logs))
	}
	return found[0]
}

func messages(logs *observer.ObservedLogs) []string {
	out := make([]string, 0, logs.Len())
	for _, e := range logs.All() {
		out = append(out, e.Message)
	}
	return out
}

// TestLogAdminCertPostureDisabled: an operator who turned the requirement off
// must be told what they gave up, on every boot, whether or not TLS is on.
func TestLogAdminCertPostureDisabled(t *testing.T) {
	for _, tlsEnabled := range []bool{false, true} {
		logger, logs := observedLogger()
		logAdminCertPosture(logger, false, tlsEnabled, true, nil, nil)

		e := entryFor(t, logs, adminCertDisabledMsg)
		if e.Level != zapcore.WarnLevel {
			t.Errorf("tls=%t: level = %v, want warn", tlsEnabled, e.Level)
		}
		if logs.Len() != 1 {
			t.Errorf("tls=%t: logged %v, want only the disabled warning", tlsEnabled, messages(logs))
		}
	}
}

// TestLogAdminCertPostureUnenforceable: configured but no TLS. The requirement
// silently does nothing, so it is a warning — escalated when the listeners are
// reachable from off-host, where a stolen token is remotely usable.
func TestLogAdminCertPostureUnenforceable(t *testing.T) {
	t.Run("loopback", func(t *testing.T) {
		logger, logs := observedLogger()
		logAdminCertPosture(logger, true, false, false, nil, nil)

		if e := entryFor(t, logs, adminCertUnenforceableMsg); e.Level != zapcore.WarnLevel {
			t.Errorf("level = %v, want warn", e.Level)
		}
		if n := logs.FilterMessage(adminCertExposedMsg).Len(); n != 0 {
			t.Errorf("loopback bind logged the escalated message %d times", n)
		}
	})

	t.Run("non-loopback escalates", func(t *testing.T) {
		logger, logs := observedLogger()
		logAdminCertPosture(logger, true, false, true, nil, nil)

		if e := entryFor(t, logs, adminCertExposedMsg); e.Level != zapcore.WarnLevel {
			t.Errorf("level = %v, want warn", e.Level)
		}
		if n := logs.FilterMessage(adminCertUnenforceableMsg).Len(); n != 0 {
			t.Errorf("non-loopback bind logged the un-escalated message %d times", n)
		}
	})
}

// TestLogAdminCertPostureEnforced: the requirement is live. The Info line tells
// the operator how to mint a certificate, and every admin that cannot log in
// until they do is named individually — this is the upgrade-lockout warning.
func TestLogAdminCertPostureEnforced(t *testing.T) {
	logger, logs := observedLogger()
	logAdminCertPosture(logger, true, true, true, []string{"ops", "release-bot"}, nil)

	if e := entryFor(t, logs, adminCertEnforcedMsg); e.Level != zapcore.InfoLevel {
		t.Errorf("level = %v, want info", e.Level)
	}
	missing := logs.FilterMessage(adminCertMissingMsg).All()
	if len(missing) != 2 {
		t.Fatalf("got %d per-identity warnings, want 2: %v", len(missing), messages(logs))
	}
	var named []string
	for _, e := range missing {
		if e.Level != zapcore.WarnLevel {
			t.Errorf("per-identity entry level = %v, want warn", e.Level)
		}
		named = append(named, e.ContextMap()["identity"].(string))
	}
	if named[0] != "ops" || named[1] != "release-bot" {
		t.Errorf("named identities = %v, want [ops release-bot]", named)
	}
}

// TestLogAdminCertPostureEnforcedAllCertified: with every admin holding a
// certificate the startup log stays quiet apart from the single Info line.
func TestLogAdminCertPostureEnforcedAllCertified(t *testing.T) {
	logger, logs := observedLogger()
	logAdminCertPosture(logger, true, true, false, nil, nil)

	entryFor(t, logs, adminCertEnforcedMsg)
	if logs.Len() != 1 {
		t.Errorf("logged %v, want only the enforcement notice", messages(logs))
	}
}

// TestLogAdminCertPostureScanError: a failed scan is reported and does not
// suppress the enforcement notice — serve must still start.
func TestLogAdminCertPostureScanError(t *testing.T) {
	logger, logs := observedLogger()
	logAdminCertPosture(logger, true, true, false, nil, errors.New("database is locked"))

	entryFor(t, logs, adminCertEnforcedMsg)
	e := entryFor(t, logs, adminCertScanFailedMsg)
	if e.Level != zapcore.WarnLevel {
		t.Errorf("level = %v, want warn", e.Level)
	}
	if got := e.ContextMap()["error"]; got != "database is locked" {
		t.Errorf("error field = %v, want the scan error", got)
	}
}
