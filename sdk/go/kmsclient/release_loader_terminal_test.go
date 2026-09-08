package kmsclient

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type terminalWatchReconcileClient struct {
	kmsv1.ConfigurationReleaseServiceClient
	calls        atomic.Int32
	blocked      chan struct{}
	rejected     chan struct{}
	rejectedOnce sync.Once
}

func (c *terminalWatchReconcileClient) GetActiveRelease(ctx context.Context, req *kmsv1.GetActiveReleaseRequest, opts ...grpc.CallOption) (*kmsv1.GetActiveReleaseResponse, error) {
	if c.calls.Add(1) == 2 {
		close(c.blocked)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return c.ConfigurationReleaseServiceClient.GetActiveRelease(ctx, req, opts...)
}

func (c *terminalWatchReconcileClient) WatchRelease(ctx context.Context, opts ...grpc.CallOption) (kmsv1.ConfigurationReleaseService_WatchReleaseClient, error) {
	stream, err := c.ConfigurationReleaseServiceClient.WatchRelease(ctx, opts...)
	if err != nil {
		return nil, err
	}
	return &terminalWatchReconcileStream{ConfigurationReleaseService_WatchReleaseClient: stream, owner: c}, nil
}

type terminalWatchReconcileStream struct {
	kmsv1.ConfigurationReleaseService_WatchReleaseClient
	owner *terminalWatchReconcileClient
}

func (s *terminalWatchReconcileStream) Recv() (*kmsv1.WatchReleaseEvent, error) {
	event, err := s.ConfigurationReleaseService_WatchReleaseClient.Recv()
	if status.Code(err) == codes.PermissionDenied {
		s.owner.rejectedOnce.Do(func() { close(s.owner.rejected) })
	}
	return event, err
}

func TestReleaseLoaderTerminalWatchCancelsBlockedReconciliationAndPreparation(t *testing.T) {
	server := newReleaseLoaderServer()
	server.setActive(testRelease(1, `{"enabled":true}`), 10)
	server.parameters["settings"] = &kmsv1.Parameter{Ref: testResource("settings"), Value: `{"enabled":true}`, ContentType: "json", Version: 1}
	server.secrets["password"] = &kmsv1.GetSecretResponse{Ref: testResource("password"), Version: 1, Value: []byte("fixture"), ContentType: "text/plain"}
	client := newReleaseTestClient(t, server)
	gate := &terminalWatchReconcileClient{ConfigurationReleaseServiceClient: client.releases, blocked: make(chan struct{}), rejected: make(chan struct{})}
	client.releases = gate
	loader, err := NewReleaseLoader(client, ReleaseLoaderConfig{Name: "runtime", SchemaVersion: new(uint64), ReconcileInterval: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	entered := make(chan context.Context, 1)
	done := make(chan struct{})
	var runErr error
	prepared := &testPreparedRelease{done: make(chan struct{})}
	go func() {
		defer close(done)
		runErr = loader.Run(ctx, func(candidateCtx context.Context, _ ReleaseSnapshot) (PreparedRelease, error) {
			entered <- candidateCtx
			<-candidateCtx.Done()
			return prepared, nil
		})
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("loader did not stop")
		}
	})
	var candidateCtx context.Context
	select {
	case candidateCtx = <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("prepare did not start")
	}
	select {
	case <-server.watchRegs:
	case <-time.After(3 * time.Second):
		t.Fatal("watch did not register")
	}
	select {
	case <-gate.blocked:
	case <-time.After(3 * time.Second):
		t.Fatal("reconciliation did not block")
	}
	server.mu.Lock()
	server.watchErr = status.Error(codes.PermissionDenied, "watch permission revoked")
	server.mu.Unlock()
	server.watchKills <- struct{}{}
	select {
	case <-gate.rejected:
	case <-time.After(3 * time.Second):
		t.Fatal("client did not receive terminal rejection")
	}
	select {
	case <-candidateCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("known terminal watch rejection did not cancel preparation while reconciliation was blocked")
	}

	select {
	case <-done:
		if !errors.Is(runErr, ErrPermissionDenied) {
			t.Fatalf("Run error = %v, want permission denied", runErr)
		}
	case <-time.After(time.Second):
		t.Fatal("terminal error did not interrupt reconciliation")
	}
	deadline := time.Now().Add(time.Second)
	for prepared.aborts.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if prepared.commits.Load() != 0 || prepared.aborts.Load() != 1 {
		t.Fatalf("prepared release: commits=%d aborts=%d, want 0 commits and 1 abort", prepared.commits.Load(), prepared.aborts.Load())
	}
}
