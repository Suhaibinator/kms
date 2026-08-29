package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"
)

func TestRunDemonstratesManagedConfigurationLifecycle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var output bytes.Buffer
	if err := run(ctx, &output); err != nil {
		t.Fatal(err)
	}

	transcript := output.String()
	for _, milestone := range []string{
		"applied: phase=startup release=dev/managed-config-example/runtime@1#1 divergent=false changed=(none)",
		"initial typed snapshot: release=1",
		"api_key=[REDACTED] api_key_version=1",
		"applied: phase=runtime release=dev/managed-config-example/runtime@2#2 divergent=true changed=api_key,runtime.greeting,runtime.request_limit",
		"atomic hot override: release=2",
		"api_key=[REDACTED] api_key_version=2 divergent=true",
		"default divergence: phase=runtime severity=error fields=runtime.greeting,runtime.request_limit",
		"old snapshot is immutable: release=1",
		"restart-required candidate: release=3 category=restart_required fields=server.listen_address",
		"last-known-good after whole-candidate rejection: release=2",
		"applied: phase=runtime release=dev/managed-config-example/runtime@4#4 divergent=false changed=api_key,runtime.greeting,runtime.request_limit",
		"restored application defaults: release=4",
		"api_key=[REDACTED] api_key_version=4 divergent=false",
	} {
		if !strings.Contains(transcript, milestone) {
			t.Errorf("output missing milestone %q:\n%s", milestone, transcript)
		}
	}

	for _, plaintext := range []string{
		initialAPIKeyPlaintext,
		hotAPIKeyPlaintext,
		rejectedAPIKeyPlaintext,
		restoredAPIKeyPlaintext,
	} {
		if strings.Contains(transcript, plaintext) {
			t.Errorf("output leaked secret plaintext %q:\n%s", plaintext, transcript)
		}
	}
	if strings.Contains(transcript, "must not publish") {
		t.Errorf("output contains a hot field from the rejected mixed candidate:\n%s", transcript)
	}
	if strings.Contains(transcript, "api_key_version=3") {
		t.Errorf("output contains the secret version from the rejected mixed candidate:\n%s", transcript)
	}
}
