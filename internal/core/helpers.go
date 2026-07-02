package core

import (
	"encoding/json"
	"time"

	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/policy"
)

// unixMSToTime converts unix milliseconds to a UTC time; 0 -> zero time.
func unixMSToTime(ms int64) time.Time {
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}

// evaluatePolicies is a thin alias so core reads naturally.
func evaluatePolicies(policies []domain.Policy, operation, path string) bool {
	return policy.Evaluate(policies, operation, path)
}

// encodeMeta serializes audit metadata deterministically; nil -> "{}".
// Values must never contain secret material — callers only pass operation
// names, labels, and version numbers.
func encodeMeta(meta map[string]string) string {
	if len(meta) == 0 {
		return "{}"
	}
	b, err := json.Marshal(meta)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// validateMetadataJSON ensures user-supplied metadata is a JSON object
// (or empty, which normalizes to "{}"). The literal JSON "null" unmarshals
// into a nil map without error, so it is rejected explicitly.
func validateMetadataJSON(s string) (string, error) {
	if s == "" {
		return "{}", nil
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil || m == nil {
		return "", domain.Errorf(domain.ErrInvalidArgument, "metadata must be a JSON object")
	}
	return s, nil
}

const maxValueBytes = 1 << 20 // 1 MiB per parameter/secret value
