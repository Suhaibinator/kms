package kmsclient

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Suhaibinator/kms/sdk/go/kmsclient/kmsclienttest"
)

// testNS is the home namespace most tests bind their client to; relative keys
// resolve against it. The fake server addresses parameters/secrets by the same
// namespace + relative key.
const testNS = "prod/app"

// newTestClient starts a fake server and a Client wired to it, bound to testNS
// unless the caller set a namespace, registering cleanup on t.
func newTestClient(t *testing.T, cfg Config) (*Client, *kmsclienttest.Server) {
	t.Helper()
	if cfg.Namespace == "" {
		cfg.Namespace = testNS
	}
	return newRawTestClient(t, cfg)
}

// newUnboundTestClient is newTestClient without a default namespace, for tests
// exercising WhoAmI discovery and ErrNoNamespace.
func newUnboundTestClient(t *testing.T, cfg Config) (*Client, *kmsclienttest.Server) {
	t.Helper()
	return newRawTestClient(t, cfg)
}

func newRawTestClient(t *testing.T, cfg Config) (*Client, *kmsclienttest.Server) {
	t.Helper()
	srv, err := kmsclienttest.New()
	if err != nil {
		t.Fatalf("start fake server: %v", err)
	}
	t.Cleanup(srv.Close)

	cfg.Endpoint = srv.Target()
	cfg.DialOptions = srv.DialOptions()
	if cfg.Timeout == 0 {
		cfg.Timeout = 2 * time.Second
	}
	if cfg.Logger == nil {
		cfg.Logger = &testLogger{}
	}
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c, srv
}

// testLogger captures log lines so tests can assert what was (and was not)
// logged, and so secrets never reach the test output.
type testLogger struct {
	mu    sync.Mutex
	lines []string
}

func (l *testLogger) Printf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

func (l *testLogger) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]string, len(l.lines))
	copy(out, l.lines)
	return out
}

// eventually polls fn until it returns true or the deadline elapses.
func eventually(t *testing.T, timeout time.Duration, fn func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return fn()
}
