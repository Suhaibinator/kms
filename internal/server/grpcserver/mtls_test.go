package grpcserver

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
	"github.com/Suhaibinator/kms/internal/core"
	"github.com/Suhaibinator/kms/internal/crypto"
	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/storage"
	"github.com/Suhaibinator/kms/internal/watch"
)

// TestMTLS_ValidCertWhoAmIAndImplicitRead exercises the full mTLS path: a client
// presenting a CA-issued certificate is mapped to its identity (method mtls),
// discovers its bound namespace via WhoAmI, and reads a parameter in that
// mTLS-only namespace under the implicit home-namespace grant — with no bearer
// token and no policy.
func TestMTLS_ValidCertWhoAmIAndImplicitRead(t *testing.T) {
	env := newTLSTestEnv(t)
	ns := domain.NamespaceRef{Env: "prod", App: "svc"}
	env.store.addNamespace(ns, domain.AuthMethodMTLS)
	cert, _ := env.issueClientCert(t, "svc", &ns)

	// Seed a parameter via the admin (token, no client cert — allowed since the
	// admin bypasses the method gate).
	adminConn := env.dial(t, nil)
	if _, err := kmsv1.NewParameterServiceClient(adminConn).PutParameter(adminCtx(),
		&kmsv1.PutParameterRequest{Ref: pRef("prod", "svc", "rate-limit"), Value: "100"}); err != nil {
		t.Fatalf("seed put: %v", err)
	}

	conn := env.dial(t, &cert) // no bearer token; identity comes from the cert
	admin := kmsv1.NewAdminServiceClient(conn)

	who, err := admin.WhoAmI(context.Background(), &kmsv1.WhoAmIRequest{})
	if err != nil {
		t.Fatalf("whoami: %v", err)
	}
	if who.GetName() != "svc" || who.GetAuthMethod() != string(domain.AuthMethodMTLS) {
		t.Fatalf("whoami = %+v, want name=svc method=mtls", who)
	}
	if who.GetNamespace().GetEnv() != "prod" || who.GetNamespace().GetApp() != "svc" {
		t.Fatalf("whoami namespace = %+v", who.GetNamespace())
	}

	resp, err := kmsv1.NewParameterServiceClient(conn).GetParameter(context.Background(),
		&kmsv1.GetParameterRequest{Ref: pRef("prod", "svc", "rate-limit")})
	if err != nil {
		t.Fatalf("cert client read: %v", err)
	}
	if resp.GetParameter().GetValue() != "100" {
		t.Fatalf("value = %q, want 100", resp.GetParameter().GetValue())
	}
}

func TestMTLS_WatchHeartbeatReauthorizesEnrolledCertificate(t *testing.T) {
	env := newTLSTestEnv(t)
	ns := domain.NamespaceRef{Env: "prod", App: "svc"}
	env.store.addNamespace(ns, domain.AuthMethodMTLS)
	cert, _ := env.issueClientCert(t, "svc", &ns)
	conn := env.dial(t, &cert)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	stream, err := kmsv1.NewWatchServiceClient(conn).Subscribe(ctx)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if err := stream.Send(&kmsv1.SubscribeRequest{
		ClientName: "mtls-client", Namespaces: []*kmsv1.NamespaceRef{pNS(ns.Env, ns.App)},
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	first, err := stream.Recv()
	if err != nil {
		t.Fatalf("initial snapshot: %v", err)
	}
	if first.GetSnapshot() == nil {
		t.Fatalf("first event = %+v, want snapshot", first)
	}
	second, err := stream.Recv()
	if err != nil {
		t.Fatalf("heartbeat after certificate reauthorization: %v", err)
	}
	if second.GetHeartbeat() == nil {
		t.Fatalf("second event = %+v, want heartbeat", second)
	}
}

func TestMTLS_ReleaseWatchHeartbeatRejectsRevokedCertificate(t *testing.T) {
	ctx := context.Background()
	st, err := storage.Open(filepath.Join(t.TempDir(), "release-mtls.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	ns := domain.NamespaceRef{Env: "prod", App: "svc"}
	if _, err := st.CreateNamespace(ctx, domain.Namespace{NamespaceRef: ns, AllowedAuthMethods: []domain.AuthMethod{domain.AuthMethodMTLS}}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateIdentity(ctx, storage.CreateIdentityParams{Name: "admin", Kind: domain.IdentityKindAdmin, TokenHash: crypto.TokenHash(adminToken)}); err != nil {
		t.Fatal(err)
	}
	svc := core.New(st, nil, "test")
	kek, err := crypto.NewKEKFromMaterial("kek", make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	svc.SetKeyring(crypto.NewKeyring(kek))
	// This test seeds the release through a token-only admin; the admin
	// client-certificate requirement has its own suite (admin_mtls_test.go).
	svc.SetAdminRequireClientCert(false)
	if err := svc.BootstrapCA(ctx); err != nil {
		t.Fatal(err)
	}
	issued, err := svc.CreateIdentity(ctx, core.Principal{
		Identity: domain.Identity{Name: "admin", Kind: domain.IdentityKindAdmin}, Method: domain.AuthMethodToken,
	}, core.CreateIdentityInput{
		Name: "svc", Kind: domain.IdentityKindClient, Namespace: &ns, AuthMethods: []domain.AuthMethod{domain.AuthMethodMTLS},
	})
	if err != nil {
		t.Fatal(err)
	}
	cert, err := tls.X509KeyPair([]byte(issued.Cert.CertPEM), []byte(issued.Cert.KeyPEM))
	if err != nil {
		t.Fatal(err)
	}

	hub := watch.NewHub(st, nil, watch.Options{HeartbeatInterval: 40 * time.Millisecond, MissedHeartbeats: 25, PruneInterval: time.Hour})
	svc.SetHub(hub)
	hubCtx, hubCancel := context.WithCancel(ctx)
	defer hubCancel()
	go func() { _ = hub.Run(hubCtx) }()
	<-hub.Started()
	serverCert, serverPool := genServerCert(t)
	caPool, err := svc.CACertPool()
	if err != nil {
		t.Fatal(err)
	}
	srv, lis := serveBufconn(t, svc, hub, Config{TLS: &tls.Config{
		MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{serverCert},
		ClientCAs: caPool, ClientAuth: tls.VerifyClientCertIfGiven,
	}})
	_ = srv
	dial := func(clientCert *tls.Certificate) *grpc.ClientConn {
		t.Helper()
		tlsCfg := &tls.Config{RootCAs: serverPool, ServerName: "bufnet", MinVersion: tls.VersionTLS12}
		if clientCert != nil {
			tlsCfg.Certificates = []tls.Certificate{*clientCert}
		}
		conn, err := grpc.NewClient("passthrough:///bufnet",
			grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
			grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)),
		)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		return conn
	}

	adminConn := dial(nil)
	if _, err := kmsv1.NewParameterServiceClient(adminConn).PutParameter(adminCtx(), &kmsv1.PutParameterRequest{
		Ref: pRef(ns.Env, ns.App, "config"), Value: `{"enabled":true}`, ContentType: "json",
	}); err != nil {
		t.Fatalf("seed release parameter: %v", err)
	}
	adminReleases := kmsv1.NewConfigurationReleaseServiceClient(adminConn)
	created, err := adminReleases.CreateRelease(adminCtx(), &kmsv1.CreateReleaseRequest{
		Namespace: pNS(ns.Env, ns.App), Name: "runtime", Entries: []*kmsv1.ReleaseEntrySelector{{
			Alias: "settings", Kind: domain.ReleaseEntryParameter, Ref: pRef(ns.Env, ns.App, "config"), Label: domain.LabelCurrent,
		}},
	})
	if err != nil {
		t.Fatalf("create release: %v", err)
	}
	active, err := adminReleases.ActivateRelease(adminCtx(), &kmsv1.ActivateReleaseRequest{
		Namespace: pNS(ns.Env, ns.App), Name: "runtime", Version: created.GetRelease().GetVersion(),
	})
	if err != nil || !active.GetChanged() {
		t.Fatalf("activate release = %+v err=%v", active, err)
	}

	conn := dial(&cert)
	streamCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	stream, err := kmsv1.NewConfigurationReleaseServiceClient(conn).WatchRelease(streamCtx)
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(&kmsv1.WatchReleaseRequest{Request: &kmsv1.WatchReleaseRequest_Register{Register: &kmsv1.ReleaseWatchRegistration{
		Namespace: pNS(ns.Env, ns.App), Name: "runtime", ClientName: "mtls-client", InstanceId: "replica-1",
	}}}); err != nil {
		t.Fatal(err)
	}
	if event, err := stream.Recv(); err != nil || event.GetSnapshot() == nil {
		t.Fatalf("initial release snapshot = %+v err=%v", event, err)
	}
	if err := st.RevokeIdentityCert(context.Background(), issued.Cert.Serial); err != nil {
		t.Fatalf("revoke certificate: %v", err)
	}
	for {
		_, err := stream.Recv()
		if err == nil {
			continue
		}
		if codeOf(err) != codes.Unauthenticated {
			t.Fatalf("release stream close code = %v, want Unauthenticated (%v)", codeOf(err), err)
		}
		break
	}
}

func TestMTLS_RevokedCertRejected(t *testing.T) {
	env := newTLSTestEnv(t)
	ns := domain.NamespaceRef{Env: "prod", App: "svc"}
	env.store.addNamespace(ns, domain.AuthMethodMTLS)
	cert, serial := env.issueClientCert(t, "svc", &ns)

	if err := env.store.RevokeIdentityCert(context.Background(), serial); err != nil {
		t.Fatalf("revoke cert: %v", err)
	}
	conn := env.dial(t, &cert)
	_, err := kmsv1.NewAdminServiceClient(conn).WhoAmI(context.Background(), &kmsv1.WhoAmIRequest{})
	if codeOf(err) != codes.Unauthenticated {
		t.Fatalf("revoked cert: code = %v, want Unauthenticated", codeOf(err))
	}
}

func TestMTLS_DisabledIdentityRejected(t *testing.T) {
	env := newTLSTestEnv(t)
	ns := domain.NamespaceRef{Env: "prod", App: "svc"}
	env.store.addNamespace(ns, domain.AuthMethodMTLS)
	cert, _ := env.issueClientCert(t, "svc", &ns)

	if err := env.store.SetIdentityDisabled(context.Background(), "svc", true); err != nil {
		t.Fatalf("disable: %v", err)
	}
	conn := env.dial(t, &cert)
	_, err := kmsv1.NewAdminServiceClient(conn).WhoAmI(context.Background(), &kmsv1.WhoAmIRequest{})
	if codeOf(err) != codes.Unauthenticated {
		t.Fatalf("disabled identity: code = %v, want Unauthenticated", codeOf(err))
	}
}

func TestMTLS_GetCACertificateIsPublic(t *testing.T) {
	env := newTLSTestEnv(t)
	conn := env.dial(t, nil)
	// No credentials attached at all.
	resp, err := kmsv1.NewAdminServiceClient(conn).GetCACertificate(context.Background(), &kmsv1.GetCACertificateRequest{})
	if err != nil {
		t.Fatalf("get ca certificate: %v", err)
	}
	if resp.GetCertPem() == "" {
		t.Fatal("expected a CA certificate PEM")
	}
}

// TestPeerCertFromContext covers the interceptor's cert-extraction helper on the
// paths that do not arise over a real mTLS connection: no peer, and a peer with
// no TLS auth info.
func TestPeerCertFromContext(t *testing.T) {
	if got := peerCertFromContext(context.Background()); got != nil {
		t.Fatalf("no peer: got %v, want nil", got)
	}
	ctx := peer.NewContext(context.Background(), &peer.Peer{})
	if got := peerCertFromContext(ctx); got != nil {
		t.Fatalf("peer without TLS: got %v, want nil", got)
	}
	// A TLS peer with no presented certificate (token-only client) yields nil.
	ctx = peer.NewContext(context.Background(), &peer.Peer{
		AuthInfo: credentials.TLSInfo{State: tls.ConnectionState{PeerCertificates: []*x509.Certificate{}}},
	})
	if got := peerCertFromContext(ctx); got != nil {
		t.Fatalf("TLS peer without cert: got %v, want nil", got)
	}
	// A certificate that was presented but never chain-verified is not a
	// credential. Under VerifyClientCertIfGiven the TLS stack fills both fields
	// together, but a future listener mode (RequestClientCert) would present a
	// leaf with no verified chain, and that must never authenticate anyone.
	ctx = peer.NewContext(context.Background(), &peer.Peer{
		AuthInfo: credentials.TLSInfo{State: tls.ConnectionState{
			PeerCertificates: []*x509.Certificate{{}},
		}},
	})
	if got := peerCertFromContext(ctx); got != nil {
		t.Fatalf("unverified chain: got %v, want nil", got)
	}
	// The mirror case: a verified chain with no leaf to attribute it to.
	ctx = peer.NewContext(context.Background(), &peer.Peer{
		AuthInfo: credentials.TLSInfo{State: tls.ConnectionState{
			VerifiedChains: [][]*x509.Certificate{{{}}},
		}},
	})
	if got := peerCertFromContext(ctx); got != nil {
		t.Fatalf("verified chain without a leaf: got %v, want nil", got)
	}
}
