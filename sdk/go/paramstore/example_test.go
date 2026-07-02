package paramstore_test

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/Suhaibinator/kms/sdk/go/paramstore"
)

// Examples are compiled (as documentation) but not executed, so they may
// reference a real endpoint without a running server.

func ExampleNewClient() {
	client, err := paramstore.NewClient(paramstore.Config{
		Endpoint: "parameter-store.prod.internal:8443",
		TLS:      paramstore.MTLSFromFiles("client.crt", "client.key", "ca.crt"),
		CacheTTL: time.Minute,
	})
	if err != nil {
		log.Fatal(err)
	}
	defer client.Close()

	ctx := context.Background()
	dbPassword, err := client.GetSecret(ctx, "/prod/payments/postgres/password")
	if err != nil {
		log.Fatal(err)
	}
	// dbPassword prints as [REDACTED]; call Value() for plaintext.
	_ = dbPassword.Value()
}

func ExampleClient_Resolve() {
	type Config struct {
		DBPassword paramstore.SecretValue
		StripeKey  paramstore.SecretValue
		RateLimit  paramstore.ParameterValue
	}

	client, err := paramstore.NewClient(paramstore.Config{Endpoint: "localhost:8443"})
	if err != nil {
		log.Fatal(err)
	}
	defer client.Close()

	cfg := Config{
		DBPassword: paramstore.SecretValue{Key: "/prod/payments/postgres/password"},
		StripeKey:  paramstore.SecretValue{Key: "/prod/payments/stripe/api-key", EnvVar: "STRIPE_KEY"},
		RateLimit:  paramstore.ParameterValue{Key: "/prod/payments/rate-limit", Dynamic: true},
	}
	if err := client.Resolve(context.Background(), &cfg); err != nil {
		log.Fatal(err)
	}

	cfg.RateLimit.OnChange(func(old, new string) {
		fmt.Printf("rate limit changed: %s -> %s\n", old, new)
	})
	_ = cfg.DBPassword.Value()
	_ = cfg.RateLimit.Get()
}

func ExampleClient_Watch() {
	client, _ := paramstore.NewClient(paramstore.Config{Endpoint: "localhost:8443"})
	defer client.Close()

	stop, err := client.Watch(context.Background(), "/prod/payments/*", func(ev paramstore.Event) {
		fmt.Printf("%s %s => %s\n", ev.Type, ev.Path, ev.Value)
	})
	if err != nil {
		log.Fatal(err)
	}
	defer stop()
}

func ExampleClient_GetSecret_errorHandling() {
	client, _ := paramstore.NewClient(paramstore.Config{Endpoint: "localhost:8443"})
	defer client.Close()

	_, err := client.GetSecret(context.Background(), "/prod/missing")
	switch {
	case errors.Is(err, paramstore.ErrNotFound):
		fmt.Println("not found")
	case errors.Is(err, paramstore.ErrPermissionDenied):
		fmt.Println("denied")
	}
}
