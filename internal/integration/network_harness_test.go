package integration

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
	"github.com/Suhaibinator/kms/internal/core"
	"github.com/Suhaibinator/kms/internal/crypto"
	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/server/grpcserver"
	"github.com/Suhaibinator/kms/internal/server/httpserver"
	"github.com/Suhaibinator/kms/internal/server/listenertls"
	"github.com/Suhaibinator/kms/internal/storage"
	"github.com/Suhaibinator/kms/internal/watch"
)

// loopbackTLSEnv is deliberately separate from harness in harness_test.go.
// The older harness stops at core.Service; this one crosses a real loopback
// TCP socket, a TLS handshake, gRPC interceptors/handlers, the watch hub, and a
// file-backed SQLite store. It has no external dependencies and is safe to run
// untagged in CI.
type loopbackTLSEnv struct {
	t       *testing.T
	dir     string
	dbPath  string
	keyPath string

	store      *storage.SQLStore
	svc        *core.Service
	hub        *watch.Hub
	hubCancel  context.CancelFunc
	hubDone    chan error
	server     *grpcserver.Server
	listener   net.Listener
	serveDone  chan error
	serverPool *x509.CertPool
	serverPEM  []byte
	adminToken string
	adminConn  *grpc.ClientConn

	// The HTTPS listener mirrors the gRPC one: same server certificate, same
	// derived listener TLS config, so both transports are exercised over a real
	// handshake against the same service.
	httpServer   *http.Server
	httpListener net.Listener
	httpDone     chan error

	closeOnce sync.Once
}

func newLoopbackTLSEnv(t *testing.T) *loopbackTLSEnv {
	t.Helper()
	setupCtx, setupCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer setupCancel()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "kms.db")
	keyPath := filepath.Join(dir, "master.key")

	store, err := storage.Open(dbPath)
	if err != nil {
		t.Fatalf("open integration store: %v", err)
	}
	failed := true
	defer func() {
		if failed {
			_ = store.Close()
		}
	}()

	keyring, err := crypto.Unseal(setupCtx, store, crypto.UnsealOptions{
		KeyFilePath:            keyPath,
		CreateKeyFileIfMissing: true,
	})
	if err != nil {
		t.Fatalf("unseal integration store: %v", err)
	}

	adminToken, adminHash, err := crypto.GenerateToken("kms")
	if err != nil {
		t.Fatalf("generate integration admin token: %v", err)
	}
	if _, err := store.CreateIdentity(setupCtx, storage.CreateIdentityParams{
		Name: "network-root", Kind: domain.IdentityKindAdmin, TokenHash: adminHash,
	}); err != nil {
		t.Fatalf("create integration admin: %v", err)
	}

	logger := zap.NewNop()
	svc := core.New(store, logger, "integration-network")
	svc.SetKeyring(keyring)
	// The seeded admin is token-only and most tests here call the API with a
	// bearer token alone. https_admin_test.go turns the admin
	// client-certificate requirement back on where it is the subject.
	svc.SetAdminRequireClientCert(false)
	if err := svc.BootstrapCA(setupCtx); err != nil {
		t.Fatalf("bootstrap integration CA: %v", err)
	}

	hub := watch.NewHub(store, logger, watch.Options{
		HeartbeatInterval:  35 * time.Millisecond,
		MissedHeartbeats:   100,
		PruneInterval:      time.Hour,
		DrainRetryInterval: 10 * time.Millisecond,
	})
	svc.SetHub(hub)
	hubCtx, hubCancel := context.WithCancel(context.Background())
	hubDone := make(chan error, 1)
	go func() { hubDone <- hub.Run(hubCtx) }()
	select {
	case <-hub.Started():
	case <-time.After(5 * time.Second):
		hubCancel()
		t.Fatal("integration watch hub did not start")
	}

	serverCert, serverPool := newLoopbackServerCertificate(t)
	// Exactly what serve builds: one operator TLS config, both listeners
	// derived from it through listenertls.Build.
	baseTLS := &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{serverCert},
	}
	listenerTLS, err := listenertls.Build(baseTLS, svc)
	if err != nil {
		hubCancel()
		t.Fatalf("build listener TLS config: %v", err)
	}
	server, err := grpcserver.New(svc, hub, grpcserver.Config{
		TLS:                   listenerTLS,
		HealthRefreshInterval: 20 * time.Millisecond,
	})
	if err != nil {
		hubCancel()
		t.Fatalf("build integration gRPC server: %v", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		hubCancel()
		t.Fatalf("listen on loopback: %v", err)
	}

	// AdminClientCertRequired is what /api/v1/health reports; enforcement is the
	// service's, so a test can flip the service setting without rebuilding this.
	httpSrv, err := httpserver.New(svc, httpserver.Config{
		Addr: "127.0.0.1:0", TLSEnabled: true, AdminClientCertRequired: true, Version: "integration-network",
	})
	if err != nil {
		hubCancel()
		_ = listener.Close()
		t.Fatalf("build integration HTTP server: %v", err)
	}
	httpSrv.TLSConfig = listenerTLS
	httpListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		hubCancel()
		_ = listener.Close()
		t.Fatalf("listen on loopback for HTTPS: %v", err)
	}

	e := &loopbackTLSEnv{
		t:          t,
		dir:        dir,
		dbPath:     dbPath,
		keyPath:    keyPath,
		store:      store,
		svc:        svc,
		hub:        hub,
		hubCancel:  hubCancel,
		hubDone:    hubDone,
		server:     server,
		listener:   listener,
		serveDone:  make(chan error, 1),
		serverPool: serverPool,
		serverPEM:  pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: serverCert.Certificate[0]}),
		adminToken: adminToken,

		httpServer:   httpSrv,
		httpListener: httpListener,
		httpDone:     make(chan error, 1),
	}
	go func() { e.serveDone <- server.Serve(listener) }()
	go func() { e.httpDone <- httpSrv.ServeTLS(httpListener, "", "") }()

	e.adminConn = e.dial(t, nil)
	readyCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		resp, healthErr := kmsv1.NewAdminServiceClient(e.adminConn).Health(readyCtx, &kmsv1.HealthRequest{})
		if healthErr == nil && resp.GetReady() {
			break
		}
		select {
		case <-readyCtx.Done():
			e.shutdown()
			t.Fatalf("loopback server did not become ready: last response=%+v err=%v", resp, healthErr)
		case <-time.After(10 * time.Millisecond):
		}
	}

	t.Cleanup(e.shutdown)
	failed = false
	return e
}

func (e *loopbackTLSEnv) shutdown() {
	e.closeOnce.Do(func() {
		if e.adminConn != nil {
			_ = e.adminConn.Close()
		}
		if e.server != nil {
			e.server.Stop()
		}
		if e.listener != nil {
			_ = e.listener.Close()
		}
		if e.httpServer != nil {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			_ = e.httpServer.Shutdown(shutdownCtx)
			cancel()
		}
		if e.httpListener != nil {
			_ = e.httpListener.Close()
		}
		if e.httpDone != nil {
			select {
			case err := <-e.httpDone:
				if err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
					e.t.Errorf("loopback HTTPS server stopped with error: %v", err)
				}
			case <-time.After(2 * time.Second):
				e.t.Error("timed out waiting for loopback HTTPS server shutdown")
			}
		}
		if e.hubCancel != nil {
			e.hubCancel()
		}
		if e.serveDone != nil {
			select {
			case err := <-e.serveDone:
				if err != nil && !errors.Is(err, grpc.ErrServerStopped) && !errors.Is(err, net.ErrClosed) {
					e.t.Errorf("loopback gRPC server stopped with error: %v", err)
				}
			case <-time.After(2 * time.Second):
				e.t.Error("timed out waiting for loopback gRPC server shutdown")
			}
		}
		if e.hubDone != nil {
			select {
			case err := <-e.hubDone:
				if err != nil && !errors.Is(err, context.Canceled) {
					e.t.Errorf("loopback watch hub stopped with error: %v", err)
				}
			case <-time.After(2 * time.Second):
				e.t.Error("timed out waiting for loopback watch hub shutdown")
			}
		}
		if e.store != nil {
			_ = e.store.Close()
		}
	})
}

func (e *loopbackTLSEnv) endpoint() string { return e.listener.Addr().String() }

// httpsURL returns the absolute URL of path on the loopback HTTPS listener.
func (e *loopbackTLSEnv) httpsURL(path string) string {
	return "https://" + e.httpListener.Addr().String() + path
}

// httpClient returns an HTTPS client trusting the loopback server certificate
// and, when clientCert is non-nil, presenting it during the handshake — the
// browser equivalent of having imported an admin certificate.
func (e *loopbackTLSEnv) httpClient(clientCert *tls.Certificate) *http.Client {
	return &http.Client{
		Timeout:   5 * time.Second,
		Transport: &http.Transport{TLSClientConfig: e.clientTLS(clientCert)},
	}
}

// caFile writes the loopback server certificate to a PEM file and returns its
// path, for clients that take a CA bundle path rather than a *tls.Config.
func (e *loopbackTLSEnv) caFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "kms-ca.pem")
	if err := os.WriteFile(path, e.serverPEM, 0o600); err != nil {
		t.Fatalf("write loopback CA file: %v", err)
	}
	return path
}

func (e *loopbackTLSEnv) clientTLS(clientCert *tls.Certificate) *tls.Config {
	cfg := &tls.Config{
		MinVersion: tls.VersionTLS12,
		RootCAs:    e.serverPool,
		ServerName: "localhost",
	}
	if clientCert != nil {
		cfg.Certificates = []tls.Certificate{*clientCert}
	}
	return cfg
}

func (e *loopbackTLSEnv) dial(t *testing.T, clientCert *tls.Certificate) *grpc.ClientConn {
	t.Helper()
	conn, err := grpc.NewClient(e.endpoint(), grpc.WithTransportCredentials(credentials.NewTLS(e.clientTLS(clientCert))))
	if err != nil {
		t.Fatalf("create loopback gRPC client: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func networkAuthContext(parent context.Context, token string) context.Context {
	return metadata.AppendToOutgoingContext(parent, "authorization", "Bearer "+token)
}

func networkSecretContext(parent context.Context, identityToken, secretToken string) context.Context {
	return metadata.AppendToOutgoingContext(parent,
		"authorization", "Bearer "+identityToken,
		"x-kms-secret-token", secretToken,
	)
}

func networkNS(env, app string) *kmsv1.NamespaceRef {
	return &kmsv1.NamespaceRef{Env: env, App: app}
}

func networkRef(env, app, key string) *kmsv1.ResourceRef {
	return &kmsv1.ResourceRef{Namespace: networkNS(env, app), Key: key}
}

func newLoopbackServerCertificate(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate loopback server key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "localhost"},
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create loopback server certificate: %v", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal loopback server key: %v", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("load loopback server key pair: %v", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(certPEM) {
		t.Fatal("trust loopback server certificate")
	}
	return pair, pool
}
