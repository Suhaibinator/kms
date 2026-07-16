package core

import (
	"context"
	"regexp"
	"strconv"
	"time"

	"github.com/Suhaibinator/kms/internal/crypto"
	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/keyutil"
	"github.com/Suhaibinator/kms/internal/policy"
	"github.com/Suhaibinator/kms/internal/storage"
)

// requireAdmin gates purely administrative operations (no per-operation policy
// path). Non-admins are denied and audited.
func (s *Service) requireAdmin(ctx context.Context, pr Principal, eventType, resourceType, name string) error {
	if pr.IsAdmin() {
		return nil
	}
	s.auditName(ctx, pr, eventType, resourceType, name, "deny", nil)
	return domain.Errorf(domain.ErrPermissionDenied, "access denied")
}

// requireAdminOrOp gates a management operation that admins may perform
// unconditionally and other identities may perform only when granted the
// specific admin operation on the resource. Denials are audited.
func (s *Service) requireAdminOrOp(ctx context.Context, pr Principal, operation, eventType, resourceType string, ref domain.Ref) error {
	if pr.IsAdmin() {
		return nil
	}
	policies, err := s.store.PoliciesForSubject(ctx, pr.Identity.Name)
	if err != nil {
		return domain.Errorf(domain.ErrPermissionDenied, "authorization unavailable")
	}
	if !policy.Authorize(policies, pr.home(), operation, ref.NS) {
		s.auditRef(ctx, pr, eventType, resourceType, ref, 0, "deny", map[string]string{"operation": operation})
		return domain.Errorf(domain.ErrPermissionDenied, "access denied")
	}
	return nil
}

// --- namespaces --------------------------------------------------------------

// CreateNamespace registers a namespace (env, app) with a description and the
// set of authentication methods that admit a client into it (default
// mTLS-only). Available to admins, or to identities granted
// admin:namespace:create on the namespace.
func (s *Service) CreateNamespace(ctx context.Context, pr Principal, ref domain.NamespaceRef, description string, methods []domain.AuthMethod) (domain.Namespace, error) {
	if err := keyutil.ValidateNamespace(ref); err != nil {
		return domain.Namespace{}, domain.Errorf(domain.ErrInvalidArgument, "%v", err)
	}
	methods, err := normalizeAuthMethods(methods)
	if err != nil {
		return domain.Namespace{}, err
	}
	if err := s.requireAdminOrOp(ctx, pr, domain.OpAdminNamespaceCreate, "namespace.create", domain.ResourceNamespace, domain.Ref{NS: ref}); err != nil {
		return domain.Namespace{}, err
	}
	ns, err := s.store.CreateNamespace(ctx, domain.Namespace{
		NamespaceRef:       ref,
		Description:        description,
		AllowedAuthMethods: methods,
		CreatedBy:          pr.Identity.Name,
		CreatedAt:          s.now(),
	})
	if err != nil {
		return domain.Namespace{}, err
	}
	s.auditRef(ctx, pr, "namespace.create", domain.ResourceNamespace, domain.Ref{NS: ref}, 0, "allow", nil)
	return ns, nil
}

// UpdateNamespace replaces a namespace's description and allowed auth-method set
// (full replace). Available to admins, or to identities granted
// admin:namespace:update on the namespace.
func (s *Service) UpdateNamespace(ctx context.Context, pr Principal, ref domain.NamespaceRef, description string, methods []domain.AuthMethod) (domain.Namespace, error) {
	if err := keyutil.ValidateNamespace(ref); err != nil {
		return domain.Namespace{}, domain.Errorf(domain.ErrInvalidArgument, "%v", err)
	}
	methods, err := normalizeAuthMethods(methods)
	if err != nil {
		return domain.Namespace{}, err
	}
	if err := s.requireAdminOrOp(ctx, pr, domain.OpAdminNamespaceUpdate, "namespace.update", domain.ResourceNamespace, domain.Ref{NS: ref}); err != nil {
		return domain.Namespace{}, err
	}
	ns, err := s.store.UpdateNamespace(ctx, ref, description, methods)
	if err != nil {
		return domain.Namespace{}, err
	}
	s.auditRef(ctx, pr, "namespace.update", domain.ResourceNamespace, domain.Ref{NS: ref}, 0, "allow", nil)
	return ns, nil
}

// DeleteNamespace removes an empty namespace. Storage verifies emptiness (no
// parameters, secrets, or bound identities) and returns ErrFailedPrecondition
// otherwise. Available to admins, or to identities granted
// admin:namespace:delete on the namespace.
func (s *Service) DeleteNamespace(ctx context.Context, pr Principal, ref domain.NamespaceRef) error {
	if err := keyutil.ValidateNamespace(ref); err != nil {
		return domain.Errorf(domain.ErrInvalidArgument, "%v", err)
	}
	if err := s.requireAdminOrOp(ctx, pr, domain.OpAdminNamespaceDelete, "namespace.delete", domain.ResourceNamespace, domain.Ref{NS: ref}); err != nil {
		return err
	}
	if err := s.store.DeleteNamespace(ctx, ref); err != nil {
		return err
	}
	s.auditRef(ctx, pr, "namespace.delete", domain.ResourceNamespace, domain.Ref{NS: ref}, 0, "allow", nil)
	return nil
}

// ListNamespaces lists namespaces with their parameter/secret counts. Admins
// see all; other identities see only namespaces they can read or list into (via
// policy or the implicit home-namespace grant), so the namespace tree is not a
// recon surface for a narrowly-scoped client.
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
	home := pr.home()
	visibleOps := [...]string{
		domain.OpParameterRead,
		domain.OpParameterList,
		domain.OpSecretRead,
		domain.OpSecretList,
	}
	visible := all[:0]
	for _, ns := range all {
		for _, operation := range visibleOps {
			// Use the full authorization decision so explicit denies override both
			// policy allows and the implicit home-namespace grant.
			if policy.Authorize(policies, home, operation, ns.NamespaceRef) {
				visible = append(visible, ns)
				break
			}
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
	s.auditName(ctx, pr, "policy.write", domain.ResourcePolicy, p.Name, "allow",
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
	s.auditName(ctx, pr, "policy.write", domain.ResourcePolicy, p.Name, "allow",
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
	s.auditName(ctx, pr, "policy.write", domain.ResourcePolicy, name, "allow",
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

// CertBundle is a one-time client-certificate issuance: the leaf certificate
// and its freshly generated private key (returned exactly once, never stored),
// plus identifying metadata.
type CertBundle struct {
	CertPEM     string
	KeyPEM      string
	Serial      string
	Fingerprint string
	NotAfter    time.Time
}

// CreateIdentityInput describes a new identity and the credentials to mint.
type CreateIdentityInput struct {
	Name        string
	Kind        string // domain.IdentityKindClient | domain.IdentityKindAdmin
	Namespace   *domain.NamespaceRef
	AuthMethods []domain.AuthMethod // empty defaults to {mtls}; admin kind always gets a token
	CertTTL     time.Duration       // 0 uses the CA default (90 days)
}

// CreateIdentityResult carries the created identity and any one-time
// credentials (token and/or client-certificate bundle) per the auth methods.
type CreateIdentityResult struct {
	Identity domain.Identity
	// Token is non-empty when a bearer token was minted (auth method "token", or
	// any admin identity). Shown exactly once.
	Token string
	// Cert is non-nil when a client certificate was minted (auth method "mtls").
	// Shown exactly once.
	Cert *CertBundle
}

// CreateIdentity mints a new identity and its credentials. Admin only. A client
// identity may be minted with a bearer token, a client-certificate bundle, or
// both, per AuthMethods (empty defaults to mTLS-only, the strongest posture);
// admin identities always receive a token (the frontend logs in with it).
// Credentials are returned exactly once.
func (s *Service) CreateIdentity(ctx context.Context, pr Principal, in CreateIdentityInput) (CreateIdentityResult, error) {
	if err := s.requireAdmin(ctx, pr, "identity.write", domain.ResourceIdentity, in.Name); err != nil {
		return CreateIdentityResult{}, err
	}
	if !identityNameRe.MatchString(in.Name) {
		return CreateIdentityResult{}, domain.Errorf(domain.ErrInvalidArgument, "invalid identity name %q", in.Name)
	}
	if in.Kind != domain.IdentityKindClient && in.Kind != domain.IdentityKindAdmin {
		return CreateIdentityResult{}, domain.Errorf(domain.ErrInvalidArgument, "identity kind must be %q or %q",
			domain.IdentityKindClient, domain.IdentityKindAdmin)
	}
	if in.Namespace != nil {
		if err := keyutil.ValidateNamespace(*in.Namespace); err != nil {
			return CreateIdentityResult{}, domain.Errorf(domain.ErrInvalidArgument, "%v", err)
		}
	}
	methods, err := normalizeAuthMethods(in.AuthMethods)
	if err != nil {
		return CreateIdentityResult{}, err
	}
	wantToken := in.Kind == domain.IdentityKindAdmin || authMethodAllowed(methods, domain.AuthMethodToken)
	wantCert := authMethodAllowed(methods, domain.AuthMethodMTLS)

	var (
		token string
		hash  []byte
	)
	if wantToken {
		if token, hash, err = crypto.GenerateToken("kms"); err != nil {
			return CreateIdentityResult{}, err
		}
	}

	id, err := s.store.CreateIdentity(ctx, storage.CreateIdentityParams{
		Name:      in.Name,
		Kind:      in.Kind,
		TokenHash: hash,
		Namespace: in.Namespace,
	})
	if err != nil {
		return CreateIdentityResult{}, err
	}

	var bundle *CertBundle
	if wantCert {
		bundle, err = s.issueCert(ctx, in.Name, in.CertTTL)
		if err != nil {
			return CreateIdentityResult{}, err
		}
	}

	meta := map[string]string{"action": "create", "kind": in.Kind}
	s.auditName(ctx, pr, "identity.write", domain.ResourceIdentity, in.Name, "allow", meta)

	// Re-read to return the full admin-facing view (namespace binding, cert
	// summaries). Fall back to the create result if the re-read fails.
	if full, ferr := s.store.GetIdentityByName(ctx, in.Name); ferr == nil {
		id = full
	}
	return CreateIdentityResult{Identity: id, Token: token, Cert: bundle}, nil
}

// guardCertTarget restricts a certificate operation reached via the delegated
// admin:identity:cert op (a non-admin caller) to safe targets. Without it,
// holding that op would be a full privilege escalation: the caller could mint a
// valid cert bundle for an admin identity and then mTLS-authenticate as that
// admin. Non-admin callers may therefore never operate on an admin-kind target,
// and only on identities bound to the caller's own namespace. Admin callers are
// unrestricted. Denials are audited (no key material).
func (s *Service) guardCertTarget(ctx context.Context, pr Principal, eventType string, target domain.Identity) error {
	if pr.IsAdmin() {
		return nil
	}
	deny := func(reason string) error {
		s.auditName(ctx, pr, eventType, domain.ResourceIdentity, target.Name, "deny",
			map[string]string{"operation": domain.OpAdminIdentityCert, "reason": reason})
		return domain.Errorf(domain.ErrPermissionDenied, "access denied")
	}
	if target.Kind == domain.IdentityKindAdmin {
		return deny("admin_target")
	}
	home := pr.home()
	if home == nil || target.Namespace == nil || *home != *target.Namespace {
		return deny("cross_namespace")
	}
	return nil
}

// certAuthzRef scopes the admin:identity:cert authorization to the caller's home
// namespace so a namespace-scoped grant ({admin:identity:cert, env, app}) matches
// — guardCertTarget then confirms the actual target lives in that namespace. An
// unbound non-admin has no home, so it authorizes against an empty-namespace ref
// (matching only wildcard env:*/app:* grants) and is denied by guardCertTarget
// regardless. Authorizing before loading the target avoids leaking identity
// existence to unauthorized callers.
func certAuthzRef(pr Principal, name string) domain.Ref {
	if h := pr.home(); h != nil {
		return domain.Ref{NS: *h, Key: name}
	}
	return domain.Ref{Key: name}
}

// IssueIdentityCertificate mints an additional client certificate for an
// existing identity (renewal/rollover). Available to admins, or to identities
// granted admin:identity:cert (restricted to non-admin targets in the caller's
// own namespace; see guardCertTarget). The private key is returned exactly once.
func (s *Service) IssueIdentityCertificate(ctx context.Context, pr Principal, name string, ttl time.Duration) (*CertBundle, error) {
	if err := s.requireAdminOrOp(ctx, pr, domain.OpAdminIdentityCert, "identity.cert.issue", domain.ResourceIdentity, certAuthzRef(pr, name)); err != nil {
		return nil, err
	}
	id, err := s.store.GetIdentityByName(ctx, name)
	if err != nil {
		return nil, err
	}
	if err := s.guardCertTarget(ctx, pr, "identity.cert.issue", id); err != nil {
		return nil, err
	}
	if id.Disabled {
		return nil, domain.Errorf(domain.ErrFailedPrecondition, "identity %s is disabled", name)
	}
	bundle, err := s.issueCert(ctx, name, ttl)
	if err != nil {
		return nil, err
	}
	s.auditName(ctx, pr, "identity.cert.issue", domain.ResourceIdentity, name, "allow",
		map[string]string{"serial": bundle.Serial})
	return bundle, nil
}

// issueCert mints a certificate via the built-in CA and records it. The caller
// audits. The identity must already exist.
func (s *Service) issueCert(ctx context.Context, name string, ttl time.Duration) (*CertBundle, error) {
	authority := s.ca.Load()
	if authority == nil {
		return nil, domain.Errorf(domain.ErrNotReady, "certificate authority not initialized")
	}
	issued, err := authority.IssueClientCert(name, ttl)
	if err != nil {
		return nil, domain.Errorf(domain.ErrInvalidArgument, "%v", err)
	}
	if err := s.store.InsertIdentityCert(ctx, name, domain.IdentityCert{
		Serial:      issued.Serial,
		Fingerprint: issued.FingerprintSHA256,
		NotAfter:    issued.NotAfter,
		CreatedAt:   s.now(),
	}); err != nil {
		return nil, err
	}
	return &CertBundle{
		CertPEM:     string(issued.CertPEM),
		KeyPEM:      string(issued.KeyPEM),
		Serial:      issued.Serial,
		Fingerprint: issued.FingerprintSHA256,
		NotAfter:    issued.NotAfter,
	}, nil
}

// RevokeIdentityCertificate revokes a single certificate by serial. The serial
// must belong to the named identity. Available to admins, or to identities
// granted admin:identity:cert.
func (s *Service) RevokeIdentityCertificate(ctx context.Context, pr Principal, name, serial string) error {
	if err := s.requireAdminOrOp(ctx, pr, domain.OpAdminIdentityCert, "identity.cert.revoke", domain.ResourceIdentity, certAuthzRef(pr, name)); err != nil {
		return err
	}
	rec, err := s.store.GetIdentityCertBySerial(ctx, serial)
	if err != nil {
		return err
	}
	if rec.IdentityName != name {
		return domain.Errorf(domain.ErrNotFound, "certificate %s for identity %s", serial, name)
	}
	target, err := s.store.GetIdentityByName(ctx, name)
	if err != nil {
		return err
	}
	if err := s.guardCertTarget(ctx, pr, "identity.cert.revoke", target); err != nil {
		return err
	}
	if err := s.store.RevokeIdentityCert(ctx, serial); err != nil {
		return err
	}
	s.auditName(ctx, pr, "identity.cert.revoke", domain.ResourceIdentity, name, "allow",
		map[string]string{"serial": serial})
	return nil
}

// ListIdentities lists identities. Admin only.
func (s *Service) ListIdentities(ctx context.Context, pr Principal, page storage.ListPage) ([]domain.Identity, string, error) {
	if err := s.requireAdmin(ctx, pr, "identity.read", domain.ResourceIdentity, ""); err != nil {
		return nil, "", err
	}
	return s.store.ListIdentities(ctx, page)
}

// RevokeIdentity disables an identity. Admin only. Disabling invalidates all of
// the identity's certificates (checked at mTLS auth time) and its token.
func (s *Service) RevokeIdentity(ctx context.Context, pr Principal, name string) error {
	if err := s.requireAdmin(ctx, pr, "identity.write", domain.ResourceIdentity, name); err != nil {
		return err
	}
	if err := s.store.SetIdentityDisabled(ctx, name, true); err != nil {
		return err
	}
	s.auditName(ctx, pr, "identity.write", domain.ResourceIdentity, name, "allow",
		map[string]string{"action": "revoke"})
	return nil
}

// RotateIdentityToken replaces a token identity's bearer token. Admin only. The
// new token is returned exactly once. Cert-only identities (no existing token)
// are rejected: rotation replaces a token, it does not add one.
func (s *Service) RotateIdentityToken(ctx context.Context, pr Principal, name string) (string, error) {
	if err := s.requireAdmin(ctx, pr, "identity.write", domain.ResourceIdentity, name); err != nil {
		return "", err
	}
	id, err := s.store.GetIdentityByName(ctx, name)
	if err != nil {
		return "", err
	}
	if !id.HasToken {
		return "", domain.Errorf(domain.ErrFailedPrecondition, "identity %s has no token to rotate", name)
	}
	token, hash, err := crypto.GenerateToken("kms")
	if err != nil {
		return "", err
	}
	if err := s.store.UpdateIdentityTokenHash(ctx, name, hash); err != nil {
		return "", err
	}
	s.auditName(ctx, pr, "identity.write", domain.ResourceIdentity, name, "allow",
		map[string]string{"action": "rotate-token"})
	return token, nil
}

// WhoAmI returns the caller's identity description. Callable by any
// authenticated identity with no policy check; it is the SDK's
// namespace-discovery mechanism.
func (s *Service) WhoAmI(_ context.Context, pr Principal) (WhoAmIResult, error) {
	return WhoAmIResult{
		Name:      pr.Identity.Name,
		Kind:      pr.Identity.Kind,
		Namespace: pr.Identity.Namespace,
		Method:    pr.Method,
	}, nil
}

// --- audit / subscribers / keys ------------------------------------------------

// ListAuditEvents queries the audit log. Admin only (or the dedicated
// admin:audit:read operation, scoped to the filter's namespace).
func (s *Service) ListAuditEvents(ctx context.Context, pr Principal, f domain.AuditFilter, page storage.ListPage) ([]domain.AuditEvent, string, error) {
	if !pr.IsAdmin() {
		ref := domain.Ref{NS: domain.NamespaceRef{Env: f.Env, App: f.App}, Key: f.KeyPrefix}
		if err := s.requireAdminOrOp(ctx, pr, domain.OpAdminAuditRead, "audit.read", "audit", ref); err != nil {
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

// RotateKEK rewraps every secret version and the built-in CA key under a fresh
// KEK derived from newMaterial (32 bytes). It is crash-safe: the metadata swap,
// the secret rewraps, and the CA rewrap commit in one storage transaction. Used
// by the rotate-kek CLI command. Admin only.
func (s *Service) RotateKEK(ctx context.Context, pr Principal, newKM domain.KeyMetadata, newMaterial []byte) (secretsRewrapped, caRewrapped int, err error) {
	if err := s.requireAdmin(ctx, pr, "key.rotate", domain.ResourceKey, ""); err != nil {
		return 0, 0, err
	}
	keyring, err := s.requireKeyring()
	if err != nil {
		return 0, 0, err
	}
	defer crypto.Zero(newMaterial)

	newKEK, err := crypto.NewKEKFromMaterial(newKM.ID, newMaterial)
	if err != nil {
		return 0, 0, err
	}
	if newKM.KeyCheck, err = crypto.NewKeyCheck(newKEK); err != nil {
		return 0, 0, err
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

	secretsRewrapped, caRewrapped, err = s.store.RotateKEK(ctx, newKM,
		func(rec storage.SecretVersionRecord) ([]byte, error) {
			oldKEK, kerr := keyring.Get(rec.KEKID)
			if kerr != nil {
				return nil, kerr
			}
			// RewrapDEK only unwraps and re-wraps the opaque outer DEK layer, whose
			// AAD is the stored aad column. For every legitimately written row this
			// equals the AAD recomputed from the row's identity at read time; a row
			// whose stored aad diverges (tampering/corruption) fails closed here and
			// at read — never returning the wrong plaintext.
			return crypto.RewrapDEK(oldKEK, newKEK, rec.EncryptedDEK, rec.AAD)
		},
		func(rec storage.CAKeyRecord) ([]byte, error) {
			oldKEK, kerr := keyring.Get(rec.KEKID)
			if kerr != nil {
				return nil, kerr
			}
			// The CA private key rewrap mirrors the secret DEK rewrap: only the
			// KEK-wrapped DEK layer changes; the DEK-encrypted key material is
			// untouched (no reissue). The AAD binds the CA key's stable id.
			return crypto.RewrapDEK(oldKEK, newKEK, rec.EncryptedDEK, caKeyAAD(rec.ID))
		})
	if err != nil {
		return 0, 0, err
	}
	keyring.SetActive(newKEK)
	s.auditName(ctx, pr, "key.rotate", domain.ResourceKey, newKM.ID, "allow",
		map[string]string{"secrets_rewrapped": strconv.Itoa(secretsRewrapped), "ca_rewrapped": strconv.Itoa(caRewrapped)})
	return secretsRewrapped, caRewrapped, nil
}
