package config

import (
	"fmt"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Duration is a time.Duration that unmarshals from a YAML string such as
// "30s" or "24h". A bare integer is interpreted as seconds. This keeps the
// config human-friendly while exposing a real time.Duration to the code.
type Duration time.Duration

// UnmarshalYAML parses a duration from a string ("30s") or an integer number
// of seconds (30). Both spellings go through parseDuration, so the YAML, the
// KMS_* environment variables, and the command-line flags accept exactly the
// same forms.
func (d *Duration) UnmarshalYAML(node *yaml.Node) error {
	// A YAML scalar decodes into a string whatever its resolved tag, so this
	// covers "30s" and a bare 30 alike; a mapping or sequence fails here.
	var s string
	if err := node.Decode(&s); err != nil {
		return fmt.Errorf("duration must be a string like \"30s\" or a number of seconds")
	}
	parsed, err := parseDuration(strings.TrimSpace(s))
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	*d = parsed
	return nil
}

// MarshalYAML renders the duration back to its string form.
func (d Duration) MarshalYAML() (any, error) {
	return time.Duration(d).String(), nil
}
