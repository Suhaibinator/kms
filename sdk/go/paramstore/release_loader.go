package paramstore

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
	"google.golang.org/protobuf/proto"
)

const (
	ReleaseStateReceived = "received"
	ReleaseStatePrepared = "prepared"
	ReleaseStateApplied  = "applied"
	ReleaseStateRejected = "rejected"

	ReleaseRejectResolutionFailed       = "resolution_failed"
	ReleaseRejectTokenUnavailable       = "token_unavailable"
	ReleaseRejectVersionMismatch        = "version_mismatch"
	ReleaseRejectDigestMismatch         = "digest_mismatch"
	ReleaseRejectPrepareFailed          = "prepare_failed"
	ReleaseRejectConfigContractMismatch = "config_contract_mismatch"
	ReleaseRejectConfigDecodeFailed     = "config_decode_failed"
	ReleaseRejectConfigValidationFailed = "config_validation_failed"
	ReleaseRejectDefaultMismatch        = "default_mismatch"
	ReleaseRejectRestartRequired        = "restart_required"
	ReleaseRejectSuperseded             = "superseded"
	ReleaseRejectActiveCheck            = "active_check_failed"
	ReleaseRejectInternal               = "internal"

	defaultReleaseReconcileInterval = time.Minute
	defaultReleaseFetchConcurrency  = 16
)

// SecretTokenProvider supplies a locally held per-secret access token or
// client-bound key share. It is called only for release entries marked as
// token-protected or client-bound.
type SecretTokenProvider func(alias, path string) (token string, ok bool)

// ValidateReleaseManifestFunc validates unresolved release identity and entry
// metadata. It runs before any pinned parameter or secret is fetched and before
// SecretTokenProvider is called.
type ValidateReleaseManifestFunc func(context.Context, ReleaseManifest) error

// ReleaseLoaderConfig configures a high-level configuration release loader.
type ReleaseLoaderConfig struct {
	// Name is the release name within the client's home namespace.
	Name string
	// ReconcileInterval controls fresh GetActiveRelease safety checks. It
	// defaults to one minute.
	ReconcileInterval time.Duration
	// SecretTokenProvider supplies locally held credentials for protected
	// secret entries. Tokens are sent only to the corresponding GetSecret RPC.
	SecretTokenProvider SecretTokenProvider
	// ValidateManifest optionally validates the immutable unresolved manifest.
	// It runs after release identity, digest, and basic entry validation, but
	// before any resource fetch or secret-token lookup.
	ValidateManifest ValidateReleaseManifestFunc
	// MaxConcurrentFetches bounds parallel pinned resource reads. Values <= 0
	// use 16; values above 256 are rejected.
	MaxConcurrentFetches int
	// InstanceID overrides the generated process-lifetime subscriber ID. It is
	// mainly useful when an application already has a stable replica identifier.
	InstanceID string
}

// PreparedRelease is application-owned state built from a complete candidate.
// Commit must be infallible and should normally perform only an atomic swap.
type PreparedRelease interface {
	Commit()
	Abort()
}

// PrepareReleaseFunc validates a candidate and constructs resources before it
// becomes visible. The context is canceled when a newer release supersedes the
// candidate.
type PrepareReleaseFunc func(context.Context, ReleaseSnapshot) (PreparedRelease, error)

// ReleaseLoader owns release stream reliability, exact resource resolution,
// lifecycle acknowledgements, and last-known-good behavior.
type ReleaseLoader struct {
	client     *Client
	cfg        ReleaseLoaderConfig
	instanceID string
	running    atomic.Bool
	lastSeen   atomic.Uint64

	ackMu      sync.Mutex
	pendingAck map[string]*kmsv1.ReleaseAcknowledgement
	dirtyAck   map[string]bool
	ackSignal  chan struct{}

	statusMu sync.RWMutex
	status   ReleaseLoaderStatus
	stats    ReleaseLoaderStats
}

// NewReleaseLoader creates a loader. It does not contact KMS until Run.
func NewReleaseLoader(client *Client, cfg ReleaseLoaderConfig) (*ReleaseLoader, error) {
	if client == nil {
		return nil, errors.New("paramstore: release loader requires a client")
	}
	cfg.Name = strings.TrimSpace(cfg.Name)
	if cfg.Name == "" {
		return nil, errors.New("paramstore: release loader Name is required")
	}
	if cfg.ReconcileInterval <= 0 {
		cfg.ReconcileInterval = defaultReleaseReconcileInterval
	}
	if cfg.MaxConcurrentFetches <= 0 {
		cfg.MaxConcurrentFetches = defaultReleaseFetchConcurrency
	}
	if cfg.MaxConcurrentFetches > 256 {
		return nil, errors.New("paramstore: MaxConcurrentFetches must not exceed 256")
	}
	instanceID := strings.TrimSpace(cfg.InstanceID)
	if instanceID == "" {
		var err error
		instanceID, err = newReleaseInstanceID()
		if err != nil {
			return nil, fmt.Errorf("paramstore: generate release loader instance ID: %w", err)
		}
	}
	return &ReleaseLoader{
		client:     client,
		cfg:        cfg,
		instanceID: instanceID,
		pendingAck: make(map[string]*kmsv1.ReleaseAcknowledgement),
		dirtyAck:   make(map[string]bool),
		ackSignal:  make(chan struct{}, 1),
		stats:      ReleaseLoaderStats{Rejected: make(map[string]uint64)},
	}, nil
}

func newReleaseInstanceID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	// UUIDv4 layout, generated without adding another public dependency.
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// InstanceID returns the subscriber instance ID reused across reconnects.
func (l *ReleaseLoader) InstanceID() string { return l.instanceID }

// Status returns a redacted, concurrency-safe loader status snapshot.
func (l *ReleaseLoader) Status() ReleaseLoaderStatus {
	l.statusMu.RLock()
	defer l.statusMu.RUnlock()
	return l.status
}

// Stats returns bounded counters without resource aliases or paths.
func (l *ReleaseLoader) Stats() ReleaseLoaderStats {
	l.statusMu.RLock()
	defer l.statusMu.RUnlock()
	out := l.stats
	out.Rejected = make(map[string]uint64, len(l.stats.Rejected))
	for category, count := range l.stats.Rejected {
		out.Rejected[category] = count
	}
	return out
}

type releaseCandidate struct {
	release  *kmsv1.ConfigurationRelease
	revision uint64
	seq      uint64
	source   releaseCandidateSource
}

type releaseCandidateSource uint8

const (
	releaseCandidateSourceActivation releaseCandidateSource = iota
	releaseCandidateSourceReconciliation
)

type releaseCandidateResult struct {
	candidate releaseCandidate
	applied   bool
	category  string
	err       error
	fatal     bool
}

type resolvedReleaseEntry struct {
	entry     ReleaseEntryMetadata
	parameter *ReleaseParameter
	secret    *Secret
}

type releaseResolutionError struct {
	category string
	err      error
}

// Run watches, resolves, prepares, and atomically applies release candidates.
// Before the first successful Commit, a non-supersession candidate failure is
// returned. After that point errors reject the candidate and the last-known-good
// release remains applied until a later candidate succeeds.
func (l *ReleaseLoader) Run(ctx context.Context, prepare PrepareReleaseFunc) error {
	if ctx == nil {
		return errors.New("paramstore: release loader Run requires a context")
	}
	if prepare == nil {
		return errors.New("paramstore: release loader Run requires a prepare function")
	}
	if !l.running.CompareAndSwap(false, true) {
		return errors.New("paramstore: release loader is already running")
	}
	defer l.running.Store(false)

	ns, bound, err := l.client.resolveNamespace(ctx)
	if err != nil {
		return fmt.Errorf("paramstore: resolve release namespace: %w", err)
	}
	if !bound {
		return ErrNoNamespace
	}

	initial, err := l.getActive(ctx, ns)
	if err != nil {
		return fmt.Errorf("paramstore: load initial active release: %w", err)
	}
	if initial.release == nil {
		return errors.New("paramstore: active release response was empty")
	}
	l.lastSeen.Store(initial.revision)

	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	events := make(chan releaseCandidate, 1)
	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		l.watchLoop(runCtx, ns, events)
	}()

	// Preparation callbacks are application code and may not return promptly
	// after cancellation. Keep exactly one callback in flight and one
	// replace-latest pending candidate so rapid activations cannot create an
	// unbounded number of goroutines or prepared resources.
	results := make(chan releaseCandidateResult, 1)
	var latestSeq uint64
	var latestCandidate releaseCandidate
	var haveLatestCandidate bool
	var retryLatestCandidate bool
	var activeCancel context.CancelFunc
	var inFlight bool
	var pending *releaseCandidate
	appliedOnce := false

	start := func(candidate releaseCandidate) {
		candidateCtx, candidateCancel := context.WithCancel(runCtx)
		activeCancel = candidateCancel
		inFlight = true
		go func() { results <- l.processCandidate(candidateCtx, ns, candidate, prepare) }()
	}

	queue := func(candidate releaseCandidate) {
		if !shouldQueueReleaseCandidate(candidate, latestCandidate, haveLatestCandidate, retryLatestCandidate) {
			return
		}
		latestSeq++
		candidate.seq = latestSeq
		latestCandidate = candidate
		haveLatestCandidate = true
		retryLatestCandidate = false
		l.observe(candidate)
		if inFlight {
			if activeCancel != nil {
				activeCancel()
			}
			copy := candidate
			pending = &copy
			return
		}
		start(candidate)
	}

	queue(initial)
	ticker := time.NewTicker(l.cfg.ReconcileInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			if activeCancel != nil {
				activeCancel()
			}
			cancelRun()
			<-watchDone
			return ctx.Err()
		case candidate := <-events:
			queue(candidate)
		case result := <-results:
			inFlight = false
			activeCancel = nil
			if result.applied {
				appliedOnce = true
			}
			if result.fatal {
				cancelRun()
				return result.err
			}
			if result.candidate.seq != latestSeq {
				if pending != nil {
					next := *pending
					pending = nil
					start(next)
				}
				continue
			}
			if result.err != nil && !appliedOnce && result.category != ReleaseRejectSuperseded {
				// An activation may have raced a startup failure before its watch
				// event reached this select loop. Fresh-read once before failing so
				// a superseded initial candidate cannot prevent startup on the new
				// authoritative release.
				fresh, freshErr := l.getActive(runCtx, ns)
				if freshErr == nil && !sameActiveCandidate(result.candidate, fresh) {
					fresh.source = releaseCandidateSourceReconciliation
					queue(fresh)
					continue
				}
				cancelRun()
				return result.err
			}
			if result.err != nil && result.category != ReleaseRejectSuperseded {
				// The active release may have been rejected because of a transient
				// fetch, validation, or preparation failure. Admit the same identity
				// again only when a later reconciliation confirms it is still active;
				// duplicate activation events remain suppressed.
				retryLatestCandidate = true
			}
		case <-ticker.C:
			candidate, getErr := l.getActive(runCtx, ns)
			if getErr != nil {
				// Transport reliability is owned here: after startup, a failed
				// reconciliation never displaces the last-known-good release.
				continue
			}
			candidate.source = releaseCandidateSourceReconciliation
			queue(candidate)
		}
	}
}

func shouldQueueReleaseCandidate(candidate, latest releaseCandidate, haveLatest, retryLatest bool) bool {
	if candidate.release == nil {
		return false
	}
	if !haveLatest {
		return true
	}
	if candidate.revision < latest.revision {
		return false
	}
	if sameQueuedCandidate(candidate, latest) {
		return retryLatest && candidate.source == releaseCandidateSourceReconciliation
	}
	return true
}

func sameQueuedCandidate(a, b releaseCandidate) bool {
	return a.release != nil && b.release != nil &&
		a.revision == b.revision &&
		a.release.GetVersion() == b.release.GetVersion() &&
		a.release.GetDigest() == b.release.GetDigest()
}

// RunTypedRelease explicitly decodes a release snapshot into T and then calls
// the application preparation function. It uses no reflection.
func RunTypedRelease[T any](
	ctx context.Context,
	loader *ReleaseLoader,
	decode func(ReleaseSnapshot) (T, error),
	prepare func(context.Context, T) (PreparedRelease, error),
) error {
	if decode == nil || prepare == nil {
		return errors.New("paramstore: RunTypedRelease requires decode and prepare functions")
	}
	if loader == nil {
		return errors.New("paramstore: RunTypedRelease requires a loader")
	}
	return loader.Run(ctx, func(ctx context.Context, snapshot ReleaseSnapshot) (PreparedRelease, error) {
		decoded, err := decode(snapshot)
		if err != nil {
			return nil, err
		}
		return prepare(ctx, decoded)
	})
}

func (l *ReleaseLoader) processCandidate(
	ctx context.Context,
	ns namespaceRef,
	candidate releaseCandidate,
	prepare PrepareReleaseFunc,
) releaseCandidateResult {
	started := time.Now()
	snapshot, category, err := l.resolveCandidate(ctx, ns, candidate)
	l.recordResolution(time.Since(started))
	if err != nil {
		if ctx.Err() != nil {
			category = ReleaseRejectSuperseded
		}
		l.reject(ns, candidate, category)
		return releaseCandidateResult{candidate: candidate, category: category, err: loaderCandidateError(category)}
	}
	l.ack(ns, candidate, ReleaseStateReceived, "")
	l.setState(ReleaseStateReceived, "")

	prepared, prepareErr := prepare(ctx, snapshot)
	if prepareErr != nil || prepared == nil {
		category = ReleaseRejectPrepareFailed
		if classifiedCategory, ok := releaseRejectionCategory(prepareErr); ok {
			category = classifiedCategory
		}
		if ctx.Err() != nil {
			category = ReleaseRejectSuperseded
		}
		l.reject(ns, candidate, category)
		return releaseCandidateResult{candidate: candidate, category: category, err: loaderCandidateError(category)}
	}

	aborted := false
	abort := func() {
		if !aborted {
			aborted = true
			prepared.Abort()
		}
	}
	if ctx.Err() != nil {
		abort()
		l.reject(ns, candidate, ReleaseRejectSuperseded)
		return releaseCandidateResult{candidate: candidate, category: ReleaseRejectSuperseded, err: loaderCandidateError(ReleaseRejectSuperseded)}
	}
	l.ack(ns, candidate, ReleaseStatePrepared, "")
	l.setState(ReleaseStatePrepared, "")

	active, activeErr := l.getActive(ctx, ns)
	if activeErr != nil {
		abort()
		category = ReleaseRejectActiveCheck
		if ctx.Err() != nil {
			category = ReleaseRejectSuperseded
		}
		l.reject(ns, candidate, category)
		return releaseCandidateResult{candidate: candidate, category: category, err: loaderCandidateError(category)}
	}
	if !sameActiveCandidate(candidate, active) || ctx.Err() != nil {
		abort()
		l.reject(ns, candidate, ReleaseRejectSuperseded)
		return releaseCandidateResult{candidate: candidate, category: ReleaseRejectSuperseded, err: loaderCandidateError(ReleaseRejectSuperseded)}
	}

	var commitPanic any
	func() {
		defer func() { commitPanic = recover() }()
		prepared.Commit()
	}()
	if commitPanic != nil {
		// Commit may have partially changed application state. Do not claim a
		// definitive rejected or applied lifecycle outcome; record only a local
		// fatal/internal failure and return control to the application.
		l.recordRejected(ReleaseRejectInternal)
		return releaseCandidateResult{
			candidate: candidate,
			category:  ReleaseRejectInternal,
			fatal:     true,
			err:       errors.New("paramstore: PreparedRelease.Commit panicked; commit must be infallible"),
		}
	}

	l.ack(ns, candidate, ReleaseStateApplied, "")
	l.recordApplied(candidate)
	return releaseCandidateResult{candidate: candidate, applied: true}
}

func sameActiveCandidate(want, got releaseCandidate) bool {
	if want.release == nil || got.release == nil {
		return false
	}
	return want.revision == got.revision &&
		want.release.GetName() == got.release.GetName() &&
		want.release.GetVersion() == got.release.GetVersion() &&
		want.release.GetDigest() == got.release.GetDigest()
}

type releaseRejectionCategorizer interface {
	ReleaseRejectionCategory() string
}

type releaseCandidateError struct {
	category string
}

func (e *releaseCandidateError) Error() string {
	return fmt.Sprintf("paramstore: configuration release candidate rejected (%s)", e.category)
}

// Format keeps diagnostic formatting on the same fixed, bounded text even for
// verbs such as %+v and %#v that might otherwise reflect the wrapped cause.
func (e *releaseCandidateError) Format(f fmt.State, verb rune) {
	if verb == 'q' {
		_, _ = fmt.Fprintf(f, "%q", e.Error())
		return
	}
	_, _ = fmt.Fprint(f, e.Error())
}

// Candidate causes are deliberately discarded. Even a categorized application
// error may wrap validation text containing resolved secret material. Higher
// level APIs that promise a typed local error must preserve it independently
// rather than routing it through the loader's acknowledgement/error boundary.
func loaderCandidateError(category string) error {
	return &releaseCandidateError{category: category}
}

func releaseRejectionCategory(err error) (string, bool) {
	if err == nil {
		return "", false
	}
	var classified releaseRejectionCategorizer
	if !errors.As(err, &classified) {
		return "", false
	}
	category := classified.ReleaseRejectionCategory()
	return category, validReleaseRejectionCategory(category)
}

func validReleaseRejectionCategory(category string) bool {
	switch category {
	case ReleaseRejectResolutionFailed,
		ReleaseRejectTokenUnavailable,
		ReleaseRejectVersionMismatch,
		ReleaseRejectDigestMismatch,
		ReleaseRejectPrepareFailed,
		ReleaseRejectConfigContractMismatch,
		ReleaseRejectConfigDecodeFailed,
		ReleaseRejectConfigValidationFailed,
		ReleaseRejectDefaultMismatch,
		ReleaseRejectRestartRequired,
		ReleaseRejectSuperseded,
		ReleaseRejectActiveCheck,
		ReleaseRejectInternal:
		return true
	default:
		return false
	}
}

func (l *ReleaseLoader) resolveCandidate(ctx context.Context, ns namespaceRef, candidate releaseCandidate) (ReleaseSnapshot, string, error) {
	release := candidate.release
	if release == nil || release.GetNamespace() == nil {
		return ReleaseSnapshot{}, ReleaseRejectResolutionFailed, errors.New("empty release")
	}
	if release.GetName() != l.cfg.Name || release.GetNamespace().GetEnv() != ns.env || release.GetNamespace().GetApp() != ns.app {
		return ReleaseSnapshot{}, ReleaseRejectVersionMismatch, errors.New("release identity mismatch")
	}
	calculatedDigest, err := deterministicReleaseDigest(release)
	if err != nil || release.GetDigest() == "" || !strings.EqualFold(calculatedDigest, release.GetDigest()) {
		return ReleaseSnapshot{}, ReleaseRejectDigestMismatch, errors.New("release digest mismatch")
	}
	entries := release.GetEntries()
	if len(entries) == 0 {
		return ReleaseSnapshot{}, ReleaseRejectResolutionFailed, errors.New("release has no entries")
	}
	manifest, err := newReleaseManifest(release, candidate)
	if err != nil {
		return ReleaseSnapshot{}, ReleaseRejectResolutionFailed, err
	}
	if l.cfg.ValidateManifest != nil {
		if validateErr := l.cfg.ValidateManifest(ctx, manifest); validateErr != nil {
			category := ReleaseRejectPrepareFailed
			if classifiedCategory, ok := releaseRejectionCategory(validateErr); ok {
				category = classifiedCategory
			}
			return ReleaseSnapshot{}, category, validateErr
		}
	}
	if ctx.Err() != nil {
		return ReleaseSnapshot{}, ReleaseRejectSuperseded, ctx.Err()
	}
	jobs := make(chan *kmsv1.ConfigurationReleaseEntry)
	results := make(chan resolvedReleaseEntry, len(entries))
	errs := make(chan releaseResolutionError, len(entries))

	workerCount := min(l.cfg.MaxConcurrentFetches, max(1, len(entries)))
	var workers sync.WaitGroup
	workers.Add(workerCount)
	for range workerCount {
		go func() {
			defer workers.Done()
			for entry := range jobs {
				item, category, err := l.resolveEntry(ctx, entry)
				if err != nil {
					errs <- releaseResolutionError{category: category, err: err}
					continue
				}
				results <- item
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, entry := range entries {
			select {
			case jobs <- entry:
			case <-ctx.Done():
				return
			}
		}
	}()
	workers.Wait()
	close(results)
	close(errs)
	if ctx.Err() != nil {
		return ReleaseSnapshot{}, ReleaseRejectSuperseded, ctx.Err()
	}
	if first, ok := <-errs; ok {
		return ReleaseSnapshot{}, first.category, first.err
	}

	snapshot := ReleaseSnapshot{
		namespace:          manifest.Namespace(),
		name:               manifest.Name(),
		version:            manifest.Version(),
		activationRevision: manifest.ActivationRevision(),
		schemaID:           manifest.SchemaID(),
		schemaVersion:      manifest.SchemaVersion(),
		digest:             manifest.Digest(),
		metadataJSON:       manifest.MetadataJSON(),
		entries:            manifest.Entries(),
		parameters:         make(map[string]ReleaseParameter),
		secrets:            make(map[string]Secret),
	}
	for item := range results {
		if item.parameter != nil {
			snapshot.parameters[item.entry.Alias] = *item.parameter
		}
		if item.secret != nil {
			snapshot.secrets[item.entry.Alias] = cloneSecret(*item.secret)
		}
	}
	return snapshot, "", nil
}

func newReleaseManifest(release *kmsv1.ConfigurationRelease, candidate releaseCandidate) (ReleaseManifest, error) {
	manifest := ReleaseManifest{
		namespace:          release.GetNamespace().GetEnv() + "/" + release.GetNamespace().GetApp(),
		name:               release.GetName(),
		version:            release.GetVersion(),
		activationRevision: candidate.revision,
		schemaID:           release.GetSchemaId(),
		schemaVersion:      release.GetSchemaVersion(),
		digest:             release.GetDigest(),
		metadataJSON:       release.GetMetadataJson(),
		entries:            make(map[string]ReleaseEntryMetadata, len(release.GetEntries())),
	}
	for _, entry := range release.GetEntries() {
		metadata, err := releaseEntryMetadata(entry)
		if err != nil {
			return ReleaseManifest{}, err
		}
		if _, exists := manifest.entries[metadata.Alias]; exists {
			return ReleaseManifest{}, errors.New("duplicate release entry alias")
		}
		manifest.entries[metadata.Alias] = metadata
	}
	return manifest, nil
}

// deterministicReleaseDigest mirrors the server's immutable protobuf
// projection. Movable labels, allocated release versions, timestamps, and the
// digest field itself are deliberately excluded.
func deterministicReleaseDigest(release *kmsv1.ConfigurationRelease) (string, error) {
	if release == nil {
		return "", errors.New("empty release")
	}
	projection := &kmsv1.ConfigurationRelease{
		Namespace:     &kmsv1.NamespaceRef{Env: release.GetNamespace().GetEnv(), App: release.GetNamespace().GetApp()},
		Name:          release.GetName(),
		SchemaId:      release.GetSchemaId(),
		SchemaVersion: release.GetSchemaVersion(),
		MetadataJson:  release.GetMetadataJson(),
	}
	entries := append([]*kmsv1.ConfigurationReleaseEntry(nil), release.GetEntries()...)
	sort.Slice(entries, func(i, j int) bool { return entries[i].GetAlias() < entries[j].GetAlias() })
	for _, entry := range entries {
		if entry == nil {
			return "", errors.New("empty release entry")
		}
		projection.Entries = append(projection.Entries, &kmsv1.ConfigurationReleaseEntry{
			Alias:           entry.GetAlias(),
			Kind:            entry.GetKind(),
			Ref:             &kmsv1.ResourceRef{Namespace: &kmsv1.NamespaceRef{Env: entry.GetRef().GetNamespace().GetEnv(), App: entry.GetRef().GetNamespace().GetApp()}, Key: entry.GetRef().GetKey()},
			Version:         entry.GetVersion(),
			ContentType:     entry.GetContentType(),
			MetadataJson:    entry.GetMetadataJson(),
			ParameterDigest: entry.GetParameterDigest(),
			ClientBound:     entry.GetClientBound(),
			HasAccessToken:  entry.GetHasAccessToken(),
		})
	}
	b, err := (proto.MarshalOptions{Deterministic: true}).Marshal(projection)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(b)
	return hex.EncodeToString(digest[:]), nil
}

func releaseEntryMetadata(entry *kmsv1.ConfigurationReleaseEntry) (ReleaseEntryMetadata, error) {
	if entry == nil || entry.GetRef() == nil || entry.GetRef().GetNamespace() == nil || entry.GetAlias() == "" || entry.GetVersion() == 0 {
		return ReleaseEntryMetadata{}, errors.New("invalid release entry")
	}
	if entry.GetKind() != "parameter" && entry.GetKind() != "secret" {
		return ReleaseEntryMetadata{}, errors.New("unknown release entry kind")
	}
	r := refFromProto(entry.GetRef())
	return ReleaseEntryMetadata{
		Alias:           entry.GetAlias(),
		Kind:            entry.GetKind(),
		Path:            r.display(),
		Version:         entry.GetVersion(),
		ContentType:     entry.GetContentType(),
		MetadataJSON:    entry.GetMetadataJson(),
		ParameterDigest: entry.GetParameterDigest(),
		ClientBound:     entry.GetClientBound(),
		HasAccessToken:  entry.GetHasAccessToken(),
	}, nil
}

func (l *ReleaseLoader) resolveEntry(ctx context.Context, entry *kmsv1.ConfigurationReleaseEntry) (resolvedReleaseEntry, string, error) {
	var out resolvedReleaseEntry
	metadata, err := releaseEntryMetadata(entry)
	if err != nil {
		return out, ReleaseRejectResolutionFailed, err
	}
	r := refFromProto(entry.GetRef())
	out.entry = metadata

	switch entry.GetKind() {
	case "parameter":
		cctx, cancel := l.client.callCtx(ctx, "")
		resp, err := l.client.params.GetParameter(cctx, &kmsv1.GetParameterRequest{
			Ref: entry.GetRef(), Version: entry.GetVersion(),
		})
		cancel()
		if err != nil {
			return out, ReleaseRejectResolutionFailed, mapError(err)
		}
		if resp.GetParameter() == nil {
			return out, ReleaseRejectResolutionFailed, errors.New("empty parameter response")
		}
		parameter := resp.GetParameter()
		if !sameResourceRef(parameter.GetRef(), entry.GetRef()) {
			return out, ReleaseRejectVersionMismatch, errors.New("parameter resource reference mismatch")
		}
		if parameter.GetVersion() != entry.GetVersion() {
			return out, ReleaseRejectVersionMismatch, errors.New("parameter version mismatch")
		}
		sum := sha256.Sum256([]byte(parameter.GetValue()))
		if entry.GetParameterDigest() == "" || !strings.EqualFold(hex.EncodeToString(sum[:]), entry.GetParameterDigest()) {
			return out, ReleaseRejectDigestMismatch, errors.New("parameter digest mismatch")
		}
		if entry.GetContentType() != "" && parameter.GetContentType() != entry.GetContentType() {
			return out, ReleaseRejectDigestMismatch, errors.New("parameter content type mismatch")
		}
		p := ReleaseParameter{value: parameter.GetValue(), entry: out.entry}
		out.parameter = &p
		return out, "", nil

	case "secret":
		token := ""
		if entry.GetHasAccessToken() || entry.GetClientBound() {
			if l.cfg.SecretTokenProvider == nil {
				return out, ReleaseRejectTokenUnavailable, errors.New("secret token provider unavailable")
			}
			var ok bool
			token, ok = l.cfg.SecretTokenProvider(entry.GetAlias(), r.display())
			if !ok || token == "" {
				return out, ReleaseRejectTokenUnavailable, errors.New("secret token unavailable")
			}
		}
		secret, err := l.client.GetSecret(ctx, r.display(), WithVersion(entry.GetVersion()), WithSecretToken(token))
		if err != nil {
			return out, ReleaseRejectResolutionFailed, err
		}
		if secret.Version() != entry.GetVersion() {
			return out, ReleaseRejectVersionMismatch, errors.New("secret version mismatch")
		}
		if secret.Path() != r.display() {
			return out, ReleaseRejectVersionMismatch, errors.New("secret resource reference mismatch")
		}
		if entry.GetContentType() != "" && secret.ContentType() != entry.GetContentType() {
			return out, ReleaseRejectVersionMismatch, errors.New("secret content type mismatch")
		}
		secret = cloneSecret(secret)
		out.secret = &secret
		return out, "", nil
	default:
		return out, ReleaseRejectResolutionFailed, errors.New("unknown release entry kind")
	}
}

func sameResourceRef(a, b *kmsv1.ResourceRef) bool {
	if a == nil || b == nil || a.GetNamespace() == nil || b.GetNamespace() == nil {
		return false
	}
	return refFromProto(a) == refFromProto(b)
}

func (l *ReleaseLoader) getActive(ctx context.Context, ns namespaceRef) (releaseCandidate, error) {
	cctx, cancel := l.client.callCtx(ctx, "")
	defer cancel()
	resp, err := l.client.releases.GetActiveRelease(cctx, &kmsv1.GetActiveReleaseRequest{
		Namespace: ns.proto(), Name: l.cfg.Name,
	})
	if err != nil {
		return releaseCandidate{}, mapError(err)
	}
	return releaseCandidate{release: resp.GetRelease(), revision: resp.GetActivationRevision()}, nil
}

func (l *ReleaseLoader) watchLoop(ctx context.Context, ns namespaceRef, events chan releaseCandidate) {
	attempt := 0
	for ctx.Err() == nil {
		receivedEvent, err := l.watchSession(ctx, ns, events)
		if ctx.Err() != nil {
			return
		}
		if receivedEvent {
			attempt = 0
		}
		l.recordReconnect()
		delay := backoffDelay(attempt)
		attempt++
		l.client.logf("paramstore: release watch ended (%v); reconnecting in %s", err, delay)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (l *ReleaseLoader) watchSession(ctx context.Context, ns namespaceRef, events chan releaseCandidate) (bool, error) {
	stream, err := l.client.releases.WatchRelease(l.client.withAuth(ctx, ""))
	if err != nil {
		return false, err
	}
	if err := stream.Send(&kmsv1.WatchReleaseRequest{Request: &kmsv1.WatchReleaseRequest_Register{
		Register: &kmsv1.ReleaseWatchRegistration{
			Namespace:        ns.proto(),
			Name:             l.cfg.Name,
			ClientName:       l.client.clientName,
			InstanceId:       l.instanceID,
			LastSeenRevision: l.lastSeen.Load(),
		},
	}}); err != nil {
		return false, err
	}
	if err := l.sendPendingAcks(stream, true); err != nil {
		return false, err
	}
	receivedEvent := false

	recv := make(chan struct {
		event *kmsv1.WatchReleaseEvent
		err   error
	}, 1)
	go func() {
		event, recvErr := stream.Recv()
		recv <- struct {
			event *kmsv1.WatchReleaseEvent
			err   error
		}{event: event, err: recvErr}
	}()
	for {
		select {
		case <-ctx.Done():
			return receivedEvent, ctx.Err()
		case <-l.ackSignal:
			if err := l.sendPendingAcks(stream, false); err != nil {
				return receivedEvent, err
			}
		case item := <-recv:
			if item.err != nil {
				return receivedEvent, item.err
			}
			event := item.event
			if event != nil {
				receivedEvent = true
				l.advanceLastSeen(event.GetRevision())
				var release *kmsv1.ConfigurationRelease
				source := releaseCandidateSourceActivation
				switch payload := event.GetEvent().(type) {
				case *kmsv1.WatchReleaseEvent_Snapshot:
					release = payload.Snapshot.GetRelease()
					source = releaseCandidateSourceReconciliation
				case *kmsv1.WatchReleaseEvent_Activation:
					release = payload.Activation.GetRelease()
				}
				if release != nil {
					offerLatestCandidate(events, releaseCandidate{release: release, revision: event.GetRevision(), source: source})
				}
			}
			go func() {
				next, recvErr := stream.Recv()
				recv <- struct {
					event *kmsv1.WatchReleaseEvent
					err   error
				}{event: next, err: recvErr}
			}()
		}
	}
}

func offerLatestCandidate(ch chan releaseCandidate, candidate releaseCandidate) {
	select {
	case ch <- candidate:
		return
	default:
	}
	select {
	case <-ch:
	default:
	}
	select {
	case ch <- candidate:
	default:
	}
}

func (l *ReleaseLoader) advanceLastSeen(revision uint64) {
	for {
		current := l.lastSeen.Load()
		if revision <= current || l.lastSeen.CompareAndSwap(current, revision) {
			return
		}
	}
}

func (l *ReleaseLoader) ack(ns namespaceRef, candidate releaseCandidate, state, rejectionCategory string) {
	ack := &kmsv1.ReleaseAcknowledgement{
		Namespace:          ns.proto(),
		Name:               l.cfg.Name,
		Version:            candidate.release.GetVersion(),
		ActivationRevision: candidate.revision,
		ClientName:         l.client.clientName,
		InstanceId:         l.instanceID,
		State:              state,
		RejectionCategory:  rejectionCategory,
		TimestampUnixMs:    time.Now().UnixMilli(),
		// Diagnostic is intentionally empty: arbitrary application errors may
		// contain secret plaintext and therefore are never forwarded implicitly.
	}
	l.ackMu.Lock()
	if current := l.pendingAck[state]; current == nil || current.GetActivationRevision() <= candidate.revision {
		l.pendingAck[state] = ack
		l.dirtyAck[state] = true
	}
	l.ackMu.Unlock()
	select {
	case l.ackSignal <- struct{}{}:
	default:
	}
}

func (l *ReleaseLoader) sendPendingAcks(stream kmsv1.ConfigurationReleaseService_WatchReleaseClient, replay bool) error {
	l.ackMu.Lock()
	states := make([]string, 0, len(l.pendingAck))
	for state := range l.pendingAck {
		if !replay && !l.dirtyAck[state] {
			continue
		}
		states = append(states, state)
	}
	sort.Strings(states)
	acks := make([]*kmsv1.ReleaseAcknowledgement, 0, len(states))
	for _, state := range states {
		acks = append(acks, proto.Clone(l.pendingAck[state]).(*kmsv1.ReleaseAcknowledgement))
	}
	l.ackMu.Unlock()
	for _, ack := range acks {
		if err := stream.Send(&kmsv1.WatchReleaseRequest{Request: &kmsv1.WatchReleaseRequest_Acknowledgement{
			Acknowledgement: ack,
		}}); err != nil {
			return err
		}
		l.ackMu.Lock()
		current := l.pendingAck[ack.GetState()]
		if current != nil && current.GetActivationRevision() == ack.GetActivationRevision() && current.GetVersion() == ack.GetVersion() {
			l.dirtyAck[ack.GetState()] = false
		}
		l.ackMu.Unlock()
	}
	return nil
}

func (l *ReleaseLoader) observe(candidate releaseCandidate) {
	l.statusMu.Lock()
	defer l.statusMu.Unlock()
	l.status.ObservedVersion = candidate.release.GetVersion()
	l.status.ObservedRevision = candidate.revision
	l.stats.Candidates++
}

func (l *ReleaseLoader) setState(state, failure string) {
	l.statusMu.Lock()
	defer l.statusMu.Unlock()
	l.status.State = state
	if failure != "" {
		l.status.LastFailureCategory = failure
		l.status.LastFailureAt = time.Now()
	}
}

func (l *ReleaseLoader) reject(ns namespaceRef, candidate releaseCandidate, category string) {
	l.ack(ns, candidate, ReleaseStateRejected, category)
	l.recordRejected(category)
}

func (l *ReleaseLoader) recordRejected(category string) {
	l.statusMu.Lock()
	defer l.statusMu.Unlock()
	l.status.State = ReleaseStateRejected
	l.status.LastFailureCategory = category
	l.status.LastFailureAt = time.Now()
	l.stats.Rejected[category]++
}

func (l *ReleaseLoader) recordApplied(candidate releaseCandidate) {
	l.statusMu.Lock()
	defer l.statusMu.Unlock()
	l.status.State = ReleaseStateApplied
	l.status.AppliedVersion = candidate.release.GetVersion()
	l.status.AppliedRevision = candidate.revision
	l.status.LastFailureCategory = ""
	l.stats.Applied++
}

func (l *ReleaseLoader) recordResolution(duration time.Duration) {
	l.statusMu.Lock()
	defer l.statusMu.Unlock()
	l.status.LastResolutionDuration = duration
}

func (l *ReleaseLoader) recordReconnect() {
	l.statusMu.Lock()
	defer l.statusMu.Unlock()
	l.status.Reconnects++
	l.stats.Reconnects++
}
