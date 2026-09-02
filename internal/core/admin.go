package core

import (
	"context"
	"errors"
	"regexp"
	"strconv"
	"time"

	"github.com/Suhaibinator/kms/internal/ca"
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
	s.m().AuthzDenied(eventType)
	s.auditName(ctx, pr, eventType, resourceType, name, "deny", nil)
	return domain.Errorf(domain.ErrPermissionDenied, "access denied")
}

// requireAdminOrOp gates a management operation that admins may perform
// unconditionally and other identities may perform only when granted the
// specific admin operation on the resource. Denials are audited.
func (s *Service) requireAdminOrOp(ctx context.Context, pr Principal, operation, eventType, resourceType string, ref domain.Ref) error {
	_, _, err := s.requireAdminOrOpContext(ctx, pr, operation, eventType, resourceType, ref)
	return err
}

// requireAdminOrOpContext preserves the immutable namespace row used for a
// delegated management decision. Namespace update/delete also bind admins so
// the mutation and its audit record cannot cross a delete/recreate ABA window.
func (s *Service) requireAdminOrOpContext(ctx context.Context, pr Principal, operation, eventType, resourceType string, ref domain.Ref) (context.Context, domain.Namespace, error) {
	if pr.IsAdmin() {
		if resourceType == domain.ResourceNamespace && operation != domain.OpAdminNamespaceCreate {
			ns, err := s.store.GetNamespace(ctx, ref.NS)
			if err != nil {
				return ctx, domain.Namespace{}, err
			}
			bound, err := storage.BindNamespaceIncarnation(ctx, ref.NS, ns.ID)
			return bound, ns, err
		}
		return ctx, domain.Namespace{}, nil
	}
	var namespace domain.Namespace
	// Delegated management operations against an existing namespace must obey
	// the same authentication-method boundary as data-plane operations. Create
	// is excluded because the target namespace does not exist yet; partially
	// specified refs (for example audit filters) cannot be gated as one namespace.
	if operation != domain.OpAdminNamespaceCreate && ref.NS.Env != "" && ref.NS.App != "" {
		var err error
		namespace, err = s.namespaceMethodCheck(ctx, pr, ref.NS, resourceType)
		if err != nil {
			return ctx, domain.Namespace{}, err
		}
		ctx, err = storage.BindNamespaceIncarnation(ctx, ref.NS, namespace.ID)
		if err != nil {
			return ctx, domain.Namespace{}, err
		}
	}
	policies, err := s.store.PoliciesForSubject(ctx, pr.Identity.Name)
	if err != nil {
		return ctx, domain.Namespace{}, domain.Errorf(domain.ErrPermissionDenied, "authorization unavailable")
	}
	if !policy.Authorize(policies, pr.home(), operation, ref.NS) {
		s.m().AuthzDenied(operation)
		if namespace.ID != 0 {
			s.auditRefWithNamespaceID(ctx, pr, eventType, resourceType, ref, namespace.ID, 0, "deny", map[string]string{"operation": operation})
		} else {
			s.auditRef(ctx, pr, eventType, resourceType, ref, 0, "deny", map[string]string{"operation": operation})
		}
		return ctx, domain.Namespace{}, domain.Errorf(domain.ErrPermissionDenied, "access denied")
	}
	return ctx, namespace, nil
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
	s.auditRefWithNamespaceID(ctx, pr, "namespace.create", domain.ResourceNamespace, domain.Ref{NS: ref}, ns.ID, 0, "allow", nil)
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
	ctx, authorizedNamespace, err := s.requireAdminOrOpContext(ctx, pr, domain.OpAdminNamespaceUpdate, "namespace.update", domain.ResourceNamespace, domain.Ref{NS: ref})
	if err != nil {
		return domain.Namespace{}, err
	}
	ns, err := s.store.UpdateNamespace(ctx, ref, description, methods)
	if err != nil {
		return domain.Namespace{}, err
	}
	s.auditRefWithNamespaceID(ctx, pr, "namespace.update", domain.ResourceNamespace, domain.Ref{NS: ref}, authorizedNamespace.ID, 0, "allow", nil)
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
	ctx, authorizedNamespace, err := s.requireAdminOrOpContext(ctx, pr, domain.OpAdminNamespaceDelete, "namespace.delete", domain.ResourceNamespace, domain.Ref{NS: ref})
	if err != nil {
		return err
	}
	if err := s.store.DeleteNamespace(ctx, ref); err != nil {
		return err
	}
	s.auditRefWithNamespaceID(ctx, pr, "namespace.delete", domain.ResourceNamespace, domain.Ref{NS: ref}, authorizedNamespace.ID, 0, "allow", nil)
	return nil
}

// ListNamespaces lists namespaces with their parameter/secret counts. Admins
// see all; other identities see only namespaces they can read or list into (via
// policy or the implicit home-namespace grant), so the namespace tree is not a
// recon surface for a narrowly-scoped client.
func (s *Service) ListNamespaces(ctx context.Context, pr Principal, page storage.ListPage) ([]domain.Namespace, string, error) {
	if pr.IsAdmin() {
		return s.store.ListNamespaces(ctx, page)
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
	limit := filteredPageLimit(page.Limit)
	visible := make([]domain.Namespace, 0, limit+1)
	scope := filteredCursorScope(pr, nil)
	cursor, err := s.openFilteredCursor(page.Token, "namespaces", scope)
	if err != nil {
		return nil, "", err
	}
	for batch := 0; batch < maxFilteredScanBatches && len(visible) <= limit; batch++ {
		rows, rawNext, err := s.store.ListNamespaces(ctx, storage.ListPage{Limit: filteredScanBatchSize, Token: cursor})
		if err != nil {
			return nil, "", err
		}
		for _, ns := range rows {
			// Multi-namespace listings filter rather than reject: a caller may be
			// authorized for namespaces that admit different authentication methods,
			// but metadata must not cross that method boundary.
			if !authMethodAllowed(ns.AllowedAuthMethods, pr.Method) {
				continue
			}
			for _, operation := range visibleOps {
				// Use the full authorization decision so explicit denies override both
				// policy allows and the implicit home-namespace grant.
				if policy.Authorize(policies, home, operation, ns.NamespaceRef) {
					visible = append(visible, ns)
					break
				}
			}
			if len(visible) > limit {
				break
			}
		}
		if len(visible) > limit {
			rawVisibleNext := storage.NamespacePageToken(visible[limit-1].NamespaceRef)
			next, err := s.sealFilteredCursor("namespaces", scope, rawVisibleNext)
			if err != nil {
				return nil, "", err
			}
			return visible[:limit], next, nil
		}
		if rawNext == "" {
			return visible, "", nil
		}
		if rawNext == cursor {
			return nil, "", domain.Errorf(domain.ErrFailedPrecondition, "namespace pagination did not advance")
		}
		cursor = rawNext
	}
	next, err := s.sealFilteredCursor("namespaces", scope, cursor)
	if err != nil {
		return nil, "", err
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
	AuthMethods []domain.AuthMethod // client: empty defaults to {mtls}; admin: token only ("mtls" is rejected, see IssueLocalAdminCertificate)
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
// both, per AuthMethods (empty defaults to mTLS-only, the strongest posture).
// An admin identity always receives a token and never a certificate here:
// admin client certificates are minted only by the offline CLI on the server
// host (IssueLocalAdminCertificate), so requesting "mtls" for an admin is an
// error. Credentials are returned exactly once.
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
	var (
		methods []domain.AuthMethod
		err     error
	)
	if in.Kind == domain.IdentityKindAdmin {
		methods, err = normalizeAdminAuthMethods(in.AuthMethods)
	} else {
		methods, err = normalizeAuthMethods(in.AuthMethods)
	}
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

	var (
		bundle  *CertBundle
		certRec *domain.IdentityCert
	)
	if wantCert {
		var generated domain.IdentityCert
		bundle, generated, err = s.generateCert(in.Name, in.CertTTL, ca.KeyEd25519)
		if err != nil {
			return CreateIdentityResult{}, err
		}
		certRec = &generated
	}

	id, err := s.store.CreateIdentity(ctx, storage.CreateIdentityParams{
		Name:      in.Name,
		Kind:      in.Kind,
		TokenHash: hash,
		Namespace: in.Namespace,
		Cert:      certRec,
	})
	if err != nil {
		return CreateIdentityResult{}, err
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
// existing client-kind identity (renewal/rollover). Available to admins, or to
// identities granted admin:identity:cert (restricted to non-admin targets in
// the caller's own namespace; see guardCertTarget). Admin-kind targets are
// refused for every caller, admins included: an admin certificate is the
// management plane's proof of possession and is minted only by the offline CLI
// on the server host (IssueLocalAdminCertificate), so a stolen online admin
// credential cannot mint durable new admin credentials. The private key is
// returned exactly once.
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
	if id.Kind == domain.IdentityKindAdmin {
		s.auditName(ctx, pr, "identity.cert.issue", domain.ResourceIdentity, name, "deny",
			map[string]string{"reason": "admin_target", "channel": "online"})
		return nil, domain.Errorf(domain.ErrPermissionDenied,
			"admin client certificates are issued only offline with 'parameter-store admin-cert issue'")
	}
	if id.Disabled {
		return nil, domain.Errorf(domain.ErrFailedPrecondition, "identity %s is disabled", name)
	}
	bundle, err := s.issueCert(ctx, name, ttl, ca.KeyEd25519)
	if err != nil {
		return nil, err
	}
	s.auditName(ctx, pr, "identity.cert.issue", domain.ResourceIdentity, name, "allow",
		map[string]string{"serial": bundle.Serial})
	return bundle, nil
}

// IssueLocalAdminCertificate mints a client certificate for an admin-kind
// identity. It exists for the offline CLI only (parameter-store admin-cert
// issue), which runs on the server host with direct database and master-key
// access: admin certificates are the management plane's proof of possession,
// so minting one must require host access rather than any online credential.
// It MUST NOT be reachable from any transport handler; IssueIdentityCertificate
// is the online path and refuses admin targets. The leaf key is ECDSA P-256 so
// the credential can be imported into browser and OS keystores. The private
// key is returned exactly once.
func (s *Service) IssueLocalAdminCertificate(ctx context.Context, pr Principal, name string, ttl time.Duration) (*CertBundle, error) {
	if err := s.requireAdmin(ctx, pr, "identity.cert.issue", domain.ResourceIdentity, name); err != nil {
		return nil, err
	}
	id, err := s.store.GetIdentityByName(ctx, name)
	if err != nil {
		return nil, err
	}
	if id.Kind != domain.IdentityKindAdmin {
		return nil, domain.Errorf(domain.ErrInvalidArgument,
			"identity %s is not an admin; client identities receive certificates through the online identity API", name)
	}
	if id.Disabled {
		return nil, domain.Errorf(domain.ErrFailedPrecondition, "identity %s is disabled", name)
	}
	bundle, err := s.issueCert(ctx, name, ttl, ca.KeyECDSAP256)
	if err != nil {
		return nil, err
	}
	s.auditName(ctx, pr, "identity.cert.issue", domain.ResourceIdentity, name, "allow",
		map[string]string{"serial": bundle.Serial, "channel": "local"})
	return bundle, nil
}

// ExpiringAdminCert names an enabled admin whose newest valid client
// certificate expires soon, so an operator can re-issue it before the TLS
// handshake starts rejecting it.
type ExpiringAdminCert struct {
	Name     string
	Serial   string
	NotAfter time.Time
}

// AdminCertReport describes the certificate posture of admin-kind identities:
// lacking lists enabled admins with no currently valid (enrolled, unrevoked,
// unexpired) certificate — they cannot authenticate while the requirement is
// enforced — and expiring lists enabled admins whose newest valid certificate
// expires within `within` (0 disables that check). An expired certificate is
// rejected by the TLS handshake itself, before core can explain anything, so
// the warning has to come ahead of time. serve logs both at startup. For
// trusted in-process callers only: not principal-gated, not exposed by any
// transport.
func (s *Service) AdminCertReport(ctx context.Context, within time.Duration) (lacking []string, expiring []ExpiringAdminCert, err error) {
	now := s.now()
	valid := func(c domain.IdentityCert) bool {
		return c.RevokedAt.IsZero() && (c.NotAfter.IsZero() || now.Before(c.NotAfter))
	}
	token := ""
	for {
		ids, next, lerr := s.store.ListIdentities(ctx, storage.ListPage{Limit: 1000, Token: token})
		if lerr != nil {
			return nil, nil, lerr
		}
		for _, id := range ids {
			if id.Kind != domain.IdentityKindAdmin || id.Disabled {
				continue
			}
			// The newest valid certificate decides: an admin mid-rollover holds
			// an old and a new one, and only the later expiry matters.
			var newest *domain.IdentityCert
			for i := range id.Certs {
				c := &id.Certs[i]
				if !valid(*c) {
					continue
				}
				if newest == nil || c.NotAfter.IsZero() || (!newest.NotAfter.IsZero() && c.NotAfter.After(newest.NotAfter)) {
					newest = c
				}
			}
			switch {
			case newest == nil:
				lacking = append(lacking, id.Name)
			case within > 0 && !newest.NotAfter.IsZero() && newest.NotAfter.Before(now.Add(within)):
				expiring = append(expiring, ExpiringAdminCert{Name: id.Name, Serial: newest.Serial, NotAfter: newest.NotAfter})
			}
		}
		if next == "" || next == token {
			return lacking, expiring, nil
		}
		token = next
	}
}

// AdminsWithoutValidCert lists enabled admin-kind identities that hold no
// currently valid client certificate. It is AdminCertReport without the
// expiry look-ahead.
func (s *Service) AdminsWithoutValidCert(ctx context.Context) ([]string, error) {
	lacking, _, err := s.AdminCertReport(ctx, 0)
	return lacking, err
}

// issueCert mints a certificate via the built-in CA and records it. The caller
// audits. The identity must already exist.
func (s *Service) issueCert(ctx context.Context, name string, ttl time.Duration, alg ca.KeyAlgorithm) (*CertBundle, error) {
	bundle, cert, err := s.generateCert(name, ttl, alg)
	if err != nil {
		return nil, err
	}
	if err := s.store.InsertIdentityCert(ctx, name, cert); err != nil {
		return nil, err
	}
	return bundle, nil
}

// generateCert mints a certificate without touching storage. CreateIdentity
// passes the resulting record into the identity transaction; renewals persist
// it separately after confirming the target identity already exists.
func (s *Service) generateCert(name string, ttl time.Duration, alg ca.KeyAlgorithm) (*CertBundle, domain.IdentityCert, error) {
	authority := s.ca.Load()
	if authority == nil {
		return nil, domain.IdentityCert{}, domain.Errorf(domain.ErrNotReady, "certificate authority not initialized")
	}
	issued, err := authority.IssueClientCertWithOptions(name, ca.IssueOptions{TTL: ttl, Key: alg})
	if err != nil {
		return nil, domain.IdentityCert{}, domain.Errorf(domain.ErrInvalidArgument, "%v", err)
	}
	cert := domain.IdentityCert{
		Serial:      issued.Serial,
		Fingerprint: issued.FingerprintSHA256,
		NotAfter:    issued.NotAfter,
		CreatedAt:   s.now(),
	}
	return &CertBundle{
		CertPEM:     string(issued.CertPEM),
		KeyPEM:      string(issued.KeyPEM),
		Serial:      issued.Serial,
		Fingerprint: issued.FingerprintSHA256,
		NotAfter:    issued.NotAfter,
	}, cert, nil
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
	if pr.IsAdmin() {
		return s.store.ListAudit(ctx, f, page)
	}
	policies, err := s.store.PoliciesForSubject(ctx, pr.Identity.Name)
	if err != nil {
		return nil, "", domain.Errorf(domain.ErrPermissionDenied, "authorization unavailable")
	}

	// A broad or partially specified filter legitimately spans namespaces, so it
	// cannot be method-gated once at authorization time. Filter each namespaced
	// row against the namespace's current method boundary instead. Global audit
	// rows have no namespace boundary and remain visible under the delegated
	// policy grant. History for a deleted namespace fails closed because its
	// allowed methods can no longer be established; admins retain that history.
	type namespaceAccess struct {
		namespace domain.Namespace
		exists    bool
	}
	accessByNamespace := make(map[domain.NamespaceRef]namespaceAccess)
	visibleEvent := func(event domain.AuditEvent) (bool, error) {
		if event.ResourceEnv == "" && event.ResourceApp == "" {
			if !globalAuditEvent(event) {
				return false, nil
			}
			return policy.Authorize(policies, nil, domain.OpAdminAuditRead, domain.NamespaceRef{}), nil
		}
		// A row with only half a namespace cannot be assigned a method or policy
		// boundary and therefore fails closed for delegated callers.
		if event.ResourceEnv == "" || event.ResourceApp == "" {
			return false, nil
		}
		nsRef := domain.NamespaceRef{Env: event.ResourceEnv, App: event.ResourceApp}
		if !policy.Authorize(policies, pr.home(), domain.OpAdminAuditRead, nsRef) {
			return false, nil
		}
		access, known := accessByNamespace[nsRef]
		if !known {
			ns, getErr := s.store.GetNamespace(ctx, nsRef)
			switch {
			case getErr == nil:
				access = namespaceAccess{namespace: ns, exists: true}
			case errors.Is(getErr, domain.ErrNotFound):
				access = namespaceAccess{}
			default:
				return false, getErr
			}
			accessByNamespace[nsRef] = access
		}
		// Bind the row to the immutable namespace incarnation captured at audit
		// time. Legacy/malformed rows with no incarnation ID fail closed.
		return access.exists && authMethodAllowed(access.namespace.AllowedAuthMethods, pr.Method) &&
			event.ResourceNamespaceID != 0 &&
			event.ResourceNamespaceID == access.namespace.ID, nil
	}

	limit := filteredPageLimit(page.Limit)
	visible := make([]domain.AuditEvent, 0, limit+1)
	scope := filteredCursorScope(pr, f)
	cursor, err := s.openFilteredCursor(page.Token, "audit", scope)
	if err != nil {
		return nil, "", err
	}
	for batch := 0; batch < maxFilteredScanBatches && len(visible) <= limit; batch++ {
		events, rawNext, err := s.store.ListAudit(ctx, f, storage.ListPage{Limit: filteredScanBatchSize, Token: cursor})
		if err != nil {
			return nil, "", err
		}
		for _, event := range events {
			allowed, err := visibleEvent(event)
			if err != nil {
				return nil, "", err
			}
			if allowed {
				visible = append(visible, event)
			}
			if len(visible) > limit {
				break
			}
		}
		if len(visible) > limit {
			rawVisibleNext := storage.AuditPageToken(visible[limit-1].ID)
			next, err := s.sealFilteredCursor("audit", scope, rawVisibleNext)
			if err != nil {
				return nil, "", err
			}
			return visible[:limit], next, nil
		}
		if rawNext == "" {
			return visible, "", nil
		}
		if rawNext == cursor {
			return nil, "", domain.Errorf(domain.ErrFailedPrecondition, "audit pagination did not advance")
		}
		cursor = rawNext
	}
	next, err := s.sealFilteredCursor("audit", scope, cursor)
	if err != nil {
		return nil, "", err
	}
	return visible, next, nil
}

func globalAuditEvent(event domain.AuditEvent) bool {
	// A row carrying an immutable namespace ID is namespaced even if malformed
	// or legacy denormalized env/app fields are blank. Never let it inherit a
	// global-resource classification for delegated audit readers.
	if event.ResourceNamespaceID != 0 {
		return false
	}
	if event.EventType == "auth.failure" && event.ResourceType == "" {
		return true
	}
	switch event.ResourceType {
	case domain.ResourcePolicy, domain.ResourceIdentity, domain.ResourceKey,
		domain.ResourceConfigurationSchema, "subscriber", "audit":
		return true
	default:
		return false
	}
}

func filteredPageLimit(limit int) int {
	switch {
	case limit <= 0:
		return 100
	case limit > 1000:
		return 1000
	default:
		return limit
	}
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

	// Serialize the active-key switch with in-process secret writers. The
	// storage transaction also verifies the active key, which fences writers in
	// other processes using the same database.
	s.keyWriteMu.Lock()
	defer s.keyWriteMu.Unlock()

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
