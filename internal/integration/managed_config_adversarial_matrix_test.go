package integration

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
	fixtureconfig "github.com/Suhaibinator/kms/internal/configstorefixture/config"
	fixturekms "github.com/Suhaibinator/kms/internal/configstorefixture/configkms"
	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/sdk/go/configstore"
	"github.com/Suhaibinator/kms/sdk/go/kmsclient"
)

const adversarialReleaseName = "runtime"

type adversarialManagedApp struct {
	t             *testing.T
	env           *loopbackTLSEnv
	ctx           context.Context
	cancel        context.CancelFunc
	authCtx       context.Context
	app           string
	namespace     *kmsv1.NamespaceRef
	schemaID      string
	schemaVersion uint64

	parameters kmsv1.ParameterServiceClient
	secrets    kmsv1.SecretServiceClient
	releases   kmsv1.ConfigurationReleaseServiceClient
	admin      kmsv1.AdminServiceClient

	tokenMu       sync.RWMutex
	secretTokens  map[string]string
	providerCalls atomic.Uint64
}

type adversarialManagedPins struct {
	database uint64
	runtime  uint64
	password uint64
	token    uint64
}

type adversarialRunningStore struct {
	store     *fixturekms.Store
	client    *kmsclient.Client
	cancel    context.CancelFunc
	closeOnce sync.Once
}

type adversarialExpectedGeneration struct {
	features      string
	payload       string
	burst         uint64
	window        [2]float64
	secret        string
	secretVersion uint64
}

func registerAdversarialManagedSchema(t *testing.T, env *loopbackTLSEnv, schemaID string) uint64 {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	document, err := os.ReadFile("../configstorefixture/runtime.schema.json")
	if err != nil {
		t.Fatalf("read generated managed schema: %v", err)
	}
	response, err := kmsv1.NewConfigurationSchemaServiceClient(env.adminConn).CreateSchema(
		networkAuthContext(ctx, env.adminToken),
		&kmsv1.CreateSchemaRequest{Id: schemaID, SchemaJson: string(document), MetadataJson: `{"owner":"adversarial-integration"}`},
	)
	if err != nil {
		t.Fatalf("register adversarial managed schema: %v", err)
	}
	return response.GetSchema().GetVersion()
}

func newAdversarialManagedApp(
	t *testing.T,
	env *loopbackTLSEnv,
	schemaID string,
	schemaVersion uint64,
	app string,
) *adversarialManagedApp {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	authCtx := networkAuthContext(ctx, env.adminToken)
	namespace := networkNS("prod", app)
	admin := kmsv1.NewAdminServiceClient(env.adminConn)
	if _, err := admin.CreateNamespace(authCtx, &kmsv1.CreateNamespaceRequest{
		Ref: namespace, AllowedAuthMethods: []string{string(domain.AuthMethodToken)},
	}); err != nil {
		cancel()
		t.Fatalf("create adversarial namespace %s: %v", app, err)
	}
	h := &adversarialManagedApp{
		t: t, env: env, ctx: ctx, cancel: cancel, authCtx: authCtx, app: app, namespace: namespace,
		schemaID: schemaID, schemaVersion: schemaVersion,
		parameters:   kmsv1.NewParameterServiceClient(env.adminConn),
		secrets:      kmsv1.NewSecretServiceClient(env.adminConn),
		releases:     kmsv1.NewConfigurationReleaseServiceClient(env.adminConn),
		admin:        admin,
		secretTokens: make(map[string]string),
	}
	t.Cleanup(cancel)
	return h
}

func (h *adversarialManagedApp) putParameter(key, document string) uint64 {
	h.t.Helper()
	response, err := h.parameters.PutParameter(h.authCtx, &kmsv1.PutParameterRequest{
		Ref: networkRef("prod", h.app, key), Value: document, ContentType: "json",
	})
	if err != nil {
		h.t.Fatalf("put parameter %s: %v", key, err)
	}
	return response.GetVersion()
}

// seedParameterBypassingWriteValidation is reserved for defense-in-depth
// integration cases whose documents the public write API correctly rejects.
// Direct storage seeding models a pre-existing or externally migrated record
// so the managed schema and generated runtime still see the hostile value.
func (h *adversarialManagedApp) seedParameterBypassingWriteValidation(key, document string) uint64 {
	h.t.Helper()
	version, _, err := h.env.store.PutParameter(h.ctx, domain.Ref{
		NS:  domain.NamespaceRef{Env: "prod", App: h.app},
		Key: key,
	}, document, "json", "{}", "adversarial-integration-seed")
	if err != nil {
		h.t.Fatalf("seed parameter %s while bypassing write validation: %v", key, err)
	}
	return version
}

func (h *adversarialManagedApp) putSecret(alias, key, plaintext string) uint64 {
	h.t.Helper()
	h.tokenMu.RLock()
	oldToken := h.secretTokens[alias]
	h.tokenMu.RUnlock()
	rpcCtx := h.authCtx
	if oldToken != "" {
		rpcCtx = networkSecretContext(h.ctx, h.env.adminToken, oldToken)
	}
	response, err := h.secrets.PutSecret(rpcCtx, &kmsv1.PutSecretRequest{
		Ref: networkRef("prod", h.app, key), Value: []byte(plaintext),
		ContentType: "text/plain", GenerateAccessToken: true,
	})
	if err != nil {
		h.t.Fatalf("put secret %s: %v", key, err)
	}
	if response.GetAccessToken() == "" {
		h.t.Fatalf("put secret %s returned no access token", key)
	}
	h.tokenMu.Lock()
	h.secretTokens[alias] = response.GetAccessToken()
	h.tokenMu.Unlock()
	return response.GetVersion()
}

func (h *adversarialManagedApp) provider(alias, _ string) (string, bool) {
	h.providerCalls.Add(1)
	h.tokenMu.RLock()
	defer h.tokenMu.RUnlock()
	token, ok := h.secretTokens[alias]
	return token, ok
}

func (h *adversarialManagedApp) seed(databaseDocument, runtimeDocument string) adversarialManagedPins {
	h.t.Helper()
	return adversarialManagedPins{
		database: h.putParameter("groups/database", databaseDocument),
		runtime:  h.putParameter("groups/runtime", runtimeDocument),
		password: h.putSecret("database_password", "secrets/database_password", "adversarial-password-v1"),
		token:    h.putSecret("runtime_token", "secrets/runtime_token", "adversarial-runtime-token-v1"),
	}
}

func (h *adversarialManagedApp) standardEntries(pins adversarialManagedPins) []*kmsv1.ReleaseEntrySelector {
	return []*kmsv1.ReleaseEntrySelector{
		{Alias: "database", Kind: domain.ReleaseEntryParameter, Ref: networkRef("prod", h.app, "groups/database"), Version: pins.database},
		{Alias: "runtime", Kind: domain.ReleaseEntryParameter, Ref: networkRef("prod", h.app, "groups/runtime"), Version: pins.runtime},
		{Alias: "database_password", Kind: domain.ReleaseEntrySecret, Ref: networkRef("prod", h.app, "secrets/database_password"), Version: pins.password},
		{Alias: "runtime_token", Kind: domain.ReleaseEntrySecret, Ref: networkRef("prod", h.app, "secrets/runtime_token"), Version: pins.token},
	}
}

func (h *adversarialManagedApp) createRelease(
	pins adversarialManagedPins,
	mutate func([]*kmsv1.ReleaseEntrySelector),
) (*kmsv1.ConfigurationRelease, *kmsv1.ValidateReleaseResponse) {
	return h.createReleaseWithSchema(pins, mutate, true)
}

func (h *adversarialManagedApp) createReleaseWithSchema(
	pins adversarialManagedPins,
	mutate func([]*kmsv1.ReleaseEntrySelector),
	attachSchema bool,
) (*kmsv1.ConfigurationRelease, *kmsv1.ValidateReleaseResponse) {
	h.t.Helper()
	entries := h.standardEntries(pins)
	if mutate != nil {
		mutate(entries)
	}
	request := &kmsv1.CreateReleaseRequest{Namespace: h.namespace, Name: adversarialReleaseName, Entries: entries}
	if attachSchema {
		request.SchemaId = h.schemaID
		request.SchemaVersion = h.schemaVersion
	}
	created, err := h.releases.CreateRelease(h.authCtx, request)
	if err != nil {
		h.t.Fatalf("create managed release: %v", err)
	}
	release := created.GetRelease()
	validation, err := h.releases.ValidateRelease(h.authCtx, &kmsv1.ValidateReleaseRequest{
		Namespace: h.namespace, Name: adversarialReleaseName, Version: release.GetVersion(),
	})
	if err != nil {
		h.t.Fatalf("validate managed release %d: %v", release.GetVersion(), err)
	}
	return release, validation
}

func (h *adversarialManagedApp) activate(
	release *kmsv1.ConfigurationRelease,
	expected *uint64,
) (*kmsv1.ActivateReleaseResponse, error) {
	return h.releases.ActivateRelease(h.authCtx, &kmsv1.ActivateReleaseRequest{
		Namespace: h.namespace, Name: adversarialReleaseName,
		Version: release.GetVersion(), ExpectedCurrentVersion: expected,
	})
}

func (h *adversarialManagedApp) mustActivate(
	release *kmsv1.ConfigurationRelease,
	expected uint64,
) *kmsv1.ActivateReleaseResponse {
	h.t.Helper()
	response, err := h.activate(release, &expected)
	if err != nil {
		h.t.Fatalf("activate release %d from %d: %v", release.GetVersion(), expected, err)
	}
	if !response.GetChanged() {
		h.t.Fatalf("activate release %d reported no change", release.GetVersion())
	}
	return response
}

func (h *adversarialManagedApp) startStore(
	instanceID string,
	allowDefaultMismatch bool,
) (*adversarialRunningStore, error) {
	return h.startStoreWithDefaults(instanceID, allowDefaultMismatch, fixtureconfig.Defaults)
}

func (h *adversarialManagedApp) startStoreWithDefaults(
	instanceID string,
	allowDefaultMismatch bool,
	defaults func() *fixtureconfig.Config,
) (*adversarialRunningStore, error) {
	h.t.Helper()
	client, err := kmsclient.NewClient(kmsclient.Config{
		Endpoint: h.env.endpoint(), Namespace: "prod/" + h.app, Token: h.env.adminToken,
		TLS: h.env.clientTLS(nil), Timeout: 3 * time.Second, ClientName: "adversarial-managed-integration",
	})
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(h.ctx)
	store, err := fixturekms.Start(ctx, client, fixturekms.Options{
		Release: adversarialReleaseName, Defaults: defaults,
		AllowDefaultMismatch: allowDefaultMismatch,
		OnDefaultMismatch:    func(configstore.DefaultMismatchReport) {},
		SecretTokenProvider:  h.provider,
		ReconcileInterval:    time.Hour,
		InstanceID:           instanceID,
	})
	if err != nil {
		cancel()
		_ = client.Close()
		return nil, err
	}
	running := &adversarialRunningStore{store: store, client: client, cancel: cancel}
	h.t.Cleanup(func() { running.close(h.t) })
	return running, nil
}

func (r *adversarialRunningStore) close(t *testing.T) {
	t.Helper()
	r.closeOnce.Do(func() {
		r.cancel()
		if err := r.store.Wait(); err != nil {
			t.Errorf("managed store shutdown: %v", err)
		}
		if err := r.client.Close(); err != nil {
			t.Errorf("managed client close: %v", err)
		}
	})
}

func adversarialDatabaseDocument(ports, labels, zones, maxOpen, duration string) string {
	return fmt.Sprintf(
		`{"endpoint":{"host":"db.internal","ports":%s,"labels":%s,"zones":%s},"max_open":%s,"timeout":%s}`,
		ports, labels, zones, maxOpen, strconv.Quote(duration),
	)
}

func adversarialRuntimeDocument(features, payload, thresholds, window string) string {
	return fmt.Sprintf(
		`{"features":%s,"payload":%s,"thresholds":%s,"window":%s}`,
		features, payload, thresholds, window,
	)
}

func defaultAdversarialDatabaseDocument() string {
	return adversarialDatabaseDocument(
		`[5432,5433]`, `{"role":["primary","readonly"]}`,
		`["us-west-1a","us-west-1b"]`, `20`, `3s`,
	)
}

func defaultAdversarialRuntimeDocument() string {
	return adversarialRuntimeDocument(
		`["search","reports"]`, strconv.Quote(base64.StdEncoding.EncodeToString([]byte("fixture-payload"))),
		`{"burst":100,"steady":25}`, `[0.25,0.75]`,
	)
}

// TestManagedConfigAdversarialSchemaRuntimeParity drives the same documents
// through the registered Draft 2020-12 schema and the generated runtime. A
// representation accepted by the schema must be accepted by the decoder.
// Application-level validation remains a separate, later gate. Nil and empty
// Go collections have distinct canonical encodings and must remain distinct.
func TestManagedConfigAdversarialSchemaRuntimeParity(t *testing.T) {
	env := newLoopbackTLSEnv(t)
	schemaID := "managed-config/adversarial-parity"
	schemaVersion := registerAdversarialManagedSchema(t, env, schemaID)
	defaultDatabase := defaultAdversarialDatabaseDocument()
	defaultRuntime := defaultAdversarialRuntimeDocument()

	type parityCase struct {
		name               string
		database           string
		runtime            string
		runtimeWriteReject codes.Code
		schemaValid        bool
		runtimeApply       bool
		reject             configstore.RejectionCategory
		check              func(*testing.T, fixturekms.Snapshot)
	}
	cases := []parityCase{
		{name: "default", schemaValid: true, runtimeApply: true},
		{
			name: "empty map is represented exactly", schemaValid: true, runtimeApply: true,
			database: adversarialDatabaseDocument(`[5432]`, `{}`, `["us-west-1a","us-west-1b"]`, `20`, `3s`),
			check: func(t *testing.T, snapshot fixturekms.Snapshot) {
				if labels := snapshot.PersistenceHandler().Endpoint().Labels; labels == nil || len(labels) != 0 {
					t.Fatalf("empty labels decoded as %#v, want non-nil empty map", labels)
				}
			},
		},
		{
			name: "null map preserves nil", schemaValid: true, runtimeApply: true,
			database: adversarialDatabaseDocument(`[5432]`, `null`, `["us-west-1a","us-west-1b"]`, `20`, `3s`),
			check: func(t *testing.T, snapshot fixturekms.Snapshot) {
				if labels := snapshot.PersistenceHandler().Endpoint().Labels; labels != nil {
					t.Fatalf("null labels decoded as %#v, want nil map", labels)
				}
			},
		},
		{
			name: "nested null slice preserves nil", schemaValid: true, runtimeApply: true,
			database: adversarialDatabaseDocument(`[5432]`, `{"role":null}`, `["us-west-1a","us-west-1b"]`, `20`, `3s`),
			check: func(t *testing.T, snapshot fixturekms.Snapshot) {
				labels := snapshot.PersistenceHandler().Endpoint().Labels
				value, exists := labels["role"]
				if !exists || value != nil {
					t.Fatalf("nested null slice decoded as %#v, want present nil entry", labels)
				}
			},
		},
		{
			name: "nullable pointer", schemaValid: true, runtimeApply: true,
			database: adversarialDatabaseDocument(`[5432]`, `{}`, `["us-west-1a","us-west-1b"]`, `null`, `3s`),
			check: func(t *testing.T, snapshot fixturekms.Snapshot) {
				if got := snapshot.PersistenceHandler().MaxOpen(); got != 0 {
					t.Fatalf("nil max_open getter = %d, want zero-value projection", got)
				}
			},
		},
		{
			name: "uint16 maximum", schemaValid: true, runtimeApply: true,
			database: adversarialDatabaseDocument(`[65535]`, `{}`, `["us-west-1a","us-west-1b"]`, `20`, `3s`),
		},
		{
			name: "uint16 overflow", schemaValid: false, runtimeApply: false,
			database: adversarialDatabaseDocument(`[65536]`, `{}`, `["us-west-1a","us-west-1b"]`, `20`, `3s`),
			reject:   configstore.RejectConfigDecodeFailed,
		},
		{
			name: "portable int32 maximum", schemaValid: true, runtimeApply: true,
			database: adversarialDatabaseDocument(`[5432]`, `{}`, `["us-west-1a","us-west-1b"]`, `2147483647`, `3s`),
		},
		{
			name: "portable int32 overflow", schemaValid: false, runtimeApply: false,
			database: adversarialDatabaseDocument(`[5432]`, `{}`, `["us-west-1a","us-west-1b"]`, `2147483648`, `3s`),
			reject:   configstore.RejectConfigDecodeFailed,
		},
		{
			name: "uint64 maximum", schemaValid: true, runtimeApply: true,
			runtime: adversarialRuntimeDocument(`["max"]`, strconv.Quote("AA=="), `{"max":18446744073709551615}`, `[0.25,0.75]`),
		},
		{
			name: "uint64 overflow", schemaValid: false, runtimeApply: false,
			runtime: adversarialRuntimeDocument(`["overflow"]`, strconv.Quote("AA=="), `{"max":18446744073709551616}`, `[0.25,0.75]`),
			reject:  configstore.RejectConfigDecodeFailed,
		},
		{
			name: "mathematically integral exponent", schemaValid: true, runtimeApply: true,
			runtime: adversarialRuntimeDocument(`["exponent"]`, strconv.Quote("AA=="), `{"exact":1e3}`, `[0.25,0.75]`),
		},
		{
			name: "fractional unsigned integer", schemaValid: false, runtimeApply: false,
			runtime: adversarialRuntimeDocument(`["fractional"]`, strconv.Quote("AA=="), `{"bad":1e-1}`, `[0.25,0.75]`),
			reject:  configstore.RejectConfigDecodeFailed,
		},
		{
			name: "float64 maximum", schemaValid: true, runtimeApply: true,
			runtime: adversarialRuntimeDocument(`["float-max"]`, strconv.Quote("AA=="), `{"ok":1}`, `[1.7976931348623157e308,-1.7976931348623157e308]`),
		},
		{
			name: "float64 overflow", schemaValid: false, runtimeApply: false,
			runtime:            adversarialRuntimeDocument(`["float-overflow"]`, strconv.Quote("AA=="), `{"ok":1}`, `[1.7976931348623159e308,0]`),
			runtimeWriteReject: codes.InvalidArgument,
			reject:             configstore.RejectConfigDecodeFailed,
		},
		{
			name: "float64 underflow rounds consistently", schemaValid: true, runtimeApply: true,
			runtime: adversarialRuntimeDocument(`["float-underflow"]`, strconv.Quote("AA=="), `{"ok":1}`, `[1e-400,0]`),
			check: func(t *testing.T, snapshot fixturekms.Snapshot) {
				if got := snapshot.BackgroundJobs().Window()[0]; got != 0 {
					t.Fatalf("underflowed float = %v, want 0", got)
				}
			},
		},
		{
			name: "canonical padded base64", schemaValid: true, runtimeApply: true,
			runtime: adversarialRuntimeDocument(`["base64"]`, strconv.Quote("AA=="), `{"ok":1}`, `[0.25,0.75]`),
			check: func(t *testing.T, snapshot fixturekms.Snapshot) {
				if got := snapshot.ApiHandler().Payload(); len(got) != 1 || got[0] != 0 {
					t.Fatalf("decoded payload = %v, want one zero byte", got)
				}
			},
		},
		{
			name: "base64 nonzero pad bits", schemaValid: false, runtimeApply: false,
			runtime: adversarialRuntimeDocument(`["base64"]`, strconv.Quote("AB=="), `{"ok":1}`, `[0.25,0.75]`),
			reject:  configstore.RejectConfigDecodeFailed,
		},
		{
			name: "base64 unpadded", schemaValid: false, runtimeApply: false,
			runtime: adversarialRuntimeDocument(`["base64"]`, strconv.Quote("Zg"), `{"ok":1}`, `[0.25,0.75]`),
			reject:  configstore.RejectConfigDecodeFailed,
		},
		{
			name: "base64 whitespace", schemaValid: false, runtimeApply: false,
			runtime: adversarialRuntimeDocument(`["base64"]`, strconv.Quote("Z g=="), `{"ok":1}`, `[0.25,0.75]`),
			reject:  configstore.RejectConfigDecodeFailed,
		},
		{
			name: "maximum duration", schemaValid: true, runtimeApply: true,
			database: adversarialDatabaseDocument(`[5432]`, `{}`, `["us-west-1a","us-west-1b"]`, `20`, `2562047h47m16.854775807s`),
		},
		{
			name: "duration overflow", schemaValid: false, runtimeApply: false,
			database: adversarialDatabaseDocument(`[5432]`, `{}`, `["us-west-1a","us-west-1b"]`, `20`, `2562047h47m16.854775808s`),
			reject:   configstore.RejectConfigDecodeFailed,
		},
		{
			name: "fractional duration spelling", schemaValid: true, runtimeApply: true,
			database: adversarialDatabaseDocument(`[5432]`, `{}`, `["us-west-1a","us-west-1b"]`, `20`, `1.0s`),
		},
		{
			name: "negative duration is semantic validation", schemaValid: true, runtimeApply: false,
			database: adversarialDatabaseDocument(`[5432]`, `{}`, `["us-west-1a","us-west-1b"]`, `20`, `-1ns`),
			reject:   configstore.RejectConfigValidationFailed,
		},
		{
			name: "empty slice remains non-nil", schemaValid: true, runtimeApply: true,
			runtime: adversarialRuntimeDocument(`[]`, strconv.Quote("AA=="), `{"ok":1}`, `[0.25,0.75]`),
			check: func(t *testing.T, snapshot fixturekms.Snapshot) {
				if features := snapshot.ApiHandler().Features(); features == nil || len(features) != 0 {
					t.Fatalf("empty features decoded as %#v, want non-nil empty slice", features)
				}
			},
		},
		{
			name: "null slice preserves nil", schemaValid: true, runtimeApply: true,
			runtime: adversarialRuntimeDocument(`null`, strconv.Quote("AA=="), `{"ok":1}`, `[0.25,0.75]`),
			check: func(t *testing.T, snapshot fixturekms.Snapshot) {
				if features := snapshot.ApiHandler().Features(); features != nil {
					t.Fatalf("null features decoded as %#v, want nil slice", features)
				}
			},
		},
		{
			name: "empty byte string remains non-nil", schemaValid: true, runtimeApply: true,
			runtime: adversarialRuntimeDocument(`["payload"]`, strconv.Quote(""), `{"ok":1}`, `[0.25,0.75]`),
			check: func(t *testing.T, snapshot fixturekms.Snapshot) {
				if payload := snapshot.ApiHandler().Payload(); payload == nil || len(payload) != 0 {
					t.Fatalf("empty payload decoded as %#v, want non-nil empty bytes", payload)
				}
			},
		},
		{
			name: "null bytes preserve nil", schemaValid: true, runtimeApply: true,
			runtime: adversarialRuntimeDocument(`["payload"]`, `null`, `{"ok":1}`, `[0.25,0.75]`),
			check: func(t *testing.T, snapshot fixturekms.Snapshot) {
				if payload := snapshot.ApiHandler().Payload(); payload != nil {
					t.Fatalf("null payload decoded as %#v, want nil bytes", payload)
				}
			},
		},
		{
			name: "empty object remains non-nil", schemaValid: true, runtimeApply: true,
			runtime: adversarialRuntimeDocument(`["thresholds"]`, strconv.Quote("AA=="), `{}`, `[0.25,0.75]`),
			check: func(t *testing.T, snapshot fixturekms.Snapshot) {
				if thresholds := snapshot.ApiHandler().Thresholds(); thresholds == nil || len(thresholds) != 0 {
					t.Fatalf("empty thresholds decoded as %#v, want non-nil empty map", thresholds)
				}
			},
		},
		{
			name: "null object preserves nil", schemaValid: true, runtimeApply: true,
			runtime: adversarialRuntimeDocument(`["thresholds"]`, strconv.Quote("AA=="), `null`, `[0.25,0.75]`),
			check: func(t *testing.T, snapshot fixturekms.Snapshot) {
				if thresholds := snapshot.ApiHandler().Thresholds(); thresholds != nil {
					t.Fatalf("null thresholds decoded as %#v, want nil map", thresholds)
				}
			},
		},
		{
			name: "empty required ports is semantic validation", schemaValid: true, runtimeApply: false,
			database: adversarialDatabaseDocument(`[]`, `{}`, `["us-west-1a","us-west-1b"]`, `20`, `3s`),
			reject:   configstore.RejectConfigValidationFailed,
		},
		{
			name: "null required ports is semantic validation", schemaValid: true, runtimeApply: false,
			database: adversarialDatabaseDocument(`null`, `{}`, `["us-west-1a","us-west-1b"]`, `20`, `3s`),
			reject:   configstore.RejectConfigValidationFailed,
		},
		{
			name: "wrong fixed array length", schemaValid: false, runtimeApply: false,
			database: adversarialDatabaseDocument(`[5432]`, `{}`, `["us-west-1a"]`, `20`, `3s`),
			reject:   configstore.RejectConfigDecodeFailed,
		},
	}

	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			database := tc.database
			if database == "" {
				database = defaultDatabase
			}
			runtimeDocument := tc.runtime
			if runtimeDocument == "" {
				runtimeDocument = defaultRuntime
			}
			app := newAdversarialManagedApp(t, env, schemaID, schemaVersion, fmt.Sprintf("managed-parity-%02d", i))
			seedRuntime := runtimeDocument
			if tc.runtimeWriteReject != codes.OK {
				seedRuntime = defaultRuntime
			}
			pins := app.seed(database, seedRuntime)
			if tc.runtimeWriteReject != codes.OK {
				response, err := app.parameters.PutParameter(app.authCtx, &kmsv1.PutParameterRequest{
					Ref: networkRef("prod", app.app, "groups/runtime"), Value: runtimeDocument, ContentType: "json",
				})
				if status.Code(err) != tc.runtimeWriteReject {
					t.Fatalf("write-layer overflow response=%+v error=%v code=%s, want %s", response, err, status.Code(err), tc.runtimeWriteReject)
				}
				pins.runtime = app.seedParameterBypassingWriteValidation("groups/runtime", runtimeDocument)
			}
			release, validation := app.createRelease(pins, nil)
			if validation.GetValid() != tc.schemaValid {
				t.Fatalf("schema validity = %t errors=%v, want %t", validation.GetValid(), validation.GetErrors(), tc.schemaValid)
			}
			zero := uint64(0)
			activation, err := app.activate(release, &zero)
			if !tc.schemaValid {
				if status.Code(err) != codes.FailedPrecondition || activation != nil {
					t.Fatalf("schema-invalid activation response=%+v err=%v code=%s, want FailedPrecondition", activation, err, status.Code(err))
				}
				return
			}
			if err != nil || !activation.GetChanged() {
				t.Fatalf("activate release changed=%t err=%v", activation.GetChanged(), err)
			}
			running, startErr := app.startStore(fmt.Sprintf("parity-%02d", i), true)
			if tc.runtimeApply {
				if startErr != nil {
					t.Fatalf("runtime rejected schema-accepted document: %v", startErr)
				}
				if got := running.store.Current().Release().Version(); got != release.GetVersion() {
					t.Fatalf("applied release = %d, want %d", got, release.GetVersion())
				}
				if tc.check != nil {
					tc.check(t, running.store.Current())
				}
				return
			}
			if startErr == nil {
				running.close(t)
				t.Fatalf("runtime accepted document expected to be rejected as %s", tc.reject)
			}
			if !strings.Contains(startErr.Error(), string(tc.reject)) {
				t.Fatalf("runtime rejection = %v, want bounded category %s", startErr, tc.reject)
			}
		})
	}

	nilDefaultCases := []struct {
		name        string
		database    string
		runtime     string
		setDefault  func(*fixtureconfig.Config)
		assertIsNil func(*testing.T, fixturekms.Snapshot)
	}{
		{
			name:     "nested map",
			database: adversarialDatabaseDocument(`[5432,5433]`, `null`, `["us-west-1a","us-west-1b"]`, `20`, `3s`),
			setDefault: func(value *fixtureconfig.Config) {
				value.Endpoint.Labels = nil
			},
			assertIsNil: func(t *testing.T, snapshot fixturekms.Snapshot) {
				if labels := snapshot.PersistenceHandler().Endpoint().Labels; labels != nil {
					t.Fatalf("published labels = %#v, want nil", labels)
				}
			},
		},
		{
			name:    "root slice",
			runtime: adversarialRuntimeDocument(`null`, strconv.Quote(base64.StdEncoding.EncodeToString([]byte("fixture-payload"))), `{"burst":100,"steady":25}`, `[0.25,0.75]`),
			setDefault: func(value *fixtureconfig.Config) {
				value.Features = nil
			},
			assertIsNil: func(t *testing.T, snapshot fixturekms.Snapshot) {
				if features := snapshot.ApiHandler().Features(); features != nil {
					t.Fatalf("published features = %#v, want nil", features)
				}
			},
		},
		{
			name:    "root bytes",
			runtime: adversarialRuntimeDocument(`["search","reports"]`, `null`, `{"burst":100,"steady":25}`, `[0.25,0.75]`),
			setDefault: func(value *fixtureconfig.Config) {
				value.Payload = nil
			},
			assertIsNil: func(t *testing.T, snapshot fixturekms.Snapshot) {
				if payload := snapshot.ApiHandler().Payload(); payload != nil {
					t.Fatalf("published payload = %#v, want nil", payload)
				}
			},
		},
		{
			name:    "root map",
			runtime: adversarialRuntimeDocument(`["search","reports"]`, strconv.Quote(base64.StdEncoding.EncodeToString([]byte("fixture-payload"))), `null`, `[0.25,0.75]`),
			setDefault: func(value *fixtureconfig.Config) {
				value.Thresholds = nil
			},
			assertIsNil: func(t *testing.T, snapshot fixturekms.Snapshot) {
				if thresholds := snapshot.ApiHandler().Thresholds(); thresholds != nil {
					t.Fatalf("published thresholds = %#v, want nil", thresholds)
				}
			},
		},
	}
	for index, tc := range nilDefaultCases {
		t.Run("nil collection matches nil application default/"+tc.name, func(t *testing.T) {
			database := tc.database
			if database == "" {
				database = defaultDatabase
			}
			runtimeDocument := tc.runtime
			if runtimeDocument == "" {
				runtimeDocument = defaultRuntime
			}
			app := newAdversarialManagedApp(t, env, schemaID, schemaVersion, fmt.Sprintf("managed-parity-nil-default-%02d", index))
			pins := app.seed(database, runtimeDocument)
			release, validation := app.createRelease(pins, nil)
			if !validation.GetValid() {
				t.Fatalf("nil-default release invalid: %v", validation.GetErrors())
			}
			app.mustActivate(release, 0)
			defaults := func() *fixtureconfig.Config {
				value := fixtureconfig.Defaults()
				tc.setDefault(value)
				return value
			}
			running, err := app.startStoreWithDefaults(fmt.Sprintf("parity-nil-default-%02d", index), false, defaults)
			if err != nil {
				t.Fatalf("strict startup rejected matching nil default: %v", err)
			}
			if running.store.Status().DefaultDivergent {
				t.Fatal("matching nil collection was reported as default divergence")
			}
			tc.assertIsNil(t, running.store.Current())
		})
	}
}

// TestManagedConfigAdversarialServerGuardsRecoveryAndRedaction verifies that
// the application contract rejects shape drift before client secret-token
// lookup, then exercises schema rejection, recovery, redacted status, and
// normal terminal cancellation.
func TestManagedConfigAdversarialServerGuardsRecoveryAndRedaction(t *testing.T) {
	env := newLoopbackTLSEnv(t)
	schemaID := "managed-config/adversarial-recovery"
	schemaVersion := registerAdversarialManagedSchema(t, env, schemaID)
	app := newAdversarialManagedApp(t, env, schemaID, schemaVersion, "managed-recovery")
	pins := app.seed(defaultAdversarialDatabaseDocument(), defaultAdversarialRuntimeDocument())
	initialRelease, validation := app.createRelease(pins, nil)
	if !validation.GetValid() {
		t.Fatalf("initial release invalid: %v", validation.GetErrors())
	}
	initialActivation := app.mustActivate(initialRelease, 0)
	running, err := app.startStore("adversarial-recovery-instance", false)
	if err != nil {
		t.Fatalf("start managed store: %v", err)
	}
	initialSnapshot := running.store.Current()
	app.providerCalls.Store(0)

	contractEntries := app.standardEntries(pins)
	contractEntries[1].Alias = "runtime_unexpected"
	if _, createErr := app.releases.CreateRelease(app.authCtx, &kmsv1.CreateReleaseRequest{
		Namespace: app.namespace, Name: adversarialReleaseName, Entries: contractEntries,
		SchemaId: app.schemaID, SchemaVersion: app.schemaVersion,
	}); status.Code(createErr) != codes.FailedPrecondition {
		t.Fatalf("create contract-drift release error = %v, want failed precondition", createErr)
	}
	if calls := app.providerCalls.Load(); calls != 0 {
		t.Fatalf("contract-rejected candidate performed %d secret token lookups; want zero prefetch work", calls)
	}
	if got := running.store.Current().Release().Version(); got != initialRelease.GetVersion() {
		t.Fatalf("contract rejection displaced LKG with release %d", got)
	}

	const parameterCanary = "ACK_PARAMETER_VALUE_MUST_NOT_LEAK"
	invalidPins := pins
	invalidPins.runtime = app.putParameter(
		"groups/runtime",
		adversarialRuntimeDocument(`["`+parameterCanary+`"]`, strconv.Quote("AA=="), `{"ok":1}`, `[0.25]`),
	)
	invalidRelease, invalidValidation := app.createRelease(invalidPins, nil)
	if invalidValidation.GetValid() {
		t.Fatal("schema-invalid release unexpectedly passed server validation")
	}
	expectedInitialVersion := initialRelease.GetVersion()
	if _, activateErr := app.releases.ActivateRelease(app.authCtx, &kmsv1.ActivateReleaseRequest{
		Namespace: app.namespace, Name: adversarialReleaseName, Version: invalidRelease.GetVersion(),
		ExpectedCurrentVersion: &expectedInitialVersion,
	}); status.Code(activateErr) != codes.FailedPrecondition {
		t.Fatalf("activate schema-invalid release error = %v, want failed precondition", activateErr)
	}
	if got := running.store.Current().Release().Version(); got != initialRelease.GetVersion() {
		t.Fatalf("invalid candidate displaced LKG with release %d", got)
	}
	statusSnapshot := running.store.Status()
	statusJSON, err := json.Marshal(statusSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	statsJSON, err := json.Marshal(running.store.Stats())
	if err != nil {
		t.Fatal(err)
	}
	serialized := []string{
		fmt.Sprint(invalidValidation.GetErrors()), fmt.Sprint(statusSnapshot), fmt.Sprintf("%+v", statusSnapshot),
		string(statusJSON), fmt.Sprint(running.store.Stats()), string(statsJSON),
	}
	for _, sensitive := range []string{parameterCanary, "adversarial-password-v1", "adversarial-runtime-token-v1"} {
		for _, rendered := range serialized {
			if strings.Contains(rendered, sensitive) {
				t.Fatalf("bounded acknowledgement/status surface leaked %q: %s", sensitive, rendered)
			}
		}
	}

	recoveryPins := pins
	recoveryPins.runtime = app.putParameter(
		"groups/runtime",
		adversarialRuntimeDocument(`["recovered"]`, strconv.Quote(base64.StdEncoding.EncodeToString([]byte("recovered-payload"))), `{"burst":777}`, `[0.4,0.6]`),
	)
	recoveryRelease, recoveryValidation := app.createRelease(recoveryPins, nil)
	if !recoveryValidation.GetValid() {
		t.Fatalf("recovery release invalid: %v", recoveryValidation.GetErrors())
	}
	app.mustActivate(recoveryRelease, initialRelease.GetVersion())
	waitForManagedState(t, func() bool {
		return running.store.Current().Release().Version() == recoveryRelease.GetVersion()
	}, "valid recovery after rejected candidates")
	if got := initialSnapshot.ApiHandler().Features(); len(got) != 2 || got[0] != "search" {
		t.Fatalf("captured LKG snapshot mutated after recovery: %v", got)
	}
	if initialActivation.GetActivationRevision() == running.store.Current().Release().ActivationRevision() {
		t.Fatal("recovery did not advance activation revision")
	}

	running.close(t)
	if err := running.store.Wait(); err != nil {
		t.Fatalf("second Wait after normal cancellation: %v", err)
	}
	if got := running.store.Current().Release().Version(); got != recoveryRelease.GetVersion() {
		t.Fatalf("terminal store lost final immutable snapshot: %d", got)
	}
}

// TestManagedConfigAdversarialExactSecretVersionsAndMixedAtomicity proves that
// releases use exact secret pins across token rotation, hot secret changes are
// publishable, and a mixed hot/restart candidate cannot leak its hot subset.
func TestManagedConfigAdversarialExactSecretVersionsAndMixedAtomicity(t *testing.T) {
	env := newLoopbackTLSEnv(t)
	schemaID := "managed-config/adversarial-secrets"
	schemaVersion := registerAdversarialManagedSchema(t, env, schemaID)
	app := newAdversarialManagedApp(t, env, schemaID, schemaVersion, "managed-secret-versions")
	pins := app.seed(defaultAdversarialDatabaseDocument(), defaultAdversarialRuntimeDocument())
	initialRelease, _ := app.createRelease(pins, nil)
	app.mustActivate(initialRelease, 0)
	running, err := app.startStore("adversarial-secret-instance", false)
	if err != nil {
		t.Fatalf("start managed store: %v", err)
	}
	oldSnapshot := running.store.Current()

	runtimeTokenV2 := app.putSecret("runtime_token", "secrets/runtime_token", "adversarial-runtime-token-v2")
	if runtimeTokenV2 != 2 {
		t.Fatalf("runtime token version = %d, want 2", runtimeTokenV2)
	}
	v2Pins := pins
	v2Pins.token = runtimeTokenV2
	v2Release, validation := app.createRelease(v2Pins, nil)
	if !validation.GetValid() {
		t.Fatalf("runtime token v2 release invalid: %v", validation.GetErrors())
	}
	app.mustActivate(v2Release, initialRelease.GetVersion())
	waitForManagedState(t, func() bool { return running.store.Current().Release().Version() == v2Release.GetVersion() }, "hot secret v2")
	if secret := running.store.Current().ApiHandler().RuntimeToken(); secret.Version() != 2 || secret.StringValue() != "adversarial-runtime-token-v2" {
		t.Fatalf("published runtime secret = version %d value %q", secret.Version(), secret.StringValue())
	}
	if secret := oldSnapshot.ApiHandler().RuntimeToken(); secret.Version() != 1 || secret.StringValue() != "adversarial-runtime-token-v1" {
		t.Fatalf("captured old secret changed = version %d value %q", secret.Version(), secret.StringValue())
	}

	passwordV2 := app.putSecret("database_password", "secrets/database_password", "adversarial-password-v2")
	mixedPins := v2Pins
	mixedPins.password = passwordV2
	mixedPins.runtime = app.putParameter(
		"groups/runtime",
		adversarialRuntimeDocument(`["mixed-hot-must-not-leak"]`, strconv.Quote(base64.StdEncoding.EncodeToString([]byte("mixed-payload"))), `{"burst":9001}`, `[0.9,0.1]`),
	)
	mixedRelease, mixedValidation := app.createRelease(mixedPins, nil)
	if !mixedValidation.GetValid() {
		t.Fatalf("mixed release invalid: %v", mixedValidation.GetErrors())
	}
	app.mustActivate(mixedRelease, v2Release.GetVersion())
	waitForManagedState(t, func() bool {
		status := running.store.Status()
		return status.Observed.Version() == mixedRelease.GetVersion() &&
			status.LastRejectionCategory == configstore.RejectRestartRequired
	}, "mixed hot/restart rejection")
	lkg := running.store.Current()
	if lkg.Release().Version() != v2Release.GetVersion() {
		t.Fatalf("mixed candidate displaced LKG with release %d", lkg.Release().Version())
	}
	if got := lkg.ApiHandler().Features(); len(got) != 2 || got[0] != "search" {
		t.Fatalf("hot subset leaked from restart-rejected candidate: %v", got)
	}
	if secret := lkg.PersistenceHandler().Password(); secret.Version() != 1 || secret.StringValue() != "adversarial-password-v1" {
		t.Fatalf("restart secret leaked from rejected candidate: version=%d value=%q", secret.Version(), secret.StringValue())
	}

	// The newly rotated per-secret credential must still resolve the exact old
	// version pinned by this recovery release.
	recoveryPins := mixedPins
	recoveryPins.password = pins.password
	recoveryRelease, recoveryValidation := app.createRelease(recoveryPins, nil)
	if !recoveryValidation.GetValid() {
		t.Fatalf("mixed recovery release invalid: %v", recoveryValidation.GetErrors())
	}
	app.mustActivate(recoveryRelease, mixedRelease.GetVersion())
	waitForManagedState(t, func() bool { return running.store.Current().Release().Version() == recoveryRelease.GetVersion() }, "exact old password pin recovery")
	recovered := running.store.Current()
	if password := recovered.PersistenceHandler().Password(); password.Version() != 1 || password.StringValue() != "adversarial-password-v1" {
		t.Fatalf("recovery resolved wrong password pin: version=%d value=%q", password.Version(), password.StringValue())
	}
	if got := recovered.ApiHandler().Features(); len(got) != 1 || got[0] != "mixed-hot-must-not-leak" {
		t.Fatalf("valid hot recovery features = %v", got)
	}

	rollbackPins := recoveryPins
	rollbackPins.token = pins.token
	rollbackRelease, rollbackValidation := app.createRelease(rollbackPins, nil)
	if !rollbackValidation.GetValid() {
		t.Fatalf("runtime secret rollback release invalid: %v", rollbackValidation.GetErrors())
	}
	app.mustActivate(rollbackRelease, recoveryRelease.GetVersion())
	waitForManagedState(t, func() bool { return running.store.Current().Release().Version() == rollbackRelease.GetVersion() }, "exact runtime secret v1 rollback")
	if secret := running.store.Current().ApiHandler().RuntimeToken(); secret.Version() != 1 || secret.StringValue() != "adversarial-runtime-token-v1" {
		t.Fatalf("runtime rollback resolved wrong secret: version=%d value=%q", secret.Version(), secret.StringValue())
	}
}

// TestManagedConfigAdversarialRapidCASAndReaders stresses stale CAS guards,
// replace-latest supersession, atomic snapshots, and stale-preparation safety
// while a burst of real activation events crosses SQLite, gRPC, and TLS.
func TestManagedConfigAdversarialRapidCASAndReaders(t *testing.T) {
	env := newLoopbackTLSEnv(t)
	schemaID := "managed-config/adversarial-concurrency"
	schemaVersion := registerAdversarialManagedSchema(t, env, schemaID)
	app := newAdversarialManagedApp(t, env, schemaID, schemaVersion, "managed-rapid-activations")
	pins := app.seed(defaultAdversarialDatabaseDocument(), defaultAdversarialRuntimeDocument())
	initialRelease, _ := app.createRelease(pins, nil)
	initialActivation := app.mustActivate(initialRelease, 0)
	running, err := app.startStore("adversarial-rapid-instance", false)
	if err != nil {
		t.Fatalf("start managed store: %v", err)
	}
	capturedInitial := running.store.Current()

	expected := map[uint64]adversarialExpectedGeneration{
		initialRelease.GetVersion(): {
			features: "search", payload: "fixture-payload", burst: 100,
			window: [2]float64{0.25, 0.75}, secret: "adversarial-runtime-token-v1", secretVersion: 1,
		},
	}
	const candidateCount = 14
	candidates := make([]*kmsv1.ConfigurationRelease, 0, candidateCount)
	latestPins := pins
	for i := 1; i <= candidateCount; i++ {
		feature := fmt.Sprintf("generation-%02d", i)
		payload := fmt.Sprintf("payload-%02d", i)
		secretValue := fmt.Sprintf("runtime-secret-%02d", i+1)
		latestPins.runtime = app.putParameter(
			"groups/runtime",
			adversarialRuntimeDocument(
				fmt.Sprintf(`[%q]`, feature),
				strconv.Quote(base64.StdEncoding.EncodeToString([]byte(payload))),
				fmt.Sprintf(`{"burst":%d}`, 1000+i),
				fmt.Sprintf(`[%0.2f,%0.2f]`, float64(i)/100, float64(100-i)/100),
			),
		)
		latestPins.token = app.putSecret("runtime_token", "secrets/runtime_token", secretValue)
		release, validation := app.createRelease(latestPins, nil)
		if !validation.GetValid() {
			t.Fatalf("rapid release %d invalid: %v", i, validation.GetErrors())
		}
		candidates = append(candidates, release)
		expected[release.GetVersion()] = adversarialExpectedGeneration{
			features: feature, payload: payload, burst: uint64(1000 + i),
			window: [2]float64{float64(i) / 100, float64(100-i) / 100},
			secret: secretValue, secretVersion: uint64(i + 1),
		}
	}

	wrongExpected := initialRelease.GetVersion() + 999
	if response, err := app.activate(candidates[0], &wrongExpected); status.Code(err) != codes.Aborted {
		t.Fatalf("stale CAS activation response=%+v error=%v code=%s, want Aborted", response, err, status.Code(err))
	}
	activeAfterCAS, err := app.releases.GetActiveRelease(app.authCtx, &kmsv1.GetActiveReleaseRequest{
		Namespace: app.namespace, Name: adversarialReleaseName,
	})
	if err != nil {
		t.Fatalf("get active after stale CAS: %v", err)
	}
	if activeAfterCAS.GetRelease().GetVersion() != initialRelease.GetVersion() || activeAfterCAS.GetActivationRevision() != initialActivation.GetActivationRevision() {
		t.Fatalf("stale CAS changed active release: %+v", activeAfterCAS)
	}

	var stopReaders atomic.Bool
	readerFailures := make(chan string, 1)
	var readers sync.WaitGroup
	for range 24 {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for !stopReaders.Load() {
				snapshot := running.store.Current()
				want, ok := expected[snapshot.Release().Version()]
				if !ok {
					select {
					case readerFailures <- fmt.Sprintf("unknown release version %d", snapshot.Release().Version()):
					default:
					}
					return
				}
				api := snapshot.ApiHandler()
				jobs := snapshot.BackgroundJobs()
				features := api.Features()
				payload := string(api.Payload())
				burst := jobs.Thresholds()["burst"]
				window := jobs.Window()
				secret := api.RuntimeToken()
				if len(features) == 0 || features[0] != want.features || payload != want.payload ||
					burst != want.burst || window != want.window || secret.StringValue() != want.secret ||
					secret.Version() != want.secretVersion {
					select {
					case readerFailures <- fmt.Sprintf(
						"mixed generation release=%d features=%v payload=%q burst=%d window=%v secretVersion=%d wantFeatures=%q wantPayload=%q wantBurst=%d wantWindow=%v wantSecretVersion=%d",
						snapshot.Release().Version(), features, payload, burst, window, secret.Version(),
						want.features, want.payload, want.burst, want.window, want.secretVersion,
					):
					default:
					}
					return
				}
			}
		}()
	}
	stopReadersAndWait := func() {
		stopReaders.Store(true)
		readers.Wait()
	}
	t.Cleanup(stopReadersAndWait)

	expectedCurrent := initialRelease.GetVersion()
	for _, release := range candidates {
		app.mustActivate(release, expectedCurrent)
		expectedCurrent = release.GetVersion()
	}
	latest := candidates[len(candidates)-1]
	waitForManagedState(t, func() bool { return running.store.Current().Release().Version() == latest.GetVersion() }, "latest rapid activation")
	if got := running.store.Current().Release().Version(); got != latest.GetVersion() {
		t.Fatalf("stale preparation overwrote latest release: got %d want %d", got, latest.GetVersion())
	}
	stopReadersAndWait()
	select {
	case failure := <-readerFailures:
		t.Fatal(failure)
	default:
	}
	if stats := running.store.Stats(); stats.Applied < 2 || stats.AppliedReleaseVersion != latest.GetVersion() {
		t.Fatalf("rapid activation stats = %+v", stats)
	}
	if got := capturedInitial.ApiHandler().RuntimeToken(); got.Version() != 1 || got.StringValue() != "adversarial-runtime-token-v1" {
		t.Fatalf("captured initial snapshot mutated: version=%d value=%q", got.Version(), got.StringValue())
	}
}
