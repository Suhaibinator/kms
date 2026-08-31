package main

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"time"

	"github.com/Suhaibinator/kms/sdk/go/kmsclient"
	"github.com/Suhaibinator/kms/sdk/go/kmsclient/kmsclienttest"
)

const (
	exampleNamespace = "dev/managed-config-example"
	exampleRelease   = "runtime"
	serverPath       = "groups/server"
	runtimePath      = "groups/runtime"
	apiKeyPath       = "secrets/api-key"
	operationTimeout = 5 * time.Second

	initialAPIKeyPlaintext  = "initial-api-key-plaintext-canary"
	hotAPIKeyPlaintext      = "hot-api-key-plaintext-canary"
	rejectedAPIKeyPlaintext = "rejected-api-key-plaintext-canary"
	restoredAPIKeyPlaintext = "restored-api-key-plaintext-canary"
)

type releaseValues struct {
	releaseVersion     uint64
	activationRevision uint64
	serverVersion      uint64
	runtimeVersion     uint64
	apiKeyVersion      uint64
	listenAddress      string
	greeting           string
	requestLimit       int
	apiKeyPlaintext    string
}

func initialReleaseValues() releaseValues {
	return releaseValues{
		releaseVersion:     1,
		activationRevision: 1,
		serverVersion:      1,
		runtimeVersion:     1,
		apiKeyVersion:      1,
		listenAddress:      "127.0.0.1:8080",
		greeting:           "hello from application defaults",
		requestLimit:       100,
		apiKeyPlaintext:    initialAPIKeyPlaintext,
	}
}

func hotOverrideValues(initial releaseValues) releaseValues {
	return releaseValues{
		releaseVersion:     2,
		activationRevision: 2,
		serverVersion:      initial.serverVersion,
		runtimeVersion:     2,
		apiKeyVersion:      2,
		listenAddress:      initial.listenAddress,
		greeting:           "maintenance mode",
		requestLimit:       5,
		apiKeyPlaintext:    hotAPIKeyPlaintext,
	}
}

func restartRequiredValues() releaseValues {
	return releaseValues{
		releaseVersion:     3,
		activationRevision: 3,
		serverVersion:      2,
		runtimeVersion:     3,
		apiKeyVersion:      3,
		listenAddress:      "127.0.0.1:9090",
		greeting:           "must not publish",
		requestLimit:       1,
		apiKeyPlaintext:    rejectedAPIKeyPlaintext,
	}
}

func restoredDefaultValues(initial releaseValues) releaseValues {
	return releaseValues{
		releaseVersion:     4,
		activationRevision: 4,
		serverVersion:      initial.serverVersion,
		runtimeVersion:     4,
		apiKeyVersion:      4,
		listenAddress:      initial.listenAddress,
		greeting:           initial.greeting,
		requestLimit:       initial.requestLimit,
		apiKeyPlaintext:    restoredAPIKeyPlaintext,
	}
}

type demoKMS struct {
	server       *kmsclienttest.Server
	client       *kmsclient.Client
	subscription *kmsclienttest.ReleaseSubscription
}

func newDemoKMS(initial releaseValues) (*demoKMS, error) {
	server, err := kmsclienttest.New()
	if err != nil {
		return nil, fmt.Errorf("start in-process KMS: %w", err)
	}

	initialSpec, err := scriptRelease(server, initial)
	if err != nil {
		server.Close()
		return nil, err
	}
	if _, err := server.SetActiveRelease(initialSpec, initial.activationRevision); err != nil {
		server.Close()
		return nil, fmt.Errorf("install initial release: %w", err)
	}

	client, err := kmsclient.NewClient(kmsclient.Config{
		Namespace:   exampleNamespace,
		ClientName:  "managed-config-example",
		DialOptions: server.DialOptions(),
	})
	if err != nil {
		server.Close()
		return nil, fmt.Errorf("create KMS client: %w", err)
	}
	return &demoKMS{server: server, client: client}, nil
}

func (d *demoKMS) close() {
	_ = d.client.Close()
	d.server.Close()
}

func (d *demoKMS) waitForInitial(ctx context.Context, version uint64) error {
	subscription, err := d.server.WaitForReleaseSubscribe(timeoutFrom(ctx))
	if err != nil {
		return fmt.Errorf("wait for release subscription: %w", err)
	}
	d.subscription = subscription
	_, err = d.waitForAcknowledgement(ctx, version, kmsclient.ReleaseStateApplied)
	return err
}

func (d *demoKMS) activate(ctx context.Context, values releaseValues, state string) (string, error) {
	if d.subscription == nil {
		return "", fmt.Errorf("activate release %d before watching releases", values.releaseVersion)
	}
	spec, err := scriptRelease(d.server, values)
	if err != nil {
		return "", err
	}
	if _, err := d.server.ActivateConfigurationRelease(spec, values.activationRevision); err != nil {
		return "", fmt.Errorf("activate release %d: %w", values.releaseVersion, err)
	}
	return d.waitForAcknowledgement(ctx, values.releaseVersion, state)
}

func scriptRelease(server *kmsclienttest.Server, values releaseValues) (kmsclienttest.ReleaseSpec, error) {
	serverJSON, err := json.Marshal(struct {
		ListenAddress string `json:"listen_address"`
	}{ListenAddress: values.listenAddress})
	if err != nil {
		return kmsclienttest.ReleaseSpec{}, fmt.Errorf("encode server group: %w", err)
	}
	runtimeJSON, err := json.Marshal(struct {
		Greeting     string `json:"greeting"`
		RequestLimit int    `json:"request_limit"`
	}{Greeting: values.greeting, RequestLimit: values.requestLimit})
	if err != nil {
		return kmsclienttest.ReleaseSpec{}, fmt.Errorf("encode runtime group: %w", err)
	}

	server.SetParameterVersion(exampleNamespace, serverPath, string(serverJSON), "json", values.serverVersion)
	server.SetParameterVersion(exampleNamespace, runtimePath, string(runtimeJSON), "json", values.runtimeVersion)
	server.SetSecretVersion(exampleNamespace, apiKeyPath, []byte(values.apiKeyPlaintext), "text/plain", values.apiKeyVersion)
	return kmsclienttest.ReleaseSpec{
		Namespace:     exampleNamespace,
		Name:          exampleRelease,
		Version:       values.releaseVersion,
		SchemaID:      "managed-config-example/runtime",
		SchemaVersion: 1,
		Entries: []kmsclienttest.ReleaseEntrySpec{
			{Alias: "server", Kind: "parameter", Path: serverPath, Version: values.serverVersion, ContentType: "json"},
			{Alias: "runtime", Kind: "parameter", Path: runtimePath, Version: values.runtimeVersion, ContentType: "json"},
			{Alias: "api_key", Kind: "secret", Path: apiKeyPath, Version: values.apiKeyVersion},
		},
	}, nil
}

func (d *demoKMS) waitForAcknowledgement(ctx context.Context, version uint64, state string) (string, error) {
	for {
		acknowledgement, err := d.subscription.WaitAcknowledgement(timeoutFrom(ctx))
		if err != nil {
			return "", fmt.Errorf("wait for release %d state %s: %w", version, state, err)
		}
		if acknowledgement.GetVersion() == version && acknowledgement.GetState() == state {
			return acknowledgement.GetRejectionCategory(), nil
		}
	}
}

func timeoutFrom(ctx context.Context) time.Duration {
	if err := ctx.Err(); err != nil {
		return time.Nanosecond
	}
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining < operationTimeout {
			return remaining
		}
	}
	return operationTimeout
}
