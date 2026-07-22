package configstore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/Suhaibinator/kms/sdk/go/paramstore"
)

// Manager owns managed release policy and the background lower-level loader.
type Manager struct {
	loader  *paramstore.ReleaseLoader
	options Options
	prepare PrepareFunc

	readyCh   chan struct{}
	done      chan struct{}
	readyOnce sync.Once
	reportMu  sync.Mutex

	mu              sync.RWMutex
	ready           bool
	observed        ReleaseIdentity
	applied         ReleaseIdentity
	divergent       bool
	lastReportedKey string
	lastReportErr   error
	lastRejectedKey string
	startupErr      *DefaultMismatchError
	waitErr         error
}

// Start validates its generated contract, starts ReleaseLoader in the
// background, and waits until the initial generation has been atomically
// published. No Manager is returned before generated Current is usable.
func Start(
	ctx context.Context,
	client *paramstore.Client,
	options Options,
	prepare PrepareFunc,
) (*Manager, error) {
	if ctx == nil {
		return nil, errors.New("configstore: Start requires a context")
	}
	if client == nil {
		return nil, errors.New("configstore: Start requires a paramstore client")
	}
	if prepare == nil {
		return nil, errors.New("configstore: Start requires a prepare function")
	}
	options.Release = strings.TrimSpace(options.Release)
	if options.Release == "" {
		return nil, errors.New("configstore: Options.Release is required")
	}
	if options.OnDefaultMismatch == nil {
		return nil, errors.New("configstore: Options.OnDefaultMismatch is required")
	}
	if err := validateContract(options.Contract); err != nil {
		return nil, err
	}
	options.Contract = append([]ContractEntry(nil), options.Contract...)

	manager := &Manager{
		options: options,
		prepare: prepare,
		readyCh: make(chan struct{}),
		done:    make(chan struct{}),
	}
	validateManifest := manifestValidator(options.Contract)
	loader, err := paramstore.NewReleaseLoader(client, paramstore.ReleaseLoaderConfig{
		Name:                options.Release,
		ReconcileInterval:   options.ReconcileInterval,
		SecretTokenProvider: options.SecretTokenProvider,
		ValidateManifest: func(ctx context.Context, manifest paramstore.ReleaseManifest) error {
			identity := releaseIdentityFromManifest(manifest)
			manager.mu.Lock()
			manager.observed = identity
			manager.mu.Unlock()
			err := validateManifest(ctx, manifest)
			if err != nil {
				manager.notifyCandidateRejected(identity, err)
			}
			return err
		},
		MaxConcurrentFetches: options.MaxConcurrentFetches,
		InstanceID:           options.InstanceID,
	})
	if err != nil {
		return nil, err
	}

	manager.loader = loader
	go manager.run(ctx)

	select {
	case <-manager.readyCh:
		return manager, nil
	case <-manager.done:
		manager.mu.RLock()
		ready := manager.ready
		startupErr := manager.startupErr
		waitErr := manager.waitErr
		manager.mu.RUnlock()
		if ready {
			return manager, nil
		}
		// A newer startup candidate can supersede a fatal mismatch and then fail
		// during resolution before prepareWithIdentity gets a chance to clear the
		// side channel. Return the typed report only when the loader's terminal
		// candidate was itself rejected for default drift.
		if startupErr != nil && manager.loader.Status().LastFailureCategory == paramstore.ReleaseRejectDefaultMismatch {
			return nil, startupErr
		}
		if waitErr == nil {
			waitErr = errors.New("configstore: release loader stopped before initial publication")
		}
		return nil, waitErr
	}
}

func (m *Manager) run(ctx context.Context) {
	err := m.loader.Run(ctx, m.prepareSnapshot)
	m.mu.Lock()
	if m.ready && ctx.Err() != nil && isContextError(err) {
		err = nil
	}
	m.waitErr = err
	m.mu.Unlock()
	close(m.done)
}

func isContextError(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func (m *Manager) prepareSnapshot(
	ctx context.Context,
	snapshot paramstore.ReleaseSnapshot,
) (paramstore.PreparedRelease, error) {
	return m.prepareWithIdentity(ctx, snapshot, ReleaseIdentityFromSnapshot(snapshot))
}

func (m *Manager) prepareWithIdentity(
	ctx context.Context,
	snapshot paramstore.ReleaseSnapshot,
	identity ReleaseIdentity,
) (paramstore.PreparedRelease, error) {
	m.mu.Lock()
	m.observed = identity
	startup := !m.ready
	if startup {
		m.startupErr = nil
	}
	m.mu.Unlock()

	candidate, err := m.prepare(ctx, snapshot)
	if err != nil {
		newAbort(candidate.Abort)()
		m.notifyCandidateRejected(identity, err)
		return nil, err
	}
	abort := newAbort(candidate.Abort)
	if candidate.Publish == nil {
		abort()
		err := Reject(RejectConfigValidationFailed,
			errors.New("configstore: prepared candidate Publish is required"))
		m.notifyCandidateRejected(identity, err)
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		abort()
		return nil, err
	}

	differences := cloneDifferences(candidate.DefaultDifferences)
	restartFields := append([]string(nil), candidate.RestartRequiredFields...)
	if !startup && len(restartFields) != 0 {
		abort()
		err := rejectWithPaths(RejectRestartRequired, &restartRequiredError{fields: restartFields}, restartFields)
		m.notifyCandidateRejected(identity, err)
		return nil, err
	}

	divergent := len(differences) != 0
	if divergent {
		phase := MismatchRuntime
		severity := MismatchError
		if startup {
			phase = MismatchStartup
			if !m.options.AllowDefaultMismatch {
				severity = MismatchFatal
			}
		}
		report := newDefaultMismatchReport(phase, severity, identity, differences)
		if err := m.reportOnce(identity, report); err != nil {
			abort()
			m.notifyCandidateRejected(identity, err)
			return nil, err
		}
		if severity == MismatchFatal {
			mismatchErr := newDefaultMismatchError(report)
			m.mu.Lock()
			m.startupErr = mismatchErr
			m.mu.Unlock()
			abort()
			paths := make([]string, len(differences))
			for i := range differences {
				paths[i] = differences[i].Path
			}
			err := rejectWithPaths(RejectDefaultMismatch, mismatchErr, paths)
			m.notifyCandidateRejected(identity, err)
			return nil, err
		}
	}

	return &managedPrepared{
		manager:   m,
		publish:   candidate.Publish,
		abort:     abort,
		identity:  identity,
		divergent: divergent,
	}, nil
}

func (m *Manager) notifyCandidateRejected(identity ReleaseIdentity, err error) {
	var candidateErr *CandidateError
	if !errors.As(err, &candidateErr) {
		return
	}
	callback := m.options.OnCandidateRejected
	if callback == nil {
		return
	}
	category := RejectionCategory(candidateErr.ReleaseRejectionCategory())
	key := identity.dedupeKey()
	m.mu.Lock()
	if m.lastRejectedKey == key {
		m.mu.Unlock()
		return
	}
	// Record before invoking application code. A panic must neither alter
	// candidate admission nor turn reconciliation into a callback storm.
	m.lastRejectedKey = key
	m.mu.Unlock()

	report := newCandidateRejectionReport(category, identity, candidateErr.pathsCopy())
	func() {
		defer func() { _ = recover() }()
		callback(report)
	}()
}

func (m *Manager) reportOnce(identity ReleaseIdentity, report DefaultMismatchReport) (err error) {
	key := identity.dedupeKey()
	m.reportMu.Lock()
	defer m.reportMu.Unlock()
	if m.lastReportedKey == key {
		return m.lastReportErr
	}
	callback := m.options.OnDefaultMismatch
	m.lastReportedKey = key
	defer func() {
		if recover() != nil {
			err = Reject(RejectInternal, errors.New("configstore: default mismatch callback panicked"))
		}
		m.lastReportErr = err
	}()
	callback(report)
	return nil
}

func (r ReleaseIdentity) dedupeKey() string {
	return fmt.Sprintf("%s\x00%s\x00%d\x00%d\x00%s",
		r.namespace, r.name, r.version, r.activationRevision, r.digest)
}

type managedPrepared struct {
	manager   *Manager
	publish   func()
	abort     func()
	identity  ReleaseIdentity
	divergent bool
}

func (p *managedPrepared) Commit() {
	// Publication is application-owned and must happen before readiness or
	// manager status becomes visible.
	p.publish()
	p.manager.mu.Lock()
	p.manager.applied = p.identity
	p.manager.divergent = p.divergent
	p.manager.ready = true
	p.manager.mu.Unlock()
	p.manager.readyOnce.Do(func() { close(p.manager.readyCh) })
}

func (p *managedPrepared) Abort() { p.abort() }

func newAbort(abort func()) func() {
	if abort == nil {
		abort = func() {}
	}
	var once sync.Once
	return func() { once.Do(abort) }
}

type restartRequiredError struct {
	fields []string
}

func (e *restartRequiredError) Error() string {
	return "configstore: candidate changes restart-required fields: " + strings.Join(e.fields, ",")
}

// Status returns a redacted, concurrency-safe manager snapshot.
func (m *Manager) Status() Status {
	if m == nil {
		return Status{}
	}
	loaderStatus := m.loader.Status()
	m.mu.RLock()
	observed := m.observed
	if loaderStatus.ObservedVersion != observed.version || loaderStatus.ObservedRevision != observed.activationRevision {
		// Prefetch contract and resolution failures do not produce a resolved
		// snapshot. Preserve the safe version/revision observed by ReleaseLoader
		// while leaving unavailable schema/digest fields empty.
		observed = ReleaseIdentity{
			name:               m.options.Release,
			version:            loaderStatus.ObservedVersion,
			activationRevision: loaderStatus.ObservedRevision,
		}
	}
	status := Status{
		State:                 loaderStatus.State,
		Ready:                 m.ready,
		Observed:              observed,
		Applied:               m.applied,
		DefaultDivergent:      m.divergent,
		LastRejectionCategory: RejectionCategory(loaderStatus.LastFailureCategory),
		LastFailureAt:         loaderStatus.LastFailureAt,
		Reconnects:            loaderStatus.Reconnects,
	}
	m.mu.RUnlock()
	return status
}

// Stats returns bounded counters and a fresh rejection map.
func (m *Manager) Stats() Stats {
	if m == nil {
		return Stats{Rejected: make(map[RejectionCategory]uint64)}
	}
	loaderStats := m.loader.Stats()
	m.mu.RLock()
	stats := Stats{
		Candidates:                loaderStats.Candidates,
		Applied:                   loaderStats.Applied,
		Rejected:                  make(map[RejectionCategory]uint64, len(loaderStats.Rejected)),
		Reconnects:                loaderStats.Reconnects,
		DefaultDivergent:          m.divergent,
		AppliedReleaseVersion:     m.applied.version,
		AppliedActivationRevision: m.applied.activationRevision,
	}
	m.mu.RUnlock()
	for category, count := range loaderStats.Rejected {
		stats.Rejected[RejectionCategory(category)] = count
	}
	return stats
}

// Wait blocks until the background loader exits. Caller context cancellation
// after initial readiness is a normal shutdown and returns nil.
func (m *Manager) Wait() error {
	if m == nil {
		return errors.New("configstore: nil Manager")
	}
	<-m.done
	m.mu.RLock()
	err := m.waitErr
	m.mu.RUnlock()
	return err
}
