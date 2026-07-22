package composed

import (
	"fmt"

	"github.com/Suhaibinator/kms/internal/configgen/testdata/commonfragment"
)

type Config struct {
	Common  *commonfragment.Config `kms:"inline"`
	AppName string                 `json:"app_name" kms:"group=runtime,reload=restart" kms_views:"worker"`
}

func (c *Config) Validate() error {
	if c == nil || c.Common == nil {
		return fmt.Errorf("common config is required")
	}
	return c.Common.Validate()
}

type EmbeddedConfig struct {
	commonfragment.Config `kms:"inline"`
	AppName               string `json:"app_name" kms:"group=runtime,reload=restart" kms_views:"app"`
}

func (c *EmbeddedConfig) Validate() error { return c.Config.Validate() }
