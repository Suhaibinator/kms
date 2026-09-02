package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
	"github.com/Suhaibinator/kms/internal/core"
	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/fileutil"
	"github.com/Suhaibinator/kms/internal/storage"
)

const (
	// auditListDefaultLimit is one page of `audit list`, matching the store's
	// own default so the CLI and the API agree on what "a page" means.
	auditListDefaultLimit = 100
	// auditListMaxLimit is the store's listing cap. A larger --limit would be
	// clamped server-side, so the command rejects it instead of silently
	// returning fewer rows than were asked for.
	auditListMaxLimit = 1000
	// auditExportPageSize is the page size `audit export` streams with: the
	// server's maximum, so a full export makes the fewest round trips.
	auditExportPageSize = 1000
	// auditFollowDefaultInterval and auditFollowMinInterval bound --interval.
	// The floor exists because the tail is a poll, not a stream: a sub-second
	// interval would turn one operator's terminal into a query loop.
	auditFollowDefaultInterval = 5 * time.Second
	auditFollowMinInterval     = time.Second
	// auditPollTimeout bounds one --follow poll. It is the 30s every other CLI
	// call allows, but derived from the follow context so an interrupt also
	// ends the request in flight.
	auditPollTimeout = 30 * time.Second
)

// auditTableHeaders are the columns `audit list` prints. They are shared by the
// first batch of a --follow tail and every page of a plain listing.
var auditTableHeaders = []string{"TIME", "EVENT", "DECISION", "ACTOR", "NAMESPACE", "KEY", "REQUEST_ID"}

// cmdAudit dispatches the audit subcommands. list and export query a running
// server as an admin; prune runs offline against the database file, because
// retiring history is a host-level operation like backup or rotate-kek — it
// needs the file, not a credential, and must work when the server is down.
func (c *CLI) cmdAudit(args []string) int {
	if len(args) == 0 {
		c.auditUsage()
		return 2
	}
	action, rest := args[0], args[1:]
	switch action {
	case "list":
		return c.cmdAuditList(rest)
	case "export":
		return c.cmdAuditExport(rest)
	case "prune":
		return c.cmdAuditPrune(rest)
	case "help", "-h", "--help":
		c.auditUsage()
		return 0
	default:
		_, _ = fmt.Fprintf(c.Stderr, "unknown audit subcommand %q\n\n", action)
		c.auditUsage()
		return 2
	}
}

func (c *CLI) auditUsage() {
	_, _ = fmt.Fprint(c.Stderr, `parameter-store audit — read, export, and retire audit history

Usage:
  parameter-store audit <action> [flags]

Actions:
  list                    List audit events newest first, or tail them with --follow
                          (--env, --app, --key-prefix, --actor, --event, --decision,
                          --since, --until, --limit, --page-token, --interval).
  export --out FILE       Stream every matching event to a JSON Lines file. The
                          destination is created exclusively and never overwritten.
  prune --older-than DUR  Retire events older than a cutoff from the database file,
                          archiving them first with --archive DIR (--dry-run,
                          --sqlite-path).

list and export talk to a running server and need an admin credential (or an
identity holding admin:audit:read). prune runs on the server host against the
database directly; the server does not need to be running.

Audit history is evidence. prune without --archive deletes it outright, so pass
an archive directory unless discarding the rows is the intent.
`)
}

// --- shared filters --------------------------------------------------------

// auditFilterFlags are the query filters `audit list` and `audit export` share.
// Both narrow the same server-side query, so they are registered, validated,
// and translated to the wire in one place — a filter that meant different
// things in the two commands would make an export unreproducible from a
// listing.
type auditFilterFlags struct {
	env       string
	app       string
	keyPrefix string
	actor     string
	event     string
	decision  string
	since     string
	until     string
}

func addAuditFilterFlags(fs *flag.FlagSet) *auditFilterFlags {
	f := &auditFilterFlags{}
	fs.StringVar(&f.env, "env", "", "exact environment `label` (default: any)")
	fs.StringVar(&f.app, "app", "", "exact application `label` (default: any)")
	fs.StringVar(&f.keyPrefix, "key-prefix", "", "resource key `prefix` within the namespace (default: any)")
	fs.StringVar(&f.actor, "actor", "", "exact actor identity `name` (default: any)")
	fs.StringVar(&f.event, "event", "", "exact event `type`, e.g. secret.read (default: any)")
	fs.StringVar(&f.decision, "decision", "", "`outcome` to match: allow, deny, or error (default: any)")
	fs.StringVar(&f.since, "since", "", "start of the window: a duration ago (24h, 7d) or an RFC 3339 `instant`")
	fs.StringVar(&f.until, "until", "", "end of the window: a duration ago (1h, 1d) or an RFC 3339 `instant`")
	return f
}

// request translates the filters into the wire request. Everything the client
// can check, it checks: a mistyped --decision would otherwise reach a server
// that treats an unknown value as a narrowing filter, and an empty result
// looks exactly like a clean audit log. now anchors the relative --since and
// --until forms so both ends of one window share a single clock reading.
func (f *auditFilterFlags) request(now time.Time) (*kmsv1.ListAuditEventsRequest, error) {
	if !domain.ValidAuditDecision(f.decision) {
		return nil, fmt.Errorf("invalid --decision %q (want allow, deny, or error)", f.decision)
	}
	from, err := parseSinceUntil("--since", f.since, now)
	if err != nil {
		return nil, err
	}
	to, err := parseSinceUntil("--until", f.until, now)
	if err != nil {
		return nil, err
	}
	if !from.IsZero() && !to.IsZero() && to.Before(from) {
		return nil, fmt.Errorf("--until %q is before --since %q", f.until, f.since)
	}
	return &kmsv1.ListAuditEventsRequest{
		Env:           f.env,
		App:           f.app,
		KeyPrefix:     f.keyPrefix,
		ActorIdentity: f.actor,
		EventType:     f.event,
		Decision:      f.decision,
		FromUnixMs:    unixMSOrZero(from),
		ToUnixMs:      unixMSOrZero(to),
	}, nil
}

// parseSinceUntil parses one end of an audit time window: a Go duration
// ("24h"), a bare day count ("7d", the spelling --ttl already accepts), or an
// RFC 3339 instant. The first two are relative and mean "that long before
// now" — an operator who writes "--since 24h" means the last day, not a
// duration into the future. An empty value leaves that end unbounded.
func parseSinceUntil(flagName, value string, now time.Time) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, nil
	}
	if d, err := time.ParseDuration(value); err == nil {
		if d < 0 {
			return time.Time{}, fmt.Errorf("invalid %s %q (a duration means that long ago and must not be negative)", flagName, value)
		}
		return now.Add(-d), nil
	}
	if days, ok := strings.CutSuffix(value, "d"); ok {
		if n, err := strconv.Atoi(days); err == nil && n >= 0 {
			return now.Add(-time.Duration(n) * 24 * time.Hour), nil
		}
	}
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid %s %q (use a duration ago like 24h or 7d, or an RFC 3339 instant)", flagName, value)
	}
	return t, nil
}

// unixMSOrZero renders a bound for the wire, where zero means "unbounded".
func unixMSOrZero(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

// auditRecordFromProto projects a wire event onto the canonical record that
// `audit export`, `audit list -o json`, and the server-side archive all share,
// so the three can be concatenated and parsed together.
//
// One field cannot be filled from the wire: AuditEvent carries no namespace
// incarnation ID, which the API deliberately does not publish. Records that
// came from a server therefore spell resource.namespace_id as 0, and only the
// archive written by the server itself carries the real value.
func auditRecordFromProto(ev *kmsv1.AuditEvent) core.AuditRecord {
	return core.AuditRecord{
		ID:        ev.GetId(),
		CreatedAt: time.UnixMilli(ev.GetCreatedAtUnixMs()).UTC(),
		Event:     ev.GetEventType(),
		Decision:  ev.GetDecision(),
		Actor:     core.AuditActor{Identity: ev.GetActorIdentity(), Type: ev.GetActorType()},
		Resource: core.AuditResource{
			Type:    ev.GetResourceType(),
			Env:     ev.GetResourceEnv(),
			App:     ev.GetResourceApp(),
			Key:     ev.GetResourceKey(),
			Version: ev.GetResourceVersion(),
		},
		SourceIP:  ev.GetSourceIp(),
		UserAgent: ev.GetUserAgent(),
		RequestID: ev.GetRequestId(),
		Metadata:  core.AuditMetadataValue(ev.GetMetadataJson()),
	}
}

// auditTableRow renders one event as the table's columns. Every field that a
// global event (an auth failure, a KEK rotation) legitimately leaves empty
// prints as "-", so a blank column never reads as output the command failed to
// produce.
func auditTableRow(ev *kmsv1.AuditEvent) []string {
	return []string{
		time.UnixMilli(ev.GetCreatedAtUnixMs()).UTC().Format(time.RFC3339),
		auditColumn(ev.GetEventType()),
		auditColumn(ev.GetDecision()),
		auditColumn(ev.GetActorIdentity()),
		auditNamespaceColumn(ev),
		auditColumn(ev.GetResourceKey()),
		auditColumn(ev.GetRequestId()),
	}
}

func auditColumn(value string) string {
	if value == "" {
		return "-"
	}
	return value
}

// auditNamespaceColumn collapses the denormalized env/app pair the way every
// other listing prints a namespace. A row with only one half cannot name a
// namespace and prints "-" rather than a misleading "prod/".
func auditNamespaceColumn(ev *kmsv1.AuditEvent) string {
	env, app := ev.GetResourceEnv(), ev.GetResourceApp()
	if env == "" || app == "" {
		return "-"
	}
	return env + "/" + app
}

// --- audit list ------------------------------------------------------------

func (c *CLI) cmdAuditList(args []string) int {
	fs := c.newFlags("audit list")
	cf := addConnFlags(c, fs)
	filters := addAuditFilterFlags(fs)
	limit := fs.Int("limit", auditListDefaultLimit, "maximum number of `events` per page (at most 1000)")
	pageToken := fs.String("page-token", "", "continue a listing from this `token`")
	follow := fs.Bool("follow", false, "keep polling for new events until interrupted")
	interval := fs.Duration("interval", auditFollowDefaultInterval, "how often --follow polls, as a `duration` (minimum 1s)")
	c.setUsage(fs, "audit list [flags]",
		"List audit events newest first, or follow the log with --follow as new events arrive.", false)
	if !c.parseFlags(fs, args) {
		return 2
	}
	if !c.rejectPositionals() {
		return 2
	}
	req, err := filters.request(time.Now())
	if err != nil {
		return c.failUsage("%v", err)
	}
	if *limit <= 0 || *limit > auditListMaxLimit {
		return c.failUsage("--limit must be between 1 and %d", auditListMaxLimit)
	}
	req.PageSize = int32(*limit)
	req.PageToken = *pageToken
	if *follow {
		// A page token is a position in a listing that runs backwards in time;
		// a tail runs forwards from the newest event. Resuming one as the
		// other would print a page and then jump, so refuse rather than guess.
		if *pageToken != "" {
			return c.failUsage("--follow tails the newest events and cannot resume a --page-token")
		}
		if *interval < auditFollowMinInterval {
			return c.failUsage("--interval must be at least %s", auditFollowMinInterval)
		}
	}

	conn, err := c.dialConn(cf)
	if err != nil {
		return c.failErr("", err)
	}
	defer func() { _ = conn.Close() }()
	client := kmsv1.NewAdminServiceClient(conn)

	if *follow {
		return c.followAuditEvents(client, cf, req, *interval)
	}
	ctx, cancel := callContext()
	defer cancel()
	resp, err := client.ListAuditEvents(cf.authCtx(ctx), req)
	if err != nil {
		return c.failErr("audit list", err)
	}
	if c.jsonOutput() {
		records := auditRecordsFromProto(resp.GetEvents())
		return c.printList(records, resp.GetNextPageToken())
	}
	c.printTable(auditTableHeaders, auditTableRows(resp.GetEvents()))
	if next := resp.GetNextPageToken(); next != "" {
		c.info("More events match; continue with --page-token %s", next)
	}
	return 0
}

func auditRecordsFromProto(events []*kmsv1.AuditEvent) []core.AuditRecord {
	records := make([]core.AuditRecord, 0, len(events))
	for _, ev := range events {
		records = append(records, auditRecordFromProto(ev))
	}
	return records
}

func auditTableRows(events []*kmsv1.AuditEvent) [][]string {
	rows := make([][]string, 0, len(events))
	for _, ev := range events {
		rows = append(rows, auditTableRow(ev))
	}
	return rows
}

// followAuditEvents tails the audit log by polling. There is no audit stream
// RPC, so --follow re-queries: it prints the first page oldest-first — the
// order a tail is read in — and then asks only for events at or after the
// newest one it has printed, discarding by id the rows it has already shown.
// The id comparison, not the timestamp, is what prevents a duplicate:
// timestamps are not unique, and the millisecond bound on the wire is coarser
// than the stored one, so the query deliberately re-fetches the boundary.
//
// It ends on SIGINT/SIGTERM, which is how an operator ends a tail, and that is
// a success rather than an error.
func (c *CLI) followAuditEvents(client kmsv1.AdminServiceClient, cf *connFlags, req *kmsv1.ListAuditEventsRequest, interval time.Duration) int {
	ctx, stopSignals := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stopSignals()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	var lastID, lastMs int64
	header := !c.jsonOutput()
	for {
		events, err := c.pollAuditEvents(ctx, client, cf, req, lastID, lastMs)
		if err != nil {
			if ctx.Err() != nil {
				return exitOK
			}
			return c.failErr("audit list", err)
		}
		for _, ev := range events {
			if id := ev.GetId(); id > lastID {
				lastID, lastMs = id, ev.GetCreatedAtUnixMs()
			}
		}
		if code := c.printFollowedAuditEvents(events, header); code != exitOK {
			return code
		}
		header = false

		select {
		case <-ctx.Done():
			return exitOK
		case <-c.followStop:
			return exitOK
		case <-ticker.C:
		}
	}
}

// pollAuditEvents fetches every event newer than lastID and returns them
// oldest first. It keeps paging until it reaches an event it has already
// printed, so a burst larger than one page is not silently dropped. req is the
// command's own request value and is reused (with its paging fields rewritten)
// across polls; nothing else holds a reference to it.
func (c *CLI) pollAuditEvents(ctx context.Context, client kmsv1.AdminServiceClient, cf *connFlags, req *kmsv1.ListAuditEventsRequest, lastID, lastMs int64) ([]*kmsv1.AuditEvent, error) {
	if lastMs > 0 {
		req.FromUnixMs = lastMs
	}
	var fresh []*kmsv1.AuditEvent
	for token := ""; ; {
		req.PageToken = token
		callCtx, cancel := context.WithTimeout(ctx, auditPollTimeout)
		resp, err := client.ListAuditEvents(cf.authCtx(callCtx), req)
		cancel()
		if err != nil {
			return nil, err
		}
		reachedSeen := false
		for _, ev := range resp.GetEvents() {
			if ev.GetId() <= lastID {
				reachedSeen = true
				break
			}
			fresh = append(fresh, ev)
		}
		token = resp.GetNextPageToken()
		if reachedSeen || token == "" {
			break
		}
	}
	// The server orders newest first; a tail reads oldest first.
	slices.Reverse(fresh)
	return fresh, nil
}

// printFollowedAuditEvents writes one poll's worth of events. In JSON mode a
// tail emits JSON Lines — one record per line — rather than the single
// document every other JSON result is: a stream has no last element to close
// the array with, and a consumer of a tail wants each event as it lands.
// header asks for the table's column row, which a tail prints once.
func (c *CLI) printFollowedAuditEvents(events []*kmsv1.AuditEvent, header bool) int {
	if c.jsonOutput() {
		if len(events) == 0 {
			return exitOK
		}
		if err := core.WriteAuditJSONL(c.Stdout, auditRecordsFromProto(events)); err != nil {
			return c.fail("writing audit events: %v", err)
		}
		return exitOK
	}
	if header {
		c.printTable(auditTableHeaders, auditTableRows(events))
		return exitOK
	}
	if len(events) == 0 {
		return exitOK
	}
	writeAlignedTable(c.Stdout, nil, auditTableRows(events))
	return exitOK
}

// --- audit export ----------------------------------------------------------

func (c *CLI) cmdAuditExport(args []string) int {
	fs := c.newFlags("audit export")
	cf := addConnFlags(c, fs)
	filters := addAuditFilterFlags(fs)
	out := fs.String("out", "", "destination JSON Lines `file`; it must not already exist")
	c.setUsage(fs, "audit export --out FILE.jsonl [flags]",
		"Stream every matching audit event to a JSON Lines file, one canonical record per line, paging through the whole result set.", false)
	if !c.parseFlags(fs, args) {
		return 2
	}
	if !c.rejectPositionals() {
		return 2
	}
	req, err := filters.request(time.Now())
	if err != nil {
		return c.failUsage("%v", err)
	}
	if *out == "" {
		return c.failUsage("--out is required")
	}
	req.PageSize = auditExportPageSize
	// Refuse a taken destination before spending a full export on it. The
	// publication below re-checks atomically, so a file that appears in the
	// meantime is still refused.
	if fileExists(*out) {
		return c.failErr("", fmt.Errorf("output file %s: %w; refusing to overwrite", *out, os.ErrExist))
	}

	conn, err := c.dialConn(cf)
	if err != nil {
		return c.failErr("", err)
	}
	defer func() { _ = conn.Close() }()

	count, err := c.writeAuditExport(kmsv1.NewAdminServiceClient(conn), cf, req, *out)
	if err != nil {
		return c.failErr("audit export", err)
	}
	c.resultLine("Exported %d audit events to %s", count, *out)
	if c.jsonOutput() {
		return c.printJSON(auditExportJSON{Path: *out, Count: count})
	}
	return 0
}

// auditExportJSON is the JSON form of `audit export`: where the records landed
// and how many there were. The records themselves are in the file, never on
// stdout — an export is often larger than a terminal, and it is the file that
// consumers read.
type auditExportJSON struct {
	Path  string `json:"path"`
	Count int64  `json:"count"`
}

// writeAuditExport streams every matching page into an owner-only staging file
// beside the destination and publishes it only once the whole export is on
// disk and synced, so an interrupted run leaves no truncated file that looks
// complete. Publication never replaces an existing entry: an export is
// evidence, and overwriting the previous one would destroy it.
func (c *CLI) writeAuditExport(client kmsv1.AdminServiceClient, cf *connFlags, req *kmsv1.ListAuditEventsRequest, out string) (int64, error) {
	staging, err := fileutil.CreatePrivateTemp(filepath.Dir(out), ".kms-audit-export-")
	if err != nil {
		return 0, err
	}
	staged := staging.Name()
	// Removed on every path: on failure it is a partial export, and on success
	// PublishNoReplace may have linked rather than renamed it.
	defer func() { _ = os.Remove(staged) }()

	var count int64
	for {
		ctx, cancel := callContext()
		resp, err := client.ListAuditEvents(cf.authCtx(ctx), req)
		cancel()
		if err != nil {
			_ = staging.Close()
			return 0, err
		}
		if err := core.WriteAuditJSONL(staging, auditRecordsFromProto(resp.GetEvents())); err != nil {
			_ = staging.Close()
			return 0, err
		}
		count += int64(len(resp.GetEvents()))
		req.PageToken = resp.GetNextPageToken()
		if req.PageToken == "" {
			break
		}
	}
	if err := staging.Sync(); err != nil {
		_ = staging.Close()
		return 0, fmt.Errorf("syncing %s: %w", out, err)
	}
	if err := staging.Close(); err != nil {
		return 0, fmt.Errorf("closing %s: %w", out, err)
	}
	if err := fileutil.PublishNoReplace(staged, out); err != nil {
		if errors.Is(err, os.ErrExist) {
			return 0, fmt.Errorf("output file %s: %w; refusing to overwrite", out, os.ErrExist)
		}
		return 0, fmt.Errorf("publishing %s: %w", out, err)
	}
	return count, nil
}

// --- audit prune -----------------------------------------------------------

func (c *CLI) cmdAuditPrune(args []string) int {
	fs := c.newFlags("audit prune")
	r := c.serverSettings(fs, "storage.sqlite_path")
	olderThan := fs.String("older-than", "", "retire events older than this: a duration (720h, 90d) or an RFC 3339 `instant`")
	archive := fs.String("archive", "", "existing `directory` receiving a JSON Lines copy of every row before it is deleted")
	dryRun := fs.Bool("dry-run", false, "report how many events would be retired, without deleting anything")
	c.setUsage(fs, "audit prune --older-than DURATION [flags]",
		"Retire audit events older than a cutoff directly from the database file. With --archive DIR every row is written to DIR/audit-<YYYYMMDD>.jsonl and synced before it is deleted; without it the rows are discarded.", true)
	if !c.parseFlags(fs, args) {
		return 2
	}
	if !c.rejectPositionals() {
		return 2
	}
	cfg, prov, _, err := r.resolve()
	if err != nil {
		return c.failErr("", err)
	}
	if err := c.requireSQLitePath(cfg); err != nil {
		return c.failUsage("%v", err)
	}
	if *olderThan == "" {
		return c.failUsage("--older-than is required")
	}
	// One clock reading anchors both the cutoff the operator is shown and the
	// cutoff the pass applies, so nothing can drift between the confirmation
	// and the delete.
	now := time.Now()
	cutoff, err := parseSinceUntil("--older-than", *olderThan, now)
	if err != nil {
		return c.failUsage("%v", err)
	}
	retain := now.Sub(cutoff)
	if retain <= 0 {
		return c.failUsage("--older-than %q is not in the past; it would retire every audit event", *olderThan)
	}
	if *archive != "" {
		if err := requireArchiveDir(*archive); err != nil {
			return c.failErr("", err)
		}
	}

	// Name the database even for a dry run: which file was counted is exactly
	// what the rehearsal is meant to establish.
	c.warnDestructiveTarget(cfg, prov)
	if !*dryRun {
		action := fmt.Sprintf("prune audit events older than %s from", *olderThan)
		if ok, code := c.confirmDestructive(action, absPath(cfg.Storage.SQLitePath)); !ok {
			return code
		}
	}

	store, err := storage.Open(cfg.Storage.SQLitePath)
	if err != nil {
		return c.failErr("opening database", err)
	}
	defer func() { _ = store.Close() }()

	retention := core.AuditRetention{
		Store:      store,
		Retain:     retain,
		ArchiveDir: *archive,
		Logger:     c.quietLogger(),
		Now:        func() time.Time { return now },
	}
	if err := retention.Validate(); err != nil {
		return c.failUsage("%v", err)
	}
	ctx := context.Background()
	if *dryRun {
		total, err := store.CountAuditBefore(ctx, retention.Cutoff())
		if err != nil {
			return c.failErr("counting audit events", err)
		}
		c.resultLine("Would prune %d audit events", total)
		if c.jsonOutput() {
			return c.printJSON(auditPruneJSON{Pruned: total, ArchiveDir: optionalString(*archive), DryRun: true})
		}
		return 0
	}

	// One RunOnce bounds its work so the server's background loop never holds
	// the database for an unbounded stretch. A one-shot command is expected to
	// finish the job instead, so repeat until a pass finds nothing left.
	var pruned int64
	for {
		n, err := retention.RunOnce(ctx)
		pruned += n
		if err != nil {
			// Archive-before-delete means the batch that failed is still in
			// the database; say what did get retired before reporting why the
			// rest did not.
			c.info("Pruned %d audit events before the failure", pruned)
			return c.failErr("pruning audit events", err)
		}
		if n == 0 {
			break
		}
	}
	if *archive != "" {
		c.resultLine("Pruned %d audit events (archived to %s)", pruned, *archive)
	} else {
		c.resultLine("Pruned %d audit events", pruned)
	}
	if c.jsonOutput() {
		return c.printJSON(auditPruneJSON{Pruned: pruned, ArchiveDir: optionalString(*archive), DryRun: false})
	}
	return 0
}

// auditPruneJSON is the JSON form of `audit prune`: what a real run deleted or
// a --dry-run counted, and where the copies went. archive_dir is null when the
// rows were discarded rather than archived, which is the difference between a
// retention policy and data loss.
type auditPruneJSON struct {
	Pruned     int64   `json:"pruned"`
	ArchiveDir *string `json:"archive_dir"`
	DryRun     bool    `json:"dry_run"`
}

// requireArchiveDir refuses an --archive directory that does not already
// exist, the rule audit.archive_dir follows in the config for the same reason:
// creating it here would guess at the permissions of a directory that is about
// to hold the only remaining copy of the audit history.
func requireArchiveDir(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("--archive: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("--archive: %s is not a directory", path)
	}
	return nil
}
