package kmsclient

import (
	"context"
	"testing"
)

func TestSecretIsSetDistinguishesResolvedFromZero(t *testing.T) {
	cases := map[string]struct {
		secret     Secret
		set, empty bool
	}{
		"zero":                   {Secret{}, false, true},
		"bind key only":          {Secret{BindKey: NewBindingKey("binding-key-0123456789-0123456789")}, false, true},
		"NewSecret nil":          {NewSecret(nil), false, true},
		"NewSecret empty":        {NewSecret([]byte("")), false, true},
		"NewSecret value":        {NewSecret([]byte("v")), true, false},
		"resolved empty":         {Secret{value: []byte{}, path: "/prod/app/k", version: 1}, true, true},
		"resolved value":         {Secret{value: []byte("v"), path: "/prod/app/k", version: 3}, true, false},
		"path without a version": {Secret{path: "/prod/app/k"}, false, true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := tc.secret.IsSet(); got != tc.set {
				t.Errorf("IsSet = %v, want %v", got, tc.set)
			}
			if got := tc.secret.IsZero(); got != tc.empty {
				t.Errorf("IsZero = %v, want %v", got, tc.empty)
			}
			if clone := tc.secret.Clone(); clone.IsSet() != tc.set || clone.IsZero() != tc.empty {
				t.Errorf("Clone changed IsSet/IsZero to %v/%v", clone.IsSet(), clone.IsZero())
			}
		})
	}
}

// An empty secret read from the store is set, zero, and carries a non-nil
// empty plaintext even though protobuf decodes the empty bytes field as nil.
func TestGetSecretResolvesEmptySecretAsSet(t *testing.T) {
	client, srv := newTestClient(t, Config{})
	srv.SetSecret(testNS, "empty", nil)

	got, err := client.GetSecret(context.Background(), "empty")
	if err != nil {
		t.Fatalf("GetSecret: %v", err)
	}
	if !got.IsSet() || !got.IsZero() {
		t.Fatalf("IsSet/IsZero = %v/%v, want true/true", got.IsSet(), got.IsZero())
	}
	if got.Value() == nil || len(got.Value()) != 0 || got.StringValue() != "" {
		t.Fatalf("Value = %#v, want non-nil []byte{}", got.Value())
	}
	again, err := client.GetSecret(context.Background(), "empty")
	if err != nil {
		t.Fatalf("GetSecret again: %v", err)
	}
	if !got.Equal(again) || !got.Equal(got.Clone()) {
		t.Fatal("repeated reads of an empty secret are not Equal")
	}
}
