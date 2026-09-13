package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
	"github.com/Suhaibinator/kms/internal/core"
	"github.com/Suhaibinator/kms/internal/domain"
)

// Opt-in because Python/Node and built SDK distributions are not dependencies
// of ordinary Go tests. The wire path is real TLS gRPC backed by SQLite; the
// proxy cuts TCP without replacing the SDK loader or its process identity.
func TestExternalSDKSessionConformance(t *testing.T) {
	if os.Getenv("KMS_SDK_CONFORMANCE") != "1" {
		t.Skip("set KMS_SDK_CONFORMANCE=1 after building TypeScript SDK and installing Python SDK dependencies")
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	node := os.Getenv("KMS_CONFORMANCE_NODE")
	if node == "" {
		node = "node"
	}
	python := os.Getenv("KMS_CONFORMANCE_PYTHON")
	if python == "" {
		python = "python3"
	}
	for _, driver := range []struct {
		name    string
		command []string
	}{
		{"typescript", []string{node, "sdk/typescript/scripts/session-conformance.mjs"}},
		{"python-sync", []string{python, "sdk/python/scripts/release_conformance_client.py", "sync"}},
		{"python-async", []string{python, "sdk/python/scripts/release_conformance_client.py", "async"}},
	} {
		t.Run(driver.name, func(t *testing.T) {
			env := newLoopbackTLSEnv(t)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			admin := core.Principal{Identity: domain.Identity{Name: "network-root", Kind: domain.IdentityKindAdmin}, Method: domain.AuthMethodToken}
			ns := domain.NamespaceRef{Env: "prod", App: "conformance-" + driver.name}
			_, schema, err := env.svc.CreateApplicationWithSchema(ctx, admin, domain.Application{Name: ns.App, ReleaseName: "runtime"}, `{"type":"object"}`, "{}")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := env.svc.CreateNamespace(ctx, admin, ns, "", []domain.AuthMethod{domain.AuthMethodToken}); err != nil {
				t.Fatal(err)
			}
			ref := domain.Ref{NS: ns, Key: "setting"}
			if _, _, err := env.svc.PutParameter(ctx, admin, ref, "1", "integer", "{}"); err != nil {
				t.Fatal(err)
			}
			rel, err := env.svc.CreateConfigurationRelease(ctx, admin, domain.CreateConfigurationReleaseInput{Namespace: ns, Name: "runtime", SchemaVersion: schema.Version, Entries: []domain.ReleaseEntrySelector{{Alias: "setting", Kind: domain.ReleaseEntryParameter, Ref: ref, Version: 1}}})
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := env.svc.ActivateConfigurationRelease(ctx, admin, domain.ReleaseTrack{Namespace: ns, Name: "runtime", SchemaVersion: schema.Version}, rel.Version, nil); err != nil {
				t.Fatal(err)
			}
			proxy, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			var connections sync.Map
			var accepted atomic.Int64
			cut := func() {
				connections.Range(func(key, value any) bool { _ = key.(net.Conn).Close(); _ = value.(net.Conn).Close(); return true })
			}
			defer func() { _ = proxy.Close(); cut() }()
			go func() {
				for {
					down, err := proxy.Accept()
					if err != nil {
						return
					}
					up, err := net.Dial("tcp", env.endpoint())
					if err != nil {
						_ = down.Close()
						return
					}
					connections.Store(down, up)
					accepted.Add(1)
					go func() {
						defer connections.Delete(down)
						defer func() { _ = down.Close() }()
						defer func() { _ = up.Close() }()
						go func() { _, _ = io.Copy(up, down) }()
						_, _ = io.Copy(down, up)
					}()
				}
			}()
			cmd := exec.CommandContext(ctx, driver.command[0], driver.command[1:]...)
			cmd.Dir = root
			_, proxyPort, _ := net.SplitHostPort(proxy.Addr().String())
			cmd.Env = append(os.Environ(), "KMS_CONFORMANCE_ENDPOINT="+net.JoinHostPort("localhost", proxyPort), "KMS_CONFORMANCE_TOKEN="+env.adminToken, "KMS_CONFORMANCE_CA_FILE="+env.caFile(t), "KMS_CONFORMANCE_NAMESPACE="+ns.Env+"/"+ns.App, "KMS_CONFORMANCE_SCHEMA_VERSION=1", "KMS_CONFORMANCE_RELEASE=runtime", "KMS_CONFORMANCE_INSTANCE=conformance-instance", "PYTHONPATH="+filepath.Join(root, "sdk/python"))
			var output bytes.Buffer
			cmd.Stdout, cmd.Stderr = &output, &output
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
				if t.Failed() {
					t.Log(output.String())
				}
			}()
			rpc := kmsv1.NewAdminServiceClient(env.adminConn)
			auth := networkAuthContext(ctx, env.adminToken)
			client := env.httpClient(nil)
			defer client.CloseIdleConnections()
			var session string
			var sequence uint64
			consistent := func() bool {
				rows, err := rpc.ListReleaseSubscribers(auth, &kmsv1.ListReleaseSubscribersRequest{Namespace: networkNS(ns.Env, ns.App), ReleaseName: "runtime", SchemaVersion: &schema.Version})
				if err != nil {
					t.Fatal(err)
				}
				if len(rows.Instances) != 1 {
					return false
				}
				row := rows.Instances[0]
				if row.Classification != "applied" || !row.Connected || row.State != "applied" {
					return false
				}
				if session == "" {
					session, sequence = row.SessionId, row.Sequence
				} else if session != row.SessionId || sequence != row.Sequence {
					t.Fatalf("reconnect changed causal identity: %s/%d -> %s/%d", session, sequence, row.SessionId, row.Sequence)
				}
				live, _, err := env.svc.ListSubscribers(ctx, admin)
				if err != nil {
					t.Fatal(err)
				}
				found := false
				for _, s := range live {
					if s.InstanceID == "conformance-instance" {
						found = s.ReleaseState == "applied" && s.Effective != nil && s.Effective.Classification == row.Classification && s.Effective.Reason == row.Reason && s.Effective.Sequence == row.Sequence
					}
				}
				if !found {
					return false
				}
				req, err := http.NewRequestWithContext(ctx, http.MethodGet, env.httpsURL("/api/v1/applications/overview?name="+ns.App), nil)
				if err != nil {
					t.Fatal(err)
				}
				req.Header.Set("Authorization", "Bearer "+env.adminToken)
				resp, err := client.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = resp.Body.Close() }()
				var body struct {
					Environments []struct {
						Status string `json:"status"`
					} `json:"environments"`
				}
				if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				return resp.StatusCode == http.StatusOK && len(body.Environments) == 1 && body.Environments[0].Status == "ready"
			}
			waitForManagedState(t, consistent, "external SDK applied through persisted/live/HTTP projection")
			before := accepted.Load()
			cut()
			waitForManagedState(t, func() bool { return accepted.Load() > before && consistent() }, "external SDK same-session transport reconnect")
			// Reconciliation must not synthesize new applied events after replay.
			deadline := time.Now().Add(250 * time.Millisecond)
			for time.Now().Before(deadline) {
				if !consistent() {
					t.Fatal("applied state regressed after replay")
				}
				time.Sleep(10 * time.Millisecond)
			}
		})
	}
}
