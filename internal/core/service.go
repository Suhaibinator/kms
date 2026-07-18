// Package core implements the service layer: authentication, authorization,
// audit, and the business logic for parameters, secrets, namespaces,
// policies, and identities. Transport layers (gRPC, HTTP) are thin adapters
// over this package; storage and crypto are its dependencies.
//
// Resources are addressed by domain.Ref (a namespace plus a relative key),
// never by a parsed path string. Every namespaced operation runs, in order:
// argument validation (internal/keyutil), the per-namespace auth-method gate
// (plan §7), authorization (internal/policy, with the implicit home-namespace
// grant folded in), the storage call, then audit and watch fan-out.
package core

import (
	"context"
	"crypto/hmac"
	"crypto/x509"
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
// it via Service.Authenticate (bearer token) or Service.VerifyClientCert
// (mTLS) and pass it to every operation.
type Principal struct {
	Identity domain.Identity
	// Method is how the caller proved its identity (token or mTLS). The
	// transport sets it; the per-namespace auth-method gate enforces it for
	// client-kind identities.
	Method domain.AuthMethod
	// Token is the identity bearer token the caller authenticated with, retained
	// only so long-lived token streams can re-authenticate periodically (see
	// ReauthorizeWatch). Empty for mTLS callers. Never logged or persisted.
	Token string
	// Serial is the serial of the client certificate an mTLS caller presented
	// (empty for token callers). It lets long-lived mTLS streams be re-validated
	// against the specific certificate, so revoking one serial tears the stream
	// down (see ReauthorizeWatch). Transports set it via CertSerial.
	Serial string
	// SecretToken is the optional per-secret access token supplied with the
	// request (x-kms-secret-token). Never logged, never persisted.
	SecretToken string
	RemoteAddr  string
	UserAgent   string
	RequestID   string
}

// IsAdmin reports whether the principal has the admin kind. Admin-kind
// identities are the management plane: they bypass the per-namespace
// auth-method gate and data-plane policy (a browser cannot practically do
// client-cert auth), but not audit or client-bound cryptography.
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
	log          *zap.Logger
	version      string
	now          func() time.Time
}

// New constructs a Service. The keyring is attached later via SetKeyring
// (after unseal); until then the service reports not-ready and refuses secret
// operations. The built-in CA is bootstrapped via BootstrapCA once the keyring
// is present.
func New(store storage.Store, logger *zap.Logger, version string) *Service {
	if logger == nil {
		logger = zap.NewNop()
	}
	s := &Service{store: store, log: logger, version: version, now: func() time.Time { return time.Now().UTC() }}
	s.auditEnabled.Store(true)
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
// built-in CA pool; this method enforces the KMS-specific claims: exactly one
// kms://identity/<name> SAN, a matching non-revoked, non-expired certificate
// record, and an enabled identity. Failures are generic and audited.
func (s *Service) VerifyClientCert(ctx context.Context, cert *x509.Certificate, remoteAddr, userAgent string) (domain.Identity, error) {
	name, err := ca.IdentityFromCert(cert)
	if err != nil {
		return domain.Identity{}, s.mtlsAuthFailure(ctx, remoteAddr, userAgent)
	}
	serial := CertSerial(cert)
	rec, err := s.store.GetIdentityCertBySerial(ctx, serial)
	if err != nil {
		return domain.Identity{}, s.mtlsAuthFailure(ctx, remoteAddr, userAgent)
	}
	if rec.IdentityName != name || rec.IdentityDisabled ||
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
	if cert == nil {
		return ""
	}
	return strings.ToLower(cert.SerialNumber.Text(16))
}

// --- namespace auth-method gate --------------------------------------------

// namespaceMethodGate enforces, before authorization, that the caller's
// authentication method is admitted by the namespace (plan §7). Admin-kind
// identities bypass it (management plane). A nonexistent namespace surfaces as
// the storage error (ErrNotFound on reads). A disallowed method is
// ErrPermissionDenied with an explicit message and an audited denial.
func (s *Service) namespaceMethodGate(ctx context.Context, pr Principal, ns domain.NamespaceRef, resourceType string) error {
	if pr.IsAdmin() {
		return nil
	}
	n, err := s.store.GetNamespace(ctx, ns)
	if err != nil {
		return err
	}
	if authMethodAllowed(n.AllowedAuthMethods, pr.Method) {
		return nil
	}
	allowed := make([]string, len(n.AllowedAuthMethods))
	for i, m := range n.AllowedAuthMethods {
		allowed[i] = string(m)
	}
	s.auditRef(ctx, pr, "authz.method_denied", resourceType, domain.Ref{NS: ns}, 0, "deny",
		map[string]string{"method": string(pr.Method), "required": strings.Join(allowed, ",")})
	return domain.Errorf(domain.ErrPermissionDenied,
		"namespace %s requires %s", ns, strings.Join(allowed, " or "))
}

// --- authorization ---------------------------------------------------------

// authorize enforces the per-namespace method gate and then policy for one
// data-plane operation on ref. Admin identities are authorized for everything
// (and skip the method gate). Denials are audited.
func (s *Service) authorize(ctx context.Context, pr Principal, operation, resourceType string, ref domain.Ref) error {
	if err := s.namespaceMethodGate(ctx, pr, ref.NS, resourceType); err != nil {
		return err
	}
	if pr.IsAdmin() {
		return nil
	}
	policies, err := s.store.PoliciesForSubject(ctx, pr.Identity.Name)
	if err != nil {
		return domain.Errorf(domain.ErrPermissionDenied, "authorization unavailable")
	}
	if !policy.Authorize(policies, pr.home(), operation, ref.NS) {
		s.auditRef(ctx, pr, "authz.denial", resourceType, ref, 0, "deny", map[string]string{"operation": operation})
		return domain.Errorf(domain.ErrPermissionDenied, "access denied")
	}
	return nil
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
func (s *Service) listFilter(ctx context.Context, pr Principal, resourceType, listOp string, ns domain.NamespaceRef, itemOps ...string) (func(domain.Ref) bool, error) {
	if err := s.namespaceMethodGate(ctx, pr, ns, resourceType); err != nil {
		return nil, err
	}
	if pr.IsAdmin() {
		return func(domain.Ref) bool { return true }, nil
	}
	policies, err := s.store.PoliciesForSubject(ctx, pr.Identity.Name)
	if err != nil {
		return nil, domain.Errorf(domain.ErrPermissionDenied, "authorization unavailable")
	}
	home := pr.home()
	// Use the full authorization decision here: explicit denies must override
	// both policy allows and the implicit home-namespace grant.
	if !policy.Authorize(policies, home, listOp, ns) {
		s.auditRef(ctx, pr, "authz.denial", resourceType, domain.Ref{NS: ns}, 0, "deny",
			map[string]string{"operation": listOp})
		return nil, domain.Errorf(domain.ErrPermissionDenied, "access denied")
	}
	return func(ref domain.Ref) bool {
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
	if pr.IsAdmin() {
		return nil
	}
	policies, err := s.store.PoliciesForSubject(ctx, pr.Identity.Name)
	if err != nil {
		return domain.Errorf(domain.ErrPermissionDenied, "authorization unavailable")
	}
	home := pr.home()
	for _, ns := range namespaces {
		if err := s.namespaceMethodGate(ctx, pr, ns, domain.ResourceParameter); err != nil {
			return err
		}
		if !policy.Authorize(policies, home, domain.OpParameterRead, ns) {
			s.auditRef(ctx, pr, "authz.denial", domain.ResourceParameter, domain.Ref{NS: ns}, 0, "deny",
				map[string]string{"operation": "subscribe"})
			return domain.Errorf(domain.ErrPermissionDenied, "access denied")
		}
	}
	return nil
}

// ReauthorizeWatch re-validates a live stream's credential and re-runs the full
// subscribe-time authorization for every subscribed namespace. The watch handler
// calls it on every heartbeat tick and closes the stream on error, so revocation
// takes effect within one heartbeat interval rather than waiting for a reconnect.
//
// Credential re-check: for token streams it re-authenticates the bearer token
// itself, so rotating or revoking a token drops the stream (ErrUnauthenticated).
// For mTLS streams it re-checks that the identity is still enabled AND that the
// presenting certificate (by serial) is still valid, so revoking a single cert
// drops the stream. Any transport that builds an mTLS Principal MUST populate
// Serial (via CertSerial); when it is empty the per-cert recheck is skipped and
// only the identity-disable check protects the stream.
//
// Authorization re-check: for each subscribed namespace it re-runs the same
// per-namespace method gate AND namespace-level policy check (home grant folded
// in) that AuthorizeSubscribe applies at subscribe time. So tightening a
// namespace's allowed methods, or revoking a client's explicit grant to a
// namespace, drops the stream on the next heartbeat (ErrPermissionDenied), while
// a home-namespace subscriber keeps its implicit grant across policy changes.
// This is namespace-level and cheap (one policy read plus a check per subscribed
// namespace per heartbeat), not the per-event predicate that was removed. Admins
// bypass namespace authorization (identity re-validated above). Callers that pass
// no namespaces get credential re-validation only.
func (s *Service) ReauthorizeWatch(ctx context.Context, pr Principal, namespaces ...domain.NamespaceRef) error {
	if pr.Method == domain.AuthMethodToken {
		id, err := s.Authenticate(ctx, pr.Token, pr.RemoteAddr, pr.UserAgent)
		if err != nil || id.Name != pr.Identity.Name || id.Kind != pr.Identity.Kind {
			return domain.Errorf(domain.ErrUnauthenticated, "invalid credentials")
		}
	} else {
		id, err := s.store.GetIdentityByName(ctx, pr.Identity.Name)
		if err != nil || id.Disabled || id.Kind != pr.Identity.Kind {
			return domain.Errorf(domain.ErrUnauthenticated, "invalid credentials")
		}
		// Re-validate the specific certificate: a single revoked/expired serial
		// (identity still enabled) must still tear the stream down. Any transport
		// that builds an mTLS Principal MUST populate Serial (via CertSerial) —
		// when it is empty this per-cert recheck is skipped and only the
		// identity-disable check above protects the stream.
		if pr.Serial != "" {
			rec, cerr := s.store.GetIdentityCertBySerial(ctx, pr.Serial)
			if cerr != nil || rec.IdentityName != pr.Identity.Name || rec.IdentityDisabled ||
				!rec.Cert.RevokedAt.IsZero() ||
				(!rec.Cert.NotAfter.IsZero() && s.now().After(rec.Cert.NotAfter)) {
				return domain.Errorf(domain.ErrUnauthenticated, "invalid credentials")
			}
		}
	}
	// Re-run the FULL per-namespace authorization (method gate + policy) for each
	// subscribed namespace, mirroring AuthorizeSubscribe, so a live stream is torn
	// down promptly when a namespace's allowed methods are tightened OR the
	// caller's grant for that namespace is revoked. Client identities only; admins
	// bypass namespace authorization (their identity was re-validated above).
	if !pr.IsAdmin() {
		policies, perr := s.store.PoliciesForSubject(ctx, pr.Identity.Name)
		if perr != nil {
			return domain.Errorf(domain.ErrPermissionDenied, "authorization unavailable")
		}
		home := pr.home()
		for _, ns := range namespaces {
			if err := s.namespaceMethodGate(ctx, pr, ns, domain.ResourceParameter); err != nil {
				return err
			}
			if !policy.Authorize(policies, home, domain.OpParameterRead, ns) {
				return domain.Errorf(domain.ErrPermissionDenied, "access denied")
			}
		}
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
	s.audit(ctx, s.buildEvent(pr, eventType, resourceType, ref, version, decision, meta))
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
