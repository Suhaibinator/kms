package configstore

import (
	"encoding/json/v2"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestEnumKeyMapConformance(t *testing.T) {
	data, err := os.ReadFile("../../testdata/enum-key-maps.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Cases []struct {
			Name    string `json:"name"`
			KeyKind string `json:"key_kind"`
			Members []struct {
				Name   string `json:"name"`
				Number int64  `json:"number"`
			} `json:"members"`
			Document string `json:"document"`
			Valid    bool   `json:"valid"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	for _, tc := range fixture.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			var target any = &struct{ Entries map[enumLevel]string }{}
			members := make([]EnumMember, len(tc.Members))
			for i, member := range tc.Members {
				members[i] = EnumMember{Name: member.Name, Number: member.Number}
			}
			key := ValueCodec{Kind: CodecInt, Bits: 8, Enum: members}
			if tc.KeyKind == "string" {
				target = &struct{ Entries map[enumMode]string }{}
				key.Kind = CodecString
				key.Bits = 0
			}
			fields := []FieldCodec{{JSONName: "entries", FieldIndex: []int{0}, Value: ValueCodec{Kind: CodecMap, Key: &key, Element: new(ValueCodec{Kind: CodecString})}}}
			err := DecodeGroup(tc.Document, target, fields)
			if !tc.Valid {
				if err == nil {
					t.Fatal("accepted invalid map")
				}
				if strings.Contains(err.Error(), "PRIVATE_UNKNOWN_KEY") {
					t.Fatalf("error leaked rejected key: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := EncodeGroup(target, fields)
			if err != nil {
				t.Fatal(err)
			}
			want, err := CanonicalParameterValue("json", []byte(tc.Document))
			if err != nil {
				t.Fatal(err)
			}
			if string(encoded) != string(want) {
				t.Fatalf("encoded %s, want %s", encoded, want)
			}
		})
	}
}

func TestEnumKeyMapRejectsInvalidDescriptorsAndValues(t *testing.T) {
	for _, tc := range []struct {
		name  string
		key   ValueCodec
		value map[enumLevel]string
	}{
		{"plain integer", ValueCodec{Kind: CodecInt}, nil},
		{"duplicate names", ValueCodec{Kind: CodecInt, Bits: 8, Enum: []EnumMember{{Name: "SAME", Number: 0}, {Name: "SAME", Number: 1}}}, nil},
		{"alias values", ValueCodec{Kind: CodecInt, Bits: 8, Enum: []EnumMember{{Name: "ZERO", Number: 0}, {Name: "ALIAS", Number: 0}}}, nil},
		{"wrong kind", ValueCodec{Kind: CodecString, Enum: levelMembers}, nil},
		{"invalid width", ValueCodec{Kind: CodecInt, Bits: 8, Enum: []EnumMember{{Name: "BIG", Number: 300}}}, nil},
		{"unknown number", ValueCodec{Kind: CodecInt, Bits: 8, Enum: levelMembers}, map[enumLevel]string{7: "hidden"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target := struct{ Entries map[enumLevel]string }{tc.value}
			fields := []FieldCodec{{JSONName: "entries", FieldIndex: []int{0}, Value: ValueCodec{Kind: CodecMap, Key: &tc.key, Element: new(ValueCodec{Kind: CodecString})}}}
			if _, err := EncodeGroup(&target, fields); err == nil {
				t.Fatal("encoded invalid map")
			}
			if tc.value == nil {
				for _, doc := range []string{`{"entries":null}`, `{"entries":{}}`} {
					if err := DecodeGroup(doc, &target, fields); err == nil {
						t.Fatalf("decoded invalid descriptor for %s", doc)
					}
				}
			}
		})
	}
}

func TestEnumKeyMapDecodeIsAtomic(t *testing.T) {
	target := struct{ Entries map[enumLevel]string }{map[enumLevel]string{-1: "old"}}
	before := map[enumLevel]string{-1: "old"}
	fields := []FieldCodec{{JSONName: "entries", FieldIndex: []int{0}, Value: ValueCodec{Kind: CodecMap, Key: new(ValueCodec{Kind: CodecInt, Bits: 8, Enum: levelMembers}), Element: new(ValueCodec{Kind: CodecString})}}}
	if err := DecodeGroup(`{"entries":{"NORMAL":"new","UNKNOWN":"bad"}}`, &target, fields); err == nil {
		t.Fatal("expected unknown key error")
	}
	if !reflect.DeepEqual(target.Entries, before) {
		t.Fatalf("failed decode changed map: %#v", target.Entries)
	}
}
