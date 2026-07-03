package core

import (
	"context"
	"testing"

	"github.com/Suhaibinator/kms/internal/domain"
)

// fakeHub records Wake calls so tests can assert that committed writes poke the
// watch fan-out.
type fakeHub struct {
	wakes int
	subs  []domain.Subscriber
}

func (h *fakeHub) Wake()                            { h.wakes++ }
func (h *fakeHub) Subscribers() []domain.Subscriber { return h.subs }

func newTestServiceWithHub(t *testing.T, store *fakeStore) (*Service, *fakeHub) {
	s := newTestService(store)
	withKeyring(t, s)
	h := &fakeHub{}
	s.SetHub(h)
	return s, h
}

func TestWritesWakeHub(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	s, hub := newTestServiceWithHub(t, store)
	admin := adminPrincipal()

	// Each committed write must wake the hub exactly once.
	steps := []struct {
		name string
		run  func() error
	}{
		{"put parameter", func() error { _, _, err := s.PutParameter(ctx, admin, tref("p"), "1", "integer", "{}"); return err }},
		{"delete parameter", func() error { _, err := s.DeleteParameter(ctx, admin, tref("p")); return err }},
		{"put secret", func() error {
			_, err := s.PutSecret(ctx, admin, PutSecretInput{Ref: tref("s"), Value: []byte("v"), ContentType: "text/plain"})
			return err
		}},
		{"disable secret", func() error { _, err := s.DisableSecret(ctx, admin, tref("s"), 1, false); return err }},
		{"enable secret", func() error { _, err := s.DisableSecret(ctx, admin, tref("s"), 1, true); return err }},
		{"promote secret", func() error { _, _, _, err := s.PromoteSecretVersion(ctx, admin, tref("s"), 1); return err }},
		{"destroy secret", func() error { _, err := s.DestroySecretVersion(ctx, admin, tref("s"), 1); return err }},
		{"delete secret", func() error { _, err := s.DeleteSecret(ctx, admin, tref("s")); return err }},
	}
	for _, step := range steps {
		before := hub.wakes
		if err := step.run(); err != nil {
			t.Fatalf("%s: %v", step.name, err)
		}
		if hub.wakes != before+1 {
			t.Errorf("%s: wakes = %d, want %d", step.name, hub.wakes, before+1)
		}
	}

	// A read must NOT wake the hub.
	before := hub.wakes
	if _, _, err := s.PutParameter(ctx, admin, tref("r"), "1", "integer", "{}"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	hub.wakes = before // reset, ignore the seed write
	if _, err := s.GetParameter(ctx, admin, tref("r"), 0, ""); err != nil {
		t.Fatalf("GetParameter: %v", err)
	}
	if hub.wakes != before {
		t.Errorf("read woke the hub: wakes = %d, want %d", hub.wakes, before)
	}
}
