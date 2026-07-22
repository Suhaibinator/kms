package core

import (
	"encoding/base64"
	"fmt"
	"time"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

const (
	goDurationFormat = "go-duration"
	kmsBase64Format  = "kms-base64"
)

// configureKMSFormats enables the two asserted formats emitted by
// kms-config-gen. Format assertions remain annotation-only for unrelated
// schemas, preserving the existing Draft 2020-12 behavior.
func configureKMSFormats(compiler *jsonschema.Compiler, schema any) {
	if !usesKMSFormat(schema) {
		return
	}
	// AssertFormat is compiler-wide in jsonschema/v6. Remove all unrelated
	// format annotations from this ephemeral decoded schema before enabling it,
	// otherwise the presence of one generated KMS format would unexpectedly
	// turn e.g. "email" into an assertion too. The immutable schema text stored
	// by KMS is not modified.
	removeUnassertedFormats(schema)
	compiler.RegisterFormat(&jsonschema.Format{
		Name: goDurationFormat,
		Validate: func(value any) error {
			text, ok := value.(string)
			if !ok {
				return nil
			}
			if _, err := time.ParseDuration(text); err != nil {
				return fmt.Errorf("not a Go duration")
			}
			return nil
		},
	})
	compiler.RegisterFormat(&jsonschema.Format{
		Name: kmsBase64Format,
		Validate: func(value any) error {
			text, ok := value.(string)
			if !ok {
				return nil
			}
			decoded, err := base64.StdEncoding.Strict().DecodeString(text)
			if err != nil || base64.StdEncoding.EncodeToString(decoded) != text {
				return fmt.Errorf("not canonical base64")
			}
			return nil
		},
	})
	compiler.AssertFormat()
}

func removeUnassertedFormats(value any) {
	object, ok := value.(map[string]any)
	if !ok {
		return
	}
	if format, ok := object["format"].(string); ok && format != goDurationFormat && format != kmsBase64Format {
		delete(object, "format")
	}
	walkSubschemas(object, removeUnassertedFormats)
}

func usesKMSFormat(value any) bool {
	object, ok := value.(map[string]any)
	if !ok {
		return false
	}
	if format, ok := object["format"].(string); ok && (format == goDurationFormat || format == kmsBase64Format) {
		return true
	}
	found := false
	walkSubschemas(object, func(child any) {
		if !found && usesKMSFormat(child) {
			found = true
		}
	})
	return found
}

// walkSubschemas follows only JSON Schema applicator locations. Instance-valued
// keywords such as const, enum, default, and examples may themselves contain a
// property named "format" and must never be rewritten.
func walkSubschemas(schema map[string]any, visit func(any)) {
	for _, keyword := range []string{
		"not", "if", "then", "else", "items", "contains", "additionalProperties",
		"unevaluatedProperties", "propertyNames", "unevaluatedItems", "contentSchema",
	} {
		if child, exists := schema[keyword]; exists {
			visit(child)
		}
	}
	for _, keyword := range []string{"allOf", "anyOf", "oneOf", "prefixItems"} {
		children, _ := schema[keyword].([]any)
		for _, child := range children {
			visit(child)
		}
	}
	for _, keyword := range []string{"$defs", "definitions", "properties", "patternProperties", "dependentSchemas"} {
		children, _ := schema[keyword].(map[string]any)
		for _, child := range children {
			visit(child)
		}
	}
}
