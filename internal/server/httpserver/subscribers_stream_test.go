package httpserver

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Suhaibinator/kms/internal/core"
	"github.com/Suhaibinator/kms/internal/domain"
)

type sseFrame struct {
	event string
	data  string
}

// readFrame reads one blank-line-terminated SSE frame, skipping comment-only
// frames (keep-alives).
func readFrame(t *testing.T, r *bufio.Reader) (sseFrame, error) {
	t.Helper()
	for {
		var frame sseFrame
		lines := 0
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return sseFrame{}, err
			}
			line = strings.TrimRight(line, "\r\n")
			if line == "" {
				break
			}
			lines++
			switch {
			case strings.HasPrefix(line, ":"):
				continue
			case strings.HasPrefix(line, "event: "):
				frame.event = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				frame.data = strings.TrimPrefix(line, "data: ")
			}
		}
		if frame.event != "" || frame.data != "" {
			return frame, nil
		}
		if lines == 0 {
			return sseFrame{}, io.ErrUnexpectedEOF
		}
	}
}

func TestReleaseSubscriberStream(t *testing.T) {
	e := newReleaseTestEnv(t)
	e.seedConsoleApp("dev")
	if shipped := e.ship("dev", "rate_limits", "7", false); shipped["status"] != "activated" {
		t.Fatalf("ship = %v", shipped)
	}
	s := newServer(e.svc, Config{Addr: ":0", Version: "test-version"})
	s.stream = streamLimits{debounce: 20 * time.Millisecond, requery: time.Hour, keepAlive: 30 * time.Millisecond, maxLifetime: 1500 * time.Millisecond, perIdentity: 1, global: 64}
	ts := httptest.NewServer(s.handler())
	defer ts.Close()

	open := func(ctx context.Context) *http.Response {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/api/v1/release-subscribers/stream?env=dev&app=gradethis&name=runtime", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+e.adminToken)
		resp, err := ts.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	ctx := t.Context()
	resp := open(ctx)
	defer func() {
		if err := resp.Body.Close(); err != nil {
			t.Errorf("close stream response body: %v", err)
		}
	}()
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") || resp.Header.Get("Cache-Control") != "no-store" || resp.Header.Get("X-Accel-Buffering") != "no" {
		t.Fatalf("stream response = %d %v", resp.StatusCode, resp.Header)
	}
	reader := bufio.NewReader(resp.Body)
	frame, err := readFrame(t, reader)
	if err != nil || frame.event != "snapshot" {
		t.Fatalf("first frame = %+v err=%v", frame, err)
	}
	var snapshot map[string]any
	if err := json.Unmarshal([]byte(frame.data), &snapshot); err != nil {
		t.Fatal(err)
	}
	summary := snapshot["summary"].(map[string]any)
	if summary["total"].(float64) != 0 || snapshot["current_revision"].(float64) == 0 || snapshot["server_time_unix_ms"].(float64) == 0 || len(snapshot["subscribers"].([]any)) != 0 {
		t.Fatalf("initial snapshot = %v", snapshot)
	}

	// A second stream for the same identity exceeds the per-identity cap.
	second := open(ctx)
	if second.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("second stream status = %d", second.StatusCode)
	}
	if err := second.Body.Close(); err != nil {
		t.Fatalf("close second stream response body: %v", err)
	}

	// A subscriber connecting wakes the stream into a fresh snapshot.
	ns := domain.NamespaceRef{Env: "dev", App: "gradethis"}
	if err := e.svc.SetReleaseSubscriberConnected(context.Background(), ns, "runtime", "api", "i1", "admin", "conn-1", true); err != nil {
		t.Fatal(err)
	}
	frame, err = readFrame(t, reader)
	if err != nil || frame.event != "snapshot" {
		t.Fatalf("second frame = %+v err=%v", frame, err)
	}
	if err := json.Unmarshal([]byte(frame.data), &snapshot); err != nil {
		t.Fatal(err)
	}
	summary = snapshot["summary"].(map[string]any)
	if summary["total"].(float64) != 1 || summary["connected"].(float64) != 1 || summary["pending"].(float64) != 1 || len(snapshot["subscribers"].([]any)) != 1 {
		t.Fatalf("snapshot after connect = %v", snapshot)
	}

	// The lifetime cap ends the stream with an `end` event and frees the slot.
	deadline := time.Now().Add(5 * time.Second)
	for {
		frame, err = readFrame(t, reader)
		if err != nil {
			t.Fatalf("waiting for end: %v", err)
		}
		if frame.event == "end" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("stream did not end at its lifetime cap")
		}
	}
	if _, err := readFrame(t, reader); err == nil {
		t.Fatal("stream must close after end")
	}
	waitFor(t, func() bool {
		s.streams.mu.Lock()
		defer s.streams.mu.Unlock()
		return s.streams.total == 0
	})
	third := open(ctx)
	if third.StatusCode != http.StatusOK {
		t.Fatalf("stream after release status = %d", third.StatusCode)
	}
	if err := third.Body.Close(); err != nil {
		t.Fatalf("close third stream response body: %v", err)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestReleaseSubscriberStreamRejectsUnauthenticatedAndUnknown(t *testing.T) {
	e := newReleaseTestEnv(t)
	w := e.do(http.MethodGet, "/api/v1/release-subscribers/stream?env=dev&app=gradethis&name=runtime", nil, nil)
	mustStatus(t, w, http.StatusUnauthorized)
	w = e.admin(http.MethodGet, "/api/v1/release-subscribers/stream?env=dev&app=gradethis&name=runtime", nil)
	mustStatus(t, w, http.StatusNotFound)
	w = e.admin(http.MethodGet, "/api/v1/release-subscribers/stream?env=dev&app=gradethis", nil)
	mustStatus(t, w, http.StatusBadRequest)
}

// --- credential re-validation on a live stream ------------------------------

// streamRecorder collects a live SSE response off a handler goroutine.
// It is a minimal, concurrency-safe ResponseWriter: the handler writes from its
// own goroutine while the test reads what has been produced so far, and it
// implements Flush so http.ResponseController can flush the SSE frames (without
// it the very first send would fail and the stream would end for the wrong
// reason).
type streamRecorder struct {
	mu     sync.Mutex
	hdr    http.Header
	buf    bytes.Buffer
	status int
}

func newStreamRecorder() *streamRecorder {
	return &streamRecorder{hdr: http.Header{}, status: http.StatusOK}
}

func (r *streamRecorder) Header() http.Header { return r.hdr }

func (r *streamRecorder) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.buf.Write(p)
}

func (r *streamRecorder) WriteHeader(code int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.status = code
}

func (r *streamRecorder) Flush() {}

func (r *streamRecorder) body() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.buf.String()
}

func (r *streamRecorder) code() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.status
}

// TestReleaseSubscriberStreamEndsOnTokenRotation: the console's live rollout
// view is a long-lived credential. Rotating the admin's bearer token must tear
// it down on the next safety re-query rather than letting it run to the
// lifetime cap on a credential that no longer exists.
func TestReleaseSubscriberStreamEndsOnTokenRotation(t *testing.T) {
	e := newReleaseTestEnv(t)
	e.seedConsoleApp("dev")
	if shipped := e.ship("dev", "rate_limits", "7", false); shipped["status"] != "activated" {
		t.Fatalf("ship = %v", shipped)
	}
	s := newServer(e.svc, Config{Addr: ":0", Version: "test-version"})
	// A short re-query so the credential check runs promptly, and a lifetime cap
	// far beyond it so an expiring stream cannot be mistaken for a revoked one.
	s.stream = streamLimits{debounce: 20 * time.Millisecond, requery: 20 * time.Millisecond,
		keepAlive: time.Hour, maxLifetime: 30 * time.Second, perIdentity: 2, global: 64}
	ts := httptest.NewServer(s.handler())
	defer ts.Close()

	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		ts.URL+"/api/v1/release-subscribers/stream?env=dev&app=gradethis&name=runtime", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+e.adminToken)
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			t.Errorf("close stream response body: %v", err)
		}
	}()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stream status = %d", resp.StatusCode)
	}
	reader := bufio.NewReader(resp.Body)
	if frame, err := readFrame(t, reader); err != nil || frame.event != "snapshot" {
		t.Fatalf("first frame = %+v err=%v", frame, err)
	}

	if _, err := e.svc.RotateIdentityToken(context.Background(), consoleAdmin(), "admin"); err != nil {
		t.Fatalf("rotate admin token: %v", err)
	}

	deadline := time.Now().Add(10 * time.Second)
	for {
		frame, err := readFrame(t, reader)
		if err != nil {
			return // the stream closed: the rotated token no longer authorizes it
		}
		if frame.event == "end" {
			t.Fatal("stream ended at its lifetime cap instead of on the rotated token")
		}
		if time.Now().After(deadline) {
			t.Fatalf("stream outlived its rotated token (still sending %q frames)", frame.event)
		}
	}
}

// TestReleaseSubscriberStreamEndsOnCertificateRevocation: an admin stream is
// admitted on a certificate plus a token, so revoking that one certificate must
// close it. The refresh here is driven by a subscriber change rather than the
// re-query tick, which is the point: every refresh re-validates, not just the
// timer-driven one.
func TestReleaseSubscriberStreamEndsOnCertificateRevocation(t *testing.T) {
	e := newReleaseTestEnv(t)
	e.seedConsoleApp("dev")
	if shipped := e.ship("dev", "rate_limits", "7", false); shipped["status"] != "activated" {
		t.Fatalf("ship = %v", shipped)
	}
	// Seeding is done with the token alone; only now does the admin need both
	// credentials, exactly as an upgraded deployment would.
	if err := e.svc.BootstrapCA(context.Background()); err != nil {
		t.Fatalf("bootstrap CA: %v", err)
	}
	cert := e.issueAdminCert(t)
	e.svc.SetAdminRequireClientCert(true)

	s := newServer(e.svc, Config{Addr: ":0", Version: "test-version", TLSEnabled: true, AdminClientCertRequired: true})
	// No re-query and no keep-alive: every frame this stream produces is one the
	// subscriber-change wake asked for, so the assertions below are exact.
	s.stream = streamLimits{debounce: 20 * time.Millisecond, requery: time.Hour,
		keepAlive: time.Hour, maxLifetime: 30 * time.Second, perIdentity: 2, global: 64}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/release-subscribers/stream?env=dev&app=gradethis&name=runtime", nil).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer "+e.adminToken)
	req = withPeerCert(req, cert)

	rec := newStreamRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.handler().ServeHTTP(rec, req)
	}()
	waitFor(t, func() bool { return strings.Contains(rec.body(), "event: snapshot") })
	if rec.code() != http.StatusOK {
		t.Fatalf("stream status = %d, want 200", rec.code())
	}

	cliPr := core.Principal{Identity: domain.Identity{Name: "cli", Kind: domain.IdentityKindAdmin}}
	if err := e.svc.RevokeIdentityCertificate(context.Background(), cliPr, "admin", core.CertSerial(cert)); err != nil {
		t.Fatalf("revoke admin certificate: %v", err)
	}
	// Wake the stream: the refresh it triggers must refuse to send a snapshot to
	// a principal whose certificate is gone.
	ns := domain.NamespaceRef{Env: "dev", App: "gradethis"}
	if err := e.svc.SetReleaseSubscriberConnected(context.Background(), ns, "runtime", "api", "i1", "admin", "conn-1", true); err != nil {
		t.Fatal(err)
	}

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("stream outlived the revoked certificate")
	}
	body := rec.body()
	if n := strings.Count(body, "event: snapshot"); n != 1 {
		t.Fatalf("snapshot frames = %d, want exactly the one sent before revocation:\n%s", n, body)
	}
	if strings.Contains(body, "event: end") {
		t.Fatalf("stream ended at its lifetime cap instead of on the revoked certificate:\n%s", body)
	}
}
