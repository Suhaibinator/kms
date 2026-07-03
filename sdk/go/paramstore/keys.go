package paramstore

import (
	"fmt"
	"strings"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
)

// namespaceRef is a fixed (env, app) pair — the namespace a client operates in.
// The zero value is the unbound namespace.
type namespaceRef struct {
	env string
	app string
}

func (n namespaceRef) isZero() bool { return n.env == "" && n.app == "" }

// String renders the namespace as "env/app" (the Config.Namespace form).
func (n namespaceRef) String() string { return n.env + "/" + n.app }

func (n namespaceRef) proto() *kmsv1.NamespaceRef {
	return &kmsv1.NamespaceRef{Env: n.env, App: n.app}
}

// ref is a fully-qualified resource reference: a namespace plus a relative key.
// For watch selectors the key holds a key pattern ("*", "prefix/*", or an exact
// key) rather than a concrete key.
type ref struct {
	ns  namespaceRef
	key string
}

// display renders the reference as the "/env/app/key" display path. It is used
// for logs and audit rendering and as the SDK's internal map key for the read
// cache, known-value tracking, and pattern matching.
func (r ref) display() string {
	return "/" + r.ns.env + "/" + r.ns.app + "/" + r.key
}

func (r ref) resourceProto() *kmsv1.ResourceRef {
	return &kmsv1.ResourceRef{Namespace: r.ns.proto(), Key: r.key}
}

func (r ref) selectorProto() *kmsv1.WatchSelector {
	return &kmsv1.WatchSelector{Namespace: r.ns.proto(), KeyPattern: r.key}
}

// refFromProto converts a wire ResourceRef into a ref.
func refFromProto(p *kmsv1.ResourceRef) ref {
	if p == nil {
		return ref{}
	}
	return ref{
		ns:  namespaceRef{env: p.GetNamespace().GetEnv(), app: p.GetNamespace().GetApp()},
		key: p.GetKey(),
	}
}

// refOf parses a well-formed "/env/app/key" display path back into a ref. It is
// used internally on strings the SDK itself constructed (never on user input);
// a malformed value degrades to a keyless ref rather than panicking.
func refOf(display string) ref {
	if r, err := splitDisplayPath(display); err == nil {
		return r
	}
	return ref{key: display}
}

// parseNamespace parses the "env/app" form accepted by Config.Namespace.
func parseNamespace(s string) (namespaceRef, error) {
	env, app, ok := strings.Cut(s, "/")
	if !ok || env == "" || app == "" || strings.Contains(app, "/") {
		return namespaceRef{}, fmt.Errorf("paramstore: invalid namespace %q, want \"env/app\"", s)
	}
	return namespaceRef{env: env, app: app}, nil
}

// splitDisplayPath parses a leading-slash display path "/env/app/key..." into an
// explicit ref. The key may contain interior slashes (e.g. "billing/stripe-key")
// but env and app are always the first two segments; the SDK never treats the
// key's interior slashes as namespace structure.
func splitDisplayPath(p string) (ref, error) {
	if !strings.HasPrefix(p, "/") {
		return ref{}, fmt.Errorf("paramstore: absolute key %q must start with %q", p, "/")
	}
	parts := strings.SplitN(strings.TrimPrefix(p, "/"), "/", 3)
	if len(parts) < 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return ref{}, fmt.Errorf("paramstore: absolute key %q must have the form \"/env/app/key\"", p)
	}
	return ref{ns: namespaceRef{env: parts[0], app: parts[1]}, key: parts[2]}, nil
}
