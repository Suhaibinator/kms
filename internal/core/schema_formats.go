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
func configureKMSFormats(compiler *jsonschema.Compiler, schema map[string]any) {
	if !usesKMSFormat(schema) {
		return
	}
	// AssertFormat is compiler-wide in jsonschema/v6. Remove all unrelated
	// format annotations from this ephemeral decoded schema before enabling it,
	// otherwise the presence of one generated KMS format would unexpectedly
	// turn e.g. "email" into an assertion too. The immutable schema text stored
	// by KMS is not modified.
	removeUnassertedFormats(schema)
	compiler.RegisterFormat(stringFormat(goDurationFormat, func(text string) error {
		if _, err := time.ParseDuration(text); err != nil {
			return fmt.Errorf("not a Go duration")
		}
		return nil
	}))
	compiler.RegisterFormat(stringFormat(kmsBase64Format, func(text string) error {
		decoded, err := base64.StdEncoding.Strict().DecodeString(text)
		if err != nil || base64.StdEncoding.EncodeToString(decoded) != text {
			return fmt.Errorf("not canonical base64")
		}
		return nil
	}))
	compiler.AssertFormat()
}

// stringFormat adapts a string check to jsonschema's untyped Validate hook.
// Per the spec, formats ignore non-string instances.
func stringFormat(name string, validate func(string) error) *jsonschema.Format {
	return &jsonschema.Format{
		Name: name,
		Validate: func(value any) error {
			text, ok := value.(string)
			if !ok {
				return nil
			}
			return validate(text)
		},
	}
}

func removeUnassertedFormats(schema map[string]any) {
	if format, ok := schema["format"].(string); ok && format != goDurationFormat && format != kmsBase64Format {
		delete(schema, "format")
	}
	walkSubschemas(schema, removeUnassertedFormats)
}

func usesKMSFormat(schema map[string]any) bool {
	if format, ok := schema["format"].(string); ok && (format == goDurationFormat || format == kmsBase64Format) {
		return true
	}
	found := false
	walkSubschemas(schema, func(child map[string]any) {
		if !found && usesKMSFormat(child) {
			found = true
		}
	})
	return found
}

// walkSubschemas follows only JSON Schema applicator locations. Instance-valued
// keywords such as const, enum, default, and examples may themselves contain a
// property named "format" and must never be rewritten. Boolean subschemas carry
// no keywords and are skipped.
func walkSubschemas(schema map[string]any, visit func(map[string]any)) {
	visitObject := func(child any) {
		if object, ok := child.(map[string]any); ok {
			visit(object)
		}
	}
	for _, keyword := range []string{
		"not", "if", "then", "else", "items", "contains", "additionalProperties",
		"unevaluatedProperties", "propertyNames", "unevaluatedItems", "contentSchema",
	} {
		visitObject(schema[keyword])
	}
	for _, keyword := range []string{"allOf", "anyOf", "oneOf", "prefixItems"} {
		children, _ := schema[keyword].([]any)
		for _, child := range children {
			visitObject(child)
		}
	}
	for _, keyword := range []string{"$defs", "definitions", "properties", "patternProperties", "dependentSchemas"} {
		children, _ := schema[keyword].(map[string]any)
		for _, child := range children {
			visitObject(child)
		}
	}
}
