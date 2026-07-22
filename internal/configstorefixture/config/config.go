// Package config declares an application-owned configuration root used by the
// managed-store integration fixture. It intentionally combines scalar and
// composite fields, two parameter groups, two secrets, multiple views, and
// both hot and restart reload policies.
package config

import (
	"errors"
	"time"

	"github.com/Suhaibinator/kms/sdk/go/paramstore"
)

//go:generate go run ../../../cmd/kms-config-gen -package . -type Config -binding-package configkms -binding-output ../configkms/config_kms.gen.go -schema-output ../runtime.schema.json -contract-output ../runtime.contract.json

// Endpoint is a nested composite stored in the database parameter group.
type Endpoint struct {
	Host   string              `json:"host"`
	Ports  []uint16            `json:"ports"`
	Labels map[string][]string `json:"labels"`
	Zones  [2]string           `json:"zones"`
}

// Config is the application-owned root consumed by kms-config-gen.
type Config struct {
	Endpoint     Endpoint            `json:"endpoint" kms:"group=database,reload=restart" kms_views:"persistence_handler,database_health"`
	Timeout      time.Duration       `json:"timeout" kms:"group=database,reload=hot" kms_views:"persistence_handler,database_health"`
	MaxOpen      *int                `json:"max_open" kms:"group=database,reload=restart" kms_views:"persistence_handler,database_health"`
	Features     []string            `json:"features" kms:"group=runtime,reload=hot" kms_views:"api_handler,background_jobs"`
	Payload      []byte              `json:"payload" kms:"group=runtime,reload=hot" kms_views:"api_handler"`
	Thresholds   map[string]uint64   `json:"thresholds" kms:"group=runtime,reload=hot" kms_views:"api_handler,background_jobs"`
	Window       [2]float64          `json:"window" kms:"group=runtime,reload=hot" kms_views:"background_jobs"`
	Password     paramstore.Secret   `json:"-" kms:"secret=database_password,reload=restart" kms_views:"persistence_handler"`
	RuntimeToken paramstore.Secret   `json:"-" kms:"secret=runtime_token,reload=hot" kms_views:"api_handler,background_jobs"`
	Local        map[string][]string `kms:"-"`
}

// Defaults returns application defaults. Secret defaults must remain zero;
// generated startup validation injects the candidate's secrets into a
// temporary defaults clone before calling Validate.
func Defaults() *Config {
	maxOpen := 20
	return &Config{
		Endpoint: Endpoint{
			Host:  "db.internal",
			Ports: []uint16{5432, 5433},
			Labels: map[string][]string{
				"role": {"primary", "readonly"},
			},
			Zones: [2]string{"us-west-1a", "us-west-1b"},
		},
		Timeout:    3 * time.Second,
		MaxOpen:    &maxOpen,
		Features:   []string{"search", "reports"},
		Payload:    []byte("fixture-payload"),
		Thresholds: map[string]uint64{"burst": 100, "steady": 25},
		Window:     [2]float64{0.25, 0.75},
		Local:      map[string][]string{"owners": {"platform", "runtime"}},
	}
}

// Validate is deliberately strict about both parameter and secret fields so
// tests exercise validation only after complete candidate assembly.
func (c *Config) Validate() error {
	if c == nil {
		return errors.New("configuration is nil")
	}
	if c.Endpoint.Host == "" || len(c.Endpoint.Ports) == 0 {
		return errors.New("database endpoint is incomplete")
	}
	if c.Timeout <= 0 {
		return errors.New("timeout must be positive")
	}
	if c.MaxOpen != nil && *c.MaxOpen <= 0 {
		return errors.New("max_open must be positive")
	}
	// Collection presence is intentionally unconstrained in this fixture so
	// integration tests can exercise the distinct valid nil and non-nil-empty
	// encodings through admission, drift reporting, and restoration.
	if c.Password.IsZero() || c.RuntimeToken.IsZero() {
		return errors.New("required secrets are unavailable")
	}
	return nil
}
