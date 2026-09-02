package cli

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/Suhaibinator/kms/internal/core"
	"github.com/Suhaibinator/kms/internal/domain"
)

// The demo data `dev` writes. It is deliberately small but not uniform: every
// parameter content type appears, one key has history, one secret expires soon
// enough for the posture panel and the expiry metrics to have something to
// show, and the unprivileged identity can read one namespace and not the
// other. A demo where everything is a string in one namespace teaches nothing.

// devSeedResult is what the banner needs from a seed run.
type devSeedResult struct {
	namespaces []string
	appToken   string
}

// devDemoSecretExpiry is how far out the expiring demo secret is dated. A few
// days is close enough to appear in the "expiring soon" posture and far enough
// that a store kept over a weekend still starts.
const devDemoSecretExpiry = 5 * 24 * time.Hour

// devReleaseSchema is the JSON Schema the seeded configuration release
// validates against. Only parameter aliases appear in the validated instance,
// so a secret entry is never required here.
const devReleaseSchema = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "properties": {
    "greeting": {"type": "string"},
    "max_connections": {"type": "integer", "minimum": 1},
    "dark_mode": {"type": "boolean"}
  },
  "required": ["greeting", "max_connections", "dark_mode"]
}`

// devReleaseName must match the application's release stream, which
// EnsureApplication creates as "runtime" when the first namespace is made.
const devReleaseName = "runtime"

// devSchemaID names the schema stream the release pins.
const devSchemaID = "demo-runtime"

// seedDevStore writes the demo content through the same core APIs the admin
// CLI drives over gRPC — no rows are inserted behind the service's back, so
// the seeded store is one a person could have built by hand, audit trail
// included. Every step tolerates "already exists", which is what makes a
// persisted --dir safe to restart.
func (c *CLI) seedDevStore(ctx context.Context, svc *core.Service) (devSeedResult, error) {
	var out devSeedResult
	admin := localAdminPrincipal()
	devNS := domain.NamespaceRef{Env: devDemoEnv, App: devDemoApp}
	prodNS := domain.NamespaceRef{Env: devDemoProdEnv, App: devDemoApp}

	// Both auth methods are allowed explicitly: the default is mTLS only, and
	// the demo identity authenticates with a bearer token.
	for _, ns := range []struct {
		ref         domain.NamespaceRef
		description string
	}{
		{devNS, "Demo application, development environment"},
		{prodNS, "Demo application, production environment"},
	} {
		if _, err := svc.CreateNamespace(ctx, admin, ns.ref, ns.description,
			[]domain.AuthMethod{domain.AuthMethodMTLS, domain.AuthMethodToken}); err != nil && !errors.Is(err, domain.ErrAlreadyExists) {
			return out, fmt.Errorf("creating namespace %s: %w", ns.ref, err)
		}
		out.namespaces = append(out.namespaces, ns.ref.String())
	}

	if err := c.seedDevParameters(ctx, svc, admin, devNS, prodNS); err != nil {
		return out, err
	}
	if err := c.seedDevSecrets(ctx, svc, admin, devNS); err != nil {
		return out, err
	}

	// A client identity homed in dev/demo. The policy below is what actually
	// grants its reads; the home binding only decides which namespace the
	// convenience commands assume.
	home := devNS
	token, err := devMintToken(ctx, svc, core.CreateIdentityInput{
		Name:        devAppName,
		Kind:        domain.IdentityKindClient,
		Namespace:   &home,
		AuthMethods: []domain.AuthMethod{domain.AuthMethodToken},
	})
	if err != nil {
		return out, fmt.Errorf("creating the %s identity: %w", devAppName, err)
	}
	out.appToken = token

	if err := c.seedDevPolicy(ctx, svc, admin, devNS); err != nil {
		return out, err
	}
	if err := c.seedDevRelease(ctx, svc, admin, devNS); err != nil {
		return out, err
	}
	return out, nil
}

// seedDevParameters writes one parameter of every content type, and gives the
// greeting two versions so a history is there to be browsed and a rollback has
// somewhere to go. A key that already exists is left exactly as it is: a
// restart against a persisted --dir must not pile a new version onto every key
// each time.
func (c *CLI) seedDevParameters(ctx context.Context, svc *core.Service, admin core.Principal, devNS, prodNS domain.NamespaceRef) error {
	params := []struct {
		ns          domain.NamespaceRef
		key         string
		contentType string
		// values are written in order, each becoming one version.
		values []string
	}{
		{devNS, "app/greeting", "string", []string{"hello from the dev store", "hello from the dev store (v2)"}},
		{devNS, "app/max-connections", "integer", []string{"25"}},
		{devNS, "app/timeout-seconds", "float", []string{"2.5"}},
		{devNS, "feature/dark-mode", "boolean", []string{"true"}},
		{devNS, "app/limits", "json", []string{`{"requests_per_second":100,"burst":20}`}},
		{devNS, "app/banner.png", "binary", []string{base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\n demo asset"))}},
		{prodNS, "app/greeting", "string", []string{"hello from production"}},
		{prodNS, "app/max-connections", "integer", []string{"200"}},
	}
	for _, p := range params {
		ref := domain.Ref{NS: p.ns, Key: p.key}
		exists, err := devParameterExists(ctx, svc, admin, ref)
		if err != nil {
			return err
		}
		if exists {
			continue
		}
		for _, value := range p.values {
			if _, _, err := svc.PutParameter(ctx, admin, ref, value, p.contentType, ""); err != nil {
				return fmt.Errorf("writing parameter %s: %w", ref, err)
			}
		}
	}
	return nil
}

// seedDevSecrets writes two secrets, one of which expires in a few days so the
// expiry posture and its metric are not permanently zero on a demo.
func (c *CLI) seedDevSecrets(ctx context.Context, svc *core.Service, admin core.Principal, devNS domain.NamespaceRef) error {
	secrets := []core.PutSecretInput{
		{
			Ref:         domain.Ref{NS: devNS, Key: "db/password"},
			Value:       []byte("demo-database-password"),
			ContentType: "text/plain",
		},
		{
			Ref:         domain.Ref{NS: devNS, Key: "api/session-signing-key"},
			Value:       []byte("demo-session-signing-key"),
			ContentType: "text/plain",
			ExpiresAt:   time.Now().Add(devDemoSecretExpiry).UnixMilli(),
		},
	}
	for _, in := range secrets {
		switch _, err := svc.GetSecretInfo(ctx, admin, in.Ref); {
		case err == nil:
			continue
		case !errors.Is(err, domain.ErrNotFound):
			return fmt.Errorf("checking secret %s: %w", in.Ref, err)
		}
		if _, err := svc.PutSecret(ctx, admin, in); err != nil {
			return fmt.Errorf("writing secret %s: %w", in.Ref, err)
		}
	}
	return nil
}

// devParameterExists reports whether a seeded key is already present, so a
// restart adds nothing.
func devParameterExists(ctx context.Context, svc *core.Service, admin core.Principal, ref domain.Ref) (bool, error) {
	switch _, err := svc.GetParameterInfo(ctx, admin, ref); {
	case err == nil:
		return true, nil
	case errors.Is(err, domain.ErrNotFound):
		return false, nil
	default:
		return false, fmt.Errorf("checking parameter %s: %w", ref, err)
	}
}

// seedDevPolicy grants the demo identity reads in dev/demo and nothing else.
// The point of the seed is that the same token is refused in prod/demo, which
// is the first thing anyone evaluating an authorization model wants to try.
func (c *CLI) seedDevPolicy(ctx context.Context, svc *core.Service, admin core.Principal, devNS domain.NamespaceRef) error {
	allow := make([]domain.PolicyRule, 0, 6)
	for _, op := range []string{
		domain.OpParameterRead,
		domain.OpParameterList,
		domain.OpSecretRead,
		domain.OpSecretList,
		domain.OpConfigurationReleaseRead,
		domain.OpConfigurationReleaseWatch,
	} {
		allow = append(allow, domain.PolicyRule{Operation: op, Env: devNS.Env, App: devNS.App})
	}
	policy := domain.Policy{Name: devAppName + "-read", Subject: devAppName, Allow: allow}
	if _, err := svc.CreatePolicy(ctx, admin, policy); err != nil {
		if !errors.Is(err, domain.ErrAlreadyExists) {
			return fmt.Errorf("creating policy %s: %w", policy.Name, err)
		}
		if _, err := svc.UpdatePolicy(ctx, admin, policy); err != nil {
			return fmt.Errorf("updating policy %s: %w", policy.Name, err)
		}
	}
	return nil
}

// seedDevRelease publishes and activates one configuration release in
// dev/demo, so the release, schema, and rollout views have real content the
// moment the console opens.
func (c *CLI) seedDevRelease(ctx context.Context, svc *core.Service, admin core.Principal, devNS domain.NamespaceRef) error {
	// A store that already has an active release keeps it. Publishing a second
	// one would pin a second schema version, which the application contract
	// adopted from the first release then rejects.
	switch _, err := svc.GetActiveConfigurationRelease(ctx, admin, devNS, devReleaseName); {
	case err == nil:
		return nil
	case !errors.Is(err, domain.ErrNotFound):
		return fmt.Errorf("checking the active configuration release: %w", err)
	}

	schema, err := svc.CreateConfigurationSchema(ctx, admin, devSchemaID, devReleaseSchema, "")
	if err != nil {
		return fmt.Errorf("creating configuration schema %s: %w", devSchemaID, err)
	}

	release, err := svc.CreateConfigurationRelease(ctx, admin, domain.CreateConfigurationReleaseInput{
		Namespace:     devNS,
		Name:          devReleaseName,
		SchemaID:      schema.ID,
		SchemaVersion: schema.Version,
		Entries: []domain.ReleaseEntrySelector{
			{Alias: "greeting", Kind: domain.ReleaseEntryParameter, Ref: domain.Ref{NS: devNS, Key: "app/greeting"}, Label: "current"},
			{Alias: "max_connections", Kind: domain.ReleaseEntryParameter, Ref: domain.Ref{NS: devNS, Key: "app/max-connections"}, Label: "current"},
			{Alias: "dark_mode", Kind: domain.ReleaseEntryParameter, Ref: domain.Ref{NS: devNS, Key: "feature/dark-mode"}, Label: "current"},
			{Alias: "db_password", Kind: domain.ReleaseEntrySecret, Ref: domain.Ref{NS: devNS, Key: "db/password"}, Label: "current"},
		},
	})
	if err != nil {
		return fmt.Errorf("creating configuration release %s: %w", devReleaseName, err)
	}
	if _, _, err := svc.ActivateConfigurationRelease(ctx, admin, devNS, devReleaseName, release.Version, nil); err != nil {
		return fmt.Errorf("activating configuration release %s: %w", devReleaseName, err)
	}
	return nil
}
