package configstore

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"reflect"
	"time"
)

// EncodeGroup encodes one complete parameter group from src using the same
// generated descriptor consumed by DecodeGroup. Src must be a non-nil pointer
// to a struct. The returned document contains every described field exactly
// once and preserves nil versus non-nil empty slices, maps, and byte slices.
func EncodeGroup(src any, fields []FieldCodec) (json.RawMessage, error) {
	value := reflect.ValueOf(src)
	if !value.IsValid() || value.Kind() != reflect.Pointer || value.IsNil() || value.Elem().Kind() != reflect.Struct {
		return nil, fmt.Errorf("configstore: EncodeGroup source must be a non-nil pointer to a struct")
	}

	encoded, err := encodeObject(value.Elem(), fields, "$")
	if err != nil {
		return nil, err
	}
	document, err := json.Marshal(encoded)
	if err != nil {
		return nil, fmt.Errorf("configstore: encode JSON group document: %w", err)
	}
	return json.RawMessage(document), nil
}

func encodeObject(source reflect.Value, fields []FieldCodec, path string) (map[string]any, error) {
	if source.Kind() != reflect.Struct {
		return nil, descriptorError(path, "source is not a struct")
	}

	result := make(map[string]any, len(fields))
	for _, field := range fields {
		if field.JSONName == "" || len(field.FieldIndex) == 0 {
			return nil, descriptorError(path, "field descriptor is incomplete")
		}
		if _, exists := result[field.JSONName]; exists {
			return nil, descriptorError(path, "field descriptor is duplicated")
		}
		value, err := valueByFieldIndex(source, field.FieldIndex)
		if err != nil {
			return nil, descriptorError(path, "field index is invalid")
		}
		encoded, err := encodeValue(value, field.Value, childPath(path, field.JSONName))
		if err != nil {
			return nil, err
		}
		result[field.JSONName] = encoded
	}
	return result, nil
}

func encodeValue(source reflect.Value, codec ValueCodec, path string) (any, error) {
	if codec.Kind == CodecPointer {
		if source.Kind() != reflect.Pointer || codec.Element == nil {
			return nil, descriptorError(path, "pointer descriptor does not match source")
		}
		if source.IsNil() {
			return nil, nil
		}
		return encodeValue(source.Elem(), *codec.Element, path)
	}

	if codec.Kind == CodecSlice || codec.Kind == CodecMap || codec.Kind == CodecBytes {
		switch codec.Kind {
		case CodecSlice:
			if source.Kind() != reflect.Slice || codec.Element == nil {
				return nil, descriptorError(path, "slice descriptor does not match source")
			}
		case CodecMap:
			if source.Kind() != reflect.Map || source.Type().Key().Kind() != reflect.String || codec.Element == nil {
				return nil, descriptorError(path, "map descriptor does not match source")
			}
		case CodecBytes:
			if source.Kind() != reflect.Slice || source.Type().Elem().Kind() != reflect.Uint8 {
				return nil, descriptorError(path, "byte descriptor does not match source")
			}
		}
		if source.IsNil() {
			return nil, nil
		}
	}

	switch codec.Kind {
	case CodecBool:
		if source.Kind() != reflect.Bool {
			return nil, descriptorError(path, "boolean descriptor does not match source")
		}
		return source.Bool(), nil

	case CodecString:
		if source.Kind() != reflect.String {
			return nil, descriptorError(path, "string descriptor does not match source")
		}
		return source.String(), nil

	case CodecInt:
		if !isSignedInteger(source.Kind()) {
			return nil, descriptorError(path, "integer descriptor does not match source")
		}
		bits, err := numericCodecBits(codec, source, path)
		if err != nil {
			return nil, err
		}
		value := source.Int()
		if bits < 64 {
			minimum := -(int64(1) << (bits - 1))
			maximum := (int64(1) << (bits - 1)) - 1
			if value < minimum || value > maximum {
				return nil, decodeRangeError(path, "integer")
			}
		}
		return value, nil

	case CodecUint:
		if !isUnsignedInteger(source.Kind()) {
			return nil, descriptorError(path, "unsigned integer descriptor does not match source")
		}
		bits, err := numericCodecBits(codec, source, path)
		if err != nil {
			return nil, err
		}
		value := source.Uint()
		if bits < 64 && value > (uint64(1)<<bits)-1 {
			return nil, decodeRangeError(path, "unsigned integer")
		}
		return value, nil

	case CodecFloat:
		if source.Kind() != reflect.Float32 && source.Kind() != reflect.Float64 {
			return nil, descriptorError(path, "number descriptor does not match source")
		}
		bits, err := numericCodecBits(codec, source, path)
		if err != nil {
			return nil, err
		}
		value := source.Float()
		if bits == 32 {
			return float32(value), nil
		}
		return value, nil

	case CodecDuration:
		if source.Kind() != reflect.Int64 {
			return nil, descriptorError(path, "duration descriptor does not match source")
		}
		return time.Duration(source.Int()).String(), nil

	case CodecStruct:
		if source.Kind() != reflect.Struct {
			return nil, descriptorError(path, "struct descriptor does not match source")
		}
		return encodeObject(source, codec.Fields, path)

	case CodecArray:
		if source.Kind() != reflect.Array || codec.Element == nil {
			return nil, descriptorError(path, "array descriptor does not match source")
		}
		items := make([]any, source.Len())
		for i := range items {
			encoded, err := encodeValue(source.Index(i), *codec.Element, path+"[]")
			if err != nil {
				return nil, err
			}
			items[i] = encoded
		}
		return items, nil

	case CodecSlice:
		items := make([]any, source.Len())
		for i := range items {
			encoded, err := encodeValue(source.Index(i), *codec.Element, path+"[]")
			if err != nil {
				return nil, err
			}
			items[i] = encoded
		}
		return items, nil

	case CodecMap:
		items := make(map[string]any, source.Len())
		iterator := source.MapRange()
		for iterator.Next() {
			key := iterator.Key().String()
			encoded, err := encodeValue(iterator.Value(), *codec.Element, path+"[*]")
			if err != nil {
				return nil, err
			}
			items[key] = encoded
		}
		return items, nil

	case CodecBytes:
		return base64.StdEncoding.EncodeToString(source.Bytes()), nil

	default:
		return nil, descriptorError(path, "codec kind is invalid")
	}
}

func valueByFieldIndex(root reflect.Value, index []int) (reflect.Value, error) {
	current := root
	for _, item := range index {
		for current.Kind() == reflect.Pointer {
			if current.IsNil() {
				return reflect.Value{}, fmt.Errorf("nil pointer")
			}
			current = current.Elem()
		}
		if current.Kind() != reflect.Struct || item < 0 || item >= current.NumField() {
			return reflect.Value{}, fmt.Errorf("invalid field index")
		}
		current = current.Field(item)
	}
	return current, nil
}
