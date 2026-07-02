package grpcserver

import (
	"context"
	"fmt"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
	"github.com/Suhaibinator/kms/internal/core"
	"github.com/Suhaibinator/kms/internal/pathutil"
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
	patterns, err := normalizePatterns(first.GetPaths())
	if err != nil {
		return status.Error(codes.InvalidArgument, err.Error())
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
		Patterns:         patterns,
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
	return w.pump(ctx, stream, sub, pr, lastRev, false)
}

// WatchParameter streams events for a single exact parameter path.
func (w *watchServer) WatchParameter(req *kmsv1.WatchParameterRequest, stream kmsv1.WatchService_WatchParameterServer) error {
	ctx := stream.Context()
	pr, err := requirePrincipal(ctx)
	if err != nil {
		return err
	}
	path, err := pathutil.Normalize(req.GetPath())
	if err != nil {
		return status.Error(codes.InvalidArgument, err.Error())
	}
	return w.serverStream(ctx, stream, pr, []string{path}, req.GetLastSeenRevision())
}

// WatchNamespace streams events for everything under a prefix. The request may
// carry either a plain path (treated as its subtree) or an explicit "base/*"
// pattern.
func (w *watchServer) WatchNamespace(req *kmsv1.WatchNamespaceRequest, stream kmsv1.WatchService_WatchNamespaceServer) error {
	ctx := stream.Context()
	pr, err := requirePrincipal(ctx)
	if err != nil {
		return err
	}
	pattern, err := namespacePattern(req.GetPathPrefix())
	if err != nil {
		return status.Error(codes.InvalidArgument, err.Error())
	}
	return w.serverStream(ctx, stream, pr, []string{pattern}, req.GetLastSeenRevision())
}

// serverStream drives a server-streaming watch (WatchParameter/WatchNamespace).
// These carry no client acks, so liveness is refreshed on each successful send
// (a send failing means the client is gone) in addition to gRPC keepalive.
func (w *watchServer) serverStream(ctx context.Context, stream eventSender, pr core.Principal, patterns []string, lastSeen uint64) error {
	allowed, err := w.s.svc.WatchAccessChecker(ctx, pr)
	if err != nil {
		return w.s.mapErr(ctx, err)
	}
	reg := watch.Registration{
		ClientName:       pr.Identity.Name,
		InstanceID:       instanceID(pr.Identity.Name),
		Identity:         pr.Identity.Name,
		RemoteAddr:       pr.RemoteAddr,
		Patterns:         patterns,
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
	return w.pump(ctx, stream, sub, pr, lastRev, true)
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
func (w *watchServer) pump(ctx context.Context, stream eventSender, sub *watch.Subscription, pr core.Principal, lastRev uint64, selfAck bool) error {
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
			// revocation and policy edits apply to this live stream.
			allowed, err := w.s.svc.ReauthorizeWatch(ctx, pr)
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

// normalizePatterns validates and canonicalizes the requested watch patterns.
func normalizePatterns(paths []string) ([]string, error) {
	if len(paths) == 0 {
		return nil, fmt.Errorf("at least one path is required")
	}
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		np, err := pathutil.NormalizePrefix(p)
		if err != nil {
			return nil, err
		}
		out = append(out, np)
	}
	return out, nil
}

// namespacePattern converts a WatchNamespace request into a prefix pattern,
// accepting either a plain path (its whole subtree) or an explicit "base/*".
func namespacePattern(raw string) (string, error) {
	if raw == "*" || raw == "/*" {
		return "/*", nil
	}
	if strings.HasSuffix(raw, "/*") {
		return pathutil.NormalizePrefix(raw)
	}
	base, err := pathutil.Normalize(raw)
	if err != nil {
		return "", err
	}
	return base + "/*", nil
}

// instanceID derives a per-connection identifier from the client name and a
// short random suffix.
func instanceID(clientName string) string {
	if clientName == "" {
		clientName = "watcher"
	}
	return clientName + "-" + watch.InstanceSuffix()
}
