package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
	"github.com/Suhaibinator/kms/internal/core"
	"github.com/Suhaibinator/kms/internal/domain"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// Exercise actual stream handling, durable reduction, live registry and browser
// read model together: replay arrival order must not become lifecycle order.
func TestReleaseSessionReplayConsistencyOverRealKMS(t *testing.T) {
	env := newLoopbackTLSEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	admin := core.Principal{Identity: domain.Identity{Name: "network-root", Kind: domain.IdentityKindAdmin}, Method: domain.AuthMethodToken}
	ns := domain.NamespaceRef{Env: "prod", App: "replay-consistency"}
	_, schema, err := env.svc.CreateApplicationWithSchema(ctx, admin, domain.Application{Name: ns.App, ReleaseName: "runtime"}, `{"type":"object"}`, "{}")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = env.svc.CreateNamespace(ctx, admin, ns, "", []domain.AuthMethod{domain.AuthMethodToken}); err != nil {
		t.Fatal(err)
	}
	ref := domain.Ref{NS: ns, Key: "setting"}
	if _, _, err = env.svc.PutParameter(ctx, admin, ref, "1", "integer", "{}"); err != nil {
		t.Fatal(err)
	}
	rel, err := env.svc.CreateConfigurationRelease(ctx, admin, domain.CreateConfigurationReleaseInput{Namespace: ns, Name: "runtime", SchemaVersion: schema.Version, Entries: []domain.ReleaseEntrySelector{{Alias: "setting", Kind: domain.ReleaseEntryParameter, Ref: ref, Version: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = env.svc.ActivateConfigurationRelease(ctx, admin, domain.ReleaseTrack{Namespace: ns, Name: "runtime", SchemaVersion: schema.Version}, rel.Version, nil); err != nil {
		t.Fatal(err)
	}
	auth := networkAuthContext(ctx, env.adminToken)
	rpc := kmsv1.NewConfigurationReleaseServiceClient(env.adminConn)
	adminRPC := kmsv1.NewAdminServiceClient(env.adminConn)
	client := env.httpClient(nil)
	defer client.CloseIdleConnections()
	assertOverview := func(want string) bool {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, env.httpsURL("/api/v1/applications/overview?name="+ns.App), nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+env.adminToken)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		var body struct {
			Environments []struct {
				Status string `json:"status"`
			} `json:"environments"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("overview HTTP %d", resp.StatusCode)
		}
		return len(body.Environments) == 1 && body.Environments[0].Status == want
	}
	for index, order := range [][]int{{0, 1, 2}, {0, 2, 1}, {1, 0, 2}, {1, 2, 0}, {2, 0, 1}, {2, 1, 0}} {
		t.Run(fmt.Sprint(order), func(t *testing.T) {
			session := &kmsv1.ReleaseSessionRef{Namespace: networkNS(ns.Env, ns.App), Name: "runtime", SchemaVersion: &schema.Version, ClientName: "replay", InstanceId: "same-process-name", SessionId: fmt.Sprintf("replay-session-%d", index)}
			if _, err := rpc.RegisterReleaseSession(auth, &kmsv1.RegisterReleaseSessionRequest{Session: session}); err != nil {
				t.Fatal(err)
			}
			target, err := rpc.GetInstanceRelease(auth, &kmsv1.GetInstanceReleaseRequest{Session: session})
			if err != nil {
				t.Fatal(err)
			}
			open := func() (kmsv1.ConfigurationReleaseService_WatchReleaseClient, context.CancelFunc) {
				streamCtx, stop := context.WithCancel(auth)
				stream, err := rpc.WatchRelease(streamCtx)
				if err != nil {
					stop()
					t.Fatal(err)
				}
				if err := stream.Send(&kmsv1.WatchReleaseRequest{Request: &kmsv1.WatchReleaseRequest_Register{Register: &kmsv1.ReleaseWatchRegistration{Namespace: session.Namespace, Name: session.Name, SchemaVersion: session.SchemaVersion, ClientName: session.ClientName, InstanceId: session.InstanceId, SessionId: session.SessionId}}}); err != nil {
					stop()
					t.Fatal(err)
				}
				if _, err := stream.Recv(); err != nil {
					stop()
					t.Fatal(err)
				}
				return stream, stop
			}
			stream, stop := open()
			defer stop()
			acks := make([]*kmsv1.ReleaseAcknowledgement, 3)
			for i, state := range []string{"received", "prepared", "applied"} {
				acks[i] = &kmsv1.ReleaseAcknowledgement{Namespace: session.Namespace, Name: session.Name, SchemaVersion: schema.Version, ClientName: session.ClientName, InstanceId: session.InstanceId, SessionId: session.SessionId, Version: rel.Version, ActivationRevision: target.ActivationRevision, TargetRevision: target.TargetRevision, Sequence: uint64(i + 1), State: state, TimestampUnixMs: 1000 + int64(i)}
			}
			send := func(i int) {
				t.Helper()
				if err := stream.Send(&kmsv1.WatchReleaseRequest{Request: &kmsv1.WatchReleaseRequest_Acknowledgement{Acknowledgement: acks[i]}}); err != nil {
					t.Fatal(err)
				}
			}
			// An unavailable target produces a response after preceding ACKs have
			// been reduced. This is a stream barrier, avoiding sleeps or a poll
			// that accidentally observes the pre-replay applied state.
			barrier := func() {
				t.Helper()
				probe := proto.Clone(acks[0]).(*kmsv1.ReleaseAcknowledgement)
				probe.Sequence = 999
				probe.TargetRevision = target.TargetRevision + 100000
				if err := stream.Send(&kmsv1.WatchReleaseRequest{Request: &kmsv1.WatchReleaseRequest_Acknowledgement{Acknowledgement: probe}}); err != nil {
					t.Fatal(err)
				}
				for {
					event, err := stream.Recv()
					if err != nil {
						t.Fatal(err)
					}
					if event.GetAcknowledgementRejected() != nil {
						return
					}
				}
			}
			consistentState := func(state, classification, health string) bool {
				rows, err := adminRPC.ListReleaseSubscribers(auth, &kmsv1.ListReleaseSubscribersRequest{Namespace: session.Namespace, ReleaseName: session.Name, SchemaVersion: session.SchemaVersion})
				if err != nil {
					t.Fatal(err)
				}
				found := false
				var effective *kmsv1.ReleaseSubscriberState
				for _, row := range rows.Instances {
					if row.SessionId == session.SessionId {
						effective = row
						found = row.State == state && row.Classification == classification && row.LastAppliedVersion == rel.Version && row.Connected
					}
				}
				if !found {
					return false
				}
				summary := rows.Summary
				if summary == nil || !summary.Complete || summary.Connected != 1 || summary.Total != summary.AppliedCurrent+summary.Rejected+summary.Pending+summary.Pinned+summary.Stale+summary.Unknown {
					t.Fatalf("incomplete/non-partitioned summary: %v", summary)
				}
				live, _, err := env.svc.ListSubscribers(ctx, admin)
				if err != nil {
					t.Fatal(err)
				}
				found = false
				for _, row := range live {
					if row.ClientName == session.ClientName && row.InstanceID == session.InstanceId {
						found = row.ReleaseState == state && row.Effective != nil && row.Effective.Classification == effective.Classification && row.Effective.Reason == effective.Reason && row.Effective.Sequence == effective.Sequence
					}
				}
				return found && assertOverview(health)
			}
			consistent := func() bool { return consistentState("applied", "applied", "ready") }
			for _, i := range order {
				send(i)
			}
			barrier()
			waitForManagedState(t, consistent, "permuted events applied across all surfaces")
			stop()
			waitForManagedState(t, func() bool { return assertOverview("unknown") }, "disconnected history is unknown")
			if _, err := rpc.RegisterReleaseSession(auth, &kmsv1.RegisterReleaseSessionRequest{Session: session, Resume: true}); err != nil {
				t.Fatal(err)
			}
			stream, stop = open()
			defer stop()
			for _, i := range order {
				send(i)
			}
			barrier()
			waitForManagedState(t, consistent, "reconnect replay preserves applied")
			if index == 0 {
				oldStream, oldStop := stream, stop
				stream, stop = open()
				defer stop()
				late := proto.Clone(acks[0]).(*kmsv1.ReleaseAcknowledgement)
				late.Sequence = 100
				if err := oldStream.Send(&kmsv1.WatchReleaseRequest{Request: &kmsv1.WatchReleaseRequest_Acknowledgement{Acknowledgement: late}}); err != nil {
					t.Fatal(err)
				}
				for {
					_, err := oldStream.Recv()
					if err == nil {
						continue
					}
					if status.Code(err) != codes.Aborted {
						t.Fatalf("old stream fence: %v", err)
					}
					break
				}
				oldStop()
				barrier()
				waitForManagedState(t, consistent, "old acknowledgement and disconnect cannot change replacement stream")
				// A genuine retry is newer causal evidence. It must be able to
				// reject, retry preparation, then recover at this same target.
				for i, phase := range []struct{ state, classification, health string }{{"rejected", "rejected", "degraded"}, {"received", "pending", "rolling"}, {"applied", "applied", "ready"}} {
					ack := proto.Clone(acks[0]).(*kmsv1.ReleaseAcknowledgement)
					ack.Sequence, ack.State = uint64(i+4), phase.state
					if phase.state == "rejected" {
						ack.RejectionCategory = domain.ReleaseRejectPrepareFailed
					}
					if err := stream.Send(&kmsv1.WatchReleaseRequest{Request: &kmsv1.WatchReleaseRequest_Acknowledgement{Acknowledgement: ack}}); err != nil {
						t.Fatal(err)
					}
					barrier()
					waitForManagedState(t, func() bool { return consistentState(phase.state, phase.classification, phase.health) }, "same-target retry preserves applied evidence")
				}
				// Move to a newer target and reject it without erasing the last
				// confirmed applied release. Then roll back to the lower version
				// at a higher target revision and prove it converges normally.
				candidate, err := env.svc.CreateConfigurationRelease(ctx, admin, domain.CreateConfigurationReleaseInput{Namespace: ns, Name: "runtime", SchemaVersion: schema.Version, Entries: []domain.ReleaseEntrySelector{{Alias: "setting", Kind: domain.ReleaseEntryParameter, Ref: ref, Version: 1}}})
				if err != nil {
					t.Fatal(err)
				}
				track := domain.ReleaseTrack{Namespace: ns, Name: "runtime", SchemaVersion: schema.Version}
				if _, _, err := env.svc.ActivateConfigurationRelease(ctx, admin, track, candidate.Version, nil); err != nil {
					t.Fatal(err)
				}
				target, err = rpc.GetInstanceRelease(auth, &kmsv1.GetInstanceReleaseRequest{Session: session})
				if err != nil {
					t.Fatal(err)
				}
				for i, phase := range []struct{ state, classification, health string }{{"received", "pending", "rolling"}, {"rejected", "rejected", "degraded"}} {
					ack := proto.Clone(acks[0]).(*kmsv1.ReleaseAcknowledgement)
					ack.Sequence, ack.State, ack.Version, ack.TargetRevision, ack.ActivationRevision = uint64(i+7), phase.state, candidate.Version, target.TargetRevision, target.ActivationRevision
					if phase.state == "rejected" {
						ack.RejectionCategory = domain.ReleaseRejectPrepareFailed
					}
					if err := stream.Send(&kmsv1.WatchReleaseRequest{Request: &kmsv1.WatchReleaseRequest_Acknowledgement{Acknowledgement: ack}}); err != nil {
						t.Fatal(err)
					}
					barrier()
					waitForManagedState(t, func() bool { return consistentState(phase.state, phase.classification, phase.health) }, "new target retains prior application")
				}
				if _, _, err := env.svc.ActivateConfigurationRelease(ctx, admin, track, rel.Version, nil); err != nil {
					t.Fatal(err)
				}
				target, err = rpc.GetInstanceRelease(auth, &kmsv1.GetInstanceReleaseRequest{Session: session})
				if err != nil {
					t.Fatal(err)
				}
				rollback := proto.Clone(acks[2]).(*kmsv1.ReleaseAcknowledgement)
				rollback.Sequence, rollback.TargetRevision, rollback.ActivationRevision = 9, target.TargetRevision, target.ActivationRevision
				if err := stream.Send(&kmsv1.WatchReleaseRequest{Request: &kmsv1.WatchReleaseRequest_Acknowledgement{Acknowledgement: rollback}}); err != nil {
					t.Fatal(err)
				}
				barrier()
				waitForManagedState(t, consistent, "rollback to lower release version applies")
			}
			stop()
			waitForManagedState(t, func() bool { return assertOverview("unknown") }, "disconnect after replay")
		})
	}
	legacy, err := rpc.WatchRelease(auth)
	if err != nil {
		t.Fatal(err)
	}
	if err := legacy.Send(&kmsv1.WatchReleaseRequest{Request: &kmsv1.WatchReleaseRequest_Register{Register: &kmsv1.ReleaseWatchRegistration{Namespace: networkNS(ns.Env, ns.App), Name: "runtime", SchemaVersion: &schema.Version, ClientName: "legacy", InstanceId: "legacy"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Recv(); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("legacy watch must fail with upgrade error, got %v", err)
	}
}
