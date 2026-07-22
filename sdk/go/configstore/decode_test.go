package configstore

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

type decodeNamedString string
type decodeMapKey string

type decodeNested struct {
	Count int16
	Note  *string
}

type decodeFixture struct {
	Enabled  bool
	Name     decodeNamedString
	Signed   int8
	Unsigned uint16
	Ratio    float32
	Delay    time.Duration
	Blob     []byte
	Nested   decodeNested
	Items    []decodeNested
	Pair     [2]string
	Labels   map[decodeMapKey]int32
	Optional *string
}

func codecPointer(codec ValueCodec) *ValueCodec { return &codec }

func fieldIndex(typ reflect.Type, name string) []int {
	field, ok := typ.FieldByName(name)
	if !ok {
		panic("missing test field " + name)
	}
	return field.Index
}

func nestedCodec() ValueCodec {
	typ := reflect.TypeFor[decodeNested]()
	return ValueCodec{Kind: CodecStruct, Fields: []FieldCodec{
		{JSONName: "count", FieldIndex: fieldIndex(typ, "Count"), Value: ValueCodec{Kind: CodecInt}},
		{JSONName: "note", FieldIndex: fieldIndex(typ, "Note"), Value: ValueCodec{
			Kind: CodecPointer, Element: codecPointer(ValueCodec{Kind: CodecString}),
		}},
	}}
}

func fixtureCodecs() []FieldCodec {
	typ := reflect.TypeFor[decodeFixture]()
	nested := nestedCodec()
	return []FieldCodec{
		{JSONName: "enabled", FieldIndex: fieldIndex(typ, "Enabled"), Value: ValueCodec{Kind: CodecBool}},
		{JSONName: "name", FieldIndex: fieldIndex(typ, "Name"), Value: ValueCodec{Kind: CodecString}},
		{JSONName: "signed", FieldIndex: fieldIndex(typ, "Signed"), Value: ValueCodec{Kind: CodecInt}},
		{JSONName: "unsigned", FieldIndex: fieldIndex(typ, "Unsigned"), Value: ValueCodec{Kind: CodecUint}},
		{JSONName: "ratio", FieldIndex: fieldIndex(typ, "Ratio"), Value: ValueCodec{Kind: CodecFloat}},
		{JSONName: "delay", FieldIndex: fieldIndex(typ, "Delay"), Value: ValueCodec{Kind: CodecDuration}},
		{JSONName: "blob", FieldIndex: fieldIndex(typ, "Blob"), Value: ValueCodec{Kind: CodecBytes}},
		{JSONName: "nested", FieldIndex: fieldIndex(typ, "Nested"), Value: nested},
		{JSONName: "items", FieldIndex: fieldIndex(typ, "Items"), Value: ValueCodec{
			Kind: CodecSlice, Element: &nested,
		}},
		{JSONName: "pair", FieldIndex: fieldIndex(typ, "Pair"), Value: ValueCodec{
			Kind: CodecArray, Element: codecPointer(ValueCodec{Kind: CodecString}),
		}},
		{JSONName: "labels", FieldIndex: fieldIndex(typ, "Labels"), Value: ValueCodec{
			Kind: CodecMap, Element: codecPointer(ValueCodec{Kind: CodecInt}),
		}},
		{JSONName: "optional", FieldIndex: fieldIndex(typ, "Optional"), Value: ValueCodec{
			Kind: CodecPointer, Element: codecPointer(ValueCodec{Kind: CodecString}),
		}},
	}
}

func TestDecodeGroupCanonicalCompositeDocument(t *testing.T) {
	document := `{
		"enabled":true,
		"name":"service",
		"signed":-8,
		"unsigned":42,
		"ratio":1.25,
		"delay":"1m30s",
		"blob":"AAEC/w==",
		"nested":{"count":7,"note":null},
		"items":[{"count":9,"note":"ready"}],
		"pair":["left","right"],
		"labels":{"one":1,"two":2},
		"optional":null
	}`
	var got decodeFixture
	if err := DecodeGroup(document, &got, fixtureCodecs()); err != nil {
		t.Fatalf("DecodeGroup() error = %v", err)
	}
	if got.Enabled != true || got.Name != "service" || got.Signed != -8 || got.Unsigned != 42 || got.Ratio != 1.25 {
		t.Fatalf("scalar decode mismatch: %#v", got)
	}
	if got.Delay != 90*time.Second {
		t.Fatalf("Delay = %s", got.Delay)
	}
	if !reflect.DeepEqual(got.Blob, []byte{0, 1, 2, 255}) {
		t.Fatalf("Blob = %v", got.Blob)
	}
	if got.Nested.Count != 7 || got.Nested.Note != nil || len(got.Items) != 1 || got.Items[0].Note == nil || *got.Items[0].Note != "ready" {
		t.Fatalf("nested decode mismatch: %#v", got)
	}
	if got.Pair != [2]string{"left", "right"} || got.Labels["two"] != 2 || got.Optional != nil {
		t.Fatalf("collection decode mismatch: %#v", got)
	}
}

type oneInt struct{ Value int8 }

func oneIntCodec() []FieldCodec {
	typ := reflect.TypeFor[oneInt]()
	return []FieldCodec{{JSONName: "value", FieldIndex: fieldIndex(typ, "Value"), Value: ValueCodec{Kind: CodecInt}}}
}

func TestDecodeGroupRejectsIncompleteAndNonCanonicalJSONWithoutValuesInErrors(t *testing.T) {
	const canary = "TOP-SECRET-CANARY"
	tests := map[string]string{
		"malformed":         `{"value":"` + canary + `"`,
		"trailing":          `{"value":1} "` + canary + `"`,
		"missing":           `{}`,
		"unknown":           `{"value":1,"` + canary + `":2}`,
		"duplicate":         `{"value":1,"value":2}`,
		"null":              `{"value":null}`,
		"wrong type":        `{"value":"` + canary + `"}`,
		"overflow":          `{"value":128}`,
		"fraction":          `{"value":1.5}`,
		"fraction exponent": `{"value":1e-2}`,
	}
	for name, document := range tests {
		t.Run(name, func(t *testing.T) {
			got := oneInt{Value: 12}
			err := DecodeGroup(document, &got, oneIntCodec())
			if err == nil {
				t.Fatal("DecodeGroup() unexpectedly succeeded")
			}
			if strings.Contains(err.Error(), canary) {
				t.Fatalf("error leaked raw input: %v", err)
			}
			if got.Value != 12 {
				t.Fatalf("destination changed on error: %#v", got)
			}
		})
	}
}

func TestDecodeGroupAcceptsEveryMathematicallyIntegralJSONNumber(t *testing.T) {
	tests := map[string]int8{
		`{"value":1.0}`:    1,
		`{"value":1e2}`:    100,
		`{"value":1.20e1}`: 12,
		`{"value":-0.0}`:   0,
	}
	for document, want := range tests {
		var got oneInt
		if err := DecodeGroup(document, &got, oneIntCodec()); err != nil {
			t.Fatalf("DecodeGroup(%s) error = %v", document, err)
		}
		if got.Value != want {
			t.Fatalf("DecodeGroup(%s) = %d, want %d", document, got.Value, want)
		}
	}
}

type integerBounds struct {
	Signed   int64
	Unsigned uint64
}

func TestDecodeGroupPreservesExactIntegerBoundaries(t *testing.T) {
	typ := reflect.TypeFor[integerBounds]()
	codecs := []FieldCodec{
		{JSONName: "signed", FieldIndex: fieldIndex(typ, "Signed"), Value: ValueCodec{Kind: CodecInt}},
		{JSONName: "unsigned", FieldIndex: fieldIndex(typ, "Unsigned"), Value: ValueCodec{Kind: CodecUint}},
	}
	var got integerBounds
	if err := DecodeGroup(`{"signed":-9.223372036854775808e18,"unsigned":1.8446744073709551615e19}`, &got, codecs); err != nil {
		t.Fatalf("DecodeGroup() boundary error = %v", err)
	}
	if got.Signed != -1<<63 || got.Unsigned != 1<<64-1 {
		t.Fatalf("boundary decode = %#v", got)
	}
	for _, document := range []string{
		`{"signed":-9.223372036854775809e18,"unsigned":0}`,
		`{"signed":0,"unsigned":1.8446744073709551616e19}`,
	} {
		if err := DecodeGroup(document, &got, codecs); err == nil {
			t.Fatalf("DecodeGroup(%s) unexpectedly accepted overflow", document)
		}
	}
}

func TestDecodeGroupHonorsGeneratedPortableIntegerWidth(t *testing.T) {
	type portableInt struct{ Value int }
	typ := reflect.TypeFor[portableInt]()
	codecs := []FieldCodec{{
		JSONName: "value", FieldIndex: fieldIndex(typ, "Value"), Value: ValueCodec{Kind: CodecInt, Bits: 32},
	}}
	var got portableInt
	if err := DecodeGroup(`{"value":2147483647}`, &got, codecs); err != nil || got.Value != 2147483647 {
		t.Fatalf("portable int maximum decode = %d, %v", got.Value, err)
	}
	if err := DecodeGroup(`{"value":2147483648}`, &got, codecs); err == nil {
		t.Fatal("portable int codec accepted a schema-invalid 64-bit-only value")
	}
}

type floatBounds struct{ Value float32 }

type namedIntPointer *int32

func TestDecodeGroupSupportsNamedPointerToScalar(t *testing.T) {
	type holder struct{ Value namedIntPointer }
	typ := reflect.TypeFor[holder]()
	codecs := []FieldCodec{{
		JSONName: "value", FieldIndex: fieldIndex(typ, "Value"),
		Value: ValueCodec{Kind: CodecPointer, Element: codecPointer(ValueCodec{Kind: CodecInt, Bits: 32})},
	}}
	var got holder
	if err := DecodeGroup(`{"value":17}`, &got, codecs); err != nil {
		t.Fatal(err)
	}
	if got.Value == nil || *got.Value != 17 {
		t.Fatalf("named pointer decode = %#v", got.Value)
	}
}

func TestDecodeGroupFloatRangeMatchesGeneratedSchemaBoundary(t *testing.T) {
	typ := reflect.TypeFor[floatBounds]()
	codecs := []FieldCodec{{JSONName: "value", FieldIndex: fieldIndex(typ, "Value"), Value: ValueCodec{Kind: CodecFloat}}}
	for _, document := range []string{
		`{"value":3.4028234663852886e38}`,
		`{"value":-3.4028234663852886e38}`,
		`{"value":1e-1000}`,
	} {
		var got floatBounds
		if err := DecodeGroup(document, &got, codecs); err != nil {
			t.Fatalf("DecodeGroup(%s) error = %v", document, err)
		}
	}
	for _, document := range []string{
		`{"value":3.4028234663852887e38}`,
		`{"value":-3.4028234663852887e38}`,
	} {
		var got floatBounds
		if err := DecodeGroup(document, &got, codecs); err == nil {
			t.Fatalf("DecodeGroup(%s) unexpectedly accepted schema overflow", document)
		}
	}
}

func TestDecodeGroupRejectsRecursiveShapeErrors(t *testing.T) {
	typ := reflect.TypeFor[decodeFixture]()
	nested := nestedCodec()
	tests := map[string]struct {
		document string
		codec    FieldCodec
	}{
		"nested missing": {
			document: `{"nested":{"count":1}}`,
			codec:    FieldCodec{JSONName: "nested", FieldIndex: fieldIndex(typ, "Nested"), Value: nested},
		},
		"nested unknown": {
			document: `{"nested":{"count":1,"note":null,"extra":"canary"}}`,
			codec:    FieldCodec{JSONName: "nested", FieldIndex: fieldIndex(typ, "Nested"), Value: nested},
		},
		"nested duplicate": {
			document: `{"nested":{"count":1,"count":2,"note":null}}`,
			codec:    FieldCodec{JSONName: "nested", FieldIndex: fieldIndex(typ, "Nested"), Value: nested},
		},
		"array length": {
			document: `{"pair":["only-one"]}`,
			codec: FieldCodec{JSONName: "pair", FieldIndex: fieldIndex(typ, "Pair"), Value: ValueCodec{
				Kind: CodecArray, Element: codecPointer(ValueCodec{Kind: CodecString}),
			}},
		},
		"duplicate map key": {
			document: `{"labels":{"same":1,"same":2}}`,
			codec: FieldCodec{JSONName: "labels", FieldIndex: fieldIndex(typ, "Labels"), Value: ValueCodec{
				Kind: CodecMap, Element: codecPointer(ValueCodec{Kind: CodecInt}),
			}},
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			var got decodeFixture
			if err := DecodeGroup(test.document, &got, []FieldCodec{test.codec}); err == nil {
				t.Fatal("DecodeGroup() unexpectedly succeeded")
			}
		})
	}
}

func TestDecodeGroupRejectsInvalidDurationAndBase64WithoutEchoingValues(t *testing.T) {
	const canary = "CANARY-NOT-A-CANONICAL-VALUE"
	typ := reflect.TypeFor[decodeFixture]()
	tests := []struct {
		document string
		codec    FieldCodec
	}{
		{
			document: `{"delay":"` + canary + `"}`,
			codec:    FieldCodec{JSONName: "delay", FieldIndex: fieldIndex(typ, "Delay"), Value: ValueCodec{Kind: CodecDuration}},
		},
		{
			document: `{"blob":"` + canary + `"}`,
			codec:    FieldCodec{JSONName: "blob", FieldIndex: fieldIndex(typ, "Blob"), Value: ValueCodec{Kind: CodecBytes}},
		},
	}
	for _, test := range tests {
		var got decodeFixture
		err := DecodeGroup(test.document, &got, []FieldCodec{test.codec})
		if err == nil {
			t.Fatal("DecodeGroup() unexpectedly succeeded")
		}
		if strings.Contains(err.Error(), canary) {
			t.Fatalf("error leaked raw value: %v", err)
		}
	}
}

func TestDecodeGroupPreservesNilVersusEmptyCollections(t *testing.T) {
	type nullableCollections struct {
		Items  []string
		Labels map[string]int
		Blob   []byte
	}
	typ := reflect.TypeFor[nullableCollections]()
	codecs := []FieldCodec{
		{JSONName: "items", FieldIndex: fieldIndex(typ, "Items"), Value: ValueCodec{Kind: CodecSlice, Element: codecPointer(ValueCodec{Kind: CodecString})}},
		{JSONName: "labels", FieldIndex: fieldIndex(typ, "Labels"), Value: ValueCodec{Kind: CodecMap, Element: codecPointer(ValueCodec{Kind: CodecInt})}},
		{JSONName: "blob", FieldIndex: fieldIndex(typ, "Blob"), Value: ValueCodec{Kind: CodecBytes}},
	}

	var nilValues nullableCollections
	if err := DecodeGroup(`{"items":null,"labels":null,"blob":null}`, &nilValues, codecs); err != nil {
		t.Fatalf("DecodeGroup(null collections) error = %v", err)
	}
	if nilValues.Items != nil || nilValues.Labels != nil || nilValues.Blob != nil {
		t.Fatalf("null collections did not decode to Go nil: %#v", nilValues)
	}

	var emptyValues nullableCollections
	if err := DecodeGroup(`{"items":[],"labels":{},"blob":""}`, &emptyValues, codecs); err != nil {
		t.Fatalf("DecodeGroup(empty collections) error = %v", err)
	}
	if emptyValues.Items == nil || emptyValues.Labels == nil || emptyValues.Blob == nil ||
		len(emptyValues.Items) != 0 || len(emptyValues.Labels) != 0 || len(emptyValues.Blob) != 0 {
		t.Fatalf("empty collections did not remain non-nil empty values: %#v", emptyValues)
	}
}

func TestRejectDecodeMapsOnlyGeneratedCanonicalPaths(t *testing.T) {
	tests := []struct {
		name     string
		document string
		group    string
		want     []string
	}{
		{name: "malformed document", document: `{`, group: "runtime", want: []string{"runtime"}},
		{name: "missing field", document: `{}`, group: "runtime", want: []string{"runtime.value"}},
		{name: "unknown input name stays at group", document: `{"value":1,"RAW-SECRET-CANARY":2}`, group: "runtime", want: []string{"runtime"}},
		{name: "invalid caller group is omitted", document: `{}`, group: "runtime\ncanary", want: nil},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var destination oneInt
			decodeErr := DecodeGroup(test.document, &destination, oneIntCodec())
			if decodeErr == nil {
				t.Fatal("DecodeGroup() unexpectedly succeeded")
			}
			err := RejectDecode(test.group, decodeErr)
			var candidateErr *CandidateError
			if !errors.As(err, &candidateErr) {
				t.Fatalf("RejectDecode() = %T, want *CandidateError", err)
			}
			if got := candidateErr.pathsCopy(); !reflect.DeepEqual(got, test.want) {
				t.Fatalf("diagnostic paths = %#v, want %#v", got, test.want)
			}
			if strings.Contains(strings.Join(candidateErr.pathsCopy(), ","), "RAW-SECRET-CANARY") {
				t.Fatal("unknown JSON property leaked into canonical path")
			}
		})
	}
}
