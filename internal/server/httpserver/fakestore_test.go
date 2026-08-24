package httpserver

import (
	"context"
	"maps"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/storage"
)

// fakeStore is an in-memory storage.Store used to drive a real core.Service in
// the HTTP handler tests. Resources are addressed by domain.Ref (namespace +
// key). It implements just enough behavior (versions, labels, revisions,
// namespace gating, client certs) to exercise every endpoint; watch replay and
// KEK rotation are stubbed.
type fakeStore struct {
	mu         sync.Mutex
	revision   uint64
	pingErr    error
	params     map[string]*fakeParam        // by refKey
	secrets    map[string]*fakeSecret       // by refKey
	namespaces map[string]*domain.Namespace // by nsKey
	nsOrder    []string                     // nsKeys in insertion order
	policies   map[string]domain.Policy
	identities map[string]*fakeIdentity // by name
	byToken    map[string]string        // token-hash hex -> identity name
	certs      map[string]string        // serial -> identity name
	caKeys     []storage.CAKeyRecord
	audit      []domain.AuditEvent
	keys       []domain.KeyMetadata
	nextID     int64
}

type fakeParam struct {
	ref         domain.Ref
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

type fakeIdentity struct {
	id    domain.Identity
	certs []domain.IdentityCert
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		params:     map[string]*fakeParam{},
		secrets:    map[string]*fakeSecret{},
		namespaces: map[string]*domain.Namespace{},
		policies:   map[string]domain.Policy{},
		identities: map[string]*fakeIdentity{},
		byToken:    map[string]string{},
		certs:      map[string]string{},
	}
}

func refKey(r domain.Ref) string         { return r.NS.Env + "\x00" + r.NS.App + "\x00" + r.Key }
func nsKey(n domain.NamespaceRef) string { return n.Env + "\x00" + n.App }

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

// requireNS reports ErrNotFound when the namespace has not been created, matching
// the real store's foreign-key discipline (caller holds mu).
func (s *fakeStore) requireNS(ns domain.NamespaceRef) error {
	if _, ok := s.namespaces[nsKey(ns)]; !ok {
		return domain.Errorf(domain.ErrNotFound, "namespace %s", ns)
	}
	return nil
}

// --- lifecycle -------------------------------------------------------------

func (s *fakeStore) Ping(context.Context) error           { return s.pingErr }
func (s *fakeStore) Close() error                         { return nil }
func (s *fakeStore) Backup(context.Context, string) error { return nil }

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

func (s *fakeStore) RotateKEK(context.Context, domain.KeyMetadata,
	func(storage.SecretVersionRecord) ([]byte, error),
	func(storage.CAKeyRecord) ([]byte, error)) (int, int, error) {
	return 0, 0, nil
}

// --- namespaces ------------------------------------------------------------

func (s *fakeStore) CreateNamespace(_ context.Context, ns domain.Namespace) (domain.Namespace, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := nsKey(ns.NamespaceRef)
	if _, ok := s.namespaces[k]; ok {
		return domain.Namespace{}, domain.Errorf(domain.ErrAlreadyExists, "namespace %s", ns.NamespaceRef)
	}
	cp := ns
	s.nextID++
	cp.ID = s.nextID
	s.namespaces[k] = &cp
	s.nsOrder = append(s.nsOrder, k)
	return cp, nil
}

func (s *fakeStore) GetNamespace(_ context.Context, ref domain.NamespaceRef) (domain.Namespace, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ns, ok := s.namespaces[nsKey(ref)]
	if !ok {
		return domain.Namespace{}, domain.Errorf(domain.ErrNotFound, "namespace %s", ref)
	}
	return s.withCounts(*ns), nil
}

func (s *fakeStore) UpdateNamespace(_ context.Context, ref domain.NamespaceRef, description string, methods []domain.AuthMethod) (domain.Namespace, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ns, ok := s.namespaces[nsKey(ref)]
	if !ok {
		return domain.Namespace{}, domain.Errorf(domain.ErrNotFound, "namespace %s", ref)
	}
	ns.Description = description
	ns.AllowedAuthMethods = methods
	return s.withCounts(*ns), nil
}

func (s *fakeStore) DeleteNamespace(_ context.Context, ref domain.NamespaceRef) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := nsKey(ref)
	if _, ok := s.namespaces[k]; !ok {
		return domain.Errorf(domain.ErrNotFound, "namespace %s", ref)
	}
	for _, p := range s.params {
		if nsKey(p.ref.NS) == k {
			return domain.Errorf(domain.ErrFailedPrecondition, "namespace %s is not empty", ref)
		}
	}
	for _, sec := range s.secrets {
		if nsKey(sec.rec.Ref.NS) == k {
			return domain.Errorf(domain.ErrFailedPrecondition, "namespace %s is not empty", ref)
		}
	}
	for _, fi := range s.identities {
		if fi.id.Namespace != nil && nsKey(*fi.id.Namespace) == k {
			return domain.Errorf(domain.ErrFailedPrecondition, "namespace %s has bound identities", ref)
		}
	}
	delete(s.namespaces, k)
	for i, o := range s.nsOrder {
		if o == k {
			s.nsOrder = append(s.nsOrder[:i], s.nsOrder[i+1:]...)
			break
		}
	}
	return nil
}

func (s *fakeStore) ListNamespaces(_ context.Context, _ storage.ListPage) ([]domain.Namespace, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]domain.Namespace, 0, len(s.namespaces))
	for _, ns := range s.namespaces {
		out = append(out, s.withCounts(*ns))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Env != out[j].Env {
			return out[i].Env < out[j].Env
		}
		return out[i].App < out[j].App
	})
	return out, "", nil
}

// withCounts fills ParameterCount/SecretCount from the current maps (caller
// holds mu).
func (s *fakeStore) withCounts(ns domain.Namespace) domain.Namespace {
	k := nsKey(ns.NamespaceRef)
	for _, p := range s.params {
		if nsKey(p.ref.NS) == k {
			ns.ParameterCount++
		}
	}
	for _, sec := range s.secrets {
		if nsKey(sec.rec.Ref.NS) == k {
			ns.SecretCount++
		}
	}
	return ns
}

// --- parameters ------------------------------------------------------------

func (s *fakeStore) PutParameter(_ context.Context, ref domain.Ref, value, contentType, metadata, createdBy string) (uint64, uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireNS(ref.NS); err != nil {
		return 0, 0, err
	}
	k := refKey(ref)
	p := s.params[k]
	now := time.Now().UTC()
	if p == nil {
		p = &fakeParam{ref: ref, contentType: contentType, metadata: metadata, createdAt: now, labels: map[string]uint64{}, versions: map[uint64]domain.Parameter{}}
		s.params[k] = p
	}
	p.next++
	v := p.next
	p.updatedAt = now
	p.versions[v] = domain.Parameter{
		Ref: ref, Value: value, ContentType: contentType, Version: v,
		Metadata: metadata, CreatedBy: createdBy, CreatedAt: now,
	}
	if old, ok := p.labels[domain.LabelCurrent]; ok {
		p.labels[domain.LabelPrevious] = old
	}
	p.labels[domain.LabelCurrent] = v
	return v, s.bump(), nil
}

func (s *fakeStore) GetParameter(_ context.Context, ref domain.Ref, version uint64, label string) (domain.Parameter, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.params[refKey(ref)]
	if p == nil {
		return domain.Parameter{}, domain.Errorf(domain.ErrNotFound, "parameter %s", ref)
	}
	v := version
	if v == 0 {
		if label == "" {
			label = domain.LabelCurrent
		}
		lv, ok := p.labels[label]
		if !ok {
			return domain.Parameter{}, domain.Errorf(domain.ErrNotFound, "parameter %s label %s", ref, label)
		}
		v = lv
	}
	par, ok := p.versions[v]
	if !ok {
		return domain.Parameter{}, domain.Errorf(domain.ErrNotFound, "parameter %s v%d", ref, v)
	}
	par.Labels = cloneLabels(p.labels)
	return par, nil
}

func (s *fakeStore) GetParameterInfo(_ context.Context, ref domain.Ref) (domain.ParameterInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.params[refKey(ref)]
	if p == nil {
		return domain.ParameterInfo{}, domain.Errorf(domain.ErrNotFound, "parameter %s", ref)
	}
	info := domain.ParameterInfo{
		Ref: ref, ContentType: p.contentType, Metadata: p.metadata,
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

func (s *fakeStore) ListParameters(_ context.Context, ns domain.NamespaceRef, keyPrefix string, _ storage.ListPage) ([]domain.Parameter, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []domain.Parameter
	for _, p := range s.params {
		if p.ref.NS != ns || !keyHasPrefix(p.ref.Key, keyPrefix) {
			continue
		}
		cur := p.labels[domain.LabelCurrent]
		par := p.versions[cur]
		par.Labels = cloneLabels(p.labels)
		out = append(out, par)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Ref.Key < out[j].Ref.Key })
	return out, "", nil
}

func (s *fakeStore) DeleteParameter(_ context.Context, ref domain.Ref) (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := refKey(ref)
	if _, ok := s.params[k]; !ok {
		return 0, domain.Errorf(domain.ErrNotFound, "parameter %s", ref)
	}
	delete(s.params, k)
	return s.bump(), nil
}

// --- secrets ---------------------------------------------------------------

func (s *fakeStore) CreateSecretVersion(_ context.Context, p storage.CreateSecretParams) (uint64, uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireNS(p.Ref.NS); err != nil {
		return 0, 0, err
	}
	k := refKey(p.Ref)
	sec := s.secrets[k]
	now := time.Now().UTC()
	if sec == nil {
		s.nextID++
		sec = &fakeSecret{
			rec: storage.SecretRecord{
				ID: s.nextID, Ref: p.Ref, ClientBound: p.ClientBound,
				ContentType: p.ContentType, Metadata: p.Metadata,
				CreatedAt: now, Labels: map[string]uint64{},
			},
			versions: map[uint64]storage.SecretVersionRecord{},
		}
		s.secrets[k] = sec
	}
	sec.next++
	v := sec.next
	payload, err := p.Encrypt(v)
	if err != nil {
		return 0, 0, err
	}
	if p.AccessTokenHash != nil {
		sec.rec.AccessTokenHash = p.AccessTokenHash
	}
	sec.versions[v] = storage.SecretVersionRecord{
		ID: int64(v), SecretID: sec.rec.ID, Version: v,
		ContentType: p.ContentType, ClientBound: p.ClientBound,
		HasAccessToken: len(sec.rec.AccessTokenHash) > 0,
		Ciphertext:     payload.Ciphertext, EncryptedDEK: payload.EncryptedDEK,
		KEKID: payload.KEKID, WrapMode: payload.WrapMode, ClientKeySalt: payload.ClientKeySalt,
		Algorithm: payload.Algorithm, Nonce: payload.Nonce, AAD: payload.AAD,
		State: domain.StateEnabled, CreatedBy: p.CreatedBy, CreatedAt: now, ExpiresAt: p.ExpiresAt,
		Metadata: p.Metadata,
	}
	sec.rec.ContentType = p.ContentType
	sec.rec.Metadata = p.Metadata
	sec.rec.UpdatedAt = now
	if old, ok := sec.rec.Labels[domain.LabelCurrent]; ok {
		sec.rec.Labels[domain.LabelPrevious] = old
	}
	sec.rec.Labels[domain.LabelCurrent] = v
	return v, s.bump(), nil
}

func (s *fakeStore) GetSecretRecord(_ context.Context, ref domain.Ref) (storage.SecretRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sec := s.secrets[refKey(ref)]
	if sec == nil {
		return storage.SecretRecord{}, domain.Errorf(domain.ErrNotFound, "secret %s", ref)
	}
	return cloneSecretRecord(sec.rec), nil
}

func (s *fakeStore) GetSecretVersion(_ context.Context, ref domain.Ref, version uint64, label string) (storage.SecretRecord, storage.SecretVersionRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sec := s.secrets[refKey(ref)]
	if sec == nil {
		return storage.SecretRecord{}, storage.SecretVersionRecord{}, domain.Errorf(domain.ErrNotFound, "secret %s", ref)
	}
	v := version
	if v == 0 {
		if label == "" {
			label = domain.LabelCurrent
		}
		lv, ok := sec.rec.Labels[label]
		if !ok {
			return storage.SecretRecord{}, storage.SecretVersionRecord{}, domain.Errorf(domain.ErrNotFound, "secret %s label %s", ref, label)
		}
		v = lv
	}
	ver, ok := sec.versions[v]
	if !ok {
		return storage.SecretRecord{}, storage.SecretVersionRecord{}, domain.Errorf(domain.ErrNotFound, "secret %s v%d", ref, v)
	}
	return cloneSecretRecord(sec.rec), ver, nil
}

func (s *fakeStore) GetSecretInfo(_ context.Context, ref domain.Ref) (domain.Secret, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sec := s.secrets[refKey(ref)]
	if sec == nil {
		return domain.Secret{}, domain.Errorf(domain.ErrNotFound, "secret %s", ref)
	}
	return s.secretMeta(sec), nil
}

func (s *fakeStore) ListSecrets(_ context.Context, ns domain.NamespaceRef, keyPrefix string, _ storage.ListPage) ([]domain.Secret, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []domain.Secret
	for _, sec := range s.secrets {
		if sec.rec.Ref.NS != ns || !keyHasPrefix(sec.rec.Ref.Key, keyPrefix) {
			continue
		}
		out = append(out, s.secretMeta(sec))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Ref.Key < out[j].Ref.Key })
	return out, "", nil
}

func (s *fakeStore) secretMeta(sec *fakeSecret) domain.Secret {
	meta := domain.Secret{
		Ref: sec.rec.Ref, ContentType: sec.rec.ContentType, ClientBound: sec.rec.ClientBound,
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

func (s *fakeStore) DeleteSecret(_ context.Context, ref domain.Ref) (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := refKey(ref)
	if _, ok := s.secrets[k]; !ok {
		return 0, domain.Errorf(domain.ErrNotFound, "secret %s", ref)
	}
	delete(s.secrets, k)
	return s.bump(), nil
}

func (s *fakeStore) SetSecretVersionState(_ context.Context, ref domain.Ref, version uint64, state string) (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sec := s.secrets[refKey(ref)]
	if sec == nil {
		return 0, domain.Errorf(domain.ErrNotFound, "secret %s", ref)
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
			return 0, domain.Errorf(domain.ErrNotFound, "secret %s v%d", ref, version)
		}
		apply(version)
	}
	return s.bump(), nil
}

func (s *fakeStore) DestroySecretVersion(_ context.Context, ref domain.Ref, version uint64) (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sec := s.secrets[refKey(ref)]
	if sec == nil {
		return 0, domain.Errorf(domain.ErrNotFound, "secret %s", ref)
	}
	ver, ok := sec.versions[version]
	if !ok {
		return 0, domain.Errorf(domain.ErrNotFound, "secret %s v%d", ref, version)
	}
	ver.State = domain.StateDestroyed
	ver.Ciphertext, ver.EncryptedDEK, ver.Nonce = nil, nil, nil
	ver.DestroyedAt = time.Now().UTC()
	sec.versions[version] = ver
	return s.bump(), nil
}

func (s *fakeStore) PromoteSecretVersion(_ context.Context, ref domain.Ref, version uint64) (uint64, uint64, uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sec := s.secrets[refKey(ref)]
	if sec == nil {
		return 0, 0, 0, domain.Errorf(domain.ErrNotFound, "secret %s", ref)
	}
	if _, ok := sec.versions[version]; !ok {
		return 0, 0, 0, domain.Errorf(domain.ErrNotFound, "secret %s v%d", ref, version)
	}
	prev := sec.rec.Labels[domain.LabelCurrent]
	if prev != version && prev != 0 {
		sec.rec.Labels[domain.LabelPrevious] = prev
	}
	sec.rec.Labels[domain.LabelCurrent] = version
	return version, sec.rec.Labels[domain.LabelPrevious], s.bump(), nil
}

func (s *fakeStore) UpdateSecretAccessTokenHash(_ context.Context, ref domain.Ref, hash []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sec := s.secrets[refKey(ref)]
	if sec == nil {
		return domain.Errorf(domain.ErrNotFound, "secret %s", ref)
	}
	sec.rec.AccessTokenHash = hash
	return nil
}

// --- identities ------------------------------------------------------------

func (s *fakeStore) CreateIdentity(_ context.Context, params storage.CreateIdentityParams) (domain.Identity, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.identities[params.Name]; ok {
		return domain.Identity{}, domain.Errorf(domain.ErrAlreadyExists, "identity %s", params.Name)
	}
	if params.Namespace != nil {
		if _, ok := s.namespaces[nsKey(*params.Namespace)]; !ok {
			return domain.Identity{}, domain.Errorf(domain.ErrNotFound, "namespace %s", *params.Namespace)
		}
	}
	s.nextID++
	fi := &fakeIdentity{id: domain.Identity{
		ID: s.nextID, Name: params.Name, Kind: params.Kind, CreatedAt: time.Now().UTC(),
		Namespace: params.Namespace, HasToken: params.TokenHash != nil,
	}}
	s.identities[params.Name] = fi
	if params.TokenHash != nil {
		s.byToken[hexKey(params.TokenHash)] = params.Name
	}
	if params.Cert != nil {
		if _, exists := s.certs[params.Cert.Serial]; exists {
			delete(s.identities, params.Name)
			delete(s.byToken, hexKey(params.TokenHash))
			return domain.Identity{}, domain.Errorf(domain.ErrAlreadyExists, "certificate %s", params.Cert.Serial)
		}
		fi.certs = append(fi.certs, *params.Cert)
		s.certs[params.Cert.Serial] = params.Name
	}
	return fi.id, nil
}

func (s *fakeStore) GetIdentityByTokenHash(_ context.Context, tokenHash []byte) (domain.Identity, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	name, ok := s.byToken[hexKey(tokenHash)]
	if !ok {
		return domain.Identity{}, domain.Errorf(domain.ErrNotFound, "identity")
	}
	// Hot auth path: namespace binding + has_token, but no cert summaries.
	return s.identities[name].id, nil
}

func (s *fakeStore) GetIdentityByName(_ context.Context, name string) (domain.Identity, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fi, ok := s.identities[name]
	if !ok {
		return domain.Identity{}, domain.Errorf(domain.ErrNotFound, "identity %s", name)
	}
	id := fi.id
	id.Certs = append([]domain.IdentityCert(nil), fi.certs...)
	return id, nil
}

func (s *fakeStore) ListIdentities(_ context.Context, _ storage.ListPage) ([]domain.Identity, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]domain.Identity, 0, len(s.identities))
	for _, fi := range s.identities {
		id := fi.id
		id.Certs = append([]domain.IdentityCert(nil), fi.certs...)
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, "", nil
}

func (s *fakeStore) SetIdentityDisabled(_ context.Context, name string, disabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	fi, ok := s.identities[name]
	if !ok {
		return domain.Errorf(domain.ErrNotFound, "identity %s", name)
	}
	fi.id.Disabled = disabled
	return nil
}

func (s *fakeStore) UpdateIdentityTokenHash(_ context.Context, name string, tokenHash []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	fi, ok := s.identities[name]
	if !ok {
		return domain.Errorf(domain.ErrNotFound, "identity %s", name)
	}
	for h, n := range s.byToken {
		if n == name {
			delete(s.byToken, h)
		}
	}
	if tokenHash != nil {
		s.byToken[hexKey(tokenHash)] = name
	}
	fi.id.HasToken = tokenHash != nil
	return nil
}

// --- built-in CA / client certificates -------------------------------------

func (s *fakeStore) InsertCAKey(_ context.Context, ca storage.CAKeyRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.caKeys {
		s.caKeys[i].State = domain.KeyStateRetired
	}
	s.caKeys = append(s.caKeys, ca)
	return nil
}

func (s *fakeStore) ActiveCAKey(context.Context) (storage.CAKeyRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.caKeys {
		if c.State == domain.KeyStateActive {
			return c, nil
		}
	}
	return storage.CAKeyRecord{}, domain.Errorf(domain.ErrNotFound, "active CA key")
}

func (s *fakeStore) InsertIdentityCert(_ context.Context, identityName string, cert domain.IdentityCert) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	fi, ok := s.identities[identityName]
	if !ok {
		return domain.Errorf(domain.ErrNotFound, "identity %s", identityName)
	}
	fi.certs = append(fi.certs, cert)
	s.certs[cert.Serial] = identityName
	return nil
}

func (s *fakeStore) ListIdentityCerts(_ context.Context, identityName string) ([]domain.IdentityCert, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fi, ok := s.identities[identityName]
	if !ok {
		return nil, domain.Errorf(domain.ErrNotFound, "identity %s", identityName)
	}
	return append([]domain.IdentityCert(nil), fi.certs...), nil
}

func (s *fakeStore) GetIdentityCertBySerial(_ context.Context, serial string) (storage.IdentityCertRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	name, ok := s.certs[serial]
	if !ok {
		return storage.IdentityCertRecord{}, domain.Errorf(domain.ErrNotFound, "certificate %s", serial)
	}
	fi := s.identities[name]
	for _, c := range fi.certs {
		if c.Serial == serial {
			return storage.IdentityCertRecord{Cert: c, IdentityName: name, IdentityDisabled: fi.id.Disabled}, nil
		}
	}
	return storage.IdentityCertRecord{}, domain.Errorf(domain.ErrNotFound, "certificate %s", serial)
}

func (s *fakeStore) RevokeIdentityCert(_ context.Context, serial string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	name, ok := s.certs[serial]
	if !ok {
		return domain.Errorf(domain.ErrNotFound, "certificate %s", serial)
	}
	fi := s.identities[name]
	for i := range fi.certs {
		if fi.certs[i].Serial == serial {
			if fi.certs[i].RevokedAt.IsZero() {
				fi.certs[i].RevokedAt = time.Now().UTC()
			}
			return nil
		}
	}
	return domain.Errorf(domain.ErrNotFound, "certificate %s", serial)
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
		if f.Env != "" && ev.ResourceEnv != f.Env {
			continue
		}
		if f.App != "" && ev.ResourceApp != f.App {
			continue
		}
		if f.KeyPrefix != "" && !strings.HasPrefix(ev.ResourceKey, f.KeyPrefix) {
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
func (s *fakeStore) SnapshotParameters(context.Context, []domain.NamespaceRef) ([]domain.Parameter, uint64, error) {
	return nil, s.revision, nil
}

// --- helpers ---------------------------------------------------------------

// keyHasPrefix reports whether key falls under keyPrefix (empty = all keys),
// matching the real store's namespace-scoped list semantics.
func keyHasPrefix(key, keyPrefix string) bool {
	if keyPrefix == "" {
		return true
	}
	return key == keyPrefix || strings.HasPrefix(key, strings.TrimSuffix(keyPrefix, "/")+"/") || strings.HasPrefix(key, keyPrefix)
}

func cloneLabels(m map[string]uint64) map[string]uint64 {
	out := make(map[string]uint64, len(m))
	maps.Copy(out, m)
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
