package paramstoretest

import (
	"testing"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
)

func TestActivateConfigurationReleaseNotifiesOnlyMatchingStreams(t *testing.T) {
	server, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	server.SetParameterVersion("prod/app", "settings", `{"enabled":true}`, "json", 1)

	matching := &ReleaseSubscription{
		Registration: &kmsv1.ReleaseWatchRegistration{Namespace: nsProto("prod/app"), Name: "runtime"},
		send:         make(chan *kmsv1.WatchReleaseEvent, 1),
	}
	otherName := &ReleaseSubscription{
		Registration: &kmsv1.ReleaseWatchRegistration{Namespace: nsProto("prod/app"), Name: "other"},
		send:         make(chan *kmsv1.WatchReleaseEvent, 1),
	}
	otherNamespace := &ReleaseSubscription{
		Registration: &kmsv1.ReleaseWatchRegistration{Namespace: nsProto("prod/other"), Name: "runtime"},
		send:         make(chan *kmsv1.WatchReleaseEvent, 1),
	}
	server.releaseSubs = []*ReleaseSubscription{matching, otherName, otherNamespace}

	_, err = server.ActivateConfigurationRelease(ReleaseSpec{
		Namespace: "prod/app",
		Name:      "runtime",
		Version:   1,
		Entries: []ReleaseEntrySpec{
			{Alias: "settings", Kind: "parameter", Path: "settings", Version: 1},
		},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}

	select {
	case <-matching.send:
	default:
		t.Fatal("matching release stream was not notified")
	}
	for name, sub := range map[string]*ReleaseSubscription{"other name": otherName, "other namespace": otherNamespace} {
		select {
		case <-sub.send:
			t.Errorf("%s stream received an unrelated activation", name)
		default:
		}
	}
}
