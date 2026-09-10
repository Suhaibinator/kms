package domain

import "time"

const ResourceReleaseInstance = "release_instance"

const OpConfigurationReleaseInstanceManage = "configuration-release:instance-manage"

// ReleaseSessionRef binds a process incarnation to an authenticated release scope.
type ReleaseSessionRef struct {
	Track      ReleaseTrack
	ClientName string
	InstanceID string
	SessionID  string
	Identity   string
}

type InstanceReleaseTarget struct {
	Release            ConfigurationRelease
	TargetRevision     uint64
	ActivationRevision uint64
	Pinned             bool
	PinRevision        uint64
	PinnedBy           string
	PinnedAt           time.Time
}
