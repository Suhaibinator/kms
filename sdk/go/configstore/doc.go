// Package configstore provides the preparation and lifecycle runtime used by
// generated managed-configuration bindings.
//
// Generated bindings remain responsible for their immutable generation type,
// atomic pointer, snapshots, and zero-overhead getters. This package owns the
// release-loader bridge, startup readiness, default-drift policy, bounded
// rejection reporting, candidate cloning, and strict JSON decoding used while
// preparing a generation.
package configstore
