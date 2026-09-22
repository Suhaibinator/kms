package configgen

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/Suhaibinator/kms/internal/configgen/testdata/composed"
	"github.com/Suhaibinator/kms/internal/configgen/testdata/composedgenerated"
	"github.com/Suhaibinator/kms/internal/configgen/testdata/generated"
	"github.com/Suhaibinator/kms/internal/configgen/testdata/mutating"
	"github.com/Suhaibinator/kms/internal/configgen/testdata/mutatinggenerated"
	"github.com/Suhaibinator/kms/internal/configgen/testdata/valid"
	"github.com/Suhaibinator/kms/sdk/go/configstore"
	"github.com/Suhaibinator/kms/sdk/go/kmsclient"
)

func TestLocalStoreIsolationAndLifecycle(t *testing.T) {
	input := &valid.Config{
		Endpoint: valid.Endpoint{Host: "localhost", Ports: []uint16{5432}, Labels: map[string][]string{"region": {"west"}}},
		Payload:  []byte("payload"), Password: kmsclient.NewSecret([]byte("secret-canary")),
		Local: map[string]string{"name": "local"},
	}
	input.Password.BindKey = kmsclient.NewBindingKey("binding-canary")
	store, err := generated.NewLocal(input)
	if err != nil {
		t.Fatal(err)
	}
	input.Endpoint.Labels["region"][0] = "changed"
	input.Endpoint.Ports[0] = 1
	input.Payload[0] = 'X'
	input.Password.Value()[0] = 'X'
	input.Local["name"] = "changed"
	snapshot := store.Current()
	copy := snapshot.Config()
	if copy.Endpoint.Ports[0] != 5432 || copy.Endpoint.Labels["region"][0] != "west" || string(copy.Payload) != "payload" || copy.Password.StringValue() != "secret-canary" || copy.Local["name"] != "local" {
		t.Fatal("input mutation reached snapshot")
	}
	if copy.Password.BindKey.IsSet() {
		t.Fatal("binding key retained")
	}
	copy.Password.Value()[0] = 'X'
	copy.Endpoint.Labels["region"][0] = "changed"
	if snapshot.Config().Password.StringValue() != "secret-canary" || snapshot.Config().Endpoint.Labels["region"][0] != "west" {
		t.Fatal("returned mutation reached snapshot")
	}
	if snapshot.PersistenceHandler().Password().StringValue() != "secret-canary" {
		t.Fatal("typed secret view")
	}
	status := store.Status()
	if status.Source != "local" || !status.Ready || status.State != "applied" || !status.Applied.IsZero() || !status.Observed.IsZero() || !snapshot.Release().IsZero() {
		t.Fatalf("unexpected status: %+v", status)
	}
	stats := store.Stats()
	if stats.Candidates != 1 || stats.Applied != 1 || stats.Reconnects != 0 || stats.DefaultDivergent || stats.AppliedReleaseVersion != 0 {
		t.Fatalf("unexpected stats: %+v", stats)
	}
	stats.Rejected[configstore.RejectInternal] = 10
	if len(store.Stats().Rejected) != 0 {
		t.Fatal("stats are not independent")
	}
	for range 2 {
		if err := store.Wait(); err != nil {
			t.Fatal(err)
		}
	}
	if store.Current().Config().Endpoint.Host != "localhost" {
		t.Fatal("wait discarded snapshot")
	}
	if strings.Contains(fmt.Sprint(status, snapshot.Config().Password), "canary") {
		t.Fatal("secret leaked")
	}
}

func TestLocalValidationAndOptionalSecrets(t *testing.T) {
	if _, err := generated.NewLocal(&valid.Config{}); err != nil {
		t.Fatalf("optional secret: %v", err)
	}
	for _, cfg := range []*valid.Config{nil, {Timeout: -1}} {
		store, err := generated.NewLocal(cfg)
		var candidate *configstore.CandidateError
		if store != nil || !errors.As(err, &candidate) {
			t.Fatalf("expected classified rejection: %v", err)
		}
	}
	if _, err := composedgenerated.NewLocal(&composed.Config{}); err == nil {
		t.Fatal("nil inline pointer accepted")
	}
	if _, err := mutatinggenerated.NewLocal(mutating.Defaults()); err == nil {
		t.Fatal("required secret accepted empty")
	}
	input := &mutating.Config{Name: " mutate secret ", Token: kmsclient.NewSecret([]byte("secret-value"))}
	var retained *mutating.Config
	mutating.ValidationObserver = func(cfg *mutating.Config) {
		retained = cfg
		cfg.Token.BindKey = kmsclient.NewBindingKey("validator-key")
	}
	t.Cleanup(func() { mutating.ValidationObserver = nil })
	store, err := mutatinggenerated.NewLocal(input)
	if err != nil {
		t.Fatal(err)
	}
	retained.Name = "changed"
	retained.Token.Value()[0] = 'X'
	if input.Name != " mutate secret " || input.Token.StringValue() != "secret-value" {
		t.Fatal("validation mutated input")
	}
	got := store.Current().Config()
	if got.Name != "mutate secret" || got.Token.StringValue() != "Secret-value" || got.Token.BindKey.IsSet() {
		t.Fatal("validated snapshot not isolated and sanitized")
	}
}
