package core

import (
	"context"
	"regexp"
	"strconv"

	"github.com/Suhaibinator/kms/internal/crypto"
	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/pathutil"
	"github.com/Suhaibinator/kms/internal/policy"
	"github.com/Suhaibinator/kms/internal/storage"
)

// requireAdmin gates administrative operations.
func (s *Service) requireAdmin(ctx context.Context, pr Principal, eventType, resourceType, path string) error {
	if pr.IsAdmin() {
		return nil
	}
	s.auditOp(ctx, pr, eventType, resourceType, path, 0, "deny", nil)
	return domain.Errorf(domain.ErrPermissionDenied, "access denied")
}

// --- namespaces --------------------------------------------------------------

// CreateNamespace registers a namespace path.
func (s *Service) CreateNamespace(ctx context.Context, pr Principal, path, description string) (domain.Namespace, error) {
	path, err := pathutil.Normalize(path)
	if err != nil {
		return domain.Namespace{}, domain.Errorf(domain.ErrInvalidArgument, "%v", err)
	}
	// Namespace creation is available to admins, or to clients granted the
	// dedicated admin operation on the path.
	if !pr.IsAdmin() {
		if err := s.authorize(ctx, pr, domain.OpAdminNamespaceCreate, domain.ResourceNamespace, path); err != nil {
			return domain.Namespace{}, err
		}
	}
	ns, err := s.store.CreateNamespace(ctx, domain.Namespace{
		Path:        path,
		Description: description,
		CreatedBy:   pr.Identity.Name,
		CreatedAt:   s.now(),
	})
	if err != nil {
		return domain.Namespace{}, err
	}
	s.auditOp(ctx, pr, "namespace.create", domain.ResourceNamespace, path, 0, "allow", nil)
	return ns, nil
}

// ListNamespaces lists namespaces. Admins see all; other identities see only
// namespaces they have some read/list grant into, so the namespace tree is not
// a recon surface for a narrowly-scoped client.
func (s *Service) ListNamespaces(ctx context.Context, pr Principal, page storage.ListPage) ([]domain.Namespace, string, error) {
	all, next, err := s.store.ListNamespaces(ctx, page)
	if err != nil {
		return nil, "", err
	}
	if pr.IsAdmin() {
		return all, next, nil
	}
	policies, err := s.store.PoliciesForSubject(ctx, pr.Identity.Name)
	if err != nil {
		return nil, "", domain.Errorf(domain.ErrPermissionDenied, "authorization unavailable")
	}
	visible := all[:0]
	for _, ns := range all {
		if policy.MayListUnder(policies, domain.OpParameterRead, ns.Path) ||
			policy.MayListUnder(policies, domain.OpParameterList, ns.Path) ||
			policy.MayListUnder(policies, domain.OpSecretRead, ns.Path) ||
			policy.MayListUnder(policies, domain.OpSecretList, ns.Path) {
			visible = append(visible, ns)
		}
	}
	return visible, next, nil
}

// --- policies ----------------------------------------------------------------

// CreatePolicy validates and stores a policy. Admin only.
func (s *Service) CreatePolicy(ctx context.Context, pr Principal, p domain.Policy) (domain.Policy, error) {
	if err := s.requireAdmin(ctx, pr, "policy.write", domain.ResourcePolicy, p.Name); err != nil {
		return domain.Policy{}, err
	}
	p, err := policy.ValidateRules(p)
	if err != nil {
		return domain.Policy{}, err
	}
	now := s.now()
	p.CreatedAt, p.UpdatedAt = now, now
	out, err := s.store.CreatePolicy(ctx, p)
	if err != nil {
		return domain.Policy{}, err
	}
	s.auditOp(ctx, pr, "policy.write", domain.ResourcePolicy, p.Name, 0, "allow",
		map[string]string{"action": "create", "subject": p.Subject})
	return out, nil
}

// UpdatePolicy replaces a policy by name. Admin only.
func (s *Service) UpdatePolicy(ctx context.Context, pr Principal, p domain.Policy) (domain.Policy, error) {
	if err := s.requireAdmin(ctx, pr, "policy.write", domain.ResourcePolicy, p.Name); err != nil {
		return domain.Policy{}, err
	}
	p, err := policy.ValidateRules(p)
	if err != nil {
		return domain.Policy{}, err
	}
	p.UpdatedAt = s.now()
	out, err := s.store.UpdatePolicy(ctx, p)
	if err != nil {
		return domain.Policy{}, err
	}
	s.auditOp(ctx, pr, "policy.write", domain.ResourcePolicy, p.Name, 0, "allow",
		map[string]string{"action": "update", "subject": p.Subject})
	return out, nil
}

// DeletePolicy removes a policy. Admin only.
func (s *Service) DeletePolicy(ctx context.Context, pr Principal, name string) error {
	if err := s.requireAdmin(ctx, pr, "policy.write", domain.ResourcePolicy, name); err != nil {
		return err
	}
	if err := s.store.DeletePolicy(ctx, name); err != nil {
		return err
	}
	s.auditOp(ctx, pr, "policy.write", domain.ResourcePolicy, name, 0, "allow",
		map[string]string{"action": "delete"})
	return nil
}

// ListPolicies lists policies. Admin only.
func (s *Service) ListPolicies(ctx context.Context, pr Principal, page storage.ListPage) ([]domain.Policy, string, error) {
	if err := s.requireAdmin(ctx, pr, "policy.read", domain.ResourcePolicy, ""); err != nil {
		return nil, "", err
	}
	return s.store.ListPolicies(ctx, page)
}

// --- identities ----------------------------------------------------------------

var identityNameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`)

// CreateIdentity mints a new identity and its bearer token. Admin only.
// The token is returned exactly once.
func (s *Service) CreateIdentity(ctx context.Context, pr Principal, name, kind string) (domain.Identity, string, error) {
	if err := s.requireAdmin(ctx, pr, "identity.write", domain.ResourceIdentity, name); err != nil {
		return domain.Identity{}, "", err
	}
	if !identityNameRe.MatchString(name) {
		return domain.Identity{}, "", domain.Errorf(domain.ErrInvalidArgument, "invalid identity name %q", name)
	}
	if kind != domain.IdentityKindClient && kind != domain.IdentityKindAdmin {
		return domain.Identity{}, "", domain.Errorf(domain.ErrInvalidArgument, "identity kind must be %q or %q",
			domain.IdentityKindClient, domain.IdentityKindAdmin)
	}
	token, hash, err := crypto.GenerateToken("kms")
	if err != nil {
		return domain.Identity{}, "", err
	}
	id, err := s.store.CreateIdentity(ctx, name, kind, hash)
	if err != nil {
		return domain.Identity{}, "", err
	}
	s.auditOp(ctx, pr, "identity.write", domain.ResourceIdentity, name, 0, "allow",
		map[string]string{"action": "create", "kind": kind})
	return id, token, nil
}

// ListIdentities lists identities. Admin only.
func (s *Service) ListIdentities(ctx context.Context, pr Principal, page storage.ListPage) ([]domain.Identity, string, error) {
	if err := s.requireAdmin(ctx, pr, "identity.read", domain.ResourceIdentity, ""); err != nil {
		return nil, "", err
	}
	return s.store.ListIdentities(ctx, page)
}

// RevokeIdentity disables an identity. Admin only.
func (s *Service) RevokeIdentity(ctx context.Context, pr Principal, name string) error {
	if err := s.requireAdmin(ctx, pr, "identity.write", domain.ResourceIdentity, name); err != nil {
		return err
	}
	if err := s.store.SetIdentityDisabled(ctx, name, true); err != nil {
		return err
	}
	s.auditOp(ctx, pr, "identity.write", domain.ResourceIdentity, name, 0, "allow",
		map[string]string{"action": "revoke"})
	return nil
}

// RotateIdentityToken replaces an identity's token. Admin only. The new
// token is returned exactly once.
func (s *Service) RotateIdentityToken(ctx context.Context, pr Principal, name string) (string, error) {
	if err := s.requireAdmin(ctx, pr, "identity.write", domain.ResourceIdentity, name); err != nil {
		return "", err
	}
	token, hash, err := crypto.GenerateToken("kms")
	if err != nil {
		return "", err
	}
	if err := s.store.UpdateIdentityTokenHash(ctx, name, hash); err != nil {
		return "", err
	}
	s.auditOp(ctx, pr, "identity.write", domain.ResourceIdentity, name, 0, "allow",
		map[string]string{"action": "rotate-token"})
	return token, nil
}

// --- audit / subscribers / keys ------------------------------------------------

// ListAuditEvents queries the audit log. Admin only (or the dedicated
// admin:audit:read operation).
func (s *Service) ListAuditEvents(ctx context.Context, pr Principal, f domain.AuditFilter, page storage.ListPage) ([]domain.AuditEvent, string, error) {
	if !pr.IsAdmin() {
		if err := s.authorize(ctx, pr, domain.OpAdminAuditRead, "audit", f.PathPrefix); err != nil {
			return nil, "", err
		}
	}
	return s.store.ListAudit(ctx, f, page)
}

// ListSubscribers returns the live watch registry. Admin only.
func (s *Service) ListSubscribers(ctx context.Context, pr Principal) ([]domain.Subscriber, uint64, error) {
	if err := s.requireAdmin(ctx, pr, "subscribers.read", "subscriber", ""); err != nil {
		return nil, 0, err
	}
	rev, err := s.store.CurrentRevision(ctx)
	if err != nil {
		return nil, 0, err
	}
	return s.getHub().Subscribers(), rev, nil
}

// ListKeyMetadata returns KEK metadata (no key material). Admin only.
func (s *Service) ListKeyMetadata(ctx context.Context, pr Principal) ([]domain.KeyMetadata, error) {
	if err := s.requireAdmin(ctx, pr, "key.read", domain.ResourceKey, ""); err != nil {
		return nil, err
	}
	keys, err := s.store.ListKeyMetadata(ctx)
	if err != nil {
		return nil, err
	}
	// Strip verifier material; callers need only id/source/state/created.
	for i := range keys {
		keys[i].KeyCheck = nil
		keys[i].KDFSalt = nil
	}
	return keys, nil
}

// --- KEK rotation ----------------------------------------------------------------

// RotateKEK rewraps every secret version under a fresh KEK derived from
// newMaterial (32 bytes). It is crash-safe: metadata swap and rewrap commit
// in one storage transaction. Used by the rotate-kek CLI command.
func (s *Service) RotateKEK(ctx context.Context, pr Principal, newKM domain.KeyMetadata, newMaterial []byte) (int, error) {
	if err := s.requireAdmin(ctx, pr, "key.rotate", domain.ResourceKey, ""); err != nil {
		return 0, err
	}
	keyring, err := s.requireKeyring()
	if err != nil {
		return 0, err
	}
	defer crypto.Zero(newMaterial)

	newKEK, err := crypto.NewKEKFromMaterial(newKM.ID, newMaterial)
	if err != nil {
		return 0, err
	}
	if newKM.KeyCheck, err = crypto.NewKeyCheck(newKEK); err != nil {
		return 0, err
	}
	if newKM.CreatedAt.IsZero() {
		newKM.CreatedAt = s.now()
	}
	newKM.State = domain.KeyStateActive

	// Register the new KEK for decryption BEFORE committing the rewrap. The
	// rewrap transaction is atomic, so a concurrent reader sees either all-old
	// or all-new kek_ids; having the new KEK already in the keyring means a
	// reader that observes the post-commit (new) state can resolve it with no
	// window where a freshly-rewrapped row is undecryptable. SetActive (below)
	// only switches the key used to encrypt new writes.
	keyring.Add(newKEK)

	count, err := s.store.RotateKEK(ctx, newKM, func(rec storage.SecretVersionRecord) ([]byte, error) {
		oldKEK, kerr := keyring.Get(rec.KEKID)
		if kerr != nil {
			return nil, kerr
		}
		// RewrapDEK only unwraps and re-wraps the opaque outer DEK layer, whose
		// AAD is the stored aad column here. Read decryption recomputes the AAD
		// from the row's identity (see decryptVersion), but the rewrap callback
		// receives only the version record (no path), so it uses rec.AAD. For
		// every legitimately written row rec.AAD == the recomputed value, so the
		// two agree; a row whose stored aad diverges (tampering/corruption) fails
		// closed at rewrap here and at read in decryptVersion — never returning
		// the wrong plaintext.
		return crypto.RewrapDEK(oldKEK, newKEK, rec.EncryptedDEK, rec.AAD)
	})
	if err != nil {
		return 0, err
	}
	keyring.SetActive(newKEK)
	s.auditOp(ctx, pr, "key.rotate", domain.ResourceKey, newKM.ID, 0, "allow",
		map[string]string{"rewrapped": strconv.Itoa(count)})
	return count, nil
}
