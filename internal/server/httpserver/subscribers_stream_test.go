package httpserver

import (
	"bufio"
	"context"
	"encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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
