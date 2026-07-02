package core

import (
	"bytes"
	"context"
	"time"

	"github.com/Suhaibinator/kms/internal/crypto"
	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/storage"
)

// fakeStore is an in-memory storage.Store for core unit tests. It implements
// the flows the service actually exercises (secrets, parameters, identities,
// policies, audit) with just enough fidelity to drive real crypto round-trips,
// plus hooks/fields to inject failures. It never sees plaintext.
type fakeStore struct {
	identitiesByName map[string]domain.Identity
	identitiesByHash map[string]domain.Identity
	policies         []domain.Policy
	secrets          map[string]*fakeSecret
	params           map[string]*fakeParam
	namespaces       []domain.Namespace
	keys             []domain.KeyMetadata
	audits           []domain.AuditEvent
	revision         uint64

	// error injection
	pingErr     error
	auditErr    error
	policiesErr error

	// optional behavior overrides
	onPoliciesForSubject func(subject string) ([]domain.Policy, error)
	onGetSecretVersion   func(path string, version uint64, label string) (storage.SecretRecord, storage.SecretVersionRecord, error)
	onListSecrets        func(prefix string, page storage.ListPage) ([]domain.Secret, string, error)
	onListParameters     func(prefix string, page storage.ListPage) ([]domain.Parameter, string, error)
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
		secrets:          map[string]*fakeSecret{},
		params:           map[string]*fakeParam{},
	}
}

// --- test helpers ---

func (f *fakeStore) addIdentity(name, kind, token string) domain.Identity {
	id := domain.Identity{ID: int64(len(f.identitiesByName) + 1), Name: name, Kind: kind, CreatedAt: time.Now()}
	f.identitiesByName[name] = id
	f.identitiesByHash[string(crypto.TokenHash(token))] = id
	return id
}

func (f *fakeStore) addPolicy(p domain.Policy) { f.policies = append(f.policies, p) }

func (f *fakeStore) auditsWithDecision(decision string) []domain.AuditEvent {
	var out []domain.AuditEvent
	for _, e := range f.audits {
		if e.Decision == decision {
			out = append(out, e)
		}
	}
	return out
}

func (f *fakeStore) lastAudit() (domain.AuditEvent, bool) {
	if len(f.audits) == 0 {
		return domain.AuditEvent{}, false
	}
	return f.audits[len(f.audits)-1], true
}

func (f *fakeStore) hasAudit(eventType, decision string) bool {
	for _, e := range f.audits {
		if e.EventType == eventType && e.Decision == decision {
			return true
		}
	}
	return false
}

func (f *fakeStore) tamperCiphertext(path string, version uint64) {
	sec := f.secrets[path]
	rec := sec.versions[version]
	rec.Ciphertext = bytes.Clone(rec.Ciphertext)
	rec.Ciphertext[0] ^= 0xff
	sec.versions[version] = rec
}

// expireVersion ages a version's expiry into the past, simulating a version
// that expired during its lifetime (PutSecret rejects writing a past expiry).
func (f *fakeStore) expireVersion(path string, version uint64) {
	sec := f.secrets[path]
	rec := sec.versions[version]
	rec.ExpiresAt = time.Now().Add(-time.Hour)
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
	rewrap func(rec storage.SecretVersionRecord) ([]byte, error)) (int, error) {
	count := 0
	for _, sec := range f.secrets {
		for v, rec := range sec.versions {
			if rec.State == domain.StateDestroyed {
				continue
			}
			nd, err := rewrap(rec)
			if err != nil {
				return 0, err
			}
			rec.EncryptedDEK = nd
			rec.KEKID = newKM.ID
			sec.versions[v] = rec
			count++
		}
	}
	f.keys = append(f.keys, newKM)
	return count, nil
}

// --- namespaces ---

func (f *fakeStore) CreateNamespace(_ context.Context, ns domain.Namespace) (domain.Namespace, error) {
	f.namespaces = append(f.namespaces, ns)
	return ns, nil
}
func (f *fakeStore) ListNamespaces(context.Context, storage.ListPage) ([]domain.Namespace, string, error) {
	return f.namespaces, "", nil
}

// --- parameters ---

func (f *fakeStore) PutParameter(_ context.Context, path, value, contentType, metadata, createdBy string) (uint64, uint64, error) {
	p := f.params[path]
	if p == nil {
		p = &fakeParam{}
		f.params[path] = p
	}
	version := uint64(len(p.history) + 1)
	f.revision++
	param := domain.Parameter{
		Path: path, Value: value, ContentType: contentType, Version: version,
		Metadata: metadata, CreatedBy: createdBy, CreatedAt: time.Now(),
		Labels: map[string]uint64{domain.LabelCurrent: version},
	}
	p.history = append(p.history, param)
	p.cur = param
	return version, f.revision, nil
}

func (f *fakeStore) GetParameter(_ context.Context, path string, version uint64, _ string) (domain.Parameter, error) {
	p := f.params[path]
	if p == nil {
		return domain.Parameter{}, domain.Errorf(domain.ErrNotFound, "parameter %s", path)
	}
	if version > 0 {
		if int(version) <= len(p.history) {
			return p.history[version-1], nil
		}
		return domain.Parameter{}, domain.Errorf(domain.ErrNotFound, "parameter %s v%d", path, version)
	}
	return p.cur, nil
}

func (f *fakeStore) GetParameterInfo(_ context.Context, path string) (domain.ParameterInfo, error) {
	p := f.params[path]
	if p == nil {
		return domain.ParameterInfo{}, domain.Errorf(domain.ErrNotFound, "parameter %s", path)
	}
	return domain.ParameterInfo{Path: path, ContentType: p.cur.ContentType, Metadata: p.cur.Metadata}, nil
}

func (f *fakeStore) ListParameters(_ context.Context, prefix string, page storage.ListPage) ([]domain.Parameter, string, error) {
	if f.onListParameters != nil {
		return f.onListParameters(prefix, page)
	}
	var out []domain.Parameter
	for _, p := range f.params {
		out = append(out, p.cur)
	}
	return out, "", nil
}

func (f *fakeStore) DeleteParameter(_ context.Context, path string) (uint64, error) {
	if _, ok := f.params[path]; !ok {
		return 0, domain.Errorf(domain.ErrNotFound, "parameter %s", path)
	}
	delete(f.params, path)
	f.revision++
	return f.revision, nil
}

// --- secrets ---

func (f *fakeStore) CreateSecretVersion(_ context.Context, p storage.CreateSecretParams) (uint64, uint64, error) {
	sec := f.secrets[p.Path]
	if sec == nil {
		sec = &fakeSecret{
			rec:      storage.SecretRecord{Path: p.Path, ClientBound: p.ClientBound, Labels: map[string]uint64{}},
			versions: map[uint64]storage.SecretVersionRecord{},
		}
		f.secrets[p.Path] = sec
	} else if sec.rec.ClientBound != p.ClientBound {
		return 0, 0, domain.Errorf(domain.ErrFailedPrecondition, "client_bound mismatch")
	}
	sec.next++
	version := sec.next
	payload, err := p.Encrypt(version)
	if err != nil {
		return 0, 0, err
	}
	sec.rec.ContentType = p.ContentType
	sec.rec.Metadata = p.Metadata
	sec.rec.UpdatedAt = time.Now()
	if p.AccessTokenHash != nil {
		sec.rec.AccessTokenHash = p.AccessTokenHash
		sec.rec.ClientBound = p.ClientBound
	}
	sec.versions[version] = storage.SecretVersionRecord{
		Version: version, Ciphertext: payload.Ciphertext, EncryptedDEK: payload.EncryptedDEK,
		KEKID: payload.KEKID, WrapMode: payload.WrapMode, ClientKeySalt: payload.ClientKeySalt,
		Algorithm: payload.Algorithm, Nonce: payload.Nonce, AAD: payload.AAD,
		State: domain.StateEnabled, CreatedBy: p.CreatedBy, CreatedAt: time.Now(), ExpiresAt: p.ExpiresAt,
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

func (f *fakeStore) GetSecretRecord(_ context.Context, path string) (storage.SecretRecord, error) {
	sec := f.secrets[path]
	if sec == nil {
		return storage.SecretRecord{}, domain.Errorf(domain.ErrNotFound, "secret %s", path)
	}
	return sec.rec, nil
}

func (f *fakeStore) GetSecretVersion(_ context.Context, path string, version uint64, label string) (storage.SecretRecord, storage.SecretVersionRecord, error) {
	if f.onGetSecretVersion != nil {
		return f.onGetSecretVersion(path, version, label)
	}
	sec := f.secrets[path]
	if sec == nil {
		return storage.SecretRecord{}, storage.SecretVersionRecord{}, domain.Errorf(domain.ErrNotFound, "secret %s", path)
	}
	v, ok := f.resolveVersion(sec, version, label)
	if !ok {
		return storage.SecretRecord{}, storage.SecretVersionRecord{}, domain.Errorf(domain.ErrNotFound, "secret %s version", path)
	}
	return sec.rec, sec.versions[v], nil
}

func (f *fakeStore) GetSecretInfo(_ context.Context, path string) (domain.Secret, error) {
	sec := f.secrets[path]
	if sec == nil {
		return domain.Secret{}, domain.Errorf(domain.ErrNotFound, "secret %s", path)
	}
	return domain.Secret{Path: path, ContentType: sec.rec.ContentType, ClientBound: sec.rec.ClientBound}, nil
}

func (f *fakeStore) ListSecrets(_ context.Context, prefix string, page storage.ListPage) ([]domain.Secret, string, error) {
	if f.onListSecrets != nil {
		return f.onListSecrets(prefix, page)
	}
	var out []domain.Secret
	for _, sec := range f.secrets {
		out = append(out, domain.Secret{Path: sec.rec.Path, ClientBound: sec.rec.ClientBound})
	}
	return out, "", nil
}

func (f *fakeStore) DeleteSecret(_ context.Context, path string) (uint64, error) {
	if _, ok := f.secrets[path]; !ok {
		return 0, domain.Errorf(domain.ErrNotFound, "secret %s", path)
	}
	delete(f.secrets, path)
	f.revision++
	return f.revision, nil
}

func (f *fakeStore) SetSecretVersionState(_ context.Context, path string, version uint64, state string) (uint64, error) {
	sec := f.secrets[path]
	if sec == nil {
		return 0, domain.Errorf(domain.ErrNotFound, "secret %s", path)
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
			return 0, domain.Errorf(domain.ErrNotFound, "secret %s v%d", path, version)
		}
		apply(version)
	}
	f.revision++
	return f.revision, nil
}

func (f *fakeStore) DestroySecretVersion(_ context.Context, path string, version uint64) (uint64, error) {
	sec := f.secrets[path]
	if sec == nil {
		return 0, domain.Errorf(domain.ErrNotFound, "secret %s", path)
	}
	rec, ok := sec.versions[version]
	if !ok {
		return 0, domain.Errorf(domain.ErrNotFound, "secret %s v%d", path, version)
	}
	rec.State = domain.StateDestroyed
	rec.Ciphertext, rec.EncryptedDEK, rec.Nonce = nil, nil, nil
	rec.DestroyedAt = time.Now()
	sec.versions[version] = rec
	f.revision++
	return f.revision, nil
}

func (f *fakeStore) PromoteSecretVersion(_ context.Context, path string, version uint64) (uint64, uint64, uint64, error) {
	sec := f.secrets[path]
	if sec == nil {
		return 0, 0, 0, domain.Errorf(domain.ErrNotFound, "secret %s", path)
	}
	rec, ok := sec.versions[version]
	if !ok {
		return 0, 0, 0, domain.Errorf(domain.ErrNotFound, "secret %s v%d", path, version)
	}
	if rec.State != domain.StateEnabled {
		return 0, 0, 0, domain.Errorf(domain.ErrFailedPrecondition, "version not enabled")
	}
	prev := sec.current
	if version != sec.current {
		sec.previous = sec.current
	}
	sec.current = version
	f.revision++
	return version, prev, f.revision, nil
}

func (f *fakeStore) UpdateSecretAccessTokenHash(_ context.Context, path string, hash []byte) error {
	sec := f.secrets[path]
	if sec == nil {
		return domain.Errorf(domain.ErrNotFound, "secret %s", path)
	}
	sec.rec.AccessTokenHash = hash
	return nil
}

// --- identities ---

func (f *fakeStore) CreateIdentity(_ context.Context, name, kind string, tokenHash []byte) (domain.Identity, error) {
	if _, ok := f.identitiesByName[name]; ok {
		return domain.Identity{}, domain.Errorf(domain.ErrAlreadyExists, "identity %s", name)
	}
	id := domain.Identity{ID: int64(len(f.identitiesByName) + 1), Name: name, Kind: kind, CreatedAt: time.Now()}
	f.identitiesByName[name] = id
	f.identitiesByHash[string(tokenHash)] = id
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
	return id, nil
}

func (f *fakeStore) ListIdentities(context.Context, storage.ListPage) ([]domain.Identity, string, error) {
	var out []domain.Identity
	for _, id := range f.identitiesByName {
		out = append(out, id)
	}
	return out, "", nil
}

func (f *fakeStore) SetIdentityDisabled(_ context.Context, name string, disabled bool) error {
	id, ok := f.identitiesByName[name]
	if !ok {
		return domain.Errorf(domain.ErrNotFound, "identity %s", name)
	}
	id.Disabled = disabled
	f.identitiesByName[name] = id
	// Keep the hash-indexed copy (read by GetIdentityByTokenHash) in sync, as
	// a single-row store would.
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
	f.identitiesByHash[string(tokenHash)] = id
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
	if f.auditErr != nil {
		return f.auditErr
	}
	f.audits = append(f.audits, ev)
	return nil
}
func (f *fakeStore) ListAudit(_ context.Context, _ domain.AuditFilter, _ storage.ListPage) ([]domain.AuditEvent, string, error) {
	return f.audits, "", nil
}

// --- change log / revisions ---

func (f *fakeStore) CurrentRevision(context.Context) (uint64, error)        { return f.revision, nil }
func (f *fakeStore) OldestRetainedRevision(context.Context) (uint64, error) { return 0, nil }
func (f *fakeStore) ListChangesSince(context.Context, uint64, int) ([]domain.ChangeLogEntry, error) {
	return nil, nil
}
func (f *fakeStore) PruneChangeLog(context.Context, time.Duration, int) (int, error) { return 0, nil }
func (f *fakeStore) SnapshotParameters(context.Context, []string) ([]domain.Parameter, uint64, error) {
	return nil, f.revision, nil
}
