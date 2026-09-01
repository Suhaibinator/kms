package integration

import (
	"context"
	"crypto/tls"
	"encoding/json/v2"
	"io"
	"net/http"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
	"github.com/Suhaibinator/kms/internal/core"
	"github.com/Suhaibinator/kms/internal/crypto"
	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/storage"
)

// adminCertEnv returns a loopback environment with the admin
// client-certificate requirement enforced and a certificate minted for the
// seeded admin through the offline-only path.
func adminCertEnv(t *testing.T) (*loopbackTLSEnv, tls.Certificate) {
	t.Helper()
	e := newLoopbackTLSEnv(t)
	cliPr := core.Principal{Identity: domain.Identity{Name: "cli", Kind: domain.IdentityKindAdmin}}
	bundle, err := e.svc.IssueLocalAdminCertificate(context.Background(), cliPr, "network-root", 0)
	if err != nil {
		t.Fatalf("issue admin certificate: %v", err)
	}
	pair, err := tls.X509KeyPair([]byte(bundle.CertPEM), []byte(bundle.KeyPEM))
	if err != nil {
		t.Fatalf("load admin key pair: %v", err)
	}
	e.svc.SetAdminRequireClientCert(true)
	return e, pair
}

// getJSON performs a GET over the loopback HTTPS listener, optionally
// presenting clientCert in the handshake and a bearer token, and returns the
// status and decoded body.
func getJSON(t *testing.T, e *loopbackTLSEnv, path, token string, clientCert *tls.Certificate) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, e.httpsURL(path), nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := e.httpClient(clientCert).Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	var body map[string]any
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatalf("decode body %q: %v", raw, err)
		}
	}
	return resp.StatusCode, body
}

// seedClientIdentity creates a token-method client identity and returns its token.
func seedClientIdentity(t *testing.T, e *loopbackTLSEnv, name string) string {
	t.Helper()
	token, hash, err := crypto.GenerateToken("kms")
	if err != nil {
		t.Fatalf("generate client token: %v", err)
	}
	if _, err := e.store.CreateIdentity(context.Background(), storage.CreateIdentityParams{
		Name: name, Kind: domain.IdentityKindClient, TokenHash: hash,
	}); err != nil {
		t.Fatalf("create client identity: %v", err)
	}
	return token
}

// TestHTTPSAdminCertMatrix drives the admin credential combinations over a real
// TLS handshake on the HTTPS listener — the path the browser console takes.
func TestHTTPSAdminCertMatrix(t *testing.T) {
	e, cert := adminCertEnv(t)

	t.Run("cert and token", func(t *testing.T) {
		code, body := getJSON(t, e, "/api/v1/whoami", e.adminToken, &cert)
		if code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body=%v", code, body)
		}
		if body["name"] != "network-root" || body["auth_method"] != string(domain.AuthMethodMTLS) {
			t.Fatalf("whoami = %v, want network-root with auth_method mtls", body)
		}
	})

	t.Run("token only", func(t *testing.T) {
		// The certificate is simply absent from the handshake: this is a stolen
		// token replayed from a machine that has no key.
		code, _ := getJSON(t, e, "/api/v1/whoami", e.adminToken, nil)
		if code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", code)
		}
	})

	t.Run("cert only", func(t *testing.T) {
		code, _ := getJSON(t, e, "/api/v1/whoami", "", &cert)
		if code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", code)
		}
	})

	t.Run("client identity unaffected", func(t *testing.T) {
		clientToken := seedClientIdentity(t, e, "https-client")
		code, body := getJSON(t, e, "/api/v1/whoami", clientToken, nil)
		if code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body=%v", code, body)
		}
		if body["name"] != "https-client" || body["auth_method"] != string(domain.AuthMethodToken) {
			t.Fatalf("whoami = %v, want https-client with auth_method token", body)
		}
	})
}

// TestHTTPSAdminCertHealthPosture: an unauthenticated caller can learn that the
// server wants a certificate and that this connection has none, which is what
// lets the login page explain the refusal without leaking token validity.
func TestHTTPSAdminCertHealthPosture(t *testing.T) {
	e, cert := adminCertEnv(t)

	code, body := getJSON(t, e, "/api/v1/health", "", nil)
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if body["admin_client_cert_required"] != true || body["client_cert_presented"] != false {
		t.Fatalf("health without a certificate = %v", body)
	}

	code, body = getJSON(t, e, "/api/v1/health", "", &cert)
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if body["client_cert_presented"] != true {
		t.Fatalf("health with a certificate = %v", body)
	}
}

// TestHTTPSAdminCertRequirementOff: an operator who opts out keeps token-only
// admin sign-in over the same listener.
func TestHTTPSAdminCertRequirementOff(t *testing.T) {
	e, _ := adminCertEnv(t)
	e.svc.SetAdminRequireClientCert(false)

	code, body := getJSON(t, e, "/api/v1/whoami", e.adminToken, nil)
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%v", code, body)
	}
	if body["auth_method"] != string(domain.AuthMethodToken) {
		t.Fatalf("auth_method = %v, want token", body["auth_method"])
	}
}

// TestGRPCAdminCertMatrix is the same matrix over the gRPC listener: both
// transports enforce the requirement identically, since both resolve
// credentials through core.
func TestGRPCAdminCertMatrix(t *testing.T) {
	e, cert := adminCertEnv(t)
	ctx := context.Background()

	certConn := kmsv1.NewAdminServiceClient(e.dial(t, &cert))
	plainConn := kmsv1.NewAdminServiceClient(e.dial(t, nil))

	who, err := certConn.WhoAmI(networkAuthContext(ctx, e.adminToken), &kmsv1.WhoAmIRequest{})
	if err != nil {
		t.Fatalf("cert+token whoami: %v", err)
	}
	if who.GetName() != "network-root" || who.GetAuthMethod() != string(domain.AuthMethodMTLS) {
		t.Fatalf("whoami = %+v, want network-root with auth_method mtls", who)
	}

	if _, err := plainConn.WhoAmI(networkAuthContext(ctx, e.adminToken), &kmsv1.WhoAmIRequest{}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("token-only whoami code = %v, want Unauthenticated (%v)", status.Code(err), err)
	}
	if _, err := certConn.WhoAmI(ctx, &kmsv1.WhoAmIRequest{}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("cert-only whoami code = %v, want Unauthenticated (%v)", status.Code(err), err)
	}

	clientToken := seedClientIdentity(t, e, "grpc-client")
	cwho, err := plainConn.WhoAmI(networkAuthContext(ctx, clientToken), &kmsv1.WhoAmIRequest{})
	if err != nil {
		t.Fatalf("client token-only whoami: %v", err)
	}
	if cwho.GetName() != "grpc-client" || cwho.GetAuthMethod() != string(domain.AuthMethodToken) {
		t.Fatalf("client whoami = %+v, want grpc-client with auth_method token", cwho)
	}
}
