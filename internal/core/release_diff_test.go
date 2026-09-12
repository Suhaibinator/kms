package core

import (
	"reflect"
	"testing"

	"github.com/Suhaibinator/kms/internal/domain"
)

func diffEntry(alias, kind, env, key string, version uint64, contentType, digest string) domain.ConfigurationReleaseEntry {
	return domain.ConfigurationReleaseEntry{
		Alias: alias, Kind: kind, Ref: domain.Ref{NS: domain.NamespaceRef{Env: env, App: "gradethis"}, Key: key},
		Version: version, ContentType: contentType, ParameterDigest: digest,
	}
}

func diffRelease(env string, version uint64, entries ...domain.ConfigurationReleaseEntry) domain.ConfigurationRelease {
	return domain.ConfigurationRelease{Namespace: domain.NamespaceRef{Env: env, App: "gradethis"}, Name: "runtime", Version: version, SchemaVersion: 1, Entries: entries}
}

type diffExpectation struct {
	change  string
	reasons []string
	kind    string
}

func TestComputeReleaseDiff(t *testing.T) {
	base := []domain.ConfigurationReleaseEntry{
		diffEntry("database", domain.ReleaseEntryParameter, "prod", "database", 3, "json", "d1"),
		diffEntry("db_password", domain.ReleaseEntrySecret, "prod", "db_password", 2, "", ""),
		diffEntry("rate_limits", domain.ReleaseEntryParameter, "prod", "rate_limits", 7, "integer", "r7"),
	}
	cases := []struct {
		name  string
		from  []domain.ConfigurationReleaseEntry
		to    []domain.ConfigurationReleaseEntry
		cross bool
		want  map[string]diffExpectation
	}{
		{
			name: "identical releases are all unchanged",
			from: base, to: base,
			want: map[string]diffExpectation{
				"database":    {domain.ReleaseDiffUnchanged, []string{}, domain.ReleaseEntryParameter},
				"db_password": {domain.ReleaseDiffUnchanged, []string{}, domain.ReleaseEntrySecret},
				"rate_limits": {domain.ReleaseDiffUnchanged, []string{}, domain.ReleaseEntryParameter},
			},
		},
		{
			name: "value, pin, added and removed",
			from: base,
			to: []domain.ConfigurationReleaseEntry{
				diffEntry("database", domain.ReleaseEntryParameter, "prod", "database", 4, "json", "d1"),
				diffEntry("feature_flags", domain.ReleaseEntryParameter, "prod", "feature_flags", 1, "json", "f1"),
				diffEntry("rate_limits", domain.ReleaseEntryParameter, "prod", "rate_limits", 8, "integer", "r8"),
			},
			want: map[string]diffExpectation{
				"database":      {domain.ReleaseDiffChanged, []string{domain.ReleaseDiffReasonPin}, domain.ReleaseEntryParameter},
				"db_password":   {domain.ReleaseDiffRemoved, []string{}, domain.ReleaseEntrySecret},
				"feature_flags": {domain.ReleaseDiffAdded, []string{}, domain.ReleaseEntryParameter},
				"rate_limits":   {domain.ReleaseDiffChanged, []string{domain.ReleaseDiffReasonValue}, domain.ReleaseEntryParameter},
			},
		},
		{
			name: "secret repin, key, kind and content type",
			from: base,
			to: []domain.ConfigurationReleaseEntry{
				diffEntry("database", domain.ReleaseEntryParameter, "prod", "database-v2", 3, "string", "d1"),
				diffEntry("db_password", domain.ReleaseEntrySecret, "prod", "db_password", 3, "", ""),
				diffEntry("rate_limits", domain.ReleaseEntrySecret, "prod", "rate_limits", 7, "", ""),
			},
			want: map[string]diffExpectation{
				"database":    {domain.ReleaseDiffChanged, []string{domain.ReleaseDiffReasonKey, domain.ReleaseDiffReasonContentType}, domain.ReleaseEntryParameter},
				"db_password": {domain.ReleaseDiffChanged, []string{domain.ReleaseDiffReasonPin}, domain.ReleaseEntrySecret},
				"rate_limits": {domain.ReleaseDiffChanged, []string{domain.ReleaseDiffReasonKind, domain.ReleaseDiffReasonContentType}, domain.ReleaseEntrySecret},
			},
		},
		{
			name: "cross-environment compares keys and digests, not versions",
			from: base,
			to: []domain.ConfigurationReleaseEntry{
				diffEntry("database", domain.ReleaseEntryParameter, "staging", "database", 9, "json", "d1"),
				diffEntry("db_password", domain.ReleaseEntrySecret, "staging", "db_password", 5, "", ""),
				diffEntry("rate_limits", domain.ReleaseEntryParameter, "staging", "rate_limits", 1, "integer", "r1"),
			},
			cross: true,
			want: map[string]diffExpectation{
				"database":    {domain.ReleaseDiffUnchanged, []string{}, domain.ReleaseEntryParameter},
				"db_password": {domain.ReleaseDiffChanged, []string{domain.ReleaseDiffReasonPin}, domain.ReleaseEntrySecret},
				"rate_limits": {domain.ReleaseDiffChanged, []string{domain.ReleaseDiffReasonValue}, domain.ReleaseEntryParameter},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			toEnv := "prod"
			if tc.cross {
				toEnv = "staging"
			}
			rows := computeReleaseDiff(diffRelease("prod", 1, tc.from...), diffRelease(toEnv, 2, tc.to...), tc.cross)
			if len(rows) != len(tc.want) {
				t.Fatalf("rows = %d, want %d: %+v", len(rows), len(tc.want), rows)
			}
			for i, row := range rows {
				if i > 0 && rows[i-1].Alias >= row.Alias {
					t.Fatalf("rows are not sorted by alias: %q after %q", row.Alias, rows[i-1].Alias)
				}
				want, ok := tc.want[row.Alias]
				if !ok {
					t.Fatalf("unexpected row %q", row.Alias)
				}
				if row.Change != want.change || row.Kind != want.kind || !reflect.DeepEqual(row.Reasons, want.reasons) {
					t.Fatalf("%s: change=%s kind=%s reasons=%v, want change=%s kind=%s reasons=%v", row.Alias, row.Change, row.Kind, row.Reasons, want.change, want.kind, want.reasons)
				}
				if (row.From == nil) != (row.Change == domain.ReleaseDiffAdded) || (row.To == nil) != (row.Change == domain.ReleaseDiffRemoved) {
					t.Fatalf("%s: sides from=%v to=%v for %s", row.Alias, row.From != nil, row.To != nil, row.Change)
				}
			}
		})
	}
}

func TestReleaseDiffCounts(t *testing.T) {
	rows := []domain.ReleaseDiffRow{
		{Alias: "a", Kind: domain.ReleaseEntryParameter, Change: domain.ReleaseDiffAdded},
		{Alias: "b", Kind: domain.ReleaseEntryParameter, Change: domain.ReleaseDiffRemoved},
		{Alias: "c", Kind: domain.ReleaseEntryParameter, Change: domain.ReleaseDiffChanged, Reasons: []string{domain.ReleaseDiffReasonContentType}},
		{Alias: "d", Kind: domain.ReleaseEntrySecret, Change: domain.ReleaseDiffChanged, Reasons: []string{domain.ReleaseDiffReasonPin},
			To: &domain.ReleaseDiffPin{Entry: domain.ConfigurationReleaseEntry{Kind: domain.ReleaseEntrySecret}, SecretState: domain.StateDisabled}},
		{Alias: "e", Kind: domain.ReleaseEntryParameter, Change: domain.ReleaseDiffChanged, Reasons: []string{domain.ReleaseDiffReasonValue},
			To: &domain.ReleaseDiffPin{ValueState: domain.ReleaseDiffValueUnavailable}},
		{Alias: "f", Kind: domain.ReleaseEntrySecret, Change: domain.ReleaseDiffUnchanged, Reasons: []string{},
			To: &domain.ReleaseDiffPin{Entry: domain.ConfigurationReleaseEntry{Kind: domain.ReleaseEntrySecret}, SecretState: domain.StateDisabled}},
		{Alias: "g", Kind: domain.ReleaseEntryParameter, Change: domain.ReleaseDiffChanged, Reasons: []string{domain.ReleaseDiffReasonValue},
			To: &domain.ReleaseDiffPin{ValueState: domain.ReleaseDiffValuePresent}},
	}
	got := releaseDiffCounts(rows)
	want := domain.ReleaseDiffCounts{Added: 1, Removed: 1, Changed: 4, Unchanged: 1, SecretsChanged: 1, Attention: 3}
	if got != want {
		t.Fatalf("counts = %+v, want %+v", got, want)
	}
}
