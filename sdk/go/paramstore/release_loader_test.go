package paramstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
)

type releaseLoaderServer struct {
	kmsv1.UnimplementedParameterServiceServer
	kmsv1.UnimplementedSecretServiceServer
	kmsv1.UnimplementedConfigurationReleaseServiceServer

	mu          sync.Mutex
	active      *kmsv1.GetActiveReleaseResponse
	parameters  map[string]*kmsv1.Parameter
	secrets     map[string]*kmsv1.GetSecretResponse
	secretToken string

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
	p := s.parameters[req.GetRef().GetKey()]
	if p == nil {
		return nil, status.Error(5, "not found")
	}
	return &kmsv1.GetParameterResponse{Parameter: p}, nil
}

func (s *releaseLoaderServer) GetSecret(ctx context.Context, req *kmsv1.GetSecretRequest) (*kmsv1.GetSecretResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
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
	recv := make(chan *kmsv1.WatchReleaseRequest, 1)
	recvErr := make(chan error, 1)
	go func() {
		for {
			request, recvError := stream.Recv()
			if recvError != nil {
				recvErr <- recvError
				return
			}
			recv <- request
		}
	}()
	for {
		select {
		case request := <-recv:
			if ack := request.GetAcknowledgement(); ack != nil {
				s.acks <- ack
			}
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

func testRelease(version, revision uint64, parameterValue string) *kmsv1.ConfigurationRelease {
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

func TestReleaseLoaderResolvesRedactsCommitsAndAcknowledges(t *testing.T) {
	server := newReleaseLoaderServer()
	release := testRelease(7, 42, `{"enabled":true}`)
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

func TestReleaseLoaderAbortsSupersededPreparedCandidateExactlyOnce(t *testing.T) {
	server := newReleaseLoaderServer()
	firstRelease := testRelease(1, 1, "one")
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
	secondRelease := testRelease(2, 2, "two")
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
	firstRelease := testRelease(1, 1, "one")
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
		release := testRelease(version, version, value)
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
	release := testRelease(3, 9, "expected")
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
	release := testRelease(3, 9, "expected")
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
			release := testRelease(4, 11, "settings-value")
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
