package mutating

import (
	"errors"
	"strings"

	"github.com/Suhaibinator/kms/sdk/go/paramstore"
)

// Config is a generation fixture whose validation canonicalizes a field in
// place. The generated store must compare the two validated copies, not the
// original unvalidated defaults retained by the Store.
type Config struct {
	Name  string            `json:"name" kms:"group=runtime,reload=hot" kms_views:"worker"`
	Token paramstore.Secret `json:"-" kms:"secret=token,reload=hot" kms_views:"worker"`
}

func Defaults() *Config {
	return &Config{Name: "  canonical name  "}
}

func (c *Config) Validate() error {
	if c.Token.IsZero() {
		return errors.New("token is required")
	}
	if strings.TrimSpace(c.Name) == "mutate secret" {
		plaintext := c.Token.Value()
		if string(plaintext) != "secret-value" {
			return errors.New("validator did not receive the raw secret")
		}
		// Deliberately canonicalize the owned secret buffer in place. Candidate
		// and effective-default validation must each receive the same raw bytes.
		plaintext[0] = 'S'
	}
	c.Name = strings.TrimSpace(c.Name)
	return nil
}
