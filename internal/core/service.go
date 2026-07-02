// Package core implements the service layer: authentication, authorization,
// audit, and the business logic for parameters, secrets, namespaces,
// policies, and identities. Transport layers (gRPC, HTTP) are thin adapters
// over this package; storage and crypto are its dependencies.
package core

import (
	"context"
	"crypto/hmac"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Suhaibinator/kms/internal/crypto"
	"github.com/Suhaibinator/kms/internal/domain"
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

// Principal is the authenticated caller plus request context. Transports
// build it via Service.Authenticate and pass it to every operation.
type Principal struct {
	Identity domain.Identity
	// Token is the identity bearer token the caller authenticated with. It is
	// retained only so long-lived streams can re-authenticate it periodically
	// (see ReauthorizeWatch); it is never logged or persisted.
	Token string
	// SecretToken is the optional per-secret access token supplied with the
	// request (x-kms-secret-token). Never logged, never persisted.
	SecretToken string
	RemoteAddr  string
	UserAgent   string
	RequestID   string
}

// IsAdmin reports whether the principal has the admin kind.
func (p Principal) IsAdmin() bool { return p.Identity.Kind == domain.IdentityKindAdmin }

// Service wires storage, crypto, watch, and audit together.
type Service struct {
	store   storage.Store
	keyring atomic.Pointer[crypto.Keyring]
	hub     atomic.Pointer[Hub]
	log     *slog.Logger
	version string
	now     func() time.Time
}

// New constructs a Service. The keyring is attached later via SetKeyring
// (after unseal); until then the service reports not-ready and refuses
// secret operations.
func New(store storage.Store, logger *slog.Logger, version string) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	s := &Service{store: store, log: logger, version: version, now: func() time.Time { return time.Now().UTC() }}
	var h Hub = noopHub{}
	s.hub.Store(&h)
	return s
}

// SetKeyring attaches the verified keyring. The service becomes ready.
func (s *Service) SetKeyring(k *crypto.Keyring) { s.keyring.Store(k) }

// SetHub attaches the watch hub.
func (s *Service) SetHub(h Hub) { s.hub.Store(&h) }

// Store exposes the underlying store to trusted in-process consumers
// (watch hub snapshot/replay, CLI). Transport layers must not use it.
func (s *Service) Store() storage.Store { return s.store }

// Logger returns the service logger.
func (s *Service) Logger() *slog.Logger { return s.log }

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
		})
		return domain.Identity{}, domain.Errorf(domain.ErrUnauthenticated, "invalid credentials")
	}
	return id, nil
}

// --- authorization ---------------------------------------------------------

// authorize enforces policy for one operation. Admin identities are
// authorized for everything. Denials are audited.
func (s *Service) authorize(ctx context.Context, pr Principal, operation, resourceType, path string) error {
	if pr.IsAdmin() {
		return nil
	}
	policies, err := s.store.PoliciesForSubject(ctx, pr.Identity.Name)
	if err != nil {
		return domain.Errorf(domain.ErrPermissionDenied, "authorization unavailable")
	}
	if !evaluatePolicies(policies, operation, path) {
		s.auditOp(ctx, pr, "authz.denial", resourceType, path, 0, "deny", map[string]string{"operation": operation})
		return domain.Errorf(domain.ErrPermissionDenied, "access denied")
	}
	return nil
}

// WatchAccessChecker returns a predicate the watch hub uses to filter events
// and snapshot contents per subscriber: parameters require parameter:read,
// secret metadata events require secret:read. Admins see everything.
//
// The predicate captures a point-in-time policy snapshot. Long-lived streams
// must refresh it periodically via ReauthorizeWatch so policy changes and
// identity revocation take effect without waiting for a reconnect.
func (s *Service) WatchAccessChecker(ctx context.Context, pr Principal) (func(resourceType, path string) bool, error) {
	if pr.IsAdmin() {
		return func(string, string) bool { return true }, nil
	}
	policies, err := s.store.PoliciesForSubject(ctx, pr.Identity.Name)
	if err != nil {
		return nil, domain.Errorf(domain.ErrPermissionDenied, "authorization unavailable")
	}
	return func(resourceType, path string) bool {
		op := domain.OpParameterRead
		if resourceType == domain.ResourceSecret {
			op = domain.OpSecretRead
		}
		return evaluatePolicies(policies, op, path)
	}, nil
}

// ReauthorizeWatch re-validates a live stream's credential and returns a fresh
// access predicate reflecting current policies. The gRPC watch handler calls
// it on every heartbeat tick and closes the stream on error. Re-validating the
// bearer token (not just the identity name) means rotating or revoking a
// client's token tears down any stream still using the old token within one
// heartbeat interval — a stolen token cannot outlive its rotation.
func (s *Service) ReauthorizeWatch(ctx context.Context, pr Principal) (func(resourceType, path string) bool, error) {
	id, err := s.Authenticate(ctx, pr.Token, pr.RemoteAddr, pr.UserAgent)
	if err != nil || id.Name != pr.Identity.Name || id.Kind != pr.Identity.Kind {
		return nil, domain.Errorf(domain.ErrUnauthenticated, "invalid credentials")
	}
	return s.WatchAccessChecker(ctx, pr)
}

// --- audit -----------------------------------------------------------------

// audit appends an audit event. Failures are logged loudly but never carry
// secret material. Callers that must fail closed use auditStrict.
func (s *Service) audit(ctx context.Context, ev domain.AuditEvent) {
	if err := s.appendAudit(ctx, ev); err != nil {
		s.log.Error("audit append failed", "event_type", ev.EventType, "path", ev.ResourcePath, "error", err)
	}
}

// auditStrict appends an audit event and reports failure so security-critical
// reads can fail closed when auditing is impossible.
func (s *Service) auditStrict(ctx context.Context, ev domain.AuditEvent) error {
	if err := s.appendAudit(ctx, ev); err != nil {
		s.log.Error("audit append failed (failing operation closed)",
			"event_type", ev.EventType, "path", ev.ResourcePath, "error", err)
		return domain.Errorf(domain.ErrFailedPrecondition, "audit unavailable")
	}
	return nil
}

func (s *Service) appendAudit(ctx context.Context, ev domain.AuditEvent) error {
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

func (s *Service) auditOp(ctx context.Context, pr Principal, eventType, resourceType, path string, version uint64, decision string, meta map[string]string) {
	s.audit(ctx, s.buildEvent(pr, eventType, resourceType, path, version, decision, meta))
}

func (s *Service) buildEvent(pr Principal, eventType, resourceType, path string, version uint64, decision string, meta map[string]string) domain.AuditEvent {
	return domain.AuditEvent{
		EventType:       eventType,
		ActorIdentity:   pr.Identity.Name,
		ActorType:       pr.Identity.Kind,
		ResourceType:    resourceType,
		ResourcePath:    path,
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
