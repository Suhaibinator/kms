package paramstore_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Suhaibinator/kms/sdk/go/paramstore"
	"github.com/Suhaibinator/kms/sdk/go/paramstore/paramstoretest"
)

type cacheIsolationPrepared struct{ committed chan struct{} }

func (p *cacheIsolationPrepared) Commit() { close(p.committed) }
func (*cacheIsolationPrepared) Abort()    {}

func TestReleaseLoaderSecretSnapshotIgnoresCallerCacheMutation(t *testing.T) {
	server, err := paramstoretest.New()
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	server.SetSecretVersion("prod/app", "password", []byte("original"), "text/plain", 3)
	_, err = server.SetActiveRelease(paramstoretest.ReleaseSpec{
		Namespace: "prod/app",
		Name:      "runtime",
		Version:   1,
		Entries: []paramstoretest.ReleaseEntrySpec{
			{Alias: "password", Kind: "secret", Path: "password", Version: 3},
		},
	}, 11)
	if err != nil {
		t.Fatal(err)
	}

	client, err := paramstore.NewClient(paramstore.Config{
		Namespace:   "prod/app",
		ClientName:  "cache-isolation-test",
		CacheTTL:    time.Minute,
		DialOptions: server.DialOptions(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	primed, err := client.GetSecret(context.Background(), "password", paramstore.WithVersion(3))
	if err != nil {
		t.Fatal(err)
	}
	primed.Value()[0] = 'X'

	loader, err := paramstore.NewReleaseLoader(client, paramstore.ReleaseLoaderConfig{Name: "runtime"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	committed := make(chan struct{})
	resolved := make(chan string, 1)
	runErr := make(chan error, 1)
	go func() {
		runErr <- loader.Run(ctx, func(_ context.Context, snapshot paramstore.ReleaseSnapshot) (paramstore.PreparedRelease, error) {
			secret, ok := snapshot.Secret("password")
			if !ok {
				resolved <- "<missing>"
			} else {
				resolved <- secret.StringValue()
			}
			return &cacheIsolationPrepared{committed: committed}, nil
		})
	}()

	select {
	case <-committed:
	case err := <-runErr:
		t.Fatalf("loader stopped before initial commit: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for initial commit")
	}
	if got := <-resolved; got != "original" {
		t.Fatalf("managed exact-version resolution used caller-mutated cache bytes: got %q, want %q", got, "original")
	}
	cancel()
	select {
	case err := <-runErr:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("loader shutdown error = %v, want context cancellation", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for loader shutdown")
	}
}
