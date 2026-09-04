package grpcserver

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json/v2"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Suhaibinator/kms/internal/crypto"
	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/storage"
)

// memStore is an in-memory storage.Store sufficient to drive a real core.Service
// and watch.Hub in tests. It implements the parameter, namespace, identity,
// certificate, CA, policy, audit, and change-log surface used by the gRPC
// handlers. Secret storage is intentionally small but functional so public
// SecretService field mappings are exercised over the real gRPC boundary; KEK
// rotation remains out of scope.
type memStore struct {
	mu sync.Mutex

	pingErr  error
	auditErr error
	// purgeResultErr is a test seam for post-commit cleanup failures. Purge
	// still applies its logical mutation before returning this error.
	purgeResultErr error

	identities      []*domain.Identity           // by name (with token/cert state)
	tokenIndex      map[string]string            // hex(tokenHash) -> identity name
	certs           map[string]*certRow          // serial -> issued cert
	caKey           *storage.CAKeyRecord         // single active CA key (nil until bootstrap)
	namespaces      map[string]*domain.Namespace // key: ns.String()
	nextNamespaceID int64
	policies        []domain.Policy
	audit           []domain.AuditEvent

	params       map[string]*paramRow  // key: ref.String()
	secrets      map[string]*secretRow // key: ref.String()
	nextSecretID int64

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

type secretRow struct {
	record   storage.SecretRecord
	next     uint64
	versions map[uint64]storage.SecretVersionRecord
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
		secrets:    make(map[string]*secretRow),
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
	m.nextNamespaceID++
	rec := &domain.Namespace{
		ID:                 m.nextNamespaceID,
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
	if e.NamespaceID == 0 {
		if ns := m.namespaces[e.Ref.NS.String()]; ns != nil {
			e.NamespaceID = ns.ID
		}
	}
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
	if params.Cert != nil {
		if _, exists := m.certs[params.Cert.Serial]; exists {
			m.identities = m.identities[:len(m.identities)-1]
			delete(m.tokenIndex, hex.EncodeToString(params.TokenHash))
			return domain.Identity{}, domain.Errorf(domain.ErrAlreadyExists, "certificate %s", params.Cert.Serial)
		}
		m.certs[params.Cert.Serial] = &certRow{identityName: params.Name, cert: *params.Cert}
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
	m.nextNamespaceID++
	ns.ID = m.nextNamespaceID
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
	for _, row := range m.secrets {
		if row.record.Ref.NS == ref {
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
		for _, row := range m.secrets {
			if row.record.Ref.NS == rec.NamespaceRef {
				full.SecretCount++
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
	if m.auditErr != nil {
		return m.auditErr
	}
	m.audit = append(m.audit, ev)
	return nil
}

// ListAudit honors only the decision filter; the remaining predicates are
// exercised against the real store in internal/storage.
func (m *memStore) ListAudit(_ context.Context, f domain.AuditFilter, _ storage.ListPage) ([]domain.AuditEvent, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]domain.AuditEvent, 0, len(m.audit))
	for _, ev := range m.audit {
		if f.Decision != "" && ev.Decision != f.Decision {
			continue
		}
		out = append(out, ev)
	}
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

func (m *memStore) SnapshotParameters(_ context.Context, namespaces []domain.NamespaceRef) ([]domain.Parameter, uint64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []domain.Parameter
	for _, row := range m.params {
		if namespaceMatchAny(namespaces, row.ref.NS) {
			out = append(out, m.paramLocked(row))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Ref.String() < out[j].Ref.String() })
	return out, m.revision, nil
}

func namespaceMatchAny(namespaces []domain.NamespaceRef, ns domain.NamespaceRef) bool {
	return slices.Contains(namespaces, ns)
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

// --- secrets ---------------------------------------------------------------

func cloneSecretLabels(labels map[string]uint64) map[string]uint64 {
	out := make(map[string]uint64, len(labels))
	for label, version := range labels {
		out[label] = version
	}
	return out
}

func cloneMemSecretRecord(record storage.SecretRecord) storage.SecretRecord {
	record.AccessTokenHash = bytes.Clone(record.AccessTokenHash)
	record.Labels = cloneSecretLabels(record.Labels)
	return record
}

func cloneMemSecretVersion(version storage.SecretVersionRecord) storage.SecretVersionRecord {
	version.Ciphertext = bytes.Clone(version.Ciphertext)
	version.EncryptedDEK = bytes.Clone(version.EncryptedDEK)
	version.BindingKeySalt = bytes.Clone(version.BindingKeySalt)
	version.Nonce = bytes.Clone(version.Nonce)
	return version
}

func validateMemSecretPayload(bound bool, payload storage.EncryptedPayload) error {
	if bound {
		if payload.WrapMode != domain.WrapModeBindingKey || len(payload.BindingKeySalt) != crypto.BindingKeySaltSize {
			return domain.Errorf(domain.ErrFailedPrecondition, "invalid bound secret payload")
		}
		return nil
	}
	if payload.WrapMode != domain.WrapModeStandard || len(payload.BindingKeySalt) != 0 {
		return domain.Errorf(domain.ErrFailedPrecondition, "invalid unbound secret payload")
	}
	return nil
}

func (m *memStore) CreateSecretVersion(_ context.Context, params storage.CreateSecretParams) (uint64, uint64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.namespaces[params.Ref.NS.String()]; !ok {
		return 0, 0, domain.Errorf(domain.ErrNotFound, "namespace %s", params.Ref.NS)
	}
	row := m.secrets[params.Ref.String()]
	exists := row != nil
	if params.Expected != nil {
		if params.Expected.Exists != exists {
			return 0, 0, domain.Errorf(domain.ErrAborted, "secret changed concurrently")
		}
		if exists && (params.Expected.ID != row.record.ID || !bytes.Equal(params.Expected.AccessTokenHash, row.record.AccessTokenHash)) {
			return 0, 0, domain.Errorf(domain.ErrAborted, "secret changed concurrently")
		}
	}
	version := uint64(1)
	if row != nil {
		version = row.next + 1
	}
	payload, err := params.Encrypt(version)
	if err != nil {
		return 0, 0, err
	}
	if err := validateMemSecretPayload(params.Bound, payload); err != nil {
		return 0, 0, err
	}
	now := m.clock()
	if row == nil {
		m.nextSecretID++
		row = &secretRow{
			record:   storage.SecretRecord{ID: m.nextSecretID, Ref: params.Ref, CreatedAt: now, Labels: map[string]uint64{}},
			versions: make(map[uint64]storage.SecretVersionRecord),
		}
		m.secrets[params.Ref.String()] = row
	}
	if params.AccessTokenHash != nil {
		row.record.AccessTokenHash = bytes.Clone(params.AccessTokenHash)
	}
	row.next = version
	row.versions[version] = storage.SecretVersionRecord{
		ID:             int64(version),
		SecretID:       row.record.ID,
		Version:        version,
		ContentType:    params.ContentType,
		Bound:          params.Bound,
		HasAccessToken: len(row.record.AccessTokenHash) != 0,
		Ciphertext:     bytes.Clone(payload.Ciphertext),
		EncryptedDEK:   bytes.Clone(payload.EncryptedDEK),
		KEKID:          payload.KEKID,
		WrapMode:       payload.WrapMode,
		BindingKeySalt: bytes.Clone(payload.BindingKeySalt),
		Algorithm:      payload.Algorithm,
		Nonce:          bytes.Clone(payload.Nonce),
		AAD:            payload.AAD,
		State:          domain.StateEnabled,
		CreatedBy:      params.CreatedBy,
		CreatedAt:      now,
		ExpiresAt:      params.ExpiresAt,
		Metadata:       params.Metadata,
	}
	if current := row.record.Labels[domain.LabelCurrent]; current != 0 {
		row.record.Labels[domain.LabelPrevious] = current
	}
	row.record.Labels[domain.LabelCurrent] = version
	row.record.Bound = params.Bound
	row.record.ContentType = params.ContentType
	row.record.Metadata = params.Metadata
	row.record.UpdatedAt = now
	revision := m.appendChangeLocked(domain.ChangeLogEntry{
		ResourceType: domain.ResourceSecret, Ref: params.Ref, ChangeType: domain.ChangePut,
		ContentType: params.ContentType, Version: version,
	})
	return version, revision, nil
}

func (m *memStore) GetSecretRecord(_ context.Context, ref domain.Ref) (storage.SecretRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	row := m.secrets[ref.String()]
	if row == nil {
		return storage.SecretRecord{}, domain.Errorf(domain.ErrNotFound, "secret %s", ref)
	}
	record := cloneMemSecretRecord(row.record)
	if current := record.Labels[domain.LabelCurrent]; current != 0 {
		record.Bound = row.versions[current].Bound
	}
	return record, nil
}

func (m *memStore) resolveSecretVersionLocked(ref domain.Ref, version uint64, label string) (*secretRow, uint64, storage.SecretVersionRecord, error) {
	row := m.secrets[ref.String()]
	if row == nil {
		return nil, 0, storage.SecretVersionRecord{}, domain.Errorf(domain.ErrNotFound, "secret %s", ref)
	}
	if version == 0 {
		if label == "" {
			label = domain.LabelCurrent
		}
		version = row.record.Labels[label]
	}
	secretVersion, ok := row.versions[version]
	if !ok || version == 0 {
		return nil, 0, storage.SecretVersionRecord{}, domain.Errorf(domain.ErrNotFound, "secret %s version %d", ref, version)
	}
	return row, version, secretVersion, nil
}

func (m *memStore) GetSecretVersion(_ context.Context, ref domain.Ref, version uint64, label string) (storage.SecretRecord, storage.SecretVersionRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	row, _, secretVersion, err := m.resolveSecretVersionLocked(ref, version, label)
	if err != nil {
		return storage.SecretRecord{}, storage.SecretVersionRecord{}, err
	}
	record := cloneMemSecretRecord(row.record)
	if current := record.Labels[domain.LabelCurrent]; current != 0 {
		record.Bound = row.versions[current].Bound
	}
	return record, cloneMemSecretVersion(secretVersion), nil
}

func (m *memStore) secretInfoLocked(row *secretRow) domain.Secret {
	currentBound := false
	if current := row.record.Labels[domain.LabelCurrent]; current != 0 {
		if version, ok := row.versions[current]; ok {
			currentBound = version.Bound
		}
	}
	info := domain.Secret{
		Ref: row.record.Ref, ContentType: row.record.ContentType, Bound: currentBound,
		HasAccessToken: len(row.record.AccessTokenHash) != 0, Metadata: row.record.Metadata,
		CreatedAt: row.record.CreatedAt, UpdatedAt: row.record.UpdatedAt,
		Labels: cloneSecretLabels(row.record.Labels),
	}
	for version := uint64(1); version <= row.next; version++ {
		record, ok := row.versions[version]
		if !ok {
			continue
		}
		info.Versions = append(info.Versions, domain.SecretVersionInfo{
			Version: record.Version, State: record.State, Bound: record.Bound,
			HasAccessToken: record.HasAccessToken, CreatedBy: record.CreatedBy,
			CreatedAt: record.CreatedAt, DestroyedAt: record.DestroyedAt,
			ExpiresAt: record.ExpiresAt, Metadata: record.Metadata,
		})
	}
	return info
}

func (m *memStore) GetSecretInfo(_ context.Context, ref domain.Ref) (domain.Secret, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	row := m.secrets[ref.String()]
	if row == nil {
		return domain.Secret{}, domain.Errorf(domain.ErrNotFound, "secret %s", ref)
	}
	return m.secretInfoLocked(row), nil
}

func (m *memStore) ListSecrets(_ context.Context, namespace domain.NamespaceRef, keyPrefix string, _ storage.ListPage) ([]domain.Secret, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.namespaces[namespace.String()]; !ok {
		return nil, "", domain.Errorf(domain.ErrNotFound, "namespace %s", namespace)
	}
	var out []domain.Secret
	for _, row := range m.secrets {
		if row.record.Ref.NS != namespace || !strings.HasPrefix(row.record.Ref.Key, keyPrefix) {
			continue
		}
		out = append(out, m.secretInfoLocked(row))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Ref.Key < out[j].Ref.Key })
	return out, "", nil
}

func (m *memStore) DeleteSecret(_ context.Context, ref domain.Ref) (uint64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.secrets[ref.String()] == nil {
		return 0, domain.Errorf(domain.ErrNotFound, "secret %s", ref)
	}
	delete(m.secrets, ref.String())
	return m.appendChangeLocked(domain.ChangeLogEntry{ResourceType: domain.ResourceSecret, Ref: ref, ChangeType: domain.ChangeDelete}), nil
}

func (m *memStore) SetSecretVersionState(_ context.Context, ref domain.Ref, version uint64, state string) (uint64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	row := m.secrets[ref.String()]
	if row == nil {
		return 0, domain.Errorf(domain.ErrNotFound, "secret %s", ref)
	}
	if version != 0 {
		if _, ok := row.versions[version]; !ok {
			return 0, domain.Errorf(domain.ErrNotFound, "secret %s version %d", ref, version)
		}
	}
	for number, record := range row.versions {
		if (version == 0 || number == version) && record.State != domain.StateDestroyed {
			record.State = state
			row.versions[number] = record
		}
	}
	changeType := domain.ChangeDisable
	if state == domain.StateEnabled {
		changeType = domain.ChangeEnable
	}
	return m.appendChangeLocked(domain.ChangeLogEntry{ResourceType: domain.ResourceSecret, Ref: ref, ChangeType: changeType, Version: version}), nil
}

func (m *memStore) DestroySecretVersion(_ context.Context, ref domain.Ref, version uint64) (uint64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if version == 0 {
		return 0, domain.Errorf(domain.ErrInvalidArgument, "version is required")
	}
	row, _, record, err := m.resolveSecretVersionLocked(ref, version, "")
	if err != nil {
		return 0, err
	}
	if record.State == domain.StateDestroyed {
		return 0, domain.Errorf(domain.ErrFailedPrecondition, "secret version already destroyed")
	}
	record.Ciphertext = nil
	record.EncryptedDEK = nil
	record.Nonce = nil
	record.State = domain.StateDestroyed
	record.DestroyedAt = m.clock()
	row.versions[version] = record
	return m.appendChangeLocked(domain.ChangeLogEntry{ResourceType: domain.ResourceSecret, Ref: ref, ChangeType: domain.ChangeDestroy, Version: version}), nil
}

func (m *memStore) PromoteSecretVersion(_ context.Context, ref domain.Ref, version uint64) (uint64, uint64, uint64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if version == 0 {
		return 0, 0, 0, domain.Errorf(domain.ErrInvalidArgument, "version is required")
	}
	row, _, record, err := m.resolveSecretVersionLocked(ref, version, "")
	if err != nil {
		return 0, 0, 0, err
	}
	if record.State != domain.StateEnabled {
		return 0, 0, 0, domain.Errorf(domain.ErrFailedPrecondition, "secret version is not enabled")
	}
	oldCurrent := row.record.Labels[domain.LabelCurrent]
	if oldCurrent != 0 && oldCurrent != version {
		row.record.Labels[domain.LabelPrevious] = oldCurrent
	}
	row.record.Labels[domain.LabelCurrent] = version
	row.record.Bound = record.Bound
	previous := row.record.Labels[domain.LabelPrevious]
	revision := m.appendChangeLocked(domain.ChangeLogEntry{ResourceType: domain.ResourceSecret, Ref: ref, ChangeType: domain.ChangePromote, Version: version})
	return version, previous, revision, nil
}

func (m *memStore) TransitionSecretVersion(_ context.Context, params storage.SecretVersionTransitionParams) (storage.SecretVersionTransitionResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if params.ExpectedCurrentVersion == 0 {
		return storage.SecretVersionTransitionResult{}, domain.Errorf(domain.ErrInvalidArgument, "expected current version is required")
	}
	row, current, source, err := m.resolveSecretVersionLocked(params.Ref, 0, domain.LabelCurrent)
	if err != nil {
		return storage.SecretVersionTransitionResult{}, err
	}
	if current != params.ExpectedCurrentVersion {
		return storage.SecretVersionTransitionResult{}, domain.Errorf(domain.ErrAborted, "secret current version changed")
	}
	if source.State == domain.StateDestroyed {
		return storage.SecretVersionTransitionResult{}, domain.Errorf(domain.ErrFailedPrecondition, "current secret version is destroyed")
	}
	targetBound := params.Kind != storage.SecretTransitionUnbind
	if source.Bound == targetBound && params.Kind != storage.SecretTransitionRotate {
		return storage.SecretVersionTransitionResult{}, domain.Errorf(domain.ErrFailedPrecondition, "secret version cannot change binding state")
	}
	if params.Kind == storage.SecretTransitionRotate && !source.Bound {
		return storage.SecretVersionTransitionResult{}, domain.Errorf(domain.ErrFailedPrecondition, "current secret version is not bound")
	}
	version := row.next + 1
	payload, err := params.Encrypt(cloneMemSecretVersion(source), version)
	if err != nil {
		return storage.SecretVersionTransitionResult{}, err
	}
	if err := validateMemSecretPayload(targetBound, payload); err != nil {
		return storage.SecretVersionTransitionResult{}, err
	}
	if m.auditErr != nil {
		return storage.SecretVersionTransitionResult{}, storage.ErrRequiredAuditUnavailable
	}
	now := m.clock()
	record := cloneMemSecretVersion(source)
	record.ID, record.Version = int64(version), version
	record.Bound, record.Ciphertext = targetBound, bytes.Clone(payload.Ciphertext)
	record.EncryptedDEK, record.KEKID = bytes.Clone(payload.EncryptedDEK), payload.KEKID
	record.WrapMode, record.BindingKeySalt = payload.WrapMode, bytes.Clone(payload.BindingKeySalt)
	record.Algorithm, record.Nonce, record.AAD = payload.Algorithm, bytes.Clone(payload.Nonce), payload.AAD
	record.CreatedBy, record.CreatedAt, record.DestroyedAt = params.CreatedBy, now, time.Time{}
	row.next, row.versions[version] = version, record
	row.record.Labels[domain.LabelPrevious], row.record.Labels[domain.LabelCurrent] = current, version
	row.record.Bound, row.record.ContentType, row.record.Metadata, row.record.UpdatedAt = targetBound, source.ContentType, source.Metadata, now
	changeType, eventType := domain.ChangeBind, "secret.bind"
	if params.Kind == storage.SecretTransitionUnbind {
		changeType, eventType = domain.ChangeUnbind, "secret.unbind"
	} else if params.Kind == storage.SecretTransitionRotate {
		changeType, eventType = domain.ChangeRotateBindingKey, "secret.binding_key.rotate"
	}
	affected := []uint64{current, version}
	revision := m.appendChangeLocked(domain.ChangeLogEntry{ResourceType: domain.ResourceSecret, Ref: params.Ref, ChangeType: changeType, Version: version, AffectedVersions: affected})
	m.appendBindingAuditLocked(eventType, params.Ref, version, affected, params.Audit)
	return storage.SecretVersionTransitionResult{CurrentVersion: version, PreviousVersion: current, Revision: revision}, nil
}

func (m *memStore) bindingCohortLocked(ref domain.Ref, anchor uint64, test storage.SecretBindingTestFunc) (*secretRow, uint64, []uint64, error) {
	row, anchor, record, err := m.resolveSecretVersionLocked(ref, anchor, "")
	if err != nil {
		return nil, 0, nil, err
	}
	if record.State == domain.StateDestroyed || !record.Bound {
		return nil, 0, nil, domain.Errorf(domain.ErrFailedPrecondition, "anchor is not a live bound version")
	}
	if err := test(cloneMemSecretVersion(record)); err != nil {
		return nil, 0, nil, err
	}
	affected := []uint64{anchor}
	for version := anchor; version > 1; {
		version--
		candidate, ok := row.versions[version]
		if !ok || candidate.State == domain.StateDestroyed || !candidate.Bound || test(cloneMemSecretVersion(candidate)) != nil {
			break
		}
		affected = append(affected, version)
	}
	for version := anchor + 1; version > anchor; version++ {
		candidate, ok := row.versions[version]
		if !ok || candidate.State == domain.StateDestroyed || !candidate.Bound || test(cloneMemSecretVersion(candidate)) != nil {
			break
		}
		affected = append(affected, version)
	}
	slices.Sort(affected)
	return row, anchor, affected, nil
}

func validateMemBindingGuard(guard storage.SecretBindingCASGuard) error {
	if guard.ExpectedRevision == nil {
		if len(guard.ExpectedAffectedVersions) != 0 {
			return domain.Errorf(domain.ErrInvalidArgument, "expected revision and affected versions must be supplied together")
		}
		return nil
	}
	if *guard.ExpectedRevision == 0 || len(guard.ExpectedAffectedVersions) == 0 {
		return domain.Errorf(domain.ErrInvalidArgument, "expected revision and affected versions must be supplied together")
	}
	for i, version := range guard.ExpectedAffectedVersions {
		if version == 0 || i > 0 && version <= guard.ExpectedAffectedVersions[i-1] {
			return domain.Errorf(domain.ErrInvalidArgument, "expected affected versions must be sorted and unique")
		}
	}
	return nil
}

func (m *memStore) PreviewSecretBindingCohort(_ context.Context, ref domain.Ref, anchor uint64, test storage.SecretBindingTestFunc) (storage.SecretBindingResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, resolved, affected, err := m.bindingCohortLocked(ref, anchor, test)
	if err != nil {
		return storage.SecretBindingResult{}, err
	}
	return storage.SecretBindingResult{AnchorVersion: resolved, AffectedVersions: affected, Revision: m.revision}, nil
}

func (m *memStore) appendBindingAuditLocked(eventType string, ref domain.Ref, anchor uint64, affected []uint64, audit storage.SecretBindingAudit) {
	metadata, _ := json.Marshal(affected)
	namespaceID := int64(0)
	if namespace := m.namespaces[ref.NS.String()]; namespace != nil {
		namespaceID = namespace.ID
	}
	createdAt := audit.CreatedAt
	if createdAt.IsZero() {
		createdAt = m.clock()
	}
	m.audit = append(m.audit, domain.AuditEvent{
		EventType: eventType, ActorIdentity: audit.ActorIdentity,
		ActorType: audit.ActorType, ResourceType: domain.ResourceSecret,
		ResourceNamespaceID: namespaceID, ResourceEnv: ref.NS.Env, ResourceApp: ref.NS.App,
		ResourceKey: ref.Key, ResourceVersion: anchor, Decision: "allow",
		SourceIP: audit.SourceIP, UserAgent: audit.UserAgent, RequestID: audit.RequestID,
		CreatedAt: createdAt, Metadata: `{"affected_versions":` + string(metadata) + `}`,
	})
}

func (m *memStore) PurgeSecretBindingCohort(_ context.Context, ref domain.Ref, anchor uint64, guard storage.SecretBindingCASGuard, test storage.SecretBindingTestFunc, audit storage.SecretBindingPurgeAudit) (storage.SecretBindingResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := validateMemBindingGuard(guard); err != nil {
		return storage.SecretBindingResult{}, err
	}
	row, resolved, affected, err := m.bindingCohortLocked(ref, anchor, test)
	if err != nil {
		return storage.SecretBindingResult{}, err
	}
	if guard.ExpectedRevision != nil && (*guard.ExpectedRevision != m.revision || !slices.Equal(guard.ExpectedAffectedVersions, affected)) {
		return storage.SecretBindingResult{}, domain.Errorf(domain.ErrAborted, "secret version set changed")
	}
	if m.auditErr != nil {
		return storage.SecretBindingResult{}, storage.ErrRequiredAuditUnavailable
	}
	now := m.clock()
	for _, version := range affected {
		record := row.versions[version]
		record.ContentType, record.Metadata, record.KEKID, record.WrapMode, record.Algorithm, record.AAD = "", "", "", "", "", ""
		record.Bound, record.HasAccessToken = false, false
		record.Ciphertext, record.EncryptedDEK, record.BindingKeySalt, record.Nonce = nil, nil, nil, nil
		record.ExpiresAt = time.Time{}
		record.State, record.DestroyedAt = domain.StateDestroyed, now
		row.versions[version] = record
	}
	if slices.Contains(affected, row.record.Labels[domain.LabelCurrent]) {
		row.record.Bound = false
		row.record.ContentType = ""
		row.record.Metadata = ""
	}
	row.record.UpdatedAt = now
	revision := m.appendChangeLocked(domain.ChangeLogEntry{
		ResourceType: domain.ResourceSecret, Ref: ref, ChangeType: domain.ChangePurgeBindingCohort,
		Version: resolved, AffectedVersions: slices.Clone(affected),
	})
	m.audit = append(m.audit, domain.AuditEvent{
		EventType: "secret.binding_cohort.purge", ActorIdentity: audit.ActorIdentity,
		ActorType: audit.ActorType, ResourceType: domain.ResourceSecret,
		ResourceEnv: ref.NS.Env, ResourceApp: ref.NS.App, ResourceKey: ref.Key,
		ResourceVersion: resolved, Decision: "allow", SourceIP: audit.SourceIP,
		UserAgent: audit.UserAgent, RequestID: audit.RequestID, CreatedAt: now,
	})
	result := storage.SecretBindingResult{AnchorVersion: resolved, AffectedVersions: affected, Revision: revision}
	if m.purgeResultErr != nil {
		return result, m.purgeResultErr
	}
	return result, nil
}

func (m *memStore) PreviewSecretUnboundVersions(_ context.Context, ref domain.Ref) (storage.SecretVersionSetResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	row := m.secrets[ref.String()]
	if row == nil {
		return storage.SecretVersionSetResult{}, domain.Errorf(domain.ErrNotFound, "secret %s", ref)
	}
	affected := make([]uint64, 0)
	for version, record := range row.versions {
		if record.State != domain.StateDestroyed && !record.Bound {
			affected = append(affected, version)
		}
	}
	slices.Sort(affected)
	if len(affected) == 0 {
		return storage.SecretVersionSetResult{}, domain.Errorf(domain.ErrFailedPrecondition, "secret has no unbound versions")
	}
	return storage.SecretVersionSetResult{AffectedVersions: affected, Revision: m.revision}, nil
}

func (m *memStore) PurgeSecretUnboundVersions(_ context.Context, ref domain.Ref, expectedRevision uint64, expectedAffected []uint64, audit storage.SecretBindingPurgeAudit) (storage.SecretVersionSetResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	row := m.secrets[ref.String()]
	if row == nil {
		return storage.SecretVersionSetResult{}, domain.Errorf(domain.ErrNotFound, "secret %s", ref)
	}
	affected := make([]uint64, 0)
	for version, record := range row.versions {
		if record.State != domain.StateDestroyed && !record.Bound {
			affected = append(affected, version)
		}
	}
	slices.Sort(affected)
	if expectedRevision == 0 || len(expectedAffected) == 0 {
		return storage.SecretVersionSetResult{}, domain.Errorf(domain.ErrInvalidArgument, "exact preview guard is required")
	}
	if expectedRevision != m.revision || !slices.Equal(expectedAffected, affected) {
		return storage.SecretVersionSetResult{}, domain.Errorf(domain.ErrAborted, "secret version set changed")
	}
	if m.auditErr != nil {
		return storage.SecretVersionSetResult{}, storage.ErrRequiredAuditUnavailable
	}
	now := m.clock()
	for _, version := range affected {
		record := row.versions[version]
		record.ContentType, record.Metadata, record.KEKID, record.WrapMode, record.Algorithm, record.AAD = "", "", "", "", "", ""
		record.Bound, record.HasAccessToken = false, false
		record.Ciphertext, record.EncryptedDEK, record.BindingKeySalt, record.Nonce = nil, nil, nil, nil
		record.ExpiresAt, record.State, record.DestroyedAt = time.Time{}, domain.StateDestroyed, now
		row.versions[version] = record
	}
	if slices.Contains(affected, row.record.Labels[domain.LabelCurrent]) {
		row.record.Bound, row.record.ContentType, row.record.Metadata = false, "", ""
	}
	row.record.UpdatedAt = now
	anchor := affected[0]
	revision := m.appendChangeLocked(domain.ChangeLogEntry{ResourceType: domain.ResourceSecret, Ref: ref, ChangeType: domain.ChangePurgeUnbound, Version: anchor, AffectedVersions: slices.Clone(affected)})
	m.appendBindingAuditLocked("secret.unbound_versions.purge", ref, anchor, affected, audit)
	result := storage.SecretVersionSetResult{AffectedVersions: affected, Revision: revision}
	if m.purgeResultErr != nil {
		return result, m.purgeResultErr
	}
	return result, nil
}

func (m *memStore) setPurgeResultErr(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.purgeResultErr = err
}

func (m *memStore) UpdateSecretAccessTokenHash(_ context.Context, ref domain.Ref, hash []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	row := m.secrets[ref.String()]
	if row == nil {
		return domain.Errorf(domain.ErrNotFound, "secret %s", ref)
	}
	row.record.AccessTokenHash = bytes.Clone(hash)
	return nil
}
