package grpcserver

import (
	"context"

	"github.com/Suhaibinator/kms/internal/core"
)

// ctxKey is an unexported context key type to avoid collisions.
type ctxKey int

const (
	principalKey ctxKey = iota
	requestIDKey
)

// withRequestID stashes the per-request correlation ID.
func withRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey, id)
}

// requestIDFrom returns the per-request correlation ID, or "" if absent.
func requestIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey).(string)
	return id
}

// withPrincipal stashes the authenticated principal in the context.
func withPrincipal(ctx context.Context, pr core.Principal) context.Context {
	return context.WithValue(ctx, principalKey, pr)
}

// PrincipalFromContext returns the authenticated principal, if any. Handlers on
// authenticated methods can rely on it being present (the auth interceptor
// installs it before the handler runs).
func PrincipalFromContext(ctx context.Context) (core.Principal, bool) {
	pr, ok := ctx.Value(principalKey).(core.Principal)
	return pr, ok
}
