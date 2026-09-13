package kmsclient

import (
	"context"
	"encoding/json/v2"
	"os"
	"strings"
	"testing"
	"time"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func TestReleaseLoaderSharedAcknowledgementConformance(t *testing.T) {
	data, err := os.ReadFile("../../testdata/release_ack_conformance.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Cases []struct {
			Name     string   `json:"name"`
			States   []string `json:"states"`
			Retained []uint64 `json:"retained_sequences"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	for _, tc := range fixture.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			loader := &ReleaseLoader{client: &Client{clientName: "test-client"}, sessionID: "test-session", pendingAck: map[string]*kmsv1.ReleaseAcknowledgement{}, ackGeneration: map[string]uint64{}, dirtyAck: map[string]bool{}, ackSignal: make(chan struct{}, 1)}
			candidate := releaseCandidate{release: &kmsv1.ConfigurationRelease{Name: "runtime", Version: 4}, revision: 1, activationRevision: 153, sessionTarget: true}
			for _, state := range tc.States {
				category := ""
				if state == ReleaseStateRejected {
					category = ReleaseRejectPrepareFailed
				}
				loader.ackWithDivergence(namespaceRef{env: "prod", app: "app"}, candidate, state, category, ackDivergence{divergent: true, fieldCount: 2})
			}
			stream := &blockFirstAcknowledgementStream{started: make(chan struct{}), allow: make(chan struct{})}
			close(stream.allow)
			for range 2 {
				if err := loader.sendPendingAcks(stream, true); err != nil {
					t.Fatal(err)
				}
			}
			acks := stream.acknowledgements()
			if len(acks) != 2*len(tc.Retained) {
				t.Fatalf("got %d events, want %d", len(acks), 2*len(tc.Retained))
			}
			for i, sequence := range tc.Retained {
				ack := acks[i]
				if ack.Sequence != sequence || ack.SessionId != "test-session" || ack.TargetRevision != 1 || ack.ActivationRevision != 153 {
					t.Fatalf("invalid event: %v", ack)
				}
				if !proto.Equal(ack, acks[i+len(tc.Retained)]) {
					t.Fatal("replay changed original payload")
				}
			}
			last := acks[len(tc.Retained)-1]
			if last.State != ReleaseStateApplied || !last.AppliedDivergent || last.DivergentFieldCount != 2 || last.RejectionCategory != "" {
				t.Fatalf("incoherent applied metadata: %v", last)
			}
		})
	}
}

type noSessionReleaseClient struct {
	kmsv1.ConfigurationReleaseServiceClient
}

func (c noSessionReleaseClient) RegisterReleaseSession(context.Context, *kmsv1.RegisterReleaseSessionRequest, ...grpc.CallOption) (*kmsv1.ReleaseSessionResponse, error) {
	return nil, status.Error(codes.Unimplemented, "old server")
}

func TestReleaseLoaderRequiresSessionProtocol(t *testing.T) {
	loader := &ReleaseLoader{client: &Client{releases: noSessionReleaseClient{}}, sessionID: "session"}
	err := loader.registerSession(context.Background(), namespaceRef{env: "prod", app: "app"})
	if err == nil || !strings.Contains(err.Error(), "upgrade the KMS server") || loader.sessionRegistered {
		t.Fatalf("expected actionable compatibility error, got %v", err)
	}
}

func TestReleaseLoaderOldExecutionCannotPublishIntoNewSession(t *testing.T) {
	loader := &ReleaseLoader{client: &Client{}, sessionID: "new", pendingAck: map[string]*kmsv1.ReleaseAcknowledgement{}, ackGeneration: map[string]uint64{}, dirtyAck: map[string]bool{}, ackSignal: make(chan struct{}, 1)}
	candidate := releaseCandidate{release: &kmsv1.ConfigurationRelease{Name: "runtime", Version: 4}, revision: 1, sessionTarget: true, sessionID: "old"}
	loader.ack(namespaceRef{env: "prod", app: "app"}, candidate, ReleaseStateRejected, ReleaseRejectSuperseded)
	if len(loader.pendingAck) != 0 || loader.nextAckSequence.Load() != 0 {
		t.Fatal("old execution polluted new session")
	}
}

func TestReleaseLoaderUnchangedAppliedTargetDoesNotGenerateCandidate(t *testing.T) {
	candidate := releaseCandidate{release: &kmsv1.ConfigurationRelease{Name: "runtime", Version: 4}, revision: 1, activationRevision: 153, sessionTarget: true, source: releaseCandidateSourceReconciliation}
	if shouldQueueReleaseCandidate(candidate, candidate, true, false) {
		t.Fatal("unchanged successful target queued again")
	}
	if !shouldQueueReleaseCandidate(candidate, candidate, true, true) {
		t.Fatal("genuine retry was suppressed")
	}
}

func TestReleaseLoaderNewRunHasNewSessionAndSequence(t *testing.T) {
	server := newReleaseLoaderServer()
	server.setActive(testRelease(1, "one"), 153)
	server.parameters["settings"] = &kmsv1.Parameter{Ref: testResource("settings"), Value: "one", ContentType: "json", Version: 1}
	server.secrets["password"] = &kmsv1.GetSecretResponse{Ref: testResource("password"), Version: 1, Value: []byte("secret"), ContentType: "text/plain"}
	loader, err := NewReleaseLoader(newReleaseTestClient(t, server), ReleaseLoaderConfig{Name: "runtime", SchemaVersion: new(uint64), InstanceID: "stable"})
	if err != nil {
		t.Fatal(err)
	}
	previousSession := ""
	for range 2 {
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() {
			done <- loader.Run(ctx, func(context.Context, ReleaseSnapshot) (PreparedRelease, error) {
				return &testPreparedRelease{done: make(chan struct{})}, nil
			})
		}()
		var registration *kmsv1.ReleaseWatchRegistration
		select {
		case registration = <-server.watchRegs:
		case <-time.After(2 * time.Second):
			cancel()
			t.Fatal("no registration")
		}
		if registration.SessionId == "" || registration.SessionId == previousSession || registration.InstanceId != "stable" {
			cancel()
			t.Fatal("run reused session or lost instance identity")
		}
		previousSession = registration.SessionId
		applied := waitReleaseAckState(t, server, ReleaseStateApplied)
		if applied.Sequence != 3 || applied.SessionId != previousSession {
			cancel()
			t.Fatalf("new execution has stale events: %v", applied)
		}
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("run did not stop")
		}
	}
}

func TestReleaseLoaderCancelledPreparationCannotChangeNextExecution(t *testing.T) {
	server := newReleaseLoaderServer()
	server.setActive(testRelease(1, "one"), 153)
	server.parameters["settings"] = &kmsv1.Parameter{Ref: testResource("settings"), Value: "one", ContentType: "json", Version: 1}
	server.secrets["password"] = &kmsv1.GetSecretResponse{Ref: testResource("password"), Version: 1, Value: []byte("secret"), ContentType: "text/plain"}
	loader, err := NewReleaseLoader(newReleaseTestClient(t, server), ReleaseLoaderConfig{Name: "runtime", SchemaVersion: new(uint64)})
	if err != nil {
		t.Fatal(err)
	}
	oldCtx, oldCancel := context.WithCancel(context.Background())
	entered, allow := make(chan struct{}), make(chan struct{})
	oldDone := make(chan error, 1)
	oldPrepared := &testPreparedRelease{done: make(chan struct{})}
	go func() {
		oldDone <- loader.Run(oldCtx, func(context.Context, ReleaseSnapshot) (PreparedRelease, error) {
			close(entered)
			<-allow
			return oldPrepared, nil
		})
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		oldCancel()
		close(allow)
		t.Fatal("preparation did not start")
	}
	oldCancel()
	select {
	case <-oldDone:
	case <-time.After(2 * time.Second):
		close(allow)
		t.Fatal("old Run did not return")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- loader.Run(ctx, func(context.Context, ReleaseSnapshot) (PreparedRelease, error) {
			return &testPreparedRelease{done: make(chan struct{})}, nil
		})
	}()
	waitReleaseAckState(t, server, ReleaseStateApplied)
	close(allow)
	deadline := time.Now().Add(2 * time.Second)
	for oldPrepared.aborts.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if oldPrepared.aborts.Load() != 1 {
		t.Fatal("old prepared resource was not aborted")
	}
	// The old callback's rejection is fenced before modifying either local
	// status or the new execution's replay queue.
	loader.ackMu.Lock()
	oldRejection := loader.pendingAck[ReleaseStateRejected]
	loader.ackMu.Unlock()
	if oldRejection != nil {
		t.Fatal("old callback polluted new acknowledgement state")
	}
	if loader.Status().State != ReleaseStateApplied {
		t.Fatal("old callback changed new execution status")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("new Run did not stop")
	}
}
