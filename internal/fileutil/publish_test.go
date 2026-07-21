package fileutil

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestPublishNoReplace(t *testing.T) {
	dir := t.TempDir()
	staging := filepath.Join(dir, "staging")
	dst := filepath.Join(dir, "published")
	if err := os.WriteFile(staging, []byte("complete"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := PublishNoReplace(staging, dst); err != nil {
		t.Fatalf("PublishNoReplace: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil || string(got) != "complete" {
		t.Fatalf("published content = %q, %v", got, err)
	}
}

func TestPublishNoReplacePreservesExistingDestination(t *testing.T) {
	dir := t.TempDir()
	staging := filepath.Join(dir, "staging")
	dst := filepath.Join(dir, "published")
	if err := os.WriteFile(staging, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := PublishNoReplace(staging, dst); !errors.Is(err, os.ErrExist) {
		t.Fatalf("PublishNoReplace collision = %v, want os.ErrExist", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil || string(got) != "existing" {
		t.Fatalf("existing destination = %q, %v", got, err)
	}
}

func TestPublishNoReplaceConcurrentPublishersExactlyOneWins(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "published")
	const publishers = 16
	staging := make([]string, publishers)
	for i := range staging {
		staging[i] = filepath.Join(dir, fmt.Sprintf("staging-%02d", i))
		if err := os.WriteFile(staging[i], []byte(fmt.Sprintf("publisher-%02d", i)), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	type result struct {
		publisher int
		err       error
	}
	start := make(chan struct{})
	results := make(chan result, publishers)
	var wg sync.WaitGroup
	for i := range staging {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			results <- result{publisher: i, err: PublishNoReplace(staging[i], dst)}
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)

	winner := -1
	for got := range results {
		switch {
		case got.err == nil:
			if winner != -1 {
				t.Fatalf("publishers %d and %d both replaced the same destination", winner, got.publisher)
			}
			winner = got.publisher
		case errors.Is(got.err, os.ErrExist):
			// Expected: publication is an atomic first-writer-wins operation.
		default:
			t.Fatalf("publisher %d returned unexpected error: %v", got.publisher, got.err)
		}
	}
	if winner == -1 {
		t.Fatal("no concurrent publisher won")
	}
	want := fmt.Sprintf("publisher-%02d", winner)
	if got, err := os.ReadFile(dst); err != nil || string(got) != want {
		t.Fatalf("published content = %q, %v; want %q", got, err, want)
	}
}
