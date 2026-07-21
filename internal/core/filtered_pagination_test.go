package core

import (
	"bytes"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"go.uber.org/zap"

	"github.com/Suhaibinator/kms/internal/crypto"
	"github.com/Suhaibinator/kms/internal/domain"
)

func TestFilteredCursorIsFixedLengthAndSurvivesServiceRestart(t *testing.T) {
	material := bytes.Repeat([]byte{0x5a}, 32)
	newService := func() *Service {
		kek, err := crypto.NewKEKFromMaterial("kek-cursor-test", material)
		if err != nil {
			t.Fatalf("NewKEKFromMaterial: %v", err)
		}
		svc := New(newFakeStore(), zap.NewNop(), "test")
		svc.SetKeyring(crypto.NewKeyring(kek))
		return svc
	}

	beforeRestart := newService()
	short, err := beforeRestart.sealFilteredCursor("audit", "scope", "a")
	if err != nil {
		t.Fatalf("seal short cursor: %v", err)
	}
	longRaw := strings.Repeat("opaque-storage-cursor-", 30)
	long, err := beforeRestart.sealFilteredCursor("audit", "scope", longRaw)
	if err != nil {
		t.Fatalf("seal long cursor: %v", err)
	}
	if len(short) != len(long) {
		t.Fatalf("cursor length leaks raw state length: short=%d long=%d", len(short), len(long))
	}
	decoded, err := base64.RawURLEncoding.DecodeString(long)
	if err != nil {
		t.Fatalf("decode sealed cursor: %v", err)
	}
	if bytes.Contains(decoded, []byte("opaque-storage-cursor")) {
		t.Fatal("sealed cursor contains recognizable raw storage state")
	}

	afterRestart := newService()
	opened, err := afterRestart.openFilteredCursor(long, "audit", "scope")
	if err != nil {
		t.Fatalf("open cursor after restart: %v", err)
	}
	if opened != longRaw {
		t.Fatalf("opened cursor = %q, want original raw state", opened)
	}
	if _, err := afterRestart.openFilteredCursor(long, "namespace", "scope"); err == nil {
		t.Fatal("cursor kind reuse should fail closed")
	}
}

func TestFilteredCursorRequiresReferencedKEKToRemainLoadedAfterRotation(t *testing.T) {
	oldMaterial := bytes.Repeat([]byte{0x31}, 32)
	newMaterial := bytes.Repeat([]byte{0x32}, 32)
	oldKEK, err := crypto.NewKEKFromMaterial("kek-old", oldMaterial)
	if err != nil {
		t.Fatal(err)
	}
	issuer := New(newFakeStore(), zap.NewNop(), "test")
	issuer.SetKeyring(crypto.NewKeyring(oldKEK))
	token, err := issuer.sealFilteredCursor("namespace", "scope", "raw-state")
	if err != nil {
		t.Fatal(err)
	}

	newKEK, err := crypto.NewKEKFromMaterial("kek-new", newMaterial)
	if err != nil {
		t.Fatal(err)
	}
	oldRetired, err := crypto.NewKEKFromMaterial("kek-old", oldMaterial)
	if err != nil {
		t.Fatal(err)
	}
	ring := crypto.NewKeyring(newKEK)
	ring.Add(oldRetired)
	restarted := New(newFakeStore(), zap.NewNop(), "test")
	restarted.SetKeyring(ring)
	opened, err := restarted.openFilteredCursor(token, "namespace", "scope")
	if err != nil || opened != "raw-state" {
		t.Fatalf("open pre-rotation cursor = %q, %v", opened, err)
	}

	newOnlyKEK, err := crypto.NewKEKFromMaterial("kek-new", newMaterial)
	if err != nil {
		t.Fatal(err)
	}
	newOnly := New(newFakeStore(), zap.NewNop(), "test")
	newOnly.SetKeyring(crypto.NewKeyring(newOnlyKEK))
	if _, err := newOnly.openFilteredCursor(token, "namespace", "scope"); err == nil {
		t.Fatal("pre-rotation cursor opened without its referenced KEK")
	}
}

func TestFilteredCursorRejectsEveryEnvelopeMutation(t *testing.T) {
	svc := New(newFakeStore(), zap.NewNop(), "test")
	token, err := svc.sealFilteredCursor("namespaces", "caller-scope", "raw-storage-cursor")
	if err != nil {
		t.Fatal(err)
	}
	if opened, err := svc.openFilteredCursor(token, "namespaces", "caller-scope"); err != nil || opened != "raw-storage-cursor" {
		t.Fatalf("unmodified cursor = %q, %v", opened, err)
	}

	sealed, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		t.Fatal(err)
	}
	for i := range sealed {
		mutated := append([]byte(nil), sealed...)
		mutated[i] ^= 1
		mutatedToken := base64.RawURLEncoding.EncodeToString(mutated)
		if _, err := svc.openFilteredCursor(mutatedToken, "namespaces", "caller-scope"); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Fatalf("mutating envelope byte %d returned %v, want ErrInvalidArgument", i, err)
		}
	}
}

func TestFilteredCursorIsBoundToExactCallerScope(t *testing.T) {
	svc := New(newFakeStore(), zap.NewNop(), "test")
	alice := Principal{Identity: domain.Identity{ID: 7, Name: "alice"}, Method: domain.AuthMethodToken}
	token, err := svc.sealFilteredCursor("audit", filteredCursorScope(alice, map[string]string{"env": "prod"}), "raw-storage-cursor")
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name      string
		principal Principal
	}{
		{name: "different identity name", principal: Principal{Identity: domain.Identity{ID: 7, Name: "mallory"}, Method: domain.AuthMethodToken}},
		{name: "different identity row", principal: Principal{Identity: domain.Identity{ID: 8, Name: "alice"}, Method: domain.AuthMethodToken}},
		{name: "different authentication method", principal: Principal{Identity: domain.Identity{ID: 7, Name: "alice"}, Method: domain.AuthMethodMTLS}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scope := filteredCursorScope(tt.principal, map[string]string{"env": "prod"})
			if _, err := svc.openFilteredCursor(token, "audit", scope); !errors.Is(err, domain.ErrInvalidArgument) {
				t.Fatalf("cross-scope cursor error = %v, want ErrInvalidArgument", err)
			}
		})
	}

	otherFilter := filteredCursorScope(alice, map[string]string{"env": "stage"})
	if _, err := svc.openFilteredCursor(token, "audit", otherFilter); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("cross-filter cursor error = %v, want ErrInvalidArgument", err)
	}
}
