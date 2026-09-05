package configstore

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"maps"
	"strings"
	"sync"

	"github.com/Suhaibinator/kms/sdk/go/kmsclient"
)

// Manager owns managed release policy and the background lower-level loader.
type Manager struct {
	loader  *kmsclient.ReleaseLoader
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
	lastRejectedKey string
	waitErr         error
}

// String prevents formatting from traversing the retained lower-level loader,
// which owns the private binding-key map for the manager's lifetime.
func (m *Manager) String() string {
	if m == nil {
		return "Manager<nil>"
	}
	return fmt.Sprintf("Manager{release=%q}", m.options.Release)
}

func (m *Manager) GoString() string { return m.String() }

func (m *Manager) Format(f fmt.State, verb rune) {
	if verb == 'q' {
		_, _ = fmt.Fprintf(f, "%q", m.String())
		return
	}
	_, _ = fmt.Fprint(f, m.String())
}

type managerJSON struct {
	Release string `json:"release"`
}

func (m *Manager) safeProjection() *managerJSON {
	if m == nil {
		return nil
	}
	return &managerJSON{Release: m.options.Release}
}

func (m *Manager) MarshalJSON() ([]byte, error) {
	return json.Marshal(m.safeProjection())
}

func (m *Manager) MarshalJSONTo(out *jsontext.Encoder) error {
	return json.MarshalEncode(out, m.safeProjection())
}

// Start validates its generated contract, starts ReleaseLoader in the
// background, and waits until the initial generation has been atomically
// published. No Manager is returned before generated Current is usable.
func Start(
	ctx context.Context,
	client *kmsclient.Client,
	options Options,
	prepare PrepareFunc,
) (*Manager, error) {
	if ctx == nil {
		return nil, errors.New("configstore: Start requires a context")
	}
	if client == nil {
		return nil, errors.New("configstore: Start requires a kmsclient client")
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
	bindingKeys := maps.Clone(options.BindingKeys)
	// The manager retains Options for callbacks and status bookkeeping; keep
	// credentials only in the lower-level loader.
	options.BindingKeys = nil

	manager := &Manager{
		options: options,
		prepare: prepare,
		readyCh: make(chan struct{}),
		done:    make(chan struct{}),
	}
	validateManifest := manifestValidator(options.Contract)
	loader, err := kmsclient.NewReleaseLoader(client, kmsclient.ReleaseLoaderConfig{
		Name:                options.Release,
		ReconcileInterval:   options.ReconcileInterval,
		SecretTokenProvider: options.SecretTokenProvider,
		BindingKeys:         bindingKeys,
		ValidateManifest: func(ctx context.Context, manifest kmsclient.ReleaseManifest) error {
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
		waitErr := manager.waitErr
		manager.mu.RUnlock()
		if ready {
			return manager, nil
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
	snapshot kmsclient.ReleaseSnapshot,
) (kmsclient.PreparedRelease, error) {
	return m.prepareWithIdentity(ctx, snapshot, ReleaseIdentityFromSnapshot(snapshot))
}

func (m *Manager) prepareWithIdentity(
	ctx context.Context,
	snapshot kmsclient.ReleaseSnapshot,
	identity ReleaseIdentity,
) (kmsclient.PreparedRelease, error) {
	m.mu.Lock()
	m.observed = identity
	startup := !m.ready
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
		// Divergence from source defaults is reported, never refused: a process
		// must be able to restart onto whatever release is active. The report is
		// the operator's signal to reconcile code and KMS.
		phase := PhaseRuntime
		if startup {
			phase = PhaseStartup
		}
		m.reportOnce(identity, newDefaultMismatchReport(phase, MismatchError, identity, differences))
	}

	var changes []FieldChange
	if !startup {
		changes = cloneChanges(candidate.Changed)
	}
	return &managedPrepared{
		manager:    m,
		publish:    candidate.Publish,
		abort:      abort,
		identity:   identity,
		divergent:  divergent,
		fieldCount: len(differences),
		startup:    startup,
		changes:    changes,
		groups:     candidate.Groups,
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

// reportOnce delivers a mismatch report at most once per release identity.
// The callback is an observer: a panic is swallowed and never influences
// candidate admission, so a broken logger cannot refuse a restart.
func (m *Manager) reportOnce(identity ReleaseIdentity, report DefaultMismatchReport) {
	key := identity.dedupeKey()
	m.reportMu.Lock()
	if m.lastReportedKey == key {
		m.reportMu.Unlock()
		return
	}
	m.lastReportedKey = key
	m.reportMu.Unlock()
	callback := m.options.OnDefaultMismatch
	if callback == nil {
		return
	}
	func() {
		defer func() { _ = recover() }()
		callback(report)
	}()
}

// clearReported forgets the last reported identity so that re-activating a
// previously reported divergent release after a clean one is reported again.
func (m *Manager) clearReported() {
	m.reportMu.Lock()
	m.lastReportedKey = ""
	m.reportMu.Unlock()
}

func (r ReleaseIdentity) dedupeKey() string {
	return fmt.Sprintf("%s\x00%s\x00%d\x00%d\x00%s",
		r.namespace, r.name, r.version, r.activationRevision, r.digest)
}

type managedPrepared struct {
	manager    *Manager
	publish    func()
	abort      func()
	identity   ReleaseIdentity
	divergent  bool
	fieldCount int
	startup    bool
	changes    []FieldChange
	groups     func() (map[string]jsontext.Value, error)
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
	if !p.divergent {
		p.manager.clearReported()
	}
	p.manager.notifyApplied(p)
	p.manager.readyOnce.Do(func() { close(p.manager.readyCh) })
}

// ReleaseDivergence implements kmsclient.ReleaseDivergenceReporter so the
// applied acknowledgement carries divergence without any field detail.
func (p *managedPrepared) ReleaseDivergence() (bool, int) {
	return p.divergent, p.fieldCount
}

func (m *Manager) notifyApplied(p *managedPrepared) {
	callback := m.options.OnApplied
	if callback == nil {
		return
	}
	phase := PhaseRuntime
	if p.startup {
		phase = PhaseStartup
	}
	report := newAppliedReport(phase, p.identity, p.divergent, p.changes, p.groups)
	// Commit must be infallible for the loader; a callback panic must never
	// turn a published generation into a fatal loader failure.
	func() {
		defer func() { _ = recover() }()
		callback(report)
	}()
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
