package kmsclient

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"
)

// SecretValue is a declarative, store-backed secret field. Declare it in a
// config struct, then resolve it with Init (or via Client.Resolve). Once
// initialized it redacts itself in all string/JSON formatting; plaintext is
// only reachable through Value/StringValue.
//
// The mutable state lives behind an unexported pointer so that SecretValue's
// redacting formatter methods can use value receivers (a struct that embedded a
// mutex directly could not, and would leak plaintext through fmt's reflection
// of unexported fields).
type SecretValue struct {
	// Key is the secret key, relative to the client namespace (e.g.
	// "stripe/api-key"), or an absolute "/env/app/key" display path.
	Key string
	// Token is the per-secret access token. For client-bound secrets it is also
	// the client key share (plan 10.7).
	Token string
	// EnvVar is an optional environment variable that, when set and non-empty,
	// overrides the store value.
	EnvVar string
	// Default is an optional fallback value, intended for development only.
	Default string

	state *secretState
}

type secretState struct {
	mu          sync.RWMutex
	initialized bool
	fromEnv     bool
	value       string
}

func (v *SecretValue) ensureState() *secretState {
	if v.state == nil {
		v.state = &secretState{}
	}
	return v.state
}

// Init resolves the value using the standard order: env override, then store
// fetch, then Default (only when the store reports the value absent, unless
// Config.FallbackToDefaultsOnError is set), else an error naming the path. It is
// idempotent: a second call after success is a no-op. Init is not safe to call
// concurrently on the same SecretValue (Client.Resolve initializes distinct
// fields in parallel, which is safe).
func (v *SecretValue) Init(client *Client) error {
	return v.InitContext(context.Background(), client)
}

// InitContext is Init with a caller-supplied context.
func (v *SecretValue) InitContext(ctx context.Context, client *Client) error {
	st := v.ensureState()
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.initialized {
		return nil
	}

	if v.EnvVar != "" {
		if ev := os.Getenv(v.EnvVar); ev != "" {
			st.value = ev
			st.fromEnv = true
			st.initialized = true
			client.logf("kmsclient: secret %q resolved from env %s (store fetch skipped)", v.Key, v.EnvVar)
			return nil
		}
	}

	if v.Key != "" {
		sec, err := client.GetSecret(ctx, v.Key, WithSecretToken(v.Token))
		if err == nil {
			st.value = sec.StringValue()
			st.initialized = true
			return nil
		}
		// Default is a dev-only escape hatch: use it when the store reports the
		// value absent (ErrNotFound), but never mask a connectivity/auth error
		// with it unless the caller opted into FallbackToDefaultsOnError.
		if v.Default != "" && client.defaultAllowedForErr(err) {
			client.logf("kmsclient: secret %q fetch failed (%v); using Default", v.Key, err)
			st.value = v.Default
			st.initialized = true
			return nil
		}
		return fmt.Errorf("kmsclient: resolve secret %q: %w", v.Key, err)
	}

	if v.Default != "" {
		st.value = v.Default
		st.initialized = true
		return nil
	}
	return fmt.Errorf("kmsclient: secret has no Key, EnvVar, or Default configured")
}

// Value returns the resolved plaintext. It panics if the value has not been
// initialized, which surfaces wiring mistakes at first use rather than serving
// an empty secret.
func (v *SecretValue) Value() string {
	st := v.state
	if st == nil {
		panic(fmt.Sprintf("kmsclient: SecretValue(%q).Value() called before Init", v.Key))
	}
	st.mu.RLock()
	defer st.mu.RUnlock()
	if !st.initialized {
		panic(fmt.Sprintf("kmsclient: SecretValue(%q).Value() called before Init", v.Key))
	}
	return st.value
}

// StringValue is an alias for Value.
func (v *SecretValue) StringValue() string { return v.Value() }

// Initialized reports whether the value has been resolved.
func (v *SecretValue) Initialized() bool {
	st := v.state
	if st == nil {
		return false
	}
	st.mu.RLock()
	defer st.mu.RUnlock()
	return st.initialized
}

// Secret returns the resolved plaintext wrapped in a redacting Secret.
func (v *SecretValue) Secret() Secret { return NewSecret([]byte(v.Value())) }

// String redacts. Value receiver so both SecretValue and *SecretValue redact,
// and so fmt does not fall through to the unexported state.
func (v SecretValue) String() string { return redactedText }

// GoString redacts (used by %#v).
func (v SecretValue) GoString() string { return redactedText }

// Format redacts for every verb.
func (v SecretValue) Format(f fmt.State, verb rune) {
	switch verb {
	case 'q':
		_, _ = fmt.Fprintf(f, "%q", redactedText)
	default:
		_, _ = io.WriteString(f, redactedText)
	}
}

// MarshalJSON redacts.
func (v SecretValue) MarshalJSON() ([]byte, error) {
	return []byte(`"` + redactedText + `"`), nil
}

// ParameterValue is a declarative, store-backed non-secret field. By default it
// hot-reloads over the client's shared Subscribe stream, so Get always returns
// the latest value without an RPC. Set Static to opt out and resolve once at
// Init.
//
// Like SecretValue, mutable state lives behind a pointer so the value type
// stays copy-safe.
type ParameterValue struct {
	// Key is the parameter key, relative to the client namespace (e.g.
	// "rate-limit"), or an absolute "/env/app/key" display path.
	Key string
	// EnvVar optionally overrides the store value; an env-set value is pinned
	// and does not hot-reload.
	EnvVar string
	// Default is an optional fallback, intended for development.
	Default string
	// Static disables hot reload: the value resolves once at Init and never
	// changes afterward. The zero value keeps hot reload ON — every non-static
	// ParameterValue tracks its namespace over the shared Subscribe stream.
	Static bool

	state *paramState
}

type paramState struct {
	value       atomic.Pointer[string]
	mu          sync.Mutex
	initialized bool
	fromEnv     bool
	client      *Client
	callbacks   []func(old, new string)
}

func (p *ParameterValue) ensureState() *paramState {
	if p.state == nil {
		p.state = &paramState{}
	}
	return p.state
}

func storeString(ptr *atomic.Pointer[string], s string) { ptr.Store(&s) }

// Init resolves the value (env override, store fetch, Default only when the
// store reports the value absent unless Config.FallbackToDefaultsOnError is set,
// else error) and, unless Static is set or the value came from an env override,
// registers the value's namespace on the client's shared subscription for hot
// reload. It is idempotent.
func (p *ParameterValue) Init(client *Client) error {
	return p.InitContext(context.Background(), client)
}

// InitContext is Init with a caller-supplied context.
func (p *ParameterValue) InitContext(ctx context.Context, client *Client) error {
	st := p.ensureState()
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.initialized {
		return nil
	}
	st.client = client

	if p.EnvVar != "" {
		if ev := os.Getenv(p.EnvVar); ev != "" {
			storeString(&st.value, ev)
			st.fromEnv = true
			st.initialized = true
			if !p.Static {
				client.logf("kmsclient: parameter %q pinned to env %s; hot reload disabled", p.Key, p.EnvVar)
			}
			return nil
		}
	}

	var (
		r       ref
		haveRef bool
	)
	if p.Key != "" {
		rr, err := client.resolveRef(ctx, p.Key)
		if err != nil {
			return err
		}
		r, haveRef = rr, true
	}

	value, err := p.resolveFromStore(ctx, client, r, haveRef)
	if err != nil {
		return err
	}
	storeString(&st.value, value)
	st.initialized = true

	if !p.Static && haveRef {
		client.subs().registerParam(r, value, p.applyUpdate)
	}
	return nil
}

func (p *ParameterValue) resolveFromStore(ctx context.Context, client *Client, r ref, haveRef bool) (string, error) {
	if haveRef {
		v, err := client.getParameter(ctx, r, getOptions{})
		if err == nil {
			return v, nil
		}
		// See SecretValue.InitContext: Default covers an absent value, not an
		// unreachable store, unless FallbackToDefaultsOnError is set.
		if p.Default != "" && client.defaultAllowedForErr(err) {
			client.logf("kmsclient: parameter %q fetch failed (%v); using Default", p.Key, err)
			return p.Default, nil
		}
		return "", fmt.Errorf("kmsclient: resolve parameter %q: %w", p.Key, err)
	}
	if p.Default != "" {
		return p.Default, nil
	}
	return "", errors.New("kmsclient: parameter has no Key, EnvVar, or Default configured")
}

// applyUpdate is invoked by the subscription manager when a new value arrives
// for this parameter's path. present is false for deletions: the value reverts
// to the configured Default so the app stops serving a value the store no longer
// has (defect M3). With no Default the last-known value is kept, since apps
// rarely want config to vanish underneath them.
func (p *ParameterValue) applyUpdate(newVal string, present bool) {
	st := p.state
	if st == nil {
		return
	}
	if !present {
		if p.Default == "" {
			return
		}
		newVal = p.Default
	}
	old := st.value.Swap(&newVal)
	oldVal := ""
	if old != nil {
		oldVal = *old
	}
	if oldVal == newVal {
		return
	}
	p.dispatch(oldVal, newVal)
}

func (p *ParameterValue) dispatch(oldVal, newVal string) {
	st := p.state
	st.mu.Lock()
	cbs := make([]func(old, new string), len(st.callbacks))
	copy(cbs, st.callbacks)
	client := st.client
	st.mu.Unlock()
	if client == nil {
		return
	}
	for _, cb := range cbs {
		cb := cb
		client.enqueueCallback(p.Key, func() { cb(oldVal, newVal) })
	}
}

// Get returns the latest value. Safe for concurrent use; returns "" before Init.
func (p *ParameterValue) Get() string {
	st := p.state
	if st == nil {
		return ""
	}
	if v := st.value.Load(); v != nil {
		return *v
	}
	return ""
}

// OnChange registers a callback invoked (on a dedicated goroutine) whenever the
// value changes. old and new are the previous and current values. Registering a
// callback on a Static or env-pinned value is allowed but it will never fire.
func (p *ParameterValue) OnChange(fn func(old, new string)) {
	st := p.ensureState()
	st.mu.Lock()
	st.callbacks = append(st.callbacks, fn)
	st.mu.Unlock()
}

// Initialized reports whether the value has been resolved.
func (p *ParameterValue) Initialized() bool {
	st := p.state
	if st == nil {
		return false
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.initialized
}

// String returns the current value (non-secret), or "" before Init.
func (p ParameterValue) String() string { return p.Get() }
