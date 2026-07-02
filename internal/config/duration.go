package config

import (
	"fmt"
	"time"

	"gopkg.in/yaml.v3"
)

// Duration is a time.Duration that unmarshals from a YAML string such as
// "30s" or "24h". A bare integer is interpreted as seconds. This keeps the
// config human-friendly while exposing a real time.Duration to the code.
type Duration time.Duration

// UnmarshalYAML parses a duration from a string ("30s") or an integer number
// of seconds (30).
func (d *Duration) UnmarshalYAML(node *yaml.Node) error {
	var s string
	if err := node.Decode(&s); err == nil {
		parsed, perr := time.ParseDuration(s)
		if perr != nil {
			return fmt.Errorf("invalid duration %q: %w", s, perr)
		}
		*d = Duration(parsed)
		return nil
	}
	var secs int64
	if err := node.Decode(&secs); err != nil {
		return fmt.Errorf("duration must be a string like \"30s\" or a number of seconds")
	}
	*d = Duration(time.Duration(secs) * time.Second)
	return nil
}

// MarshalYAML renders the duration back to its string form.
func (d Duration) MarshalYAML() (any, error) {
	return time.Duration(d).String(), nil
}
