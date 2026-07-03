package grpcserver

import (
	"context"
	"crypto/rand"
	"crypto/x509"
	"encoding/hex"
	"runtime/debug"
	"strings"

	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	"github.com/Suhaibinator/kms/internal/core"
	"github.com/Suhaibinator/kms/internal/domain"
)

// publicMethods bypass authentication and readiness gating: health probes must
// answer even before the master key is loaded and without credentials.
var publicMethods = map[string]bool{
	"/kms.v1.AdminService/Health":           true,
	"/kms.v1.AdminService/GetCACertificate": true,
	"/grpc.health.v1.Health/Check":          true,
	"/grpc.health.v1.Health/Watch":          true,
}

// unaryInterceptor applies, in order: panic recovery, request-ID tagging,
// authentication, and readiness gating.
func (s *Server) unaryInterceptor(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (resp any, err error) {
	reqID := newRequestID()
	ctx = withRequestID(ctx, reqID)

	defer func() {
		if r := recover(); r != nil {
			s.log.Error("panic in unary handler", zap.String("method", info.FullMethod),
				zap.String("request_id", reqID), zap.Any("panic", r), zap.String("stack", string(debug.Stack())))
			err = status.Error(codes.Internal, "internal error")
			resp = nil
		}
	}()

	if publicMethods[info.FullMethod] {
		return handler(ctx, req)
	}

	// Readiness before authentication: during a database outage or while the
	// master key is missing, callers get Unavailable rather than a misleading
	// Unauthenticated from an auth path that cannot reach storage.
	if rerr := s.svc.Ready(ctx); rerr != nil {
		return nil, status.Error(codes.Unavailable, "service not ready")
	}
	ctx, err = s.authenticate(ctx, reqID)
	if err != nil {
		return nil, err
	}
	return handler(ctx, req)
}

// streamInterceptor mirrors unaryInterceptor for streaming RPCs, wrapping the
// stream so the handler observes the authenticated context.
func (s *Server) streamInterceptor(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) (err error) {
	reqID := newRequestID()
	ctx := withRequestID(ss.Context(), reqID)

	defer func() {
		if r := recover(); r != nil {
			s.log.Error("panic in stream handler", zap.String("method", info.FullMethod),
				zap.String("request_id", reqID), zap.Any("panic", r), zap.String("stack", string(debug.Stack())))
			err = status.Error(codes.Internal, "internal error")
		}
	}()

	if publicMethods[info.FullMethod] {
		return handler(srv, &wrappedStream{ServerStream: ss, ctx: ctx})
	}

	if rerr := s.svc.Ready(ctx); rerr != nil {
		return status.Error(codes.Unavailable, "service not ready")
	}
	ctx, err = s.authenticate(ctx, reqID)
	if err != nil {
		return err
	}
	return handler(srv, &wrappedStream{ServerStream: ss, ctx: ctx})
}

// wrappedStream overrides Context so downstream handlers see the authenticated,
// request-tagged context.
type wrappedStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (w *wrappedStream) Context() context.Context { return w.ctx }

// authenticate resolves the caller's credential to a principal and stashes it
// in the context. A verified mTLS client certificate (checked against the
// built-in CA pool during the TLS handshake) takes precedence: it proves
// possession, so it maps to an mTLS-method principal. A bearer token is the
// fallback (and the only option for token-method identities). Failures return a
// generic Unauthenticated status; the method that produced the credential
// determines the per-namespace auth-method gate downstream.
func (s *Server) authenticate(ctx context.Context, reqID string) (context.Context, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	token := bearerToken(md)
	secretToken := firstMD(md, "x-kms-secret-token")
	remoteAddr := remoteAddrFrom(ctx)
	userAgent := firstMD(md, "user-agent")

	// A presented client certificate has already been chain-verified by the TLS
	// layer (VerifyClientCertIfGiven). Prefer it: it is proof of possession.
	if cert := peerCertFromContext(ctx); cert != nil {
		if id, err := s.svc.VerifyClientCert(ctx, cert, remoteAddr, userAgent); err == nil {
			return withPrincipal(ctx, core.Principal{
				Identity:    id,
				Method:      domain.AuthMethodMTLS,
				Serial:      core.CertSerial(cert),
				SecretToken: secretToken,
				RemoteAddr:  remoteAddr,
				UserAgent:   userAgent,
				RequestID:   reqID,
			}), nil
		}
		// Certificate present but not valid (revoked/expired/disabled/unmapped).
		// Fall through to token auth so a client that also holds a valid token can
		// still authenticate where the namespace admits the token method.
	}

	id, err := s.svc.Authenticate(ctx, token, remoteAddr, userAgent)
	if err != nil {
		return ctx, status.Error(codes.Unauthenticated, "unauthenticated")
	}
	return withPrincipal(ctx, core.Principal{
		Identity:    id,
		Method:      domain.AuthMethodToken,
		Token:       token,
		SecretToken: secretToken,
		RemoteAddr:  remoteAddr,
		UserAgent:   userAgent,
		RequestID:   reqID,
	}), nil
}

// peerCertFromContext returns the verified leaf client certificate from the TLS
// peer, or nil when the connection is plaintext or the client presented none.
// Under tls.VerifyClientCertIfGiven a present certificate has already been
// validated against the client-CA pool, so its mere presence here means it
// chained to a trusted CA.
func peerCertFromContext(ctx context.Context) *x509.Certificate {
	p, ok := peer.FromContext(ctx)
	if !ok || p.AuthInfo == nil {
		return nil
	}
	tlsInfo, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok {
		return nil
	}
	// Require a verified chain, not mere presence: only a certificate that
	// chained to the client-CA pool is trustworthy. This holds regardless of the
	// listener's ClientAuth mode, so the guarantee survives a future switch away
	// from VerifyClientCertIfGiven (e.g. RequestClientCert, which does not verify).
	if len(tlsInfo.State.VerifiedChains) == 0 || len(tlsInfo.State.PeerCertificates) == 0 {
		return nil
	}
	return tlsInfo.State.PeerCertificates[0]
}

// bearerToken extracts the token from an "authorization: Bearer <tok>" header.
func bearerToken(md metadata.MD) string {
	v := firstMD(md, "authorization")
	if v == "" {
		return ""
	}
	if len(v) >= 7 && strings.EqualFold(v[:7], "bearer ") {
		return strings.TrimSpace(v[7:])
	}
	return strings.TrimSpace(v)
}

// firstMD returns the first value for key, or "".
func firstMD(md metadata.MD, key string) string {
	vals := md.Get(key)
	if len(vals) == 0 {
		return ""
	}
	return vals[0]
}

// remoteAddrFrom returns the caller's network address from the peer, or "".
func remoteAddrFrom(ctx context.Context) string {
	if p, ok := peer.FromContext(ctx); ok && p.Addr != nil {
		return p.Addr.String()
	}
	return ""
}

// newRequestID returns a random hex request identifier.
func newRequestID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "req-unknown"
	}
	return hex.EncodeToString(b)
}
