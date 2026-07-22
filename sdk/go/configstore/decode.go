package configstore

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"reflect"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// CodecKind selects the canonical JSON representation for a generated field.
type CodecKind uint8

const (
	CodecInvalid CodecKind = iota
	CodecBool
	CodecString
	CodecInt
	CodecUint
	CodecFloat
	CodecDuration
	CodecStruct
	CodecArray
	CodecSlice
	CodecMap
	CodecBytes
	CodecPointer
)

// ValueCodec recursively describes one generated Go value. Element is required
// for arrays, slices, maps, and pointers. Fields is required for structs.
type ValueCodec struct {
	Kind    CodecKind
	Element *ValueCodec
	Fields  []FieldCodec
}

// FieldCodec maps one required JSON property to a generated Go struct field.
// FieldIndex is the reflect.StructField.Index path emitted at generation time.
type FieldCodec struct {
	JSONName   string
	FieldIndex []int
	Value      ValueCodec
}

type jsonNodeKind uint8

const (
	nodeNull jsonNodeKind = iota
	nodeBool
	nodeString
	nodeNumber
	nodeObject
	nodeArray
)

type jsonNode struct {
	kind       jsonNodeKind
	boolean    bool
	text       string
	properties []jsonProperty
	elements   []jsonNode
}

type jsonProperty struct {
	name  string
	value jsonNode
}

const maxJSONDepth = 1_000

var errInvalidJSON = errors.New("configstore: invalid JSON document")

// DecodeGroup strictly decodes one complete parameter group document into
// dst. Dst must be a non-nil pointer to a struct. Every described field is
// required, and every JSON property must be described exactly once.
//
// Errors contain only generated canonical paths and fixed diagnostics; raw
// JSON values and unknown property names are never included.
func DecodeGroup(document string, dst any, fields []FieldCodec) error {
	root, err := parseJSONDocument(document)
	if err != nil {
		return errInvalidJSON
	}
	value := reflect.ValueOf(dst)
	if !value.IsValid() || value.Kind() != reflect.Pointer || value.IsNil() || value.Elem().Kind() != reflect.Struct {
		return errors.New("configstore: DecodeGroup destination must be a non-nil pointer to a struct")
	}
	if err := decodeObject(root, value.Elem(), fields, "$"); err != nil {
		return err
	}
	return nil
}

func parseJSONDocument(document string) (jsonNode, error) {
	if !utf8.ValidString(document) {
		return jsonNode{}, errInvalidJSON
	}
	decoder := json.NewDecoder(strings.NewReader(document))
	decoder.UseNumber()
	token, err := decoder.Token()
	if err != nil {
		return jsonNode{}, errInvalidJSON
	}
	root, err := readJSONNode(decoder, token, 0)
	if err != nil {
		return jsonNode{}, errInvalidJSON
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return jsonNode{}, errInvalidJSON
	}
	return root, nil
}

func readJSONNode(decoder *json.Decoder, token json.Token, depth int) (jsonNode, error) {
	if depth > maxJSONDepth {
		return jsonNode{}, errInvalidJSON
	}
	switch item := token.(type) {
	case nil:
		return jsonNode{kind: nodeNull}, nil
	case bool:
		return jsonNode{kind: nodeBool, boolean: item}, nil
	case string:
		return jsonNode{kind: nodeString, text: item}, nil
	case json.Number:
		return jsonNode{kind: nodeNumber, text: item.String()}, nil
	case json.Delim:
		switch item {
		case '{':
			node := jsonNode{kind: nodeObject}
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return jsonNode{}, errInvalidJSON
				}
				key, ok := keyToken.(string)
				if !ok {
					return jsonNode{}, errInvalidJSON
				}
				valueToken, err := decoder.Token()
				if err != nil {
					return jsonNode{}, errInvalidJSON
				}
				value, err := readJSONNode(decoder, valueToken, depth+1)
				if err != nil {
					return jsonNode{}, errInvalidJSON
				}
				node.properties = append(node.properties, jsonProperty{name: key, value: value})
			}
			end, err := decoder.Token()
			if err != nil || end != json.Delim('}') {
				return jsonNode{}, errInvalidJSON
			}
			return node, nil
		case '[':
			node := jsonNode{kind: nodeArray}
			for decoder.More() {
				valueToken, err := decoder.Token()
				if err != nil {
					return jsonNode{}, errInvalidJSON
				}
				value, err := readJSONNode(decoder, valueToken, depth+1)
				if err != nil {
					return jsonNode{}, errInvalidJSON
				}
				node.elements = append(node.elements, value)
			}
			end, err := decoder.Token()
			if err != nil || end != json.Delim(']') {
				return jsonNode{}, errInvalidJSON
			}
			return node, nil
		default:
			return jsonNode{}, errInvalidJSON
		}
	default:
		return jsonNode{}, errInvalidJSON
	}
}

func decodeObject(node jsonNode, destination reflect.Value, fields []FieldCodec, path string) error {
	if node.kind != nodeObject {
		return decodeTypeError(path, "object")
	}
	if destination.Kind() != reflect.Struct {
		return descriptorError(path, "destination is not a struct")
	}

	byName := make(map[string]int, len(fields))
	fieldTypes := make([]reflect.Type, len(fields))
	for i, field := range fields {
		if field.JSONName == "" || len(field.FieldIndex) == 0 {
			return descriptorError(path, "field descriptor is incomplete")
		}
		if _, exists := byName[field.JSONName]; exists {
			return descriptorError(path, "field descriptor is duplicated")
		}
		fieldType, err := typeByFieldIndex(destination.Type(), field.FieldIndex)
		if err != nil {
			return descriptorError(path, "field index is invalid")
		}
		byName[field.JSONName] = i
		fieldTypes[i] = fieldType
	}

	values := make([]*jsonNode, len(fields))
	for i := range node.properties {
		property := &node.properties[i]
		fieldIndex, known := byName[property.name]
		if !known {
			return fmt.Errorf("configstore: unknown field at %s", path)
		}
		if values[fieldIndex] != nil {
			return fmt.Errorf("configstore: duplicate field at %s", childPath(path, fields[fieldIndex].JSONName))
		}
		values[fieldIndex] = &property.value
	}
	for i := range fields {
		if values[i] == nil {
			return fmt.Errorf("configstore: missing required field at %s", childPath(path, fields[i].JSONName))
		}
	}

	decoded := make([]reflect.Value, len(fields))
	for i, field := range fields {
		decoded[i] = reflect.New(fieldTypes[i]).Elem()
		if err := decodeValue(*values[i], decoded[i], field.Value, childPath(path, field.JSONName)); err != nil {
			return err
		}
	}
	for i, field := range fields {
		if err := setByFieldIndex(destination, field.FieldIndex, decoded[i]); err != nil {
			return descriptorError(path, "field cannot be assigned")
		}
	}
	return nil
}

func decodeValue(node jsonNode, destination reflect.Value, codec ValueCodec, path string) error {
	if codec.Kind == CodecPointer {
		if destination.Kind() != reflect.Pointer || codec.Element == nil {
			return descriptorError(path, "pointer descriptor does not match destination")
		}
		if node.kind == nodeNull {
			destination.SetZero()
			return nil
		}
		item := reflect.New(destination.Type().Elem())
		if err := decodeValue(node, item.Elem(), *codec.Element, path); err != nil {
			return err
		}
		destination.Set(item)
		return nil
	}
	if node.kind == nodeNull {
		return decodeTypeError(path, codecName(codec.Kind))
	}

	switch codec.Kind {
	case CodecBool:
		if node.kind != nodeBool {
			return decodeTypeError(path, "boolean")
		}
		if destination.Kind() != reflect.Bool {
			return descriptorError(path, "boolean descriptor does not match destination")
		}
		destination.SetBool(node.boolean)

	case CodecString:
		if node.kind != nodeString {
			return decodeTypeError(path, "string")
		}
		if destination.Kind() != reflect.String {
			return descriptorError(path, "string descriptor does not match destination")
		}
		destination.SetString(node.text)

	case CodecInt:
		if node.kind != nodeNumber {
			return decodeTypeError(path, "integer")
		}
		if !isSignedInteger(destination.Kind()) {
			return descriptorError(path, "integer descriptor does not match destination")
		}
		value, err := parseSignedJSONInteger(node.text, destination.Type().Bits())
		if err != nil {
			return decodeRangeError(path, "integer")
		}
		destination.SetInt(value)

	case CodecUint:
		if node.kind != nodeNumber {
			return decodeTypeError(path, "unsigned integer")
		}
		if !isUnsignedInteger(destination.Kind()) {
			return descriptorError(path, "unsigned integer descriptor does not match destination")
		}
		value, err := parseUnsignedJSONInteger(node.text, destination.Type().Bits())
		if err != nil {
			return decodeRangeError(path, "unsigned integer")
		}
		destination.SetUint(value)

	case CodecFloat:
		if node.kind != nodeNumber {
			return decodeTypeError(path, "number")
		}
		if destination.Kind() != reflect.Float32 && destination.Kind() != reflect.Float64 {
			return descriptorError(path, "number descriptor does not match destination")
		}
		value, err := parseJSONFloat(node.text, destination.Type().Bits())
		if err != nil || math.IsInf(value, 0) || math.IsNaN(value) {
			return decodeRangeError(path, "number")
		}
		destination.SetFloat(value)

	case CodecDuration:
		if node.kind != nodeString {
			return decodeTypeError(path, "duration string")
		}
		if destination.Kind() != reflect.Int64 {
			return descriptorError(path, "duration descriptor does not match destination")
		}
		value, err := time.ParseDuration(node.text)
		if err != nil {
			return fmt.Errorf("configstore: invalid duration at %s", path)
		}
		destination.SetInt(int64(value))

	case CodecStruct:
		if destination.Kind() != reflect.Struct {
			return descriptorError(path, "struct descriptor does not match destination")
		}
		return decodeObject(node, destination, codec.Fields, path)

	case CodecArray:
		if node.kind != nodeArray {
			return decodeTypeError(path, "array")
		}
		if destination.Kind() != reflect.Array || codec.Element == nil {
			return descriptorError(path, "array descriptor does not match destination")
		}
		if len(node.elements) != destination.Len() {
			return fmt.Errorf("configstore: wrong array length at %s", path)
		}
		for i := range node.elements {
			if err := decodeValue(node.elements[i], destination.Index(i), *codec.Element, path+"[]"); err != nil {
				return err
			}
		}

	case CodecSlice:
		if node.kind != nodeArray {
			return decodeTypeError(path, "array")
		}
		if destination.Kind() != reflect.Slice || codec.Element == nil {
			return descriptorError(path, "slice descriptor does not match destination")
		}
		items := reflect.MakeSlice(destination.Type(), len(node.elements), len(node.elements))
		for i := range node.elements {
			if err := decodeValue(node.elements[i], items.Index(i), *codec.Element, path+"[]"); err != nil {
				return err
			}
		}
		destination.Set(items)

	case CodecMap:
		if node.kind != nodeObject {
			return decodeTypeError(path, "object")
		}
		if destination.Kind() != reflect.Map || destination.Type().Key().Kind() != reflect.String || codec.Element == nil {
			return descriptorError(path, "map descriptor does not match destination")
		}
		items := reflect.MakeMapWithSize(destination.Type(), len(node.properties))
		seen := make(map[string]struct{}, len(node.properties))
		for _, property := range node.properties {
			if _, exists := seen[property.name]; exists {
				return fmt.Errorf("configstore: duplicate map key at %s", path)
			}
			seen[property.name] = struct{}{}
			key := reflect.New(destination.Type().Key()).Elem()
			key.SetString(property.name)
			item := reflect.New(destination.Type().Elem()).Elem()
			if err := decodeValue(property.value, item, *codec.Element, path+"[*]"); err != nil {
				return err
			}
			items.SetMapIndex(key, item)
		}
		destination.Set(items)

	case CodecBytes:
		if node.kind != nodeString {
			return decodeTypeError(path, "base64 string")
		}
		if destination.Kind() != reflect.Slice || destination.Type().Elem().Kind() != reflect.Uint8 {
			return descriptorError(path, "byte descriptor does not match destination")
		}
		decoded, err := base64.StdEncoding.Strict().DecodeString(node.text)
		if err != nil || base64.StdEncoding.EncodeToString(decoded) != node.text {
			return fmt.Errorf("configstore: invalid base64 at %s", path)
		}
		bytes := reflect.MakeSlice(destination.Type(), len(decoded), len(decoded))
		reflect.Copy(bytes, reflect.ValueOf(decoded))
		destination.Set(bytes)

	default:
		return descriptorError(path, "codec kind is invalid")
	}
	return nil
}

func parseSignedJSONInteger(text string, bits int) (int64, error) {
	integer, err := exactJSONInteger(text)
	if err != nil || !integer.IsInt64() {
		return 0, errors.New("integer out of range")
	}
	value := integer.Int64()
	if bits < 64 {
		minimum := -(int64(1) << (bits - 1))
		maximum := (int64(1) << (bits - 1)) - 1
		if value < minimum || value > maximum {
			return 0, errors.New("integer out of range")
		}
	}
	return value, nil
}

func parseUnsignedJSONInteger(text string, bits int) (uint64, error) {
	integer, err := exactJSONInteger(text)
	if err != nil || !integer.IsUint64() {
		return 0, errors.New("unsigned integer out of range")
	}
	value := integer.Uint64()
	if bits < 64 && value > (uint64(1)<<bits)-1 {
		return 0, errors.New("unsigned integer out of range")
	}
	return value, nil
}

// exactJSONInteger accepts every JSON number that is mathematically integral,
// including decimal and exponent spellings accepted by JSON Schema's integer
// type. big.Rat avoids float64 rounding at int64/uint64 boundaries.
func exactJSONInteger(text string) (*big.Int, error) {
	value, ok := new(big.Rat).SetString(text)
	if !ok || value.Denom().Cmp(big.NewInt(1)) != 0 {
		return nil, errors.New("JSON number is not an exact integer")
	}
	return new(big.Int).Set(value.Num()), nil
}

func parseJSONFloat(text string, bits int) (float64, error) {
	exact, ok := new(big.Rat).SetString(text)
	if !ok {
		return 0, errors.New("invalid JSON number")
	}
	maximumText := strconv.FormatFloat(math.MaxFloat64, 'g', -1, 64)
	if bits == 32 {
		// The generated JSON Schema serializes the float32 boundary after
		// widening it to float64, matching encoding/json's numeric output.
		maximumText = strconv.FormatFloat(float64(math.MaxFloat32), 'g', -1, 64)
	}
	maximum, ok := new(big.Rat).SetString(maximumText)
	if !ok {
		panic("configstore: internal floating-point boundary is invalid")
	}
	magnitude := new(big.Rat).Abs(exact)
	if magnitude.Cmp(maximum) > 0 {
		return 0, errors.New("JSON number is outside configured floating-point range")
	}
	value, err := strconv.ParseFloat(text, bits)
	if err != nil || math.IsInf(value, 0) || math.IsNaN(value) {
		return 0, errors.New("JSON number is outside configured floating-point range")
	}
	return value, nil
}

func typeByFieldIndex(root reflect.Type, index []int) (reflect.Type, error) {
	current := root
	for _, item := range index {
		if current.Kind() == reflect.Pointer {
			current = current.Elem()
		}
		if current.Kind() != reflect.Struct || item < 0 || item >= current.NumField() {
			return nil, errors.New("invalid field index")
		}
		current = current.Field(item).Type
	}
	return current, nil
}

func setByFieldIndex(root reflect.Value, index []int, value reflect.Value) error {
	current := root
	for i, item := range index {
		for current.Kind() == reflect.Pointer {
			if current.IsNil() {
				if !current.CanSet() {
					return errors.New("field cannot be set")
				}
				current.Set(reflect.New(current.Type().Elem()))
			}
			current = current.Elem()
		}
		if current.Kind() != reflect.Struct || item < 0 || item >= current.NumField() {
			return errors.New("invalid field index")
		}
		field := current.Field(item)
		if i == len(index)-1 {
			if !field.CanSet() || !value.Type().AssignableTo(field.Type()) {
				return errors.New("field cannot be set")
			}
			field.Set(value)
			return nil
		}
		current = field
	}
	return errors.New("empty field index")
}

func isSignedInteger(kind reflect.Kind) bool {
	switch kind {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return true
	default:
		return false
	}
}

func isUnsignedInteger(kind reflect.Kind) bool {
	switch kind {
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return true
	default:
		return false
	}
}

func childPath(parent, child string) string {
	if parent == "$" {
		return "$." + child
	}
	return parent + "." + child
}

func codecName(kind CodecKind) string {
	switch kind {
	case CodecBool:
		return "boolean"
	case CodecString:
		return "string"
	case CodecInt:
		return "integer"
	case CodecUint:
		return "unsigned integer"
	case CodecFloat:
		return "number"
	case CodecDuration:
		return "duration string"
	case CodecStruct, CodecMap:
		return "object"
	case CodecArray, CodecSlice:
		return "array"
	case CodecBytes:
		return "base64 string"
	default:
		return "configured value"
	}
}

func decodeTypeError(path, expected string) error {
	return fmt.Errorf("configstore: expected %s at %s", expected, path)
}

func decodeRangeError(path, expected string) error {
	return fmt.Errorf("configstore: %s out of range at %s", expected, path)
}

func descriptorError(path, problem string) error {
	return fmt.Errorf("configstore: invalid codec descriptor at %s (%s)", path, problem)
}
