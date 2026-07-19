package storage

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/Suhaibinator/kms/internal/domain"
)

func TestConfigurationReleaseConcurrentActivationCAS(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	seedNS(t, st, "prod", "app")
	ns := nsRef("prod", "app")
	versions := make([]uint64, 3)
	for i := range versions {
		release, err := st.CreateConfigurationRelease(ctx, domain.ConfigurationRelease{
			Namespace: ns, Name: "runtime", Digest: string(rune('a' + i)), Metadata: "{}",
		})
		if err != nil {
			t.Fatal(err)
		}
		versions[i] = release.Version
	}
	zero := uint64(0)
	if _, changed, err := st.ActivateConfigurationRelease(ctx, ns, "runtime", versions[0], &zero); err != nil || !changed {
		t.Fatalf("initial activation changed=%v err=%v", changed, err)
	}
	before, err := st.CurrentRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}

	type result struct {
		changed bool
		err     error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	var wg sync.WaitGroup
	for _, version := range versions[1:] {
		wg.Add(1)
		go func(version uint64) {
			defer wg.Done()
			<-start
			expected := versions[0]
			_, changed, err := st.ActivateConfigurationRelease(ctx, ns, "runtime", version, &expected)
			results <- result{changed: changed, err: err}
		}(version)
	}
	close(start)
	wg.Wait()
	close(results)

	var succeeded, conflicted int
	for result := range results {
		switch {
		case result.err == nil && result.changed:
			succeeded++
		case errors.Is(result.err, domain.ErrAborted):
			conflicted++
		default:
			t.Fatalf("unexpected concurrent activation result: changed=%v err=%v", result.changed, result.err)
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("succeeded=%d conflicted=%d, want 1/1", succeeded, conflicted)
	}
	after, err := st.CurrentRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if after != before+1 {
		t.Fatalf("concurrent CAS appended %d revisions, want 1", after-before)
	}
}
