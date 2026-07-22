package config

import (
	"testing"

	"github.com/Suhaibinator/kms/sdk/go/kmsclient"
)

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		config  *Config
		wantErr bool
	}{
		{name: "complete", config: withAPIKey(Defaults())},
		{name: "nil", config: nil, wantErr: true},
		{name: "missing listen address", config: withAPIKey(&Config{Greeting: "hello", RequestLimit: 1}), wantErr: true},
		{name: "missing greeting", config: withAPIKey(&Config{ListenAddress: ":8080", RequestLimit: 1}), wantErr: true},
		{name: "zero request limit", config: withAPIKey(&Config{ListenAddress: ":8080", Greeting: "hello"}), wantErr: true},
		{name: "oversized request limit", config: withAPIKey(&Config{ListenAddress: ":8080", Greeting: "hello", RequestLimit: 10_001}), wantErr: true},
		{name: "missing API key", config: Defaults(), wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.config.Validate()
			if (err != nil) != test.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %t", err, test.wantErr)
			}
		})
	}
}

func withAPIKey(config *Config) *Config {
	config.APIKey = kmsclient.NewSecret([]byte("test-only"))
	return config
}
