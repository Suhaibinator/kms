// Package config declares the application-owned configuration contract for
// the minimal managed-configuration example.
package config

import (
	"errors"
	"strings"

	"github.com/Suhaibinator/kms/sdk/go/kmsclient"
)

//go:generate go run ../../../cmd/kms-config-gen -package . -type Config -binding-package configkms -binding-output ../configkms/config_kms.gen.go -schema-output ../runtime.schema.json -contract-output ../runtime.contract.json

const (
	defaultListenAddress = "127.0.0.1:8080"
	defaultGreeting      = "hello from application defaults"
	defaultRequestLimit  = 100
)

// Config is decoded and published as one immutable release generation.
type Config struct {
	ListenAddress string           `json:"listen_address" kms:"group=server,reload=restart" kms_views:"http_server"`
	Greeting      string           `json:"greeting" kms:"group=runtime,reload=hot" kms_views:"request_handler"`
	RequestLimit  int              `json:"request_limit" kms:"group=runtime,reload=hot" kms_views:"request_handler"`
	APIKey        kmsclient.Secret `json:"-" kms:"secret=api_key,reload=hot" kms_views:"request_handler"`
}

// Defaults returns the values owned by application source. Runtime values
// that differ from these defaults are reported as emergency overrides.
func Defaults() *Config {
	return &Config{
		ListenAddress: defaultListenAddress,
		Greeting:      defaultGreeting,
		RequestLimit:  defaultRequestLimit,
	}
}

// Validate rejects an incomplete or unsafe candidate before publication.
func (c *Config) Validate() error {
	if c == nil {
		return errors.New("configuration is nil")
	}
	if strings.TrimSpace(c.ListenAddress) == "" {
		return errors.New("listen address is required")
	}
	if strings.TrimSpace(c.Greeting) == "" {
		return errors.New("greeting is required")
	}
	if c.RequestLimit < 1 || c.RequestLimit > 10_000 {
		return errors.New("request limit must be between 1 and 10000")
	}
	if c.APIKey.IsZero() {
		return errors.New("API key is required")
	}
	return nil
}
