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
	"math/big"
	"net"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	healthgrpc "google.golang.org/grpc/health/grpc_health_v1"

	"github.com/Suhaibinator/kms/internal/core"
	"github.com/Suhaibinator/kms/internal/server/listenertls"
)

// The gRPC listener never sees the reloadable config directly: serve hands it
// one stub whose GetConfigForClient returns the current slot, and
// credentials.NewTLS wraps that stub once, at construction, for the life of the
// process. This test pins that the indirection survives that wrapping — ALPN
// still negotiates h2, RPCs still complete, and a Swap reaches the next
// connection — over a real loopback socket rather than bufconn, because a
// rotation is only interesting where a new TCP connection is made.

// serverCertWithSerial returns a self-signed loopback server certificate
// carrying the given serial, which is how the test tells one keypair from the
// other across a swap.
func serverCertWithSerial(t *testing.T, serial int64) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("gen server key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(serial),
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
	return pair
}

// derivedListenerConfig builds the config serve derives for the gRPC listener:
// the operator keypair plus the built-in CA, VerifyClientCertIfGiven.
func derivedListenerConfig(t *testing.T, svc *core.Service, pair tls.Certificate) *tls.Config {
	t.Helper()
	derived, err := listenertls.Build(&tls.Config{
		MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{pair},
	}, svc)
	if err != nil {
		t.Fatalf("build listener TLS config: %v", err)
	}
	return derived
}

// handshakeRecord captures what the client observed on its connection.
type handshakeRecord struct {
	mu     sync.Mutex
	serial string
	alpn   string
}

func (h *handshakeRecord) verify(state tls.ConnectionState) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(state.PeerCertificates) > 0 {
		h.serial = state.PeerCertificates[0].SerialNumber.String()
	}
	h.alpn = state.NegotiatedProtocol
	return nil
}

func (h *handshakeRecord) observed() (serial, alpn string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.serial, h.alpn
}

// TestReloadableTLSThroughCredentials completes a health check against the
// certificate the holder serves, swaps the keypair, and shows the next
// connection getting the new one — all through the credentials the gRPC server
// was constructed with and never rebuilt.
func TestReloadableTLSThroughCredentials(t *testing.T) {
	store := newMemStore()
	svc, hub := buildService(t, store, true)
	if err := svc.BootstrapCA(context.Background()); err != nil {
		t.Fatalf("bootstrap CA: %v", err)
	}

	holder := listenertls.NewReloadable(derivedListenerConfig(t, svc, serverCertWithSerial(t, 11)))
	srv, err := New(svc, hub, Config{TLS: holder.Listener("h2")})
	if err != nil {
		t.Fatalf("New server: %v", err)
	}
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(func() {
		srv.GracefulStop()
		_ = lis.Close()
	})
	addr := lis.Addr().String()

	// check opens a fresh connection — the only way to observe a swap — and
	// completes one RPC over it, returning what the handshake showed.
	check := func(t *testing.T) (serial, alpn string) {
		t.Helper()
		rec := &handshakeRecord{}
		conn, err := grpc.NewClient("passthrough:///"+addr,
			grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "tcp", addr)
			}),
			grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{
				InsecureSkipVerify: true, // the test inspects the certificate itself
				MinVersion:         tls.VersionTLS12,
				VerifyConnection:   rec.verify,
			})))
		if err != nil {
			t.Fatalf("new client: %v", err)
		}
		defer func() { _ = conn.Close() }()

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		resp, err := healthgrpc.NewHealthClient(conn).Check(ctx, &healthgrpc.HealthCheckRequest{})
		if err != nil {
			t.Fatalf("health check: %v", err)
		}
		if resp.GetStatus() != healthgrpc.HealthCheckResponse_SERVING {
			t.Fatalf("health status = %v, want SERVING", resp.GetStatus())
		}
		return rec.observed()
	}

	serial, alpn := check(t)
	if serial != "11" {
		t.Fatalf("served serial = %s, want 11", serial)
	}
	// credentials.NewTLS offers only h2; the slot must agree, or no gRPC client
	// could ever connect.
	if alpn != "h2" {
		t.Fatalf("negotiated %q, want h2", alpn)
	}

	holder.Swap(derivedListenerConfig(t, svc, serverCertWithSerial(t, 22)))

	serial, alpn = check(t)
	if serial != "22" {
		t.Errorf("served serial after the swap = %s, want 22", serial)
	}
	if alpn != "h2" {
		t.Errorf("negotiated %q after the swap, want h2", alpn)
	}
}
