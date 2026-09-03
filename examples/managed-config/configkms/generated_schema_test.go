package configkms

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/jsontext"
	"os"
	"testing"
)

func TestGeneratedSchemaIsExactFreshArtifactWithContractDigest(t *testing.T) {
	want, err := os.ReadFile("../runtime.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	first := GeneratedSchema()
	second := GeneratedSchema()
	if string(first) != string(want) || string(second) != string(want) {
		t.Fatal("GeneratedSchema does not match the emitted schema artifact")
	}
	first[0] ^= 0xff
	if string(second) != string(want) || string(GeneratedSchema()) != string(want) {
		t.Fatal("GeneratedSchema returned shared mutable storage")
	}
	compact := jsontext.Value(second).Clone()
	if err := compact.Compact(jsontext.AllowDuplicateNames(false), jsontext.AllowInvalidUTF8(false)); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(compact)
	if got := hex.EncodeToString(digest[:]); got != generatedSchemaSHA256 {
		t.Fatalf("compacted schema digest = %s, contract digest = %s", got, generatedSchemaSHA256)
	}
}
