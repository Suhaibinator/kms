// Package paramstoretest provides an in-process, scriptable fake of the KMS
// gRPC services, backed by bufconn. It lets SDK consumers (and the SDK's own
// tests) exercise Client behaviour end to end without a real server: script
// parameter/secret values, inject errors, drive the Subscribe stream (snapshots,
// changes, heartbeats), and forcibly drop streams to test reconnect.
package paramstoretest

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

func notFound(path string) error {
	return status.Errorf(codes.NotFound, "no such resource: %s", path)
}

// Server is a fake KMS server. Create one with New and stop it with Close.
type Server struct {
	kmsv1.UnimplementedParameterServiceServer
	kmsv1.UnimplementedSecretServiceServer
	kmsv1.UnimplementedWatchServiceServer

	lis  *bufconn.Listener
	grpc *grpc.Server

	mu           sync.Mutex
	params       map[string]*kmsv1.Parameter
	secrets      map[string][]byte
	secretMeta   map[string]*kmsv1.GetSecretResponse
	revision     uint64
	paramErr     map[string]error
	secretErr    map[string]error
	lastMetadata map[string]metadata.MD // method -> incoming md
	putSecrets   []PutSecretCall
	getParamHook func(path string)

	subMu     sync.Mutex
	subs      []*Subscription
	subNotify chan *Subscription
}

// PutSecretCall records a PutSecret invocation for assertions.
type PutSecretCall struct {
	Path                string
	Value               []byte
	ClientBound         bool
	GenerateAccessToken bool
}

// New starts a fake server on an in-memory bufconn listener.
func New() (*Server, error) {
	s := &Server{
		lis:          bufconn.Listen(1 << 20),
		params:       make(map[string]*kmsv1.Parameter),
		secrets:      make(map[string][]byte),
		secretMeta:   make(map[string]*kmsv1.GetSecretResponse),
		paramErr:     make(map[string]error),
		secretErr:    make(map[string]error),
		lastMetadata: make(map[string]metadata.MD),
		subNotify:    make(chan *Subscription, 16),
	}
	s.grpc = grpc.NewServer()
	kmsv1.RegisterParameterServiceServer(s.grpc, s)
	kmsv1.RegisterSecretServiceServer(s.grpc, s)
	kmsv1.RegisterWatchServiceServer(s.grpc, s)
	go func() { _ = s.grpc.Serve(s.lis) }()
	return s, nil
}

// Close stops the server.
func (s *Server) Close() { s.grpc.Stop() }

// Target is the gRPC target to dial; pair it with DialOptions.
func (s *Server) Target() string { return "passthrough:///bufnet" }

// DialOptions returns grpc dial options wiring a client to this server.
func (s *Server) DialOptions() []grpc.DialOption {
	return []grpc.DialOption{
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return s.lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	}
}

// --- scripting API ---------------------------------------------------------

// SetParameter stores a parameter value and bumps the revision.
func (s *Server) SetParameter(path, value string) uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.revision++
	s.params[path] = &kmsv1.Parameter{
		Path:    path,
		Value:   value,
		Version: s.revision,
	}
	return s.revision
}

// RemoveParameter removes a parameter and bumps the revision.
func (s *Server) RemoveParameter(path string) uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.revision++
	delete(s.params, path)
	return s.revision
}

// SetSecret stores secret plaintext.
func (s *Server) SetSecret(path string, value []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.revision++
	s.secrets[path] = value
	s.secretMeta[path] = &kmsv1.GetSecretResponse{
		Path:    path,
		Version: s.revision,
		Value:   value,
	}
}

// SetParameterError makes GetParameter for path return err.
func (s *Server) SetParameterError(path string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.paramErr[path] = err
}

// SetGetParameterHook installs fn to run at the start of every GetParameter,
// before the value is read, with the requested path. It lets a test inject a
// concurrent event mid-fetch to exercise reconcile/stream races. Pass nil to
// clear. The hook runs outside the server lock.
func (s *Server) SetGetParameterHook(fn func(path string)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.getParamHook = fn
}

// SetSecretError makes GetSecret for path return err.
func (s *Server) SetSecretError(path string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.secretErr[path] = err
}

// LastMetadata returns the incoming gRPC metadata seen by the most recent call
// to the named method (e.g. "GetSecret", "GetParameter", "Subscribe").
func (s *Server) LastMetadata(method string) metadata.MD {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastMetadata[method]
}

// PutSecretCalls returns a copy of recorded PutSecret invocations.
func (s *Server) PutSecretCalls() []PutSecretCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]PutSecretCall, len(s.putSecrets))
	copy(out, s.putSecrets)
	return out
}

// Revision returns the current global revision.
func (s *Server) Revision() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.revision
}

func (s *Server) recordMD(ctx context.Context, method string) {
	md, _ := metadata.FromIncomingContext(ctx)
	s.mu.Lock()
	s.lastMetadata[method] = md
	s.mu.Unlock()
}

// --- ParameterService ------------------------------------------------------

func (s *Server) GetParameter(ctx context.Context, req *kmsv1.GetParameterRequest) (*kmsv1.GetParameterResponse, error) {
	s.recordMD(ctx, "GetParameter")
	s.mu.Lock()
	hook := s.getParamHook
	s.mu.Unlock()
	if hook != nil {
		hook(req.GetPath())
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.paramErr[req.GetPath()]; err != nil {
		return nil, err
	}
	p, ok := s.params[req.GetPath()]
	if !ok {
		return nil, notFound(req.GetPath())
	}
	return &kmsv1.GetParameterResponse{Parameter: p}, nil
}

func (s *Server) PutParameter(ctx context.Context, req *kmsv1.PutParameterRequest) (*kmsv1.PutParameterResponse, error) {
	s.recordMD(ctx, "PutParameter")
	rev := s.SetParameter(req.GetPath(), req.GetValue())
	return &kmsv1.PutParameterResponse{Version: rev, Revision: rev}, nil
}

func (s *Server) ListParameters(ctx context.Context, req *kmsv1.ListParametersRequest) (*kmsv1.ListParametersResponse, error) {
	s.recordMD(ctx, "ListParameters")
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*kmsv1.Parameter
	for path, p := range s.params {
		if req.GetPathPrefix() == "" || hasPrefix(path, req.GetPathPrefix()) {
			out = append(out, p)
		}
	}
	return &kmsv1.ListParametersResponse{Parameters: out}, nil
}

// --- SecretService ---------------------------------------------------------

func (s *Server) GetSecret(ctx context.Context, req *kmsv1.GetSecretRequest) (*kmsv1.GetSecretResponse, error) {
	s.recordMD(ctx, "GetSecret")
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.secretErr[req.GetPath()]; err != nil {
		return nil, err
	}
	meta, ok := s.secretMeta[req.GetPath()]
	if !ok {
		return nil, notFound(req.GetPath())
	}
	return meta, nil
}

func (s *Server) PutSecret(ctx context.Context, req *kmsv1.PutSecretRequest) (*kmsv1.PutSecretResponse, error) {
	s.recordMD(ctx, "PutSecret")
	s.SetSecret(req.GetPath(), req.GetValue())
	s.mu.Lock()
	s.putSecrets = append(s.putSecrets, PutSecretCall{
		Path:                req.GetPath(),
		Value:               req.GetValue(),
		ClientBound:         req.GetClientBound(),
		GenerateAccessToken: req.GetGenerateAccessToken(),
	})
	rev := s.revision
	s.mu.Unlock()
	resp := &kmsv1.PutSecretResponse{Version: rev, Revision: rev}
	if req.GetGenerateAccessToken() {
		resp.AccessToken = "minted-token-for-" + req.GetPath()
	}
	return resp, nil
}

// --- WatchService ----------------------------------------------------------

// Subscribe registers the first request as a Subscription and then relays
// events the test pushes into it until the test closes it or the client
// disconnects.
func (s *Server) Subscribe(stream kmsv1.WatchService_SubscribeServer) error {
	s.recordMD(stream.Context(), "Subscribe")
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	md, _ := metadata.FromIncomingContext(stream.Context())
	sub := &Subscription{
		ClientName:       first.GetClientName(),
		Paths:            first.GetPaths(),
		LastSeenRevision: first.GetLastSeenRevision(),
		Metadata:         md,
		send:             make(chan *kmsv1.SubscribeEvent, 64),
		acks:             make(chan uint64, 64),
		closeCh:          make(chan struct{}),
		recvDone:         make(chan struct{}),
	}
	s.registerSub(sub)
	defer s.unregisterSub(sub)

	// Reader goroutine: capture heartbeat acks.
	go func() {
		defer close(sub.recvDone)
		for {
			m, err := stream.Recv()
			if err != nil {
				return
			}
			select {
			case sub.acks <- m.GetAckedRevision():
			default:
			}
		}
	}()

	for {
		select {
		case ev := <-sub.send:
			if err := stream.Send(ev); err != nil {
				return err
			}
		case <-sub.closeCh:
			return fmt.Errorf("subscription closed by test")
		case <-stream.Context().Done():
			return stream.Context().Err()
		}
	}
}

func (s *Server) registerSub(sub *Subscription) {
	s.subMu.Lock()
	s.subs = append(s.subs, sub)
	s.subMu.Unlock()
	select {
	case s.subNotify <- sub:
	default:
	}
}

func (s *Server) unregisterSub(sub *Subscription) {
	s.subMu.Lock()
	defer s.subMu.Unlock()
	for i, x := range s.subs {
		if x == sub {
			s.subs = append(s.subs[:i], s.subs[i+1:]...)
			break
		}
	}
}

// SubscribeCount returns the number of currently open subscriptions.
func (s *Server) SubscribeCount() int {
	s.subMu.Lock()
	defer s.subMu.Unlock()
	return len(s.subs)
}

// WaitForSubscribe blocks until a new Subscribe stream registers (or timeout).
func (s *Server) WaitForSubscribe(timeout time.Duration) (*Subscription, error) {
	select {
	case sub := <-s.subNotify:
		return sub, nil
	case <-time.After(timeout):
		return nil, fmt.Errorf("timed out waiting for Subscribe")
	}
}

// Subscription is a handle to one open Subscribe stream that a test can drive.
type Subscription struct {
	ClientName       string
	Paths            []string
	LastSeenRevision uint64
	Metadata         metadata.MD

	send     chan *kmsv1.SubscribeEvent
	acks     chan uint64
	closeCh  chan struct{}
	recvDone chan struct{}
	closed   sync.Once
}

// PushSnapshot sends a snapshot event carrying the given parameters.
func (sub *Subscription) PushSnapshot(revision uint64, params ...*kmsv1.Parameter) {
	sub.send <- &kmsv1.SubscribeEvent{
		Event:    &kmsv1.SubscribeEvent_Snapshot{Snapshot: &kmsv1.Snapshot{Parameters: params}},
		Revision: revision,
	}
}

// PushChange sends a parameter change event.
func (sub *Subscription) PushChange(revision uint64, path, changeType, value string, version uint64) {
	sub.send <- &kmsv1.SubscribeEvent{
		Event: &kmsv1.SubscribeEvent_Change{Change: &kmsv1.ParameterChange{
			Path:       path,
			ChangeType: changeType,
			Value:      value,
			Version:    version,
		}},
		Revision: revision,
	}
}

// PushSecretChange sends a secret metadata change event.
func (sub *Subscription) PushSecretChange(revision uint64, path, changeType string, version uint64) {
	sub.send <- &kmsv1.SubscribeEvent{
		Event: &kmsv1.SubscribeEvent_SecretChange{SecretChange: &kmsv1.SecretMetadataChange{
			Path:       path,
			ChangeType: changeType,
			Version:    version,
		}},
		Revision: revision,
	}
}

// SendHeartbeat sends a heartbeat event carrying the current revision.
func (sub *Subscription) SendHeartbeat(revision uint64) {
	sub.send <- &kmsv1.SubscribeEvent{
		Event:    &kmsv1.SubscribeEvent_Heartbeat{Heartbeat: &kmsv1.Heartbeat{ServerTimeUnixMs: time.Now().UnixMilli()}},
		Revision: revision,
	}
}

// WaitAck blocks until the client sends a heartbeat ack (or timeout) and returns
// the acked revision.
func (sub *Subscription) WaitAck(timeout time.Duration) (uint64, error) {
	select {
	case rev := <-sub.acks:
		return rev, nil
	case <-time.After(timeout):
		return 0, fmt.Errorf("timed out waiting for ack")
	}
}

// Kill forcibly terminates this subscription's stream to simulate a dropped
// connection, exercising client reconnect/resume.
func (sub *Subscription) Kill() {
	sub.closed.Do(func() { close(sub.closeCh) })
}

// Helpers ------------------------------------------------------------------

func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}
