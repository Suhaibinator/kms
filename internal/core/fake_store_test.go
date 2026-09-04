package core

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"sort"
	"sync"
	"time"

	"github.com/Suhaibinator/kms/internal/crypto"
	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/storage"
)

// fakeStore is an in-memory storage.Store for core unit tests. It implements
// the flows the service actually exercises (namespaces, secrets, parameters,
// identities, certificates, the CA key, policies, audit) with just enough
// fidelity to drive real crypto round-trips, plus hooks/fields to inject
// failures. It never sees plaintext.
//
// Resources are keyed by their display path (ref.String()) or, for namespaces,
// by "env/app" (ns.String()).
type fakeStore struct {
	secretMu sync.RWMutex
	auditMu  sync.RWMutex

	identitiesByName map[string]domain.Identity
	identitiesByHash map[string]domain.Identity
	certsBySerial    map[string]certEntry
	policies         []domain.Policy
	secrets          map[string]*fakeSecret
	params           map[string]*fakeParam
	namespaces       map[string]domain.Namespace
	caKey            *storage.CAKeyRecord
	keys             []domain.KeyMetadata
	audits           []domain.AuditEvent
	revision         uint64
	nextSecretID     int64
	nextNamespaceID  int64

	// error injection
	pingErr  error
	auditErr error
	// bindingAuditErr fails only the transactional binding allow audit, so
	// tests can distinguish it from the subsequent best-effort error audit.
	bindingAuditErr error
	policiesErr     error
	// purgeResultErr simulates a post-commit purge cleanup failure: the fake
	// applies the tombstones/audit/revision, then returns this error with the
	// populated result just like SQLStore.
	purgeResultErr error

	// optional behavior overrides
	onPoliciesForSubject      func(subject string) ([]domain.Policy, error)
	beforeCreateSecretVersion func(storage.CreateSecretParams)
	beforeBindingOperation    func(operation string)
	onListNamespaces          func(storage.ListPage) ([]domain.Namespace, string, error)
	onListAudit               func(domain.AuditFilter, storage.ListPage) ([]domain.AuditEvent, string, error)
}

type certEntry struct {
	cert     domain.IdentityCert
	identity string
}

type fakeSecret struct {
	rec      storage.SecretRecord
	versions map[uint64]storage.SecretVersionRecord
	next     uint64
	current  uint64
	previous uint64
}

type fakeParam struct {
	cur     domain.Parameter
	history []domain.Parameter
}

// Compile-time assertion that the fake satisfies the real Store contract.
var _ storage.Store = (*fakeStore)(nil)

func newFakeStore() *fakeStore {
	return &fakeStore{
		identitiesByName: map[string]domain.Identity{},
		identitiesByHash: map[string]domain.Identity{},
		certsBySerial:    map[string]certEntry{},
		secrets:          map[string]*fakeSecret{},
		params:           map[string]*fakeParam{},
		namespaces:       map[string]domain.Namespace{},
	}
}

// --- test helpers ---

func (f *fakeStore) addIdentity(name, kind, token string) domain.Identity {
	id := domain.Identity{ID: int64(len(f.identitiesByName) + 1), Name: name, Kind: kind, CreatedAt: time.Now(), HasToken: token != ""}
	f.identitiesByName[name] = id
	if token != "" {
		f.identitiesByHash[string(crypto.TokenHash(token))] = id
	}
	return id
}

func (f *fakeStore) addPolicy(p domain.Policy) { f.policies = append(f.policies, p) }

// addNamespace seeds a namespace with the given allowed auth methods.
func (f *fakeStore) addNamespace(ns domain.NamespaceRef, methods ...domain.AuthMethod) {
	if len(methods) == 0 {
		methods = []domain.AuthMethod{domain.AuthMethodMTLS}
	}
	f.nextNamespaceID++
	f.namespaces[ns.String()] = domain.Namespace{
		ID:                 f.nextNamespaceID,
		NamespaceRef:       ns,
		AllowedAuthMethods: methods,
		CreatedAt:          time.Now(),
	}
}

func (f *fakeStore) lastAudit() (domain.AuditEvent, bool) {
	f.auditMu.RLock()
	defer f.auditMu.RUnlock()
	if len(f.audits) == 0 {
		return domain.AuditEvent{}, false
	}
	return f.audits[len(f.audits)-1], true
}

func (f *fakeStore) hasAudit(eventType, decision string) bool {
	f.auditMu.RLock()
	defer f.auditMu.RUnlock()
	for _, e := range f.audits {
		if e.EventType == eventType && e.Decision == decision {
			return true
		}
	}
	return false
}

func (f *fakeStore) tamperCiphertext(ref domain.Ref, version uint64) {
	f.secretMu.Lock()
	defer f.secretMu.Unlock()
	sec := f.secrets[ref.String()]
	rec := sec.versions[version]
	rec.Ciphertext = bytes.Clone(rec.Ciphertext)
	rec.Ciphertext[0] ^= 0xff
	sec.versions[version] = rec
}

// expireVersion ages a version's expiry into the past, simulating a version
// that expired during its lifetime (PutSecret rejects writing a past expiry).
func (f *fakeStore) expireVersion(ref domain.Ref, version uint64) {
	f.secretMu.Lock()
	defer f.secretMu.Unlock()
	sec := f.secrets[ref.String()]
	rec := sec.versions[version]
	rec.ExpiresAt = time.Now().Add(-time.Hour)
	sec.versions[version] = rec
}

func (f *fakeStore) markVersionDestroyedAt(ref domain.Ref, version uint64) {
	f.secretMu.Lock()
	defer f.secretMu.Unlock()
	sec := f.secrets[ref.String()]
	rec := sec.versions[version]
	rec.DestroyedAt = time.Now()
	sec.versions[version] = rec
}

// --- lifecycle ---

func (f *fakeStore) Ping(context.Context) error           { return f.pingErr }
func (f *fakeStore) Close() error                         { return nil }
func (f *fakeStore) Backup(context.Context, string) error { return nil }

// --- key metadata ---

func (f *fakeStore) InsertKeyMetadata(context.Context, domain.KeyMetadata) error { return nil }
func (f *fakeStore) GetKeyMetadata(context.Context, string) (domain.KeyMetadata, error) {
	return domain.KeyMetadata{}, domain.ErrNotFound
}
func (f *fakeStore) ListKeyMetadata(context.Context) ([]domain.KeyMetadata, error) {
	return f.keys, nil
}
func (f *fakeStore) ActiveKeyMetadata(context.Context) (domain.KeyMetadata, error) {
	return domain.KeyMetadata{}, domain.ErrNotFound
}
func (f *fakeStore) SetKeyState(context.Context, string, string) error { return nil }

func (f *fakeStore) RotateKEK(_ context.Context, newKM domain.KeyMetadata,
	rewrapSecret func(rec storage.SecretVersionRecord) ([]byte, error),
	rewrapCA func(rec storage.CAKeyRecord) ([]byte, error)) (int, int, error) {
	secrets := 0
	for _, sec := range f.secrets {
		for v, rec := range sec.versions {
			if rec.State == domain.StateDestroyed {
				continue
			}
			nd, err := rewrapSecret(rec)
			if err != nil {
				return 0, 0, err
			}
			rec.EncryptedDEK = nd
			rec.KEKID = newKM.ID
			sec.versions[v] = rec
			secrets++
		}
	}
	caCount := 0
	if f.caKey != nil {
		nd, err := rewrapCA(*f.caKey)
		if err != nil {
			return 0, 0, err
		}
		f.caKey.EncryptedDEK = nd
		f.caKey.KEKID = newKM.ID
		caCount++
	}
	f.keys = append(f.keys, newKM)
	return secrets, caCount, nil
}

// --- namespaces ---

func (f *fakeStore) CreateNamespace(_ context.Context, ns domain.Namespace) (domain.Namespace, error) {
	key := ns.String()
	if _, ok := f.namespaces[key]; ok {
		return domain.Namespace{}, domain.Errorf(domain.ErrAlreadyExists, "namespace %s", ns.NamespaceRef)
	}
	f.nextNamespaceID++
	ns.ID = f.nextNamespaceID
	f.namespaces[key] = ns
	return ns, nil
}

func (f *fakeStore) GetNamespace(_ context.Context, ref domain.NamespaceRef) (domain.Namespace, error) {
	ns, ok := f.namespaces[ref.String()]
	if !ok {
		return domain.Namespace{}, domain.Errorf(domain.ErrNotFound, "namespace %s", ref)
	}
	return ns, nil
}

func (f *fakeStore) UpdateNamespace(_ context.Context, ref domain.NamespaceRef, description string, methods []domain.AuthMethod) (domain.Namespace, error) {
	ns, ok := f.namespaces[ref.String()]
	if !ok {
		return domain.Namespace{}, domain.Errorf(domain.ErrNotFound, "namespace %s", ref)
	}
	ns.Description = description
	ns.AllowedAuthMethods = methods
	f.namespaces[ref.String()] = ns
	return ns, nil
}

func (f *fakeStore) DeleteNamespace(_ context.Context, ref domain.NamespaceRef) error {
	if _, ok := f.namespaces[ref.String()]; !ok {
		return domain.Errorf(domain.ErrNotFound, "namespace %s", ref)
	}
	for _, p := range f.params {
		if p.cur.Ref.NS == ref {
			return domain.Errorf(domain.ErrFailedPrecondition, "namespace %s is not empty (parameters)", ref)
		}
	}
	for _, sec := range f.secrets {
		if sec.rec.Ref.NS == ref {
			return domain.Errorf(domain.ErrFailedPrecondition, "namespace %s is not empty (secrets)", ref)
		}
	}
	for _, id := range f.identitiesByName {
		if id.Namespace != nil && *id.Namespace == ref {
			return domain.Errorf(domain.ErrFailedPrecondition, "namespace %s is not empty (bound identities)", ref)
		}
	}
	delete(f.namespaces, ref.String())
	return nil
}

func (f *fakeStore) ListNamespaces(_ context.Context, page storage.ListPage) ([]domain.Namespace, string, error) {
	if f.onListNamespaces != nil {
		return f.onListNamespaces(page)
	}
	out := make([]domain.Namespace, 0, len(f.namespaces))
	for _, ns := range f.namespaces {
		out = append(out, ns)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Env != out[j].Env {
			return out[i].Env < out[j].Env
		}
		return out[i].App < out[j].App
	})
	return out, "", nil
}

// --- parameters ---

func (f *fakeStore) PutParameter(_ context.Context, ref domain.Ref, value, contentType, metadata, createdBy string) (uint64, uint64, error) {
	// Intentional fidelity gap: the real SQLStore requires the namespace to
	// pre-exist and returns ErrNotFound otherwise (covered in storage/store_test.go);
	// this fake is deliberately lenient and writes without that check.
	p := f.params[ref.String()]
	if p == nil {
		p = &fakeParam{}
		f.params[ref.String()] = p
	}
	version := uint64(len(p.history) + 1)
	f.revision++
	param := domain.Parameter{
		Ref: ref, Value: value, ContentType: contentType, Version: version,
		Metadata: metadata, CreatedBy: createdBy, CreatedAt: time.Now(),
		Labels: map[string]uint64{domain.LabelCurrent: version},
	}
	p.history = append(p.history, param)
	p.cur = param
	return version, f.revision, nil
}

func (f *fakeStore) GetParameter(_ context.Context, ref domain.Ref, version uint64, _ string) (domain.Parameter, error) {
	p := f.params[ref.String()]
	if p == nil {
		return domain.Parameter{}, domain.Errorf(domain.ErrNotFound, "parameter %s", ref)
	}
	if version > 0 {
		if int(version) <= len(p.history) {
			return p.history[version-1], nil
		}
		return domain.Parameter{}, domain.Errorf(domain.ErrNotFound, "parameter %s v%d", ref, version)
	}
	return p.cur, nil
}

func (f *fakeStore) GetParameterInfo(_ context.Context, ref domain.Ref) (domain.ParameterInfo, error) {
	p := f.params[ref.String()]
	if p == nil {
		return domain.ParameterInfo{}, domain.Errorf(domain.ErrNotFound, "parameter %s", ref)
	}
	return domain.ParameterInfo{Ref: ref, ContentType: p.cur.ContentType, Metadata: p.cur.Metadata}, nil
}

func (f *fakeStore) ListParameters(_ context.Context, ns domain.NamespaceRef, keyPrefix string, _ storage.ListPage) ([]domain.Parameter, string, error) {
	var out []domain.Parameter
	for _, p := range f.params {
		if p.cur.Ref.NS == ns && keyHasPrefix(p.cur.Ref.Key, keyPrefix) {
			out = append(out, p.cur)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Ref.Key < out[j].Ref.Key })
	return out, "", nil
}

func (f *fakeStore) DeleteParameter(_ context.Context, ref domain.Ref) (uint64, error) {
	if _, ok := f.params[ref.String()]; !ok {
		return 0, domain.Errorf(domain.ErrNotFound, "parameter %s", ref)
	}
	delete(f.params, ref.String())
	f.revision++
	return f.revision, nil
}

// --- secrets ---

func (f *fakeStore) CreateSecretVersion(_ context.Context, p storage.CreateSecretParams) (uint64, uint64, error) {
	// Intentional fidelity gap: the real SQLStore requires the namespace to
	// pre-exist and returns ErrNotFound otherwise (covered in storage/store_test.go);
	// this fake is deliberately lenient and writes without that check.
	if hook := f.beforeCreateSecretVersion; hook != nil {
		f.beforeCreateSecretVersion = nil
		hook(p)
	}
	f.secretMu.Lock()
	defer f.secretMu.Unlock()
	key := p.Ref.String()
	sec := f.secrets[key]
	if p.Expected != nil {
		exists := sec != nil
		if exists != p.Expected.Exists || (exists && (sec.rec.ID != p.Expected.ID || !bytes.Equal(sec.rec.AccessTokenHash, p.Expected.AccessTokenHash))) {
			return 0, 0, domain.Errorf(domain.ErrAborted, "secret changed concurrently")
		}
	}
	if sec == nil {
		sec = &fakeSecret{versions: map[uint64]storage.SecretVersionRecord{}}
	}
	version := sec.next + 1
	payload, err := p.Encrypt(version)
	if err != nil {
		return 0, 0, err
	}
	if sec.rec.ID == 0 {
		f.nextSecretID++
		sec.rec = storage.SecretRecord{
			ID:        f.nextSecretID,
			Ref:       p.Ref,
			CreatedAt: time.Now(),
			Labels:    map[string]uint64{},
		}
		f.secrets[key] = sec
	}
	sec.next = version
	sec.rec.ContentType = p.ContentType
	sec.rec.Metadata = p.Metadata
	sec.rec.Bound = p.Bound
	sec.rec.UpdatedAt = time.Now()
	if p.AccessTokenHash != nil {
		sec.rec.AccessTokenHash = bytes.Clone(p.AccessTokenHash)
	}
	sec.versions[version] = storage.SecretVersionRecord{
		Version: version, ContentType: p.ContentType, Bound: p.Bound,
		HasAccessToken: len(sec.rec.AccessTokenHash) > 0,
		Ciphertext:     payload.Ciphertext, EncryptedDEK: payload.EncryptedDEK,
		KEKID: payload.KEKID, WrapMode: payload.WrapMode, BindingKeySalt: payload.BindingKeySalt,
		Algorithm: payload.Algorithm, Nonce: payload.Nonce, AAD: payload.AAD,
		State: domain.StateEnabled, CreatedBy: p.CreatedBy, CreatedAt: time.Now(), ExpiresAt: p.ExpiresAt,
		Metadata: p.Metadata,
	}
	if sec.current != 0 {
		sec.previous = sec.current
		sec.rec.Labels[domain.LabelPrevious] = sec.previous
	}
	sec.current = version
	sec.rec.Labels[domain.LabelCurrent] = version
	f.revision++
	return version, f.revision, nil
}

func (f *fakeStore) TransitionSecretVersion(_ context.Context, p storage.SecretVersionTransitionParams) (storage.SecretVersionTransitionResult, error) {
	if f.beforeBindingOperation != nil {
		f.beforeBindingOperation(string(p.Kind))
	}
	f.secretMu.Lock()
	defer f.secretMu.Unlock()
	sec := f.secrets[p.Ref.String()]
	if sec == nil {
		return storage.SecretVersionTransitionResult{}, domain.Errorf(domain.ErrNotFound, "secret %s", p.Ref)
	}
	if p.ExpectedCurrentVersion == 0 {
		return storage.SecretVersionTransitionResult{}, domain.Errorf(domain.ErrInvalidArgument, "expected current version is required")
	}
	if sec.current != p.ExpectedCurrentVersion {
		return storage.SecretVersionTransitionResult{}, domain.Errorf(domain.ErrAborted, "secret current version changed")
	}
	source := sec.versions[sec.current]
	if source.State == domain.StateDestroyed {
		return storage.SecretVersionTransitionResult{}, domain.Errorf(domain.ErrFailedPrecondition, "secret current version is destroyed")
	}
	targetBound := p.Kind != storage.SecretTransitionUnbind
	if p.Kind == storage.SecretTransitionBind && source.Bound {
		return storage.SecretVersionTransitionResult{}, domain.Errorf(domain.ErrFailedPrecondition, "secret current version is already bound")
	}
	if p.Kind != storage.SecretTransitionBind && !source.Bound {
		return storage.SecretVersionTransitionResult{}, domain.Errorf(domain.ErrFailedPrecondition, "secret current version is not bound")
	}
	newVersion := sec.next + 1
	payload, err := p.Encrypt(source, newVersion)
	if err != nil {
		return storage.SecretVersionTransitionResult{}, err
	}
	f.auditMu.Lock()
	defer f.auditMu.Unlock()
	if err := f.requiredBindingAuditError(); err != nil {
		return storage.SecretVersionTransitionResult{}, err
	}
	now := time.Now()
	clone := source
	clone.ID = 0
	clone.Version = newVersion
	clone.Bound = targetBound
	clone.Ciphertext = bytes.Clone(payload.Ciphertext)
	clone.EncryptedDEK = bytes.Clone(payload.EncryptedDEK)
	clone.KEKID = payload.KEKID
	clone.WrapMode = payload.WrapMode
	clone.BindingKeySalt = bytes.Clone(payload.BindingKeySalt)
	clone.Algorithm = payload.Algorithm
	clone.Nonce = bytes.Clone(payload.Nonce)
	clone.AAD = payload.AAD
	clone.CreatedBy = p.CreatedBy
	clone.CreatedAt = now
	clone.DestroyedAt = time.Time{}
	sec.versions[newVersion] = clone
	sec.next = newVersion
	sec.previous = sec.current
	sec.current = newVersion
	sec.rec.Labels[domain.LabelPrevious] = sec.previous
	sec.rec.Labels[domain.LabelCurrent] = sec.current
	sec.rec.Bound = targetBound
	sec.rec.ContentType = clone.ContentType
	sec.rec.Metadata = clone.Metadata
	sec.rec.UpdatedAt = now
	f.revision++
	eventType := "secret." + string(p.Kind)
	if p.Kind == storage.SecretTransitionRotate {
		eventType = "secret.binding_key.rotate"
	}
	f.appendBindingAuditLocked(eventType, p.Ref, newVersion, []uint64{sec.previous, newVersion}, p.Audit)
	return storage.SecretVersionTransitionResult{CurrentVersion: newVersion, PreviousVersion: sec.previous, Revision: f.revision}, nil
}

func (f *fakeStore) PreviewSecretBindingCohort(_ context.Context, ref domain.Ref, anchor uint64, test storage.SecretBindingTestFunc) (storage.SecretBindingResult, error) {
	if f.beforeBindingOperation != nil {
		f.beforeBindingOperation("preview")
	}
	f.secretMu.RLock()
	defer f.secretMu.RUnlock()
	_, resolved, _, err := f.bindingVersion(ref, anchor)
	if err != nil {
		return storage.SecretBindingResult{}, err
	}
	affected, err := f.bindingCohort(ref, resolved, test)
	if err != nil {
		return storage.SecretBindingResult{}, err
	}
	return storage.SecretBindingResult{AnchorVersion: resolved, AffectedVersions: affected, Revision: f.revision}, nil
}

func (f *fakeStore) appendBindingAuditLocked(eventType string, ref domain.Ref, anchor uint64, affected []uint64, audit storage.SecretBindingAudit) {
	metadata, _ := json.Marshal(affected)
	ns := f.namespaces[ref.NS.String()]
	f.audits = append(f.audits, domain.AuditEvent{
		EventType: eventType, ActorIdentity: audit.ActorIdentity, ActorType: audit.ActorType,
		ResourceType: domain.ResourceSecret, ResourceNamespaceID: ns.ID, ResourceEnv: ref.NS.Env,
		ResourceApp: ref.NS.App, ResourceKey: ref.Key, ResourceVersion: anchor, Decision: "allow",
		SourceIP: audit.SourceIP, UserAgent: audit.UserAgent, RequestID: audit.RequestID,
		CreatedAt: audit.CreatedAt, Metadata: `{"affected_versions":` + string(metadata) + `}`,
	})
}

func (f *fakeStore) requiredBindingAuditError() error {
	if f.bindingAuditErr != nil || f.auditErr != nil {
		return storage.ErrRequiredAuditUnavailable
	}
	return nil
}

func (f *fakeStore) PurgeSecretBindingCohort(_ context.Context, ref domain.Ref, anchor uint64, guard storage.SecretBindingCASGuard, test storage.SecretBindingTestFunc, audit storage.SecretBindingPurgeAudit) (storage.SecretBindingResult, error) {
	if f.beforeBindingOperation != nil {
		f.beforeBindingOperation("purge")
	}
	f.secretMu.Lock()
	defer f.secretMu.Unlock()
	if err := validateFakeBindingGuard(guard); err != nil {
		return storage.SecretBindingResult{}, err
	}
	sec, resolved, _, err := f.bindingVersion(ref, anchor)
	if err != nil {
		return storage.SecretBindingResult{}, err
	}
	affected, err := f.bindingCohort(ref, resolved, test)
	if err != nil {
		return storage.SecretBindingResult{}, err
	}
	if err := f.checkBindingGuard(guard, affected); err != nil {
		return storage.SecretBindingResult{}, err
	}
	f.auditMu.Lock()
	defer f.auditMu.Unlock()
	if err := f.requiredBindingAuditError(); err != nil {
		return storage.SecretBindingResult{}, err
	}

	destroyedAt := time.Now()
	for _, version := range affected {
		rec := sec.versions[version]
		rec.ContentType = ""
		rec.Bound = false
		rec.HasAccessToken = false
		rec.Ciphertext = nil
		rec.EncryptedDEK = nil
		rec.KEKID = ""
		rec.WrapMode = ""
		rec.BindingKeySalt = nil
		rec.Algorithm = ""
		rec.Nonce = nil
		rec.AAD = ""
		rec.State = domain.StateDestroyed
		rec.DestroyedAt = destroyedAt
		rec.ExpiresAt = time.Time{}
		rec.Metadata = ""
		sec.versions[version] = rec
	}
	if containsFakeVersion(affected, sec.current) {
		sec.rec.Bound = false
		sec.rec.ContentType = ""
		sec.rec.Metadata = ""
	}
	f.revision++
	f.appendBindingAuditLocked("secret.binding_cohort.purge", ref, resolved, affected, audit)
	result := storage.SecretBindingResult{AnchorVersion: resolved, AffectedVersions: affected, Revision: f.revision}
	return result, f.purgeResultErr
}

func (f *fakeStore) PreviewSecretUnboundVersions(_ context.Context, ref domain.Ref) (storage.SecretVersionSetResult, error) {
	f.secretMu.RLock()
	defer f.secretMu.RUnlock()
	sec := f.secrets[ref.String()]
	if sec == nil {
		return storage.SecretVersionSetResult{}, domain.Errorf(domain.ErrNotFound, "secret %s", ref)
	}
	versions := make([]uint64, 0)
	for version := uint64(1); version <= sec.next; version++ {
		rec, ok := sec.versions[version]
		if ok && rec.State != domain.StateDestroyed && !rec.Bound {
			versions = append(versions, version)
		}
	}
	if len(versions) == 0 {
		return storage.SecretVersionSetResult{}, domain.Errorf(domain.ErrFailedPrecondition, "secret has no unbound versions")
	}
	return storage.SecretVersionSetResult{AffectedVersions: versions, Revision: f.revision}, nil
}

func (f *fakeStore) PurgeSecretUnboundVersions(_ context.Context, ref domain.Ref, expectedRevision uint64, expectedAffected []uint64, audit storage.SecretBindingPurgeAudit) (storage.SecretVersionSetResult, error) {
	f.secretMu.Lock()
	defer f.secretMu.Unlock()
	sec := f.secrets[ref.String()]
	if sec == nil {
		return storage.SecretVersionSetResult{}, domain.Errorf(domain.ErrNotFound, "secret %s", ref)
	}
	affected := make([]uint64, 0)
	for version := uint64(1); version <= sec.next; version++ {
		rec, ok := sec.versions[version]
		if ok && rec.State != domain.StateDestroyed && !rec.Bound {
			affected = append(affected, version)
		}
	}
	if expectedRevision == 0 || len(expectedAffected) == 0 {
		return storage.SecretVersionSetResult{}, domain.Errorf(domain.ErrInvalidArgument, "exact preview guard is required")
	}
	if expectedRevision != f.revision || !equalFakeVersions(expectedAffected, affected) {
		return storage.SecretVersionSetResult{}, domain.Errorf(domain.ErrAborted, "secret version set changed")
	}
	f.auditMu.Lock()
	defer f.auditMu.Unlock()
	if err := f.requiredBindingAuditError(); err != nil {
		return storage.SecretVersionSetResult{}, err
	}
	destroyedAt := time.Now()
	for _, version := range affected {
		rec := sec.versions[version]
		rec.ContentType = ""
		rec.Bound = false
		rec.HasAccessToken = false
		rec.Ciphertext = nil
		rec.EncryptedDEK = nil
		rec.KEKID = ""
		rec.WrapMode = ""
		rec.BindingKeySalt = nil
		rec.Algorithm = ""
		rec.Nonce = nil
		rec.AAD = ""
		rec.State = domain.StateDestroyed
		rec.DestroyedAt = destroyedAt
		rec.ExpiresAt = time.Time{}
		rec.Metadata = ""
		sec.versions[version] = rec
	}
	if containsFakeVersion(affected, sec.current) {
		sec.rec.Bound = false
		sec.rec.ContentType = ""
		sec.rec.Metadata = ""
	}
	f.revision++
	f.appendBindingAuditLocked("secret.unbound_versions.purge", ref, affected[0], affected, audit)
	result := storage.SecretVersionSetResult{AffectedVersions: append([]uint64(nil), affected...), Revision: f.revision}
	return result, f.purgeResultErr
}

func (f *fakeStore) bindingVersion(ref domain.Ref, version uint64) (*fakeSecret, uint64, storage.SecretVersionRecord, error) {
	sec := f.secrets[ref.String()]
	if sec == nil {
		return nil, 0, storage.SecretVersionRecord{}, domain.Errorf(domain.ErrNotFound, "secret %s", ref)
	}
	resolved, ok := f.resolveVersion(sec, version, "")
	if !ok {
		return nil, 0, storage.SecretVersionRecord{}, domain.Errorf(domain.ErrNotFound, "secret %s version", ref)
	}
	return sec, resolved, sec.versions[resolved], nil
}

func (f *fakeStore) bindingCohort(ref domain.Ref, anchor uint64, test storage.SecretBindingTestFunc) ([]uint64, error) {
	sec := f.secrets[ref.String()]
	anchorRecord, ok := sec.versions[anchor]
	if !ok {
		return nil, domain.Errorf(domain.ErrNotFound, "secret %s v%d", ref, anchor)
	}
	if anchorRecord.State == domain.StateDestroyed || !anchorRecord.Bound {
		return nil, domain.Errorf(domain.ErrFailedPrecondition, "secret anchor is unavailable or unbound")
	}
	if err := test(anchorRecord); err != nil {
		return nil, err
	}

	first, last := anchor, anchor
	for version := anchor - 1; version > 0; version-- {
		rec, ok := sec.versions[version]
		if !ok || rec.State == domain.StateDestroyed || !rec.Bound || test(rec) != nil {
			break
		}
		first = version
	}
	for version := anchor + 1; version <= sec.next; version++ {
		rec, ok := sec.versions[version]
		if !ok || rec.State == domain.StateDestroyed || !rec.Bound || test(rec) != nil {
			break
		}
		last = version
	}
	affected := make([]uint64, 0, last-first+1)
	for version := first; version <= last; version++ {
		affected = append(affected, version)
	}
	return affected, nil
}

func validateFakeBindingGuard(guard storage.SecretBindingCASGuard) error {
	if guard.ExpectedRevision == 0 || len(guard.ExpectedAffectedVersions) == 0 {
		return domain.Errorf(domain.ErrInvalidArgument, "expected revision and affected versions must be supplied together")
	}
	for i, version := range guard.ExpectedAffectedVersions {
		if version == 0 || (i > 0 && version <= guard.ExpectedAffectedVersions[i-1]) {
			return domain.Errorf(domain.ErrInvalidArgument, "expected affected versions must be sorted and unique")
		}
	}
	return nil
}

func (f *fakeStore) checkBindingGuard(guard storage.SecretBindingCASGuard, affected []uint64) error {
	if guard.ExpectedRevision != f.revision || !equalFakeVersions(guard.ExpectedAffectedVersions, affected) {
		return domain.Errorf(domain.ErrAborted, "secret version set changed")
	}
	return nil
}

func equalFakeVersions(a, b []uint64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func containsFakeVersion(versions []uint64, target uint64) bool {
	for _, version := range versions {
		if version == target {
			return true
		}
	}
	return false
}

func (f *fakeStore) resolveVersion(sec *fakeSecret, version uint64, label string) (uint64, bool) {
	if version > 0 {
		_, ok := sec.versions[version]
		return version, ok
	}
	switch label {
	case "", domain.LabelCurrent:
		return sec.current, sec.current != 0
	case domain.LabelPrevious:
		return sec.previous, sec.previous != 0
	default:
		return 0, false
	}
}

func (f *fakeStore) GetSecretRecord(_ context.Context, ref domain.Ref) (storage.SecretRecord, error) {
	f.secretMu.RLock()
	defer f.secretMu.RUnlock()
	sec := f.secrets[ref.String()]
	if sec == nil {
		return storage.SecretRecord{}, domain.Errorf(domain.ErrNotFound, "secret %s", ref)
	}
	return sec.rec, nil
}

func (f *fakeStore) GetSecretVersion(_ context.Context, ref domain.Ref, version uint64, label string) (storage.SecretRecord, storage.SecretVersionRecord, error) {
	f.secretMu.RLock()
	defer f.secretMu.RUnlock()
	sec := f.secrets[ref.String()]
	if sec == nil {
		return storage.SecretRecord{}, storage.SecretVersionRecord{}, domain.Errorf(domain.ErrNotFound, "secret %s", ref)
	}
	v, ok := f.resolveVersion(sec, version, label)
	if !ok {
		return storage.SecretRecord{}, storage.SecretVersionRecord{}, domain.Errorf(domain.ErrNotFound, "secret %s version", ref)
	}
	return sec.rec, sec.versions[v], nil
}

func (f *fakeStore) GetSecretInfo(_ context.Context, ref domain.Ref) (domain.Secret, error) {
	f.secretMu.RLock()
	defer f.secretMu.RUnlock()
	sec := f.secrets[ref.String()]
	if sec == nil {
		return domain.Secret{}, domain.Errorf(domain.ErrNotFound, "secret %s", ref)
	}
	versions := make([]domain.SecretVersionInfo, 0, len(sec.versions))
	for _, rec := range sec.versions {
		versions = append(versions, domain.SecretVersionInfo{
			Version: rec.Version, State: rec.State, Bound: rec.Bound, HasAccessToken: rec.HasAccessToken,
			CreatedBy: rec.CreatedBy, CreatedAt: rec.CreatedAt, DestroyedAt: rec.DestroyedAt,
			ExpiresAt: rec.ExpiresAt, Metadata: rec.Metadata,
		})
	}
	sort.Slice(versions, func(i, j int) bool { return versions[i].Version < versions[j].Version })
	return domain.Secret{
		Ref: ref, ContentType: sec.rec.ContentType, Bound: sec.rec.Bound,
		HasAccessToken: len(sec.rec.AccessTokenHash) > 0, Metadata: sec.rec.Metadata,
		CreatedAt: sec.rec.CreatedAt, UpdatedAt: sec.rec.UpdatedAt,
		Labels: sec.rec.Labels, Versions: versions,
	}, nil
}

func (f *fakeStore) GetSecretVersionInfo(ctx context.Context, ref domain.Ref, version uint64) (domain.Secret, error) {
	info, err := f.GetSecretInfo(ctx, ref)
	if err != nil {
		return domain.Secret{}, err
	}
	for _, candidate := range info.Versions {
		if candidate.Version == version {
			info.Bound = candidate.Bound
			info.Versions = []domain.SecretVersionInfo{candidate}
			info.Labels = nil
			return info, nil
		}
	}
	return domain.Secret{}, domain.Errorf(domain.ErrNotFound, "secret %s version %d", ref, version)
}

func (f *fakeStore) ListSecrets(_ context.Context, ns domain.NamespaceRef, keyPrefix string, _ storage.ListPage) ([]domain.Secret, string, error) {
	f.secretMu.RLock()
	defer f.secretMu.RUnlock()
	var out []domain.Secret
	for _, sec := range f.secrets {
		if sec.rec.Ref.NS == ns && keyHasPrefix(sec.rec.Ref.Key, keyPrefix) {
			out = append(out, domain.Secret{
				Ref: sec.rec.Ref, ContentType: sec.rec.ContentType, Bound: sec.rec.Bound,
				HasAccessToken: len(sec.rec.AccessTokenHash) > 0, Metadata: sec.rec.Metadata,
				CreatedAt: sec.rec.CreatedAt, UpdatedAt: sec.rec.UpdatedAt, Labels: sec.rec.Labels,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Ref.Key < out[j].Ref.Key })
	return out, "", nil
}

func (f *fakeStore) DeleteSecret(_ context.Context, ref domain.Ref) (uint64, error) {
	f.secretMu.Lock()
	defer f.secretMu.Unlock()
	if _, ok := f.secrets[ref.String()]; !ok {
		return 0, domain.Errorf(domain.ErrNotFound, "secret %s", ref)
	}
	delete(f.secrets, ref.String())
	f.revision++
	return f.revision, nil
}

func (f *fakeStore) SetSecretVersionState(_ context.Context, ref domain.Ref, version uint64, state string) (uint64, error) {
	f.secretMu.Lock()
	defer f.secretMu.Unlock()
	sec := f.secrets[ref.String()]
	if sec == nil {
		return 0, domain.Errorf(domain.ErrNotFound, "secret %s", ref)
	}
	apply := func(v uint64) {
		rec := sec.versions[v]
		if rec.State != domain.StateDestroyed {
			rec.State = state
			sec.versions[v] = rec
		}
	}
	if version == 0 {
		for v := range sec.versions {
			apply(v)
		}
	} else {
		if _, ok := sec.versions[version]; !ok {
			return 0, domain.Errorf(domain.ErrNotFound, "secret %s v%d", ref, version)
		}
		apply(version)
	}
	f.revision++
	return f.revision, nil
}

func (f *fakeStore) DestroySecretVersion(_ context.Context, ref domain.Ref, version uint64) (uint64, error) {
	f.secretMu.Lock()
	defer f.secretMu.Unlock()
	sec := f.secrets[ref.String()]
	if sec == nil {
		return 0, domain.Errorf(domain.ErrNotFound, "secret %s", ref)
	}
	rec, ok := sec.versions[version]
	if !ok {
		return 0, domain.Errorf(domain.ErrNotFound, "secret %s v%d", ref, version)
	}
	rec.State = domain.StateDestroyed
	rec.Ciphertext, rec.EncryptedDEK, rec.Nonce = nil, nil, nil
	rec.DestroyedAt = time.Now()
	sec.versions[version] = rec
	f.revision++
	return f.revision, nil
}

func (f *fakeStore) PromoteSecretVersion(_ context.Context, ref domain.Ref, version uint64) (uint64, uint64, uint64, error) {
	f.secretMu.Lock()
	defer f.secretMu.Unlock()
	sec := f.secrets[ref.String()]
	if sec == nil {
		return 0, 0, 0, domain.Errorf(domain.ErrNotFound, "secret %s", ref)
	}
	rec, ok := sec.versions[version]
	if !ok {
		return 0, 0, 0, domain.Errorf(domain.ErrNotFound, "secret %s v%d", ref, version)
	}
	if rec.State != domain.StateEnabled || !rec.DestroyedAt.IsZero() {
		return 0, 0, 0, domain.Errorf(domain.ErrFailedPrecondition, "version not enabled")
	}
	prev := sec.current
	if version != sec.current {
		sec.previous = sec.current
		sec.rec.Labels[domain.LabelPrevious] = sec.previous
	}
	sec.current = version
	sec.rec.Labels[domain.LabelCurrent] = version
	sec.rec.ContentType = rec.ContentType
	sec.rec.Metadata = rec.Metadata
	sec.rec.Bound = rec.Bound
	sec.rec.UpdatedAt = time.Now()
	f.revision++
	return version, prev, f.revision, nil
}

func (f *fakeStore) UpdateSecretAccessTokenHash(_ context.Context, ref domain.Ref, hash []byte) error {
	f.secretMu.Lock()
	defer f.secretMu.Unlock()
	sec := f.secrets[ref.String()]
	if sec == nil {
		return domain.Errorf(domain.ErrNotFound, "secret %s", ref)
	}
	sec.rec.AccessTokenHash = hash
	return nil
}

// --- identities ---

func (f *fakeStore) CreateIdentity(_ context.Context, params storage.CreateIdentityParams) (domain.Identity, error) {
	if _, ok := f.identitiesByName[params.Name]; ok {
		return domain.Identity{}, domain.Errorf(domain.ErrAlreadyExists, "identity %s", params.Name)
	}
	if params.Namespace != nil {
		if _, ok := f.namespaces[params.Namespace.String()]; !ok {
			return domain.Identity{}, domain.Errorf(domain.ErrNotFound, "namespace %s", *params.Namespace)
		}
	}
	id := domain.Identity{
		ID: int64(len(f.identitiesByName) + 1), Name: params.Name, Kind: params.Kind,
		CreatedAt: time.Now(), Namespace: params.Namespace, HasToken: len(params.TokenHash) > 0,
	}
	f.identitiesByName[params.Name] = id
	if len(params.TokenHash) > 0 {
		f.identitiesByHash[string(params.TokenHash)] = id
	}
	if params.Cert != nil {
		if _, exists := f.certsBySerial[params.Cert.Serial]; exists {
			delete(f.identitiesByName, params.Name)
			delete(f.identitiesByHash, string(params.TokenHash))
			return domain.Identity{}, domain.Errorf(domain.ErrAlreadyExists, "certificate %s", params.Cert.Serial)
		}
		f.certsBySerial[params.Cert.Serial] = certEntry{cert: *params.Cert, identity: params.Name}
	}
	return id, nil
}

func (f *fakeStore) GetIdentityByTokenHash(_ context.Context, tokenHash []byte) (domain.Identity, error) {
	id, ok := f.identitiesByHash[string(tokenHash)]
	if !ok {
		return domain.Identity{}, domain.Errorf(domain.ErrNotFound, "identity")
	}
	return id, nil
}

func (f *fakeStore) GetIdentityByName(_ context.Context, name string) (domain.Identity, error) {
	id, ok := f.identitiesByName[name]
	if !ok {
		return domain.Identity{}, domain.Errorf(domain.ErrNotFound, "identity %s", name)
	}
	id.Certs = f.certsForIdentity(name)
	return id, nil
}

func (f *fakeStore) certsForIdentity(name string) []domain.IdentityCert {
	var out []domain.IdentityCert
	for _, e := range f.certsBySerial {
		if e.identity == name {
			out = append(out, e.cert)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Serial < out[j].Serial })
	return out
}

func (f *fakeStore) ListIdentities(context.Context, storage.ListPage) ([]domain.Identity, string, error) {
	var out []domain.Identity
	for _, id := range f.identitiesByName {
		id.Certs = f.certsForIdentity(id.Name)
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, "", nil
}

func (f *fakeStore) SetIdentityDisabled(_ context.Context, name string, disabled bool) error {
	id, ok := f.identitiesByName[name]
	if !ok {
		return domain.Errorf(domain.ErrNotFound, "identity %s", name)
	}
	id.Disabled = disabled
	f.identitiesByName[name] = id
	// Keep the hash-indexed copy (read by GetIdentityByTokenHash) in sync.
	for h, existing := range f.identitiesByHash {
		if existing.Name == name {
			existing.Disabled = disabled
			f.identitiesByHash[h] = existing
		}
	}
	return nil
}

func (f *fakeStore) UpdateIdentityTokenHash(_ context.Context, name string, tokenHash []byte) error {
	id, ok := f.identitiesByName[name]
	if !ok {
		return domain.Errorf(domain.ErrNotFound, "identity %s", name)
	}
	// The real store keeps one hash column per identity, so rotating a token
	// invalidates the old one. Mirror that: drop any existing hash for this
	// identity before installing the new one.
	for h, existing := range f.identitiesByHash {
		if existing.Name == name {
			delete(f.identitiesByHash, h)
		}
	}
	id.HasToken = len(tokenHash) > 0
	f.identitiesByName[name] = id
	if len(tokenHash) > 0 {
		f.identitiesByHash[string(tokenHash)] = id
	}
	return nil
}

// --- built-in CA / client certificates ---

func (f *fakeStore) InsertCAKey(_ context.Context, ca storage.CAKeyRecord) error {
	rec := ca
	f.caKey = &rec
	return nil
}

func (f *fakeStore) ActiveCAKey(context.Context) (storage.CAKeyRecord, error) {
	if f.caKey == nil {
		return storage.CAKeyRecord{}, domain.Errorf(domain.ErrNotFound, "no active ca key")
	}
	return *f.caKey, nil
}

func (f *fakeStore) InsertIdentityCert(_ context.Context, identityName string, cert domain.IdentityCert) error {
	if _, ok := f.identitiesByName[identityName]; !ok {
		return domain.Errorf(domain.ErrNotFound, "identity %s", identityName)
	}
	if _, ok := f.certsBySerial[cert.Serial]; ok {
		return domain.Errorf(domain.ErrAlreadyExists, "certificate %s", cert.Serial)
	}
	f.certsBySerial[cert.Serial] = certEntry{cert: cert, identity: identityName}
	return nil
}

func (f *fakeStore) ListIdentityCerts(_ context.Context, identityName string) ([]domain.IdentityCert, error) {
	if _, ok := f.identitiesByName[identityName]; !ok {
		return nil, domain.Errorf(domain.ErrNotFound, "identity %s", identityName)
	}
	return f.certsForIdentity(identityName), nil
}

func (f *fakeStore) GetIdentityCertBySerial(_ context.Context, serial string) (storage.IdentityCertRecord, error) {
	e, ok := f.certsBySerial[serial]
	if !ok {
		return storage.IdentityCertRecord{}, domain.Errorf(domain.ErrNotFound, "certificate %s", serial)
	}
	return storage.IdentityCertRecord{
		Cert:             e.cert,
		IdentityName:     e.identity,
		IdentityDisabled: f.identitiesByName[e.identity].Disabled,
	}, nil
}

func (f *fakeStore) RevokeIdentityCert(_ context.Context, serial string) error {
	e, ok := f.certsBySerial[serial]
	if !ok {
		return domain.Errorf(domain.ErrNotFound, "certificate %s", serial)
	}
	if e.cert.RevokedAt.IsZero() {
		e.cert.RevokedAt = time.Now()
		f.certsBySerial[serial] = e
	}
	return nil
}

// --- policies ---

func (f *fakeStore) CreatePolicy(_ context.Context, p domain.Policy) (domain.Policy, error) {
	f.policies = append(f.policies, p)
	return p, nil
}
func (f *fakeStore) UpdatePolicy(_ context.Context, p domain.Policy) (domain.Policy, error) {
	for i := range f.policies {
		if f.policies[i].Name == p.Name {
			f.policies[i] = p
			return p, nil
		}
	}
	return domain.Policy{}, domain.Errorf(domain.ErrNotFound, "policy %s", p.Name)
}
func (f *fakeStore) DeletePolicy(_ context.Context, name string) error {
	for i := range f.policies {
		if f.policies[i].Name == name {
			f.policies = append(f.policies[:i], f.policies[i+1:]...)
			return nil
		}
	}
	return domain.Errorf(domain.ErrNotFound, "policy %s", name)
}
func (f *fakeStore) ListPolicies(context.Context, storage.ListPage) ([]domain.Policy, string, error) {
	return f.policies, "", nil
}
func (f *fakeStore) PoliciesForSubject(_ context.Context, subject string) ([]domain.Policy, error) {
	if f.onPoliciesForSubject != nil {
		return f.onPoliciesForSubject(subject)
	}
	if f.policiesErr != nil {
		return nil, f.policiesErr
	}
	var out []domain.Policy
	for _, p := range f.policies {
		if p.Subject == subject || p.Subject == "*" {
			out = append(out, p)
		}
	}
	return out, nil
}

// --- audit ---

func (f *fakeStore) AppendAudit(_ context.Context, ev domain.AuditEvent) error {
	f.auditMu.Lock()
	defer f.auditMu.Unlock()
	if f.auditErr != nil {
		return f.auditErr
	}
	f.audits = append(f.audits, ev)
	return nil
}
func (f *fakeStore) ListAudit(_ context.Context, filter domain.AuditFilter, page storage.ListPage) ([]domain.AuditEvent, string, error) {
	if f.onListAudit != nil {
		return f.onListAudit(filter, page)
	}
	f.auditMu.RLock()
	defer f.auditMu.RUnlock()
	return f.audits, "", nil
}

// --- change log / revisions ---

func (f *fakeStore) CurrentRevision(context.Context) (uint64, error)        { return f.revision, nil }
func (f *fakeStore) OldestRetainedRevision(context.Context) (uint64, error) { return 0, nil }
func (f *fakeStore) ListChangesSince(context.Context, uint64, int) ([]domain.ChangeLogEntry, error) {
	return nil, nil
}
func (f *fakeStore) PruneChangeLog(context.Context, time.Duration, int) (int, error) { return 0, nil }
func (f *fakeStore) SnapshotParameters(context.Context, []domain.NamespaceRef) ([]domain.Parameter, uint64, error) {
	return nil, f.revision, nil
}

// keyHasPrefix reports whether key falls under the segment-aware key prefix
// ("" matches all keys).
func keyHasPrefix(key, prefix string) bool {
	if prefix == "" {
		return true
	}
	return key == prefix || (len(key) > len(prefix) && key[:len(prefix)] == prefix && key[len(prefix)] == '/')
}
