package cli

import (
	"context"
	"database/sql"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Suhaibinator/kms/internal/core"
	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/fileutil"
	"github.com/Suhaibinator/kms/internal/keyutil"
	"github.com/Suhaibinator/kms/internal/storage"
)

// kv is one source record from a SuhaibParameterStore export.
type kv struct {
	Key   string
	Value string
}

// importEntry is a source record mapped to its destination resource reference.
type importEntry struct {
	Key   string
	Value string
	Ref   domain.Ref
}

// importResult is one row of the mapping report.
type importResult struct {
	Key   string
	Path  string
	Token string // empty for dry-run
}

func (c *CLI) cmdImport(args []string) int {
	fs := c.newFlags("import")
	from := fs.String("from", "", "source export: JSON file or SuhaibParameterStore SQLite db")
	namespace := fs.String("namespace", "", "destination namespace env/app, e.g. prod/gradethis")
	env := fs.String("env", "", "destination environment (alternative to --namespace)")
	app := fs.String("app", "", "destination application (alternative to --namespace)")
	dryRun := fs.Bool("dry-run", false, "print old key -> new path mapping without writing")
	report := fs.String("report", "", "write the mapping report to this file instead of stdout")
	r := c.serverSettings(fs, "storage.sqlite_path", "encryption.kek_file")
	c.setUsage(fs, "import [flags]",
		"Import keys from a SuhaibParameterStore export into a namespace as secrets. --dry-run prints the old key -> new path mapping and needs no database or master key.", true)
	if !c.parseFlags(fs, args) {
		return 2
	}
	if !c.rejectPositionals() {
		return 2
	}
	cfg, _, _, err := r.resolve()
	if err != nil {
		return c.fail("%v", err)
	}
	if *from == "" {
		return c.fail("--from is required")
	}
	ns, err := resolveImportNamespace(*namespace, *env, *app)
	if err != nil {
		return c.fail("%v", err)
	}

	items, err := loadImportSource(*from)
	if err != nil {
		return c.fail("reading source: %v", err)
	}
	entries, err := buildImportEntries(ns, items)
	if err != nil {
		return c.fail("%v", err)
	}

	out, closeReport, err := c.reportWriter(*report)
	if err != nil {
		return c.fail("%v", err)
	}
	reportClosed := false
	defer func() {
		if !reportClosed {
			_ = closeReport()
		}
	}()

	if *dryRun {
		results := make([]importResult, len(entries))
		for i, e := range entries {
			results[i] = importResult{Key: e.Key, Path: e.Ref.String()}
		}
		if err := writeImportReport(out, results, false); err != nil {
			return c.fail("writing import report: %v", err)
		}
		reportClosed = true
		if err := closeReport(); err != nil {
			return c.fail("closing import report: %v", err)
		}
		_, _ = fmt.Fprintf(c.Stderr, "Dry run: %d keys would be imported into %s. No data written.\n", len(entries), ns)
		return 0
	}

	// Real import: open the store, unseal, and write each value as a secret.
	if err := c.requireSQLitePath(cfg); err != nil {
		return c.fail("%v", err)
	}
	store, err := storage.Open(cfg.Storage.SQLitePath)
	if err != nil {
		return c.fail("opening destination database: %v", err)
	}
	defer func() { _ = store.Close() }()

	ctx := context.Background()
	keyring, err := c.unseal(ctx, store, cfg.Encryption.KEKFile, false)
	if err != nil {
		return c.fail("acquiring master key: %v", err)
	}
	svc := core.New(store, c.quietLogger(), Version)
	svc.SetKeyring(keyring)

	pr := core.Principal{Identity: domain.Identity{Name: "import", Kind: domain.IdentityKindAdmin}}

	// A parameter/secret write requires the namespace to already exist. Create it
	// if missing so import works against a fresh database; an existing namespace
	// is left untouched.
	if _, err := svc.CreateNamespace(ctx, pr, ns, "imported from SuhaibParameterStore", nil); err != nil && !errors.Is(err, domain.ErrAlreadyExists) {
		return c.fail("ensuring namespace %s: %v", ns, err)
	}

	results := make([]importResult, 0, len(entries))
	for _, e := range entries {
		res, err := svc.PutSecret(ctx, pr, core.PutSecretInput{
			Ref:           e.Ref,
			Value:         []byte(e.Value),
			ContentType:   "text/plain",
			GenerateToken: true,
		})
		if err != nil {
			return c.fail("importing %s -> %s: %v", e.Key, e.Ref, err)
		}
		results = append(results, importResult{Key: e.Key, Path: e.Ref.String(), Token: res.AccessToken})
	}
	if err := writeImportReport(out, results, true); err != nil {
		return c.fail("writing one-time import token report: %v", err)
	}
	reportClosed = true
	if err := closeReport(); err != nil {
		return c.fail("closing one-time import token report: %v", err)
	}
	_, _ = fmt.Fprintf(c.Stderr, "Imported %d secrets into %s.\n", len(results), ns)
	return 0
}

// resolveImportNamespace resolves the destination namespace from either
// --namespace env/app or the --env/--app pair (mutually exclusive with it).
func resolveImportNamespace(namespace, env, app string) (domain.NamespaceRef, error) {
	if namespace != "" {
		if env != "" || app != "" {
			return domain.NamespaceRef{}, fmt.Errorf("pass either --namespace or --env/--app, not both")
		}
		ns, err := keyutil.ParseNamespace(namespace)
		if err != nil {
			return domain.NamespaceRef{}, fmt.Errorf("invalid --namespace %q: %v", namespace, err)
		}
		return ns, nil
	}
	if env == "" || app == "" {
		return domain.NamespaceRef{}, fmt.Errorf("--namespace (or both --env and --app) is required")
	}
	ns := domain.NamespaceRef{Env: env, App: app}
	if err := keyutil.ValidateNamespace(ns); err != nil {
		return domain.NamespaceRef{}, fmt.Errorf("invalid namespace: %v", err)
	}
	return ns, nil
}

// reportWriter returns the destination for the mapping report: the named file
// (refusing to overwrite), or stdout when path is empty.
func (c *CLI) reportWriter(path string) (io.Writer, func() error, error) {
	if path == "" {
		return c.Stdout, func() error { return nil }, nil
	}
	f, err := fileutil.OpenPrivateExclusive(path)
	if err != nil {
		return nil, nil, fmt.Errorf("opening report file %s: %w", path, err)
	}
	return f, f.Close, nil
}

// writeImportReport renders the mapping. When withTokens is set it includes the
// per-secret access tokens and a one-time warning.
func writeImportReport(w io.Writer, results []importResult, withTokens bool) error {
	if withTokens {
		if _, err := fmt.Fprintln(w, "# import mapping: old key -> new path -> access token"); err != nil {
			return err
		}
	} else {
		if _, err := fmt.Fprintln(w, "# import mapping (dry run): old key -> new path"); err != nil {
			return err
		}
	}
	for _, r := range results {
		if withTokens {
			if _, err := fmt.Fprintf(w, "%s -> %s -> %s\n", r.Key, r.Path, r.Token); err != nil {
				return err
			}
		} else {
			if _, err := fmt.Fprintf(w, "%s -> %s\n", r.Key, r.Path); err != nil {
				return err
			}
		}
	}
	if withTokens {
		if _, err := fmt.Fprintln(w, "# WARNING: access tokens are shown once here and are not recoverable. Update app configs now."); err != nil {
			return err
		}
	}
	return nil
}

// slug converts a flat key into a path segment: lowercased with underscores
// replaced by hyphens (e.g. gradethis_TWILIO_SID -> gradethis-twilio-sid).
func slug(key string) string {
	return strings.ToLower(strings.ReplaceAll(key, "_", "-"))
}

// buildImportEntries maps each source key to a relative key slug(key) within
// the destination namespace, validating keys and rejecting collisions. Output
// is sorted by key for a deterministic report.
func buildImportEntries(ns domain.NamespaceRef, items []kv) ([]importEntry, error) {
	byPath := map[string][]string{}
	out := make([]importEntry, 0, len(items))
	var invalid []string
	for _, it := range items {
		if it.Key == "" {
			invalid = append(invalid, "(empty key)")
			continue
		}
		key := slug(it.Key)
		if err := keyutil.ValidateKey(key); err != nil {
			invalid = append(invalid, fmt.Sprintf("%q (%v)", it.Key, err))
			continue
		}
		ref := domain.Ref{NS: ns, Key: key}
		byPath[ref.String()] = append(byPath[ref.String()], it.Key)
		out = append(out, importEntry{Key: it.Key, Value: it.Value, Ref: ref})
	}
	if len(invalid) > 0 {
		sort.Strings(invalid)
		return nil, fmt.Errorf("cannot map keys to valid keys: %s", strings.Join(invalid, "; "))
	}

	var collisions []string
	for p, keys := range byPath {
		if len(keys) > 1 {
			sort.Strings(keys)
			collisions = append(collisions, fmt.Sprintf("%s <- [%s]", p, strings.Join(keys, ", ")))
		}
	}
	if len(collisions) > 0 {
		sort.Strings(collisions)
		return nil, fmt.Errorf("path collisions (distinct keys slug to the same key): %s", strings.Join(collisions, "; "))
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

// loadImportSource reads an export as either a SQLite database (parameters
// table) or a JSON object/array, detected by extension or magic bytes.
func loadImportSource(path string) ([]kv, error) {
	if looksLikeSQLite(path) {
		return loadFromSQLite(path)
	}
	return loadFromJSON(path)
}

func looksLikeSQLite(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".db", ".sqlite", ".sqlite3":
		return true
	}
	return validateSQLiteFile(path) == nil
}

// loadFromSQLite reads key/value rows from the old store's parameters table,
// opening the database read-only so the source is never modified.
func loadFromSQLite(path string) ([]kv, error) {
	db, err := sql.Open("sqlite", fmt.Sprintf("file:%s?mode=ro", path))
	if err != nil {
		return nil, fmt.Errorf("opening source database: %w", err)
	}
	defer func() { _ = db.Close() }()

	rows, err := db.Query("SELECT key, value FROM parameters ORDER BY key")
	if err != nil {
		return nil, fmt.Errorf("reading parameters table: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []kv
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out = append(out, kv{Key: k, Value: v})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, errors.New("source database has no rows in the parameters table")
	}
	return out, nil
}

// loadFromJSON parses either a {"KEY":"value",...} object or a
// [{"key":...,"value":...}] array.
func loadFromJSON(path string) ([]kv, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return nil, errors.New("source JSON is empty")
	}
	switch trimmed[0] {
	case '{':
		var obj map[string]jsontext.Value
		if err := json.Unmarshal(raw, &obj); err != nil {
			return nil, fmt.Errorf("parsing JSON object: %w", err)
		}
		out := make([]kv, 0, len(obj))
		for k, v := range obj {
			out = append(out, kv{Key: k, Value: coerceJSONValue(v)})
		}
		return out, nil
	case '[':
		var arr []struct {
			Key   string         `json:"key"`
			Value jsontext.Value `json:"value"`
		}
		if err := json.Unmarshal(raw, &arr); err != nil {
			return nil, fmt.Errorf("parsing JSON array: %w", err)
		}
		out := make([]kv, 0, len(arr))
		for _, e := range arr {
			out = append(out, kv{Key: e.Key, Value: coerceJSONValue(e.Value)})
		}
		return out, nil
	default:
		return nil, errors.New("source JSON must be an object or an array")
	}
}

// coerceJSONValue renders a JSON value as the string to import: a JSON string
// yields its unquoted text; anything else keeps its raw JSON encoding.
func coerceJSONValue(raw jsontext.Value) string {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	return string(raw)
}
