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
	clientCAPool, err := svc.CACertPool()
	if err != nil {
		hubCancel()
		t.Fatalf("load client CA pool: %v", err)
	}
	server, err := grpcserver.New(svc, hub, grpcserver.Config{
		TLS: &tls.Config{
			MinVersion:   tls.VersionTLS12,
			Certificates: []tls.Certificate{serverCert},
			ClientCAs:    clientCAPool,
			ClientAuth:   tls.VerifyClientCertIfGiven,
		},
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
	}
	go func() { e.serveDone <- server.Serve(listener) }()

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
