// Package documented is a generator fixture whose root type carries doc
// comments and a literal defaults function.
package documented

import (
	"time"

	"github.com/Suhaibinator/kms/sdk/go/kmsclient"
)

// Retry controls how a failed call is retried.
type Retry struct {
	// Attempts is the number of tries before giving up.
	Attempts int           `json:"attempts"`
	Backoff  time.Duration `json:"backoff"` // Backoff is the initial delay between tries.
}

// Config is the documented fixture root.
type Config struct {
	// ListenAddress is the host:port the server binds.
	ListenAddress string `json:"listen_address" kms:"group=server,reload=restart" kms_views:"http"`
	// Greeting is shown on the landing page.
	Greeting string `json:"greeting" kms:"group=runtime,reload=hot" kms_views:"http"`
	// RequestLimit caps requests per minute per client.
	RequestLimit int `json:"request_limit" kms:"group=runtime,reload=hot" kms_views:"http"`
	// Retry tunes outbound retries.
	Retry Retry `json:"retry" kms:"group=runtime,reload=hot" kms_views:"http"`
	// MaxIdle is optional; nil means unlimited.
	MaxIdle *int `json:"max_idle" kms:"group=runtime,reload=hot" kms_views:"http"`
	// Tags label the deployment.
	Tags []string `json:"tags" kms:"group=runtime,reload=hot" kms_views:"http"`
	// Verbose enables debug logging.
	Verbose *bool `json:"verbose" kms:"group=runtime,reload=hot" kms_views:"http"`
	// Burst is a pointer to a zero value by default.
	Burst *int `json:"burst" kms:"group=runtime,reload=hot" kms_views:"http"`
	// Fallback is built by a helper that returns a literal.
	Fallback Retry `json:"fallback" kms:"group=runtime,reload=hot" kms_views:"http"`
	// Computed is built by a helper the generator cannot evaluate.
	Computed Retry            `json:"computed" kms:"group=runtime,reload=hot" kms_views:"http"`
	APIKey   kmsclient.Secret `json:"-" kms:"secret=api_key,reload=hot" kms_views:"http"`
}

func defaultFallback() Retry {
	return Retry{Attempts: 5, Backoff: time.Second}
}

func computedRetry() Retry {
	attempts := 2
	return Retry{Attempts: attempts, Backoff: time.Second}
}

const defaultRequestLimit = 100

// Defaults returns application-owned defaults.
func Defaults() *Config {
	maxIdle := 8
	return &Config{
		ListenAddress: "127.0.0.1:8080",
		Greeting:      "hello",
		RequestLimit:  defaultRequestLimit,
		Retry:         Retry{Attempts: 3, Backoff: 250 * time.Millisecond},
		MaxIdle:       &maxIdle,
		Tags:          []string{"blue", "canary"},
		Verbose:       new(true),
		Burst:         new(int),
		Fallback:      defaultFallback(),
		Computed:      computedRetry(),
	}
}

// Validate accepts every candidate; the fixture only exercises generation.
func (*Config) Validate() error { return nil }
