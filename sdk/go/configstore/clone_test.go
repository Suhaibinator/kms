package configstore

import (
	"reflect"
	"testing"

	"github.com/Suhaibinator/kms/sdk/go/paramstore"
)

type cloneFixture struct {
	Pointer *int
	Bytes   []byte
	Map     map[string][]int
	Secret  paramstore.Secret
}

func TestCloneDeepCopiesMutableValuesAndSecrets(t *testing.T) {
	number := 7
	original := cloneFixture{
		Pointer: &number,
		Bytes:   []byte{1, 2, 3},
		Map:     map[string][]int{"numbers": {4, 5}},
		Secret:  paramstore.NewSecret([]byte("secret-canary")),
	}
	cloned := Clone(original)

	*cloned.Pointer = 99
	cloned.Bytes[0] = 9
	cloned.Map["numbers"][0] = 8
	cloned.Secret.Value()[0] = 'X'

	if *original.Pointer != 7 || !reflect.DeepEqual(original.Bytes, []byte{1, 2, 3}) ||
		!reflect.DeepEqual(original.Map["numbers"], []int{4, 5}) || original.Secret.StringValue() != "secret-canary" {
		t.Fatalf("Clone shared mutable storage with original: %#v", original)
	}
}

func TestClonePreservesCycles(t *testing.T) {
	original := make(map[string]any)
	original["self"] = original
	cloned := Clone(original)
	cloned["new"] = true
	self, ok := cloned["self"].(map[string]any)
	if !ok || self["new"] != true {
		t.Fatalf("cycle was not preserved: %#v", cloned)
	}
	if _, exists := original["new"]; exists {
		t.Fatal("clone mutation changed original cycle")
	}
}
