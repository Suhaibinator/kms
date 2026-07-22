package paramstoretest_test

import (
	"context"
	"testing"
	"time"

	"github.com/Suhaibinator/kms/sdk/go/paramstore"
	"github.com/Suhaibinator/kms/sdk/go/paramstore/paramstoretest"
)

type preparedRelease struct {
	committed chan struct{}
}

func (p *preparedRelease) Commit() { close(p.committed) }
func (*preparedRelease) Abort()    {}

func TestConfigurationReleaseScripting(t *testing.T) {
	server, err := paramstoretest.New()
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	server.SetParameterVersion("prod/app", "groups/runtime", `{"enabled":true}`, "json", 7)
	server.SetSecretVersion("prod/app", "password", []byte("test-secret"), "text/plain", 3)
	_, err = server.SetActiveRelease(paramstoretest.ReleaseSpec{
		Namespace: "prod/app",
		Name:      "runtime",
		Version:   9,
		Entries: []paramstoretest.ReleaseEntrySpec{
			{Alias: "settings", Kind: "parameter", Path: "groups/runtime", Version: 7},
			{Alias: "password", Kind: "secret", Path: "password", Version: 3},
		},
	}, 21)
	if err != nil {
		t.Fatal(err)
	}

	client, err := paramstore.NewClient(paramstore.Config{
		Namespace:   "prod/app",
		ClientName:  "paramstoretest-release",
		DialOptions: server.DialOptions(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	loader, err := paramstore.NewReleaseLoader(client, paramstore.ReleaseLoaderConfig{Name: "runtime"})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	committed := make(chan struct{})
	runErr := make(chan error, 1)
	go func() {
		runErr <- loader.Run(ctx, func(_ context.Context, snapshot paramstore.ReleaseSnapshot) (paramstore.PreparedRelease, error) {
			parameter, ok := snapshot.Parameter("settings")
			if !ok || parameter.Value() != `{"enabled":true}` || parameter.Entry().Version != 7 {
				t.Errorf("unexpected parameter: %#v, present=%t", parameter, ok)
			}
			secret, ok := snapshot.Secret("password")
			if !ok || secret.StringValue() != "test-secret" || secret.Version() != 3 {
				t.Errorf("unexpected secret metadata, present=%t version=%d", ok, secret.Version())
			}
			return &preparedRelease{committed: committed}, nil
		})
	}()

	select {
	case <-committed:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for initial release commit")
	}
	sub, err := server.WaitForReleaseSubscribe(3 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if got := sub.Registration.GetLastSeenRevision(); got != 21 {
		t.Fatalf("last seen revision = %d, want 21", got)
	}

	foundApplied := false
	for range 3 {
		ack, waitErr := sub.WaitAcknowledgement(3 * time.Second)
		if waitErr != nil {
			t.Fatal(waitErr)
		}
		if ack.GetState() == paramstore.ReleaseStateApplied {
			foundApplied = true
			break
		}
	}
	if !foundApplied {
		t.Fatal("applied acknowledgement was not observed")
	}

	cancel()
	select {
	case err := <-runErr:
		if err != context.Canceled {
			t.Fatalf("Run returned %v, want context cancellation", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for loader shutdown")
	}
}
