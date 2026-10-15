package domain

import (
	"mime"
	"strings"
)

// EmptyJSONSecretMessage explains why a zero-byte secret value was rejected.
// Every other content type accepts an empty value.
const EmptyJSONSecretMessage = "an empty value is not valid JSON; use a non-JSON content type to store an empty secret"

// IsJSONMediaType reports whether contentType names JSON: application/json or
// any structured-syntax "+json" suffix type. Parameters such as charset are
// ignored, including malformed ones, so "application/json; charset" still
// counts as JSON.
func IsJSONMediaType(contentType string) bool {
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		bare, _, _ := strings.Cut(contentType, ";")
		mediaType = strings.ToLower(strings.TrimSpace(bare))
	}
	return mediaType == "application/json" || strings.HasSuffix(mediaType, "+json")
}
