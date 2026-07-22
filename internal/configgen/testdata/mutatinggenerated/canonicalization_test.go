package mutatinggenerated

import (
	"context"
	"sync/atomic"
	"testing"

	rootconfig "github.com/Suhaibinator/kms/internal/configgen/testdata/mutating"
	"github.com/Suhaibinator/kms/sdk/go/configstore"
	"github.com/Suhaibinator/kms/sdk/go/paramstore"
	"github.com/Suhaibinator/kms/sdk/go/paramstore/paramstoretest"
)

func TestMutatingValidatorDoesNotCreateDefaultDrift(t *testing.T) {
	for _, test := range []struct {
		name    string
		rawName string
		want    string
	}{
		{name: "non-secret field", rawName: "  canonical name  ", want: "canonical name"},
		{name: "secret bytes", rawName: "  mutate secret  ", want: "mutate secret"},
	} {
		t.Run(test.name, func(t *testing.T) {
			testMutatingValidator(t, test.rawName, test.want)
		})
	}
}

func testMutatingValidator(t *testing.T, rawName, wantName string) {
	t.Helper()
	const (
		namespace   = "prod/mutating-validator"
		releaseName = "runtime"
		groupPath   = "groups/runtime"
		secretPath  = "secrets/token"
	)

	server, err := paramstoretest.New()
	if err != nil {
		t.Fatal(err)
	}
	server.SetParameterVersion(namespace, groupPath, `{"name":"`+rawName+`"}`, "json", 1)
	server.SetSecretVersion(namespace, secretPath, []byte("secret-value"), "text/plain", 1)
	if _, err := server.SetActiveRelease(paramstoretest.ReleaseSpec{
		Namespace: namespace,
		Name:      releaseName,
		Version:   1,
		Entries: []paramstoretest.ReleaseEntrySpec{
			{Alias: "runtime", Kind: "parameter", Path: groupPath, Version: 1, ContentType: "json"},
			{Alias: "token", Kind: "secret", Path: secretPath, Version: 1},
		},
	}, 1); err != nil {
		server.Close()
		t.Fatal(err)
	}

	client, err := paramstore.NewClient(paramstore.Config{
		Namespace:   namespace,
		ClientName:  "mutating-validator-test",
		DialOptions: server.DialOptions(),
	})
	if err != nil {
		server.Close()
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	var store *Store
	t.Cleanup(func() {
		cancel()
		if store != nil {
			if err := store.Wait(); err != nil {
				t.Errorf("store shutdown: %v", err)
			}
		}
		if err := client.Close(); err != nil {
			t.Errorf("client close: %v", err)
		}
		server.Close()
	})

	var mismatchCalls atomic.Uint64
	store, err = Start(ctx, client, Options{
		Release: releaseName,
		Defaults: func() *rootconfig.Config {
			return &rootconfig.Config{Name: rawName}
		},
		OnDefaultMismatch: func(configstore.DefaultMismatchReport) {
			mismatchCalls.Add(1)
		},
		InstanceID: "mutating-validator-instance",
	})
	if err != nil {
		t.Fatalf("Start rejected semantically identical canonical values: %T: %v", err, err)
	}
	worker := store.Current().Worker()
	if got := worker.Name(); got != wantName {
		t.Fatalf("published canonical name = %q, want %q", got, wantName)
	}
	if got := worker.Token().StringValue(); got != "Secret-value" && wantName == "mutate secret" {
		t.Fatalf("published canonical secret = %q, want %q", got, "Secret-value")
	}
	if got := mismatchCalls.Load(); got != 0 {
		t.Fatalf("default mismatch callback calls = %d, want 0", got)
	}
}
