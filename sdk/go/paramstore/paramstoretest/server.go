// Package paramstoretest provides an in-process, scriptable fake of the KMS
// gRPC services, backed by bufconn. It lets SDK consumers (and the SDK's own
// tests) exercise Client behaviour end to end without a real server: script
// parameter/secret values by namespace + relative key (or by display path),
// inject errors, set the WhoAmI identity, drive the Subscribe stream (snapshots,
// changes, heartbeats), and forcibly drop streams to test reconnect.
package paramstoretest

import (
	"context"
	"fmt"
	"net"
	"strings"
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

func notFound(display string) error {
	return status.Errorf(codes.NotFound, "no such resource: %s", display)
}

// nsProto parses an "env/app" namespace string into a NamespaceRef.
func nsProto(namespace string) *kmsv1.NamespaceRef {
	env, app, _ := strings.Cut(namespace, "/")
	return &kmsv1.NamespaceRef{Env: env, App: app}
}

// resourceProto builds a ResourceRef from a namespace + relative key.
func resourceProto(namespace, key string) *kmsv1.ResourceRef {
	return &kmsv1.ResourceRef{Namespace: nsProto(namespace), Key: key}
}

// displayOf renders a ResourceRef as its "/env/app/key" display path, the
// canonical key used by the fake's internal maps.
func displayOf(r *kmsv1.ResourceRef) string {
	return "/" + r.GetNamespace().GetEnv() + "/" + r.GetNamespace().GetApp() + "/" + r.GetKey()
}

// splitDisplay splits a "/env/app/key..." display path into its namespace and
// relative key.
func splitDisplay(p string) (namespace, key string) {
	parts := strings.SplitN(strings.TrimPrefix(p, "/"), "/", 3)
	if len(parts) < 3 {
		return "", strings.TrimPrefix(p, "/")
	}
	return parts[0] + "/" + parts[1], parts[2]
}

// Param builds a *kmsv1.Parameter for pushing on a Subscription snapshot,
// addressed by namespace + relative key.
func Param(namespace, key, value string, version uint64) *kmsv1.Parameter {
	return &kmsv1.Parameter{Ref: resourceProto(namespace, key), Value: value, Version: version}
}

// ParamPath is Param addressed by "/env/app/key" display path.
func ParamPath(displayPath, value string, version uint64) *kmsv1.Parameter {
	ns, key := splitDisplay(displayPath)
	return Param(ns, key, value, version)
}

// Server is a fake KMS server. Create one with New and stop it with Close.
type Server struct {
	kmsv1.UnimplementedParameterServiceServer
	kmsv1.UnimplementedSecretServiceServer
	kmsv1.UnimplementedWatchServiceServer
	kmsv1.UnimplementedAdminServiceServer

	lis  *bufconn.Listener
	grpc *grpc.Server

	mu           sync.Mutex
	params       map[string]*kmsv1.Parameter         // display path -> parameter
	secretMeta   map[string]*kmsv1.GetSecretResponse // display path -> secret
	revision     uint64
	paramErr     map[string]error       // display path -> error
	secretErr    map[string]error       // display path -> error
	lastMetadata map[string]metadata.MD // method -> incoming md
	putSecrets   []PutSecretCall
	getParamHook func(displayPath string)
	identity     *kmsv1.WhoAmIResponse

	subMu     sync.Mutex
	subs      []*Subscription
	subNotify chan *Subscription
}

// PutSecretCall records a PutSecret invocation for assertions.
type PutSecretCall struct {
	Namespace           string
	Key                 string
	Path                string // display path
	Value               []byte
	ClientBound         bool
	GenerateAccessToken bool
}

// New starts a fake server on an in-memory bufconn listener.
func New() (*Server, error) {
	s := &Server{
		lis:          bufconn.Listen(1 << 20),
		params:       make(map[string]*kmsv1.Parameter),
		secretMeta:   make(map[string]*kmsv1.GetSecretResponse),
		paramErr:     make(map[string]error),
		secretErr:    make(map[string]error),
		lastMetadata: make(map[string]metadata.MD),
		identity:     &kmsv1.WhoAmIResponse{Name: "test", Kind: "client"},
		subNotify:    make(chan *Subscription, 16),
	}
	s.grpc = grpc.NewServer()
	kmsv1.RegisterParameterServiceServer(s.grpc, s)
	kmsv1.RegisterSecretServiceServer(s.grpc, s)
	kmsv1.RegisterWatchServiceServer(s.grpc, s)
	kmsv1.RegisterAdminServiceServer(s.grpc, s)
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

// SetParameter stores a parameter value addressed by namespace + relative key
// and bumps the revision.
func (s *Server) SetParameter(namespace, key, value string) uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.revision++
	ref := resourceProto(namespace, key)
	s.params[displayOf(ref)] = &kmsv1.Parameter{Ref: ref, Value: value, Version: s.revision}
	return s.revision
}

// SetParameterPath is SetParameter addressed by "/env/app/key" display path.
func (s *Server) SetParameterPath(displayPath, value string) uint64 {
	ns, key := splitDisplay(displayPath)
	return s.SetParameter(ns, key, value)
}

// RemoveParameter removes a parameter and bumps the revision.
func (s *Server) RemoveParameter(namespace, key string) uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.revision++
	delete(s.params, displayOf(resourceProto(namespace, key)))
	return s.revision
}

// RemoveParameterPath is RemoveParameter addressed by display path.
func (s *Server) RemoveParameterPath(displayPath string) uint64 {
	ns, key := splitDisplay(displayPath)
	return s.RemoveParameter(ns, key)
}

// SetSecret stores secret plaintext addressed by namespace + relative key.
func (s *Server) SetSecret(namespace, key string, value []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.revision++
	ref := resourceProto(namespace, key)
	s.secretMeta[displayOf(ref)] = &kmsv1.GetSecretResponse{
		Ref:     ref,
		Version: s.revision,
		Value:   value,
	}
}

// SetSecretPath is SetSecret addressed by display path.
func (s *Server) SetSecretPath(displayPath string, value []byte) {
	ns, key := splitDisplay(displayPath)
	s.SetSecret(ns, key, value)
}

// SetParameterError makes GetParameter for namespace+key return err.
func (s *Server) SetParameterError(namespace, key string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.paramErr[displayOf(resourceProto(namespace, key))] = err
}

// SetParameterErrorPath is SetParameterError addressed by display path.
func (s *Server) SetParameterErrorPath(displayPath string, err error) {
	ns, key := splitDisplay(displayPath)
	s.SetParameterError(ns, key, err)
}

// SetSecretError makes GetSecret for namespace+key return err.
func (s *Server) SetSecretError(namespace, key string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.secretErr[displayOf(resourceProto(namespace, key))] = err
}

// SetSecretErrorPath is SetSecretError addressed by display path.
func (s *Server) SetSecretErrorPath(displayPath string, err error) {
	ns, key := splitDisplay(displayPath)
	s.SetSecretError(ns, key, err)
}

// SetGetParameterHook installs fn to run at the start of every GetParameter,
// before the value is read, with the requested display path. It lets a test
// inject a concurrent event mid-fetch to exercise reconcile/stream races. Pass
// nil to clear. The hook runs outside the server lock.
func (s *Server) SetGetParameterHook(fn func(displayPath string)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.getParamHook = fn
}

// SetIdentity sets the identity returned by WhoAmI. namespace is "env/app", or
// "" for an unbound identity. It lets a test drive namespace discovery.
func (s *Server) SetIdentity(name, kind, namespace, authMethod string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	resp := &kmsv1.WhoAmIResponse{Name: name, Kind: kind, AuthMethod: authMethod}
	if namespace != "" {
		resp.Namespace = nsProto(namespace)
	}
	s.identity = resp
}

// LastMetadata returns the incoming gRPC metadata seen by the most recent call
// to the named method (e.g. "GetSecret", "GetParameter", "Subscribe", "WhoAmI").
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
	display := displayOf(req.GetRef())
	s.mu.Lock()
	hook := s.getParamHook
	s.mu.Unlock()
	if hook != nil {
		hook(display)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.paramErr[display]; err != nil {
		return nil, err
	}
	p, ok := s.params[display]
	if !ok {
		return nil, notFound(display)
	}
	return &kmsv1.GetParameterResponse{Parameter: p}, nil
}

func (s *Server) PutParameter(ctx context.Context, req *kmsv1.PutParameterRequest) (*kmsv1.PutParameterResponse, error) {
	s.recordMD(ctx, "PutParameter")
	rev := s.SetParameterPath(displayOf(req.GetRef()), req.GetValue())
	return &kmsv1.PutParameterResponse{Version: rev, Revision: rev}, nil
}

func (s *Server) ListParameters(ctx context.Context, req *kmsv1.ListParametersRequest) (*kmsv1.ListParametersResponse, error) {
	s.recordMD(ctx, "ListParameters")
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*kmsv1.Parameter
	for _, p := range s.params {
		if p.GetRef().GetNamespace().GetEnv() != req.GetNamespace().GetEnv() ||
			p.GetRef().GetNamespace().GetApp() != req.GetNamespace().GetApp() {
			continue
		}
		if !strings.HasPrefix(p.GetRef().GetKey(), req.GetKeyPrefix()) {
			continue
		}
		out = append(out, p)
	}
	return &kmsv1.ListParametersResponse{Parameters: out}, nil
}

// --- SecretService ---------------------------------------------------------

func (s *Server) GetSecret(ctx context.Context, req *kmsv1.GetSecretRequest) (*kmsv1.GetSecretResponse, error) {
	s.recordMD(ctx, "GetSecret")
	display := displayOf(req.GetRef())
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.secretErr[display]; err != nil {
		return nil, err
	}
	meta, ok := s.secretMeta[display]
	if !ok {
		return nil, notFound(display)
	}
	return meta, nil
}

func (s *Server) PutSecret(ctx context.Context, req *kmsv1.PutSecretRequest) (*kmsv1.PutSecretResponse, error) {
	s.recordMD(ctx, "PutSecret")
	display := displayOf(req.GetRef())
	s.SetSecretPath(display, req.GetValue())
	ns, key := splitDisplay(display)
	s.mu.Lock()
	s.putSecrets = append(s.putSecrets, PutSecretCall{
		Namespace:           ns,
		Key:                 key,
		Path:                display,
		Value:               req.GetValue(),
		ClientBound:         req.GetClientBound(),
		GenerateAccessToken: req.GetGenerateAccessToken(),
	})
	rev := s.revision
	s.mu.Unlock()
	resp := &kmsv1.PutSecretResponse{Version: rev, Revision: rev}
	if req.GetGenerateAccessToken() {
		resp.AccessToken = "minted-token-for-" + display
	}
	return resp, nil
}

// --- AdminService ----------------------------------------------------------

func (s *Server) WhoAmI(ctx context.Context, _ *kmsv1.WhoAmIRequest) (*kmsv1.WhoAmIResponse, error) {
	s.recordMD(ctx, "WhoAmI")
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.identity, nil
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
		Selectors:        first.GetSelectors(),
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
	Selectors        []*kmsv1.WatchSelector
	LastSeenRevision uint64
	Metadata         metadata.MD

	send     chan *kmsv1.SubscribeEvent
	acks     chan uint64
	closeCh  chan struct{}
	recvDone chan struct{}
	closed   sync.Once
}

// HasSelector reports whether the subscription requested the given namespace
// ("env/app") and key pattern.
func (sub *Subscription) HasSelector(namespace, keyPattern string) bool {
	for _, sel := range sub.Selectors {
		ns := sel.GetNamespace().GetEnv() + "/" + sel.GetNamespace().GetApp()
		if ns == namespace && sel.GetKeyPattern() == keyPattern {
			return true
		}
	}
	return false
}

// SelectorDisplays renders the requested selectors as "/env/app/pattern" for
// convenient assertions.
func (sub *Subscription) SelectorDisplays() []string {
	out := make([]string, 0, len(sub.Selectors))
	for _, sel := range sub.Selectors {
		out = append(out, "/"+sel.GetNamespace().GetEnv()+"/"+sel.GetNamespace().GetApp()+"/"+sel.GetKeyPattern())
	}
	return out
}

// PushSnapshot sends a snapshot event carrying the given parameters. Build
// parameters with Param / ParamPath.
func (sub *Subscription) PushSnapshot(revision uint64, params ...*kmsv1.Parameter) {
	sub.send <- &kmsv1.SubscribeEvent{
		Event:    &kmsv1.SubscribeEvent_Snapshot{Snapshot: &kmsv1.Snapshot{Parameters: params}},
		Revision: revision,
	}
}

// PushChange sends a parameter change event addressed by namespace + relative key.
func (sub *Subscription) PushChange(revision uint64, namespace, key, changeType, value string, version uint64) {
	sub.send <- &kmsv1.SubscribeEvent{
		Event: &kmsv1.SubscribeEvent_Change{Change: &kmsv1.ParameterChange{
			Ref:        resourceProto(namespace, key),
			ChangeType: changeType,
			Value:      value,
			Version:    version,
		}},
		Revision: revision,
	}
}

// PushChangePath is PushChange addressed by "/env/app/key" display path.
func (sub *Subscription) PushChangePath(revision uint64, displayPath, changeType, value string, version uint64) {
	ns, key := splitDisplay(displayPath)
	sub.PushChange(revision, ns, key, changeType, value, version)
}

// PushSecretChange sends a secret metadata change event addressed by namespace
// + relative key.
func (sub *Subscription) PushSecretChange(revision uint64, namespace, key, changeType string, version uint64) {
	sub.send <- &kmsv1.SubscribeEvent{
		Event: &kmsv1.SubscribeEvent_SecretChange{SecretChange: &kmsv1.SecretMetadataChange{
			Ref:        resourceProto(namespace, key),
			ChangeType: changeType,
			Version:    version,
		}},
		Revision: revision,
	}
}

// PushSecretChangePath is PushSecretChange addressed by display path.
func (sub *Subscription) PushSecretChangePath(revision uint64, displayPath, changeType string, version uint64) {
	ns, key := splitDisplay(displayPath)
	sub.PushSecretChange(revision, ns, key, changeType, version)
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
