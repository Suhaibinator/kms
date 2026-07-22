package valid

import (
	"fmt"
	"time"

	"github.com/Suhaibinator/kms/sdk/go/paramstore"
)

type RetryLimit int16

type Endpoint struct {
	Host   string              `json:"host"`
	Ports  []uint16            `json:"ports"`
	Labels map[string][]string `json:"labels"`
}

type Config struct {
	Endpoint Endpoint          `json:"endpoint" kms:"group=database,reload=restart" kms_views:"persistence_handler,database_health"`
	Timeout  time.Duration     `json:"timeout" kms:"group=database,reload=hot" kms_views:"persistence_handler,database_health"`
	Limit    RetryLimit        `json:"limit" kms:"group=rate_limits,reload=hot" kms_views:"api_handler"`
	Ratio    *float32          `json:"ratio" kms:"group=rate_limits,reload=hot" kms_views:"api_handler"`
	Payload  []byte            `json:"payload" kms:"group=rate_limits,reload=hot" kms_views:"api_handler"`
	Password paramstore.Secret `json:"-" kms:"secret=database_password,reload=restart" kms_views:"persistence_handler"`
	Local    map[string]string `kms:"-"`
}

func (c *Config) Validate() error {
	if c.Timeout < 0 {
		return fmt.Errorf("timeout must not be negative")
	}
	return nil
}

type SecretsOnly struct {
	Token paramstore.Secret `json:"-" kms:"secret=token,reload=hot" kms_views:"worker"`
}

func (*SecretsOnly) Validate() error { return nil }
