package listenertls

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"io"
	"math/big"
	"net"
	"sync"
	"testing"
	"time"
)

// These tests drive Reloadable over real TLS handshakes on loopback rather than
// only inspecting the configs it hands out: the whole point of the type is that
// a Swap changes what the *next* handshake sees while established connections
// keep the state they handshook with, and only a real handshake shows that.

// serverKeyPair returns a self-signed loopback server certificate carrying the
// given serial — distinct serials are how a test tells one keypair from the
// other across a rotation — together with the parsed leaf.
func serverKeyPair(t *testing.T, serial int64) (tls.Certificate, *x509.Certificate) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate server key: %v", err)
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
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, pub, priv)
	if err != nil {
		t.Fatalf("create server certificate: %v", err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse server certificate: %v", err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: priv, Leaf: leaf}, leaf
}

// operatorCA is a throwaway client CA standing in for the bundle
// security.client_ca_file loads: the certificate plus the key that signs leaves
// for it, so a test can mint a client certificate that chains to exactly one of
// two authorities.
type operatorCA struct {
	cert *x509.Certificate
	key  ed25519.PrivateKey
}

func newOperatorCA(t *testing.T, cn string, serial int64) operatorCA {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate CA key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(serial),
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign,
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, pub, priv)
	if err != nil {
		t.Fatalf("create CA certificate: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse CA certificate: %v", err)
	}
	return operatorCA{cert: cert, key: priv}
}

// pool returns a client-CA pool holding just this authority.
func (ca operatorCA) pool() *x509.CertPool {
	pool := x509.NewCertPool()
	pool.AddCert(ca.cert)
	return pool
}

// issueClient mints a client certificate chaining to ca.
func (ca operatorCA) issueClient(t *testing.T, cn string) tls.Certificate {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate client key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(100),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, pub, ca.key)
	if err != nil {
		t.Fatalf("create client certificate: %v", err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: priv}
}

// derivedFor builds the listener configuration exactly as serve does: the
// operator's server keypair and optional client CA go through
// Config.BuildServerTLS' shape, then Build adds the built-in CA and settles on
// VerifyClientCertIfGiven.
func derivedFor(t *testing.T, pair tls.Certificate, clientCAs *x509.CertPool) *tls.Config {
	t.Helper()
	base := &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{pair}}
	if clientCAs != nil {
		base.ClientCAs = clientCAs
		base.ClientAuth = tls.RequireAndVerifyClientCert
	}
	derived, err := Build(base, newService(t))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return derived
}

// echoServer serves cfg on a loopback listener, echoing every byte back until
// the peer closes. It is the smallest server that shows both a completed
// handshake and a connection that keeps working across a Swap.
func echoServer(t *testing.T, cfg *tls.Config) string {
	t.Helper()
	lis, err := tls.Listen("tcp", "127.0.0.1:0", cfg)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = lis.Close() })
	go func() {
		for {
			conn, err := lis.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()
	return lis.Addr().String()
}

// roundTrip writes one byte and reads it back. A handshake the server only
// rejects after sending its own Finished — TLS 1.3 client authentication — does
// not fail at Dial, so the refusal has to be observed on the first exchange.
func roundTrip(conn *tls.Conn) error {
	if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		return err
	}
	if _, err := conn.Write([]byte{0x2a}); err != nil {
		return err
	}
	buf := make([]byte, 1)
	if _, err := io.ReadFull(conn, buf); err != nil {
		return err
	}
	if buf[0] != 0x2a {
		return fmt.Errorf("echoed %#x, want 0x2a", buf[0])
	}
	return nil
}

// dialServer opens a connection and completes one round trip, returning the
// live connection so the caller can keep using it across a Swap.
func dialServer(t *testing.T, addr string, cfg *tls.Config) (*tls.Conn, error) {
	t.Helper()
	conn, err := tls.Dial("tcp", addr, cfg)
	if err != nil {
		return nil, err
	}
	if err := roundTrip(conn); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return conn, nil
}

// serverSerial reports the serial of the certificate the peer presented.
func serverSerial(t *testing.T, conn *tls.Conn) string {
	t.Helper()
	certs := conn.ConnectionState().PeerCertificates
	if len(certs) == 0 {
		t.Fatal("peer presented no certificate")
	}
	return certs[0].SerialNumber.String()
}

// trusting returns a client config trusting every given server certificate, so
// one client can verify the server across a keypair rotation.
func trusting(leaves ...*x509.Certificate) *tls.Config {
	pool := x509.NewCertPool()
	for _, leaf := range leaves {
		pool.AddCert(leaf)
	}
	return &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
}

// TestNewReloadableNilDerived: TLS off yields a nil holder whose Listener is
// nil, so serve can hand the result straight to a plaintext listener without a
// second branch.
func TestNewReloadableNilDerived(t *testing.T) {
	holder := NewReloadable(nil)
	if holder != nil {
		t.Fatalf("NewReloadable(nil) = %v, want nil", holder)
	}
	if cfg := holder.Listener("h2"); cfg != nil {
		t.Errorf("nil holder Listener = %v, want nil", cfg)
	}
	if cfg := holder.Current(); cfg != nil {
		t.Errorf("nil holder Current = %v, want nil", cfg)
	}
}

// TestListenerStubCarriesNoKeyMaterial pins the indirection: the config the
// listener keeps forever holds nothing that a reload has to change — every
// per-handshake decision comes from GetConfigForClient.
func TestListenerStubCarriesNoKeyMaterial(t *testing.T) {
	pair, _ := serverKeyPair(t, 1)
	holder := NewReloadable(derivedFor(t, pair, nil))

	stub := holder.Listener("h2")
	if len(stub.Certificates) != 0 {
		t.Errorf("stub carries %d certificates, want 0", len(stub.Certificates))
	}
	if stub.ClientCAs != nil {
		t.Error("stub carries a ClientCAs pool; a reload could not replace it")
	}
	if stub.GetConfigForClient == nil {
		t.Fatal("stub has no GetConfigForClient; the listener would serve nothing")
	}
	if stub.MinVersion != tls.VersionTLS12 {
		t.Errorf("stub MinVersion = %#x, want TLS 1.2", stub.MinVersion)
	}
	// The slot behind the stub is the config that actually governs a handshake.
	slot, err := stub.GetConfigForClient(&tls.ClientHelloInfo{})
	if err != nil {
		t.Fatalf("GetConfigForClient: %v", err)
	}
	if len(slot.Certificates) != 1 {
		t.Fatalf("slot carries %d certificates, want 1", len(slot.Certificates))
	}
	if len(slot.NextProtos) != 1 || slot.NextProtos[0] != "h2" {
		t.Errorf("slot NextProtos = %v, want [h2]", slot.NextProtos)
	}
}

// TestSwapRotatesServerCertificate is the cert-rotation statement: new
// handshakes see the new keypair, and a connection opened before the swap keeps
// the one it handshook with.
func TestSwapRotatesServerCertificate(t *testing.T) {
	pairA, leafA := serverKeyPair(t, 11)
	pairB, leafB := serverKeyPair(t, 22)
	holder := NewReloadable(derivedFor(t, pairA, nil))
	addr := echoServer(t, holder.Listener("h2"))
	client := trusting(leafA, leafB)

	before, err := dialServer(t, addr, client)
	if err != nil {
		t.Fatalf("dial before swap: %v", err)
	}
	defer func() { _ = before.Close() }()
	if got := serverSerial(t, before); got != "11" {
		t.Fatalf("serial before swap = %s, want 11", got)
	}

	derivedB := derivedFor(t, pairB, nil)
	holder.Swap(derivedB)
	if holder.Current() != derivedB {
		t.Error("Current does not return the config passed to Swap")
	}

	after, err := dialServer(t, addr, client)
	if err != nil {
		t.Fatalf("dial after swap: %v", err)
	}
	defer func() { _ = after.Close() }()
	if got := serverSerial(t, after); got != "22" {
		t.Fatalf("serial after swap = %s, want 22", got)
	}

	// The connection established under certificate A is untouched: it still
	// carries traffic, and it still reports the certificate it handshook with.
	if err := roundTrip(before); err != nil {
		t.Fatalf("connection opened before the swap stopped working: %v", err)
	}
	if got := serverSerial(t, before); got != "11" {
		t.Errorf("pre-swap connection now reports serial %s, want 11", got)
	}
}

// TestListenerALPNPerSlot: ALPN is negotiated from the config
// GetConfigForClient returns, so each listener needs its own slot — gRPC
// insists on h2, while the browser-facing listener must also speak http/1.1.
func TestListenerALPNPerSlot(t *testing.T) {
	pair, leaf := serverKeyPair(t, 33)
	holder := NewReloadable(derivedFor(t, pair, nil))
	grpcAddr := echoServer(t, holder.Listener("h2"))
	httpAddr := echoServer(t, holder.Listener("h2", "http/1.1"))

	offering := func(protos ...string) *tls.Config {
		cfg := trusting(leaf)
		cfg.NextProtos = protos
		return cfg
	}

	for _, tc := range []struct {
		name string
		addr string
	}{
		{"grpc slot", grpcAddr},
		{"http slot", httpAddr},
	} {
		conn, err := dialServer(t, tc.addr, offering("h2", "http/1.1"))
		if err != nil {
			t.Fatalf("%s: dial: %v", tc.name, err)
		}
		if got := conn.ConnectionState().NegotiatedProtocol; got != "h2" {
			t.Errorf("%s: negotiated %q, want h2", tc.name, got)
		}
		_ = conn.Close()
	}

	// A client speaking only HTTP/1.1 gets http/1.1 from the browser-facing
	// slot. Against the h2-only slot the standard library's deliberate
	// http/1.1 fallback keeps the connection but negotiates nothing, which is
	// exactly the state in which a gRPC client gives up — the two slots are
	// not interchangeable.
	conn, err := dialServer(t, httpAddr, offering("http/1.1"))
	if err != nil {
		t.Fatalf("http/1.1-only client refused by the HTTP slot: %v", err)
	}
	defer func() { _ = conn.Close() }()
	if got := conn.ConnectionState().NegotiatedProtocol; got != "http/1.1" {
		t.Errorf("HTTP slot negotiated %q, want http/1.1", got)
	}
	grpcConn, err := dialServer(t, grpcAddr, offering("http/1.1"))
	if err != nil {
		t.Fatalf("http/1.1-only client against the h2 slot: %v", err)
	}
	defer func() { _ = grpcConn.Close() }()
	if got := grpcConn.ConnectionState().NegotiatedProtocol; got != "" {
		t.Errorf("h2-only slot negotiated %q for an http/1.1-only client, want none", got)
	}

	// A protocol with no fallback shows the slot's own NextProtos are what the
	// handshake consults: the h2 slot refuses it outright.
	if conn, err := dialServer(t, grpcAddr, offering("spdy/3")); err == nil {
		_ = conn.Close()
		t.Error("the h2-only slot accepted a client offering only spdy/3")
	}
}

// TestSwapReplacesClientCAPool: rotating security.client_ca_file must change
// which client certificates complete a handshake. ClientAuth stays
// VerifyClientCertIfGiven either way (Build's policy), so a client presenting a
// certificate from a retired authority is refused while one presenting none
// still connects.
func TestSwapReplacesClientCAPool(t *testing.T) {
	pair, leaf := serverKeyPair(t, 44)
	caOne := newOperatorCA(t, "operator-ca-1", 1)
	caTwo := newOperatorCA(t, "operator-ca-2", 2)
	derivedOne := derivedFor(t, pair, caOne.pool())
	holder := NewReloadable(derivedOne)
	addr := echoServer(t, holder.Listener("h2"))

	if derivedOne.ClientAuth != tls.VerifyClientCertIfGiven {
		t.Fatalf("derived ClientAuth = %v, want VerifyClientCertIfGiven", derivedOne.ClientAuth)
	}
	// GetClientCertificate rather than Certificates: the standard library's
	// client silently sends an empty certificate when its chain matches none of
	// the acceptable CAs the server advertises, which would make the swap look
	// like it worked no matter what the server actually verifies. Forcing the
	// certificate onto the wire puts the decision back where it belongs.
	certOne := caOne.issueClient(t, "svc")
	withCertOne := trusting(leaf)
	withCertOne.GetClientCertificate = func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
		return &certOne, nil
	}

	conn, err := dialServer(t, addr, withCertOne)
	if err != nil {
		t.Fatalf("client certificate from CA 1 refused while CA 1 is trusted: %v", err)
	}
	_ = conn.Close()

	derivedTwo := derivedFor(t, pair, caTwo.pool())
	holder.Swap(derivedTwo)
	if holder.Current() != derivedTwo {
		t.Error("Current does not return the config passed to Swap")
	}

	if conn, err := dialServer(t, addr, withCertOne); err == nil {
		_ = conn.Close()
		t.Error("client certificate from the retired CA still completes a handshake")
	}
	// The certificate is optional, not demanded: a token-only client must still
	// reach the login endpoint after the pool changes.
	tokenOnly, err := dialServer(t, addr, trusting(leaf))
	if err != nil {
		t.Fatalf("certificate-free client refused after the pool swap: %v", err)
	}
	defer func() { _ = tokenOnly.Close() }()
	// And the built-in CA survives every swap — it is added by Build, not by
	// the operator's bundle.
	if !verifiesAgainst(t, caTwo.cert, holder.Current().ClientCAs) {
		t.Error("the new operator CA is missing from the swapped pool")
	}
	if verifiesAgainst(t, caOne.cert, holder.Current().ClientCAs) {
		t.Error("the retired operator CA is still in the swapped pool")
	}
}

// TestSwapDuringHandshakes exercises the atomic pointer under -race: a reload
// can land at any moment, including mid-handshake on several connections.
func TestSwapDuringHandshakes(t *testing.T) {
	pairA, leafA := serverKeyPair(t, 55)
	pairB, leafB := serverKeyPair(t, 66)
	derivedA := derivedFor(t, pairA, nil)
	derivedB := derivedFor(t, pairB, nil)
	holder := NewReloadable(derivedA)
	addr := echoServer(t, holder.Listener("h2"))
	client := trusting(leafA, leafB)

	done := make(chan struct{})
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-done:
					return
				default:
				}
				conn, err := dialServer(t, addr, client)
				if err != nil {
					// t.Errorf is safe from a goroutine; Fatalf is not.
					t.Errorf("handshake during reload: %v", err)
					return
				}
				_ = conn.Close()
			}
		}()
	}
	for i := range 20 {
		if i%2 == 0 {
			holder.Swap(derivedB)
		} else {
			holder.Swap(derivedA)
		}
		time.Sleep(time.Millisecond)
	}
	close(done)
	wg.Wait()
}
