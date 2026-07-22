package commonfragment

import (
	"fmt"

	"github.com/Suhaibinator/kms/sdk/go/kmsclient"
)

type Limits struct {
	Burst int `json:"burst" kms:"group=runtime,reload=hot" kms_views:"worker"`
}

type Config struct {
	Endpoint string           `json:"endpoint" kms:"group=database,reload=restart" kms_views:"database"`
	Token    kmsclient.Secret `json:"-" kms:"secret=common_token,reload=restart" kms_views:"worker"`
	Limits   *Limits          `kms:"inline"`
}

func (c *Config) Validate() error {
	if c == nil || c.Limits == nil {
		return fmt.Errorf("common limits are required")
	}
	return nil
}
