package core

import (
	"context"
	"sort"
	"strconv"
	"strings"

	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/keyutil"
	"github.com/Suhaibinator/kms/internal/storage"
)

// ListReleaseSubscriberProjection classifies the complete population before
// paging effective instances. A cursor binds to the projection's content hash
// so a concurrent state change cannot silently skip or duplicate instances.
func (s *Service) ListReleaseSubscriberProjection(ctx context.Context, pr Principal, filter domain.ReleaseFilter, page storage.ListPage) (domain.SubscriberStreamSnapshot, string, error) {
	var out domain.SubscriberStreamSnapshot
	if err := s.authorizeSubscriberInspection(ctx, pr, filter.Namespace, filter.Name); err != nil {
		return out, "", err
	}
	if err := keyutil.ValidateNamespace(filter.Namespace); err != nil {
		return out, "", domain.Errorf(domain.ErrInvalidArgument, "%v", err)
	}
	rs, err := s.releaseStore()
	if err != nil {
		return out, "", err
	}
	acks, actives, err := rs.ReadReleaseProjection(ctx, filter)
	if err != nil {
		return out, "", err
	}
	tracks := map[domain.ReleaseTrack][]domain.ReleaseAcknowledgement{}
	for _, ack := range acks {
		if ack.SessionID == "" {
			continue
		}
		track := domain.ReleaseTrack{Namespace: ack.Namespace, Name: ack.ReleaseName, SchemaVersion: ack.SchemaVersion}
		tracks[track] = append(tracks[track], ack)
	}
	if filter.Name != "" && filter.SchemaVersion != nil {
		track := domain.ReleaseTrack{Namespace: filter.Namespace, Name: filter.Name, SchemaVersion: *filter.SchemaVersion}
		if _, active := actives[track]; !active && len(tracks[track]) == 0 {
			return out, "", domain.Errorf(domain.ErrNotFound, "release track has no active target or sessions")
		}
		if _, ok := tracks[track]; !ok {
			tracks[track] = nil
		}
	}
	out.ServerTime = s.now()
	out.Instances = []domain.SubscriberInstance{}
	out.Subscribers = []domain.ReleaseAcknowledgement{}
	out.Summary = domain.RolloutSummary{Complete: true, OtherReleaseNames: []string{}, RejectedInstances: []domain.SubscriberInstance{}}
	orderedTracks := make([]domain.ReleaseTrack, 0, len(tracks))
	for track := range tracks {
		orderedTracks = append(orderedTracks, track)
	}
	sort.Slice(orderedTracks, func(i, j int) bool {
		a, b := orderedTracks[i], orderedTracks[j]
		if a.Namespace.Env != b.Namespace.Env {
			return a.Namespace.Env < b.Namespace.Env
		}
		if a.Namespace.App != b.Namespace.App {
			return a.Namespace.App < b.Namespace.App
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.SchemaVersion < b.SchemaVersion
	})
	for _, track := range orderedTracks {
		rows := tracks[track]
		active := actives[track]
		if filter.Name != "" && filter.SchemaVersion != nil {
			out.CurrentRevision = active.ActivationRevision
		}
		instances := ProjectSubscriberInstances(rows, active.ActivationRevision, out.ServerTime, active.Release.Version)
		out.Instances = append(out.Instances, instances...)
		part, _ := computeRollout(rows, track.Name, active.ActivationRevision, out.ServerTime, active.Release.Version)
		out.Summary.Total += part.Total
		out.Summary.Connected += part.Connected
		out.Summary.AppliedCurrent += part.AppliedCurrent
		out.Summary.AppliedDivergent += part.AppliedDivergent
		out.Summary.Rejected += part.Rejected
		out.Summary.Pending += part.Pending
		out.Summary.Pinned += part.Pinned
		out.Summary.DifferentPins += part.DifferentPins
		out.Summary.Stale += part.Stale
		out.Summary.Unknown += part.Unknown
		for _, rejected := range part.RejectedInstances {
			if len(out.Summary.RejectedInstances) < maxRolloutInstanceFindings {
				out.Summary.RejectedInstances = append(out.Summary.RejectedInstances, rejected)
			} else {
				out.Summary.Truncated = true
			}
		}
		out.Summary.Truncated = out.Summary.Truncated || part.Truncated
	}
	sort.Slice(out.Instances, func(i, j int) bool {
		a, b := out.Instances[i], out.Instances[j]
		if a.Namespace.Env != b.Namespace.Env {
			return a.Namespace.Env < b.Namespace.Env
		}
		if a.Namespace.App != b.Namespace.App {
			return a.Namespace.App < b.Namespace.App
		}
		if a.ReleaseName != b.ReleaseName {
			return a.ReleaseName < b.ReleaseName
		}
		if a.SchemaVersion != b.SchemaVersion {
			return a.SchemaVersion < b.SchemaVersion
		}
		if a.Identity != b.Identity {
			return a.Identity < b.Identity
		}
		if a.ClientName != b.ClientName {
			return a.ClientName < b.ClientName
		}
		if a.InstanceID != b.InstanceID {
			return a.InstanceID < b.InstanceID
		}
		return a.SessionID < b.SessionID
	})
	out.ProjectionRevision = projectionRevision(out.Instances, out.CurrentRevision, filter)
	start := 0
	if page.Token != "" {
		parts := strings.Split(page.Token, ":")
		if len(parts) != 2 || parts[0] != out.ProjectionRevision {
			return domain.SubscriberStreamSnapshot{}, "", domain.Errorf(domain.ErrFailedPrecondition, "subscriber projection changed; restart pagination")
		}
		start, err = strconv.Atoi(parts[1])
		if err != nil || start < 0 || start > len(out.Instances) {
			return domain.SubscriberStreamSnapshot{}, "", domain.Errorf(domain.ErrInvalidArgument, "invalid subscriber cursor")
		}
	}
	limit := page.Limit
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	end := start + limit
	next := ""
	if end < len(out.Instances) {
		next = out.ProjectionRevision + ":" + strconv.Itoa(end)
	} else {
		end = len(out.Instances)
	}
	out.Instances = out.Instances[start:end]
	return out, next, nil
}
