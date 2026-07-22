package grpcserver

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/storage"
	"github.com/Suhaibinator/kms/internal/watch"
	"go.uber.org/zap"
)

type configurationReleaseServer struct {
	kmsv1.UnimplementedConfigurationReleaseServiceServer
	s              *Server
	connectionMu   sync.Mutex
	connections    map[releaseConnectionKey]*releaseConnectionState
	nextConnection uint64
}

type releaseConnectionState struct {
	// registrationMu preserves the order in which connection generations are
	// persisted without blocking registrations for other subscriber instances.
	registrationMu sync.Mutex
	active         map[uint64]struct{}
	persistedID    uint64
}

type releaseConnectionKey struct {
	namespace  domain.NamespaceRef
	name       string
	clientName string
	instanceID string
	identity   string
}

func (h *configurationReleaseServer) addConnection(key releaseConnectionKey) uint64 {
	h.connectionMu.Lock()
	defer h.connectionMu.Unlock()
	h.nextConnection++
	if h.connections == nil {
		h.connections = make(map[releaseConnectionKey]*releaseConnectionState)
	}
	state := h.connections[key]
	if state == nil {
		state = &releaseConnectionState{active: make(map[uint64]struct{})}
		h.connections[key] = state
	}
	state.active[h.nextConnection] = struct{}{}
	return h.nextConnection
}

// persistConnection serializes registrations for one subscriber instance and
// records the generation that storage most recently accepted. Keeping this
// distinct from the active stream IDs lets the last duplicate stream clear
// liveness even when a newer duplicate closed before it.
func (h *configurationReleaseServer) persistConnection(key releaseConnectionKey, id uint64, persist func() error) error {
	h.connectionMu.Lock()
	state := h.connections[key]
	h.connectionMu.Unlock()
	if state == nil {
		return fmt.Errorf("release connection registration disappeared")
	}
	state.registrationMu.Lock()
	defer state.registrationMu.Unlock()
	if err := persist(); err != nil {
		return err
	}
	h.connectionMu.Lock()
	if h.connections[key] == state {
		state.persistedID = id
	}
	h.connectionMu.Unlock()
	return nil
}

func (h *configurationReleaseServer) removeConnection(key releaseConnectionKey, id uint64) (bool, uint64) {
	h.connectionMu.Lock()
	defer h.connectionMu.Unlock()
	state := h.connections[key]
	if state == nil {
		return false, 0
	}
	if _, ok := state.active[id]; !ok {
		return false, 0
	}
	delete(state.active, id)
	if len(state.active) != 0 {
		return false, 0
	}
	delete(h.connections, key)
	return state.persistedID != 0, state.persistedID
}

func (h *configurationReleaseServer) CreateRelease(ctx context.Context, req *kmsv1.CreateReleaseRequest) (*kmsv1.CreateReleaseResponse, error) {
	pr, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	entries := make([]domain.ReleaseEntrySelector, 0, len(req.GetEntries()))
	for _, e := range req.GetEntries() {
		entries = append(entries, domain.ReleaseEntrySelector{Alias: e.GetAlias(), Kind: e.GetKind(), Ref: refFromProto(e.GetRef()), Version: e.GetVersion(), Label: e.GetLabel()})
	}
	out, err := h.s.svc.CreateConfigurationRelease(ctx, pr, domain.CreateConfigurationReleaseInput{Namespace: nsRefFromProto(req.GetNamespace()), Name: req.GetName(), SchemaID: req.GetSchemaId(), SchemaVersion: req.GetSchemaVersion(), Entries: entries, Metadata: req.GetMetadataJson()})
	if err != nil {
		return nil, h.s.mapErr(ctx, err)
	}
	return &kmsv1.CreateReleaseResponse{Release: toProtoConfigurationRelease(out)}, nil
}

func (h *configurationReleaseServer) ValidateRelease(ctx context.Context, req *kmsv1.ValidateReleaseRequest) (*kmsv1.ValidateReleaseResponse, error) {
	pr, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	errs, err := h.s.svc.ValidateConfigurationRelease(ctx, pr, nsRefFromProto(req.GetNamespace()), req.GetName(), req.GetVersion())
	if err != nil {
		return nil, h.s.mapErr(ctx, err)
	}
	out := make([]*kmsv1.ReleaseValidationError, 0, len(errs))
	for _, e := range errs {
		out = append(out, toProtoReleaseValidationError(e))
	}
	return &kmsv1.ValidateReleaseResponse{Valid: len(out) == 0, Errors: out}, nil
}

func toProtoReleaseValidationError(e domain.ReleaseValidationError) *kmsv1.ReleaseValidationError {
	return &kmsv1.ReleaseValidationError{Alias: e.Alias, Code: e.Code, SchemaPointer: e.SchemaPointer, Message: e.Message}
}

func (h *configurationReleaseServer) ActivateRelease(ctx context.Context, req *kmsv1.ActivateReleaseRequest) (*kmsv1.ActivateReleaseResponse, error) {
	pr, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	var expected *uint64
	if req.ExpectedCurrentVersion != nil {
		v := req.GetExpectedCurrentVersion()
		expected = &v
	}
	active, changed, err := h.s.svc.ActivateConfigurationRelease(ctx, pr, nsRefFromProto(req.GetNamespace()), req.GetName(), req.GetVersion(), expected)
	if err != nil {
		return nil, h.s.mapErr(ctx, err)
	}
	return &kmsv1.ActivateReleaseResponse{Release: toProtoConfigurationRelease(active.Release), CurrentVersion: active.Release.Version, PreviousVersion: active.PreviousVersion, ActivationRevision: active.ActivationRevision, Changed: changed}, nil
}

func (h *configurationReleaseServer) GetRelease(ctx context.Context, req *kmsv1.GetReleaseRequest) (*kmsv1.GetReleaseResponse, error) {
	pr, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	out, err := h.s.svc.GetConfigurationRelease(ctx, pr, nsRefFromProto(req.GetNamespace()), req.GetName(), req.GetVersion())
	if err != nil {
		return nil, h.s.mapErr(ctx, err)
	}
	return &kmsv1.GetReleaseResponse{Release: toProtoConfigurationRelease(out)}, nil
}
func (h *configurationReleaseServer) GetActiveRelease(ctx context.Context, req *kmsv1.GetActiveReleaseRequest) (*kmsv1.GetActiveReleaseResponse, error) {
	pr, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	out, err := h.s.svc.GetActiveConfigurationRelease(ctx, pr, nsRefFromProto(req.GetNamespace()), req.GetName())
	if err != nil {
		return nil, h.s.mapErr(ctx, err)
	}
	return &kmsv1.GetActiveReleaseResponse{Release: toProtoConfigurationRelease(out.Release), ActivationRevision: out.ActivationRevision, PreviousVersion: out.PreviousVersion}, nil
}
func (h *configurationReleaseServer) ListReleases(ctx context.Context, req *kmsv1.ListReleasesRequest) (*kmsv1.ListReleasesResponse, error) {
	pr, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	rows, next, err := h.s.svc.ListConfigurationReleases(ctx, pr, nsRefFromProto(req.GetNamespace()), req.GetName(), pageFrom(req.GetPageSize(), req.GetPageToken()))
	if err != nil {
		return nil, h.s.mapErr(ctx, err)
	}
	out := make([]*kmsv1.ReleaseSummary, 0, len(rows))
	for _, r := range rows {
		out = append(out, &kmsv1.ReleaseSummary{Release: toProtoConfigurationRelease(r.Release), Current: r.Current, Previous: r.Previous, ActivationRevision: r.ActivationRevision})
	}
	return &kmsv1.ListReleasesResponse{Releases: out, NextPageToken: next}, nil
}

func (h *configurationReleaseServer) WatchRelease(stream kmsv1.ConfigurationReleaseService_WatchReleaseServer) error {
	ctx := stream.Context()
	pr, err := requirePrincipal(ctx)
	if err != nil {
		return err
	}
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	regp := first.GetRegister()
	if regp == nil {
		return h.s.mapErr(ctx, domain.Errorf(domain.ErrInvalidArgument, "first watch message must register"))
	}
	ns := nsRefFromProto(regp.GetNamespace())
	if regp.GetClientName() == "" || regp.GetInstanceId() == "" || len(regp.GetClientName()) > 128 || len(regp.GetInstanceId()) > 128 {
		return h.s.mapErr(ctx, domain.Errorf(domain.ErrInvalidArgument, "client_name and instance_id must be between 1 and 128 bytes"))
	}
	ctx, err = h.s.svc.AuthorizeReleaseWatchContext(ctx, pr, ns, regp.GetName())
	if err != nil {
		return h.s.mapErr(ctx, err)
	}
	namespaceID, ok := storage.ExpectedNamespaceIncarnation(ctx, ns)
	if !ok {
		return h.s.mapErr(ctx, domain.Errorf(domain.ErrAborted, "namespace %s changed during request; retry", ns))
	}
	reg := watch.ReleaseRegistration{Namespace: ns, NamespaceID: namespaceID, Name: regp.GetName(), ClientName: regp.GetClientName(), InstanceID: regp.GetInstanceId(), Identity: pr.Identity.Name, LastSeenRevision: regp.GetLastSeenRevision()}
	sub, err := h.s.hub.SubscribeRelease(ctx, reg)
	if err != nil {
		return h.s.mapErr(ctx, err)
	}
	connectionKey := releaseConnectionKey{namespace: ns, name: reg.Name, clientName: reg.ClientName, instanceID: reg.InstanceID, identity: pr.Identity.Name}
	connectionID := h.addConnection(connectionKey)
	connectionIDText := fmt.Sprintf("%d", connectionID)
	if err := h.persistConnection(connectionKey, connectionID, func() error {
		return h.s.svc.SetReleaseSubscriberConnected(ctx, ns, reg.Name, reg.ClientName, reg.InstanceID, pr.Identity.Name, connectionIDText, true)
	}); err != nil {
		if last, persistedID := h.removeConnection(connectionKey, connectionID); last {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			cleanupErr := h.s.svc.SetReleaseSubscriberConnected(cleanupCtx, ns, reg.Name, reg.ClientName, reg.InstanceID, pr.Identity.Name, fmt.Sprintf("%d", persistedID), false)
			cleanupCancel()
			if cleanupErr != nil {
				h.s.log.Warn("release subscriber failed-registration cleanup failed",
					zap.String("release", reg.Name),
					zap.String("client", reg.ClientName),
					zap.String("instance_id", reg.InstanceID),
					zap.Error(cleanupErr))
			}
		}
		sub.Close()
		return h.s.mapErr(ctx, err)
	}
	defer func() {
		sub.Close()
		if last, persistedID := h.removeConnection(connectionKey, connectionID); last {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cleanupCancel()
			if cleanupErr := h.s.svc.SetReleaseSubscriberConnected(cleanupCtx, ns, reg.Name, reg.ClientName, reg.InstanceID, pr.Identity.Name, fmt.Sprintf("%d", persistedID), false); cleanupErr != nil {
				h.s.log.Warn("release subscriber disconnect cleanup failed",
					zap.String("release", reg.Name),
					zap.String("client", reg.ClientName),
					zap.String("instance_id", reg.InstanceID),
					zap.Error(cleanupErr))
			}
		}
	}()
	bl := sub.Backlog()
	for _, e := range bl.Events {
		event := &kmsv1.WatchReleaseEvent{Revision: e.Revision}
		if bl.IsSnapshot {
			event.Event = &kmsv1.WatchReleaseEvent_Snapshot{Snapshot: &kmsv1.ReleaseSnapshotEvent{Release: toProtoConfigurationRelease(e.Release)}}
		} else {
			event.Event = &kmsv1.WatchReleaseEvent_Activation{Activation: &kmsv1.ReleaseActivationEvent{Release: toProtoConfigurationRelease(e.Release)}}
		}
		if err := stream.Send(event); err != nil {
			return err
		}
	}
	recvErr := make(chan error, 1)
	go func() {
		for {
			req, err := stream.Recv()
			if err != nil {
				recvErr <- err
				return
			}
			a := req.GetAcknowledgement()
			if a == nil {
				recvErr <- domain.Errorf(domain.ErrInvalidArgument, "watch message must be an acknowledgement")
				return
			}
			if nsRefFromProto(a.GetNamespace()) != ns || a.GetName() != reg.Name || a.GetClientName() != reg.ClientName || a.GetInstanceId() != reg.InstanceID {
				recvErr <- domain.Errorf(domain.ErrInvalidArgument, "acknowledgement does not match registration")
				return
			}
			err = h.s.svc.AcknowledgeConfigurationRelease(ctx, pr, domain.ReleaseAcknowledgement{Namespace: ns, ReleaseName: a.GetName(), ReleaseVersion: a.GetVersion(), ActivationRevision: a.GetActivationRevision(), ClientName: a.GetClientName(), InstanceID: a.GetInstanceId(), ConnectionID: connectionIDText, State: a.GetState(), RejectionCategory: a.GetRejectionCategory(), Diagnostic: a.GetDiagnostic(), ClientTimestamp: unixMSToTime(a.GetTimestampUnixMs())})
			if err != nil {
				recvErr <- err
				return
			}
		}
	}()
	ticker := time.NewTicker(h.s.hub.HeartbeatInterval())
	defer ticker.Stop()
	last := bl.Revision
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-sub.Done():
			return nil
		case err := <-recvErr:
			if err == io.EOF {
				return nil
			}
			return h.s.mapErr(ctx, err)
		case e := <-sub.Events():
			release := e.Release
			if release.Name == "" {
				release, err = h.s.svc.GetConfigurationRelease(ctx, pr, e.Namespace, e.Name, e.Version)
				if err != nil {
					return h.s.mapErr(ctx, err)
				}
			}
			if err := stream.Send(&kmsv1.WatchReleaseEvent{Event: &kmsv1.WatchReleaseEvent_Activation{Activation: &kmsv1.ReleaseActivationEvent{Release: toProtoConfigurationRelease(release)}}, Revision: e.Revision}); err != nil {
				return err
			}
			last = e.Revision
		case <-ticker.C:
			if err := h.s.svc.ReauthorizeReleaseWatch(ctx, pr, ns, reg.Name); err != nil {
				return h.s.mapErr(ctx, err)
			}
			if err := stream.Send(&kmsv1.WatchReleaseEvent{Event: &kmsv1.WatchReleaseEvent_Heartbeat{Heartbeat: &kmsv1.Heartbeat{ServerTimeUnixMs: time.Now().UnixMilli()}}, Revision: last}); err != nil {
				return err
			}
		}
	}
}

type configurationSchemaServer struct {
	kmsv1.UnimplementedConfigurationSchemaServiceServer
	s *Server
}

func (h *configurationSchemaServer) CreateSchema(ctx context.Context, req *kmsv1.CreateSchemaRequest) (*kmsv1.CreateSchemaResponse, error) {
	pr, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	out, err := h.s.svc.CreateConfigurationSchema(ctx, pr, req.GetId(), req.GetSchemaJson(), req.GetMetadataJson())
	if err != nil {
		return nil, h.s.mapErr(ctx, err)
	}
	return &kmsv1.CreateSchemaResponse{Schema: toProtoConfigurationSchema(out)}, nil
}
func (h *configurationSchemaServer) GetSchema(ctx context.Context, req *kmsv1.GetSchemaRequest) (*kmsv1.GetSchemaResponse, error) {
	pr, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	out, err := h.s.svc.GetConfigurationSchema(ctx, pr, req.GetId(), req.GetVersion())
	if err != nil {
		return nil, h.s.mapErr(ctx, err)
	}
	return &kmsv1.GetSchemaResponse{Schema: toProtoConfigurationSchema(out)}, nil
}
func (h *configurationSchemaServer) ListSchemas(ctx context.Context, req *kmsv1.ListSchemasRequest) (*kmsv1.ListSchemasResponse, error) {
	pr, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	rows, next, err := h.s.svc.ListConfigurationSchemas(ctx, pr, req.GetId(), pageFrom(req.GetPageSize(), req.GetPageToken()))
	if err != nil {
		return nil, h.s.mapErr(ctx, err)
	}
	out := make([]*kmsv1.ConfigurationSchema, 0, len(rows))
	for _, r := range rows {
		out = append(out, toProtoConfigurationSchema(r))
	}
	return &kmsv1.ListSchemasResponse{Schemas: out, NextPageToken: next}, nil
}
