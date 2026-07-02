package paramstore

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestGetParameterAndSecret(t *testing.T) {
	c, srv := newTestClient(t, Config{Token: "identity-token"})
	srv.SetParameter("/prod/rate-limit", "100")
	srv.SetSecret("/prod/db/password", []byte("hunter2"))

	ctx := context.Background()
	val, err := c.GetParameter(ctx, "/prod/rate-limit")
	if err != nil {
		t.Fatalf("GetParameter: %v", err)
	}
	if val != "100" {
		t.Errorf("GetParameter = %q, want 100", val)
	}

	sec, err := c.GetSecret(ctx, "/prod/db/password")
	if err != nil {
		t.Fatalf("GetSecret: %v", err)
	}
	if sec.StringValue() != "hunter2" {
		t.Errorf("GetSecret value = %q, want hunter2", sec.StringValue())
	}
	if sec.Path() != "/prod/db/password" {
		t.Errorf("Secret.Path = %q", sec.Path())
	}
}

func TestAuthorizationMetadata(t *testing.T) {
	c, srv := newTestClient(t, Config{Token: "identity-token"})
	srv.SetParameter("/p", "v")

	if _, err := c.GetParameter(context.Background(), "/p"); err != nil {
		t.Fatalf("GetParameter: %v", err)
	}
	md := srv.LastMetadata("GetParameter")
	got := md.Get("authorization")
	if len(got) != 1 || got[0] != "Bearer identity-token" {
		t.Errorf("authorization metadata = %v, want [Bearer identity-token]", got)
	}
}

func TestWithSecretTokenMetadata(t *testing.T) {
	c, srv := newTestClient(t, Config{Token: "identity-token"})
	srv.SetSecret("/bound", []byte("v"))

	_, err := c.GetSecret(context.Background(), "/bound", WithSecretToken("per-secret-token"))
	if err != nil {
		t.Fatalf("GetSecret: %v", err)
	}
	md := srv.LastMetadata("GetSecret")
	if got := md.Get("x-kms-secret-token"); len(got) != 1 || got[0] != "per-secret-token" {
		t.Errorf("x-kms-secret-token metadata = %v, want [per-secret-token]", got)
	}
	if got := md.Get("authorization"); len(got) != 1 || got[0] != "Bearer identity-token" {
		t.Errorf("authorization metadata = %v", got)
	}
}

func TestErrorMapping(t *testing.T) {
	c, srv := newTestClient(t, Config{})

	cases := []struct {
		code codes.Code
		want error
	}{
		{codes.NotFound, ErrNotFound},
		{codes.PermissionDenied, ErrPermissionDenied},
		{codes.Unauthenticated, ErrUnauthenticated},
		{codes.FailedPrecondition, ErrFailedPrecondition},
	}
	for _, tc := range cases {
		path := "/err/" + tc.code.String()
		srv.SetParameterError(path, status.Error(tc.code, "boom"))
		_, err := c.GetParameter(context.Background(), path)
		if !errors.Is(err, tc.want) {
			t.Errorf("code %s: got %v, want errors.Is %v", tc.code, err, tc.want)
		}
	}

	// Missing parameter maps to ErrNotFound.
	_, err := c.GetParameter(context.Background(), "/does/not/exist")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("missing param: got %v, want ErrNotFound", err)
	}
}

func TestCacheTTL(t *testing.T) {
	c, srv := newTestClient(t, Config{CacheTTL: time.Minute})
	srv.SetParameter("/cached", "first")

	if v, _ := c.GetParameter(context.Background(), "/cached"); v != "first" {
		t.Fatalf("first read = %q", v)
	}
	// Change the server value; a cached read should still return the old one.
	srv.SetParameter("/cached", "second")
	if v, _ := c.GetParameter(context.Background(), "/cached"); v != "first" {
		t.Errorf("cached read = %q, want first (served from cache)", v)
	}

	// A secret is cached similarly.
	srv.SetSecret("/cached-secret", []byte("s1"))
	if s, _ := c.GetSecret(context.Background(), "/cached-secret"); s.StringValue() != "s1" {
		t.Fatalf("first secret read = %q", s.StringValue())
	}
	srv.SetSecret("/cached-secret", []byte("s2"))
	if s, _ := c.GetSecret(context.Background(), "/cached-secret"); s.StringValue() != "s1" {
		t.Errorf("cached secret read = %q, want s1", s.StringValue())
	}
}

func TestCacheDisabled(t *testing.T) {
	c, srv := newTestClient(t, Config{}) // CacheTTL 0
	srv.SetParameter("/p", "first")
	if v, _ := c.GetParameter(context.Background(), "/p"); v != "first" {
		t.Fatalf("read = %q", v)
	}
	srv.SetParameter("/p", "second")
	if v, _ := c.GetParameter(context.Background(), "/p"); v != "second" {
		t.Errorf("read = %q, want second (no cache)", v)
	}
}

func TestPutParameterAndSecret(t *testing.T) {
	c, srv := newTestClient(t, Config{})
	ctx := context.Background()

	res, err := c.PutParameter(ctx, "/new-param", "42")
	if err != nil {
		t.Fatalf("PutParameter: %v", err)
	}
	if res.Version == 0 {
		t.Errorf("PutParameter version = 0")
	}
	if v, _ := c.GetParameter(ctx, "/new-param"); v != "42" {
		t.Errorf("readback = %q, want 42", v)
	}

	sres, err := c.PutSecret(ctx, "/new-secret", []byte("shh"), WithGenerateAccessToken(), WithClientBound())
	if err != nil {
		t.Fatalf("PutSecret: %v", err)
	}
	if sres.AccessToken == "" {
		t.Errorf("expected minted access token")
	}
	calls := srv.PutSecretCalls()
	if len(calls) != 1 || !calls[0].ClientBound || !calls[0].GenerateAccessToken {
		t.Errorf("PutSecret call not recorded with flags: %+v", calls)
	}
}

func TestNewClientValidation(t *testing.T) {
	if _, err := NewClient(Config{}); err == nil {
		t.Error("expected error for empty Endpoint")
	}
}

// TestConcurrentCloseAndSubscribe proves the L5 fix: Close and the first subs()
// no longer race on the c.sub field. Run under -race to detect a regression.
func TestConcurrentCloseAndSubscribe(t *testing.T) {
	for i := 0; i < 30; i++ {
		c, _ := newTestClient(t, Config{})
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			_ = c.subs() // writes c.sub under subMu
		}()
		go func() {
			defer wg.Done()
			_ = c.Close() // reads c.sub under subMu
		}()
		wg.Wait()
	}
}
