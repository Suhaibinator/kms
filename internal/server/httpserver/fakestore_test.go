package httpserver

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/pathutil"
	"github.com/Suhaibinator/kms/internal/storage"
)

// fakeStore is an in-memory storage.Store used to drive a real core.Service in
// the HTTP handler tests. It implements just enough behavior (versions, labels,
// revisions, filtering) to exercise every endpoint; watch replay and KEK
// rotation are stubbed.
type fakeStore struct {
	mu         sync.Mutex
	revision   uint64
	pingErr    error
	params     map[string]*fakeParam
	secrets    map[string]*fakeSecret
	namespaces []domain.Namespace
	policies   map[string]domain.Policy
	identities map[string]*domain.Identity // by name
	byToken    map[string]string           // token-hash hex -> identity name
	audit      []domain.AuditEvent
	keys       []domain.KeyMetadata
	nextID     int64
}

type fakeParam struct {
	contentType string
	metadata    string
	createdAt   time.Time
	updatedAt   time.Time
	next        uint64
	labels      map[string]uint64
	versions    map[uint64]domain.Parameter
}

type fakeSecret struct {
	rec      storage.SecretRecord
	next     uint64
	versions map[uint64]storage.SecretVersionRecord
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		params:     map[string]*fakeParam{},
		secrets:    map[string]*fakeSecret{},
		policies:   map[string]domain.Policy{},
		identities: map[string]*domain.Identity{},
		byToken:    map[string]string{},
	}
}

func hexKey(b []byte) string {
	const hexdigits = "0123456789abcdef"
	var sb strings.Builder
	for _, c := range b {
		sb.WriteByte(hexdigits[c>>4])
		sb.WriteByte(hexdigits[c&0x0f])
	}
	return sb.String()
}

func (s *fakeStore) bump() uint64 { s.revision++; return s.revision }

// --- lifecycle -------------------------------------------------------------

func (s *fakeStore) Ping(context.Context) error { return s.pingErr }
func (s *fakeStore) Close() error               { return nil }
func (s *fakeStore) Backup(context.Context, string) error {
	return nil
}

// --- key metadata ----------------------------------------------------------

func (s *fakeStore) InsertKeyMetadata(_ context.Context, km domain.KeyMetadata) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.keys = append(s.keys, km)
	return nil
}

func (s *fakeStore) GetKeyMetadata(_ context.Context, id string) (domain.KeyMetadata, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, k := range s.keys {
		if k.ID == id {
			return k, nil
		}
	}
	return domain.KeyMetadata{}, domain.ErrNotFound
}

func (s *fakeStore) ListKeyMetadata(context.Context) ([]domain.KeyMetadata, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]domain.KeyMetadata, len(s.keys))
	copy(out, s.keys)
	return out, nil
}

func (s *fakeStore) ActiveKeyMetadata(context.Context) (domain.KeyMetadata, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, k := range s.keys {
		if k.State == domain.KeyStateActive {
			return k, nil
		}
	}
	return domain.KeyMetadata{}, domain.ErrNotFound
}

func (s *fakeStore) SetKeyState(context.Context, string, string) error { return nil }

func (s *fakeStore) RotateKEK(context.Context, domain.KeyMetadata, func(storage.SecretVersionRecord) ([]byte, error)) (int, error) {
	return 0, nil
}

// --- namespaces ------------------------------------------------------------

func (s *fakeStore) CreateNamespace(_ context.Context, ns domain.Namespace) (domain.Namespace, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.namespaces {
		if existing.Path == ns.Path {
			return domain.Namespace{}, domain.Errorf(domain.ErrAlreadyExists, "namespace %s", ns.Path)
		}
	}
	s.namespaces = append(s.namespaces, ns)
	return ns, nil
}

func (s *fakeStore) ListNamespaces(_ context.Context, _ storage.ListPage) ([]domain.Namespace, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]domain.Namespace, len(s.namespaces))
	copy(out, s.namespaces)
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, "", nil
}

// --- parameters ------------------------------------------------------------

func (s *fakeStore) PutParameter(_ context.Context, path, value, contentType, metadata, createdBy string) (uint64, uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.params[path]
	now := time.Now().UTC()
	if p == nil {
		p = &fakeParam{contentType: contentType, metadata: metadata, createdAt: now, labels: map[string]uint64{}, versions: map[uint64]domain.Parameter{}}
		s.params[path] = p
	}
	p.next++
	v := p.next
	p.updatedAt = now
	p.versions[v] = domain.Parameter{
		Path: path, Value: value, ContentType: contentType, Version: v,
		Metadata: metadata, CreatedBy: createdBy, CreatedAt: now,
	}
	if old, ok := p.labels[domain.LabelCurrent]; ok {
		p.labels[domain.LabelPrevious] = old
	}
	p.labels[domain.LabelCurrent] = v
	return v, s.bump(), nil
}

func (s *fakeStore) GetParameter(_ context.Context, path string, version uint64, label string) (domain.Parameter, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.params[path]
	if p == nil {
		return domain.Parameter{}, domain.Errorf(domain.ErrNotFound, "parameter %s", path)
	}
	v := version
	if v == 0 {
		if label == "" {
			label = domain.LabelCurrent
		}
		lv, ok := p.labels[label]
		if !ok {
			return domain.Parameter{}, domain.Errorf(domain.ErrNotFound, "parameter %s label %s", path, label)
		}
		v = lv
	}
	par, ok := p.versions[v]
	if !ok {
		return domain.Parameter{}, domain.Errorf(domain.ErrNotFound, "parameter %s v%d", path, v)
	}
	par.Labels = cloneLabels(p.labels)
	return par, nil
}

func (s *fakeStore) GetParameterInfo(_ context.Context, path string) (domain.ParameterInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.params[path]
	if p == nil {
		return domain.ParameterInfo{}, domain.Errorf(domain.ErrNotFound, "parameter %s", path)
	}
	info := domain.ParameterInfo{
		Path: path, ContentType: p.contentType, Metadata: p.metadata,
		CreatedAt: p.createdAt, UpdatedAt: p.updatedAt, Labels: cloneLabels(p.labels),
	}
	for v := uint64(1); v <= p.next; v++ {
		par, ok := p.versions[v]
		if !ok {
			continue
		}
		info.Versions = append(info.Versions, domain.ParameterVersionInfo{
			Version: v, ContentType: par.ContentType, State: domain.StateEnabled,
			CreatedBy: par.CreatedBy, CreatedAt: par.CreatedAt, Metadata: par.Metadata,
		})
	}
	return info, nil
}

func (s *fakeStore) ListParameters(_ context.Context, prefix string, _ storage.ListPage) ([]domain.Parameter, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []domain.Parameter
	for path, p := range s.params {
		if !pathutil.HasPrefix(path, prefix) {
			continue
		}
		cur := p.labels[domain.LabelCurrent]
		par := p.versions[cur]
		par.Labels = cloneLabels(p.labels)
		out = append(out, par)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, "", nil
}

func (s *fakeStore) DeleteParameter(_ context.Context, path string) (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.params[path]; !ok {
		return 0, domain.Errorf(domain.ErrNotFound, "parameter %s", path)
	}
	delete(s.params, path)
	return s.bump(), nil
}

// --- secrets ---------------------------------------------------------------

func (s *fakeStore) CreateSecretVersion(_ context.Context, p storage.CreateSecretParams) (uint64, uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sec := s.secrets[p.Path]
	now := time.Now().UTC()
	if sec == nil {
		s.nextID++
		sec = &fakeSecret{
			rec: storage.SecretRecord{
				ID: s.nextID, Path: p.Path, ClientBound: p.ClientBound,
				ContentType: p.ContentType, Metadata: p.Metadata,
				CreatedAt: now, Labels: map[string]uint64{},
			},
			versions: map[uint64]storage.SecretVersionRecord{},
		}
		s.secrets[p.Path] = sec
	}
	sec.next++
	v := sec.next
	// Encrypt is called with the version number, exactly as the real store does.
	payload, err := p.Encrypt(v)
	if err != nil {
		return 0, 0, err
	}
	sec.versions[v] = storage.SecretVersionRecord{
		ID: int64(v), SecretID: sec.rec.ID, Version: v,
		Ciphertext: payload.Ciphertext, EncryptedDEK: payload.EncryptedDEK,
		KEKID: payload.KEKID, WrapMode: payload.WrapMode, ClientKeySalt: payload.ClientKeySalt,
		Algorithm: payload.Algorithm, Nonce: payload.Nonce, AAD: payload.AAD,
		State: domain.StateEnabled, CreatedBy: p.CreatedBy, CreatedAt: now, ExpiresAt: p.ExpiresAt,
		Metadata: p.Metadata,
	}
	sec.rec.ContentType = p.ContentType
	sec.rec.Metadata = p.Metadata
	sec.rec.UpdatedAt = now
	if p.AccessTokenHash != nil {
		sec.rec.AccessTokenHash = p.AccessTokenHash
	}
	if old, ok := sec.rec.Labels[domain.LabelCurrent]; ok {
		sec.rec.Labels[domain.LabelPrevious] = old
	}
	sec.rec.Labels[domain.LabelCurrent] = v
	return v, s.bump(), nil
}

func (s *fakeStore) GetSecretRecord(_ context.Context, path string) (storage.SecretRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sec := s.secrets[path]
	if sec == nil {
		return storage.SecretRecord{}, domain.Errorf(domain.ErrNotFound, "secret %s", path)
	}
	return cloneSecretRecord(sec.rec), nil
}

func (s *fakeStore) GetSecretVersion(_ context.Context, path string, version uint64, label string) (storage.SecretRecord, storage.SecretVersionRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sec := s.secrets[path]
	if sec == nil {
		return storage.SecretRecord{}, storage.SecretVersionRecord{}, domain.Errorf(domain.ErrNotFound, "secret %s", path)
	}
	v := version
	if v == 0 {
		if label == "" {
			label = domain.LabelCurrent
		}
		lv, ok := sec.rec.Labels[label]
		if !ok {
			return storage.SecretRecord{}, storage.SecretVersionRecord{}, domain.Errorf(domain.ErrNotFound, "secret %s label %s", path, label)
		}
		v = lv
	}
	ver, ok := sec.versions[v]
	if !ok {
		return storage.SecretRecord{}, storage.SecretVersionRecord{}, domain.Errorf(domain.ErrNotFound, "secret %s v%d", path, v)
	}
	return cloneSecretRecord(sec.rec), ver, nil
}

func (s *fakeStore) GetSecretInfo(_ context.Context, path string) (domain.Secret, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sec := s.secrets[path]
	if sec == nil {
		return domain.Secret{}, domain.Errorf(domain.ErrNotFound, "secret %s", path)
	}
	return s.secretMeta(sec), nil
}

func (s *fakeStore) ListSecrets(_ context.Context, prefix string, _ storage.ListPage) ([]domain.Secret, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []domain.Secret
	for path, sec := range s.secrets {
		if !pathutil.HasPrefix(path, prefix) {
			continue
		}
		out = append(out, s.secretMeta(sec))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, "", nil
}

func (s *fakeStore) secretMeta(sec *fakeSecret) domain.Secret {
	meta := domain.Secret{
		Path: sec.rec.Path, ContentType: sec.rec.ContentType, ClientBound: sec.rec.ClientBound,
		HasAccessToken: len(sec.rec.AccessTokenHash) > 0, Metadata: sec.rec.Metadata,
		CreatedAt: sec.rec.CreatedAt, UpdatedAt: sec.rec.UpdatedAt, Labels: cloneLabels(sec.rec.Labels),
	}
	for v := uint64(1); v <= sec.next; v++ {
		ver, ok := sec.versions[v]
		if !ok {
			continue
		}
		meta.Versions = append(meta.Versions, domain.SecretVersionInfo{
			Version: v, State: ver.State, CreatedBy: ver.CreatedBy, CreatedAt: ver.CreatedAt,
			DestroyedAt: ver.DestroyedAt, ExpiresAt: ver.ExpiresAt, Metadata: ver.Metadata,
		})
	}
	return meta
}

func (s *fakeStore) DeleteSecret(_ context.Context, path string) (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.secrets[path]; !ok {
		return 0, domain.Errorf(domain.ErrNotFound, "secret %s", path)
	}
	delete(s.secrets, path)
	return s.bump(), nil
}

func (s *fakeStore) SetSecretVersionState(_ context.Context, path string, version uint64, state string) (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sec := s.secrets[path]
	if sec == nil {
		return 0, domain.Errorf(domain.ErrNotFound, "secret %s", path)
	}
	apply := func(v uint64) {
		ver := sec.versions[v]
		if ver.State != domain.StateDestroyed {
			ver.State = state
			sec.versions[v] = ver
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
	return s.bump(), nil
}

func (s *fakeStore) DestroySecretVersion(_ context.Context, path string, version uint64) (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sec := s.secrets[path]
	if sec == nil {
		return 0, domain.Errorf(domain.ErrNotFound, "secret %s", path)
	}
	ver, ok := sec.versions[version]
	if !ok {
		return 0, domain.Errorf(domain.ErrNotFound, "secret %s v%d", path, version)
	}
	ver.State = domain.StateDestroyed
	ver.Ciphertext, ver.EncryptedDEK, ver.Nonce = nil, nil, nil
	ver.DestroyedAt = time.Now().UTC()
	sec.versions[version] = ver
	return s.bump(), nil
}

func (s *fakeStore) PromoteSecretVersion(_ context.Context, path string, version uint64) (uint64, uint64, uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sec := s.secrets[path]
	if sec == nil {
		return 0, 0, 0, domain.Errorf(domain.ErrNotFound, "secret %s", path)
	}
	if _, ok := sec.versions[version]; !ok {
		return 0, 0, 0, domain.Errorf(domain.ErrNotFound, "secret %s v%d", path, version)
	}
	prev := sec.rec.Labels[domain.LabelCurrent]
	if prev != version && prev != 0 {
		sec.rec.Labels[domain.LabelPrevious] = prev
	}
	sec.rec.Labels[domain.LabelCurrent] = version
	return version, sec.rec.Labels[domain.LabelPrevious], s.bump(), nil
}

func (s *fakeStore) UpdateSecretAccessTokenHash(_ context.Context, path string, hash []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sec := s.secrets[path]
	if sec == nil {
		return domain.Errorf(domain.ErrNotFound, "secret %s", path)
	}
	sec.rec.AccessTokenHash = hash
	return nil
}

// --- identities ------------------------------------------------------------

func (s *fakeStore) CreateIdentity(_ context.Context, name, kind string, tokenHash []byte) (domain.Identity, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.identities[name]; ok {
		return domain.Identity{}, domain.Errorf(domain.ErrAlreadyExists, "identity %s", name)
	}
	s.nextID++
	id := &domain.Identity{ID: s.nextID, Name: name, Kind: kind, CreatedAt: time.Now().UTC()}
	s.identities[name] = id
	s.byToken[hexKey(tokenHash)] = name
	return *id, nil
}

func (s *fakeStore) GetIdentityByTokenHash(_ context.Context, tokenHash []byte) (domain.Identity, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	name, ok := s.byToken[hexKey(tokenHash)]
	if !ok {
		return domain.Identity{}, domain.Errorf(domain.ErrNotFound, "identity")
	}
	return *s.identities[name], nil
}

func (s *fakeStore) GetIdentityByName(_ context.Context, name string) (domain.Identity, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, ok := s.identities[name]
	if !ok {
		return domain.Identity{}, domain.Errorf(domain.ErrNotFound, "identity %s", name)
	}
	return *id, nil
}

func (s *fakeStore) ListIdentities(_ context.Context, _ storage.ListPage) ([]domain.Identity, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]domain.Identity, 0, len(s.identities))
	for _, id := range s.identities {
		out = append(out, *id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, "", nil
}

func (s *fakeStore) SetIdentityDisabled(_ context.Context, name string, disabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, ok := s.identities[name]
	if !ok {
		return domain.Errorf(domain.ErrNotFound, "identity %s", name)
	}
	id.Disabled = disabled
	return nil
}

func (s *fakeStore) UpdateIdentityTokenHash(_ context.Context, name string, tokenHash []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.identities[name]; !ok {
		return domain.Errorf(domain.ErrNotFound, "identity %s", name)
	}
	for h, n := range s.byToken {
		if n == name {
			delete(s.byToken, h)
		}
	}
	s.byToken[hexKey(tokenHash)] = name
	return nil
}

// --- policies --------------------------------------------------------------

func (s *fakeStore) CreatePolicy(_ context.Context, p domain.Policy) (domain.Policy, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.policies[p.Name]; ok {
		return domain.Policy{}, domain.Errorf(domain.ErrAlreadyExists, "policy %s", p.Name)
	}
	s.policies[p.Name] = p
	return p, nil
}

func (s *fakeStore) UpdatePolicy(_ context.Context, p domain.Policy) (domain.Policy, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.policies[p.Name]; !ok {
		return domain.Policy{}, domain.Errorf(domain.ErrNotFound, "policy %s", p.Name)
	}
	s.policies[p.Name] = p
	return p, nil
}

func (s *fakeStore) DeletePolicy(_ context.Context, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.policies[name]; !ok {
		return domain.Errorf(domain.ErrNotFound, "policy %s", name)
	}
	delete(s.policies, name)
	return nil
}

func (s *fakeStore) ListPolicies(_ context.Context, _ storage.ListPage) ([]domain.Policy, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]domain.Policy, 0, len(s.policies))
	for _, p := range s.policies {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, "", nil
}

func (s *fakeStore) PoliciesForSubject(_ context.Context, subject string) ([]domain.Policy, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []domain.Policy
	for _, p := range s.policies {
		if p.Subject == subject || p.Subject == "*" {
			out = append(out, p)
		}
	}
	return out, nil
}

// --- audit -----------------------------------------------------------------

func (s *fakeStore) AppendAudit(_ context.Context, ev domain.AuditEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	ev.ID = s.nextID
	s.audit = append(s.audit, ev)
	return nil
}

func (s *fakeStore) ListAudit(_ context.Context, f domain.AuditFilter, _ storage.ListPage) ([]domain.AuditEvent, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []domain.AuditEvent
	for _, ev := range s.audit {
		if f.PathPrefix != "" && !strings.HasPrefix(ev.ResourcePath, f.PathPrefix) {
			continue
		}
		if f.ActorIdentity != "" && ev.ActorIdentity != f.ActorIdentity {
			continue
		}
		if f.EventType != "" && ev.EventType != f.EventType {
			continue
		}
		if !f.From.IsZero() && ev.CreatedAt.Before(f.From) {
			continue
		}
		if !f.To.IsZero() && ev.CreatedAt.After(f.To) {
			continue
		}
		out = append(out, ev)
	}
	return out, "", nil
}

// --- change log / revisions ------------------------------------------------

func (s *fakeStore) CurrentRevision(context.Context) (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.revision, nil
}

func (s *fakeStore) OldestRetainedRevision(context.Context) (uint64, error) { return 0, nil }
func (s *fakeStore) ListChangesSince(context.Context, uint64, int) ([]domain.ChangeLogEntry, error) {
	return nil, nil
}
func (s *fakeStore) PruneChangeLog(context.Context, time.Duration, int) (int, error) { return 0, nil }
func (s *fakeStore) SnapshotParameters(context.Context, []string) ([]domain.Parameter, uint64, error) {
	return nil, s.revision, nil
}

// --- clone helpers ---------------------------------------------------------

func cloneLabels(m map[string]uint64) map[string]uint64 {
	out := make(map[string]uint64, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func cloneSecretRecord(r storage.SecretRecord) storage.SecretRecord {
	r.Labels = cloneLabels(r.Labels)
	if r.AccessTokenHash != nil {
		h := make([]byte, len(r.AccessTokenHash))
		copy(h, r.AccessTokenHash)
		r.AccessTokenHash = h
	}
	return r
}

// compile-time check that fakeStore satisfies the Store contract.
var _ storage.Store = (*fakeStore)(nil)
