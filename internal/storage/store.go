package storage

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"

	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/fileutil"
)

// schemaVersion is the schema version this build supports. Opening a database
// stamped with a higher version is refused.
const schemaVersion = 6

// tsLayout is a fixed-width RFC3339 UTC layout with nanosecond precision. Unlike
// time.RFC3339Nano it never trims trailing zeros, so every stored timestamp has
// identical width and lexicographic string ordering equals chronological
// ordering — which SQL range filters and ORDER BY rely on. Values still parse
// back to the exact time.Time they came from.
const tsLayout = "2006-01-02T15:04:05.000000000Z07:00"

// changeLogDDL creates the change_log table with a guaranteed
// INTEGER PRIMARY KEY AUTOINCREMENT, so revisions stay monotonic and are never
// reused after pruning (SQLite tracks the max in sqlite_sequence).
const changeLogDDL = `CREATE TABLE IF NOT EXISTS change_log (
	revision       INTEGER PRIMARY KEY AUTOINCREMENT,
	resource_type  TEXT NOT NULL,
	namespace_id   INTEGER NOT NULL DEFAULT 0,
	env            TEXT NOT NULL,
	app            TEXT NOT NULL,
	key            TEXT NOT NULL,
	change_type    TEXT NOT NULL,
	value          TEXT,
	content_type   TEXT NOT NULL DEFAULT '',
	version_number INTEGER NOT NULL DEFAULT 0,
	label          TEXT NOT NULL DEFAULT '',
	created_at     TEXT NOT NULL
)`

const changeLogIndexDDL = `CREATE INDEX IF NOT EXISTS idx_change_log_ns ON change_log(env, app)`
const changeLogNamespaceIndexDDL = `CREATE INDEX IF NOT EXISTS idx_change_log_namespace_revision ON change_log(namespace_id, revision)`

// SQLStore is the SQLite-backed implementation of Store.
type SQLStore struct {
	db *gorm.DB
}

// compile-time assertion that *SQLStore satisfies Store.
var _ Store = (*SQLStore)(nil)

// Options tunes how the database is opened.
type Options struct {
	// SynchronousFull selects PRAGMA synchronous=FULL (maximum durability)
	// instead of the default NORMAL, which is the recommended setting with WAL.
	SynchronousFull bool
	// BusyTimeout is how long SQLite waits on a locked database before
	// returning SQLITE_BUSY. Defaults to 5s when zero.
	BusyTimeout time.Duration
}

// Open opens (creating if needed) the database at path with default options.
func Open(path string) (*SQLStore, error) {
	return OpenWithOptions(path, Options{})
}

// ValidateKMSDatabase opens an existing database read-only and verifies that
// it is a migrated KMS database supported by this build. Unlike Open it never
// creates a missing file or adds KMS tables to an unrelated SQLite database.
func ValidateKMSDatabase(path string) error {
	if path == "" {
		return domain.Errorf(domain.ErrInvalidArgument, "database path is empty")
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat database %q: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("database %q is not a regular file", path)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolve database path %q: %w", path, err)
	}
	u := url.URL{Scheme: "file", Path: abs}
	db, err := gorm.Open(sqlite.Open(u.String()+"?mode=ro&_pragma=query_only(1)"), &gorm.Config{
		Logger:                 logger.Default.LogMode(logger.Silent),
		SkipDefaultTransaction: true,
	})
	if err != nil {
		return fmt.Errorf("open database %q read-only: %w", path, err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return fmt.Errorf("access database %q: %w", path, err)
	}
	defer func() { _ = sqlDB.Close() }()

	for _, table := range []string{"schema_migrations", "key_metadata", "namespaces", "identities", "change_log"} {
		var count int64
		if err := db.Raw("SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?", table).Scan(&count).Error; err != nil {
			return fmt.Errorf("inspect database %q: %w", path, err)
		}
		if count != 1 {
			return fmt.Errorf("database %q is not a KMS database (missing table %s)", path, table)
		}
	}
	var stored schemaMigrationModel
	if err := db.Order("version DESC").First(&stored).Error; err != nil {
		return fmt.Errorf("database %q has no valid KMS schema version: %w", path, err)
	}
	if stored.Version > schemaVersion {
		return fmt.Errorf("database schema version %d is newer than supported version %d; upgrade the binary", stored.Version, schemaVersion)
	}
	if stored.Version < 1 {
		return fmt.Errorf("database %q has invalid KMS schema version %d", path, stored.Version)
	}
	return nil
}

// OpenWithOptions opens the database at path with the given options. Pragmas are
// applied through the DSN so they take effect on every pooled connection.
func OpenWithOptions(path string, opts Options) (*SQLStore, error) {
	if path == "" {
		return nil, domain.Errorf(domain.ErrInvalidArgument, "database path is empty")
	}
	stablePath, err := fileutil.ResolveStablePath(path)
	if err != nil {
		return nil, fmt.Errorf("validate database path %q: %w", path, err)
	}
	// SQLite creates a missing database with its default 0666 mode, leaving the
	// effective permissions to the process umask. Reserve a missing path with a
	// restrictive mode first; O_EXCL also ensures this step never truncates an
	// existing database. Existing databases retain their operator-selected mode.
	created, err := fileutil.OpenPrivateExclusive(stablePath)
	if err == nil {
		if err := created.Close(); err != nil {
			return nil, fmt.Errorf("close new database %q: %w", path, err)
		}
	} else if errors.Is(err, os.ErrExist) {
		// Never hand SQLite a symlink, an attacker-owned pre-existing entry, or
		// an inherited broad ACL. The entry-stable parent plus exact-file checks
		// make the secured path safe to reuse for SQLite's pathname open.
		stablePath, err = fileutil.SecureExistingPrivateFile(stablePath)
		if err != nil {
			return nil, fmt.Errorf("secure existing database %q: %w", path, err)
		}
	} else {
		return nil, fmt.Errorf("create database %q: %w", path, err)
	}
	absPath := stablePath
	busy := opts.BusyTimeout
	if busy <= 0 {
		busy = 5 * time.Second
	}
	sync := "NORMAL"
	if opts.SynchronousFull {
		sync = "FULL"
	}
	// _txlock=immediate makes every transaction begin with BEGIN IMMEDIATE so a
	// writer acquires the write lock up front. This prevents WAL stale-snapshot
	// conflicts between concurrent read-then-write transactions (e.g. two
	// PutParameter calls racing to assign the next version).
	databaseURL := url.URL{Scheme: "file", Path: filepath.ToSlash(absPath)}
	dsn := fmt.Sprintf(
		"%s?_txlock=immediate&_pragma=busy_timeout(%d)&_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=synchronous(%s)",
		databaseURL.String(), busy.Milliseconds(), sync,
	)
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger:                 logger.Default.LogMode(logger.Silent),
		SkipDefaultTransaction: true,
	})
	if err != nil {
		return nil, fmt.Errorf("open database %q: %w", path, err)
	}
	s := &SQLStore{db: db}
	if err := s.migrate(); err != nil {
		if sqlDB, e := db.DB(); e == nil {
			_ = sqlDB.Close()
		}
		return nil, err
	}
	return s, nil
}

func (s *SQLStore) migrate() error {
	if err := s.db.AutoMigrate(&schemaMigrationModel{}); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}
	var stored schemaMigrationModel
	current := 0
	err := s.db.Order("version DESC").First(&stored).Error
	switch {
	case err == nil:
		current = stored.Version
	case errors.Is(err, gorm.ErrRecordNotFound):
		current = 0
	default:
		return fmt.Errorf("read schema version: %w", err)
	}
	if current > schemaVersion {
		return fmt.Errorf("database schema version %d is newer than supported version %d; upgrade the binary", current, schemaVersion)
	}

	// change_log is created explicitly to guarantee AUTOINCREMENT regardless of
	// how GORM renders the tag.
	if err := s.db.Exec(changeLogDDL).Error; err != nil {
		return fmt.Errorf("create change_log: %w", err)
	}
	// CREATE TABLE IF NOT EXISTS does not evolve existing raw-DDL tables. Legacy
	// rows remain namespace_id=0 and therefore fail closed for incarnation-bound
	// watch replay; all new rows are stamped by appendChange.
	if !s.db.Migrator().HasColumn(&changeLogModel{}, "NamespaceID") {
		if err := s.db.Exec("ALTER TABLE change_log ADD COLUMN namespace_id INTEGER NOT NULL DEFAULT 0").Error; err != nil {
			return fmt.Errorf("add change_log namespace incarnation: %w", err)
		}
	}
	if err := s.db.Exec(changeLogIndexDDL).Error; err != nil {
		return fmt.Errorf("create change_log index: %w", err)
	}
	if err := s.db.Exec(changeLogNamespaceIndexDDL).Error; err != nil {
		return fmt.Errorf("create change_log namespace incarnation index: %w", err)
	}
	if err := ensureReleaseSubscriberIdentityKeys(s.db); err != nil {
		return err
	}
	needsSecretVersionAttributeBackfill := current < 2 ||
		!s.db.Migrator().HasColumn(&secretVersionModel{}, "ContentType") ||
		!s.db.Migrator().HasColumn(&secretVersionModel{}, "ClientBound") ||
		!s.db.Migrator().HasColumn(&secretVersionModel{}, "HasAccessToken")
	// GORM attempts to rebuild manually repaired SQLite composite-key tables
	// when its inferred DDL differs textually, even when the physical key is
	// already correct. Migrate the ordinary models first, then create subscriber
	// tables only when absent; existing tables were repaired and are verified by
	// physical schema below.
	ordinaryModels := make([]any, 0, len(autoMigrateModels)-2)
	for _, model := range autoMigrateModels {
		switch model.(type) {
		case *releaseSubscriberStateModel, *releaseSubscriberConnectionModel:
			continue
		default:
			ordinaryModels = append(ordinaryModels, model)
		}
	}
	if err := s.db.AutoMigrate(ordinaryModels...); err != nil {
		return fmt.Errorf("auto-migrate: %w", err)
	}
	if !s.db.Migrator().HasTable(&releaseSubscriberStateModel{}) {
		if err := s.db.AutoMigrate(&releaseSubscriberStateModel{}); err != nil {
			return fmt.Errorf("create release subscriber states: %w", err)
		}
	}
	if !s.db.Migrator().HasTable(&releaseSubscriberConnectionModel{}) {
		if err := s.db.AutoMigrate(&releaseSubscriberConnectionModel{}); err != nil {
			return fmt.Errorf("create release subscriber connections: %w", err)
		}
	}
	if err := verifyReleaseSubscriberIdentityKeys(s.db); err != nil {
		return fmt.Errorf("verify release subscriber identity keys: %w", err)
	}
	if needsSecretVersionAttributeBackfill {
		// v2 makes content/protection metadata immutable per secret version.
		// Older schemas only retained the latest values on secrets, so that is
		// the only safe backfill available for existing version history. Future
		// writes persist the attributes alongside every new version.
		if err := s.db.Transaction(func(tx *gorm.DB) error {
			return tx.Exec(`UPDATE secret_versions
				SET content_type = (SELECT content_type FROM secrets WHERE secrets.id = secret_versions.secret_id),
				    client_bound = (SELECT client_bound FROM secrets WHERE secrets.id = secret_versions.secret_id),
				    has_access_token = CASE WHEN COALESCE(length((SELECT access_token_hash FROM secrets WHERE secrets.id = secret_versions.secret_id)), 0) > 0 THEN 1 ELSE 0 END`).Error
		}); err != nil {
			return fmt.Errorf("backfill secret version attributes: %w", err)
		}
	}

	// Defensive verification of the critical AUTOINCREMENT invariant.
	var ddl string
	if err := s.db.Raw("SELECT sql FROM sqlite_master WHERE type='table' AND name='change_log'").Scan(&ddl).Error; err != nil {
		return fmt.Errorf("inspect change_log: %w", err)
	}
	if !strings.Contains(strings.ToUpper(ddl), "AUTOINCREMENT") {
		return fmt.Errorf("change_log is missing AUTOINCREMENT: %q", ddl)
	}

	if current < schemaVersion {
		if err := s.db.Create(&schemaMigrationModel{Version: schemaVersion, AppliedAt: fmtTime(time.Now())}).Error; err != nil {
			return fmt.Errorf("record schema version: %w", err)
		}
	}
	return nil
}

// Ping verifies the database is reachable.
func (s *SQLStore) Ping(ctx context.Context) error {
	sqlDB, err := s.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.PingContext(ctx)
}

// Close closes the underlying database.
func (s *SQLStore) Close() error {
	sqlDB, err := s.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}

// Backup writes a consistent online backup to destPath using VACUUM INTO. It
// fails if destPath already exists.
func (s *SQLStore) Backup(ctx context.Context, destPath string) error {
	if destPath == "" {
		return domain.Errorf(domain.ErrInvalidArgument, "backup destination is empty")
	}
	requestedDest := destPath
	stableDest, err := fileutil.ResolveStablePath(destPath)
	if err != nil {
		return fmt.Errorf("validate backup destination %s: %w", destPath, err)
	}
	destPath = stableDest
	if _, err := os.Stat(destPath); err == nil {
		return domain.Errorf(domain.ErrAlreadyExists, "backup destination %s already exists", requestedDest)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat backup destination: %w", err)
	}
	// VACUUM INTO chooses its own create mode. Build the backup beneath a private
	// staging directory, restrict it before publication, then use the platform's
	// atomic no-replace primitive so a concurrently-created path always wins.
	stagingDir, err := fileutil.MkdirPrivateTemp(filepath.Dir(destPath), ".kms-backup-")
	if err != nil {
		return fmt.Errorf("create backup staging directory: %w", err)
	}
	stagedPath := filepath.Join(stagingDir, "backup.db")
	defer func() {
		// Keep cleanup non-recursive: if a hostile writer swaps the staging
		// directory name, cleanup must not descend into their replacement.
		for _, suffix := range []string{"", "-journal", "-wal", "-shm"} {
			_ = os.Remove(stagedPath + suffix)
		}
		_ = os.Remove(stagingDir)
	}()

	// VACUUM INTO does not accept bound parameters on all drivers, so build a
	// safely single-quote-escaped literal.
	quoted := "'" + strings.ReplaceAll(stagedPath, "'", "''") + "'"
	if err := s.db.WithContext(ctx).Exec("VACUUM INTO " + quoted).Error; err != nil {
		return fmt.Errorf("vacuum into %s: %w", requestedDest, err)
	}
	stagedFile, err := fileutil.OpenForOwnerRestriction(stagedPath)
	if err != nil {
		return fmt.Errorf("open completed backup: %w", err)
	}
	if err := fileutil.RestrictOwnerOnly(stagedFile, false); err != nil {
		_ = stagedFile.Close()
		return fmt.Errorf("restrict backup permissions: %w", err)
	}
	if err := stagedFile.Close(); err != nil {
		return fmt.Errorf("close completed backup: %w", err)
	}
	if err := fileutil.PublishNoReplace(stagedPath, destPath); err != nil {
		if errors.Is(err, os.ErrExist) {
			return domain.Errorf(domain.ErrAlreadyExists, "backup destination %s already exists", requestedDest)
		}
		return fmt.Errorf("publish backup %s: %w", requestedDest, err)
	}
	return nil
}

// ---- shared helpers -------------------------------------------------------

// fmtTime renders t as fixed-width RFC3339 UTC text; the zero time becomes "".
func fmtTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(tsLayout)
}

// fmtTimePtr is fmtTime for nullable columns: the zero time becomes SQL NULL.
func fmtTimePtr(t time.Time) *string {
	if t.IsZero() {
		return nil
	}
	s := t.UTC().Format(tsLayout)
	return &s
}

// parseTime parses a stored timestamp; "" and unparseable input become the zero
// time.
func parseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t.UTC()
	}
	return time.Time{}
}

func parseTimePtr(s *string) time.Time {
	if s == nil {
		return time.Time{}
	}
	return parseTime(*s)
}

// likeEscape escapes LIKE metacharacters for use with ESCAPE '\'.
func likeEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// applyKeyPrefix restricts q to rows whose key column begins with prefix,
// treated as an opaque byte prefix ("billing" matches "billing", "billing/x",
// and "billingx" alike). It is a non-authz browsing convenience only — never a
// security boundary; keys are opaque and the server never interprets '/'. An
// empty prefix matches everything. Keys are always namespace-scoped by the
// caller, so this only narrows within a single namespace.
func applyKeyPrefix(q *gorm.DB, column, prefix string) *gorm.DB {
	if prefix == "" {
		return q
	}
	return q.Where(column+` LIKE ? ESCAPE '\'`, likeEscape(prefix)+"%")
}

func clampLimit(limit int) int {
	switch {
	case limit <= 0:
		return 100
	case limit > 1000:
		return 1000
	default:
		return limit
	}
}

func encodeToken(key string) string {
	return base64.StdEncoding.EncodeToString([]byte(key))
}

func decodeToken(tok string) (string, error) {
	if tok == "" {
		return "", nil
	}
	b, err := base64.StdEncoding.DecodeString(tok)
	if err != nil {
		return "", domain.Errorf(domain.ErrInvalidArgument, "invalid page token")
	}
	return string(b), nil
}

func encodeIntToken(id int64) string {
	return encodeToken(strconv.FormatInt(id, 10))
}

func decodeIntToken(tok string) (int64, error) {
	s, err := decodeToken(tok)
	if err != nil {
		return 0, err
	}
	if s == "" {
		return 0, nil
	}
	id, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, domain.Errorf(domain.ErrInvalidArgument, "invalid page token")
	}
	return id, nil
}

// isUniqueErr reports whether err is a SQLite UNIQUE-constraint violation.
func isUniqueErr(err error) bool {
	return err != nil && strings.Contains(strings.ToUpper(err.Error()), "UNIQUE CONSTRAINT")
}

// resolveNamespaceID looks up the primary key of an existing namespace. It
// returns domain.ErrNotFound (naming the namespace) when none exists, so
// callers that address a resource by ref surface a clear error before touching
// the parameters/secrets tables (whose namespace_id foreign key requires it).
func resolveNamespaceID(tx *gorm.DB, ns domain.NamespaceRef) (int64, error) {
	var m namespaceModel
	q := tx.Select("id").Where("env = ? AND app = ?", ns.Env, ns.App)
	expectedID, bound := ExpectedNamespaceIncarnation(tx.Statement.Context, ns)
	if bound {
		q = q.Where("id = ?", expectedID)
	}
	err := q.First(&m).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			if bound {
				return 0, domain.Errorf(domain.ErrAborted, "namespace %s changed during request; retry", ns)
			}
			return 0, domain.Errorf(domain.ErrNotFound, "namespace %s", ns)
		}
		return 0, err
	}
	return m.ID, nil
}

// appendChange inserts a change_log row and returns its assigned revision.
func appendChange(tx *gorm.DB, cl *changeLogModel) (uint64, error) {
	if cl.NamespaceID == 0 && cl.Env != "" && cl.App != "" {
		id, err := resolveNamespaceID(tx, domain.NamespaceRef{Env: cl.Env, App: cl.App})
		if err != nil {
			return 0, err
		}
		cl.NamespaceID = id
	}
	if cl.CreatedAt == "" {
		cl.CreatedAt = fmtTime(time.Now())
	}
	if err := tx.Omit(clause.Associations).Create(cl).Error; err != nil {
		return 0, err
	}
	return uint64(cl.Revision), nil
}
