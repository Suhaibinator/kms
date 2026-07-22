// Package paramstoretest provides an in-process, scriptable fake of the KMS
// gRPC services, backed by bufconn. It lets SDK consumers (and the SDK's own
// tests) exercise Client behaviour end to end without a real server: script
// parameter/secret values by namespace + relative key (or by display path),
// inject errors, set the WhoAmI identity, drive the Subscribe stream (snapshots,
// changes, heartbeats), script exact-version configuration releases and their
// lifecycle acknowledgements, and forcibly drop streams to test reconnect.
package paramstoretest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"sort"
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
	"google.golang.org/protobuf/proto"
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
	kmsv1.UnimplementedConfigurationReleaseServiceServer

	lis  *bufconn.Listener
	grpc *grpc.Server

	mu             sync.Mutex
	params         map[string]*kmsv1.Parameter // display path -> current parameter
	paramVersions  map[string]map[uint64]*kmsv1.Parameter
	secretMeta     map[string]*kmsv1.GetSecretResponse // display path -> current secret
	secretVersions map[string]map[uint64]*kmsv1.GetSecretResponse
	revision       uint64
	paramErr       map[string]error       // display path -> error
	secretErr      map[string]error       // display path -> error
	lastMetadata   map[string]metadata.MD // method -> incoming md
	putSecrets     []PutSecretCall
	getParamHook   func(displayPath string)
	listHook       func(namespace string)
	identity       *kmsv1.WhoAmIResponse

	subMu     sync.Mutex
	subs      []*Subscription
	subNotify chan *Subscription

	releaseMu        sync.Mutex
	activeRelease    *kmsv1.GetActiveReleaseResponse
	releaseSubs      []*ReleaseSubscription
	releaseSubNotify chan *ReleaseSubscription
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
		lis:              bufconn.Listen(1 << 20),
		params:           make(map[string]*kmsv1.Parameter),
		paramVersions:    make(map[string]map[uint64]*kmsv1.Parameter),
		secretMeta:       make(map[string]*kmsv1.GetSecretResponse),
		secretVersions:   make(map[string]map[uint64]*kmsv1.GetSecretResponse),
		paramErr:         make(map[string]error),
		secretErr:        make(map[string]error),
		lastMetadata:     make(map[string]metadata.MD),
		identity:         &kmsv1.WhoAmIResponse{Name: "test", Kind: "client"},
		subNotify:        make(chan *Subscription, 16),
		releaseSubNotify: make(chan *ReleaseSubscription, 16),
	}
	s.grpc = grpc.NewServer()
	kmsv1.RegisterParameterServiceServer(s.grpc, s)
	kmsv1.RegisterSecretServiceServer(s.grpc, s)
	kmsv1.RegisterWatchServiceServer(s.grpc, s)
	kmsv1.RegisterAdminServiceServer(s.grpc, s)
	kmsv1.RegisterConfigurationReleaseServiceServer(s.grpc, s)
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

// ReleaseEntrySpec describes one exact resource pin in a scripted
// configuration release. Path may be relative to ReleaseSpec.Namespace or an
// absolute /env/app/key display path.
type ReleaseEntrySpec struct {
	Alias          string
	Kind           string
	Path           string
	Version        uint64
	ContentType    string
	MetadataJSON   string
	ClientBound    bool
	HasAccessToken bool
}

// ReleaseSpec describes an immutable release for ReleaseLoader and generated
// managed-store tests. Parameter digests and the release digest are calculated
// by the fake from the exact scripted versions.
type ReleaseSpec struct {
	Namespace     string
	Name          string
	Version       uint64
	SchemaID      string
	SchemaVersion uint64
	MetadataJSON  string
	Entries       []ReleaseEntrySpec
}

// SetActiveRelease builds and installs an active release without notifying
// existing release streams. Use it before starting a loader.
func (s *Server) SetActiveRelease(spec ReleaseSpec, activationRevision uint64) (*kmsv1.ConfigurationRelease, error) {
	release, err := s.buildRelease(spec)
	if err != nil {
		return nil, err
	}
	s.releaseMu.Lock()
	s.activeRelease = &kmsv1.GetActiveReleaseResponse{
		Release:            proto.Clone(release).(*kmsv1.ConfigurationRelease),
		ActivationRevision: activationRevision,
	}
	s.releaseMu.Unlock()
	return proto.Clone(release).(*kmsv1.ConfigurationRelease), nil
}

// ActivateConfigurationRelease installs a release and broadcasts one
// activation event to every current release subscription.
func (s *Server) ActivateConfigurationRelease(spec ReleaseSpec, activationRevision uint64) (*kmsv1.ConfigurationRelease, error) {
	release, err := s.SetActiveRelease(spec, activationRevision)
	if err != nil {
		return nil, err
	}
	s.releaseMu.Lock()
	var subs []*ReleaseSubscription
	for _, sub := range s.releaseSubs {
		registration := sub.Registration
		if registration.GetName() == spec.Name &&
			registration.GetNamespace().GetEnv()+"/"+registration.GetNamespace().GetApp() == spec.Namespace {
			subs = append(subs, sub)
		}
	}
	s.releaseMu.Unlock()
	for _, sub := range subs {
		sub.send <- &kmsv1.WatchReleaseEvent{
			Event: &kmsv1.WatchReleaseEvent_Activation{Activation: &kmsv1.ReleaseActivationEvent{
				Release: proto.Clone(release).(*kmsv1.ConfigurationRelease),
			}},
			Revision: activationRevision,
		}
	}
	return release, nil
}

func (s *Server) buildRelease(spec ReleaseSpec) (*kmsv1.ConfigurationRelease, error) {
	if spec.Namespace == "" || spec.Name == "" || spec.Version == 0 || len(spec.Entries) == 0 {
		return nil, errors.New("paramstoretest: release namespace, name, version, and entries are required")
	}
	release := &kmsv1.ConfigurationRelease{
		Namespace:     nsProto(spec.Namespace),
		Name:          spec.Name,
		Version:       spec.Version,
		SchemaId:      spec.SchemaID,
		SchemaVersion: spec.SchemaVersion,
		MetadataJson:  spec.MetadataJSON,
	}
	seen := make(map[string]struct{}, len(spec.Entries))
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, item := range spec.Entries {
		if item.Alias == "" || item.Path == "" || item.Version == 0 {
			return nil, errors.New("paramstoretest: release entry alias, path, and version are required")
		}
		if _, ok := seen[item.Alias]; ok {
			return nil, fmt.Errorf("paramstoretest: duplicate release alias %q", item.Alias)
		}
		seen[item.Alias] = struct{}{}
		namespace, key := spec.Namespace, item.Path
		if strings.HasPrefix(item.Path, "/") {
			namespace, key = splitDisplay(item.Path)
		}
		ref := resourceProto(namespace, key)
		display := displayOf(ref)
		entry := &kmsv1.ConfigurationReleaseEntry{
			Alias:          item.Alias,
			Kind:           item.Kind,
			Ref:            ref,
			Version:        item.Version,
			ContentType:    item.ContentType,
			MetadataJson:   item.MetadataJSON,
			ClientBound:    item.ClientBound,
			HasAccessToken: item.HasAccessToken,
		}
		switch item.Kind {
		case "parameter":
			parameter := s.paramVersions[display][item.Version]
			if parameter == nil {
				return nil, fmt.Errorf("paramstoretest: parameter %s version %d is unavailable", display, item.Version)
			}
			if entry.ContentType == "" {
				entry.ContentType = parameter.GetContentType()
			}
			digest := sha256.Sum256([]byte(parameter.GetValue()))
			entry.ParameterDigest = hex.EncodeToString(digest[:])
		case "secret":
			secret := s.secretVersions[display][item.Version]
			if secret == nil {
				return nil, fmt.Errorf("paramstoretest: secret %s version %d is unavailable", display, item.Version)
			}
			if entry.ContentType == "" {
				entry.ContentType = secret.GetContentType()
			}
		default:
			// Deliberately allow invalid kinds so callers can test contract
			// rejection before any resource is fetched.
		}
		release.Entries = append(release.Entries, entry)
	}
	var err error
	release.Digest, err = deterministicReleaseDigest(release)
	if err != nil {
		return nil, fmt.Errorf("paramstoretest: calculate release digest: %w", err)
	}
	return release, nil
}

func deterministicReleaseDigest(release *kmsv1.ConfigurationRelease) (string, error) {
	if release == nil || release.GetNamespace() == nil {
		return "", errors.New("empty release")
	}
	projection := &kmsv1.ConfigurationRelease{
		Namespace:     &kmsv1.NamespaceRef{Env: release.GetNamespace().GetEnv(), App: release.GetNamespace().GetApp()},
		Name:          release.GetName(),
		SchemaId:      release.GetSchemaId(),
		SchemaVersion: release.GetSchemaVersion(),
		MetadataJson:  release.GetMetadataJson(),
	}
	entries := append([]*kmsv1.ConfigurationReleaseEntry(nil), release.GetEntries()...)
	sort.Slice(entries, func(i, j int) bool { return entries[i].GetAlias() < entries[j].GetAlias() })
	for _, entry := range entries {
		if entry == nil || entry.GetRef() == nil || entry.GetRef().GetNamespace() == nil {
			return "", errors.New("empty release entry")
		}
		projection.Entries = append(projection.Entries, &kmsv1.ConfigurationReleaseEntry{
			Alias:           entry.GetAlias(),
			Kind:            entry.GetKind(),
			Ref:             &kmsv1.ResourceRef{Namespace: &kmsv1.NamespaceRef{Env: entry.GetRef().GetNamespace().GetEnv(), App: entry.GetRef().GetNamespace().GetApp()}, Key: entry.GetRef().GetKey()},
			Version:         entry.GetVersion(),
			ContentType:     entry.GetContentType(),
			MetadataJson:    entry.GetMetadataJson(),
			ParameterDigest: entry.GetParameterDigest(),
			ClientBound:     entry.GetClientBound(),
			HasAccessToken:  entry.GetHasAccessToken(),
		})
	}
	b, err := (proto.MarshalOptions{Deterministic: true}).Marshal(projection)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(b)
	return hex.EncodeToString(digest[:]), nil
}

// SetParameter stores a parameter value addressed by namespace + relative key
// and bumps the revision.
func (s *Server) SetParameter(namespace, key, value string) uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.revision++
	s.setParameterVersionLocked(namespace, key, value, "", s.revision)
	return s.revision
}

// SetParameterVersion stores one exact immutable parameter version for release
// tests. It also makes that version the current value and bumps the fake's
// global revision. contentType should normally be "json" for managed groups.
func (s *Server) SetParameterVersion(namespace, key, value, contentType string, version uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.revision++
	s.setParameterVersionLocked(namespace, key, value, contentType, version)
}

func (s *Server) setParameterVersionLocked(namespace, key, value, contentType string, version uint64) {
	ref := resourceProto(namespace, key)
	display := displayOf(ref)
	p := &kmsv1.Parameter{Ref: ref, Value: value, ContentType: contentType, Version: version}
	s.params[display] = p
	versions := s.paramVersions[display]
	if versions == nil {
		versions = make(map[uint64]*kmsv1.Parameter)
		s.paramVersions[display] = versions
	}
	versions[version] = p
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
	s.setSecretVersionLocked(namespace, key, value, "", s.revision)
}

// SetSecretVersion stores one exact immutable secret version for release
// tests. The plaintext is copied so later test mutations cannot alter it.
func (s *Server) SetSecretVersion(namespace, key string, value []byte, contentType string, version uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.revision++
	s.setSecretVersionLocked(namespace, key, value, contentType, version)
}

func (s *Server) setSecretVersionLocked(namespace, key string, value []byte, contentType string, version uint64) {
	ref := resourceProto(namespace, key)
	display := displayOf(ref)
	secret := &kmsv1.GetSecretResponse{
		Ref:         ref,
		Version:     version,
		Value:       append([]byte(nil), value...),
		ContentType: contentType,
	}
	s.secretMeta[display] = secret
	versions := s.secretVersions[display]
	if versions == nil {
		versions = make(map[uint64]*kmsv1.GetSecretResponse)
		s.secretVersions[display] = versions
	}
	versions[version] = secret
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

// SetListParametersHook installs fn to run at the start of every
// ListParameters, before the values are read, with the requested namespace
// ("env/app"). It lets a test inject a concurrent event mid-reconcile to
// exercise the reconcile/stream race. Pass nil to clear. The hook runs outside
// the server lock.
func (s *Server) SetListParametersHook(fn func(namespace string)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.listHook = fn
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
	p := s.params[display]
	if req.GetVersion() > 0 {
		p = s.paramVersions[display][req.GetVersion()]
	}
	if p == nil {
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
	hook := s.listHook
	s.mu.Unlock()
	if hook != nil {
		ns := req.GetNamespace().GetEnv() + "/" + req.GetNamespace().GetApp()
		hook(ns)
	}
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
	meta := s.secretMeta[display]
	if req.GetVersion() > 0 {
		meta = s.secretVersions[display][req.GetVersion()]
	}
	if meta == nil {
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

// --- ConfigurationReleaseService -----------------------------------------

// GetActiveRelease returns the release installed by SetActiveRelease or
// ActivateConfigurationRelease.
func (s *Server) GetActiveRelease(ctx context.Context, req *kmsv1.GetActiveReleaseRequest) (*kmsv1.GetActiveReleaseResponse, error) {
	s.recordMD(ctx, "GetActiveRelease")
	s.releaseMu.Lock()
	defer s.releaseMu.Unlock()
	active := s.activeRelease
	if active == nil || active.GetRelease() == nil ||
		active.GetRelease().GetName() != req.GetName() ||
		active.GetRelease().GetNamespace().GetEnv() != req.GetNamespace().GetEnv() ||
		active.GetRelease().GetNamespace().GetApp() != req.GetNamespace().GetApp() {
		return nil, notFound(req.GetName())
	}
	return proto.Clone(active).(*kmsv1.GetActiveReleaseResponse), nil
}

// WatchRelease captures registrations and acknowledgements and relays events
// sent through ActivateRelease or the returned ReleaseSubscription handle.
func (s *Server) WatchRelease(stream kmsv1.ConfigurationReleaseService_WatchReleaseServer) error {
	s.recordMD(stream.Context(), "WatchRelease")
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	registration := first.GetRegister()
	if registration == nil {
		return status.Error(codes.InvalidArgument, "first release watch request must register")
	}
	md, _ := metadata.FromIncomingContext(stream.Context())
	sub := &ReleaseSubscription{
		Registration: proto.Clone(registration).(*kmsv1.ReleaseWatchRegistration),
		Metadata:     md,
		send:         make(chan *kmsv1.WatchReleaseEvent, 64),
		acks:         make(chan *kmsv1.ReleaseAcknowledgement, 64),
		closeCh:      make(chan struct{}),
	}
	s.registerReleaseSub(sub)
	defer s.unregisterReleaseSub(sub)

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
				select {
				case sub.acks <- proto.Clone(ack).(*kmsv1.ReleaseAcknowledgement):
				default:
				}
			}
		case event := <-sub.send:
			if err := stream.Send(event); err != nil {
				return err
			}
		case <-sub.closeCh:
			return status.Error(codes.Unavailable, "release stream closed by test")
		case err := <-recvErr:
			return err
		case <-stream.Context().Done():
			return stream.Context().Err()
		}
	}
}

func (s *Server) registerReleaseSub(sub *ReleaseSubscription) {
	s.releaseMu.Lock()
	s.releaseSubs = append(s.releaseSubs, sub)
	s.releaseMu.Unlock()
	select {
	case s.releaseSubNotify <- sub:
	default:
	}
}

func (s *Server) unregisterReleaseSub(sub *ReleaseSubscription) {
	s.releaseMu.Lock()
	defer s.releaseMu.Unlock()
	for i, existing := range s.releaseSubs {
		if existing == sub {
			s.releaseSubs = append(s.releaseSubs[:i], s.releaseSubs[i+1:]...)
			return
		}
	}
}

// ReleaseSubscribeCount returns the number of open release streams.
func (s *Server) ReleaseSubscribeCount() int {
	s.releaseMu.Lock()
	defer s.releaseMu.Unlock()
	return len(s.releaseSubs)
}

// WaitForReleaseSubscribe waits for the next release stream registration.
func (s *Server) WaitForReleaseSubscribe(timeout time.Duration) (*ReleaseSubscription, error) {
	select {
	case sub := <-s.releaseSubNotify:
		return sub, nil
	case <-time.After(timeout):
		return nil, errors.New("timed out waiting for release subscription")
	}
}

// ReleaseSubscription is a handle to one open configuration-release stream.
type ReleaseSubscription struct {
	Registration *kmsv1.ReleaseWatchRegistration
	Metadata     metadata.MD

	send    chan *kmsv1.WatchReleaseEvent
	acks    chan *kmsv1.ReleaseAcknowledgement
	closeCh chan struct{}
	closed  sync.Once
}

// PushSnapshot sends an authoritative release snapshot event.
func (sub *ReleaseSubscription) PushSnapshot(release *kmsv1.ConfigurationRelease, revision uint64) {
	sub.send <- &kmsv1.WatchReleaseEvent{
		Event: &kmsv1.WatchReleaseEvent_Snapshot{Snapshot: &kmsv1.ReleaseSnapshotEvent{
			Release: proto.Clone(release).(*kmsv1.ConfigurationRelease),
		}},
		Revision: revision,
	}
}

// PushActivation sends one release activation event without changing the
// fake's GetActiveRelease response.
func (sub *ReleaseSubscription) PushActivation(release *kmsv1.ConfigurationRelease, revision uint64) {
	sub.send <- &kmsv1.WatchReleaseEvent{
		Event: &kmsv1.WatchReleaseEvent_Activation{Activation: &kmsv1.ReleaseActivationEvent{
			Release: proto.Clone(release).(*kmsv1.ConfigurationRelease),
		}},
		Revision: revision,
	}
}

// WaitAcknowledgement returns the next lifecycle acknowledgement.
func (sub *ReleaseSubscription) WaitAcknowledgement(timeout time.Duration) (*kmsv1.ReleaseAcknowledgement, error) {
	select {
	case ack := <-sub.acks:
		return ack, nil
	case <-time.After(timeout):
		return nil, errors.New("timed out waiting for release acknowledgement")
	}
}

// Kill forcibly disconnects this release stream.
func (sub *ReleaseSubscription) Kill() {
	sub.closed.Do(func() { close(sub.closeCh) })
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
		Namespaces:       first.GetNamespaces(),
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
	Namespaces       []*kmsv1.NamespaceRef
	LastSeenRevision uint64
	Metadata         metadata.MD

	send     chan *kmsv1.SubscribeEvent
	acks     chan uint64
	closeCh  chan struct{}
	recvDone chan struct{}
	closed   sync.Once
}

// HasNamespace reports whether the subscription requested the given namespace
// ("env/app").
func (sub *Subscription) HasNamespace(namespace string) bool {
	for _, ns := range sub.Namespaces {
		if ns.GetEnv()+"/"+ns.GetApp() == namespace {
			return true
		}
	}
	return false
}

// NamespaceStrings renders the requested namespaces as "env/app" for convenient
// assertions.
func (sub *Subscription) NamespaceStrings() []string {
	out := make([]string, 0, len(sub.Namespaces))
	for _, ns := range sub.Namespaces {
		out = append(out, ns.GetEnv()+"/"+ns.GetApp())
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
