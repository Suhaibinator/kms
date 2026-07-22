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
	switch value := value.(type) {
	case map[string]any:
		if format, ok := value["format"].(string); ok && format != goDurationFormat && format != kmsBase64Format {
			delete(value, "format")
		}
		for _, child := range value {
			removeUnassertedFormats(child)
		}
	case []any:
		for _, child := range value {
			removeUnassertedFormats(child)
		}
	}
}

func usesKMSFormat(value any) bool {
	switch value := value.(type) {
	case map[string]any:
		if format, ok := value["format"].(string); ok && (format == goDurationFormat || format == kmsBase64Format) {
			return true
		}
		for _, child := range value {
			if usesKMSFormat(child) {
				return true
			}
		}
	case []any:
		for _, child := range value {
			if usesKMSFormat(child) {
				return true
			}
		}
	}
	return false
}
