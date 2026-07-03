package grpcserver

import (
	"context"
	"encoding/hex"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Suhaibinator/kms/internal/crypto"
	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/keyutil"
	"github.com/Suhaibinator/kms/internal/storage"
)

// memStore is an in-memory storage.Store sufficient to drive a real core.Service
// and watch.Hub in tests. It implements the parameter, namespace, identity,
// certificate, CA, policy, audit, and change-log surface used by the gRPC
// handlers; secret value storage and KEK rotation are out of scope and panic if
// reached.
type memStore struct {
	mu sync.Mutex

	pingErr error

	identities []*domain.Identity           // by name (with token/cert state)
	tokenIndex map[string]string            // hex(tokenHash) -> identity name
	certs      map[string]*certRow          // serial -> issued cert
	caKey      *storage.CAKeyRecord         // single active CA key (nil until bootstrap)
	namespaces map[string]*domain.Namespace // key: ns.String()
	policies   []domain.Policy
	audit      []domain.AuditEvent

	params map[string]*paramRow // key: ref.String()

	changelog []domain.ChangeLogEntry
	revision  uint64

	pruneCalls int

	// getParamErr, when set, is returned by GetParameter to exercise error
	// mapping through a real handler path.
	getParamErr error

	clock func() time.Time
}

type paramRow struct {
	ref         domain.Ref
	value       string
	contentType string
	metadata    string
	createdBy   string
	version     uint64
	createdAt   time.Time
	updatedAt   time.Time
}

type certRow struct {
	identityName string
	cert         domain.IdentityCert
}

func newMemStore() *memStore {
	return &memStore{
		tokenIndex: make(map[string]string),
		certs:      make(map[string]*certRow),
		namespaces: make(map[string]*domain.Namespace),
		params:     make(map[string]*paramRow),
		clock:      func() time.Time { return time.Now().UTC() },
	}
}

// addIdentity registers a token-authenticated identity, optionally bound to a
// home namespace. An empty token creates a cert-only identity.
func (m *memStore) addIdentity(name, kind, token string, ns *domain.NamespaceRef) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id := &domain.Identity{Name: name, Kind: kind, CreatedAt: m.clock(), Namespace: ns}
	if token != "" {
		hash := hex.EncodeToString(crypto.TokenHash(token))
		m.tokenIndex[hash] = name
		id.HasToken = true
	}
	m.identities = append(m.identities, id)
}

func (m *memStore) addPolicy(p domain.Policy) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.policies = append(m.policies, p)
}

func (m *memStore) clearPolicies() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.policies = nil
}

// addNamespace registers a namespace with the given allowed auth methods
// (defaulting to both mtls and token when none are given).
func (m *memStore) addNamespace(ns domain.NamespaceRef, methods ...domain.AuthMethod) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(methods) == 0 {
		methods = []domain.AuthMethod{domain.AuthMethodMTLS, domain.AuthMethodToken}
	}
	m.putNamespaceLocked(ns, "", methods)
}

func (m *memStore) putNamespaceLocked(ns domain.NamespaceRef, desc string, methods []domain.AuthMethod) *domain.Namespace {
	rec := &domain.Namespace{
		NamespaceRef:       ns,
		Description:        desc,
		AllowedAuthMethods: methods,
		CreatedAt:          m.clock(),
	}
	m.namespaces[ns.String()] = rec
	return rec
}

// injectSecretChange appends a secret change-log entry with a poisoned value to
// prove the watch layer never forwards secret values.
func (m *memStore) injectSecretChange(ref domain.Ref, changeType string, version uint64) uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.appendChangeLocked(domain.ChangeLogEntry{
		ResourceType: domain.ResourceSecret,
		Ref:          ref,
		ChangeType:   changeType,
		Value:        "LEAK-CANARY-MUST-NOT-APPEAR",
		Version:      version,
	})
}

func (m *memStore) appendChangeLocked(e domain.ChangeLogEntry) uint64 {
	m.revision++
	e.Revision = m.revision
	if e.CreatedAt.IsZero() {
		e.CreatedAt = m.clock()
	}
	m.changelog = append(m.changelog, e)
	return m.revision
}

// --- lifecycle ---

func (m *memStore) Ping(context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.pingErr
}
func (m *memStore) Close() error                         { return nil }
func (m *memStore) Backup(context.Context, string) error { return nil }

// --- identities ---

func (m *memStore) GetIdentityByTokenHash(_ context.Context, hash []byte) (domain.Identity, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	name, ok := m.tokenIndex[hex.EncodeToString(hash)]
	if !ok {
		return domain.Identity{}, domain.ErrNotFound
	}
	id := m.identityByNameLocked(name)
	if id == nil {
		return domain.Identity{}, domain.ErrNotFound
	}
	return *id, nil
}

func (m *memStore) CreateIdentity(_ context.Context, params storage.CreateIdentityParams) (domain.Identity, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.identityByNameLocked(params.Name) != nil {
		return domain.Identity{}, domain.Errorf(domain.ErrAlreadyExists, "identity %s", params.Name)
	}
	id := &domain.Identity{
		Name:      params.Name,
		Kind:      params.Kind,
		CreatedAt: m.clock(),
		Namespace: params.Namespace,
		HasToken:  len(params.TokenHash) > 0,
	}
	m.identities = append(m.identities, id)
	if len(params.TokenHash) > 0 {
		m.tokenIndex[hex.EncodeToString(params.TokenHash)] = params.Name
	}
	return *id, nil
}

func (m *memStore) GetIdentityByName(_ context.Context, name string) (domain.Identity, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id := m.identityByNameLocked(name)
	if id == nil {
		return domain.Identity{}, domain.ErrNotFound
	}
	out := *id
	out.Certs = m.certsForLocked(name)
	return out, nil
}

func (m *memStore) identityByNameLocked(name string) *domain.Identity {
	for _, id := range m.identities {
		if id.Name == name {
			return id
		}
	}
	return nil
}

func (m *memStore) certsForLocked(name string) []domain.IdentityCert {
	var out []domain.IdentityCert
	for _, c := range m.certs {
		if c.identityName == name {
			out = append(out, c.cert)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Serial < out[j].Serial })
	return out
}

func (m *memStore) ListIdentities(_ context.Context, _ storage.ListPage) ([]domain.Identity, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]domain.Identity, 0, len(m.identities))
	for _, id := range m.identities {
		full := *id
		full.Certs = m.certsForLocked(id.Name)
		out = append(out, full)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, "", nil
}

func (m *memStore) SetIdentityDisabled(_ context.Context, name string, disabled bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	id := m.identityByNameLocked(name)
	if id == nil {
		return domain.ErrNotFound
	}
	id.Disabled = disabled
	return nil
}

func (m *memStore) UpdateIdentityTokenHash(_ context.Context, name string, hash []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	id := m.identityByNameLocked(name)
	if id == nil {
		return domain.ErrNotFound
	}
	for k, n := range m.tokenIndex {
		if n == name {
			delete(m.tokenIndex, k)
		}
	}
	if len(hash) > 0 {
		m.tokenIndex[hex.EncodeToString(hash)] = name
		id.HasToken = true
	} else {
		id.HasToken = false
	}
	return nil
}

// --- built-in CA / client certificates ---

func (m *memStore) InsertCAKey(_ context.Context, ca storage.CAKeyRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec := ca
	m.caKey = &rec
	return nil
}

func (m *memStore) ActiveCAKey(_ context.Context) (storage.CAKeyRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.caKey == nil {
		return storage.CAKeyRecord{}, domain.ErrNotFound
	}
	return *m.caKey, nil
}

func (m *memStore) InsertIdentityCert(_ context.Context, identityName string, cert domain.IdentityCert) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.identityByNameLocked(identityName) == nil {
		return domain.Errorf(domain.ErrNotFound, "identity %s", identityName)
	}
	m.certs[cert.Serial] = &certRow{identityName: identityName, cert: cert}
	return nil
}

func (m *memStore) ListIdentityCerts(_ context.Context, identityName string) ([]domain.IdentityCert, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.certsForLocked(identityName), nil
}

func (m *memStore) GetIdentityCertBySerial(_ context.Context, serial string) (storage.IdentityCertRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.certs[serial]
	if !ok {
		return storage.IdentityCertRecord{}, domain.Errorf(domain.ErrNotFound, "cert %s", serial)
	}
	disabled := false
	if id := m.identityByNameLocked(c.identityName); id != nil {
		disabled = id.Disabled
	}
	return storage.IdentityCertRecord{
		Cert:             c.cert,
		IdentityName:     c.identityName,
		IdentityDisabled: disabled,
	}, nil
}

func (m *memStore) RevokeIdentityCert(_ context.Context, serial string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.certs[serial]
	if !ok {
		return domain.Errorf(domain.ErrNotFound, "cert %s", serial)
	}
	if c.cert.RevokedAt.IsZero() {
		c.cert.RevokedAt = m.clock()
	}
	return nil
}

// --- policies ---

func (m *memStore) PoliciesForSubject(_ context.Context, subject string) ([]domain.Policy, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []domain.Policy
	for _, p := range m.policies {
		if p.Subject == subject || p.Subject == "*" {
			out = append(out, p)
		}
	}
	return out, nil
}

func (m *memStore) CreatePolicy(_ context.Context, p domain.Policy) (domain.Policy, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, existing := range m.policies {
		if existing.Name == p.Name {
			return domain.Policy{}, domain.Errorf(domain.ErrAlreadyExists, "policy %s", p.Name)
		}
	}
	m.policies = append(m.policies, p)
	return p, nil
}

func (m *memStore) UpdatePolicy(_ context.Context, p domain.Policy) (domain.Policy, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, existing := range m.policies {
		if existing.Name == p.Name {
			m.policies[i] = p
			return p, nil
		}
	}
	return domain.Policy{}, domain.Errorf(domain.ErrNotFound, "policy %s", p.Name)
}

func (m *memStore) DeletePolicy(_ context.Context, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, existing := range m.policies {
		if existing.Name == name {
			m.policies = append(m.policies[:i], m.policies[i+1:]...)
			return nil
		}
	}
	return domain.Errorf(domain.ErrNotFound, "policy %s", name)
}

func (m *memStore) ListPolicies(_ context.Context, _ storage.ListPage) ([]domain.Policy, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]domain.Policy, len(m.policies))
	copy(out, m.policies)
	return out, "", nil
}

// --- namespaces ---

func (m *memStore) CreateNamespace(_ context.Context, ns domain.Namespace) (domain.Namespace, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.namespaces[ns.String()]; ok {
		return domain.Namespace{}, domain.Errorf(domain.ErrAlreadyExists, "namespace %s", ns.NamespaceRef)
	}
	if ns.CreatedAt.IsZero() {
		ns.CreatedAt = m.clock()
	}
	rec := ns
	m.namespaces[ns.String()] = &rec
	return rec, nil
}

func (m *memStore) GetNamespace(_ context.Context, ref domain.NamespaceRef) (domain.Namespace, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := m.namespaces[ref.String()]
	if !ok {
		return domain.Namespace{}, domain.Errorf(domain.ErrNotFound, "namespace %s", ref)
	}
	return *rec, nil
}

func (m *memStore) UpdateNamespace(_ context.Context, ref domain.NamespaceRef, description string, methods []domain.AuthMethod) (domain.Namespace, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := m.namespaces[ref.String()]
	if !ok {
		return domain.Namespace{}, domain.Errorf(domain.ErrNotFound, "namespace %s", ref)
	}
	rec.Description = description
	rec.AllowedAuthMethods = methods
	return *rec, nil
}

func (m *memStore) DeleteNamespace(_ context.Context, ref domain.NamespaceRef) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.namespaces[ref.String()]; !ok {
		return domain.Errorf(domain.ErrNotFound, "namespace %s", ref)
	}
	for _, row := range m.params {
		if row.ref.NS == ref {
			return domain.Errorf(domain.ErrFailedPrecondition, "namespace %s is not empty", ref)
		}
	}
	for _, id := range m.identities {
		if id.Namespace != nil && *id.Namespace == ref {
			return domain.Errorf(domain.ErrFailedPrecondition, "namespace %s has bound identities", ref)
		}
	}
	delete(m.namespaces, ref.String())
	return nil
}

func (m *memStore) ListNamespaces(_ context.Context, _ storage.ListPage) ([]domain.Namespace, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]domain.Namespace, 0, len(m.namespaces))
	for _, rec := range m.namespaces {
		full := *rec
		for _, row := range m.params {
			if row.ref.NS == rec.NamespaceRef {
				full.ParameterCount++
			}
		}
		out = append(out, full)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out, "", nil
}

// --- audit ---

func (m *memStore) AppendAudit(_ context.Context, ev domain.AuditEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.audit = append(m.audit, ev)
	return nil
}

func (m *memStore) ListAudit(_ context.Context, _ domain.AuditFilter, _ storage.ListPage) ([]domain.AuditEvent, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]domain.AuditEvent, len(m.audit))
	copy(out, m.audit)
	return out, "", nil
}

// --- parameters ---

func (m *memStore) PutParameter(_ context.Context, ref domain.Ref, value, contentType, metadata, createdBy string) (uint64, uint64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	// A parameter/secret write requires the namespace to exist. Tests seed most
	// writes through the admin path (which bypasses the method gate), so
	// auto-provision a permissive namespace when the test did not create one.
	if _, ok := m.namespaces[ref.NS.String()]; !ok {
		m.putNamespaceLocked(ref.NS, "", []domain.AuthMethod{domain.AuthMethodMTLS, domain.AuthMethodToken})
	}
	row := m.params[ref.String()]
	if row == nil {
		row = &paramRow{ref: ref, createdAt: m.clock()}
		m.params[ref.String()] = row
	}
	row.version++
	row.value = value
	row.contentType = contentType
	row.metadata = metadata
	row.createdBy = createdBy
	row.updatedAt = m.clock()
	rev := m.appendChangeLocked(domain.ChangeLogEntry{
		ResourceType: domain.ResourceParameter,
		Ref:          ref,
		ChangeType:   domain.ChangePut,
		Value:        value,
		ContentType:  contentType,
		Version:      row.version,
	})
	return row.version, rev, nil
}

func (m *memStore) GetParameter(_ context.Context, ref domain.Ref, _ uint64, _ string) (domain.Parameter, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.getParamErr != nil {
		return domain.Parameter{}, m.getParamErr
	}
	row := m.params[ref.String()]
	if row == nil {
		return domain.Parameter{}, domain.Errorf(domain.ErrNotFound, "parameter %s", ref)
	}
	return m.paramLocked(row), nil
}

func (m *memStore) paramLocked(row *paramRow) domain.Parameter {
	return domain.Parameter{
		Ref:         row.ref,
		Value:       row.value,
		ContentType: row.contentType,
		Version:     row.version,
		Metadata:    row.metadata,
		CreatedBy:   row.createdBy,
		CreatedAt:   row.createdAt,
		Labels:      map[string]uint64{domain.LabelCurrent: row.version},
	}
}

func (m *memStore) GetParameterInfo(_ context.Context, ref domain.Ref) (domain.ParameterInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	row := m.params[ref.String()]
	if row == nil {
		return domain.ParameterInfo{}, domain.Errorf(domain.ErrNotFound, "parameter %s", ref)
	}
	return domain.ParameterInfo{
		Ref:         row.ref,
		ContentType: row.contentType,
		Metadata:    row.metadata,
		CreatedAt:   row.createdAt,
		UpdatedAt:   row.updatedAt,
		Labels:      map[string]uint64{domain.LabelCurrent: row.version},
	}, nil
}

func (m *memStore) ListParameters(_ context.Context, ns domain.NamespaceRef, keyPrefix string, _ storage.ListPage) ([]domain.Parameter, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []domain.Parameter
	for _, row := range m.params {
		if row.ref.NS != ns {
			continue
		}
		if keyPrefix != "" && (row.ref.Key != keyPrefix && !strings.HasPrefix(row.ref.Key, keyPrefix)) {
			continue
		}
		out = append(out, m.paramLocked(row))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Ref.Key < out[j].Ref.Key })
	return out, "", nil
}

func (m *memStore) DeleteParameter(_ context.Context, ref domain.Ref) (uint64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.params[ref.String()]; !ok {
		return 0, domain.Errorf(domain.ErrNotFound, "parameter %s", ref)
	}
	delete(m.params, ref.String())
	return m.appendChangeLocked(domain.ChangeLogEntry{
		ResourceType: domain.ResourceParameter,
		Ref:          ref,
		ChangeType:   domain.ChangeDelete,
	}), nil
}

// --- change log / revisions ---

func (m *memStore) CurrentRevision(context.Context) (uint64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.pingErr != nil {
		return 0, m.pingErr
	}
	return m.revision, nil
}

func (m *memStore) OldestRetainedRevision(context.Context) (uint64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.changelog) == 0 {
		return 0, nil
	}
	return m.changelog[0].Revision, nil
}

func (m *memStore) ListChangesSince(_ context.Context, since uint64, limit int) ([]domain.ChangeLogEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []domain.ChangeLogEntry
	for _, e := range m.changelog {
		if e.Revision > since {
			out = append(out, e)
			if len(out) >= limit {
				break
			}
		}
	}
	return out, nil
}

func (m *memStore) PruneChangeLog(_ context.Context, _ time.Duration, _ int) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pruneCalls++
	return 0, nil
}

func (m *memStore) SnapshotParameters(_ context.Context, selectors []domain.WatchSelector) ([]domain.Parameter, uint64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []domain.Parameter
	for _, row := range m.params {
		if selectorMatchAny(selectors, row.ref) {
			out = append(out, m.paramLocked(row))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Ref.String() < out[j].Ref.String() })
	return out, m.revision, nil
}

func selectorMatchAny(selectors []domain.WatchSelector, ref domain.Ref) bool {
	for _, s := range selectors {
		if s.NS == ref.NS && keyutil.MatchKey(s.KeyPattern, ref.Key) {
			return true
		}
	}
	return false
}

// --- key metadata (unused: KEK rotation not exercised here) ---

func (m *memStore) InsertKeyMetadata(context.Context, domain.KeyMetadata) error { panic("unused") }
func (m *memStore) GetKeyMetadata(context.Context, string) (domain.KeyMetadata, error) {
	panic("unused")
}
func (m *memStore) ListKeyMetadata(context.Context) ([]domain.KeyMetadata, error) { panic("unused") }
func (m *memStore) ActiveKeyMetadata(context.Context) (domain.KeyMetadata, error) {
	panic("unused")
}
func (m *memStore) SetKeyState(context.Context, string, string) error { panic("unused") }
func (m *memStore) RotateKEK(context.Context, domain.KeyMetadata,
	func(storage.SecretVersionRecord) ([]byte, error),
	func(storage.CAKeyRecord) ([]byte, error)) (int, int, error) {
	panic("unused")
}

// --- secrets (value storage out of scope here) ---

func (m *memStore) CreateSecretVersion(context.Context, storage.CreateSecretParams) (uint64, uint64, error) {
	panic("unused")
}
func (m *memStore) GetSecretRecord(context.Context, domain.Ref) (storage.SecretRecord, error) {
	panic("unused")
}
func (m *memStore) GetSecretVersion(context.Context, domain.Ref, uint64, string) (storage.SecretRecord, storage.SecretVersionRecord, error) {
	panic("unused")
}
func (m *memStore) GetSecretInfo(context.Context, domain.Ref) (domain.Secret, error) { panic("unused") }
func (m *memStore) ListSecrets(context.Context, domain.NamespaceRef, string, storage.ListPage) ([]domain.Secret, string, error) {
	return nil, "", nil
}
func (m *memStore) DeleteSecret(context.Context, domain.Ref) (uint64, error) { panic("unused") }
func (m *memStore) SetSecretVersionState(context.Context, domain.Ref, uint64, string) (uint64, error) {
	panic("unused")
}
func (m *memStore) DestroySecretVersion(context.Context, domain.Ref, uint64) (uint64, error) {
	panic("unused")
}
func (m *memStore) PromoteSecretVersion(context.Context, domain.Ref, uint64) (uint64, uint64, uint64, error) {
	panic("unused")
}
func (m *memStore) UpdateSecretAccessTokenHash(context.Context, domain.Ref, []byte) error {
	panic("unused")
}
