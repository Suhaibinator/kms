// Package core implements the service layer: authentication, authorization,
// audit, and the business logic for parameters, secrets, namespaces,
// policies, and identities. Transport layers (gRPC, HTTP) are thin adapters
// over this package; storage and crypto are its dependencies.
//
// Resources are addressed by domain.Ref (a namespace plus a relative key),
// never by a parsed path string. Every namespaced operation runs, in order:
// argument validation (internal/keyutil), the per-namespace auth-method gate,
// authorization (internal/policy, with the implicit home-namespace
// grant folded in), the storage call, then audit and watch fan-out.
package core

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"

	"github.com/Suhaibinator/kms/internal/ca"
	"github.com/Suhaibinator/kms/internal/crypto"
	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/policy"
	"github.com/Suhaibinator/kms/internal/storage"
)

// Hub is the watch fan-out. The implementation (internal/watch) tails the
// change log; core only pokes it after committed writes and queries the
// registry for the admin API.
type Hub interface {
	// Wake tells the hub new change-log entries may exist. It must be cheap,
	// non-blocking, and safe to call concurrently.
	Wake()
	// Subscribers returns the live subscriber registry.
	Subscribers() []domain.Subscriber
}

// noopHub is used until (or unless) a real hub is attached.
type noopHub struct{}

func (noopHub) Wake()                            {}
func (noopHub) Subscribers() []domain.Subscriber { return nil }

// Principal is the authenticated caller plus request context. Transports build
// it via Service.ResolvePrincipal, which verifies the presented bearer token
// and/or client certificate and enforces the admin client-certificate
// requirement, and pass it to every operation.
type Principal struct {
	Identity domain.Identity
	// Method is how the caller proved its identity (token or mTLS). The
	// transport sets it; the per-namespace auth-method gate enforces it for
	// client-kind identities.
	Method domain.AuthMethod
	// Token is the identity bearer token the caller presented, retained only so
	// long-lived streams can re-authenticate periodically (see
	// ReauthorizeWatch). Set for token callers and for callers that presented a
	// valid token alongside a client certificate; an admin admitted under the
	// client-certificate requirement always carries it. Never logged or
	// persisted.
	Token string
	// Serial is the serial of the client certificate an mTLS caller presented
	// (empty for token callers). It lets long-lived mTLS streams be re-validated
	// against the specific certificate, so revoking one serial tears the stream
	// down (see ReauthorizeWatch). Transports set it alongside Fingerprint via
	// CertSerial and CertFingerprint.
	Serial string
	// Fingerprint is the lowercase SHA-256 fingerprint of the exact client leaf
	// certificate an mTLS caller presented. Together with Serial it binds
	// long-lived reauthorization to the enrolled certificate rather than merely
	// to issuer-scoped serial and SAN claims. Empty for token callers.
	Fingerprint string
	// SecretToken is the optional per-secret access token supplied with the
	// request (x-kms-secret-token). Never logged, never persisted.
	SecretToken string
	RemoteAddr  string
	UserAgent   string
	RequestID   string
}

// IsAdmin reports whether the principal has the admin kind. Admin-kind
// identities are the management plane: they bypass the per-namespace
// auth-method gate and data-plane policy, but not audit or client-bound
// cryptography. Instead of the namespace gate they are subject to the global
// admin client-certificate requirement, enforced when the principal is built
// (see ResolvePrincipal and admitAdmin), so a principal that reaches an
// operation has already satisfied it.
func (p Principal) IsAdmin() bool { return p.Identity.Kind == domain.IdentityKindAdmin }

// home returns the caller's bound namespace (nil when unbound), passed to
// policy.Authorize as the implicit home-namespace grant.
func (p Principal) home() *domain.NamespaceRef { return p.Identity.Namespace }

// WhoAmIResult is the identity self-description returned by WhoAmI. It is the
// SDK's namespace-discovery mechanism.
type WhoAmIResult struct {
	Name      string
	Kind      string
	Namespace *domain.NamespaceRef
	Method    domain.AuthMethod
}

// Service wires storage, crypto, watch, the built-in CA, and audit together.
type Service struct {
	store        storage.Store
	keyring      atomic.Pointer[crypto.Keyring]
	keyWriteMu   sync.RWMutex
	hub          atomic.Pointer[Hub]
	ca           atomic.Pointer[ca.CA]
	auditEnabled atomic.Bool
	// adminRequireClientCert is the effective admin client-certificate
	// requirement (security.admin_require_client_cert, relaxed by serve when
	// TLS is off). On by default so in-process consumers keep the secure
	// behavior unless they explicitly opt out. See admitAdmin.
	adminRequireClientCert atomic.Bool
	log                    *zap.Logger
	version                string
	now                    func() time.Time
	// releaseNotify fans out "subscriber state changed" wakeups to the
	// console's live rollout streams.
	releaseNotify *releaseSubscriberNotifier
	// filteredPageKey encrypts continuation state for authorization-filtered
	// listings. Raw storage cursors can contain hidden namespace names and must
	// never be returned to delegated callers.
	filteredPageKey [32]byte
	// verifyLimits holds the per-identity request and mismatch budgets for
	// VerifyReleaseDefaults (see release_verify.go). Process-local.
	verifyLimits atomic.Pointer[verifyLimiters]
}

// New constructs a Service. The keyring is attached later via SetKeyring
// (after unseal); until then the service reports not-ready and refuses secret
// operations. The built-in CA is bootstrapped via BootstrapCA once the keyring
// is present.
func New(store storage.Store, logger *zap.Logger, version string) *Service {
	if logger == nil {
		logger = zap.NewNop()
	}
	s := &Service{store: store, log: logger, version: version, now: func() time.Time { return time.Now().UTC() }, filteredPageKey: mustNewFilteredPageKey(), releaseNotify: newReleaseSubscriberNotifier()}
	s.auditEnabled.Store(true)
	s.adminRequireClientCert.Store(true)
	s.verifyLimits.Store(newVerifyLimiters(DefaultVerifyDefaultsLimits()))
	var h Hub = noopHub{}
	s.hub.Store(&h)
	return s
}

// SetKeyring attaches the verified keyring. The service becomes ready.
func (s *Service) SetKeyring(k *crypto.Keyring) { s.keyring.Store(k) }

// SetHub attaches the watch hub.
func (s *Service) SetHub(h Hub) { s.hub.Store(&h) }

// SetAuditEnabled controls whether audit events are persisted. Auditing is on
// by default so non-server consumers retain the secure behavior unless they
// explicitly opt out through configuration.
func (s *Service) SetAuditEnabled(enabled bool) { s.auditEnabled.Store(enabled) }

// SetAdminRequireClientCert sets the effective admin client-certificate
// requirement: while true, an admin-kind identity is admitted only when it
// presents a valid client certificate AND its bearer token (see admitAdmin).
// serve passes the configured value, forced to false when TLS is disabled
// because no client certificate can be presented without it.
func (s *Service) SetAdminRequireClientCert(required bool) {
	s.adminRequireClientCert.Store(required)
}

// AdminRequireClientCert reports the effective admin client-certificate
// requirement.
func (s *Service) AdminRequireClientCert() bool { return s.adminRequireClientCert.Load() }

// Store exposes the underlying store to trusted in-process consumers
// (watch hub snapshot/replay, CLI). Transport layers must not use it.
func (s *Service) Store() storage.Store { return s.store }

// Logger returns the service logger.
func (s *Service) Logger() *zap.Logger { return s.log }

// Version returns the build version string.
func (s *Service) Version() string { return s.version }

func (s *Service) getHub() Hub { return *s.hub.Load() }

// Ready reports whether the service can serve: store reachable and master
// key acquired + verified.
func (s *Service) Ready(ctx context.Context) error {
	if s.keyring.Load() == nil {
		return domain.Errorf(domain.ErrNotReady, "master key not acquired")
	}
	if err := s.store.Ping(ctx); err != nil {
		return domain.Errorf(domain.ErrNotReady, "database unreachable")
	}
	return nil
}

// CurrentRevision returns the latest change-log revision.
func (s *Service) CurrentRevision(ctx context.Context) (uint64, error) {
	return s.store.CurrentRevision(ctx)
}

func (s *Service) requireKeyring() (*crypto.Keyring, error) {
	k := s.keyring.Load()
	if k == nil {
		return nil, domain.Errorf(domain.ErrNotReady, "master key not acquired")
	}
	return k, nil
}

// --- built-in CA -----------------------------------------------------------

// BootstrapCA prepares the built-in CA after unseal. On first call for a fresh
// store it generates a CA, wraps its private key under the active KEK (same
// envelope discipline as a secret DEK), and persists it; on subsequent starts
// it loads and decrypts the stored CA. Idempotent across restarts.
func (s *Service) BootstrapCA(ctx context.Context) error {
	keyring, err := s.requireKeyring()
	if err != nil {
		return err
	}
	rec, err := s.store.ActiveCAKey(ctx)
	if errors.Is(err, domain.ErrNotFound) {
		return s.generateCA(ctx, keyring)
	}
	if err != nil {
		return err
	}
	kek, err := keyring.Get(rec.KEKID)
	if err != nil {
		return err
	}
	keyPEM, err := unwrapCAKey(kek, rec)
	if err != nil {
		return err
	}
	defer crypto.Zero(keyPEM)
	authority, err := ca.Load([]byte(rec.CertPEM), keyPEM)
	if err != nil {
		return err
	}
	s.ca.Store(authority)
	return nil
}

func (s *Service) generateCA(ctx context.Context, keyring *crypto.Keyring) error {
	authority, certPEM, keyPEM, err := ca.Generate()
	if err != nil {
		return err
	}
	defer crypto.Zero(keyPEM)
	id, err := newCAKeyID()
	if err != nil {
		return err
	}
	kek := keyring.Active()
	encKey, encDEK, err := wrapCAKey(kek, id, keyPEM)
	if err != nil {
		return err
	}
	if err := s.store.InsertCAKey(ctx, storage.CAKeyRecord{
		ID:           id,
		CertPEM:      string(certPEM),
		EncryptedKey: encKey,
		EncryptedDEK: encDEK,
		KEKID:        kek.ID,
		State:        domain.KeyStateActive,
		CreatedAt:    s.now(),
	}); err != nil {
		return err
	}
	s.ca.Store(authority)
	return nil
}

// CACertPEM returns the PEM-encoded built-in CA certificate (public; served
// unauthenticated at GET /api/v1/ca). It errors if the CA is not bootstrapped.
func (s *Service) CACertPEM() ([]byte, error) {
	authority := s.ca.Load()
	if authority == nil {
		return nil, domain.Errorf(domain.ErrNotReady, "certificate authority not initialized")
	}
	return authority.CertPEM(), nil
}

// CACertPool returns a pool containing only the built-in CA, for the listeners'
// client-CA set (operators AddCert their own client CA to it). It errors if the
// CA is not bootstrapped.
func (s *Service) CACertPool() (*x509.CertPool, error) {
	authority := s.ca.Load()
	if authority == nil {
		return nil, domain.Errorf(domain.ErrNotReady, "certificate authority not initialized")
	}
	return authority.CertPool(), nil
}

// CACertificate returns the parsed built-in CA certificate for inclusion in a
// listener's client-CA pool. It errors if the CA is not bootstrapped.
func (s *Service) CACertificate() (*x509.Certificate, error) {
	authority := s.ca.Load()
	if authority == nil {
		return nil, domain.Errorf(domain.ErrNotReady, "certificate authority not initialized")
	}
	return authority.Certificate(), nil
}

// --- authentication --------------------------------------------------------

// Authenticate resolves a bearer token to an identity. Failures are generic:
// they never reveal whether the token was close, or whether an identity
// exists. An audit event is emitted for failures.
func (s *Service) Authenticate(ctx context.Context, token, remoteAddr, userAgent string) (domain.Identity, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return domain.Identity{}, domain.Errorf(domain.ErrUnauthenticated, "missing credentials")
	}
	id, err := s.store.GetIdentityByTokenHash(ctx, crypto.TokenHash(token))
	if err != nil || id.Disabled {
		s.audit(ctx, domain.AuditEvent{
			EventType: "auth.failure",
			ActorType: "unknown",
			Decision:  "deny",
			SourceIP:  remoteAddr,
			UserAgent: userAgent,
			Metadata:  encodeMeta(map[string]string{"method": string(domain.AuthMethodToken)}),
		})
		return domain.Identity{}, domain.Errorf(domain.ErrUnauthenticated, "invalid credentials")
	}
	return id, nil
}

// VerifyClientCert maps a verified peer certificate to an identity for mTLS
// authentication. The TLS layer has already checked the chain against the
// configured client-CA pool; this method enforces the KMS-specific claims:
// exactly one kms://identity/<name> SAN, an exact fingerprint match to the
// enrolled non-revoked/non-expired certificate, and an enabled identity.
// Failures are generic and audited.
func (s *Service) VerifyClientCert(ctx context.Context, cert *x509.Certificate, remoteAddr, userAgent string) (domain.Identity, error) {
	name, err := ca.IdentityFromCert(cert)
	if err != nil {
		return domain.Identity{}, s.mtlsAuthFailure(ctx, remoteAddr, userAgent)
	}
	serial := CertSerial(cert)
	fingerprint := CertFingerprint(cert)
	rec, err := s.store.GetIdentityCertBySerial(ctx, serial)
	if err != nil {
		return domain.Identity{}, s.mtlsAuthFailure(ctx, remoteAddr, userAgent)
	}
	if fingerprint == "" || rec.Cert.Fingerprint != fingerprint ||
		rec.IdentityName != name || rec.IdentityDisabled ||
		!rec.Cert.RevokedAt.IsZero() ||
		(!rec.Cert.NotAfter.IsZero() && s.now().After(rec.Cert.NotAfter)) {
		return domain.Identity{}, s.mtlsAuthFailure(ctx, remoteAddr, userAgent)
	}
	id, err := s.store.GetIdentityByName(ctx, name)
	if err != nil || id.Disabled {
		return domain.Identity{}, s.mtlsAuthFailure(ctx, remoteAddr, userAgent)
	}
	return id, nil
}

func (s *Service) mtlsAuthFailure(ctx context.Context, remoteAddr, userAgent string) error {
	s.audit(ctx, domain.AuditEvent{
		EventType: "auth.failure",
		ActorType: "unknown",
		Decision:  "deny",
		SourceIP:  remoteAddr,
		UserAgent: userAgent,
		Metadata:  encodeMeta(map[string]string{"method": string(domain.AuthMethodMTLS)}),
	})
	return domain.Errorf(domain.ErrUnauthenticated, "invalid credentials")
}

// CertSerial renders a client certificate's serial the same way internal/ca
// does (lowercase hex, no leading "0x"), so lookups by serial match the stored
// form. Transports use it to populate Principal.Serial for mTLS callers.
func CertSerial(cert *x509.Certificate) string {
	if cert == nil || cert.SerialNumber == nil {
		return ""
	}
	return strings.ToLower(cert.SerialNumber.Text(16))
}

// CertFingerprint returns the lowercase SHA-256 fingerprint of the exact DER
// leaf certificate. It matches the representation persisted at enrollment.
func CertFingerprint(cert *x509.Certificate) string {
	if cert == nil || len(cert.Raw) == 0 {
		return ""
	}
	sum := sha256.Sum256(cert.Raw)
	return hex.EncodeToString(sum[:])
}

// --- namespace auth-method gate --------------------------------------------

// namespaceMethodCheck returns the exact immutable namespace row whose method
// policy was checked. Keeping that row identity lets every later storage call
// and audit event remain bound to the same namespace incarnation even if
// another process deletes and recreates the (env, app) name mid-request.
func (s *Service) namespaceMethodCheck(ctx context.Context, pr Principal, ns domain.NamespaceRef, resourceType string) (domain.Namespace, error) {
	n, err := s.store.GetNamespace(ctx, ns)
	if err != nil {
		return domain.Namespace{}, err
	}
	if expectedID, ok := storage.ExpectedNamespaceIncarnation(ctx, ns); ok && expectedID != n.ID {
		return domain.Namespace{}, domain.Errorf(domain.ErrAborted, "namespace %s changed during request; retry", ns)
	}
	if pr.IsAdmin() || authMethodAllowed(n.AllowedAuthMethods, pr.Method) {
		return n, nil
	}
	allowed := make([]string, len(n.AllowedAuthMethods))
	for i, m := range n.AllowedAuthMethods {
		allowed[i] = string(m)
	}
	s.auditRefWithNamespaceID(ctx, pr, "authz.method_denied", resourceType, domain.Ref{NS: ns}, n.ID, 0, "deny",
		map[string]string{"method": string(pr.Method), "required": strings.Join(allowed, ",")})
	return domain.Namespace{}, domain.Errorf(domain.ErrPermissionDenied,
		"namespace %s requires %s", ns, strings.Join(allowed, " or "))
}

// namespaceMethodGate preserves the error-only form used by management and
// watch reauthorization paths.
func (s *Service) namespaceMethodGate(ctx context.Context, pr Principal, ns domain.NamespaceRef, resourceType string) error {
	_, err := s.namespaceMethodCheck(ctx, pr, ns, resourceType)
	return err
}

// namespaceAuthorizationContext performs the method check and binds subsequent
// storage work to the exact row that was observed. Admins still bypass the
// namespace method policy. For compatibility with existing admin semantics, a
// missing namespace is left for the addressed storage operation to report; no
// row was authorized in that case, so there is no incarnation to bind.
func (s *Service) namespaceAuthorizationContext(ctx context.Context, pr Principal, ns domain.NamespaceRef, resourceType string) (context.Context, domain.Namespace, error) {
	n, err := s.namespaceMethodCheck(ctx, pr, ns, resourceType)
	if err != nil {
		if pr.IsAdmin() && errors.Is(err, domain.ErrNotFound) {
			return ctx, domain.Namespace{}, nil
		}
		return ctx, domain.Namespace{}, err
	}
	bound, err := storage.BindNamespaceIncarnation(ctx, ns, n.ID)
	if err != nil {
		return ctx, domain.Namespace{}, err
	}
	return bound, n, nil
}

// --- authorization ---------------------------------------------------------

// authorize enforces the per-namespace method gate and then policy for one
// data-plane operation on ref. Admin identities are authorized for everything
// (and skip the method restriction). It returns a context pinned to the exact
// namespace row used for the decision. Denials are audited.
func (s *Service) authorize(ctx context.Context, pr Principal, operation, resourceType string, ref domain.Ref) (context.Context, domain.Namespace, error) {
	bound, n, err := s.namespaceAuthorizationContext(ctx, pr, ref.NS, resourceType)
	if err != nil {
		return ctx, domain.Namespace{}, err
	}
	if pr.IsAdmin() {
		return bound, n, nil
	}
	policies, err := s.store.PoliciesForSubject(bound, pr.Identity.Name)
	if err != nil {
		return ctx, domain.Namespace{}, domain.Errorf(domain.ErrPermissionDenied, "authorization unavailable")
	}
	if !policy.Authorize(policies, pr.home(), operation, ref.NS) {
		s.auditRefWithNamespaceID(bound, pr, "authz.denial", resourceType, ref, n.ID, 0, "deny", map[string]string{"operation": operation})
		return ctx, domain.Namespace{}, domain.Errorf(domain.ErrPermissionDenied, "access denied")
	}
	return bound, n, nil
}

// listFilter enforces the method gate over the list namespace, authorizes the
// enumeration with the full deny-aware policy decision, and returns a per-item
// predicate. Enumerating the
// namespace requires listOp; each item is included only if the caller is
// authorized for one of itemOps on the namespace. Admins pass everything through.
//
// Authorization is namespace-level, so the per-item predicate is constant across
// a namespace; it is still applied per item to keep the "list reveals what read
// denies" gap closed at the operation level: parameter listings return values
// inline, so they pass only parameter:read as the item op; secret listings
// return metadata only, so they pass secret:list and secret:read.
func (s *Service) listFilter(ctx context.Context, pr Principal, resourceType, listOp string, ns domain.NamespaceRef, itemOps ...string) (context.Context, domain.Namespace, func(domain.Ref) bool, error) {
	bound, n, err := s.namespaceAuthorizationContext(ctx, pr, ns, resourceType)
	if err != nil {
		return ctx, domain.Namespace{}, nil, err
	}
	if pr.IsAdmin() {
		return bound, n, func(domain.Ref) bool { return true }, nil
	}
	policies, err := s.store.PoliciesForSubject(bound, pr.Identity.Name)
	if err != nil {
		return ctx, domain.Namespace{}, nil, domain.Errorf(domain.ErrPermissionDenied, "authorization unavailable")
	}
	home := pr.home()
	// Use the full authorization decision here: explicit denies must override
	// both policy allows and the implicit home-namespace grant.
	if !policy.Authorize(policies, home, listOp, ns) {
		s.auditRefWithNamespaceID(bound, pr, "authz.denial", resourceType, domain.Ref{NS: ns}, n.ID, 0, "deny",
			map[string]string{"operation": listOp})
		return ctx, domain.Namespace{}, nil, domain.Errorf(domain.ErrPermissionDenied, "access denied")
	}
	return bound, n, func(ref domain.Ref) bool {
		for _, op := range itemOps {
			if policy.Authorize(policies, home, op, ref.NS) {
				return true
			}
		}
		return false
	}, nil
}

// --- watch plumbing --------------------------------------------------------

// AuthorizeSubscribe checks that pr may register a watch over every requested
// namespace: the namespace auth-method gate plus namespace-level read
// authorization (the implicit home grant covers the caller's own namespace).
// Authorization is all-or-nothing per namespace and checked once here; the watch
// hub performs no per-event filtering, so an admitted subscriber receives every
// change in each authorized namespace.
func (s *Service) AuthorizeSubscribe(ctx context.Context, pr Principal, namespaces []domain.NamespaceRef) error {
	_, err := s.AuthorizeSubscribeContext(ctx, pr, namespaces)
	return err
}

// AuthorizeSubscribeContext is the context-preserving form used by transports.
// The returned context pins every subscribed namespace incarnation through the
// initial snapshot and subsequent heartbeat reauthorization.
func (s *Service) AuthorizeSubscribeContext(ctx context.Context, pr Principal, namespaces []domain.NamespaceRef) (context.Context, error) {
	var policies []domain.Policy
	var err error
	if !pr.IsAdmin() {
		policies, err = s.store.PoliciesForSubject(ctx, pr.Identity.Name)
	}
	if err != nil {
		return ctx, domain.Errorf(domain.ErrPermissionDenied, "authorization unavailable")
	}
	home := pr.home()
	for _, ns := range namespaces {
		bound, n, err := s.namespaceAuthorizationContext(ctx, pr, ns, domain.ResourceParameter)
		if err != nil {
			return ctx, err
		}
		// A live watch cannot safely follow a name that has no current row: there
		// is no immutable incarnation to bind, so a later create could silently
		// turn the registration into access to a namespace that was never checked.
		// Admins retain their ordinary data-plane missing-namespace behavior, but
		// registration itself requires a concrete namespace just like non-admins.
		if n.ID == 0 {
			return ctx, domain.Errorf(domain.ErrNotFound, "namespace %s", ns)
		}
		ctx = bound
		if !pr.IsAdmin() && !policy.Authorize(policies, home, domain.OpParameterRead, ns) {
			s.auditRefWithNamespaceID(ctx, pr, "authz.denial", domain.ResourceParameter, domain.Ref{NS: ns}, n.ID, 0, "deny",
				map[string]string{"operation": "subscribe"})
			return ctx, domain.Errorf(domain.ErrPermissionDenied, "access denied")
		}
	}
	return ctx, nil
}

// ReauthorizeWatch re-validates a live stream's credential and re-runs the full
// subscribe-time authorization for every subscribed namespace. The watch handler
// calls it on every heartbeat tick and closes the stream on error, so revocation
// takes effect within one heartbeat interval rather than waiting for a reconnect.
//
// Credential re-check: for token streams it re-authenticates the bearer token
// itself, so rotating or revoking a token drops the stream (ErrUnauthenticated).
// For mTLS streams it re-checks that the identity is still enabled AND that the
// exact presenting certificate (serial plus fingerprint) is still enrolled and
// valid, so revoking a single cert drops the stream. Any transport that builds
// an mTLS Principal MUST populate Serial and Fingerprint; missing either fails
// reauthorization closed. An admin stream admitted under the client-certificate
// requirement presented both credentials, so its bearer token is re-checked as
// well: rotating the admin token drops the stream just as revoking the
// certificate does. Non-admin callers that happened to present both are only
// re-checked on the certificate, the credential that admitted them.
//
// Authorization re-check: for each subscribed namespace it re-runs the same
// per-namespace method gate AND namespace-level policy check (home grant folded
// in) that AuthorizeSubscribe applies at subscribe time. So tightening a
// namespace's allowed methods, or revoking a client's explicit grant to a
// namespace, drops the stream on the next heartbeat (ErrPermissionDenied), while
// a home-namespace subscriber keeps its implicit grant across policy changes.
// This is namespace-level and cheap (one policy read plus a check per subscribed
// namespace per heartbeat), not the per-event predicate that was removed. Admins
// bypass method/policy restrictions, but still re-check that each context-bound
// namespace incarnation exists so a delete/recreate closes a stale stream.
// Callers that pass no namespaces get credential re-validation only.
func (s *Service) ReauthorizeWatch(ctx context.Context, pr Principal, namespaces ...domain.NamespaceRef) error {
	switch pr.Method {
	case domain.AuthMethodToken:
		if err := s.reauthToken(ctx, pr); err != nil {
			return err
		}
	case domain.AuthMethodMTLS:
		id, err := s.store.GetIdentityByName(ctx, pr.Identity.Name)
		if err != nil || id.Disabled || id.Kind != pr.Identity.Kind {
			return domain.Errorf(domain.ErrUnauthenticated, "invalid credentials")
		}
		// Re-validate the specific enrolled certificate: a revoked/expired serial
		// or a different leaf carrying the same issuer-scoped serial and SAN must
		// tear the stream down. Any transport that builds an mTLS Principal MUST
		// populate Serial and Fingerprint. Missing either binding is invalid.
		if pr.Serial == "" || pr.Fingerprint == "" {
			return domain.Errorf(domain.ErrUnauthenticated, "invalid credentials")
		}
		rec, cerr := s.store.GetIdentityCertBySerial(ctx, pr.Serial)
		if cerr != nil || rec.Cert.Fingerprint != pr.Fingerprint ||
			rec.IdentityName != pr.Identity.Name || rec.IdentityDisabled ||
			!rec.Cert.RevokedAt.IsZero() ||
			(!rec.Cert.NotAfter.IsZero() && s.now().After(rec.Cert.NotAfter)) {
			return domain.Errorf(domain.ErrUnauthenticated, "invalid credentials")
		}
		if pr.IsAdmin() && s.adminRequireClientCert.Load() {
			if err := s.admitAdmin(ctx, pr); err != nil {
				return err
			}
			if err := s.reauthToken(ctx, pr); err != nil {
				return err
			}
		}
	default:
		return domain.Errorf(domain.ErrUnauthenticated, "invalid credentials")
	}
	// Re-run the FULL per-namespace authorization (method gate + policy) for each
	// subscribed namespace, mirroring AuthorizeSubscribe, so a live stream is torn
	// down promptly when a namespace is replaced, its allowed methods are
	// tightened, or the caller's grant is revoked. Admins bypass method/policy
	// restrictions but not the exact-row existence check in namespaceMethodCheck.
	var policies []domain.Policy
	if !pr.IsAdmin() {
		var perr error
		policies, perr = s.store.PoliciesForSubject(ctx, pr.Identity.Name)
		if perr != nil {
			return domain.Errorf(domain.ErrPermissionDenied, "authorization unavailable")
		}
	}
	home := pr.home()
	for _, ns := range namespaces {
		if err := s.namespaceMethodGate(ctx, pr, ns, domain.ResourceParameter); err != nil {
			return err
		}
		if !pr.IsAdmin() && !policy.Authorize(policies, home, domain.OpParameterRead, ns) {
			return domain.Errorf(domain.ErrPermissionDenied, "access denied")
		}
	}
	return nil
}

// reauthToken re-authenticates a retained bearer token and requires it to still
// resolve to the principal's identity (same name and kind).
func (s *Service) reauthToken(ctx context.Context, pr Principal) error {
	id, err := s.Authenticate(ctx, pr.Token, pr.RemoteAddr, pr.UserAgent)
	if err != nil || id.Name != pr.Identity.Name || id.Kind != pr.Identity.Kind {
		return domain.Errorf(domain.ErrUnauthenticated, "invalid credentials")
	}
	return nil
}

// --- audit -----------------------------------------------------------------

// audit appends an audit event. Failures are logged loudly but never carry
// secret material. Callers that must fail closed use auditStrict.
func (s *Service) audit(ctx context.Context, ev domain.AuditEvent) {
	if err := s.appendAudit(ctx, ev); err != nil {
		s.log.Error("audit append failed", zap.String("event_type", ev.EventType), zap.Error(err))
	}
}

// auditStrict appends an audit event and reports failure so security-critical
// reads can fail closed when auditing is impossible.
func (s *Service) auditStrict(ctx context.Context, ev domain.AuditEvent) error {
	if err := s.appendAudit(ctx, ev); err != nil {
		s.log.Error("audit append failed (failing operation closed)",
			zap.String("event_type", ev.EventType), zap.Error(err))
		return domain.Errorf(domain.ErrFailedPrecondition, "audit unavailable")
	}
	return nil
}

func (s *Service) appendAudit(ctx context.Context, ev domain.AuditEvent) error {
	if !s.auditEnabled.Load() {
		return nil
	}
	if ev.CreatedAt.IsZero() {
		ev.CreatedAt = s.now()
	}
	if ev.Metadata == "" {
		ev.Metadata = "{}"
	}
	// Audit writes must survive request cancellation: a caller disconnecting
	// mid-request must not suppress the record of what it did.
	actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return s.store.AppendAudit(actx, ev)
}

// auditRef records a namespaced operation, denormalizing the ref's env/app/key.
func (s *Service) auditRef(ctx context.Context, pr Principal, eventType, resourceType string, ref domain.Ref, version uint64, decision string, meta map[string]string) {
	s.audit(ctx, s.buildRefEvent(ctx, pr, eventType, resourceType, ref, version, decision, meta))
}

// auditRefWithNamespaceID records the namespace row observed before the
// operation. Callers use it for mutable/delete paths where a post-operation
// name lookup could otherwise stamp a newly recreated namespace (ABA).
func (s *Service) auditRefWithNamespaceID(ctx context.Context, pr Principal, eventType, resourceType string, ref domain.Ref, namespaceID int64, version uint64, decision string, meta map[string]string) {
	s.audit(ctx, s.buildRefEventWithNamespaceID(pr, eventType, resourceType, ref, namespaceID, version, decision, meta))
}

func (s *Service) auditRefStrictWithNamespaceID(ctx context.Context, pr Principal, eventType, resourceType string, ref domain.Ref, namespaceID int64, version uint64, decision string, meta map[string]string) error {
	return s.auditStrict(ctx, s.buildRefEventWithNamespaceID(pr, eventType, resourceType, ref, namespaceID, version, decision, meta))
}

func (s *Service) buildRefEvent(ctx context.Context, pr Principal, eventType, resourceType string, ref domain.Ref, version uint64, decision string, meta map[string]string) domain.AuditEvent {
	event := s.buildEvent(pr, eventType, resourceType, ref, version, decision, meta)
	if ref.NS.Env != "" && ref.NS.App != "" {
		if namespaceID, ok := storage.ExpectedNamespaceIncarnation(ctx, ref.NS); ok {
			event.ResourceNamespaceID = namespaceID
		}
	}
	return event
}

func (s *Service) buildRefEventWithNamespaceID(pr Principal, eventType, resourceType string, ref domain.Ref, namespaceID int64, version uint64, decision string, meta map[string]string) domain.AuditEvent {
	event := s.buildEvent(pr, eventType, resourceType, ref, version, decision, meta)
	event.ResourceNamespaceID = namespaceID
	return event
}

// auditName records an operation on a name-addressed resource (policy,
// identity, key) whose name is carried in ResourceKey.
func (s *Service) auditName(ctx context.Context, pr Principal, eventType, resourceType, name string, decision string, meta map[string]string) {
	s.audit(ctx, s.buildEvent(pr, eventType, resourceType, domain.Ref{Key: name}, 0, decision, meta))
}

func (s *Service) buildEvent(pr Principal, eventType, resourceType string, ref domain.Ref, version uint64, decision string, meta map[string]string) domain.AuditEvent {
	return domain.AuditEvent{
		EventType:       eventType,
		ActorIdentity:   pr.Identity.Name,
		ActorType:       pr.Identity.Kind,
		ResourceType:    resourceType,
		ResourceEnv:     ref.NS.Env,
		ResourceApp:     ref.NS.App,
		ResourceKey:     ref.Key,
		ResourceVersion: version,
		Decision:        decision,
		SourceIP:        pr.RemoteAddr,
		UserAgent:       pr.UserAgent,
		RequestID:       pr.RequestID,
		CreatedAt:       s.now(),
		Metadata:        encodeMeta(meta),
	}
}

// --- helpers ---------------------------------------------------------------

// tokenHashMatches compares a supplied token against a stored hash in
// constant time. Empty inputs never match.
func tokenHashMatches(token string, storedHash []byte) bool {
	if token == "" || len(storedHash) == 0 {
		return false
	}
	return hmac.Equal(crypto.TokenHash(token), storedHash)
}
