package composedgenerated

import (
	"strings"
	"testing"

	"github.com/Suhaibinator/kms/internal/configgen/testdata/commonfragment"
	rootconfig "github.com/Suhaibinator/kms/internal/configgen/testdata/composed"
	"github.com/Suhaibinator/kms/sdk/go/kmsclient"
)

func TestEncodeParameterGroupsFlattensInlineFragments(t *testing.T) {
	root := &rootconfig.Config{
		Common: &commonfragment.Config{
			Endpoint: "db.internal",
			Token:    kmsclient.NewSecret([]byte("inline-secret-must-not-appear")),
			Limits:   &commonfragment.Limits{Burst: 12},
		},
		AppName: "launch-partner",
	}

	groups, err := EncodeParameterGroups(root)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(groups["database"]), `{"endpoint":"db.internal"}`; got != want {
		t.Fatalf("database group = %s, want %s", got, want)
	}
	if got, want := string(groups["runtime"]), `{"app_name":"launch-partner","burst":12}`; got != want {
		t.Fatalf("runtime group = %s, want %s", got, want)
	}
	for alias, document := range groups {
		if strings.Contains(string(document), "inline-secret-must-not-appear") || strings.Contains(string(document), "common_token") {
			t.Fatalf("group %q exposed a secret: %s", alias, document)
		}
	}
}

func TestEncodeParameterGroupsRejectsNilInlinePointers(t *testing.T) {
	tests := []struct {
		name string
		root *rootconfig.Config
	}{
		{name: "nil root", root: nil},
		{name: "nil common fragment", root: &rootconfig.Config{}},
		{name: "nil nested fragment", root: &rootconfig.Config{Common: &commonfragment.Config{}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := EncodeParameterGroups(test.root); err == nil {
				t.Fatal("invalid inline pointer graph was encoded")
			}
		})
	}
}
