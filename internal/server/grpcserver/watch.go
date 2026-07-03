package grpcserver

import (
	"context"
	"fmt"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
	"github.com/Suhaibinator/kms/internal/core"
	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/keyutil"
	"github.com/Suhaibinator/kms/internal/watch"
)

type watchServer struct {
	kmsv1.UnimplementedWatchServiceServer
	s *Server
}

// eventSender is the common Send surface of the bidi and server-streaming watch
// RPCs, letting the backlog/pump logic be shared.
type eventSender interface {
	Send(*kmsv1.SubscribeEvent) error
}

// Subscribe is the bidirectional hot-reload stream. The first client message is
// the registration; subsequent messages are heartbeat acks. The server streams
// an initial backlog (snapshot or replay), then live change events interleaved
// with heartbeats.
func (w *watchServer) Subscribe(stream kmsv1.WatchService_SubscribeServer) error {
	ctx := stream.Context()
	pr, err := requirePrincipal(ctx)
	if err != nil {
		return err
	}

	first, err := stream.Recv()
	if err != nil {
		// Client closed before registering; nothing to do.
		return err
	}
	selectors, err := normalizeSelectors(selectorsFromProto(first.GetSelectors()))
	if err != nil {
		return status.Error(codes.InvalidArgument, err.Error())
	}
	if err := w.s.svc.AuthorizeSubscribe(ctx, pr, selectors); err != nil {
		return w.s.mapErr(ctx, err)
	}
	allowed, err := w.s.svc.WatchAccessChecker(ctx, pr)
	if err != nil {
		return w.s.mapErr(ctx, err)
	}

	reg := watch.Registration{
		ClientName:       first.GetClientName(),
		InstanceID:       instanceID(first.GetClientName()),
		Identity:         pr.Identity.Name,
		RemoteAddr:       pr.RemoteAddr,
		Selectors:        selectors,
		LastSeenRevision: first.GetLastSeenRevision(),
		Allowed:          allowed,
	}
	sub, err := w.s.hub.Subscribe(ctx, reg)
	if err != nil {
		return w.s.mapErr(ctx, err)
	}
	defer sub.Close()

	lastRev, err := w.sendBacklog(stream, sub)
	if err != nil {
		return err
	}

	// Read acks concurrently. Any receive error means the client is gone; close
	// the subscription so the main loop returns.
	go func() {
		for {
			req, rerr := stream.Recv()
			if rerr != nil {
				sub.Close()
				return
			}
			sub.Ack(req.GetAckedRevision())
		}
	}()

	// Bidi clients ack their own liveness, so do not self-refresh.
	return w.pump(ctx, stream, sub, pr, selectors, lastRev, false)
}

// WatchParameter streams events for a single exact parameter key.
func (w *watchServer) WatchParameter(req *kmsv1.WatchParameterRequest, stream kmsv1.WatchService_WatchParameterServer) error {
	ctx := stream.Context()
	pr, err := requirePrincipal(ctx)
	if err != nil {
		return err
	}
	ref := refFromProto(req.GetRef())
	if err := keyutil.ValidateNamespace(ref.NS); err != nil {
		return status.Error(codes.InvalidArgument, err.Error())
	}
	if err := keyutil.ValidateKey(ref.Key); err != nil {
		return status.Error(codes.InvalidArgument, err.Error())
	}
	sel := domain.WatchSelector{NS: ref.NS, KeyPattern: ref.Key}
	return w.serverStream(ctx, stream, pr, []domain.WatchSelector{sel}, req.GetLastSeenRevision())
}

// WatchNamespace streams events for a namespace, optionally filtered to a key
// pattern (empty or "*" watches every key in the namespace).
func (w *watchServer) WatchNamespace(req *kmsv1.WatchNamespaceRequest, stream kmsv1.WatchService_WatchNamespaceServer) error {
	ctx := stream.Context()
	pr, err := requirePrincipal(ctx)
	if err != nil {
		return err
	}
	ns := nsRefFromProto(req.GetNamespace())
	if err := keyutil.ValidateNamespace(ns); err != nil {
		return status.Error(codes.InvalidArgument, err.Error())
	}
	pattern := req.GetKeyPattern()
	if pattern != "" && pattern != "*" {
		if err := keyutil.ValidateKeyPattern(pattern); err != nil {
			return status.Error(codes.InvalidArgument, err.Error())
		}
	}
	sel := domain.WatchSelector{NS: ns, KeyPattern: pattern}
	return w.serverStream(ctx, stream, pr, []domain.WatchSelector{sel}, req.GetLastSeenRevision())
}

// serverStream drives a server-streaming watch (WatchParameter/WatchNamespace).
// These carry no client acks, so liveness is refreshed on each successful send
// (a send failing means the client is gone) in addition to gRPC keepalive.
func (w *watchServer) serverStream(ctx context.Context, stream eventSender, pr core.Principal, selectors []domain.WatchSelector, lastSeen uint64) error {
	if err := w.s.svc.AuthorizeSubscribe(ctx, pr, selectors); err != nil {
		return w.s.mapErr(ctx, err)
	}
	allowed, err := w.s.svc.WatchAccessChecker(ctx, pr)
	if err != nil {
		return w.s.mapErr(ctx, err)
	}
	reg := watch.Registration{
		ClientName:       pr.Identity.Name,
		InstanceID:       instanceID(pr.Identity.Name),
		Identity:         pr.Identity.Name,
		RemoteAddr:       pr.RemoteAddr,
		Selectors:        selectors,
		LastSeenRevision: lastSeen,
		Allowed:          allowed,
	}
	sub, err := w.s.hub.Subscribe(ctx, reg)
	if err != nil {
		return w.s.mapErr(ctx, err)
	}
	defer sub.Close()

	lastRev, err := w.sendBacklog(stream, sub)
	if err != nil {
		return err
	}
	return w.pump(ctx, stream, sub, pr, selectors, lastRev, true)
}

// sendBacklog delivers the subscription's initial state and returns the highest
// revision the client is now caught up to.
func (w *watchServer) sendBacklog(stream eventSender, sub *watch.Subscription) (uint64, error) {
	bl := sub.Backlog()
	if bl.IsSnapshot {
		ev := &kmsv1.SubscribeEvent{
			Event: &kmsv1.SubscribeEvent_Snapshot{
				Snapshot: &kmsv1.Snapshot{Parameters: toProtoParameters(bl.Snapshot)},
			},
			Revision: bl.Revision,
		}
		if err := stream.Send(ev); err != nil {
			return 0, err
		}
		return bl.Revision, nil
	}
	for _, e := range bl.Replay {
		if err := stream.Send(toSubscribeEvent(e)); err != nil {
			return 0, err
		}
	}
	return bl.Revision, nil
}

// pump forwards live events and heartbeats until the stream, subscription, or
// context ends. On every heartbeat tick it re-authorizes the stream: an error
// (identity revoked/disabled) closes the stream, and a fresh predicate is
// swapped in so policy changes take effect within one heartbeat interval. When
// selfAck is set, each successful send refreshes the subscriber's liveness
// (used by server-streaming watchers with no client acks).
func (w *watchServer) pump(ctx context.Context, stream eventSender, sub *watch.Subscription, pr core.Principal, selectors []domain.WatchSelector, lastRev uint64, selfAck bool) error {
	ticker := time.NewTicker(w.s.hub.HeartbeatInterval())
	defer ticker.Stop()

	if selfAck {
		sub.Ack(lastRev)
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-sub.Done():
			return nil
		case e := <-sub.Events():
			if err := stream.Send(toSubscribeEvent(e)); err != nil {
				return err
			}
			lastRev = e.Revision
			if selfAck {
				sub.Ack(lastRev)
			}
		case <-ticker.C:
			// Re-validate identity and refresh the authorization predicate so
			// revocation and policy edits apply to this live stream. Passing the
			// selectors re-runs the per-namespace method gate, so a namespace
			// tightened to mtls-only drops an existing token stream.
			allowed, err := w.s.svc.ReauthorizeWatch(ctx, pr, selectors...)
			if err != nil {
				return w.s.mapErr(ctx, err)
			}
			sub.UpdateAllowed(allowed)

			hb := &kmsv1.SubscribeEvent{
				Event:    &kmsv1.SubscribeEvent_Heartbeat{Heartbeat: &kmsv1.Heartbeat{ServerTimeUnixMs: time.Now().UnixMilli()}},
				Revision: lastRev,
			}
			if err := stream.Send(hb); err != nil {
				return err
			}
			if selfAck {
				sub.Ack(lastRev)
			}
		}
	}
}

// normalizeSelectors validates the requested watch selectors. At least one is
// required; each names a namespace and an optional key pattern (empty or "*"
// selects every key in the namespace).
func normalizeSelectors(sels []domain.WatchSelector) ([]domain.WatchSelector, error) {
	if len(sels) == 0 {
		return nil, fmt.Errorf("at least one selector is required")
	}
	out := make([]domain.WatchSelector, 0, len(sels))
	for _, sel := range sels {
		if err := keyutil.ValidateNamespace(sel.NS); err != nil {
			return nil, err
		}
		if sel.KeyPattern != "" && sel.KeyPattern != "*" {
			if err := keyutil.ValidateKeyPattern(sel.KeyPattern); err != nil {
				return nil, err
			}
		}
		out = append(out, sel)
	}
	return out, nil
}

// instanceID derives a per-connection identifier from the client name and a
// short random suffix.
func instanceID(clientName string) string {
	if clientName == "" {
		clientName = "watcher"
	}
	return clientName + "-" + watch.InstanceSuffix()
}
