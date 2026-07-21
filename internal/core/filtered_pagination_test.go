package core

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"

	"go.uber.org/zap"

	"github.com/Suhaibinator/kms/internal/crypto"
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
