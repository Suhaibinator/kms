package integration

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/pem"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
	"github.com/Suhaibinator/kms/internal/core"
	"github.com/Suhaibinator/kms/internal/domain"
	paramstore "github.com/Suhaibinator/kms/sdk/go/paramstore"
)

func TestLoopbackSDKRequiresExplicitVerifiedTLS(t *testing.T) {
	e := newLoopbackTLSEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	adminCtx := networkAuthContext(ctx, e.adminToken)
	admin := kmsv1.NewAdminServiceClient(e.adminConn)
	if _, err := admin.CreateNamespace(adminCtx, &kmsv1.CreateNamespaceRequest{
		Ref: networkNS("prod", "sdk-tls"), AllowedAuthMethods: []string{string(domain.AuthMethodToken)},
	}); err != nil {
		t.Fatalf("create SDK namespace: %v", err)
	}
	if _, err := kmsv1.NewParameterServiceClient(e.adminConn).PutParameter(adminCtx, &kmsv1.PutParameterRequest{
		Ref: networkRef("prod", "sdk-tls", "mode"), Value: "verified", ContentType: "string",
	}); err != nil {
		t.Fatalf("seed SDK parameter: %v", err)
	}

	if client, err := paramstore.NewClient(paramstore.Config{
		Endpoint: e.endpoint(), Namespace: "prod/sdk-tls", Token: e.adminToken,
	}); err == nil {
		_ = client.Close()
		t.Fatal("SDK accepted an endpoint without an explicit transport-security choice")
	} else if !strings.Contains(err.Error(), "transport security is required") {
		t.Fatalf("implicit-transport error = %v", err)
	}

	// Explicit cleartext remains available for local development, but it cannot
	// accidentally speak to this TLS-only listener.
	insecureClient, err := paramstore.NewClient(paramstore.Config{
		Endpoint: e.endpoint(), Namespace: "prod/sdk-tls", Token: e.adminToken, Insecure: true,
		Timeout: time.Second,
	})
	if err != nil {
		t.Fatalf("construct explicitly insecure SDK client: %v", err)
	}
	defer func() { _ = insecureClient.Close() }()
	if value, err := insecureClient.GetParameter(ctx, "mode"); err == nil {
		t.Fatalf("cleartext SDK call crossed TLS listener and returned %q", value)
	}

	wrongNameTLS := e.clientTLS(nil)
	wrongNameTLS.ServerName = "not-localhost.invalid"
	wrongNameClient, err := paramstore.NewClient(paramstore.Config{
		Endpoint: e.endpoint(), Namespace: "prod/sdk-tls", Token: e.adminToken,
		TLS: wrongNameTLS, Timeout: time.Second,
	})
	if err != nil {
		t.Fatalf("construct wrong-hostname SDK client: %v", err)
	}
	defer func() { _ = wrongNameClient.Close() }()
	if value, err := wrongNameClient.GetParameter(ctx, "mode"); err == nil {
		t.Fatalf("SDK skipped hostname verification and returned %q", value)
	}

	client, err := paramstore.NewClient(paramstore.Config{
		Endpoint: e.endpoint(), Namespace: "prod/sdk-tls", Token: e.adminToken,
		TLS: e.clientTLS(nil), Timeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("construct verified TLS SDK client: %v", err)
	}
	defer func() { _ = client.Close() }()
	value, err := client.GetParameter(ctx, "mode")
	if err != nil {
		t.Fatalf("verified SDK read over loopback TLS: %v", err)
	}
	if value != "verified" {
		t.Fatalf("verified SDK value = %q, want verified", value)
	}
}

func TestLoopbackDelegatedListingsEnforceMethodAndDenyBoundaries(t *testing.T) {
	e := newLoopbackTLSEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rootCtx := networkAuthContext(ctx, e.adminToken)
	admin := kmsv1.NewAdminServiceClient(e.adminConn)
	params := kmsv1.NewParameterServiceClient(e.adminConn)

	for _, tc := range []struct {
		app     string
		methods []string
	}{
		{app: "visible-token", methods: []string{string(domain.AuthMethodToken)}},
		{app: "hidden-mtls", methods: []string{string(domain.AuthMethodMTLS)}},
		{app: "hidden-deny", methods: []string{string(domain.AuthMethodToken)}},
	} {
		if _, err := admin.CreateNamespace(rootCtx, &kmsv1.CreateNamespaceRequest{
			Ref: networkNS("prod", tc.app), AllowedAuthMethods: tc.methods,
		}); err != nil {
			t.Fatalf("create namespace %s: %v", tc.app, err)
		}
		if _, err := params.PutParameter(rootCtx, &kmsv1.PutParameterRequest{
			Ref: networkRef("prod", tc.app, "canary"), Value: tc.app, ContentType: "string",
		}); err != nil {
			t.Fatalf("seed namespace %s: %v", tc.app, err)
		}
	}

	created, err := admin.CreateIdentity(rootCtx, &kmsv1.CreateIdentityRequest{
		Name: "delegated-auditor", Kind: domain.IdentityKindClient,
		AuthMethods: []string{string(domain.AuthMethodToken)},
	})
	if err != nil {
		t.Fatalf("create delegated auditor: %v", err)
	}
	if created.GetToken() == "" {
		t.Fatal("delegated auditor did not receive a token")
	}
	if _, err := admin.CreatePolicy(rootCtx, &kmsv1.CreatePolicyRequest{Policy: &kmsv1.Policy{
		Name: "delegated-auditor-scope", Subject: "delegated-auditor",
		Allow: []*kmsv1.PolicyRule{
			{Operation: domain.OpParameterRead, Env: "*", App: "*"},
			{Operation: domain.OpAdminAuditRead, Env: "*", App: "*"},
		},
		Deny: []*kmsv1.PolicyRule{
			{Operation: "parameter:*", Env: "prod", App: "hidden-deny"},
			{Operation: "secret:*", Env: "prod", App: "hidden-deny"},
			{Operation: domain.OpAdminAuditRead, Env: "prod", App: "hidden-deny"},
		},
	}}); err != nil {
		t.Fatalf("create delegated policy: %v", err)
	}

	auditorCtx := networkAuthContext(ctx, created.GetToken())
	namespaces, err := admin.ListNamespaces(auditorCtx, &kmsv1.ListNamespacesRequest{PageSize: 100})
	if err != nil {
		t.Fatalf("delegated ListNamespaces: %v", err)
	}
	gotNamespaces := make(map[string]bool)
	for _, ns := range namespaces.GetNamespaces() {
		gotNamespaces[ns.GetRef().GetEnv()+"/"+ns.GetRef().GetApp()] = true
	}
	if !gotNamespaces["prod/visible-token"] {
		t.Fatalf("delegated namespaces = %v, missing legitimate token namespace", gotNamespaces)
	}
	for _, hidden := range []string{"prod/hidden-mtls", "prod/hidden-deny"} {
		if gotNamespaces[hidden] {
			t.Fatalf("delegated namespace listing crossed boundary into %s: %v", hidden, gotNamespaces)
		}
	}

	audits, err := admin.ListAuditEvents(auditorCtx, &kmsv1.ListAuditEventsRequest{PageSize: 1000})
	if err != nil {
		t.Fatalf("delegated broad ListAuditEvents: %v", err)
	}
	visibleAudit := false
	for _, event := range audits.GetEvents() {
		ns := event.GetResourceEnv() + "/" + event.GetResourceApp()
		switch ns {
		case "prod/visible-token":
			visibleAudit = true
		case "prod/hidden-mtls", "prod/hidden-deny":
			t.Fatalf("delegated audit listing crossed method/policy boundary: %+v", event)
		}
	}
	if !visibleAudit {
		t.Fatalf("delegated audit events omitted the legitimate token namespace: %+v", audits.GetEvents())
	}

	rootAudits, err := admin.ListAuditEvents(rootCtx, &kmsv1.ListAuditEventsRequest{PageSize: 1000})
	if err != nil {
		t.Fatalf("admin ListAuditEvents control: %v", err)
	}
	adminNamespaces := make(map[string]bool)
	for _, event := range rootAudits.GetEvents() {
		adminNamespaces[event.GetResourceEnv()+"/"+event.GetResourceApp()] = true
	}
	for _, want := range []string{"prod/visible-token", "prod/hidden-mtls", "prod/hidden-deny"} {
		if !adminNamespaces[want] {
			t.Fatalf("admin audit control missing %s; saw %v", want, adminNamespaces)
		}
	}
}

func TestLoopbackMTLSBindsExactLeafAndRevocation(t *testing.T) {
	e := newLoopbackTLSEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rootCtx := networkAuthContext(ctx, e.adminToken)
	admin := kmsv1.NewAdminServiceClient(e.adminConn)
	if _, err := admin.CreateNamespace(rootCtx, &kmsv1.CreateNamespaceRequest{
		Ref: networkNS("prod", "mtls-exact"), AllowedAuthMethods: []string{string(domain.AuthMethodMTLS)},
	}); err != nil {
		t.Fatalf("create mTLS namespace: %v", err)
	}
	identity, err := admin.CreateIdentity(rootCtx, &kmsv1.CreateIdentityRequest{
		Name: "mtls-exact-client", Kind: domain.IdentityKindClient,
		Namespace: networkNS("prod", "mtls-exact"), AuthMethods: []string{string(domain.AuthMethodMTLS)},
	})
	if err != nil {
		t.Fatalf("create mTLS identity: %v", err)
	}
	first := mustNetworkTLSCertificate(t, identity.GetCert())
	secondBundle, err := admin.IssueIdentityCertificate(rootCtx, &kmsv1.IssueIdentityCertificateRequest{Name: "mtls-exact-client"})
	if err != nil {
		t.Fatalf("issue rollover certificate: %v", err)
	}
	second := mustNetworkTLSCertificate(t, secondBundle.GetCert())

	firstConn := e.dial(t, &first)
	secondConn := e.dial(t, &second)
	firstAdmin := kmsv1.NewAdminServiceClient(firstConn)
	secondAdmin := kmsv1.NewAdminServiceClient(secondConn)
	assertWhoAmIMTLS := func(name string, client kmsv1.AdminServiceClient) {
		t.Helper()
		who, err := client.WhoAmI(ctx, &kmsv1.WhoAmIRequest{})
		if err != nil {
			t.Fatalf("%s WhoAmI: %v", name, err)
		}
		if who.GetName() != "mtls-exact-client" || who.GetAuthMethod() != string(domain.AuthMethodMTLS) {
			t.Fatalf("%s WhoAmI = %+v", name, who)
		}
	}
	assertWhoAmIMTLS("first cert", firstAdmin)
	assertWhoAmIMTLS("second cert", secondAdmin)

	// Model a different trusted leaf reusing the enrolled serial: the TLS
	// connection remains valid, but the enrollment fingerprint no longer equals
	// the presented leaf and every RPC must fail authentication.
	raw, err := sql.Open("sqlite", e.dbPath)
	if err != nil {
		t.Fatalf("open raw integration DB: %v", err)
	}
	defer func() { _ = raw.Close() }()
	if _, err := raw.ExecContext(ctx, `UPDATE identity_certs SET fingerprint = ? WHERE serial = ?`,
		strings.Repeat("0", 64), identity.GetCert().GetSerial()); err != nil {
		t.Fatalf("replace enrolled fingerprint: %v", err)
	}
	if _, err := firstAdmin.WhoAmI(ctx, &kmsv1.WhoAmIRequest{}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("fingerprint-mismatched leaf code = %v, want Unauthenticated (%v)", status.Code(err), err)
	}

	if _, err := raw.ExecContext(ctx, `UPDATE identity_certs SET fingerprint = ? WHERE serial = ?`,
		core.CertFingerprint(first.Leaf), identity.GetCert().GetSerial()); err != nil {
		t.Fatalf("restore enrolled fingerprint: %v", err)
	}
	assertWhoAmIMTLS("restored first cert", firstAdmin)

	if _, err := admin.RevokeIdentityCertificate(rootCtx, &kmsv1.RevokeIdentityCertificateRequest{
		Name: "mtls-exact-client", Serial: identity.GetCert().GetSerial(),
	}); err != nil {
		t.Fatalf("revoke first certificate: %v", err)
	}
	if _, err := firstAdmin.WhoAmI(ctx, &kmsv1.WhoAmIRequest{}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("revoked leaf code = %v, want Unauthenticated (%v)", status.Code(err), err)
	}
	assertWhoAmIMTLS("unrevoked rollover cert", secondAdmin)
}

func TestLoopbackWatchDoesNotCrossNamespaceRecreation(t *testing.T) {
	e := newLoopbackTLSEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rootCtx := networkAuthContext(ctx, e.adminToken)
	admin := kmsv1.NewAdminServiceClient(e.adminConn)
	if _, err := admin.CreateNamespace(rootCtx, &kmsv1.CreateNamespaceRequest{
		Ref: networkNS("prod", "watch-incarnation"), AllowedAuthMethods: []string{string(domain.AuthMethodToken)},
	}); err != nil {
		t.Fatalf("create watched namespace: %v", err)
	}

	stream, err := kmsv1.NewWatchServiceClient(e.adminConn).Subscribe(rootCtx)
	if err != nil {
		t.Fatalf("open watch stream: %v", err)
	}
	if err := stream.Send(&kmsv1.SubscribeRequest{
		ClientName: "incarnation-regression", Namespaces: []*kmsv1.NamespaceRef{networkNS("prod", "watch-incarnation")},
	}); err != nil {
		t.Fatalf("register watch stream: %v", err)
	}
	first, err := stream.Recv()
	if err != nil || first.GetSnapshot() == nil {
		t.Fatalf("initial watch event = %+v err=%v, want snapshot", first, err)
	}

	if _, err := admin.DeleteNamespace(rootCtx, &kmsv1.DeleteNamespaceRequest{Ref: networkNS("prod", "watch-incarnation")}); err != nil {
		t.Fatalf("delete watched namespace: %v", err)
	}
	if _, err := admin.CreateNamespace(rootCtx, &kmsv1.CreateNamespaceRequest{
		Ref: networkNS("prod", "watch-incarnation"), AllowedAuthMethods: []string{string(domain.AuthMethodToken)},
	}); err != nil {
		t.Fatalf("recreate watched namespace: %v", err)
	}
	const forbidden = "new-incarnation-must-not-reach-old-stream"
	if _, err := kmsv1.NewParameterServiceClient(e.adminConn).PutParameter(rootCtx, &kmsv1.PutParameterRequest{
		Ref: networkRef("prod", "watch-incarnation", "canary"), Value: forbidden, ContentType: "string",
	}); err != nil {
		t.Fatalf("write recreated namespace: %v", err)
	}

	for {
		event, err := stream.Recv()
		if err != nil {
			if status.Code(err) != codes.Aborted {
				t.Fatalf("stale watch closed with %v, want Aborted (%v)", status.Code(err), err)
			}
			break
		}
		if change := event.GetChange(); change != nil && change.GetValue() == forbidden {
			t.Fatalf("old watch received data from recreated namespace: %+v", change)
		}
	}
}

func TestLoopbackReleaseAcknowledgementsAreIdentityIsolated(t *testing.T) {
	e := newLoopbackTLSEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rootCtx := networkAuthContext(ctx, e.adminToken)
	admin := kmsv1.NewAdminServiceClient(e.adminConn)
	if _, err := admin.CreateNamespace(rootCtx, &kmsv1.CreateNamespaceRequest{
		Ref: networkNS("prod", "release-identity"), AllowedAuthMethods: []string{string(domain.AuthMethodToken)},
	}); err != nil {
		t.Fatalf("create release namespace: %v", err)
	}
	if _, err := kmsv1.NewParameterServiceClient(e.adminConn).PutParameter(rootCtx, &kmsv1.PutParameterRequest{
		Ref: networkRef("prod", "release-identity", "workers"), Value: "4", ContentType: "integer",
	}); err != nil {
		t.Fatalf("seed release parameter: %v", err)
	}
	releases := kmsv1.NewConfigurationReleaseServiceClient(e.adminConn)
	created, err := releases.CreateRelease(rootCtx, &kmsv1.CreateReleaseRequest{
		Namespace: networkNS("prod", "release-identity"), Name: "runtime",
		Entries: []*kmsv1.ReleaseEntrySelector{{
			Alias: "workers", Kind: domain.ReleaseEntryParameter,
			Ref: networkRef("prod", "release-identity", "workers"), Label: domain.LabelCurrent,
		}},
	})
	if err != nil {
		t.Fatalf("create release: %v", err)
	}
	active, err := releases.ActivateRelease(rootCtx, &kmsv1.ActivateReleaseRequest{
		Namespace: networkNS("prod", "release-identity"), Name: "runtime", Version: created.GetRelease().GetVersion(),
	})
	if err != nil || !active.GetChanged() {
		t.Fatalf("activate release = %+v err=%v", active, err)
	}

	tokens := make(map[string]string)
	for _, identity := range []string{"release-reader-a", "release-reader-b"} {
		res, err := admin.CreateIdentity(rootCtx, &kmsv1.CreateIdentityRequest{
			Name: identity, Kind: domain.IdentityKindClient,
			AuthMethods: []string{string(domain.AuthMethodToken)},
		})
		if err != nil {
			t.Fatalf("create %s: %v", identity, err)
		}
		tokens[identity] = res.GetToken()
	}
	if _, err := admin.CreatePolicy(rootCtx, &kmsv1.CreatePolicyRequest{Policy: &kmsv1.Policy{
		Name: "release-watchers", Subject: "*",
		Allow: []*kmsv1.PolicyRule{{Operation: domain.OpConfigurationReleaseWatch, Env: "prod", App: "release-identity"}},
	}}); err != nil {
		t.Fatalf("create release watch policy: %v", err)
	}

	type watchedRelease struct {
		identity string
		stream   kmsv1.ConfigurationReleaseService_WatchReleaseClient
		cancel   context.CancelFunc
	}
	watchers := make([]watchedRelease, 0, 2)
	for _, identity := range []string{"release-reader-a", "release-reader-b"} {
		watchCtx, watchCancel := context.WithCancel(networkAuthContext(ctx, tokens[identity]))
		stream, err := kmsv1.NewConfigurationReleaseServiceClient(e.adminConn).WatchRelease(watchCtx)
		if err != nil {
			watchCancel()
			t.Fatalf("open release watch for %s: %v", identity, err)
		}
		if err := stream.Send(&kmsv1.WatchReleaseRequest{Request: &kmsv1.WatchReleaseRequest_Register{
			Register: &kmsv1.ReleaseWatchRegistration{
				Namespace: networkNS("prod", "release-identity"), Name: "runtime",
				ClientName: "shared-client", InstanceId: "shared-instance",
			},
		}}); err != nil {
			watchCancel()
			t.Fatalf("register release watch for %s: %v", identity, err)
		}
		if event, err := stream.Recv(); err != nil || event.GetSnapshot() == nil {
			watchCancel()
			t.Fatalf("release snapshot for %s = %+v err=%v", identity, event, err)
		}
		watchers = append(watchers, watchedRelease{identity: identity, stream: stream, cancel: watchCancel})
	}
	defer func() {
		for _, watcher := range watchers {
			watcher.cancel()
		}
	}()

	ack := func(w watchedRelease, state string, clientTime time.Time) {
		t.Helper()
		if err := w.stream.Send(&kmsv1.WatchReleaseRequest{Request: &kmsv1.WatchReleaseRequest_Acknowledgement{
			Acknowledgement: &kmsv1.ReleaseAcknowledgement{
				Namespace: networkNS("prod", "release-identity"), Name: "runtime",
				Version: created.GetRelease().GetVersion(), ActivationRevision: active.GetActivationRevision(),
				ClientName: "shared-client", InstanceId: "shared-instance", State: state,
				TimestampUnixMs: clientTime.UnixMilli(),
			},
		}}); err != nil {
			t.Fatalf("send %s acknowledgement for %s: %v", state, w.identity, err)
		}
	}
	ack(watchers[0], domain.ReleaseStateReceived, time.Now())
	// An attacker-controlled future timestamp must not pin the row. The later
	// message is ordered by server receipt and therefore replaces it.
	ack(watchers[1], domain.ReleaseStatePrepared, time.Now().Add(365*24*time.Hour))
	wantClientTimestamp := time.Now().Add(-365 * 24 * time.Hour).Truncate(time.Millisecond)
	ack(watchers[1], domain.ReleaseStatePrepared, wantClientTimestamp)

	var rows []*kmsv1.ReleaseSubscriberState
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := admin.ListReleaseSubscribers(rootCtx, &kmsv1.ListReleaseSubscribersRequest{
			Namespace: networkNS("prod", "release-identity"), ReleaseName: "runtime", PageSize: 100,
		})
		if err != nil {
			t.Fatalf("list release subscribers: %v", err)
		}
		rows = resp.GetSubscribers()
		observed := make(map[string]*kmsv1.ReleaseSubscriberState)
		for _, row := range rows {
			observed[row.GetIdentity()] = row
		}
		if len(rows) == 2 && observed["release-reader-a"].GetState() == domain.ReleaseStateReceived &&
			observed["release-reader-b"].GetState() == domain.ReleaseStatePrepared &&
			observed["release-reader-b"].GetClientTimestampUnixMs() == wantClientTimestamp.UnixMilli() {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(rows) != 2 {
		t.Fatalf("shared client/instance collapsed identities: %+v", rows)
	}
	byIdentity := make(map[string]*kmsv1.ReleaseSubscriberState)
	for _, row := range rows {
		byIdentity[row.GetIdentity()] = row
		if row.GetClientName() != "shared-client" || row.GetInstanceId() != "shared-instance" || !row.GetConnected() {
			t.Fatalf("subscriber identity row lost connection identity: %+v", row)
		}
	}
	if byIdentity["release-reader-a"].GetState() != domain.ReleaseStateReceived {
		t.Fatalf("reader A acknowledgement overwritten: %+v", byIdentity["release-reader-a"])
	}
	readerB := byIdentity["release-reader-b"]
	if readerB.GetState() != domain.ReleaseStatePrepared {
		t.Fatalf("future client timestamp won over receipt order: %+v", readerB)
	}
	if readerB.GetClientTimestampUnixMs() != wantClientTimestamp.UnixMilli() {
		t.Fatalf("reader B client timestamp = %d, want %d", readerB.GetClientTimestampUnixMs(), wantClientTimestamp.UnixMilli())
	}
}

func TestLoopbackConcurrentClientBoundWritesCommitExactlyOneWinner(t *testing.T) {
	e := newLoopbackTLSEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	rootCtx := networkAuthContext(ctx, e.adminToken)
	admin := kmsv1.NewAdminServiceClient(e.adminConn)
	if _, err := admin.CreateNamespace(rootCtx, &kmsv1.CreateNamespaceRequest{
		Ref: networkNS("prod", "secret-race"), AllowedAuthMethods: []string{string(domain.AuthMethodToken)},
	}); err != nil {
		t.Fatalf("create secret race namespace: %v", err)
	}
	secrets := kmsv1.NewSecretServiceClient(e.adminConn)

	type writeResult struct {
		value string
		resp  *kmsv1.PutSecretResponse
		err   error
	}
	runConcurrent := func(secretToken string, count int) []writeResult {
		t.Helper()
		start := make(chan struct{})
		results := make(chan writeResult, count)
		var wg sync.WaitGroup
		for i := 0; i < count; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				value := fmt.Sprintf("candidate-%02d", i)
				callCtx := rootCtx
				if secretToken != "" {
					callCtx = networkSecretContext(ctx, e.adminToken, secretToken)
				}
				resp, err := secrets.PutSecret(callCtx, &kmsv1.PutSecretRequest{
					Ref: networkRef("prod", "secret-race", "bound"), Value: []byte(value),
					ContentType: "text/plain", ClientBound: true, GenerateAccessToken: true,
				})
				results <- writeResult{value: value, resp: resp, err: err}
			}(i)
		}
		close(start)
		wg.Wait()
		close(results)
		out := make([]writeResult, 0, count)
		for result := range results {
			out = append(out, result)
		}
		return out
	}

	assertOneWinner := func(phase string, results []writeResult, wantVersion uint64) writeResult {
		t.Helper()
		var winners []writeResult
		for _, result := range results {
			if result.err == nil {
				winners = append(winners, result)
				continue
			}
			switch status.Code(result.err) {
			case codes.Aborted, codes.PermissionDenied:
				// Aborted means the atomic expectation caught an overlapping
				// transaction; PermissionDenied means this request observed the
				// already-committed token before it entered its write.
			default:
				t.Fatalf("%s loser returned %v: %v", phase, status.Code(result.err), result.err)
			}
		}
		if len(winners) != 1 {
			t.Fatalf("%s winners = %+v, want exactly one", phase, winners)
		}
		winner := winners[0]
		if winner.resp.GetVersion() != wantVersion || winner.resp.GetAccessToken() == "" {
			t.Fatalf("%s winner = %+v, want version %d and one-time token", phase, winner, wantVersion)
		}
		return winner
	}

	created := assertOneWinner("concurrent create", runConcurrent("", 8), 1)
	metadata, err := secrets.GetSecretMetadata(rootCtx, &kmsv1.GetSecretMetadataRequest{
		Ref: networkRef("prod", "secret-race", "bound"),
	})
	if err != nil {
		t.Fatalf("metadata after concurrent create: %v", err)
	}
	if len(metadata.GetSecret().GetVersions()) != 1 || metadata.GetSecret().GetLabels()[domain.LabelCurrent] != 1 {
		t.Fatalf("concurrent create committed multiple versions: %+v", metadata.GetSecret())
	}
	createdRead, err := secrets.GetSecret(networkSecretContext(ctx, e.adminToken, created.resp.GetAccessToken()), &kmsv1.GetSecretRequest{
		Ref: networkRef("prod", "secret-race", "bound"),
	})
	if err != nil || string(createdRead.GetValue()) != created.value {
		t.Fatalf("read create winner = %q err=%v, want %q", createdRead.GetValue(), err, created.value)
	}

	rotated := assertOneWinner("concurrent token rotation", runConcurrent(created.resp.GetAccessToken(), 8), 2)
	metadata, err = secrets.GetSecretMetadata(rootCtx, &kmsv1.GetSecretMetadataRequest{
		Ref: networkRef("prod", "secret-race", "bound"),
	})
	if err != nil {
		t.Fatalf("metadata after concurrent rotation: %v", err)
	}
	if len(metadata.GetSecret().GetVersions()) != 2 || metadata.GetSecret().GetLabels()[domain.LabelCurrent] != 2 {
		t.Fatalf("concurrent rotation committed multiple versions: %+v", metadata.GetSecret())
	}
	rotatedRead, err := secrets.GetSecret(networkSecretContext(ctx, e.adminToken, rotated.resp.GetAccessToken()), &kmsv1.GetSecretRequest{
		Ref: networkRef("prod", "secret-race", "bound"),
	})
	if err != nil || string(rotatedRead.GetValue()) != rotated.value {
		t.Fatalf("read rotation winner = %q err=%v, want %q", rotatedRead.GetValue(), err, rotated.value)
	}
	if staleRead, err := secrets.GetSecret(networkSecretContext(ctx, e.adminToken, created.resp.GetAccessToken()), &kmsv1.GetSecretRequest{
		Ref: networkRef("prod", "secret-race", "bound"),
	}); err == nil {
		t.Fatalf("rotated-out token read current plaintext %q", staleRead.GetValue())
	}
}

func mustNetworkTLSCertificate(t *testing.T, bundle *kmsv1.CertBundle) tls.Certificate {
	t.Helper()
	if bundle == nil || bundle.GetCertPem() == "" || bundle.GetKeyPem() == "" || bundle.GetSerial() == "" {
		t.Fatalf("incomplete certificate bundle: %+v", bundle)
	}
	pair, err := tls.X509KeyPair([]byte(bundle.GetCertPem()), []byte(bundle.GetKeyPem()))
	if err != nil {
		t.Fatalf("load client certificate: %v", err)
	}
	block, _ := pem.Decode([]byte(bundle.GetCertPem()))
	if block == nil {
		t.Fatal("client certificate PEM is empty")
	}
	pair.Leaf, err = x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse client leaf: %v", err)
	}
	return pair
}
