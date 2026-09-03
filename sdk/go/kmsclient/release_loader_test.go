package kmsclient

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"
)

type releaseLoaderServer struct {
	kmsv1.UnimplementedParameterServiceServer
	kmsv1.UnimplementedSecretServiceServer
	kmsv1.UnimplementedConfigurationReleaseServiceServer

	mu               sync.Mutex
	active           *kmsv1.GetActiveReleaseResponse
	parameters       map[string]*kmsv1.Parameter
	secrets          map[string]*kmsv1.GetSecretResponse
	secretToken      string
	parameterFetches int
	secretFetches    int

	watchEvents chan *kmsv1.WatchReleaseEvent
	watchKills  chan struct{}
	watchRegs   chan *kmsv1.ReleaseWatchRegistration
	acks        chan *kmsv1.ReleaseAcknowledgement
}

func newReleaseLoaderServer() *releaseLoaderServer {
	return &releaseLoaderServer{
		parameters:  make(map[string]*kmsv1.Parameter),
		secrets:     make(map[string]*kmsv1.GetSecretResponse),
		watchEvents: make(chan *kmsv1.WatchReleaseEvent, 16),
		watchKills:  make(chan struct{}, 4),
		watchRegs:   make(chan *kmsv1.ReleaseWatchRegistration, 4),
		acks:        make(chan *kmsv1.ReleaseAcknowledgement, 32),
	}
}

func testResource(key string) *kmsv1.ResourceRef {
	return &kmsv1.ResourceRef{Namespace: &kmsv1.NamespaceRef{Env: "prod", App: "app"}, Key: key}
}

func (s *releaseLoaderServer) GetParameter(_ context.Context, req *kmsv1.GetParameterRequest) (*kmsv1.GetParameterResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.parameterFetches++
	p := s.parameters[req.GetRef().GetKey()]
	if p == nil {
		return nil, status.Error(5, "not found")
	}
	return &kmsv1.GetParameterResponse{Parameter: p}, nil
}

func (s *releaseLoaderServer) GetSecret(ctx context.Context, req *kmsv1.GetSecretRequest) (*kmsv1.GetSecretResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.secretFetches++
	md, _ := metadata.FromIncomingContext(ctx)
	if values := md.Get(mdSecretToken); len(values) > 0 {
		s.secretToken = values[0]
	}
	secret := s.secrets[req.GetRef().GetKey()]
	if secret == nil {
		return nil, status.Error(5, "not found")
	}
	return secret, nil
}

func (s *releaseLoaderServer) GetActiveRelease(_ context.Context, _ *kmsv1.GetActiveReleaseRequest) (*kmsv1.GetActiveReleaseResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active == nil {
		return nil, status.Error(5, "not found")
	}
	return s.active, nil
}

func (s *releaseLoaderServer) WatchRelease(stream kmsv1.ConfigurationReleaseService_WatchReleaseServer) error {
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	s.watchRegs <- first.GetRegister()
	recvErr := make(chan error, 1)
	go func() {
		// Record each acknowledgement before the next Recv so a following EOF
		// cannot overtake it. This mirrors the production release server.
		for {
			request, recvError := stream.Recv()
			if recvError != nil {
				recvErr <- recvError
				return
			}
			if ack := request.GetAcknowledgement(); ack != nil {
				s.acks <- ack
			}
		}
	}()
	for {
		select {
		case event := <-s.watchEvents:
			if err := stream.Send(event); err != nil {
				return err
			}
		case <-s.watchKills:
			return status.Error(14, "test disconnect")
		case err := <-recvErr:
			return err
		case <-stream.Context().Done():
			return stream.Context().Err()
		}
	}
}

func (s *releaseLoaderServer) setActive(release *kmsv1.ConfigurationRelease, revision uint64) {
	s.mu.Lock()
	s.active = &kmsv1.GetActiveReleaseResponse{Release: release, ActivationRevision: revision}
	s.mu.Unlock()
}

func testRelease(version uint64, parameterValue string) *kmsv1.ConfigurationRelease {
	digest := sha256.Sum256([]byte(parameterValue))
	release := &kmsv1.ConfigurationRelease{
		Namespace: &kmsv1.NamespaceRef{Env: "prod", App: "app"},
		Name:      "runtime",
		Version:   version,
		Entries: []*kmsv1.ConfigurationReleaseEntry{
			{
				Alias: "settings", Kind: "parameter", Ref: testResource("settings"), Version: version,
				ContentType: "json", ParameterDigest: hex.EncodeToString(digest[:]),
			},
			{
				Alias: "password", Kind: "secret", Ref: testResource("password"), Version: version,
				ContentType: "text/plain", HasAccessToken: true,
			},
		},
	}
	release.Digest, _ = deterministicReleaseDigest(release)
	return release
}

func newReleaseTestClient(t *testing.T, server *releaseLoaderServer) *Client {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	grpcServer := grpc.NewServer()
	kmsv1.RegisterParameterServiceServer(grpcServer, server)
	kmsv1.RegisterSecretServiceServer(grpcServer, server)
	kmsv1.RegisterConfigurationReleaseServiceServer(grpcServer, server)
	go func() { _ = grpcServer.Serve(lis) }()
	t.Cleanup(func() {
		grpcServer.Stop()
		_ = lis.Close()
	})
	client, err := NewClient(Config{
		Namespace:  "prod/app",
		ClientName: "loader-test",
		DialOptions: []grpc.DialOption{
			grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
			grpc.WithTransportCredentials(insecure.NewCredentials()),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

type gatedRejectedAckClient struct {
	kmsv1.ConfigurationReleaseServiceClient
	started chan struct{}
	allow   <-chan struct{}
	once    sync.Once
}

func (c *gatedRejectedAckClient) WatchRelease(ctx context.Context, opts ...grpc.CallOption) (kmsv1.ConfigurationReleaseService_WatchReleaseClient, error) {
	stream, err := c.ConfigurationReleaseServiceClient.WatchRelease(ctx, opts...)
	if err != nil {
		return nil, err
	}
	return &gatedRejectedAckStream{
		ConfigurationReleaseService_WatchReleaseClient: stream,
		started: c.started,
		allow:   c.allow,
		once:    &c.once,
	}, nil
}

type gatedRejectedAckStream struct {
	kmsv1.ConfigurationReleaseService_WatchReleaseClient
	started chan struct{}
	allow   <-chan struct{}
	once    *sync.Once
}

func (s *gatedRejectedAckStream) Send(request *kmsv1.WatchReleaseRequest) error {
	ack := request.GetAcknowledgement()
	if ack != nil && ack.GetState() == ReleaseStateRejected {
		s.once.Do(func() { close(s.started) })
		select {
		case <-s.allow:
		case <-s.Context().Done():
			return s.Context().Err()
		}
	}
	return s.ConfigurationReleaseService_WatchReleaseClient.Send(request)
}

type failRejectedAckOnceClient struct {
	kmsv1.ConfigurationReleaseServiceClient
	failed   atomic.Bool
	attempts chan struct{}
}

func (c *failRejectedAckOnceClient) WatchRelease(ctx context.Context, opts ...grpc.CallOption) (kmsv1.ConfigurationReleaseService_WatchReleaseClient, error) {
	stream, err := c.ConfigurationReleaseServiceClient.WatchRelease(ctx, opts...)
	if err != nil {
		return nil, err
	}
	return &failRejectedAckOnceStream{
		ConfigurationReleaseService_WatchReleaseClient: stream,
		failed:   &c.failed,
		attempts: c.attempts,
	}, nil
}

type failRejectedAckOnceStream struct {
	kmsv1.ConfigurationReleaseService_WatchReleaseClient
	failed   *atomic.Bool
	attempts chan struct{}
}

func (s *failRejectedAckOnceStream) Send(request *kmsv1.WatchReleaseRequest) error {
	ack := request.GetAcknowledgement()
	if ack != nil && ack.GetState() == ReleaseStateRejected {
		s.attempts <- struct{}{}
		if s.failed.CompareAndSwap(false, true) {
			_ = s.CloseSend()
			return errors.New("injected rejected acknowledgement send failure")
		}
	}
	return s.ConfigurationReleaseService_WatchReleaseClient.Send(request)
}

type blockFirstAcknowledgementStream struct {
	kmsv1.ConfigurationReleaseService_WatchReleaseClient
	started chan struct{}
	allow   chan struct{}
	calls   atomic.Int32
	mu      sync.Mutex
	sent    []*kmsv1.ReleaseAcknowledgement
}

func (s *blockFirstAcknowledgementStream) Send(request *kmsv1.WatchReleaseRequest) error {
	if s.calls.Add(1) == 1 {
		close(s.started)
		<-s.allow
	}
	s.mu.Lock()
	s.sent = append(s.sent, proto.Clone(request.GetAcknowledgement()).(*kmsv1.ReleaseAcknowledgement))
	s.mu.Unlock()
	return nil
}

func (s *blockFirstAcknowledgementStream) acknowledgements() []*kmsv1.ReleaseAcknowledgement {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*kmsv1.ReleaseAcknowledgement, len(s.sent))
	for i, acknowledgement := range s.sent {
		out[i] = proto.Clone(acknowledgement).(*kmsv1.ReleaseAcknowledgement)
	}
	return out
}

type testPreparedRelease struct {
	commits atomic.Int32
	aborts  atomic.Int32
	done    chan struct{}
}

func (p *testPreparedRelease) Commit() {
	p.commits.Add(1)
	select {
	case <-p.done:
	default:
		close(p.done)
	}
}
func (p *testPreparedRelease) Abort() { p.aborts.Add(1) }

type testReleaseRejectionError struct {
	category string
	detail   string
}

func (e *testReleaseRejectionError) Error() string { return e.detail }

func (e *testReleaseRejectionError) ReleaseRejectionCategory() string { return e.category }

func TestShouldQueueReleaseCandidateRetriesOnlyFromReconciliation(t *testing.T) {
	release := testRelease(2, "two")
	latest := releaseCandidate{release: release, revision: 2, source: releaseCandidateSourceActivation}

	tests := []struct {
		name        string
		candidate   releaseCandidate
		haveLatest  bool
		retryLatest bool
		want        bool
	}{
		{name: "first candidate", candidate: latest, want: true},
		{name: "nil candidate", candidate: releaseCandidate{}, want: false},
		{
			name:        "duplicate activation after rejection",
			candidate:   releaseCandidate{release: release, revision: 2, source: releaseCandidateSourceActivation},
			haveLatest:  true,
			retryLatest: true,
			want:        false,
		},
		{
			name:        "duplicate reconciliation while attempt is not retryable",
			candidate:   releaseCandidate{release: release, revision: 2, source: releaseCandidateSourceReconciliation},
			haveLatest:  true,
			retryLatest: false,
			want:        false,
		},
		{
			name:        "reconciliation after rejection",
			candidate:   releaseCandidate{release: release, revision: 2, source: releaseCandidateSourceReconciliation},
			haveLatest:  true,
			retryLatest: true,
			want:        true,
		},
		{
			name:        "stale reconciliation",
			candidate:   releaseCandidate{release: testRelease(1, "one"), revision: 1, source: releaseCandidateSourceReconciliation},
			haveLatest:  true,
			retryLatest: true,
			want:        false,
		},
		{
			name:       "new activation",
			candidate:  releaseCandidate{release: testRelease(3, "three"), revision: 3, source: releaseCandidateSourceActivation},
			haveLatest: true,
			want:       true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldQueueReleaseCandidate(tt.candidate, latest, tt.haveLatest, tt.retryLatest); got != tt.want {
				t.Fatalf("shouldQueueReleaseCandidate() = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestReleaseLoaderResolvesRedactsCommitsAndAcknowledges(t *testing.T) {
	server := newReleaseLoaderServer()
	release := testRelease(7, `{"enabled":true}`)
	server.setActive(release, 42)
	server.parameters["settings"] = &kmsv1.Parameter{Ref: testResource("settings"), Value: `{"enabled":true}`, ContentType: "json", Version: 7}
	server.secrets["password"] = &kmsv1.GetSecretResponse{Ref: testResource("password"), Version: 7, Value: []byte("very-secret"), ContentType: "text/plain"}
	client := newReleaseTestClient(t, server)
	loader, err := NewReleaseLoader(client, ReleaseLoaderConfig{
		Name: "runtime",
		SecretTokenProvider: func(alias, path string) (string, bool) {
			if alias != "password" || path != "/prod/app/password" {
				t.Fatalf("unexpected token request for %s %s", alias, path)
			}
			return "local-token", true
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	prepared := &testPreparedRelease{done: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	go func() {
		runErr <- loader.Run(ctx, func(_ context.Context, snapshot ReleaseSnapshot) (PreparedRelease, error) {
			if snapshot.Version() != 7 || snapshot.ActivationRevision() != 42 || snapshot.Digest() != release.GetDigest() {
				t.Errorf("unexpected snapshot identity: %s", snapshot)
			}
			parameter, ok := snapshot.Parameter("settings")
			if !ok || parameter.Value() != `{"enabled":true}` {
				t.Errorf("unexpected parameter: %#v %t", parameter, ok)
			}
			secret, ok := snapshot.Secret("password")
			if !ok || secret.StringValue() != "very-secret" {
				t.Error("secret was not explicitly accessible")
			}
			if strings.Contains(fmt.Sprintf("%v", snapshot), "very-secret") || strings.Contains(fmt.Sprintf("%#v", snapshot), "very-secret") {
				t.Error("snapshot formatting leaked secret")
			}
			encoded, marshalErr := json.Marshal(snapshot)
			if marshalErr != nil || strings.Contains(string(encoded), "very-secret") || strings.Contains(string(encoded), `{"enabled":true}`) {
				t.Errorf("snapshot JSON included resolved values: %s (%v)", encoded, marshalErr)
			}
			return prepared, nil
		})
	}()

	select {
	case <-prepared.done:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for release commit")
	}
	if prepared.commits.Load() != 1 || prepared.aborts.Load() != 0 {
		t.Fatalf("commit/abort = %d/%d", prepared.commits.Load(), prepared.aborts.Load())
	}
	server.mu.Lock()
	token := server.secretToken
	server.mu.Unlock()
	if token != "local-token" {
		t.Fatalf("secret token = %q", token)
	}
	if !eventually(t, 2*time.Second, func() bool {
		status := loader.Status()
		return status.AppliedVersion == 7 && status.AppliedRevision == 42
	}) {
		t.Fatalf("status = %+v", loader.Status())
	}

	states := make(map[string]bool)
	deadline := time.After(2 * time.Second)
	for len(states) < 3 {
		select {
		case ack := <-server.acks:
			states[ack.GetState()] = true
			if ack.GetDiagnostic() != "" {
				t.Fatal("loader emitted unredacted diagnostic")
			}
		case <-deadline:
			t.Fatalf("acks = %v, want received/prepared/applied", states)
		}
	}
	for _, state := range []string{ReleaseStateReceived, ReleaseStatePrepared, ReleaseStateApplied} {
		if !states[state] {
			t.Fatalf("missing %s ack: %v", state, states)
		}
	}
	server.watchKills <- struct{}{}
	var secondRegistration *kmsv1.ReleaseWatchRegistration
	select {
	case secondRegistration = <-server.watchRegs:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for release watch reconnect")
	}
	if secondRegistration.GetInstanceId() != loader.InstanceID() || secondRegistration.GetLastSeenRevision() != 42 {
		t.Fatalf("reconnect registration = %#v", secondRegistration)
	}
	replayed := make(map[string]int)
	for len(replayed) < 3 {
		select {
		case ack := <-server.acks:
			replayed[ack.GetState()]++
		case <-time.After(2 * time.Second):
			t.Fatalf("replayed acks = %v", replayed)
		}
	}
	for state, count := range replayed {
		if count != 1 {
			t.Fatalf("state %s replayed %d times on one reconnect", state, count)
		}
	}
	cancel()
	if err := <-runErr; err != context.Canceled {
		t.Fatalf("Run error = %v, want context.Canceled", err)
	}
}

func TestReleaseLoaderStartupRejectionWaitsForRejectedAcknowledgementSend(t *testing.T) {
	server := newReleaseLoaderServer()
	release := testRelease(7, `{"enabled":true}`)
	server.setActive(release, 42)
	server.parameters["settings"] = &kmsv1.Parameter{Ref: testResource("settings"), Value: `{"enabled":true}`, ContentType: "json", Version: 7}
	server.secrets["password"] = &kmsv1.GetSecretResponse{Ref: testResource("password"), Version: 7, Value: []byte("secret"), ContentType: "text/plain"}
	client := newReleaseTestClient(t, server)

	rejectedSendStarted := make(chan struct{})
	allowRejectedSend := make(chan struct{})
	sendReleased := false
	defer func() {
		if !sendReleased {
			close(allowRejectedSend)
		}
	}()
	client.releases = &gatedRejectedAckClient{
		ConfigurationReleaseServiceClient: client.releases,
		started:                           rejectedSendStarted,
		allow:                             allowRejectedSend,
	}

	loader, err := NewReleaseLoader(client, ReleaseLoaderConfig{
		Name:                "runtime",
		SecretTokenProvider: func(string, string) (string, bool) { return "token", true },
	})
	if err != nil {
		t.Fatal(err)
	}
	allowPrepareFailure := make(chan struct{})
	runErr := make(chan error, 1)
	go func() {
		runErr <- loader.Run(context.Background(), func(context.Context, ReleaseSnapshot) (PreparedRelease, error) {
			<-allowPrepareFailure
			return nil, errors.New("reject initial candidate")
		})
	}()

	select {
	case <-server.watchRegs:
	case <-time.After(2 * time.Second):
		t.Fatal("release watch did not register before startup rejection")
	}
	close(allowPrepareFailure)
	select {
	case <-rejectedSendStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("release watch never attempted the rejected acknowledgement")
	}

	select {
	case err := <-runErr:
		t.Fatalf("Run returned %v before the rejected acknowledgement send completed", err)
	case <-time.After(250 * time.Millisecond):
	}

	close(allowRejectedSend)
	sendReleased = true
	var rejected *kmsv1.ReleaseAcknowledgement
	deadline := time.After(2 * time.Second)
	for rejected == nil {
		select {
		case ack := <-server.acks:
			if ack.GetState() == ReleaseStateRejected {
				rejected = ack
			}
		case <-deadline:
			t.Fatal("server did not record the rejected acknowledgement")
		}
	}
	if rejected.GetRejectionCategory() != ReleaseRejectPrepareFailed {
		t.Fatalf("rejection category = %q, want %q", rejected.GetRejectionCategory(), ReleaseRejectPrepareFailed)
	}
	if err := <-runErr; err == nil || !strings.Contains(err.Error(), ReleaseRejectPrepareFailed) {
		t.Fatalf("Run error = %v, want %s", err, ReleaseRejectPrepareFailed)
	}
}

func TestReleaseLoaderGracefulStopRetriesRejectedAcknowledgementAfterSendFailure(t *testing.T) {
	server := newReleaseLoaderServer()
	client := newReleaseTestClient(t, server)
	attempts := make(chan struct{}, 2)
	client.releases = &failRejectedAckOnceClient{
		ConfigurationReleaseServiceClient: client.releases,
		attempts:                          attempts,
	}
	loader, err := NewReleaseLoader(client, ReleaseLoaderConfig{Name: "runtime"})
	if err != nil {
		t.Fatal(err)
	}

	ns, err := parseNamespace("prod/app")
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	gracefulStop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		loader.watchLoop(ctx, ns, make(chan releaseCandidate, 1), gracefulStop)
		close(done)
	}()

	select {
	case <-server.watchRegs:
	case <-time.After(2 * time.Second):
		t.Fatal("initial release watch did not register")
	}
	loader.ackMu.Lock()
	loader.pendingAck[ReleaseStateRejected] = &kmsv1.ReleaseAcknowledgement{
		Namespace:          ns.proto(),
		Name:               "runtime",
		Version:            7,
		ActivationRevision: 42,
		State:              ReleaseStateRejected,
		RejectionCategory:  ReleaseRejectPrepareFailed,
	}
	loader.dirtyAck[ReleaseStateRejected] = true
	loader.ackMu.Unlock()
	close(gracefulStop)

	select {
	case <-attempts:
	case <-time.After(2 * time.Second):
		t.Fatal("graceful stop did not attempt the pending rejected acknowledgement")
	}
	select {
	case <-server.watchRegs:
	case <-done:
		t.Fatal("graceful stop abandoned the rejected acknowledgement after one send failure")
	case <-time.After(2 * time.Second):
		t.Fatal("graceful stop did not reconnect after the rejected acknowledgement send failure")
	}
	select {
	case <-attempts:
	case <-done:
		t.Fatal("graceful stop ended before retrying the rejected acknowledgement")
	case <-time.After(2 * time.Second):
		t.Fatal("reconnected watch did not retry the rejected acknowledgement")
	}

	var rejected *kmsv1.ReleaseAcknowledgement
	deadline := time.After(2 * time.Second)
	for rejected == nil {
		select {
		case ack := <-server.acks:
			if ack.GetState() == ReleaseStateRejected {
				rejected = ack
			}
		case <-done:
			t.Fatal("graceful stop ended before the server recorded the retried acknowledgement")
		case <-deadline:
			t.Fatal("server did not record the retried rejected acknowledgement")
		}
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("release watch did not finish after the successful retry")
	}
}

func TestReleaseLoaderDoesNotLoseNewerAcknowledgementForSameCandidate(t *testing.T) {
	loader := &ReleaseLoader{
		client:        &Client{clientName: "test-client"},
		pendingAck:    make(map[string]*kmsv1.ReleaseAcknowledgement),
		ackGeneration: make(map[string]uint64),
		dirtyAck:      make(map[string]bool),
		ackSignal:     make(chan struct{}, 1),
	}
	ns := namespaceRef{env: "prod", app: "app"}
	candidate := releaseCandidate{
		release:  &kmsv1.ConfigurationRelease{Name: "runtime", Version: 7},
		revision: 42,
	}
	loader.ack(ns, candidate, ReleaseStateRejected, ReleaseRejectResolutionFailed)
	// Emulate watchSession consuming the signal before it begins sending the
	// snapshotted acknowledgement, leaving room for the racing update signal.
	<-loader.ackSignal

	stream := &blockFirstAcknowledgementStream{
		started: make(chan struct{}),
		allow:   make(chan struct{}),
	}
	firstSend := make(chan error, 1)
	go func() { firstSend <- loader.sendPendingAcks(stream, false) }()
	<-stream.started

	// Reconciliation can reject the same immutable candidate again for a newer
	// reason while the prior acknowledgement is blocked in transport Send.
	loader.ack(ns, candidate, ReleaseStateRejected, ReleaseRejectPrepareFailed)
	close(stream.allow)
	if err := <-firstSend; err != nil {
		t.Fatalf("send first acknowledgement: %v", err)
	}

	// Emulate the watch loop consuming the racing update signal. It must send
	// the newer payload rather than treating the old send as having flushed it.
	<-loader.ackSignal
	if err := loader.sendPendingAcks(stream, false); err != nil {
		t.Fatalf("send newer acknowledgement: %v", err)
	}

	acknowledgements := stream.acknowledgements()
	if len(acknowledgements) != 2 {
		t.Fatalf("sent acknowledgements = %d, want 2", len(acknowledgements))
	}
	if got := acknowledgements[0].GetRejectionCategory(); got != ReleaseRejectResolutionFailed {
		t.Fatalf("first rejection category = %q, want %q", got, ReleaseRejectResolutionFailed)
	}
	if got := acknowledgements[1].GetRejectionCategory(); got != ReleaseRejectPrepareFailed {
		t.Fatalf("newer rejection category = %q, want %q", got, ReleaseRejectPrepareFailed)
	}
}

func TestReleaseLoaderRetriesStillActiveCandidateOnReconciliation(t *testing.T) {
	server := newReleaseLoaderServer()
	firstRelease := testRelease(1, "one")
	server.setActive(firstRelease, 1)
	server.parameters["settings"] = &kmsv1.Parameter{Ref: testResource("settings"), Value: "one", ContentType: "json", Version: 1}
	server.secrets["password"] = &kmsv1.GetSecretResponse{Ref: testResource("password"), Version: 1, Value: []byte("secret-one"), ContentType: "text/plain"}
	client := newReleaseTestClient(t, server)
	loader, err := NewReleaseLoader(client, ReleaseLoaderConfig{
		Name:                "runtime",
		ReconcileInterval:   100 * time.Millisecond,
		SecretTokenProvider: func(string, string) (string, bool) { return "token", true },
	})
	if err != nil {
		t.Fatal(err)
	}

	firstPrepared := &testPreparedRelease{done: make(chan struct{})}
	recoveredPrepared := &testPreparedRelease{done: make(chan struct{})}
	firstFailureReturned := make(chan struct{})
	retryStarted := make(chan struct{})
	allowRecovery := make(chan struct{})
	var secondAttempts atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	go func() {
		runErr <- loader.Run(ctx, func(candidateCtx context.Context, snapshot ReleaseSnapshot) (PreparedRelease, error) {
			if snapshot.Version() == 1 {
				return firstPrepared, nil
			}
			if snapshot.Version() != 2 {
				return nil, fmt.Errorf("unexpected release version %d", snapshot.Version())
			}
			switch secondAttempts.Add(1) {
			case 1:
				close(firstFailureReturned)
				return nil, errors.New("transient local preparation failure")
			case 2:
				close(retryStarted)
				select {
				case <-allowRecovery:
					return recoveredPrepared, nil
				case <-candidateCtx.Done():
					return nil, candidateCtx.Err()
				}
			default:
				return nil, errors.New("unexpected extra preparation attempt")
			}
		})
	}()
	defer func() {
		cancel()
		if err := <-runErr; err != context.Canceled {
			t.Errorf("Run error = %v, want context.Canceled", err)
		}
	}()

	select {
	case <-firstPrepared.done:
	case <-time.After(3 * time.Second):
		t.Fatal("initial release did not commit")
	}
	select {
	case <-server.watchRegs:
	case <-time.After(2 * time.Second):
		t.Fatal("release watch did not register")
	}

	secondRelease := testRelease(2, "two")
	server.mu.Lock()
	server.parameters["settings"] = &kmsv1.Parameter{Ref: testResource("settings"), Value: "two", ContentType: "json", Version: 2}
	server.secrets["password"] = &kmsv1.GetSecretResponse{Ref: testResource("password"), Version: 2, Value: []byte("secret-two"), ContentType: "text/plain"}
	server.mu.Unlock()
	server.setActive(secondRelease, 2)
	activation := &kmsv1.WatchReleaseEvent{
		Event:    &kmsv1.WatchReleaseEvent_Activation{Activation: &kmsv1.ReleaseActivationEvent{Release: secondRelease}},
		Revision: 2,
	}
	server.watchEvents <- activation

	select {
	case <-firstFailureReturned:
	case <-time.After(2 * time.Second):
		t.Fatal("active candidate did not reach the transient failure")
	}
	// Repeated activation delivery must not create an immediate retry loop.
	server.watchEvents <- activation
	server.watchEvents <- activation

	select {
	case <-retryStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("still-active candidate was not retried by reconciliation")
	}
	if got := secondAttempts.Load(); got != 2 {
		t.Fatalf("preparation attempts for release 2 = %d, want 2", got)
	}
	status := loader.Status()
	if status.AppliedVersion != 1 || status.LastFailureCategory != ReleaseRejectPrepareFailed {
		t.Fatalf("last-known-good status during retry = %+v", status)
	}
	if got := loader.Stats().Rejected[ReleaseRejectPrepareFailed]; got != 1 {
		t.Fatalf("prepare-failed rejections = %d, want 1", got)
	}

	close(allowRecovery)
	select {
	case <-recoveredPrepared.done:
	case <-time.After(3 * time.Second):
		t.Fatal("reconciled candidate did not commit after recovery")
	}
	if !eventually(t, 2*time.Second, func() bool {
		status := loader.Status()
		return status.AppliedVersion == 2 && status.AppliedRevision == 2 && status.LastFailureCategory == ""
	}) {
		t.Fatalf("final loader status = %+v", loader.Status())
	}
	if got := secondAttempts.Load(); got != 2 {
		t.Fatalf("final preparation attempts for release 2 = %d, want 2", got)
	}
}

func TestReleaseLoaderValidatesImmutableManifestBeforeResolution(t *testing.T) {
	server := newReleaseLoaderServer()
	release := testRelease(8, `{"enabled":true}`)
	release.SchemaVersion = 3
	release.MetadataJson = `{"owner":"config"}`
	release.Digest, _ = deterministicReleaseDigest(release)
	server.setActive(release, 43)
	server.parameters["settings"] = &kmsv1.Parameter{Ref: testResource("settings"), Value: `{"enabled":true}`, ContentType: "json", Version: 8}
	server.secrets["password"] = &kmsv1.GetSecretResponse{Ref: testResource("password"), Version: 8, Value: []byte("secret"), ContentType: "text/plain"}

	client := newReleaseTestClient(t, server)
	var validated atomic.Bool
	var tokenLookups atomic.Int32
	loader, err := NewReleaseLoader(client, ReleaseLoaderConfig{
		Name: "runtime",
		ValidateManifest: func(ctx context.Context, manifest ReleaseManifest) error {
			if ctx == nil {
				t.Error("manifest validator context is nil")
			}
			if manifest.Namespace() != "prod/app" || manifest.Name() != "runtime" || manifest.Version() != 8 || manifest.ActivationRevision() != 43 {
				t.Errorf("unexpected manifest identity: %s", manifest)
			}
			if manifest.SchemaVersion() != 3 || manifest.Digest() != release.GetDigest() || manifest.MetadataJSON() != `{"owner":"config"}` {
				t.Errorf("unexpected manifest metadata: %s", manifest)
			}
			entries := manifest.Entries()
			if len(entries) != 2 {
				t.Errorf("manifest entries = %v", entries)
			}
			settings, ok := manifest.Entry("settings")
			if !ok || settings.Kind != "parameter" || settings.Path != "/prod/app/settings" || settings.Version != 8 {
				t.Errorf("settings entry = %+v, %t", settings, ok)
			}
			delete(entries, "password")
			settings.Alias = "mutated"
			entries["settings"] = settings
			unchanged, ok := manifest.Entry("settings")
			if !ok || unchanged.Alias != "settings" || len(manifest.Entries()) != 2 {
				t.Error("manifest accessors exposed mutable internal state")
			}
			server.mu.Lock()
			parameterFetches, secretFetches := server.parameterFetches, server.secretFetches
			server.mu.Unlock()
			if parameterFetches != 0 || secretFetches != 0 || tokenLookups.Load() != 0 {
				t.Errorf("resolution ran before validation: parameter=%d secret=%d token=%d", parameterFetches, secretFetches, tokenLookups.Load())
			}
			validated.Store(true)
			return nil
		},
		SecretTokenProvider: func(string, string) (string, bool) {
			if !validated.Load() {
				t.Error("secret token provider ran before manifest validation")
			}
			tokenLookups.Add(1)
			return "token", true
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	prepared := &testPreparedRelease{done: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	go func() {
		runErr <- loader.Run(ctx, func(context.Context, ReleaseSnapshot) (PreparedRelease, error) {
			return prepared, nil
		})
	}()
	select {
	case <-prepared.done:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for release commit")
	}
	server.mu.Lock()
	parameterFetches, secretFetches := server.parameterFetches, server.secretFetches
	server.mu.Unlock()
	if !validated.Load() || parameterFetches != 1 || secretFetches != 1 || tokenLookups.Load() != 1 {
		t.Fatalf("validation/resolution counts = validated:%t parameter:%d secret:%d token:%d", validated.Load(), parameterFetches, secretFetches, tokenLookups.Load())
	}
	cancel()
	if err := <-runErr; err != context.Canceled {
		t.Fatalf("Run error = %v, want context.Canceled", err)
	}
}

func TestReleaseLoaderChecksBasicEntriesBeforeManifestValidator(t *testing.T) {
	server := newReleaseLoaderServer()
	release := testRelease(9, "settings")
	release.Entries[1].Kind = "unsupported"
	release.Digest, _ = deterministicReleaseDigest(release)
	server.setActive(release, 44)
	client := newReleaseTestClient(t, server)
	var validatorCalled atomic.Bool
	var tokenLookups atomic.Int32
	loader, err := NewReleaseLoader(client, ReleaseLoaderConfig{
		Name: "runtime",
		ValidateManifest: func(context.Context, ReleaseManifest) error {
			validatorCalled.Store(true)
			return nil
		},
		SecretTokenProvider: func(string, string) (string, bool) {
			tokenLookups.Add(1)
			return "token", true
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	err = loader.Run(context.Background(), func(context.Context, ReleaseSnapshot) (PreparedRelease, error) {
		t.Error("prepare ran for a malformed release entry")
		return nil, nil
	})
	if err == nil || err.Error() != "kmsclient: configuration release candidate rejected (resolution_failed)" {
		t.Fatalf("Run error = %v", err)
	}
	server.mu.Lock()
	parameterFetches, secretFetches := server.parameterFetches, server.secretFetches
	server.mu.Unlock()
	if validatorCalled.Load() || parameterFetches != 0 || secretFetches != 0 || tokenLookups.Load() != 0 {
		t.Fatalf("invalid entries reached validation/resolution: validator=%t parameter=%d secret=%d token=%d", validatorCalled.Load(), parameterFetches, secretFetches, tokenLookups.Load())
	}
}

func TestReleaseLoaderClassifiedManifestFailurePreventsResolutionAndRedactsCause(t *testing.T) {
	server := newReleaseLoaderServer()
	release := testRelease(9, "settings")
	server.setActive(release, 44)
	client := newReleaseTestClient(t, server)
	classified := &testReleaseRejectionError{
		category: ReleaseRejectConfigContractMismatch,
		detail:   "contract mismatch with sensitive local detail",
	}
	var tokenLookups atomic.Int32
	var prepareCalled atomic.Bool
	loader, err := NewReleaseLoader(client, ReleaseLoaderConfig{
		Name: "runtime",
		ValidateManifest: func(context.Context, ReleaseManifest) error {
			return classified
		},
		SecretTokenProvider: func(string, string) (string, bool) {
			tokenLookups.Add(1)
			return "token", true
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	err = loader.Run(context.Background(), func(context.Context, ReleaseSnapshot) (PreparedRelease, error) {
		prepareCalled.Store(true)
		return nil, nil
	})
	if err == nil {
		t.Fatal("Run unexpectedly succeeded")
	}
	wantText := "kmsclient: configuration release candidate rejected (config_contract_mismatch)"
	for _, rendered := range []string{err.Error(), fmt.Sprintf("%v", err), fmt.Sprintf("%+v", err), fmt.Sprintf("%#v", err)} {
		if rendered != wantText || strings.Contains(rendered, classified.detail) {
			t.Errorf("candidate error was not fixed/redacted: %q", rendered)
		}
	}
	var recovered *testReleaseRejectionError
	if errors.As(err, &recovered) || errors.Is(err, classified) {
		t.Fatalf("classified manifest cause escaped loader boundary: %#v", recovered)
	}
	server.mu.Lock()
	parameterFetches, secretFetches := server.parameterFetches, server.secretFetches
	server.mu.Unlock()
	if parameterFetches != 0 || secretFetches != 0 || tokenLookups.Load() != 0 || prepareCalled.Load() {
		t.Fatalf("rejected manifest triggered resolution/preparation: parameter=%d secret=%d token=%d prepare=%t", parameterFetches, secretFetches, tokenLookups.Load(), prepareCalled.Load())
	}
	if status := loader.Status(); status.LastFailureCategory != ReleaseRejectConfigContractMismatch {
		t.Fatalf("status = %+v", status)
	}
}

func TestReleaseLoaderClassifiesOnlyAllowedPreparationErrors(t *testing.T) {
	tests := []struct {
		name         string
		cause        error
		wantCategory string
	}{
		{
			name:         "classified default mismatch",
			cause:        &testReleaseRejectionError{category: ReleaseRejectDefaultMismatch, detail: "default mismatch sensitive detail"},
			wantCategory: ReleaseRejectDefaultMismatch,
		},
		{
			name:         "ordinary error",
			cause:        errors.New("ordinary preparation error with secret plaintext"),
			wantCategory: ReleaseRejectPrepareFailed,
		},
		{
			name:         "unbounded category",
			cause:        &testReleaseRejectionError{category: "field_name_from_user", detail: "invalid category sensitive detail"},
			wantCategory: ReleaseRejectPrepareFailed,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := newReleaseLoaderServer()
			release := testRelease(10, "settings")
			server.setActive(release, 45)
			server.parameters["settings"] = &kmsv1.Parameter{Ref: testResource("settings"), Value: "settings", ContentType: "json", Version: 10}
			server.secrets["password"] = &kmsv1.GetSecretResponse{Ref: testResource("password"), Version: 10, Value: []byte("secret"), ContentType: "text/plain"}
			client := newReleaseTestClient(t, server)
			loader, err := NewReleaseLoader(client, ReleaseLoaderConfig{
				Name:                "runtime",
				SecretTokenProvider: func(string, string) (string, bool) { return "token", true },
			})
			if err != nil {
				t.Fatal(err)
			}
			err = loader.Run(context.Background(), func(context.Context, ReleaseSnapshot) (PreparedRelease, error) {
				return nil, tt.cause
			})
			if err == nil {
				t.Fatal("Run unexpectedly succeeded")
			}
			wantText := fmt.Sprintf("kmsclient: configuration release candidate rejected (%s)", tt.wantCategory)
			for _, rendered := range []string{err.Error(), fmt.Sprintf("%+v", err), fmt.Sprintf("%#v", err)} {
				if rendered != wantText || strings.Contains(rendered, tt.cause.Error()) {
					t.Errorf("candidate error was not fixed/redacted: %q", rendered)
				}
			}
			if errors.Is(err, tt.cause) {
				t.Fatal("preparation cause was exposed through Unwrap")
			}
			if status := loader.Status(); status.LastFailureCategory != tt.wantCategory {
				t.Fatalf("status = %+v", status)
			}
		})
	}
}

func TestReleaseLoaderAbortsSupersededPreparedCandidateExactlyOnce(t *testing.T) {
	server := newReleaseLoaderServer()
	firstRelease := testRelease(1, "one")
	server.setActive(firstRelease, 1)
	server.parameters["settings"] = &kmsv1.Parameter{Ref: testResource("settings"), Value: "one", ContentType: "json", Version: 1}
	server.secrets["password"] = &kmsv1.GetSecretResponse{Ref: testResource("password"), Version: 1, Value: []byte("secret-one"), ContentType: "text/plain"}
	client := newReleaseTestClient(t, server)
	loader, err := NewReleaseLoader(client, ReleaseLoaderConfig{Name: "runtime", SecretTokenProvider: func(string, string) (string, bool) { return "token", true }})
	if err != nil {
		t.Fatal(err)
	}
	stalePrepared := &testPreparedRelease{done: make(chan struct{})}
	currentPrepared := &testPreparedRelease{done: make(chan struct{})}
	stalePreparing := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	go func() {
		runErr <- loader.Run(ctx, func(candidateCtx context.Context, snapshot ReleaseSnapshot) (PreparedRelease, error) {
			if snapshot.Version() == 1 {
				close(stalePreparing)
				<-candidateCtx.Done()
				return stalePrepared, nil
			}
			return currentPrepared, nil
		})
	}()
	select {
	case <-stalePreparing:
	case <-time.After(2 * time.Second):
		t.Fatal("initial candidate did not reach prepare")
	}
	select {
	case <-server.watchRegs:
	case <-time.After(2 * time.Second):
		t.Fatal("release watch did not register")
	}
	secondRelease := testRelease(2, "two")
	server.mu.Lock()
	server.parameters["settings"] = &kmsv1.Parameter{Ref: testResource("settings"), Value: "two", ContentType: "json", Version: 2}
	server.secrets["password"] = &kmsv1.GetSecretResponse{Ref: testResource("password"), Version: 2, Value: []byte("secret-two"), ContentType: "text/plain"}
	server.mu.Unlock()
	server.setActive(secondRelease, 2)
	server.watchEvents <- &kmsv1.WatchReleaseEvent{
		Event:    &kmsv1.WatchReleaseEvent_Activation{Activation: &kmsv1.ReleaseActivationEvent{Release: secondRelease}},
		Revision: 2,
	}
	select {
	case <-currentPrepared.done:
	case <-time.After(3 * time.Second):
		t.Fatal("new candidate did not commit")
	}
	if stalePrepared.commits.Load() != 0 || stalePrepared.aborts.Load() != 1 {
		t.Fatalf("stale commit/abort = %d/%d, want 0/1", stalePrepared.commits.Load(), stalePrepared.aborts.Load())
	}
	if currentPrepared.commits.Load() != 1 || currentPrepared.aborts.Load() != 0 {
		t.Fatalf("current commit/abort = %d/%d, want 1/0", currentPrepared.commits.Load(), currentPrepared.aborts.Load())
	}
	cancel()
	if err := <-runErr; err != context.Canceled {
		t.Fatalf("Run error = %v", err)
	}
}

func TestReleaseLoaderBoundsNonCooperativePreparationAndKeepsLatest(t *testing.T) {
	server := newReleaseLoaderServer()
	firstRelease := testRelease(1, "one")
	server.setActive(firstRelease, 1)
	server.parameters["settings"] = &kmsv1.Parameter{Ref: testResource("settings"), Value: "one", ContentType: "json", Version: 1}
	server.secrets["password"] = &kmsv1.GetSecretResponse{Ref: testResource("password"), Version: 1, Value: []byte("secret-one"), ContentType: "text/plain"}
	client := newReleaseTestClient(t, server)
	loader, err := NewReleaseLoader(client, ReleaseLoaderConfig{Name: "runtime", SecretTokenProvider: func(string, string) (string, bool) { return "token", true }})
	if err != nil {
		t.Fatal(err)
	}

	releaseFirst := make(chan struct{})
	firstStarted := make(chan struct{})
	preparedLatest := &testPreparedRelease{done: make(chan struct{})}
	var activeCallbacks atomic.Int32
	var maxCallbacks atomic.Int32
	var versionsMu sync.Mutex
	var preparedVersions []uint64
	ctx, cancel := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	go func() {
		runErr <- loader.Run(ctx, func(_ context.Context, snapshot ReleaseSnapshot) (PreparedRelease, error) {
			active := activeCallbacks.Add(1)
			defer activeCallbacks.Add(-1)
			for {
				previous := maxCallbacks.Load()
				if active <= previous || maxCallbacks.CompareAndSwap(previous, active) {
					break
				}
			}
			versionsMu.Lock()
			preparedVersions = append(preparedVersions, snapshot.Version())
			versionsMu.Unlock()
			if snapshot.Version() == 1 {
				close(firstStarted)
				<-releaseFirst // Deliberately ignore cancellation.
				return &testPreparedRelease{done: make(chan struct{})}, nil
			}
			return preparedLatest, nil
		})
	}()
	select {
	case <-firstStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("initial candidate did not reach prepare")
	}
	select {
	case <-server.watchRegs:
	case <-time.After(2 * time.Second):
		t.Fatal("release watch did not register")
	}
	for _, activation := range []struct {
		version uint64
		value   string
	}{{2, "two"}, {3, "three"}} {
		version, value := activation.version, activation.value
		release := testRelease(version, value)
		server.mu.Lock()
		server.parameters["settings"] = &kmsv1.Parameter{Ref: testResource("settings"), Value: value, ContentType: "json", Version: version}
		server.secrets["password"] = &kmsv1.GetSecretResponse{Ref: testResource("password"), Version: version, Value: []byte("secret-" + value), ContentType: "text/plain"}
		server.mu.Unlock()
		server.setActive(release, version)
		server.watchEvents <- &kmsv1.WatchReleaseEvent{Event: &kmsv1.WatchReleaseEvent_Activation{Activation: &kmsv1.ReleaseActivationEvent{Release: release}}, Revision: version}
	}
	time.Sleep(100 * time.Millisecond)
	if got := maxCallbacks.Load(); got != 1 {
		t.Fatalf("concurrent prepare callbacks = %d, want 1", got)
	}
	close(releaseFirst)
	select {
	case <-preparedLatest.done:
	case <-time.After(3 * time.Second):
		t.Fatal("latest candidate did not commit")
	}
	versionsMu.Lock()
	gotVersions := append([]uint64(nil), preparedVersions...)
	versionsMu.Unlock()
	if fmt.Sprint(gotVersions) != "[1 3]" {
		t.Fatalf("prepared versions = %v, want only initial and latest", gotVersions)
	}
	cancel()
	if err := <-runErr; err != context.Canceled {
		t.Fatalf("Run error = %v, want context.Canceled", err)
	}
}

func TestReleaseLoaderFailsStartupOnDigestMismatch(t *testing.T) {
	server := newReleaseLoaderServer()
	release := testRelease(3, "expected")
	server.setActive(release, 9)
	server.parameters["settings"] = &kmsv1.Parameter{Ref: testResource("settings"), Value: "tampered", ContentType: "json", Version: 3}
	server.secrets["password"] = &kmsv1.GetSecretResponse{Ref: testResource("password"), Version: 3, Value: []byte("secret"), ContentType: "text/plain"}
	client := newReleaseTestClient(t, server)
	loader, err := NewReleaseLoader(client, ReleaseLoaderConfig{Name: "runtime", SecretTokenProvider: func(string, string) (string, bool) { return "token", true }})
	if err != nil {
		t.Fatal(err)
	}
	err = loader.Run(context.Background(), func(context.Context, ReleaseSnapshot) (PreparedRelease, error) {
		t.Fatal("prepare must not run for a digest mismatch")
		return nil, nil
	})
	if err == nil || !strings.Contains(err.Error(), ReleaseRejectDigestMismatch) {
		t.Fatalf("Run error = %v", err)
	}
	status := loader.Status()
	if status.AppliedVersion != 0 || status.LastFailureCategory != ReleaseRejectDigestMismatch {
		t.Fatalf("status = %+v", status)
	}
}

func TestReleaseLoaderFailsStartupOnReleaseProjectionDigestMismatch(t *testing.T) {
	server := newReleaseLoaderServer()
	release := testRelease(3, "expected")
	release.Digest = strings.Repeat("0", 64)
	server.setActive(release, 9)
	client := newReleaseTestClient(t, server)
	loader, err := NewReleaseLoader(client, ReleaseLoaderConfig{Name: "runtime"})
	if err != nil {
		t.Fatal(err)
	}
	prepared := atomic.Bool{}
	err = loader.Run(context.Background(), func(context.Context, ReleaseSnapshot) (PreparedRelease, error) {
		prepared.Store(true)
		return nil, nil
	})
	if err == nil || !strings.Contains(err.Error(), ReleaseRejectDigestMismatch) {
		t.Fatalf("Run error = %v, want %s", err, ReleaseRejectDigestMismatch)
	}
	if prepared.Load() {
		t.Fatal("prepare must not run for a release projection digest mismatch")
	}
}

func TestReleaseLoaderRejectsReturnedResourceReferenceMismatch(t *testing.T) {
	tests := []struct {
		name           string
		mismatchSecret bool
	}{
		{name: "parameter"},
		{name: "secret", mismatchSecret: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := newReleaseLoaderServer()
			release := testRelease(4, "settings-value")
			server.setActive(release, 11)
			parameterRef := testResource("settings")
			secretRef := testResource("password")
			if tt.mismatchSecret {
				secretRef = testResource("different-secret")
			} else {
				parameterRef = testResource("different-parameter")
			}
			server.parameters["settings"] = &kmsv1.Parameter{Ref: parameterRef, Value: "settings-value", ContentType: "json", Version: 4}
			server.secrets["password"] = &kmsv1.GetSecretResponse{Ref: secretRef, Version: 4, Value: []byte("secret"), ContentType: "text/plain"}
			client := newReleaseTestClient(t, server)
			loader, err := NewReleaseLoader(client, ReleaseLoaderConfig{Name: "runtime", SecretTokenProvider: func(string, string) (string, bool) { return "token", true }})
			if err != nil {
				t.Fatal(err)
			}
			err = loader.Run(context.Background(), func(context.Context, ReleaseSnapshot) (PreparedRelease, error) {
				t.Fatal("prepare must not run for a returned-ref mismatch")
				return nil, nil
			})
			if err == nil || !strings.Contains(err.Error(), ReleaseRejectVersionMismatch) {
				t.Fatalf("Run error = %v", err)
			}
		})
	}
}

// divergentPreparedRelease is a PreparedRelease that also reports divergence
// from source defaults, as configstore's managed candidate does.
type divergentPreparedRelease struct {
	testPreparedRelease
	divergent bool
	count     int
	panics    bool
}

func (p *divergentPreparedRelease) ReleaseDivergence() (bool, int) {
	if p.panics {
		panic("divergence reporter panic")
	}
	return p.divergent, p.count
}

func collectLifecycleAcks(t *testing.T, server *releaseLoaderServer) map[string]*kmsv1.ReleaseAcknowledgement {
	t.Helper()
	acks := make(map[string]*kmsv1.ReleaseAcknowledgement)
	deadline := time.After(3 * time.Second)
	for len(acks) < 3 {
		select {
		case ack := <-server.acks:
			acks[ack.GetState()] = ack
		case <-deadline:
			t.Fatalf("acks = %v, want received/prepared/applied", acks)
		}
	}
	return acks
}

func newDivergenceTestLoader(t *testing.T, version uint64, revision uint64) (*releaseLoaderServer, *ReleaseLoader) {
	t.Helper()
	server := newReleaseLoaderServer()
	release := testRelease(version, `{"enabled":true}`)
	server.setActive(release, revision)
	server.parameters["settings"] = &kmsv1.Parameter{Ref: testResource("settings"), Value: `{"enabled":true}`, ContentType: "json", Version: version}
	server.secrets["password"] = &kmsv1.GetSecretResponse{Ref: testResource("password"), Version: version, Value: []byte("secret"), ContentType: "text/plain"}
	client := newReleaseTestClient(t, server)
	loader, err := NewReleaseLoader(client, ReleaseLoaderConfig{
		Name:                "runtime",
		SecretTokenProvider: func(string, string) (string, bool) { return "token", true },
	})
	if err != nil {
		t.Fatal(err)
	}
	return server, loader
}

func TestReleaseLoaderAppliedAckCarriesDivergenceFromReporter(t *testing.T) {
	tests := []struct {
		name      string
		prepared  *divergentPreparedRelease
		wantFlag  bool
		wantCount uint32
	}{
		{name: "divergent", prepared: &divergentPreparedRelease{divergent: true, count: 3}, wantFlag: true, wantCount: 3},
		{name: "divergent count is capped", prepared: &divergentPreparedRelease{divergent: true, count: 1 << 20}, wantFlag: true, wantCount: 65535},
		{name: "divergent negative count", prepared: &divergentPreparedRelease{divergent: true, count: -1}, wantFlag: true, wantCount: 0},
		{name: "not divergent ignores count", prepared: &divergentPreparedRelease{divergent: false, count: 9}},
		{name: "panicking reporter is not divergent", prepared: &divergentPreparedRelease{divergent: true, count: 2, panics: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, loader := newDivergenceTestLoader(t, 3, 30)
			tt.prepared.done = make(chan struct{})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			runErr := make(chan error, 1)
			go func() {
				runErr <- loader.Run(ctx, func(context.Context, ReleaseSnapshot) (PreparedRelease, error) {
					return tt.prepared, nil
				})
			}()
			select {
			case <-tt.prepared.done:
			case <-time.After(3 * time.Second):
				t.Fatal("timed out waiting for release commit")
			}
			acks := collectLifecycleAcks(t, server)
			for _, state := range []string{ReleaseStateReceived, ReleaseStatePrepared} {
				if acks[state].GetAppliedDivergent() || acks[state].GetDivergentFieldCount() != 0 {
					t.Fatalf("%s ack carried divergence: %+v", state, acks[state])
				}
			}
			applied := acks[ReleaseStateApplied]
			if applied.GetAppliedDivergent() != tt.wantFlag || applied.GetDivergentFieldCount() != tt.wantCount {
				t.Fatalf("applied ack divergence = (%v, %d), want (%v, %d)",
					applied.GetAppliedDivergent(), applied.GetDivergentFieldCount(), tt.wantFlag, tt.wantCount)
			}
			if applied.GetDiagnostic() != "" || applied.GetActivationRevision() != 30 {
				t.Fatalf("applied ack = %+v", applied)
			}
			if status := loader.Status(); status.AppliedVersion != 3 || status.LastFailureCategory != "" {
				t.Fatalf("reporter changed loader outcome: %+v", status)
			}
			cancel()
			select {
			case <-runErr:
			case <-time.After(3 * time.Second):
				t.Fatal("loader did not stop")
			}
		})
	}
}

func TestReleaseLoaderPlainPreparedReleaseAckHasNoDivergence(t *testing.T) {
	server, loader := newDivergenceTestLoader(t, 4, 40)
	prepared := &testPreparedRelease{done: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = loader.Run(ctx, func(context.Context, ReleaseSnapshot) (PreparedRelease, error) { return prepared, nil })
	}()
	select {
	case <-prepared.done:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for release commit")
	}
	acks := collectLifecycleAcks(t, server)
	for state, ack := range acks {
		if ack.GetAppliedDivergent() || ack.GetDivergentFieldCount() != 0 {
			t.Fatalf("%s ack carried divergence without a reporter: %+v", state, ack)
		}
	}
}

func TestReleaseLoaderRejectedAckNeverCarriesDivergence(t *testing.T) {
	server, loader := newDivergenceTestLoader(t, 5, 50)
	err := loader.Run(context.Background(), func(context.Context, ReleaseSnapshot) (PreparedRelease, error) {
		return nil, &testReleaseRejectionError{category: ReleaseRejectDefaultMismatch, detail: "detail"}
	})
	if err == nil {
		t.Fatal("Run unexpectedly succeeded")
	}
	deadline := time.After(3 * time.Second)
	sawRejected := false
	for !sawRejected {
		select {
		case ack := <-server.acks:
			if ack.GetAppliedDivergent() || ack.GetDivergentFieldCount() != 0 {
				t.Fatalf("%s ack carried divergence: %+v", ack.GetState(), ack)
			}
			sawRejected = ack.GetState() == ReleaseStateRejected
		case <-deadline:
			t.Fatal("timed out waiting for rejected ack")
		}
	}
}
