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
	"sync"
	"sync/atomic"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"

	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/fileutil"
)

// schemaVersion is the greenfield 0.3.x baseline. This build intentionally has
// no executable migration path from any 0.2.x schema.
const schemaVersion = 1

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
	affected_versions_json TEXT NOT NULL DEFAULT '[]',
	label          TEXT NOT NULL DEFAULT '',
	created_at     TEXT NOT NULL
)`

const changeLogIndexDDL = `CREATE INDEX IF NOT EXISTS idx_change_log_ns ON change_log(env, app)`
const changeLogNamespaceIndexDDL = `CREATE INDEX IF NOT EXISTS idx_change_log_namespace_revision ON change_log(namespace_id, revision)`

const (
	sqlStoreMaxOpenConns = 0 // database/sql default: unlimited
	sqlStoreMaxIdleConns = 2 // database/sql default, made explicit for restoration after purge
)

var baselineReferenceID atomic.Uint64

// SQLStore is the SQLite-backed implementation of Store.
type SQLStore struct {
	db *gorm.DB

	// purgeMu serializes the temporary database/sql pool quiescence used by a
	// physical secret purge. It does not protect ordinary SQL transactions.
	purgeMu          sync.Mutex
	poolMaxOpenConns int
	poolMaxIdleConns int
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
// it is an exact KMS database baseline supported by this build. Unlike Open it never
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
	empty, err := inspectBaselinePath(path)
	if err != nil {
		return err
	}
	if empty {
		return incompatibleBaseline("database %q is empty", path)
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
	initialize := false
	created, err := fileutil.OpenPrivateExclusive(stablePath)
	if err == nil {
		initialize = true
		if err := created.Close(); err != nil {
			return nil, fmt.Errorf("close new database %q: %w", path, err)
		}
	} else if errors.Is(err, os.ErrExist) {
		// Compatibility inspection must precede any permission normalization.
		// A rejected database is operator data, not ours to chmod or otherwise
		// mutate. First prove, without chmod or writes, that the existing entry is
		// the current user's already-private stable regular file.
		stablePath, err = fileutil.ValidateExistingPrivateFile(stablePath)
		if err != nil {
			return nil, fmt.Errorf("validate existing database %q: %w", path, err)
		}
		empty, inspectErr := inspectBaselinePath(stablePath)
		if inspectErr != nil {
			return nil, inspectErr
		}
		// Only a compatible baseline (or a truly schema-empty file) becomes KMS
		// state. At that point enforce the private-file contract before reopening
		// it read-write.
		stablePath, err = fileutil.SecureExistingPrivateFile(stablePath)
		if err != nil {
			return nil, fmt.Errorf("secure existing database %q: %w", path, err)
		}
		initialize = empty
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
	databaseURI := sqliteFileURI(filepath.ToSlash(absPath))
	dsn := fmt.Sprintf(
		"%s?_txlock=immediate&_pragma=busy_timeout(%d)&_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=secure_delete(ON)&_pragma=synchronous(%s)",
		databaseURI, busy.Milliseconds(), sync,
	)
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger:                 logger.Default.LogMode(logger.Silent),
		SkipDefaultTransaction: true,
	})
	if err != nil {
		return nil, fmt.Errorf("open database %q: %w", path, err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("access database %q: %w", path, err)
	}
	// Own the pool policy explicitly. database/sql exposes the current max-open
	// value but has no max-idle getter, while physical purge temporarily changes
	// both and must restore them exactly.
	sqlDB.SetMaxOpenConns(sqlStoreMaxOpenConns)
	sqlDB.SetMaxIdleConns(sqlStoreMaxIdleConns)
	s := &SQLStore{
		db:               db,
		poolMaxOpenConns: sqlStoreMaxOpenConns,
		poolMaxIdleConns: sqlStoreMaxIdleConns,
	}
	if initialize {
		err = initializeBaseline(db)
	} else {
		err = verifyBaselineDB(db)
	}
	if err == nil {
		// Recover from a crash after a logically committed purge but before its
		// WAL cleanup. The store is not returned to callers until every frame has
		// been checkpointed and the WAL has been truncated.
		err = db.WithContext(context.Background()).Connection(func(conn *gorm.DB) error {
			return truncateWAL(conn)
		})
		if err != nil {
			err = fmt.Errorf("prepare database %q for service: %w", path, err)
		}
	}
	if err != nil {
		if sqlDB, e := db.DB(); e == nil {
			_ = sqlDB.Close()
		}
		return nil, err
	}
	return s, nil
}

// sqliteFileURI returns a SQLite file URI for an absolute path whose directory
// separators have already been normalized to slashes. A Windows drive path is
// an absolute URI path, not an authority: C:/kms.db must therefore become
// file:///C:/kms.db (empty host), never file://C:/kms.db (host "C:").
// url.URL performs the required escaping so literal ?, #, and % bytes in a
// filename cannot be reinterpreted as URI query, fragment, or escape syntax.
func sqliteFileURI(slashPath string) string {
	if isWindowsDriveSlashPath(slashPath) {
		slashPath = "/" + slashPath
	}
	return (&url.URL{Scheme: "file", Path: slashPath}).String()
}

func isWindowsDriveSlashPath(path string) bool {
	if len(path) < 3 || path[1] != ':' || path[2] != '/' {
		return false
	}
	drive := path[0]
	return drive >= 'A' && drive <= 'Z' || drive >= 'a' && drive <= 'z'
}

func incompatibleBaseline(format string, args ...any) error {
	return fmt.Errorf("incompatible 0.3.x database baseline: "+format+"; create a fresh database for KMS 0.3.x", args...)
}

func inspectBaselinePath(path string) (bool, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return false, fmt.Errorf("resolve database path %q: %w", path, err)
	}
	databaseURI := sqliteFileURI(filepath.ToSlash(abs))
	db, err := gorm.Open(sqlite.Open(databaseURI+"?mode=ro&_pragma=query_only(1)"), &gorm.Config{
		Logger:                 logger.Default.LogMode(logger.Silent),
		SkipDefaultTransaction: true,
	})
	if err != nil {
		return false, incompatibleBaseline("cannot inspect database %q read-only: %v", path, err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return false, fmt.Errorf("access database %q: %w", path, err)
	}
	defer func() { _ = sqlDB.Close() }()
	return inspectBaselineDB(db)
}

type baselineSchemaObject struct {
	Type      string
	Name      string
	TableName string
	SQL       string
}

func readBaselineSchema(db *gorm.DB) ([]baselineSchemaObject, error) {
	var objects []baselineSchemaObject
	err := db.Raw(`SELECT type, name, tbl_name AS table_name, COALESCE(sql, '') AS sql
		FROM sqlite_master
		WHERE type IN ('table', 'index', 'trigger', 'view')
		  AND name NOT GLOB 'sqlite_*'
		ORDER BY type, name`).Scan(&objects).Error
	return objects, err
}

func materializeBaseline(tx *gorm.DB) error {
	if err := tx.AutoMigrate(&schemaMigrationModel{}); err != nil {
		return fmt.Errorf("create schema version table: %w", err)
	}
	if err := tx.AutoMigrate(autoMigrateModels...); err != nil {
		return fmt.Errorf("create 0.3.x baseline: %w", err)
	}
	if err := tx.Exec(changeLogDDL).Error; err != nil {
		return fmt.Errorf("create change_log: %w", err)
	}
	if err := tx.Exec(changeLogIndexDDL).Error; err != nil {
		return fmt.Errorf("create change_log index: %w", err)
	}
	if err := tx.Exec(changeLogNamespaceIndexDDL).Error; err != nil {
		return fmt.Errorf("create change_log namespace index: %w", err)
	}
	if err := tx.Create(&schemaMigrationModel{Version: schemaVersion, AppliedAt: fmtTime(time.Now())}).Error; err != nil {
		return fmt.Errorf("record schema version: %w", err)
	}
	return nil
}

func referenceBaselineSchema() ([]baselineSchemaObject, error) {
	// Shared in-memory databases are keyed by name. A timestamp is not unique
	// enough here: Windows clocks can return the same value to concurrent calls,
	// making otherwise independent verifiers race over one schema_migrations
	// table. The process-local sequence is collision-free for the lifetime in
	// which an in-memory database can exist.
	dsn := fmt.Sprintf("file:kms-baseline-reference-%d?mode=memory&cache=shared", baselineReferenceID.Add(1))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger:                 logger.Default.LogMode(logger.Silent),
		SkipDefaultTransaction: true,
	})
	if err != nil {
		return nil, fmt.Errorf("create baseline verifier: %w", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("access baseline verifier: %w", err)
	}
	defer func() { _ = sqlDB.Close() }()
	if err := db.Transaction(materializeBaseline); err != nil {
		return nil, fmt.Errorf("materialize baseline verifier: %w", err)
	}
	return readBaselineSchema(db)
}

func inspectBaselineDB(db *gorm.DB) (bool, error) {
	actual, err := readBaselineSchema(db)
	if err != nil {
		return false, incompatibleBaseline("cannot inspect schema: %v", err)
	}
	// Initialization is allowed only for a database with no user schema objects
	// whatsoever. A view, trigger, or standalone user index is state just as a
	// table is and must never be overwritten by KMS initialization.
	if len(actual) == 0 {
		return true, nil
	}

	expected, err := referenceBaselineSchema()
	if err != nil {
		return false, err
	}
	if len(actual) != len(expected) {
		return false, incompatibleBaseline("physical schema has %d objects; expected %d", len(actual), len(expected))
	}
	for i := range actual {
		if actual[i] != expected[i] {
			return false, incompatibleBaseline("physical schema differs at %s %q", actual[i].Type, actual[i].Name)
		}
	}

	var stamps []schemaMigrationModel
	if err := db.Order("version ASC").Find(&stamps).Error; err != nil {
		return false, incompatibleBaseline("cannot read schema version: %v", err)
	}
	if len(stamps) != 1 || stamps[0].Version != schemaVersion {
		versions := make([]string, 0, len(stamps))
		for _, stamp := range stamps {
			versions = append(versions, strconv.Itoa(stamp.Version))
		}
		return false, incompatibleBaseline("schema stamp must be exactly %d (found %s)", schemaVersion, strings.Join(versions, ", "))
	}
	return false, nil
}

func verifyBaselineDB(db *gorm.DB) error {
	empty, err := inspectBaselineDB(db)
	if err != nil {
		return err
	}
	if empty {
		return incompatibleBaseline("database is empty")
	}
	return nil
}

func initializeBaseline(db *gorm.DB) error {
	return initializeBaselineWithVerifier(db, verifyBaselineDB)
}

func initializeBaselineWithVerifier(db *gorm.DB, verify func(*gorm.DB) error) error {
	return db.Transaction(func(tx *gorm.DB) error {
		var count int64
		if err := tx.Raw("SELECT COUNT(*) FROM sqlite_master WHERE type IN ('table', 'index', 'trigger', 'view') AND name NOT GLOB 'sqlite_*'").Scan(&count).Error; err != nil {
			return fmt.Errorf("inspect empty database: %w", err)
		}
		if count != 0 {
			return incompatibleBaseline("database changed while it was being opened")
		}
		if err := materializeBaseline(tx); err != nil {
			return err
		}
		// Exact physical-schema and stamp verification belongs inside this same
		// transaction. If model materialization ever drifts from the accepted
		// baseline, returning the verifier error rolls every DDL change back.
		return verify(tx)
	})
}

type walCheckpointResult struct {
	Busy         int
	Log          int
	Checkpointed int
}

// truncateWAL checkpoints every committed frame into the main database and
// truncates the live WAL artifact. SQLite documents a successful TRUNCATE
// checkpoint as the exact tuple (busy, log, checkpointed) = (0, 0, 0); merely
// executing the PRAGMA without inspecting its row can silently accept BUSY.
// db must be pinned to one physical connection when concurrent use is possible.
func truncateWAL(db *gorm.DB) error {
	var result walCheckpointResult
	row := db.Raw("PRAGMA main.wal_checkpoint(TRUNCATE)").Row()
	if err := row.Scan(&result.Busy, &result.Log, &result.Checkpointed); err != nil {
		return fmt.Errorf("truncate SQLite WAL: %w", err)
	}
	if result.Busy != 0 || result.Log != 0 || result.Checkpointed != 0 {
		return fmt.Errorf("truncate SQLite WAL incomplete (busy=%d, log=%d, checkpointed=%d)", result.Busy, result.Log, result.Checkpointed)
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
	if cl.AffectedVersionsJSON == "" {
		cl.AffectedVersionsJSON = "[]"
	}
	if err := tx.Omit(clause.Associations).Create(cl).Error; err != nil {
		return 0, err
	}
	return uint64(cl.Revision), nil
}
