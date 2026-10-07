package configstore

import (
	"reflect"
	"strings"
	"testing"
)

type enumLevel int8
type enumSink uint16
type enumMode string

type enumFixture struct {
	Level    enumLevel
	Optional *enumLevel
	Sinks    []enumSink
	Modes    map[string]enumMode
}

var levelMembers = []EnumMember{{Name: "LOW", Number: -1}, {Name: "NORMAL", Number: 0}, {Name: "HIGH", Number: 5}}

func enumCodecs() []FieldCodec {
	typ := reflect.TypeFor[enumFixture]()
	level := ValueCodec{Kind: CodecInt, Bits: 8, Enum: levelMembers}
	return []FieldCodec{
		{JSONName: "level", FieldIndex: fieldIndex(typ, "Level"), Value: level},
		{JSONName: "optional", FieldIndex: fieldIndex(typ, "Optional"), Value: ValueCodec{Kind: CodecPointer, Element: &level}},
		{JSONName: "sinks", FieldIndex: fieldIndex(typ, "Sinks"), Value: ValueCodec{Kind: CodecSlice, Element: new(ValueCodec{
			Kind: CodecUint, Bits: 16, Enum: []EnumMember{{Name: "none", Number: 0}, {Name: "file", Number: 2}},
		})}},
		{JSONName: "modes", FieldIndex: fieldIndex(typ, "Modes"), Value: ValueCodec{Kind: CodecMap, Element: new(ValueCodec{
			Kind: CodecString, Enum: []EnumMember{{Name: "fast"}, {Name: "safe"}},
		})}},
	}
}

func TestEnumCodecRoundTripsNames(t *testing.T) {
	const document = `{"level":"LOW","modes":{"a":"safe"},"optional":"HIGH","sinks":["file","none"]}`
	var decoded enumFixture
	if err := DecodeGroup(document, &decoded, enumCodecs()); err != nil {
		t.Fatal(err)
	}
	want := enumFixture{Level: -1, Optional: new(enumLevel(5)), Sinks: []enumSink{2, 0}, Modes: map[string]enumMode{"a": "safe"}}
	if !reflect.DeepEqual(decoded, want) {
		t.Fatalf("decoded = %#v, want %#v", decoded, want)
	}
	encoded, err := EncodeGroup(&decoded, enumCodecs())
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := CanonicalParameterValue("json", encoded)
	if err != nil {
		t.Fatal(err)
	}
	if string(canonical) != document {
		t.Fatalf("encoded = %s, want %s", canonical, document)
	}
}

func TestEnumCodecRejectsNumbersAndUnknownNamesWithoutEchoingValues(t *testing.T) {
	const valid = `{"level":"LOW","modes":{"a":"safe"},"optional":null,"sinks":[]}`
	for _, tc := range []struct{ name, document, want string }{
		{"number for int enum", strings.Replace(valid, `"LOW"`, `-1`, 1), "expected enum name at $.level"},
		{"unknown int enum name", strings.Replace(valid, `"LOW"`, `"SECRET_LEVEL"`, 1), `unknown enum value at $.level; allowed values are "LOW", "NORMAL", "HIGH"`},
		{"case differs", strings.Replace(valid, `"LOW"`, `"low"`, 1), "unknown enum value at $.level"},
		{"number for uint enum", strings.Replace(valid, `"sinks":[]`, `"sinks":[2]`, 1), "expected enum name at $.sinks[]"},
		{"unknown uint enum name", strings.Replace(valid, `"sinks":[]`, `"sinks":["SECRET_LEVEL"]`, 1), `allowed values are "none", "file"`},
		{"unknown string enum value", strings.Replace(valid, `"safe"`, `"SECRET_LEVEL"`, 1), `unknown enum value at $.modes[*]; allowed values are "fast", "safe"`},
		{"null enum", strings.Replace(valid, `"LOW"`, `null`, 1), "expected enum at $.level"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var decoded enumFixture
			err := DecodeGroup(tc.document, &decoded, enumCodecs())
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want substring %q", err, tc.want)
			}
			if strings.Contains(err.Error(), "SECRET_LEVEL") {
				t.Fatalf("error echoes the rejected value: %v", err)
			}
		})
	}
}

func TestEnumCodecRangeChecksMemberNumbers(t *testing.T) {
	typ := reflect.TypeFor[enumFixture]()
	for _, tc := range []struct {
		name   string
		fields []FieldCodec
		doc    string
	}{
		{"signed member wider than bits", []FieldCodec{{JSONName: "level", FieldIndex: fieldIndex(typ, "Level"), Value: ValueCodec{
			Kind: CodecInt, Bits: 8, Enum: []EnumMember{{Name: "BIG", Number: 300}},
		}}}, `{"level":"BIG"}`},
		{"negative unsigned member", []FieldCodec{{JSONName: "sinks", FieldIndex: fieldIndex(typ, "Sinks"), Value: ValueCodec{
			Kind: CodecSlice, Element: new(ValueCodec{Kind: CodecUint, Bits: 16, Enum: []EnumMember{{Name: "minus", Number: -1}}}),
		}}}, `{"sinks":["minus"]}`},
		{"unsigned member wider than bits", []FieldCodec{{JSONName: "sinks", FieldIndex: fieldIndex(typ, "Sinks"), Value: ValueCodec{
			Kind: CodecSlice, Element: new(ValueCodec{Kind: CodecUint, Bits: 16, Enum: []EnumMember{{Name: "big", Number: 1 << 16}}}),
		}}}, `{"sinks":["big"]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var decoded enumFixture
			err := DecodeGroup(tc.doc, &decoded, tc.fields)
			if err == nil || !strings.Contains(err.Error(), "enum number does not fit the destination width") {
				t.Fatalf("error = %v, want enum width descriptor error", err)
			}
		})
	}
}

func TestEnumCodecRejectsUndeclaredValuesOnEncode(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value enumFixture
		want  string
	}{
		{"int enum", enumFixture{Level: 3}, "value 3 at $.level is not a declared enum number"},
		{"pointer int enum", enumFixture{Optional: new(enumLevel(-7))}, "value -7 at $.optional is not a declared enum number"},
		{"uint enum", enumFixture{Sinks: []enumSink{1}}, "value 1 at $.sinks[] is not a declared enum number"},
		{"string enum", enumFixture{Modes: map[string]enumMode{"a": "turbo"}}, "value at $.modes[*] is not a declared enum value"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := EncodeGroup(&tc.value, enumCodecs())
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want substring %q", err, tc.want)
			}
		})
	}
}
