package storage

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/Suhaibinator/kms/internal/domain"
)

func TestSchemaConcurrencyReproDifferentDigest(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	if _, err := st.CreateApplication(ctx, domain.Application{Name: "concurrent", ReleaseName: "runtime"}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	start := make(chan struct{})
	for i := range 8 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, err := st.CreateConfigurationSchema(ctx, domain.ConfigurationSchema{Application: "concurrent", ReleaseName: "runtime", Schema: fmt.Sprintf(`{"type":"object","x":%d}`, i), Digest: fmt.Sprintf("digest-%d", i), Metadata: "{}"})
			errs <- err
		}(i)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Logf("schema create error: %v", err)
		}
	}
	rows, _, err := st.ListConfigurationSchemas(ctx, "concurrent", "runtime", ListPage{})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("rows=%d versions=%+v", len(rows), rows)
}
