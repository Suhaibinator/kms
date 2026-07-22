package kmsclient

import (
	"context"
	"testing"
	"time"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
)

func TestFaultRunCancellationDuringPreparationAbortsLateCandidateAndKeepsLKG(t *testing.T) {
	server := newReleaseLoaderServer()
	firstRelease := testRelease(1, 1, "one")
	server.setActive(firstRelease, 1)
	server.parameters["settings"] = &kmsv1.Parameter{Ref: testResource("settings"), Value: "one", ContentType: "json", Version: 1}
	server.secrets["password"] = &kmsv1.GetSecretResponse{Ref: testResource("password"), Version: 1, Value: []byte("secret-one"), ContentType: "text/plain"}
	client := newReleaseTestClient(t, server)
	loader, err := NewReleaseLoader(client, ReleaseLoaderConfig{
		Name:                "runtime",
		SecretTokenProvider: func(string, string) (string, bool) { return "token", true },
	})
	if err != nil {
		t.Fatal(err)
	}

	firstPrepared := &testPreparedRelease{done: make(chan struct{})}
	latePrepared := &testPreparedRelease{done: make(chan struct{})}
	secondStarted := make(chan struct{})
	secondCanceled := make(chan struct{})
	allowLateReturn := make(chan struct{})
	lateReturnReleased := false
	t.Cleanup(func() {
		if !lateReturnReleased {
			close(allowLateReturn)
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	runErr := make(chan error, 1)
	go func() {
		runErr <- loader.Run(ctx, func(candidateCtx context.Context, snapshot ReleaseSnapshot) (PreparedRelease, error) {
			if snapshot.Version() == 1 {
				return firstPrepared, nil
			}
			close(secondStarted)
			<-candidateCtx.Done()
			close(secondCanceled)
			<-allowLateReturn // Deliberately return after Run has already stopped.
			return latePrepared, nil
		})
	}()
	select {
	case <-firstPrepared.done:
	case <-time.After(3 * time.Second):
		t.Fatal("initial release did not commit")
	}
	select {
	case <-server.watchRegs:
	case <-time.After(2 * time.Second):
		t.Fatal("release watch did not register")
	}

	secondRelease := testRelease(2, 2, "two")
	server.mu.Lock()
	server.parameters["settings"] = &kmsv1.Parameter{Ref: testResource("settings"), Value: "two", ContentType: "json", Version: 2}
	server.secrets["password"] = &kmsv1.GetSecretResponse{Ref: testResource("password"), Version: 2, Value: []byte("secret-two"), ContentType: "text/plain"}
	server.mu.Unlock()
	server.setActive(secondRelease, 2)
	server.watchEvents <- &kmsv1.WatchReleaseEvent{
		Event:    &kmsv1.WatchReleaseEvent_Activation{Activation: &kmsv1.ReleaseActivationEvent{Release: secondRelease}},
		Revision: 2,
	}
	select {
	case <-secondStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("second release did not enter preparation")
	}

	cancel()
	select {
	case <-secondCanceled:
	case <-time.After(2 * time.Second):
		t.Fatal("preparation context was not canceled")
	}
	select {
	case err := <-runErr:
		if err != context.Canceled {
			t.Fatalf("Run error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run waited for non-cooperative preparation to return")
	}
	if status := loader.Status(); status.AppliedVersion != 1 || status.AppliedRevision != 1 {
		t.Fatalf("cancellation changed LKG status: %+v", status)
	}

	close(allowLateReturn)
	lateReturnReleased = true
	deadline := time.Now().Add(2 * time.Second)
	for latePrepared.aborts.Load() != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if latePrepared.commits.Load() != 0 || latePrepared.aborts.Load() != 1 {
		t.Fatalf("late prepared candidate commit/abort = %d/%d, want 0/1", latePrepared.commits.Load(), latePrepared.aborts.Load())
	}
	if firstPrepared.commits.Load() != 1 || firstPrepared.aborts.Load() != 0 {
		t.Fatalf("LKG prepared release commit/abort = %d/%d, want 1/0", firstPrepared.commits.Load(), firstPrepared.aborts.Load())
	}
}
