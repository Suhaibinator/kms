package grpcserver

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"time"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
	"github.com/Suhaibinator/kms/internal/core"
	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/storage"
	"github.com/Suhaibinator/kms/internal/watch"
)

func sessionRef(p *kmsv1.ReleaseSessionRef, identity string) (domain.ReleaseSessionRef, error) {
	if p == nil || p.SchemaVersion == nil {
		return domain.ReleaseSessionRef{}, domain.Errorf(domain.ErrInvalidArgument, "session and schema_version are required")
	}
	return domain.ReleaseSessionRef{Track: domain.ReleaseTrack{Namespace: nsRefFromProto(p.Namespace), Name: p.Name, SchemaVersion: p.GetSchemaVersion()}, ClientName: p.ClientName, InstanceID: p.InstanceId, SessionID: p.SessionId, Identity: identity}, nil
}
func targetProto(t domain.InstanceReleaseTarget) *kmsv1.InstanceReleaseTarget {
	out := &kmsv1.InstanceReleaseTarget{TargetRevision: t.TargetRevision, ActivationRevision: t.ActivationRevision, Pinned: t.Pinned, PinRevision: t.PinRevision, PinnedBy: t.PinnedBy}
	if !t.PinnedAt.IsZero() {
		out.PinnedAtUnixMs = t.PinnedAt.UnixMilli()
	}
	if t.Release.Version > 0 {
		out.Release = toProtoConfigurationRelease(t.Release)
	}
	return out
}
func (h *configurationReleaseServer) RegisterReleaseSession(ctx context.Context, r *kmsv1.RegisterReleaseSessionRequest) (*kmsv1.ReleaseSessionResponse, error) {
	pr, e := requirePrincipal(ctx)
	if e != nil {
		return nil, e
	}
	ref, e := sessionRef(r.Session, pr.Identity.Name)
	if e == nil {
		e = h.s.svc.RegisterReleaseSession(ctx, pr, ref, r.Resume)
	}
	if e != nil {
		return nil, h.s.mapErr(ctx, e)
	}
	return &kmsv1.ReleaseSessionResponse{PinCapable: true}, nil
}
func (h *configurationReleaseServer) GetInstanceRelease(ctx context.Context, r *kmsv1.GetInstanceReleaseRequest) (*kmsv1.InstanceReleaseTarget, error) {
	pr, e := requirePrincipal(ctx)
	if e != nil {
		return nil, e
	}
	ref, e := sessionRef(r.Session, pr.Identity.Name)
	if e != nil {
		return nil, h.s.mapErr(ctx, e)
	}
	out, e := h.s.svc.GetInstanceRelease(ctx, pr, ref)
	if e != nil {
		return nil, h.s.mapErr(ctx, e)
	}
	return targetProto(out), nil
}
func (h *configurationReleaseServer) SetReleasePin(ctx context.Context, r *kmsv1.SetReleasePinRequest) (*kmsv1.InstanceReleaseTarget, error) {
	pr, e := requirePrincipal(ctx)
	if e != nil {
		return nil, e
	}
	if r.Session == nil || r.ExpectedPinRevision == nil {
		return nil, h.s.mapErr(ctx, domain.Errorf(domain.ErrInvalidArgument, "session and expected_pin_revision are required"))
	}
	ref, e := sessionRef(r.Session, r.Session.Identity)
	if e != nil {
		return nil, h.s.mapErr(ctx, e)
	}
	out, e := h.s.svc.SetReleasePin(ctx, pr, ref, r.Version, r.GetExpectedPinRevision())
	if e != nil {
		return nil, h.s.mapErr(ctx, e)
	}
	return targetProto(out), nil
}

// Each session stream resolves the authoritative target on notifications and
// heartbeats. Coalescing deliberately skips superseded assignments; retained
// delivery records still authenticate acknowledgements from earlier work.
func (h *configurationReleaseServer) watchInstanceRelease(stream kmsv1.ConfigurationReleaseService_WatchReleaseServer, pr core.Principal, r *kmsv1.ReleaseWatchRegistration) error {
	ctx := stream.Context()
	ref, e := sessionRef(&kmsv1.ReleaseSessionRef{Namespace: r.Namespace, Name: r.Name, SchemaVersion: r.SchemaVersion, ClientName: r.ClientName, InstanceId: r.InstanceId, SessionId: r.SessionId}, pr.Identity.Name)
	if e != nil {
		return h.s.mapErr(ctx, e)
	}
	ctx, e = h.s.svc.AuthorizeReleaseWatchContext(ctx, pr, ref.Track)
	if e != nil {
		return h.s.mapErr(ctx, e)
	}
	if e = h.s.svc.RegisterReleaseSession(ctx, pr, ref, true); e != nil {
		return h.s.mapErr(ctx, e)
	}
	namespaceID, _ := storage.ExpectedNamespaceIncarnation(ctx, ref.Track.Namespace)
	sub, e := h.s.hub.SubscribeRelease(ctx, watch.ReleaseRegistration{Namespace: ref.Track.Namespace, NamespaceID: namespaceID, Name: ref.Track.Name, SchemaVersion: ref.Track.SchemaVersion, ClientName: ref.ClientName, InstanceID: ref.InstanceID, SessionID: ref.SessionID, Identity: ref.Identity, RemoteAddr: pr.RemoteAddr})
	if e != nil {
		return h.s.mapErr(ctx, e)
	}
	defer sub.Close()
	var bytes [16]byte
	if _, e = rand.Read(bytes[:]); e != nil {
		return e
	}
	connection := hex.EncodeToString(bytes[:])
	notifications, unsubscribe := h.s.svc.SubscribeReleaseSubscribers(ref.Track)
	defer unsubscribe()
	if e = h.s.svc.ConnectReleaseSession(ctx, ref, connection, true); e != nil {
		return h.s.mapErr(ctx, e)
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = h.s.svc.ConnectReleaseSession(cleanup, ref, connection, false)
	}()
	effective, e := h.s.svc.ReleaseSessionAcknowledgement(ctx, pr, ref)
	if e != nil {
		return h.s.mapErr(ctx, e)
	}
	sub.RecordEffectiveAcknowledgement(effective)
	// Receive messages and terminal errors share one FIFO, so client half-close
	// cannot overtake a queued acknowledgement or its rejection response.
	type receiveResult struct {
		request *kmsv1.WatchReleaseRequest
		err     error
	}
	incoming := make(chan receiveResult, 1)
	go func() {
		for {
			request, e := stream.Recv()
			select {
			case incoming <- receiveResult{request: request, err: e}:
			case <-ctx.Done():
				return
			}
			if e != nil {
				return
			}
		}
	}()
	ticker := time.NewTicker(h.s.hub.HeartbeatInterval())
	defer ticker.Stop()
	var last uint64
	sendTarget := func(force bool) error {
		if e := h.s.svc.ReauthorizeReleaseWatch(ctx, pr, ref.Track); e != nil {
			return e
		}
		t, e := h.s.svc.GetInstanceRelease(ctx, pr, ref)
		if e != nil {
			return e
		}
		if force || t.TargetRevision != last {
			if e := stream.Send(&kmsv1.WatchReleaseEvent{Event: &kmsv1.WatchReleaseEvent_Target{Target: targetProto(t)}, Revision: t.TargetRevision}); e != nil {
				return e
			}
			last = t.TargetRevision
		}
		return nil
	}
	if e = sendTarget(true); e != nil {
		return h.s.mapErr(ctx, e)
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-sub.Done():
			return nil
		case <-sub.Events():
			if e := sendTarget(false); e != nil {
				return h.s.mapErr(ctx, e)
			}
		case <-notifications:
			if e := sendTarget(false); e != nil {
				return h.s.mapErr(ctx, e)
			}
		case <-ticker.C:
			if e := sendTarget(false); e != nil {
				return h.s.mapErr(ctx, e)
			}
			if e := stream.Send(&kmsv1.WatchReleaseEvent{Event: &kmsv1.WatchReleaseEvent_Heartbeat{Heartbeat: &kmsv1.Heartbeat{ServerTimeUnixMs: time.Now().UnixMilli()}}, Revision: last}); e != nil {
				return e
			}
		case received := <-incoming:
			if received.err == io.EOF {
				return nil
			}
			if received.err != nil {
				return received.err
			}
			a := received.request.GetAcknowledgement()
			if a == nil || a.SessionId != ref.SessionID || nsRefFromProto(a.Namespace) != ref.Track.Namespace || a.Name != ref.Track.Name || a.SchemaVersion != ref.Track.SchemaVersion || a.ClientName != ref.ClientName || a.InstanceId != ref.InstanceID {
				return h.s.mapErr(ctx, domain.Errorf(domain.ErrInvalidArgument, "acknowledgement does not match process session"))
			}
			ack := domain.ReleaseAcknowledgement{Sequence: a.Sequence, SessionID: ref.SessionID, TargetRevision: a.TargetRevision, Namespace: ref.Track.Namespace, SchemaVersion: ref.Track.SchemaVersion, ReleaseName: ref.Track.Name, ReleaseVersion: a.Version, ActivationRevision: a.ActivationRevision, ClientName: ref.ClientName, InstanceID: ref.InstanceID, ConnectionID: connection, State: a.State, RejectionCategory: a.RejectionCategory, Diagnostic: a.Diagnostic, ClientTimestamp: unixMSToTime(a.TimestampUnixMs), AppliedDivergent: a.AppliedDivergent, DivergentFieldCount: a.DivergentFieldCount}
			result, e := h.s.svc.AcknowledgeConfigurationReleaseResult(ctx, pr, ack)
			if e == nil {
				sub.RecordEffectiveAcknowledgement(result.Effective)
			}
			if _, ok := errors.AsType[*domain.ReleaseAcknowledgementUnavailableError](e); ok {
				e = stream.Send(&kmsv1.WatchReleaseEvent{Event: &kmsv1.WatchReleaseEvent_AcknowledgementRejected{AcknowledgementRejected: &kmsv1.ReleaseAcknowledgementRejectedEvent{Namespace: a.Namespace, Name: a.Name, SchemaVersion: a.SchemaVersion, Version: a.Version, ActivationRevision: a.ActivationRevision, TargetRevision: a.TargetRevision, SessionId: ref.SessionID, ClientName: a.ClientName, InstanceId: a.InstanceId, State: a.State, Sequence: a.Sequence, Reason: "target_unavailable"}}})
			}
			if e != nil {
				return h.s.mapErr(ctx, e)
			}
		}
	}
}
