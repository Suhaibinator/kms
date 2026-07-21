package storage

import (
	"net/url"
	"strings"
	"testing"
)

func TestSQLiteFileURI(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		slashPath       string
		wantURI         string
		wantHost        string
		wantDecodedPath string
	}{
		{
			name:            "unix absolute",
			slashPath:       "/var/lib/kms/store.db",
			wantURI:         "file:///var/lib/kms/store.db",
			wantDecodedPath: "/var/lib/kms/store.db",
		},
		{
			name:            "uppercase Windows drive is a path",
			slashPath:       "C:/Program Files/KMS/store.db",
			wantURI:         "file:///C:/Program%20Files/KMS/store.db",
			wantDecodedPath: "/C:/Program Files/KMS/store.db",
		},
		{
			name:            "lowercase Windows drive is a path",
			slashPath:       "d:/kms/store.db",
			wantURI:         "file:///d:/kms/store.db",
			wantDecodedPath: "/d:/kms/store.db",
		},
		{
			name:            "URI metacharacters remain literal filename bytes",
			slashPath:       "/tmp/a?b#c%2F.db",
			wantURI:         "file:///tmp/a%3Fb%23c%252F.db",
			wantDecodedPath: "/tmp/a?b#c%2F.db",
		},
		{
			name:            "backslash remains a literal Unix filename byte",
			slashPath:       `/tmp/a\b.db`,
			wantURI:         "file:///tmp/a%5Cb.db",
			wantDecodedPath: `/tmp/a\b.db`,
		},
		{
			name:            "non-letter drive prefix is not rewritten",
			slashPath:       "1:/not-a-drive/store.db",
			wantURI:         "file://1:/not-a-drive/store.db",
			wantHost:        "1:",
			wantDecodedPath: "/not-a-drive/store.db",
		},
		{
			// Keep the UNC server/share spelling in the path with an empty URI
			// authority. This avoids asking SQLite to interpret the server name
			// as a URI host, while the Windows VFS still receives //server/share.
			name:            "UNC spelling stays in the empty-host path",
			slashPath:       "//server/share/store.db",
			wantURI:         "file:////server/share/store.db",
			wantDecodedPath: "//server/share/store.db",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := sqliteFileURI(tt.slashPath)
			if got != tt.wantURI {
				t.Fatalf("sqliteFileURI(%q) = %q, want %q", tt.slashPath, got, tt.wantURI)
			}
			parsed, err := url.Parse(got)
			if err != nil {
				t.Fatalf("parse generated URI: %v", err)
			}
			if parsed.Scheme != "file" || parsed.Host != tt.wantHost || parsed.Path != tt.wantDecodedPath {
				t.Fatalf("parsed URI = scheme %q host %q path %q, want file/%q/%q",
					parsed.Scheme, parsed.Host, parsed.Path, tt.wantHost, tt.wantDecodedPath)
			}
			if isWindowsDriveSlashPath(tt.slashPath) && strings.HasPrefix(got, "file://"+tt.slashPath[:2]) {
				t.Fatalf("drive %q was rendered as a URI authority: %q", tt.slashPath[:2], got)
			}
		})
	}
}

func TestIsWindowsDriveSlashPath(t *testing.T) {
	t.Parallel()
	for _, path := range []string{"C:/db", "z:/dir/db"} {
		if !isWindowsDriveSlashPath(path) {
			t.Errorf("isWindowsDriveSlashPath(%q) = false", path)
		}
	}
	for _, path := range []string{"/C:/db", "1:/db", "C:relative", "CC:/db", "//server/share/db", ""} {
		if isWindowsDriveSlashPath(path) {
			t.Errorf("isWindowsDriveSlashPath(%q) = true", path)
		}
	}
}
