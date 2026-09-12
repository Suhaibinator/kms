package core

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/storage"
)

// releaseDiffValueCapBytes is the largest parameter value the diff carries
// inline per side; above it the pin reports the size and digest only. It sits
// above the console highlighter's 200 KiB cap so a structural comparison is
// always possible for anything the response does include.
const releaseDiffValueCapBytes = 256 << 10

// releaseDiffValueBudgetBytes bounds the values of one response as a whole;
// rows are filled in alias order, so which rows fall past the budget is
// deterministic and the console's per-row "load value" fallback covers them.
const releaseDiffValueBudgetBytes = 4 << 20

// computeReleaseDiff pairs the entries of two releases by alias and classifies
// each pair. It reads no values: value changes are detected through the
// parameter digest each entry captured at release creation.
//
// crossEnvironment relaxes two rules for parameters compared across
// namespaces, where every ref differs by construction and version numbers
// are independent histories: the key reason compares the relative key only,
// and a version difference with an equal digest is not a change.
func computeReleaseDiff(from, to domain.ConfigurationRelease, crossEnvironment bool) []domain.ReleaseDiffRow {
	fromEntries := make(map[string]domain.ConfigurationReleaseEntry, len(from.Entries))
	for _, e := range from.Entries {
		fromEntries[e.Alias] = e
	}
	toEntries := make(map[string]domain.ConfigurationReleaseEntry, len(to.Entries))
	for _, e := range to.Entries {
		toEntries[e.Alias] = e
	}
	aliases := make([]string, 0, len(fromEntries)+len(toEntries))
	for alias := range fromEntries {
		aliases = append(aliases, alias)
	}
	for alias := range toEntries {
		if _, ok := fromEntries[alias]; !ok {
			aliases = append(aliases, alias)
		}
	}
	sort.Strings(aliases)

	rows := make([]domain.ReleaseDiffRow, 0, len(aliases))
	for _, alias := range aliases {
		before, hasBefore := fromEntries[alias]
		after, hasAfter := toEntries[alias]
		row := domain.ReleaseDiffRow{Alias: alias, Reasons: []string{}}
		switch {
		case !hasBefore:
			row.Kind, row.Change = after.Kind, domain.ReleaseDiffAdded
			row.To = &domain.ReleaseDiffPin{Entry: after}
		case !hasAfter:
			row.Kind, row.Change = before.Kind, domain.ReleaseDiffRemoved
			row.From = &domain.ReleaseDiffPin{Entry: before}
		default:
			row.Kind = after.Kind
			row.From = &domain.ReleaseDiffPin{Entry: before}
			row.To = &domain.ReleaseDiffPin{Entry: after}
			row.Reasons = entryChangeReasons(before, after, crossEnvironment)
			row.Change = domain.ReleaseDiffUnchanged
			if len(row.Reasons) > 0 {
				row.Change = domain.ReleaseDiffChanged
			}
		}
		rows = append(rows, row)
	}
	return rows
}

func entryChangeReasons(before, after domain.ConfigurationReleaseEntry, crossEnvironment bool) []string {
	reasons := []string{}
	if before.Kind != after.Kind {
		reasons = append(reasons, domain.ReleaseDiffReasonKind)
	}
	sameRef := before.Ref == after.Ref
	if crossEnvironment {
		sameRef = before.Ref.Key == after.Ref.Key
	}
	if !sameRef {
		reasons = append(reasons, domain.ReleaseDiffReasonKey)
	}
	if before.ContentType != after.ContentType {
		reasons = append(reasons, domain.ReleaseDiffReasonContentType)
	}
	parameter := before.Kind == domain.ReleaseEntryParameter && after.Kind == domain.ReleaseEntryParameter
	switch {
	case parameter && before.ParameterDigest != after.ParameterDigest:
		reasons = append(reasons, domain.ReleaseDiffReasonValue)
	case parameter && before.Version != after.Version && !crossEnvironment:
		reasons = append(reasons, domain.ReleaseDiffReasonPin)
	case !parameter && before.Version != after.Version:
		reasons = append(reasons, domain.ReleaseDiffReasonPin)
	}
	return reasons
}

// releaseDiffCounts folds the rows into the strip numbers. Attention is the
// server's share of the console's "needs attention" group: a kind or content
// type change, a to-side secret that is not enabled, or a changed parameter
// whose value could not be read.
func releaseDiffCounts(rows []domain.ReleaseDiffRow) domain.ReleaseDiffCounts {
	var c domain.ReleaseDiffCounts
	for _, row := range rows {
		switch row.Change {
		case domain.ReleaseDiffAdded:
			c.Added++
		case domain.ReleaseDiffRemoved:
			c.Removed++
		case domain.ReleaseDiffChanged:
			c.Changed++
		default:
			c.Unchanged++
		}
		if row.Change == domain.ReleaseDiffUnchanged {
			continue
		}
		if row.Kind == domain.ReleaseEntrySecret {
			c.SecretsChanged++
		}
		if releaseDiffRowNeedsAttention(row) {
			c.Attention++
		}
	}
	return c
}

func releaseDiffRowNeedsAttention(row domain.ReleaseDiffRow) bool {
	for _, reason := range row.Reasons {
		if reason == domain.ReleaseDiffReasonKind || reason == domain.ReleaseDiffReasonContentType {
			return true
		}
	}
	if row.To != nil && row.To.Entry.Kind == domain.ReleaseEntrySecret && row.To.SecretState != "" && row.To.SecretState != domain.StateEnabled {
		return true
	}
	if row.Change == domain.ReleaseDiffChanged && row.Kind == domain.ReleaseEntryParameter {
		for _, pin := range []*domain.ReleaseDiffPin{row.From, row.To} {
			if pin != nil && pin.ValueState == domain.ReleaseDiffValueUnavailable {
				return true
			}
		}
	}
	return false
}

// DiffConfigurationReleases compares two releases for the console: entries
// paired by alias, parameter values on the rows that differ (under the size
// cap and budget), per-version authorship, and secret metadata. Secret values
// are never read. Reading is authorized per track and then per pinned
// resource; a denied resource degrades its pin to "unavailable" instead of
// failing the comparison. No audit event is written, like GetParameter.
func (s *Service) DiffConfigurationReleases(ctx context.Context, pr Principal, in domain.ReleaseDiffInput) (domain.ReleaseDiff, error) {
	for _, track := range []domain.ReleaseTrack{in.From, in.To} {
		if err := validateReleaseAddress(track.Namespace, track.Name); err != nil {
			return domain.ReleaseDiff{}, err
		}
	}
	for _, sel := range []domain.ReleaseDiffSelector{in.FromSelector, in.ToSelector} {
		if (sel.Version == 0) == (sel.Label == "") {
			return domain.ReleaseDiff{}, domain.Errorf(domain.ErrInvalidArgument, "from and to must each be a version or the label current or previous")
		}
		if sel.Label != "" && sel.Label != domain.LabelCurrent && sel.Label != domain.LabelPrevious {
			return domain.ReleaseDiff{}, domain.Errorf(domain.ErrInvalidArgument, "release label must be current or previous")
		}
	}
	rs, err := s.releaseStore()
	if err != nil {
		return domain.ReleaseDiff{}, err
	}
	fromCtx, _, err := s.authorize(ctx, pr, domain.OpConfigurationReleaseRead, domain.ResourceConfigurationRelease, domain.Ref{NS: in.From.Namespace, Key: in.From.Name})
	if err != nil {
		return domain.ReleaseDiff{}, err
	}
	toCtx := fromCtx
	if in.To != in.From {
		if toCtx, _, err = s.authorize(ctx, pr, domain.OpConfigurationReleaseRead, domain.ResourceConfigurationRelease, domain.Ref{NS: in.To.Namespace, Key: in.To.Name}); err != nil {
			return domain.ReleaseDiff{}, err
		}
	}
	from, err := s.resolveReleaseDiffSide(fromCtx, rs, "from", in.From, in.FromSelector)
	if err != nil {
		return domain.ReleaseDiff{}, err
	}
	to, err := s.resolveReleaseDiffSide(toCtx, rs, "to", in.To, in.ToSelector)
	if err != nil {
		return domain.ReleaseDiff{}, err
	}
	if from.Release.Track() == to.Release.Track() && from.Release.Version == to.Release.Version {
		return domain.ReleaseDiff{}, domain.Errorf(domain.ErrInvalidArgument, "from and to resolve to the same release %s", releaseDiffLabel(from.Release))
	}

	crossEnvironment := from.Release.Namespace != to.Release.Namespace
	rows := computeReleaseDiff(from.Release, to.Release, crossEnvironment)
	s.fillReleaseDiffPins(ctx, pr, rows, in.IncludeValues)
	counts := releaseDiffCounts(rows)
	return domain.ReleaseDiff{
		From: from, To: to,
		Identical:        counts.Added == 0 && counts.Removed == 0 && counts.Changed == 0,
		SchemaChanged:    from.Release.SchemaVersion != to.Release.SchemaVersion,
		CrossEnvironment: crossEnvironment,
		Counts:           counts,
		Rows:             rows,
		ValueCapBytes:    releaseDiffValueCapBytes,
		ValuesIncluded:   in.IncludeValues,
	}, nil
}

func releaseDiffLabel(r domain.ConfigurationRelease) string {
	return fmt.Sprintf("%s@%d:%d", r.Name, r.SchemaVersion, r.Version)
}

// resolveReleaseDiffSide turns a selector into a release plus the track's
// label facts. Not-found errors name the side so the console can say which
// release is missing.
func (s *Service) resolveReleaseDiffSide(ctx context.Context, rs storage.ReleaseStore, side string, track domain.ReleaseTrack, sel domain.ReleaseDiffSelector) (domain.ReleaseDiffSide, error) {
	var active *domain.ActiveConfigurationRelease
	current, err := rs.GetActiveConfigurationRelease(ctx, track)
	switch {
	case err == nil:
		active = &current
	case errors.Is(err, domain.ErrNotFound):
	default:
		return domain.ReleaseDiffSide{}, err
	}

	version := sel.Version
	switch sel.Label {
	case domain.LabelCurrent:
		if active == nil {
			return domain.ReleaseDiffSide{}, domain.Errorf(domain.ErrNotFound, "%s release %s@%d has no active version", side, track.Name, track.SchemaVersion)
		}
		version = active.Release.Version
	case domain.LabelPrevious:
		if active == nil {
			return domain.ReleaseDiffSide{}, domain.Errorf(domain.ErrNotFound, "%s release %s@%d has no active version", side, track.Name, track.SchemaVersion)
		}
		if active.PreviousVersion == 0 {
			return domain.ReleaseDiffSide{}, domain.Errorf(domain.ErrFailedPrecondition, "no previous release: %s release %s@%d:%d is the first activation", side, track.Name, track.SchemaVersion, active.Release.Version)
		}
		version = active.PreviousVersion
	}

	var release domain.ConfigurationRelease
	if active != nil && active.Release.Version == version {
		release = active.Release
	} else {
		release, err = rs.GetConfigurationRelease(ctx, track, version)
		if errors.Is(err, domain.ErrNotFound) {
			return domain.ReleaseDiffSide{}, domain.Errorf(domain.ErrNotFound, "%s release %s@%d:%d not found", side, track.Name, track.SchemaVersion, version)
		}
		if err != nil {
			return domain.ReleaseDiffSide{}, err
		}
	}
	out := domain.ReleaseDiffSide{Release: release}
	if active != nil {
		out.PreviousVersion = active.PreviousVersion
		out.Previous = active.PreviousVersion == release.Version
		if active.Release.Version == release.Version {
			out.Current = true
			out.ActivationRevision = active.ActivationRevision
		}
	}
	return out, nil
}

// fillReleaseDiffPins attaches authorship, values and secret metadata to the
// pins of every row that differs. Unchanged rows stay entry-only; the console
// loads them on demand.
func (s *Service) fillReleaseDiffPins(ctx context.Context, pr Principal, rows []domain.ReleaseDiffRow, includeValues bool) {
	budget := releaseDiffValueBudgetBytes
	secrets := map[domain.Ref]*domain.Secret{}
	for i := range rows {
		row := &rows[i]
		for _, pin := range []*domain.ReleaseDiffPin{row.From, row.To} {
			if pin == nil {
				continue
			}
			switch {
			case pin.Entry.Kind == domain.ReleaseEntrySecret:
				pin.ValueState = domain.ReleaseDiffValueSecret
				if row.Change != domain.ReleaseDiffUnchanged {
					s.fillReleaseDiffSecret(ctx, pr, pin, secrets)
				}
			case row.Change == domain.ReleaseDiffUnchanged:
				pin.ValueState = domain.ReleaseDiffValueOmittedUnchanged
			case !includeValues:
				pin.ValueState = domain.ReleaseDiffValueOmittedRequest
			default:
				s.fillReleaseDiffParameter(ctx, pr, pin, &budget)
			}
		}
	}
}

func (s *Service) fillReleaseDiffParameter(ctx context.Context, pr Principal, pin *domain.ReleaseDiffPin, budget *int) {
	pin.ValueState = domain.ReleaseDiffValueUnavailable
	bound, _, err := s.authorize(ctx, pr, domain.OpParameterRead, domain.ResourceParameter, pin.Entry.Ref)
	if err != nil {
		return
	}
	param, err := s.store.GetParameter(bound, pin.Entry.Ref, pin.Entry.Version, "")
	if err != nil {
		return
	}
	pin.CreatedBy, pin.CreatedAt = param.CreatedBy, param.CreatedAt
	pin.ValueBytes = len(param.Value)
	if pin.ValueBytes > releaseDiffValueCapBytes || pin.ValueBytes > *budget {
		pin.ValueState = domain.ReleaseDiffValueOmittedSize
		return
	}
	*budget -= pin.ValueBytes
	pin.ValueState = domain.ReleaseDiffValuePresent
	pin.Value = param.Value
}

func (s *Service) fillReleaseDiffSecret(ctx context.Context, pr Principal, pin *domain.ReleaseDiffPin, cache map[domain.Ref]*domain.Secret) {
	info, seen := cache[pin.Entry.Ref]
	if !seen {
		bound, _, err := s.authorize(ctx, pr, domain.OpSecretRead, domain.ResourceSecret, pin.Entry.Ref)
		if err == nil {
			if loaded, err := s.store.GetSecretInfo(bound, pin.Entry.Ref); err == nil {
				info = &loaded
			}
		}
		cache[pin.Entry.Ref] = info
	}
	if info == nil {
		pin.ValueState = domain.ReleaseDiffValueUnavailable
		return
	}
	pin.Bound = info.Bound
	for _, ver := range info.Versions {
		if ver.Version != pin.Entry.Version {
			continue
		}
		pin.CreatedBy, pin.CreatedAt = ver.CreatedBy, ver.CreatedAt
		pin.SecretState = ver.State
		if !ver.DestroyedAt.IsZero() {
			pin.SecretState = domain.StateDestroyed
		}
		pin.Bound = ver.Bound
		pin.ExpiresAt = ver.ExpiresAt
		return
	}
	pin.ValueState = domain.ReleaseDiffValueUnavailable
}
