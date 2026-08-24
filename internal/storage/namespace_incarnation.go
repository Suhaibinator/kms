package storage

import (
	"context"
	"maps"

	"github.com/Suhaibinator/kms/internal/domain"
)

// namespaceIncarnationContextKey is deliberately private: callers can request
// a binding through BindNamespaceIncarnation, but no unrelated context value
// can accidentally collide with the storage enforcement state.
type namespaceIncarnationContextKey struct{}

type namespaceIncarnations map[domain.NamespaceRef]int64

// BindNamespaceIncarnation pins subsequent storage work in ctx to the exact
// immutable namespace row observed during authorization. Bindings accumulate
// so one operation (for example, configuration-release creation) can safely
// access resources in several namespaces.
//
// Rebinding an already-pinned name to another row fails closed. That catches a
// delete/recreate race even when the second authorization check happens in a
// different component later in the same request.
func BindNamespaceIncarnation(ctx context.Context, ns domain.NamespaceRef, id int64) (context.Context, error) {
	if id <= 0 {
		return ctx, domain.Errorf(domain.ErrAborted, "namespace %s changed during request; retry", ns)
	}
	if current, ok := ExpectedNamespaceIncarnation(ctx, ns); ok {
		if current != id {
			return ctx, domain.Errorf(domain.ErrAborted, "namespace %s changed during request; retry", ns)
		}
		return ctx, nil
	}

	bindings := make(namespaceIncarnations)
	if existing, ok := ctx.Value(namespaceIncarnationContextKey{}).(namespaceIncarnations); ok {
		bindings = make(namespaceIncarnations, len(existing)+1)
		maps.Copy(bindings, existing)
	}
	bindings[ns] = id
	return context.WithValue(ctx, namespaceIncarnationContextKey{}, bindings), nil
}

// ExpectedNamespaceIncarnation reports the immutable namespace row ID pinned
// in ctx. It is exported so the core audit path can stamp the same identity
// without performing a racy post-operation name lookup.
func ExpectedNamespaceIncarnation(ctx context.Context, ns domain.NamespaceRef) (int64, bool) {
	if ctx == nil {
		return 0, false
	}
	bindings, ok := ctx.Value(namespaceIncarnationContextKey{}).(namespaceIncarnations)
	if !ok {
		return 0, false
	}
	id, ok := bindings[ns]
	return id, ok
}
