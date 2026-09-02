package httpserver

import (
	"encoding/json/v2"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/metrics"
)

// streamLimits tunes the live rollout stream. Tests shorten the intervals.
type streamLimits struct {
	// debounce coalesces bursts of subscriber changes into one snapshot.
	debounce time.Duration
	// requery is the safety re-read interval in case a change was not signalled.
	requery time.Duration
	// keepAlive is the SSE comment interval that keeps proxies from timing out.
	keepAlive time.Duration
	// maxLifetime ends the stream (event: end) so clients periodically
	// re-authenticate by reconnecting.
	maxLifetime time.Duration
	perIdentity int
	global      int
}

func defaultStreamLimits() streamLimits {
	return streamLimits{debounce: 250 * time.Millisecond, requery: 5 * time.Second, keepAlive: 15 * time.Second, maxLifetime: 5 * time.Minute, perIdentity: 4, global: 64}
}

// streamRegistry counts open streams per identity and globally.
type streamRegistry struct {
	mu         sync.Mutex
	byIdentity map[string]int
	total      int
}

func newStreamRegistry() streamRegistry { return streamRegistry{byIdentity: map[string]int{}} }

// Refusal reasons from acquire, as recorded in the audit log. streamLimiter
// maps them onto the exporter's limiter label.
const (
	reasonGlobalStreamLimit   = "global_stream_limit"
	reasonIdentityStreamLimit = "identity_stream_limit"
)

// streamLimiter names the metrics limiter behind an acquire refusal.
func streamLimiter(reason string) string {
	if reason == reasonGlobalStreamLimit {
		return metrics.LimiterSSEGlobal
	}
	return metrics.LimiterSSEIdentity
}

// acquire reserves a slot; the returned release must be called when the
// stream ends. reason names the exceeded cap when the request is refused.
func (r *streamRegistry) acquire(identity string, limits streamLimits) (release func(), reason string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.total >= limits.global {
		return nil, reasonGlobalStreamLimit
	}
	if r.byIdentity[identity] >= limits.perIdentity {
		return nil, reasonIdentityStreamLimit
	}
	r.total++
	r.byIdentity[identity]++
	return func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.total--
		r.byIdentity[identity]--
		if r.byIdentity[identity] <= 0 {
			delete(r.byIdentity, identity)
		}
	}, ""
}

// handleReleaseSubscriberStream serves the live rollout view as fetch-streamed
// server-sent events: an initial `snapshot`, a new one whenever the release's
// subscriber state changes (debounced) or on the safety re-query tick,
// `: keep-alive` comments, and a final `end` event at the lifetime cap.
func (s *server) handleReleaseSubscriberStream(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	pr := principalFrom(ctx)
	ns := nsRefFromQuery(r)
	name := r.URL.Query().Get("name")
	snapshot, err := s.svc.GetReleaseRolloutSnapshot(ctx, pr, ns, name)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	release, reason := s.streams.acquire(pr.Identity.Name, s.stream)
	if release == nil {
		s.rateLimited(streamLimiter(reason))
		s.svc.AuditReleaseStreamRejected(ctx, pr, ns, name, reason)
		writeErrorCode(w, http.StatusTooManyRequests, "rate_limited", "too many live subscriber streams open")
		return
	}
	defer release()
	// The gauge tracks streams that actually hold a slot, so it matches what
	// the caps are counting. Deferred immediately: a client that disconnects
	// mid-stream unwinds through here like any other exit.
	if m := s.cfg.Metrics; m != nil {
		m.SSEStreamStarted()
		defer m.SSEStreamEnded()
	}

	// The server's WriteTimeout would otherwise cut a healthy stream; clear it
	// for this response only. Recorders without deadline support are fine.
	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Time{})
	h := w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no")
	h.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)

	send := func(event string, payload any) error {
		data, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data); err != nil {
			return err
		}
		return rc.Flush()
	}
	sendSnapshot := func(snap domain.SubscriberStreamSnapshot) error {
		return send("snapshot", toSubscriberStreamSnapshotDTO(snap))
	}
	if err := sendSnapshot(snapshot); err != nil {
		return
	}

	wake, unsubscribe := s.svc.SubscribeReleaseSubscribers(ns, name)
	defer unsubscribe()
	keepAlive := time.NewTicker(s.stream.keepAlive)
	defer keepAlive.Stop()
	requery := time.NewTicker(s.stream.requery)
	defer requery.Stop()
	lifetime := time.NewTimer(s.stream.maxLifetime)
	defer lifetime.Stop()
	var debounce *time.Timer
	var debounceC <-chan time.Time
	refresh := func() error {
		// Re-validate the credentials that opened the stream (token rotation,
		// certificate revocation, identity disable) on every refresh, as the gRPC
		// watch streams do on every heartbeat, instead of relying solely on
		// maxLifetime to end a stream whose principal has been revoked.
		if err := s.svc.ReauthorizeWatch(ctx, pr); err != nil {
			return err
		}
		snap, err := s.svc.GetReleaseRolloutSnapshot(ctx, pr, ns, name)
		if err != nil {
			return err
		}
		return sendSnapshot(snap)
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-lifetime.C:
			_ = send("end", map[string]any{"reason": "lifetime"})
			return
		case <-wake:
			if debounce == nil {
				debounce = time.NewTimer(s.stream.debounce)
				debounceC = debounce.C
			}
		case <-debounceC:
			debounce, debounceC = nil, nil
			if err := refresh(); err != nil {
				return
			}
		case <-requery.C:
			if err := refresh(); err != nil {
				return
			}
		case <-keepAlive.C:
			if _, err := fmt.Fprint(w, ": keep-alive\n\n"); err != nil {
				return
			}
			if err := rc.Flush(); err != nil {
				return
			}
		}
	}
}
