package core

import (
	"context"
	"maps"
	"strconv"

	"github.com/Suhaibinator/kms/internal/domain"
)

// releaseAuditMetadata preserves existing fields while identifying the exact
// selected track. Zero is an explicit schema-free selection, not an omission.
// Only pass a revision established by an authoritative activation read/write.
func releaseAuditMetadata(schemaVersion, activationRevision uint64, metadata map[string]string) map[string]string {
	out := maps.Clone(metadata)
	if out == nil {
		out = make(map[string]string)
	}
	out["schema_version"] = strconv.FormatUint(schemaVersion, 10)
	if activationRevision != 0 {
		out["activation_revision"] = strconv.FormatUint(activationRevision, 10)
	}
	return out
}

type releaseAuditTrackKey struct{}
type releaseAuditSourceSchemaKey struct{}

// Carry numeric selection into shared authorization-denial audit emitters.
// This adds no authorization or namespace binding; downstream parameter/secret
// audits must not inherit the enclosing release's identity.
func withReleaseAuditTrack(ctx context.Context, track domain.ReleaseTrack) context.Context {
	return context.WithValue(ctx, releaseAuditTrackKey{}, track)
}

// A broad list must not inherit an earlier exact selection from its context.
func withReleaseAuditFilter(ctx context.Context, filter domain.ReleaseFilter) context.Context {
	if filter.Name != "" && filter.SchemaVersion != nil {
		return withReleaseAuditTrack(ctx, domain.ReleaseTrack{Namespace: filter.Namespace, Name: filter.Name, SchemaVersion: *filter.SchemaVersion})
	}
	return context.WithValue(ctx, releaseAuditTrackKey{}, struct{}{})
}

func scopedReleaseAuditMetadata(ctx context.Context, resourceType string, ref domain.Ref, metadata map[string]string) map[string]string {
	track, ok := ctx.Value(releaseAuditTrackKey{}).(domain.ReleaseTrack)
	if !ok || resourceType != domain.ResourceConfigurationRelease || track.Namespace != ref.NS || (ref.Key != "" && track.Name != ref.Key) {
		return metadata
	}
	return releaseAuditMetadata(track.SchemaVersion, 0, metadata)
}
