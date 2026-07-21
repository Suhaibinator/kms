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
	"github.com/Suhaibinator/kms/internal/storage"
	"github.com/Suhaibinator/kms/internal/watch"
)

type watchServer struct {
	kmsv1.UnimplementedWatchServiceServer
	s *Server
}

// Subscribe is the hot-reload stream. The first client message is the
// registration (the namespaces to watch); subsequent messages are heartbeat
// acks. The server streams an initial backlog (snapshot or replay), then live
// change events interleaved with heartbeats. Authorization is namespace-level
// and checked once, here, at registration; every change in a subscribed
// namespace is then delivered and routed client-side.
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
	namespaces, err := normalizeNamespaces(namespacesFromProto(first.GetNamespaces()))
	if err != nil {
		return status.Error(codes.InvalidArgument, err.Error())
	}
	ctx, err = w.s.svc.AuthorizeSubscribeContext(ctx, pr, namespaces)
	if err != nil {
		return w.s.mapErr(ctx, err)
	}

	namespaceIDs := make(map[domain.NamespaceRef]int64, len(namespaces))
	for _, ns := range namespaces {
		id, ok := storage.ExpectedNamespaceIncarnation(ctx, ns)
		if !ok {
			return w.s.mapErr(ctx, domain.Errorf(domain.ErrAborted, "namespace %s changed during request; retry", ns))
		}
		namespaceIDs[ns] = id
	}
	reg := watch.Registration{
		ClientName:       first.GetClientName(),
		InstanceID:       instanceID(first.GetClientName()),
		Identity:         pr.Identity.Name,
		RemoteAddr:       pr.RemoteAddr,
		Namespaces:       namespaces,
		NamespaceIDs:     namespaceIDs,
		LastSeenRevision: first.GetLastSeenRevision(),
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

	return w.pump(ctx, stream, sub, pr, namespaces, lastRev)
}

// sendBacklog delivers the subscription's initial state and returns the highest
// revision the client is now caught up to.
func (w *watchServer) sendBacklog(stream kmsv1.WatchService_SubscribeServer, sub *watch.Subscription) (uint64, error) {
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
// (identity revoked/disabled, or a namespace tightened to a method this caller
// no longer satisfies) closes the stream. Bidi clients ack their own liveness.
func (w *watchServer) pump(ctx context.Context, stream kmsv1.WatchService_SubscribeServer, sub *watch.Subscription, pr core.Principal, namespaces []domain.NamespaceRef, lastRev uint64) error {
	ticker := time.NewTicker(w.s.hub.HeartbeatInterval())
	defer ticker.Stop()

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
		case <-ticker.C:
			// Re-validate identity and re-run the per-namespace method gate so
			// revocation and namespace method changes apply to this live stream.
			if err := w.s.svc.ReauthorizeWatch(ctx, pr, namespaces...); err != nil {
				return w.s.mapErr(ctx, err)
			}
			hb := &kmsv1.SubscribeEvent{
				Event:    &kmsv1.SubscribeEvent_Heartbeat{Heartbeat: &kmsv1.Heartbeat{ServerTimeUnixMs: time.Now().UnixMilli()}},
				Revision: lastRev,
			}
			if err := stream.Send(hb); err != nil {
				return err
			}
		}
	}
}

// normalizeNamespaces validates the requested namespaces. At least one is
// required.
func normalizeNamespaces(namespaces []domain.NamespaceRef) ([]domain.NamespaceRef, error) {
	if len(namespaces) == 0 {
		return nil, fmt.Errorf("at least one namespace is required")
	}
	out := make([]domain.NamespaceRef, 0, len(namespaces))
	for _, ns := range namespaces {
		if err := keyutil.ValidateNamespace(ns); err != nil {
			return nil, err
		}
		out = append(out, ns)
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
