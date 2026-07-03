package grpcserver

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"log/slog"
	"math/big"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/test/bufconn"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
	"github.com/Suhaibinator/kms/internal/core"
	"github.com/Suhaibinator/kms/internal/crypto"
	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/watch"
)

const (
	adminToken  = "admin-secret-token"
	clientToken = "client-secret-token"
)

type testEnv struct {
	store *memStore
	svc   *core.Service
	hub   *watch.Hub
	srv   *Server
	conn  *grpc.ClientConn
}

func (e *testEnv) param() kmsv1.ParameterServiceClient {
	return kmsv1.NewParameterServiceClient(e.conn)
}
func (e *testEnv) secret() kmsv1.SecretServiceClient     { return kmsv1.NewSecretServiceClient(e.conn) }
func (e *testEnv) admin() kmsv1.AdminServiceClient       { return kmsv1.NewAdminServiceClient(e.conn) }
func (e *testEnv) watchClient() kmsv1.WatchServiceClient { return kmsv1.NewWatchServiceClient(e.conn) }

// newTestEnv wires a real core.Service and watch.Hub over an in-memory store and
// serves them on an in-process bufconn listener (plaintext, token auth). When
// ready is false the service has no keyring and reports not-ready.
func newTestEnv(t *testing.T, ready bool) *testEnv {
	t.Helper()
	store := newMemStore()
	store.addIdentity("admin", domain.IdentityKindAdmin, adminToken, nil)
	store.addIdentity("client", domain.IdentityKindClient, clientToken, nil)

	svc, hub := buildService(t, store, ready)
	srv, lis := serveBufconn(t, svc, hub, Config{})

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	return &testEnv{store: store, svc: svc, hub: hub, srv: srv, conn: conn}
}

// buildService constructs the core.Service and started watch.Hub shared by the
// plaintext and TLS harnesses.
func buildService(t *testing.T, store *memStore, ready bool) (*core.Service, *watch.Hub) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := core.New(store, logger, "v-test")
	if ready {
		kek, err := crypto.NewKEKFromMaterial("kek-1", make([]byte, 32))
		if err != nil {
			t.Fatalf("build kek: %v", err)
		}
		svc.SetKeyring(crypto.NewKeyring(kek))
	}

	// Fast heartbeats keep the e2e tests quick, while a generous missed-heartbeat
	// count keeps the liveness window wide enough that normal test latency never
	// trips a spurious drop.
	hub := watch.NewHub(store, logger, watch.Options{
		HeartbeatInterval: 60 * time.Millisecond,
		MissedHeartbeats:  25,
		PruneInterval:     time.Hour,
	})
	svc.SetHub(hub)

	hubCtx, hubCancel := context.WithCancel(context.Background())
	go func() { _ = hub.Run(hubCtx) }()
	select {
	case <-hub.Started():
	case <-time.After(2 * time.Second):
		hubCancel()
		t.Fatal("hub did not start")
	}
	t.Cleanup(hubCancel)
	return svc, hub
}

// serveBufconn registers the services and serves them on an in-process listener.
func serveBufconn(t *testing.T, svc *core.Service, hub *watch.Hub, cfg Config) (*Server, *bufconn.Listener) {
	t.Helper()
	srv, err := New(svc, hub, cfg)
	if err != nil {
		t.Fatalf("New server: %v", err)
	}
	lis := bufconn.Listen(1 << 20)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(func() {
		srv.GracefulStop()
		_ = lis.Close()
	})
	return srv, lis
}

// authCtx returns a context carrying a bearer token (and optional secret token).
func authCtx(token string) context.Context {
	md := metadata.Pairs("authorization", "Bearer "+token)
	return metadata.NewOutgoingContext(context.Background(), md)
}

func adminCtx() context.Context  { return authCtx(adminToken) }
func clientCtx() context.Context { return authCtx(clientToken) }

// ref builds a domain.Ref for terse test call sites.
func ref(env, app, key string) domain.Ref {
	return domain.Ref{NS: domain.NamespaceRef{Env: env, App: app}, Key: key}
}

// pRef builds a wire ResourceRef for terse test call sites.
func pRef(env, app, key string) *kmsv1.ResourceRef {
	return &kmsv1.ResourceRef{Namespace: &kmsv1.NamespaceRef{Env: env, App: app}, Key: key}
}

// pNS builds a wire NamespaceRef.
func pNS(env, app string) *kmsv1.NamespaceRef {
	return &kmsv1.NamespaceRef{Env: env, App: app}
}

// --- TLS / mTLS harness ----------------------------------------------------

type tlsTestEnv struct {
	*testEnv
	lis        *bufconn.Listener
	serverPool *x509.CertPool // trust anchor for the server cert (client RootCAs)
}

// newTLSTestEnv serves the gRPC services over TLS on a bufconn listener with the
// built-in CA bootstrapped and VerifyClientCertIfGiven, so both token and mTLS
// clients can connect. It returns the environment and the pool that trusts the
// server certificate.
func newTLSTestEnv(t *testing.T) *tlsTestEnv {
	t.Helper()
	store := newMemStore()
	store.addIdentity("admin", domain.IdentityKindAdmin, adminToken, nil)
	svc, hub := buildService(t, store, true)
	if err := svc.BootstrapCA(context.Background()); err != nil {
		t.Fatalf("bootstrap CA: %v", err)
	}

	serverCert, serverPool := genServerCert(t)
	caPool, err := svc.CACertPool()
	if err != nil {
		t.Fatalf("ca pool: %v", err)
	}
	tlsCfg := &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{serverCert},
		ClientCAs:    caPool,
		ClientAuth:   tls.VerifyClientCertIfGiven,
	}
	srv, lis := serveBufconn(t, svc, hub, Config{TLS: tlsCfg})

	return &tlsTestEnv{
		testEnv:    &testEnv{store: store, svc: svc, hub: hub, srv: srv},
		lis:        lis,
		serverPool: serverPool,
	}
}

// dial opens a TLS client connection to the listener, optionally presenting a
// client certificate.
func (e *tlsTestEnv) dial(t *testing.T, clientCert *tls.Certificate) *grpc.ClientConn {
	t.Helper()
	tlsCfg := &tls.Config{
		RootCAs:    e.serverPool,
		ServerName: "bufnet",
		MinVersion: tls.VersionTLS12,
	}
	if clientCert != nil {
		tlsCfg.Certificates = []tls.Certificate{*clientCert}
	}
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return e.lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)),
	)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// issueClientCert creates a cert-only identity (bound to ns when non-nil) and
// mints it a client certificate via the built-in CA, returning a usable TLS
// certificate and the issued serial.
func (e *tlsTestEnv) issueClientCert(t *testing.T, name string, ns *domain.NamespaceRef) (tls.Certificate, string) {
	t.Helper()
	e.store.addIdentity(name, domain.IdentityKindClient, "", ns)
	adminPr := core.Principal{Identity: domain.Identity{Name: "admin", Kind: domain.IdentityKindAdmin}, Method: domain.AuthMethodToken}
	bundle, err := e.svc.IssueIdentityCertificate(context.Background(), adminPr, name, 0)
	if err != nil {
		t.Fatalf("issue cert for %s: %v", name, err)
	}
	pair, err := tls.X509KeyPair([]byte(bundle.CertPEM), []byte(bundle.KeyPEM))
	if err != nil {
		t.Fatalf("load client key pair: %v", err)
	}
	return pair, bundle.Serial
}

// genServerCert creates a short-lived self-signed server certificate for
// "bufnet" plus a pool trusting it.
func genServerCert(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("gen server key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "bufnet"},
		DNSNames:     []string{"bufnet"},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IsCA:         true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create server cert: %v", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal server key: %v", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("load server key pair: %v", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(certPEM) {
		t.Fatal("append server cert")
	}
	return pair, pool
}
