package kmsclient

import (
	"context"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestSecretResolutionOrder(t *testing.T) {
	c, srv := newTestClient(t, Config{})
	srv.SetSecret(testNS, "store/only", []byte("from-store"))

	t.Run("env overrides store", func(t *testing.T) {
		t.Setenv("MY_SECRET", "from-env")
		sv := SecretValue{Key: "store/only", EnvVar: "MY_SECRET", Default: "from-default"}
		if err := sv.Init(c); err != nil {
			t.Fatalf("Init: %v", err)
		}
		if sv.Value() != "from-env" {
			t.Errorf("Value = %q, want from-env", sv.Value())
		}
	})

	t.Run("store when no env", func(t *testing.T) {
		sv := SecretValue{Key: "store/only", EnvVar: "UNSET_SECRET", Default: "from-default"}
		if err := sv.Init(c); err != nil {
			t.Fatalf("Init: %v", err)
		}
		if sv.Value() != "from-store" {
			t.Errorf("Value = %q, want from-store", sv.Value())
		}
	})

	t.Run("default when store missing", func(t *testing.T) {
		sv := SecretValue{Key: "missing", Default: "from-default"}
		if err := sv.Init(c); err != nil {
			t.Fatalf("Init: %v", err)
		}
		if sv.Value() != "from-default" {
			t.Errorf("Value = %q, want from-default", sv.Value())
		}
	})

	t.Run("error when nothing resolves", func(t *testing.T) {
		sv := SecretValue{Key: "missing"}
		err := sv.Init(c)
		if err == nil {
			t.Fatal("expected error")
		}
		if !strings.Contains(err.Error(), "missing") {
			t.Errorf("error should name the key: %v", err)
		}
	})
}

func TestParameterResolutionOrder(t *testing.T) {
	c, srv := newTestClient(t, Config{})
	srv.SetParameter(testNS, "p/store", "100")

	t.Run("env overrides", func(t *testing.T) {
		t.Setenv("RATE", "999")
		pv := ParameterValue{Key: "p/store", EnvVar: "RATE", Default: "1", Static: true}
		if err := pv.Init(c); err != nil {
			t.Fatalf("Init: %v", err)
		}
		if pv.Get() != "999" {
			t.Errorf("Get = %q, want 999", pv.Get())
		}
	})

	t.Run("store", func(t *testing.T) {
		pv := ParameterValue{Key: "p/store", Default: "1", Static: true}
		if err := pv.Init(c); err != nil {
			t.Fatalf("Init: %v", err)
		}
		if pv.Get() != "100" {
			t.Errorf("Get = %q, want 100", pv.Get())
		}
	})

	t.Run("default", func(t *testing.T) {
		pv := ParameterValue{Key: "p/missing", Default: "7", Static: true}
		if err := pv.Init(c); err != nil {
			t.Fatalf("Init: %v", err)
		}
		if pv.Get() != "7" {
			t.Errorf("Get = %q, want 7", pv.Get())
		}
	})

	t.Run("error", func(t *testing.T) {
		pv := ParameterValue{Key: "p/missing", Static: true}
		if err := pv.Init(c); err == nil || !strings.Contains(err.Error(), "p/missing") {
			t.Errorf("expected error naming key, got %v", err)
		}
	})
}

// TestDefaultOnlyUsedOnNotFound verifies that a dev Default substitutes only for
// an affirmatively-absent value, never masking a store connectivity/auth error,
// unless the caller opts into FallbackToDefaultsOnError.
func TestDefaultOnlyUsedOnNotFound(t *testing.T) {
	unavailable := func() error { return status.Error(codes.Unavailable, "store down") }

	t.Run("secret: NotFound uses Default", func(t *testing.T) {
		c, _ := newTestClient(t, Config{})
		sv := SecretValue{Key: "absent", Default: "dev-value"}
		if err := sv.Init(c); err != nil {
			t.Fatalf("Init: %v", err)
		}
		if sv.Value() != "dev-value" {
			t.Errorf("Value = %q, want dev-value", sv.Value())
		}
	})

	t.Run("secret: non-NotFound error does NOT use Default", func(t *testing.T) {
		c, srv := newTestClient(t, Config{})
		srv.SetSecretError(testNS, "flaky", unavailable())
		sv := SecretValue{Key: "flaky", Default: "sk_test_dev"}
		err := sv.Init(c)
		if err == nil {
			t.Fatal("expected error when store unreachable and Default set")
		}
		if !strings.Contains(err.Error(), "flaky") {
			t.Errorf("error should name key: %v", err)
		}
		if sv.Initialized() {
			t.Error("value must not be initialized on hard error")
		}
	})

	t.Run("secret: opt-in flag restores fallback", func(t *testing.T) {
		c, srv := newTestClient(t, Config{FallbackToDefaultsOnError: true})
		srv.SetSecretError(testNS, "flaky", unavailable())
		sv := SecretValue{Key: "flaky", Default: "dev-value"}
		if err := sv.Init(c); err != nil {
			t.Fatalf("Init with fallback flag: %v", err)
		}
		if sv.Value() != "dev-value" {
			t.Errorf("Value = %q, want dev-value", sv.Value())
		}
	})

	t.Run("parameter: non-NotFound error does NOT use Default", func(t *testing.T) {
		c, srv := newTestClient(t, Config{})
		srv.SetParameterError(testNS, "flaky", unavailable())
		pv := ParameterValue{Key: "flaky", Default: "999"}
		err := pv.Init(c)
		if err == nil {
			t.Fatal("expected error")
		}
		if !strings.Contains(err.Error(), "flaky") {
			t.Errorf("error should name key: %v", err)
		}
	})

	t.Run("parameter: opt-in flag restores fallback", func(t *testing.T) {
		c, srv := newTestClient(t, Config{FallbackToDefaultsOnError: true})
		srv.SetParameterError(testNS, "flaky", unavailable())
		pv := ParameterValue{Key: "flaky", Default: "42", Static: true}
		if err := pv.Init(c); err != nil {
			t.Fatalf("Init with fallback flag: %v", err)
		}
		if pv.Get() != "42" {
			t.Errorf("Get = %q, want 42", pv.Get())
		}
	})

	t.Run("resolve: hard error with Default still fails", func(t *testing.T) {
		c, srv := newTestClient(t, Config{})
		srv.SetSecret(testNS, "ok", []byte("v"))
		srv.SetSecretError(testNS, "flaky", unavailable())
		type Cfg struct {
			OK    SecretValue
			Flaky SecretValue
		}
		cfg := &Cfg{OK: SecretValue{Key: "ok"}, Flaky: SecretValue{Key: "flaky", Default: "dev"}}
		if err := c.Resolve(context.Background(), cfg); err == nil {
			t.Fatal("expected Resolve to fail on hard error despite Default")
		}
	})
}

func TestInitIdempotent(t *testing.T) {
	c, srv := newTestClient(t, Config{})
	srv.SetSecret(testNS, "idem", []byte("v1"))

	sv := SecretValue{Key: "idem"}
	if err := sv.Init(c); err != nil {
		t.Fatalf("Init: %v", err)
	}
	// Change the underlying value; a second Init must not re-fetch.
	srv.SetSecret(testNS, "idem", []byte("v2"))
	if err := sv.Init(c); err != nil {
		t.Fatalf("second Init: %v", err)
	}
	if sv.Value() != "v1" {
		t.Errorf("Value = %q, want v1 (idempotent Init)", sv.Value())
	}
}

func TestResolveStructWalking(t *testing.T) {
	c, srv := newTestClient(t, Config{})
	srv.SetSecret(testNS, "db/password", []byte("dbpw"))
	srv.SetSecret(testNS, "stripe/key", []byte("sk_test"))
	srv.SetParameter(testNS, "rate", "50")
	srv.SetParameter(testNS, "nested/timeout", "30s")

	type Nested struct {
		Timeout ParameterValue
	}
	type Config2 struct {
		DBPassword SecretValue
		StripeKey  *SecretValue
		Rate       ParameterValue
		Nested     Nested
		NestedPtr  *Nested
		unexported SecretValue // must be skipped
		NilPtr     *Nested     // nil pointer, must be skipped safely
	}

	cfg := &Config2{
		DBPassword: SecretValue{Key: "db/password"},
		StripeKey:  &SecretValue{Key: "stripe/key"},
		Rate:       ParameterValue{Key: "rate", Static: true},
		Nested:     Nested{Timeout: ParameterValue{Key: "nested/timeout", Static: true}},
		NestedPtr:  &Nested{Timeout: ParameterValue{Key: "nested/timeout", Static: true}},
	}
	_ = cfg.unexported

	if err := c.Resolve(context.Background(), cfg); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if cfg.DBPassword.Value() != "dbpw" {
		t.Errorf("DBPassword = %q", cfg.DBPassword.Value())
	}
	if cfg.StripeKey.Value() != "sk_test" {
		t.Errorf("StripeKey = %q", cfg.StripeKey.Value())
	}
	if cfg.Rate.Get() != "50" {
		t.Errorf("Rate = %q", cfg.Rate.Get())
	}
	if cfg.Nested.Timeout.Get() != "30s" {
		t.Errorf("Nested.Timeout = %q", cfg.Nested.Timeout.Get())
	}
	if cfg.NestedPtr.Timeout.Get() != "30s" {
		t.Errorf("NestedPtr.Timeout = %q", cfg.NestedPtr.Timeout.Get())
	}
	if cfg.unexported.Initialized() {
		t.Errorf("unexported field should not have been initialized")
	}
}

// TestResolveWalksSlicesAndArrays proves the L6 fix: SecretValue/ParameterValue
// fields inside slices, pointer slices, and arrays of structs are resolved
// rather than silently skipped (which used to leave them uninitialized and
// panic at .Value()).
func TestResolveWalksSlicesAndArrays(t *testing.T) {
	c, srv := newTestClient(t, Config{})
	srv.SetSecret(testNS, "s/a", []byte("sa"))
	srv.SetSecret(testNS, "s/b", []byte("sb"))
	srv.SetSecret(testNS, "s/ptr", []byte("sptr"))
	srv.SetParameter(testNS, "p/a", "pa")
	srv.SetParameter(testNS, "p/b", "pb")
	srv.SetParameter(testNS, "p/arr", "parr")

	type Sub struct {
		Secret SecretValue
		Param  ParameterValue
	}
	type Cfg struct {
		Slice    []Sub
		PtrSlice []*Sub
		Arr      [1]Sub
	}
	cfg := &Cfg{
		Slice: []Sub{
			{Secret: SecretValue{Key: "s/a"}, Param: ParameterValue{Key: "p/a", Static: true}},
			{Secret: SecretValue{Key: "s/b"}, Param: ParameterValue{Key: "p/b", Static: true}},
		},
		PtrSlice: []*Sub{
			{Secret: SecretValue{Key: "s/ptr"}, Param: ParameterValue{Key: "p/a", Static: true}},
		},
		Arr: [1]Sub{
			{Secret: SecretValue{Key: "s/a"}, Param: ParameterValue{Key: "p/arr", Static: true}},
		},
	}

	if err := c.Resolve(context.Background(), cfg); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if cfg.Slice[0].Secret.Value() != "sa" || cfg.Slice[0].Param.Get() != "pa" {
		t.Errorf("Slice[0] = %q/%q, want sa/pa", cfg.Slice[0].Secret.Value(), cfg.Slice[0].Param.Get())
	}
	if cfg.Slice[1].Secret.Value() != "sb" || cfg.Slice[1].Param.Get() != "pb" {
		t.Errorf("Slice[1] = %q/%q, want sb/pb", cfg.Slice[1].Secret.Value(), cfg.Slice[1].Param.Get())
	}
	if cfg.PtrSlice[0].Secret.Value() != "sptr" {
		t.Errorf("PtrSlice[0].Secret = %q, want sptr", cfg.PtrSlice[0].Secret.Value())
	}
	if cfg.Arr[0].Secret.Value() != "sa" || cfg.Arr[0].Param.Get() != "parr" {
		t.Errorf("Arr[0] = %q/%q, want sa/parr", cfg.Arr[0].Secret.Value(), cfg.Arr[0].Param.Get())
	}
}

// TestResolveSkipsMapValues documents the L6 boundary: map values are not walked
// (they are unaddressable), so such a field is left uninitialized and Resolve
// does not error on it.
func TestResolveSkipsMapValues(t *testing.T) {
	c, srv := newTestClient(t, Config{})
	srv.SetSecret(testNS, "s/a", []byte("sa"))

	type Cfg struct {
		M map[string]SecretValue
	}
	cfg := &Cfg{M: map[string]SecretValue{"k": {Key: "s/a"}}}
	if err := c.Resolve(context.Background(), cfg); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	sv := cfg.M["k"]
	if sv.Initialized() {
		t.Error("map-valued SecretValue should not be walked/initialized")
	}
}

func TestResolveErrors(t *testing.T) {
	c, _ := newTestClient(t, Config{})

	if err := c.Resolve(context.Background(), struct{}{}); err == nil {
		t.Error("expected error for non-pointer")
	}
	var nilPtr *struct{}
	if err := c.Resolve(context.Background(), nilPtr); err == nil {
		t.Error("expected error for nil pointer")
	}
	x := 5
	if err := c.Resolve(context.Background(), &x); err == nil {
		t.Error("expected error for pointer to non-struct")
	}
}

func TestResolveReportsMissingField(t *testing.T) {
	c, srv := newTestClient(t, Config{})
	srv.SetSecret(testNS, "ok", []byte("v"))

	type Config3 struct {
		OK      SecretValue
		Missing SecretValue
	}
	cfg := &Config3{
		OK:      SecretValue{Key: "ok"},
		Missing: SecretValue{Key: "nope"},
	}
	err := c.Resolve(context.Background(), cfg)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "nope") {
		t.Errorf("error should name missing key: %v", err)
	}
	// The resolvable field should still have been initialized.
	if !cfg.OK.Initialized() {
		t.Error("resolvable field should be initialized despite sibling error")
	}
}

func TestExplicitInitPattern(t *testing.T) {
	c, srv := newTestClient(t, Config{})
	srv.SetSecret(testNS, "a", []byte("va"))
	srv.SetParameter(testNS, "b", "vb")

	type Config4 struct {
		A SecretValue
		B ParameterValue
	}
	cfg := &Config4{A: SecretValue{Key: "a"}, B: ParameterValue{Key: "b", Static: true}}
	// Explicit per-field Init (plan 9.5).
	if err := cfg.A.Init(c); err != nil {
		t.Fatal(err)
	}
	if err := cfg.B.Init(c); err != nil {
		t.Fatal(err)
	}
	if cfg.A.Value() != "va" || cfg.B.Get() != "vb" {
		t.Errorf("A=%q B=%q", cfg.A.Value(), cfg.B.Get())
	}
}
