package grpcserver

import (
	"context"
	"encoding/hex"
	"sort"
	"sync"
	"time"

	"github.com/Suhaibinator/kms/internal/crypto"
	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/pathutil"
	"github.com/Suhaibinator/kms/internal/storage"
)

// memStore is an in-memory storage.Store sufficient to drive a real core.Service
// and watch.Hub in tests. It implements the parameter, identity, policy,
// namespace, audit, and change-log surface used by the gRPC handlers; secret
// value storage and KEK rotation are out of scope and panic if reached.
type memStore struct {
	mu sync.Mutex

	pingErr error

	identities map[string]domain.Identity // key: hex(tokenHash)
	policies   []domain.Policy
	namespaces []domain.Namespace
	audit      []domain.AuditEvent

	params map[string]*paramRow

	changelog []domain.ChangeLogEntry
	revision  uint64

	pruneCalls int

	// getParamErr, when set, is returned by GetParameter to exercise error
	// mapping through a real handler path.
	getParamErr error

	clock func() time.Time
}

type paramRow struct {
	value       string
	contentType string
	metadata    string
	createdBy   string
	version     uint64
	createdAt   time.Time
	updatedAt   time.Time
}

func newMemStore() *memStore {
	return &memStore{
		identities: make(map[string]domain.Identity),
		params:     make(map[string]*paramRow),
		clock:      func() time.Time { return time.Now().UTC() },
	}
}

func (m *memStore) addIdentity(name, kind, token string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.identities[hex.EncodeToString(crypto.TokenHash(token))] = domain.Identity{
		Name: name, Kind: kind, CreatedAt: m.clock(),
	}
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

// injectSecretChange appends a secret change-log entry with a poisoned value to
// prove the watch layer never forwards secret values.
func (m *memStore) injectSecretChange(path, changeType string, version uint64) uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.appendChangeLocked(domain.ChangeLogEntry{
		ResourceType: domain.ResourceSecret,
		Path:         path,
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
	id, ok := m.identities[hex.EncodeToString(hash)]
	if !ok {
		return domain.Identity{}, domain.ErrNotFound
	}
	return id, nil
}

func (m *memStore) CreateIdentity(_ context.Context, name, kind string, hash []byte) (domain.Identity, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, id := range m.identities {
		if id.Name == name {
			return domain.Identity{}, domain.Errorf(domain.ErrAlreadyExists, "identity %s", name)
		}
	}
	id := domain.Identity{Name: name, Kind: kind, CreatedAt: m.clock()}
	m.identities[hex.EncodeToString(hash)] = id
	return id, nil
}

func (m *memStore) GetIdentityByName(_ context.Context, name string) (domain.Identity, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, id := range m.identities {
		if id.Name == name {
			return id, nil
		}
	}
	return domain.Identity{}, domain.ErrNotFound
}

func (m *memStore) ListIdentities(_ context.Context, _ storage.ListPage) ([]domain.Identity, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]domain.Identity, 0, len(m.identities))
	for _, id := range m.identities {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, "", nil
}

func (m *memStore) SetIdentityDisabled(_ context.Context, name string, disabled bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, id := range m.identities {
		if id.Name == name {
			id.Disabled = disabled
			m.identities[k] = id
			return nil
		}
	}
	return domain.ErrNotFound
}

func (m *memStore) UpdateIdentityTokenHash(_ context.Context, name string, hash []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, id := range m.identities {
		if id.Name == name {
			delete(m.identities, k)
			m.identities[hex.EncodeToString(hash)] = id
			return nil
		}
	}
	return domain.ErrNotFound
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
	for _, existing := range m.namespaces {
		if existing.Path == ns.Path {
			return domain.Namespace{}, domain.Errorf(domain.ErrAlreadyExists, "namespace %s", ns.Path)
		}
	}
	m.namespaces = append(m.namespaces, ns)
	return ns, nil
}

func (m *memStore) ListNamespaces(_ context.Context, _ storage.ListPage) ([]domain.Namespace, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]domain.Namespace, len(m.namespaces))
	copy(out, m.namespaces)
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

func (m *memStore) PutParameter(_ context.Context, path, value, contentType, metadata, createdBy string) (uint64, uint64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	row := m.params[path]
	if row == nil {
		row = &paramRow{createdAt: m.clock()}
		m.params[path] = row
	}
	row.version++
	row.value = value
	row.contentType = contentType
	row.metadata = metadata
	row.createdBy = createdBy
	row.updatedAt = m.clock()
	rev := m.appendChangeLocked(domain.ChangeLogEntry{
		ResourceType: domain.ResourceParameter,
		Path:         path,
		ChangeType:   domain.ChangePut,
		Value:        value,
		ContentType:  contentType,
		Version:      row.version,
	})
	return row.version, rev, nil
}

func (m *memStore) GetParameter(_ context.Context, path string, _ uint64, _ string) (domain.Parameter, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.getParamErr != nil {
		return domain.Parameter{}, m.getParamErr
	}
	row := m.params[path]
	if row == nil {
		return domain.Parameter{}, domain.Errorf(domain.ErrNotFound, "parameter %s", path)
	}
	return m.paramLocked(path, row), nil
}

func (m *memStore) paramLocked(path string, row *paramRow) domain.Parameter {
	return domain.Parameter{
		Path:        path,
		Value:       row.value,
		ContentType: row.contentType,
		Version:     row.version,
		Metadata:    row.metadata,
		CreatedBy:   row.createdBy,
		CreatedAt:   row.createdAt,
		Labels:      map[string]uint64{domain.LabelCurrent: row.version},
	}
}

func (m *memStore) GetParameterInfo(_ context.Context, path string) (domain.ParameterInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	row := m.params[path]
	if row == nil {
		return domain.ParameterInfo{}, domain.Errorf(domain.ErrNotFound, "parameter %s", path)
	}
	return domain.ParameterInfo{
		Path:        path,
		ContentType: row.contentType,
		Metadata:    row.metadata,
		CreatedAt:   row.createdAt,
		UpdatedAt:   row.updatedAt,
		Labels:      map[string]uint64{domain.LabelCurrent: row.version},
	}, nil
}

func (m *memStore) ListParameters(_ context.Context, prefix string, _ storage.ListPage) ([]domain.Parameter, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []domain.Parameter
	for path, row := range m.params {
		if pathutil.HasPrefix(path, prefix) {
			out = append(out, m.paramLocked(path, row))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, "", nil
}

func (m *memStore) DeleteParameter(_ context.Context, path string) (uint64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.params[path]; !ok {
		return 0, domain.Errorf(domain.ErrNotFound, "parameter %s", path)
	}
	delete(m.params, path)
	return m.appendChangeLocked(domain.ChangeLogEntry{
		ResourceType: domain.ResourceParameter,
		Path:         path,
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

func (m *memStore) SnapshotParameters(_ context.Context, patterns []string) ([]domain.Parameter, uint64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []domain.Parameter
	for path, row := range m.params {
		if patternMatchAny(patterns, path) {
			out = append(out, m.paramLocked(path, row))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, m.revision, nil
}

func patternMatchAny(patterns []string, path string) bool {
	for _, p := range patterns {
		if pathutil.Match(p, path) {
			return true
		}
	}
	return false
}

// --- unused: secret value storage and KEK rotation are out of scope here ---

func (m *memStore) InsertKeyMetadata(context.Context, domain.KeyMetadata) error { panic("unused") }
func (m *memStore) GetKeyMetadata(context.Context, string) (domain.KeyMetadata, error) {
	panic("unused")
}
func (m *memStore) ListKeyMetadata(context.Context) ([]domain.KeyMetadata, error) { panic("unused") }
func (m *memStore) ActiveKeyMetadata(context.Context) (domain.KeyMetadata, error) {
	panic("unused")
}
func (m *memStore) SetKeyState(context.Context, string, string) error { panic("unused") }
func (m *memStore) RotateKEK(context.Context, domain.KeyMetadata, func(storage.SecretVersionRecord) ([]byte, error)) (int, error) {
	panic("unused")
}
func (m *memStore) CreateSecretVersion(context.Context, storage.CreateSecretParams) (uint64, uint64, error) {
	panic("unused")
}
func (m *memStore) GetSecretRecord(context.Context, string) (storage.SecretRecord, error) {
	panic("unused")
}
func (m *memStore) GetSecretVersion(context.Context, string, uint64, string) (storage.SecretRecord, storage.SecretVersionRecord, error) {
	panic("unused")
}
func (m *memStore) GetSecretInfo(context.Context, string) (domain.Secret, error) { panic("unused") }
func (m *memStore) ListSecrets(context.Context, string, storage.ListPage) ([]domain.Secret, string, error) {
	panic("unused")
}
func (m *memStore) DeleteSecret(context.Context, string) (uint64, error) { panic("unused") }
func (m *memStore) SetSecretVersionState(context.Context, string, uint64, string) (uint64, error) {
	panic("unused")
}
func (m *memStore) DestroySecretVersion(context.Context, string, uint64) (uint64, error) {
	panic("unused")
}
func (m *memStore) PromoteSecretVersion(context.Context, string, uint64) (uint64, uint64, uint64, error) {
	panic("unused")
}
func (m *memStore) UpdateSecretAccessTokenHash(context.Context, string, []byte) error {
	panic("unused")
}
