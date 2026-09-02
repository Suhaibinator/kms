package listenertls

import (
	"crypto/tls"
	"sync"
	"sync/atomic"
)

// Reloadable holds the derived listener configuration and lets it be replaced
// while the listeners keep running. Each listener is handed an outer
// *tls.Config that carries no key material itself: its GetConfigForClient
// returns the current per-listener slot, so a Swap takes effect on the very
// next handshake without touching the listener, the *http.Server, or the
// gRPC credentials.
//
// GetConfigForClient rather than GetCertificate because the config it returns
// governs Certificates, ClientCAs and ClientAuth for that handshake: the server
// key pair and the client-CA pool swap in one pointer store, and the derived
// VerifyClientCertIfGiven policy travels with them. A custom
// VerifyPeerCertificate would not do: the transports require the
// stdlib-populated VerifiedChains that only the config's own ClientCAs
// produce.
//
// Slots are built once per listener rather than cloned per handshake because
// (1) ALPN is negotiated from the returned config's NextProtos — gRPC needs
// ["h2"], HTTP ["h2", "http/1.1"] — and (2) a tls.Config generates its
// session-ticket keys lazily per instance, so a fresh clone per handshake
// would silently disable session resumption.
//
// Connections already established keep the certificate and verification
// state they handshook with; only new handshakes see swapped material.
type Reloadable struct {
	mu      sync.Mutex
	derived *tls.Config
	slots   []*slot
}

// slot is one listener's view: the derived config plus that listener's ALPN
// protocols, replaced atomically on Swap.
type slot struct {
	protos []string
	cfg    atomic.Pointer[tls.Config]
}

// NewReloadable wraps the derived config (from Build). A nil derived config
// means TLS is off and yields a nil *Reloadable, on which Listener returns nil
// so callers can pass it straight to a plaintext listener.
func NewReloadable(derived *tls.Config) *Reloadable {
	if derived == nil {
		return nil
	}
	return &Reloadable{derived: derived}
}

// Listener returns the outer config for one listener, negotiating exactly the
// given ALPN protocols. It is safe to call before or after Swap; every slot
// it creates follows subsequent swaps.
func (r *Reloadable) Listener(nextProtos ...string) *tls.Config {
	if r == nil {
		return nil
	}
	s := &slot{protos: append([]string(nil), nextProtos...)}
	r.mu.Lock()
	s.cfg.Store(slotConfig(r.derived, s.protos))
	r.slots = append(r.slots, s)
	r.mu.Unlock()
	return &tls.Config{
		MinVersion:         tls.VersionTLS12,
		GetConfigForClient: func(*tls.ClientHelloInfo) (*tls.Config, error) { return s.cfg.Load(), nil },
	}
}

// Swap installs derived for every listener. Handshakes in flight finish with
// the config they started with; the next one sees the new material.
func (r *Reloadable) Swap(derived *tls.Config) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.derived = derived
	for _, s := range r.slots {
		s.cfg.Store(slotConfig(derived, s.protos))
	}
}

// Current returns the derived config currently served to new handshakes.
func (r *Reloadable) Current() *tls.Config {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.derived
}

func slotConfig(derived *tls.Config, protos []string) *tls.Config {
	cfg := derived.Clone()
	cfg.NextProtos = append([]string(nil), protos...)
	return cfg
}
