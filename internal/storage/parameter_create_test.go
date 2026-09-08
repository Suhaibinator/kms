package storage

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"

	"github.com/Suhaibinator/kms/internal/domain"
)

func TestCreateParameterRejectsExistingWithoutMutation(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	seedNS(t, st, "prod", "app")
	r := ref("prod", "app", "config")
	if _, _, err := st.PutParameter(ctx, r, "original", "string", `{"owner":"original"}`, "admin"); err != nil {
		t.Fatal(err)
	}
	before, err := st.GetParameterInfo(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	revision, err := st.CurrentRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	v, rev, err := st.CreateParameter(ctx, r, "42", "integer", `{"owner":"replacement"}`, "other")
	if !errors.Is(err, domain.ErrAlreadyExists) || v != 0 || rev != 0 {
		t.Fatalf("create = %d, %d, %v", v, rev, err)
	}
	after, err := st.GetParameterInfo(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("metadata changed: before %+v after %+v", before, after)
	}
	current, err := st.GetParameter(ctx, r, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if current.Value != "original" {
		t.Fatalf("value = %q", current.Value)
	}
	next, err := st.CurrentRevision(ctx)
	if err != nil || next != revision {
		t.Fatalf("revision = %d, %v; want %d", next, err, revision)
	}
}

func TestPutParameterPreservingMetadataCopiesCurrentMetadataAtomically(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	seedNS(t, st, "prod", "app")
	r := ref("prod", "app", "config")
	if _, _, err := st.PutParameter(ctx, r, "original", "string", `{"owner":"platform"}`, "admin"); err != nil {
		t.Fatal(err)
	}
	version, _, err := st.PutParameterPreservingMetadata(ctx, r, "updated", "string", "editor")
	if err != nil {
		t.Fatal(err)
	}
	current, err := st.GetParameter(ctx, r, version, "")
	if err != nil {
		t.Fatal(err)
	}
	if current.Value != "updated" || current.Metadata != `{"owner":"platform"}` {
		t.Fatalf("current = %+v", current)
	}
	info, err := st.GetParameterInfo(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	if info.Metadata != `{"owner":"platform"}` {
		t.Fatalf("parameter metadata = %q", info.Metadata)
	}
}

func TestCreateParameterConcurrentWriters(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	seedNS(t, st, "prod", "app")
	r := ref("prod", "app", "race")
	start := make(chan struct{})
	results := make(chan error, 12)
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			<-start
			_, _, err := st.CreateParameter(ctx, r, "initial", "string", "{}", "admin")
			results <- err
		})
	}
	close(start)
	wg.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		} else if !errors.Is(err, domain.ErrAlreadyExists) {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if successes != 1 {
		t.Fatalf("successful creates = %d", successes)
	}
	info, err := st.GetParameterInfo(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	if len(info.Versions) != 1 || info.Labels[domain.LabelCurrent] != 1 || len(info.Labels) != 1 {
		t.Fatalf("info = %+v", info)
	}
}

func TestNamespaceIdentityCountMatchesDeletionBlocker(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	ns := seedNS(t, st, "prod", "app")
	identity, err := st.CreateIdentity(ctx, CreateIdentityParams{Name: "bound", Kind: domain.IdentityKindClient, Namespace: &ns.NamespaceRef})
	if err != nil {
		t.Fatal(err)
	}
	list, _, err := st.ListNamespaces(ctx, ListPage{})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].IdentityCount != 1 || list[0].ParameterCount != 0 || list[0].SecretCount != 0 {
		t.Fatalf("namespace counts = %+v", list)
	}
	if err := st.DeleteNamespace(ctx, ns.NamespaceRef); !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Fatalf("delete = %v", err)
	}
	if err := st.SetIdentityDisabled(ctx, identity.Name, true); err != nil {
		t.Fatal(err)
	}
	list, _, err = st.ListNamespaces(ctx, ListPage{})
	if err != nil || list[0].IdentityCount != 1 {
		t.Fatalf("after disabling = %+v, %v", list, err)
	}
	if err := st.DeleteNamespace(ctx, ns.NamespaceRef); !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Fatalf("disabled identity must still block deletion: %v", err)
	}
}
