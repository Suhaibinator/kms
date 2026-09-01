package grpcserver

import (
	"context"
	"crypto/tls"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
	"github.com/Suhaibinator/kms/internal/core"
	"github.com/Suhaibinator/kms/internal/domain"
)

// issueAdminCert mints a client certificate for the harness's seeded admin
// through the offline-only issuance path, the way `parameter-store admin-cert
// issue` does. The seeded admin is token-only, so this is what turns it into an
// admin that can authenticate under the client-certificate requirement.
func (e *tlsTestEnv) issueAdminCert(t *testing.T) tls.Certificate {
	t.Helper()
	cliPr := core.Principal{Identity: domain.Identity{Name: "cli", Kind: domain.IdentityKindAdmin}}
	bundle, err := e.svc.IssueLocalAdminCertificate(context.Background(), cliPr, "admin", 0)
	if err != nil {
		t.Fatalf("issue admin cert: %v", err)
	}
	pair, err := tls.X509KeyPair([]byte(bundle.CertPEM), []byte(bundle.KeyPEM))
	if err != nil {
		t.Fatalf("load admin key pair: %v", err)
	}
	return pair
}

// newAdminCertEnv returns a TLS environment with the admin client-certificate
// requirement in force and a certificate issued for the seeded admin.
func newAdminCertEnv(t *testing.T) (*tlsTestEnv, tls.Certificate) {
	t.Helper()
	env := newTLSTestEnv(t)
	cert := env.issueAdminCert(t)
	env.svc.SetAdminRequireClientCert(true)
	return env, cert
}

func whoAmI(ctx context.Context, conn *grpc.ClientConn) (*kmsv1.WhoAmIResponse, error) {
	return kmsv1.NewAdminServiceClient(conn).WhoAmI(ctx, &kmsv1.WhoAmIRequest{})
}

// TestAdminCert_TokenOnlyRejected is the whole point of the feature: a stolen
// admin token, replayed from a machine with no certificate, buys nothing.
func TestAdminCert_TokenOnlyRejected(t *testing.T) {
	env, _ := newAdminCertEnv(t)
	conn := env.dial(t, nil)

	_, err := whoAmI(adminCtx(), conn)
	if codeOf(err) != codes.Unauthenticated {
		t.Fatalf("token-only admin: code = %v, want Unauthenticated (%v)", codeOf(err), err)
	}
}

// TestAdminCert_CertOnlyRejected is the mirror case: a stolen certificate and
// key, without the token, is equally useless.
func TestAdminCert_CertOnlyRejected(t *testing.T) {
	env, cert := newAdminCertEnv(t)
	conn := env.dial(t, &cert)

	_, err := whoAmI(context.Background(), conn)
	if codeOf(err) != codes.Unauthenticated {
		t.Fatalf("cert-only admin: code = %v, want Unauthenticated (%v)", codeOf(err), err)
	}
}

// TestAdminCert_CertAndTokenAccepted: both credentials together authenticate,
// and the resulting principal reports the stronger method.
func TestAdminCert_CertAndTokenAccepted(t *testing.T) {
	env, cert := newAdminCertEnv(t)
	conn := env.dial(t, &cert)

	who, err := whoAmI(adminCtx(), conn)
	if err != nil {
		t.Fatalf("cert+token admin: %v", err)
	}
	if who.GetName() != "admin" || who.GetKind() != domain.IdentityKindAdmin {
		t.Errorf("whoami = %+v, want the admin identity", who)
	}
	if who.GetAuthMethod() != string(domain.AuthMethodMTLS) {
		t.Errorf("auth_method = %q, want %q", who.GetAuthMethod(), domain.AuthMethodMTLS)
	}
}

// TestAdminCert_MismatchedCredentialsRejected: an admin certificate paired with
// a different identity's valid token is not a valid combination — neither
// credential is allowed to "win".
func TestAdminCert_MismatchedCredentialsRejected(t *testing.T) {
	env, cert := newAdminCertEnv(t)
	env.store.addIdentity("client", domain.IdentityKindClient, clientToken, nil)
	conn := env.dial(t, &cert)

	_, err := whoAmI(clientCtx(), conn)
	if codeOf(err) != codes.Unauthenticated {
		t.Fatalf("admin cert + client token: code = %v, want Unauthenticated (%v)", codeOf(err), err)
	}
}

// TestAdminCert_RequirementOffAllowsTokenOnly: with the requirement disabled
// the previous behavior is intact, which is what an operator who opts out gets.
func TestAdminCert_RequirementOffAllowsTokenOnly(t *testing.T) {
	env := newTLSTestEnv(t) // buildService leaves the requirement off
	conn := env.dial(t, nil)

	who, err := whoAmI(adminCtx(), conn)
	if err != nil {
		t.Fatalf("token-only admin with the requirement off: %v", err)
	}
	if who.GetAuthMethod() != string(domain.AuthMethodToken) {
		t.Errorf("auth_method = %q, want token", who.GetAuthMethod())
	}
}

// TestAdminCert_WatchStreamClosesOnTokenRotation: an admin stream was admitted
// on both credentials, so rotating the token must tear it down on the next
// heartbeat even though the certificate is still perfectly valid.
func TestAdminCert_WatchStreamClosesOnTokenRotation(t *testing.T) {
	env, cert := newAdminCertEnv(t)
	ns := domain.NamespaceRef{Env: "prod", App: "svc"}
	env.store.addNamespace(ns)
	conn := env.dial(t, &cert)

	ctx, cancel := context.WithTimeout(adminCtx(), 5*time.Second)
	defer cancel()
	stream, err := kmsv1.NewWatchServiceClient(conn).Subscribe(ctx)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if err := stream.Send(&kmsv1.SubscribeRequest{
		ClientName: "admin-console", Namespaces: []*kmsv1.NamespaceRef{pNS(ns.Env, ns.App)},
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

	cliPr := core.Principal{Identity: domain.Identity{Name: "cli", Kind: domain.IdentityKindAdmin}}
	if _, err := env.svc.RotateIdentityToken(context.Background(), cliPr, "admin"); err != nil {
		t.Fatalf("rotate admin token: %v", err)
	}
	for {
		_, err := stream.Recv()
		if err == nil {
			continue // heartbeats until the reauthorization tick notices
		}
		if codeOf(err) != codes.Unauthenticated {
			t.Fatalf("stream close code = %v, want Unauthenticated (%v)", codeOf(err), err)
		}
		break
	}
}

// TestAdminCert_CreateIdentityRejectsAdminMTLS: the online API must not be able
// to mint an admin certificate, so asking for one is an argument error rather
// than a silently token-only identity.
func TestAdminCert_CreateIdentityRejectsAdminMTLS(t *testing.T) {
	env, cert := newAdminCertEnv(t)
	conn := env.dial(t, &cert)

	_, err := kmsv1.NewAdminServiceClient(conn).CreateIdentity(adminCtx(), &kmsv1.CreateIdentityRequest{
		Name:        "ops",
		Kind:        domain.IdentityKindAdmin,
		AuthMethods: []string{string(domain.AuthMethodMTLS)},
	})
	if codeOf(err) != codes.InvalidArgument {
		t.Fatalf("create admin identity with mtls: code = %v, want InvalidArgument (%v)", codeOf(err), err)
	}
}

// TestAdminCert_IssueCertRejectsAdminTarget: online issuance for an admin
// target is refused outright — an attacker holding both live admin credentials
// still cannot mint a durable new one over the network.
func TestAdminCert_IssueCertRejectsAdminTarget(t *testing.T) {
	env, cert := newAdminCertEnv(t)
	conn := env.dial(t, &cert)

	_, err := kmsv1.NewAdminServiceClient(conn).IssueIdentityCertificate(adminCtx(),
		&kmsv1.IssueIdentityCertificateRequest{Name: "admin"})
	if codeOf(err) != codes.PermissionDenied {
		t.Fatalf("issue cert for an admin target: code = %v, want PermissionDenied (%v)", codeOf(err), err)
	}
}
