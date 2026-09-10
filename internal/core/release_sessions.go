package core

import (
	"context"

	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/storage"
)

func (s *Service) sessionStore() (storage.ReleaseSessionStore, error) {
	rs, err := s.releaseStore()
	if err != nil {
		return nil, err
	}
	st, ok := rs.(storage.ReleaseSessionStore)
	if !ok {
		return nil, domain.Errorf(domain.ErrNotReady, "instance release sessions unavailable")
	}
	return st, nil
}
func (s *Service) authorizeSession(ctx context.Context, pr Principal, ref domain.ReleaseSessionRef, manage bool) (context.Context, error) {
	if err := validateReleaseAddress(ref.Track.Namespace, ref.Track.Name); err != nil {
		return ctx, err
	}
	for _, v := range []string{ref.SessionID, ref.InstanceID, ref.ClientName, ref.Identity} {
		if len(v) == 0 || len(v) > 128 {
			return ctx, domain.Errorf(domain.ErrInvalidArgument, "session identifiers must contain 1 to 128 bytes")
		}
	}
	ctx = withReleaseAuditTrack(ctx, ref.Track)
	op := domain.OpConfigurationReleaseRead
	if manage {
		op = domain.OpConfigurationReleaseInstanceManage
	} else if ref.Identity != pr.Identity.Name {
		return ctx, domain.Errorf(domain.ErrPermissionDenied, "release session identity mismatch")
	}
	ctx, _, err := s.authorize(ctx, pr, op, domain.ResourceConfigurationRelease, domain.Ref{NS: ref.Track.Namespace, Key: ref.Track.Name})
	return ctx, err
}
func (s *Service) RegisterReleaseSession(ctx context.Context, pr Principal, ref domain.ReleaseSessionRef, resume bool) error {
	ctx, err := s.authorizeSession(ctx, pr, ref, false)
	if err != nil {
		return err
	}
	if err := s.AuthorizeReleaseWatch(ctx, pr, ref.Track); err != nil {
		return err
	}
	if ref.Track.SchemaVersion > 0 {
		rs, e := s.releaseStore()
		if e != nil {
			return e
		}
		if _, e = rs.GetConfigurationSchema(ctx, ref.Track.Namespace.App, ref.Track.Name, ref.Track.SchemaVersion); e != nil {
			return e
		}
	}
	st, err := s.sessionStore()
	if err != nil {
		return err
	}
	return st.RegisterReleaseSession(ctx, ref, resume)
}
func (s *Service) GetInstanceRelease(ctx context.Context, pr Principal, ref domain.ReleaseSessionRef) (domain.InstanceReleaseTarget, error) {
	ctx, err := s.authorizeSession(ctx, pr, ref, false)
	if err != nil {
		return domain.InstanceReleaseTarget{}, err
	}
	st, err := s.sessionStore()
	if err != nil {
		return domain.InstanceReleaseTarget{}, err
	}
	return st.ResolveInstanceRelease(ctx, ref)
}
func (s *Service) SetReleasePin(ctx context.Context, pr Principal, ref domain.ReleaseSessionRef, version, expected uint64) (domain.InstanceReleaseTarget, error) {
	ctx, err := s.authorizeSession(ctx, pr, ref, true)
	if err != nil {
		return domain.InstanceReleaseTarget{}, err
	}
	if version > 0 {
		violations, err := s.ValidateConfigurationRelease(ctx, pr, ref.Track, version)
		if err != nil {
			return domain.InstanceReleaseTarget{}, err
		}
		if len(violations) > 0 {
			return domain.InstanceReleaseTarget{}, domain.Errorf(domain.ErrFailedPrecondition, "release validation failed: %s", violations[0].Code)
		}
	}
	st, err := s.sessionStore()
	if err != nil {
		return domain.InstanceReleaseTarget{}, err
	}
	ev := s.buildRefEvent(ctx, pr, "configuration_release.pin", domain.ResourceConfigurationRelease, domain.Ref{NS: ref.Track.Namespace, Key: ref.Track.Name}, version, "allow", nil)
	if version == 0 {
		ev.EventType = "configuration_release.unpin"
	}
	out, err := st.SetReleasePin(ctx, ref, version, expected, ev)
	if err == nil {
		s.notifyReleaseSubscribers(ref.Track)
	}
	return out, err
}
func (s *Service) ConnectReleaseSession(ctx context.Context, ref domain.ReleaseSessionRef, connection string, connected bool) error {
	st, err := s.sessionStore()
	if err != nil {
		return err
	}
	err = st.ConnectReleaseSession(ctx, ref, connection, connected)
	if err == nil {
		s.notifyReleaseSubscribers(ref.Track)
	}
	return err
}

func (s *Service) authorizeSubscriberInspection(ctx context.Context, pr Principal, ns domain.NamespaceRef, name string) error {
	if pr.IsAdmin() {
		return nil
	}
	if name == "" {
		return s.requireAdmin(ctx, pr, "configuration_release.subscribers", domain.ResourceConfigurationRelease, name)
	}
	_, _, err := s.authorize(ctx, pr, domain.OpConfigurationReleaseInstanceManage, domain.ResourceConfigurationRelease, domain.Ref{NS: ns, Key: name})
	if err != nil {
		s.auditRef(ctx, pr, "configuration_release.subscribers", domain.ResourceConfigurationRelease, domain.Ref{NS: ns, Key: name}, 0, "deny", nil)
	}
	return err
}
