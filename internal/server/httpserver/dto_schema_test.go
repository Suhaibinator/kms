package httpserver

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/Suhaibinator/kms/internal/domain"
)

func TestSchemaDTOContractPresence(t *testing.T) {
	nonempty := []domain.ApplicationContractField{
		{Alias: "config", Kind: domain.ReleaseEntryParameter, ContentType: "json"},
		{Alias: "token", Kind: domain.ReleaseEntrySecret},
	}
	for _, test := range []struct {
		name     string
		contract []domain.ApplicationContractField
		wantSet  bool
		want     any
	}{
		{name: "unknown", contract: nil, wantSet: false},
		{name: "established empty", contract: []domain.ApplicationContractField{}, wantSet: true, want: []any{}},
		{name: "established nonempty", contract: nonempty, wantSet: true, want: []any{
			map[string]any{"alias": "config", "kind": "parameter", "content_type": "json"},
			map[string]any{"alias": "token", "kind": "secret"},
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			encoded, err := json.Marshal(toSchemaDTO(domain.ConfigurationSchema{Contract: test.contract}))
			if err != nil {
				t.Fatal(err)
			}
			var object map[string]any
			if err := json.Unmarshal(encoded, &object); err != nil {
				t.Fatal(err)
			}
			got, set := object["contract"]
			if set != test.wantSet || set && !reflect.DeepEqual(got, test.want) {
				t.Fatalf("contract present=%v value=%#v, want present=%v value=%#v", set, got, test.wantSet, test.want)
			}
		})
	}
}
