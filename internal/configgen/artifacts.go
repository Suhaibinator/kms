package configgen

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"go/token"
	"math"
)

const maxGeneratedSchemaBytes = 256 << 10

type schemaDocument struct {
	Schema               string                    `json:"$schema"`
	Description          string                    `json:"description,omitempty"`
	Type                 string                    `json:"type"`
	AdditionalProperties bool                      `json:"additionalProperties"`
	Required             []string                  `json:"required"`
	Properties           map[string]map[string]any `json:"properties"`
}

func renderSchema(model *ir) ([]byte, error) {
	doc := schemaDocument{
		Schema:               "https://json-schema.org/draft/2020-12/schema",
		Description:          model.Annotations.rootDoc,
		Type:                 "object",
		AdditionalProperties: false,
		Required:             make([]string, 0, len(model.Groups)),
		Properties:           make(map[string]map[string]any, len(model.Groups)),
	}
	for _, group := range model.Groups {
		required := make([]string, 0, len(group.Fields))
		properties := make(map[string]any, len(group.Fields))
		groupDefault := make(map[string]any, len(group.Fields))
		groupComplete := true
		for _, field := range group.Fields {
			required = append(required, field.JSONName)
			raw := managedFieldDefault(model.Annotations.defaults, field.GoPath)
			property := annotatedSchema(field.Type, model.Annotations.docs[field.Position], raw, model.Annotations.docs)
			properties[field.JSONName] = property
			if value, known := property["default"]; known {
				groupDefault[field.JSONName] = value
			} else {
				groupComplete = false
			}
		}
		doc.Required = append(doc.Required, group.Alias)
		schema := map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"required":             required,
			"properties":           properties,
		}
		if groupComplete && model.Annotations.defaults != nil {
			schema["default"] = groupDefault
		}
		doc.Properties[group.Alias] = schema
	}
	data, err := marshalArtifact(doc, "schema")
	if err == nil && len(data) > maxGeneratedSchemaBytes {
		return nil, fmt.Errorf("configgen: generated schema is %d bytes; maximum is %d", len(data), maxGeneratedSchemaBytes)
	}
	return data, err
}

// annotatedSchema renders a property schema and adds the source-level
// description and the evaluated default when they are known. Nested struct
// properties are annotated the same way so a form can show help and defaults
// per field, not just per group.
func annotatedSchema(value *typeIR, description string, raw any, docs map[token.Pos]string) map[string]any {
	schema := schemaForType(value)
	inner := schema
	if nullable, ok := schema["anyOf"].([]any); ok && len(nullable) == 2 {
		if first, ok := nullable[0].(map[string]any); ok {
			inner = first
		}
	}
	if value.Kind == typePointer {
		annotateStructProperties(value.Elem, inner, raw, docs)
	} else {
		annotateStructProperties(value, inner, raw, docs)
	}
	if description != "" {
		schema["description"] = description
	}
	if converted, known := schemaDefault(value, raw); known {
		schema["default"] = converted
	}
	return schema
}

func annotateStructProperties(value *typeIR, schema map[string]any, raw any, docs map[token.Pos]string) {
	if value.Kind != typeStruct {
		return
	}
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		return
	}
	tree, _ := raw.(map[string]any)
	for _, field := range value.Fields {
		if !field.Included {
			continue
		}
		var child any
		switch raw.(type) {
		case unknownDefault:
			child = unknownDefault{}
		case nil, nilDefault:
			child = nil
		default:
			if tree == nil {
				child = unknownDefault{}
			} else {
				child = tree[field.GoName]
			}
		}
		properties[field.JSONName] = annotatedSchema(field.Type, docs[field.Position], child, docs)
	}
}

func schemaForType(value *typeIR) map[string]any {
	switch value.Kind {
	case typeBool:
		return map[string]any{"type": "boolean"}
	case typeString:
		return map[string]any{"type": "string"}
	case typeInt:
		minimum, maximum := signedBounds(value.Bits)
		return map[string]any{"type": "integer", "minimum": minimum, "maximum": maximum}
	case typeUint:
		return map[string]any{"type": "integer", "minimum": uint64(0), "maximum": unsignedMaximum(value.Bits)}
	case typeFloat:
		maximum := math.MaxFloat64
		if value.Bits == 32 {
			maximum = math.MaxFloat32
		}
		return map[string]any{"type": "number", "minimum": -maximum, "maximum": maximum}
	case typeDuration:
		return map[string]any{"type": "string", "format": "go-duration"}
	case typePointer:
		return nullableSchema(schemaForType(value.Elem))
	case typeBytes:
		return nullableSchema(map[string]any{"type": "string", "format": "kms-base64"})
	case typeArray:
		return map[string]any{"type": "array", "items": schemaForType(value.Elem), "minItems": value.Len, "maxItems": value.Len}
	case typeSlice:
		return nullableSchema(map[string]any{"type": "array", "items": schemaForType(value.Elem)})
	case typeMap:
		return nullableSchema(map[string]any{"type": "object", "additionalProperties": schemaForType(value.Elem)})
	case typeStruct:
		required := make([]string, 0, len(value.Fields))
		properties := make(map[string]any)
		for _, field := range value.Fields {
			if !field.Included {
				continue
			}
			required = append(required, field.JSONName)
			properties[field.JSONName] = schemaForType(field.Type)
		}
		return map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"required":             required,
			"properties":           properties,
		}
	default:
		panic("configgen: invalid normalized type")
	}
}

func nullableSchema(value map[string]any) map[string]any {
	return map[string]any{"anyOf": []any{value, map[string]any{"type": "null"}}}
}

func signedBounds(bits int) (int64, int64) {
	if bits >= 64 {
		return math.MinInt64, math.MaxInt64
	}
	maximum := int64(1)<<(bits-1) - 1
	return -maximum - 1, maximum
}

func unsignedMaximum(bits int) uint64 {
	if bits >= 64 {
		return math.MaxUint64
	}
	return uint64(1)<<bits - 1
}

type contractDocument struct {
	Format       string           `json:"format"`
	Source       contractSource   `json:"source"`
	SchemaSHA256 string           `json:"schema_sha256"`
	Groups       []contractGroup  `json:"groups"`
	Fields       []contractField  `json:"fields"`
	Secrets      []contractSecret `json:"secrets"`
	Views        []contractView   `json:"views"`
}

type contractSource struct {
	Package string `json:"package"`
	Type    string `json:"type"`
}

type contractGroup struct {
	Alias       string   `json:"alias"`
	Kind        string   `json:"kind"`
	ContentType string   `json:"content_type"`
	Fields      []string `json:"fields"`
}

type contractField struct {
	Group    string   `json:"group"`
	JSONName string   `json:"json_name"`
	GoName   string   `json:"go_name"`
	GoPath   string   `json:"go_path"`
	Reload   string   `json:"reload"`
	Encoding string   `json:"encoding"`
	Views    []string `json:"views"`
}

type contractSecret struct {
	Alias    string   `json:"alias"`
	Kind     string   `json:"kind"`
	GoName   string   `json:"go_name"`
	GoPath   string   `json:"go_path"`
	Reload   string   `json:"reload"`
	Encoding string   `json:"encoding"`
	Views    []string `json:"views"`
}

type contractView struct {
	Name   string   `json:"name"`
	Method string   `json:"method"`
	Fields []string `json:"fields"`
}

type renderedContract struct {
	SchemaSHA256 string
	Entries      []contractEntry
}

type contractEntry struct {
	Alias       string
	Kind        string
	ContentType string
}

func renderContract(model *ir, schema []byte) ([]byte, renderedContract, error) {
	hash := sha256.Sum256(schema)
	hashText := hex.EncodeToString(hash[:])
	doc := contractDocument{
		Format:       "kms-config-contract/v1",
		Source:       contractSource{Package: model.PackagePath, Type: model.TypeName},
		SchemaSHA256: hashText,
		Groups:       make([]contractGroup, 0, len(model.Groups)),
		Fields:       make([]contractField, 0, len(model.Fields)-len(model.Secrets)),
		Secrets:      make([]contractSecret, 0, len(model.Secrets)),
		Views:        make([]contractView, 0, len(model.Views)),
	}
	rendered := renderedContract{SchemaSHA256: hashText}
	for _, group := range model.Groups {
		contractGroup := contractGroup{Alias: group.Alias, Kind: "parameter", ContentType: "json"}
		for _, field := range group.Fields {
			contractGroup.Fields = append(contractGroup.Fields, field.JSONName)
			doc.Fields = append(doc.Fields, contractField{
				Group: group.Alias, JSONName: field.JSONName, GoName: field.GoName, GoPath: field.GoPath,
				Reload: field.Reload, Encoding: canonicalEncoding(field.Type), Views: append([]string(nil), field.Views...),
			})
		}
		doc.Groups = append(doc.Groups, contractGroup)
		rendered.Entries = append(rendered.Entries, contractEntry{Alias: group.Alias, Kind: "parameter", ContentType: "json"})
	}
	for _, field := range model.Secrets {
		doc.Secrets = append(doc.Secrets, contractSecret{
			Alias: field.Source, Kind: "secret", GoName: field.GoName, GoPath: field.GoPath, Reload: field.Reload,
			Encoding: "secret", Views: append([]string(nil), field.Views...),
		})
		rendered.Entries = append(rendered.Entries, contractEntry{Alias: field.Source, Kind: "secret"})
	}
	for _, view := range model.Views {
		contractView := contractView{Name: view.Name, Method: view.Method}
		for _, field := range view.Fields {
			contractView.Fields = append(contractView.Fields, field.canonicalName())
		}
		doc.Views = append(doc.Views, contractView)
	}
	data, err := marshalArtifact(doc, "contract")
	return data, rendered, err
}

func canonicalEncoding(value *typeIR) string {
	switch value.Kind {
	case typePointer:
		return "nullable-" + canonicalEncoding(value.Elem)
	case typeBytes, typeSlice, typeMap:
		return "nullable-" + value.Encoding
	default:
		return value.Encoding
	}
}

func marshalArtifact(value any, name string) ([]byte, error) {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("configgen: marshal %s: %w", name, err)
	}
	return append(data, '\n'), nil
}
