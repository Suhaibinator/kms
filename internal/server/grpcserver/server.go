// Package grpcserver exposes the KMS core service over gRPC. It is a thin
// adapter: interceptors authenticate callers and gate readiness, handlers
// translate protobuf messages to core calls and map domain errors to gRPC
// status codes. No business logic or storage access lives here — the sole
// exception is the watch hub, which the server drives for the streaming
// WatchService.
package grpcserver

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"time"

	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/health"
	healthgrpc "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/keepalive"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
	"github.com/Suhaibinator/kms/internal/core"
	"github.com/Suhaibinator/kms/internal/watch"
	"github.com/Suhaibinator/kms/sdk/go/configstore"
)

// Config configures the gRPC server.
type Config struct {
	// Addr is the listen address (host:port) used by Listen.
	Addr string
	// TLS, when non-nil, wraps the listener in TLS. A nil value serves
	// plaintext and is intended for development only.
	TLS *tls.Config
	// KeepaliveTime is the interval the server pings idle transports.
	KeepaliveTime time.Duration
	// KeepaliveMinTime is the minimum client ping interval the server tolerates
	// before terminating the connection.
	KeepaliveMinTime time.Duration
	// HealthRefreshInterval controls how often the standard gRPC health service
	// re-evaluates readiness. Defaults to 5s.
	HealthRefreshInterval time.Duration
}

// defaults for keepalive and health refresh.
const (
	defaultKeepaliveTime         = 30 * time.Second
	defaultKeepaliveMinTime      = 10 * time.Second
	defaultHealthRefreshInterval = 5 * time.Second
	// The artifact itself remains capped by configstore at exactly 4 MiB. The
	// transport needs room for protobuf field framing plus the namespace,
	// execution options, and plan digest carried beside those bytes.
	defaultsProtobufEnvelopeHeadroom = 64 << 10
	maxReceiveMessageBytes           = configstore.MaxDefaultsArtifactSize + defaultsProtobufEnvelopeHeadroom
)

// Server wraps a *grpc.Server wired to the core service and watch hub.
type Server struct {
	cfg    Config
	svc    *core.Service
	hub    *watch.Hub
	log    *zap.Logger
	grpc   *grpc.Server
	health *health.Server

	done chan struct{}
}

// New builds a Server. The gRPC services are registered immediately; call
// Serve with a listener (or Listen) to begin serving.
func New(svc *core.Service, hub *watch.Hub, cfg Config) (*Server, error) {
	if cfg.KeepaliveTime <= 0 {
		cfg.KeepaliveTime = defaultKeepaliveTime
	}
	if cfg.KeepaliveMinTime <= 0 {
		cfg.KeepaliveMinTime = defaultKeepaliveMinTime
	}
	if cfg.HealthRefreshInterval <= 0 {
		cfg.HealthRefreshInterval = defaultHealthRefreshInterval
	}
	log := svc.Logger()
	if log == nil {
		log = zap.NewNop()
	}

	s := &Server{
		cfg:  cfg,
		svc:  svc,
		hub:  hub,
		log:  log,
		done: make(chan struct{}),
	}
	if err := svc.ResetReleaseSubscriberConnections(context.Background()); err != nil {
		return nil, fmt.Errorf("resetting release subscriber connections: %w", err)
	}

	opts := []grpc.ServerOption{
		grpc.MaxRecvMsgSize(maxReceiveMessageBytes),
		grpc.ChainUnaryInterceptor(s.unaryInterceptor),
		grpc.ChainStreamInterceptor(s.streamInterceptor),
		grpc.KeepaliveParams(keepalive.ServerParameters{
			Time:    cfg.KeepaliveTime,
			Timeout: 20 * time.Second,
		}),
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
			MinTime:             cfg.KeepaliveMinTime,
			PermitWithoutStream: true,
		}),
	}
	if cfg.TLS != nil {
		opts = append(opts, grpc.Creds(credentials.NewTLS(cfg.TLS)))
	}

	s.grpc = grpc.NewServer(opts...)
	s.health = health.NewServer()

	kmsv1.RegisterParameterServiceServer(s.grpc, &parameterServer{s: s})
	kmsv1.RegisterSecretServiceServer(s.grpc, &secretServer{s: s})
	kmsv1.RegisterAdminServiceServer(s.grpc, &adminServer{s: s})
	kmsv1.RegisterWatchServiceServer(s.grpc, &watchServer{s: s})
	kmsv1.RegisterConfigurationReleaseServiceServer(s.grpc, &configurationReleaseServer{s: s, connections: make(map[releaseConnectionKey]*releaseConnectionState)})
	kmsv1.RegisterConfigurationSchemaServiceServer(s.grpc, &configurationSchemaServer{s: s})
	healthgrpc.RegisterHealthServer(s.grpc, s.health)

	return s, nil
}

// Listen builds a TCP listener on the configured address.
func (s *Server) Listen() (net.Listener, error) {
	return net.Listen("tcp", s.cfg.Addr)
}

// Serve begins serving on lis and blocks until the server stops. It also runs a
// background loop that reflects readiness into the standard health service.
func (s *Server) Serve(lis net.Listener) error {
	go s.refreshHealth()
	return s.grpc.Serve(lis)
}

// GracefulStop stops accepting new RPCs and blocks until in-flight RPCs finish.
func (s *Server) GracefulStop() {
	s.stopHealth()
	s.grpc.GracefulStop()
}

// Stop stops the server immediately, cancelling in-flight RPCs.
func (s *Server) Stop() {
	s.stopHealth()
	s.grpc.Stop()
}

// GRPCServer exposes the underlying *grpc.Server (for tests / reflection).
func (s *Server) GRPCServer() *grpc.Server { return s.grpc }

func (s *Server) stopHealth() {
	select {
	case <-s.done:
	default:
		close(s.done)
	}
}

// refreshHealth periodically maps service readiness onto the health service so
// external probes (LB, k8s) see NOT_SERVING until the master key is loaded and
// the store is reachable.
func (s *Server) refreshHealth() {
	set := func() {
		st := healthgrpc.HealthCheckResponse_NOT_SERVING
		if s.svc.Ready(context.Background()) == nil {
			st = healthgrpc.HealthCheckResponse_SERVING
		}
		s.health.SetServingStatus("", st)
		s.health.SetServingStatus("kms.v1.ParameterService", st)
		s.health.SetServingStatus("kms.v1.SecretService", st)
		s.health.SetServingStatus("kms.v1.WatchService", st)
		s.health.SetServingStatus("kms.v1.ConfigurationReleaseService", st)
		s.health.SetServingStatus("kms.v1.ConfigurationSchemaService", st)
	}
	set()
	ticker := time.NewTicker(s.cfg.HealthRefreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-ticker.C:
			set()
		}
	}
}
