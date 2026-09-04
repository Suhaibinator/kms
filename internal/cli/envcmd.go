package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
	"github.com/Suhaibinator/kms/internal/envinject"
	"github.com/Suhaibinator/kms/internal/fileutil"
	"github.com/Suhaibinator/kms/internal/keyutil"
)

// secretTokenEnvPrefix names the environment variables exec and env consult
// for per-secret tokens: KMS_SECRET_TOKEN_<NAME>, where NAME is the variable
// the secret maps to (without --env-prefix or the _B64 suffix). These are
// inputs to the CLI and never reach a child process.
const secretTokenEnvPrefix = "KMS_SECRET_TOKEN_"

// maxEnvTotalBytes caps the injected environment as a whole. Individual
// entries are capped per platform (maxEnvEntryBytes) so a value that would
// make the kernel refuse the exec is reported by name instead of as E2BIG.
const maxEnvTotalBytes = 2 << 20

// listPageSize is the page size env and exec request when draining a
// namespace; it is the server's maximum.
const listPageSize = 1000

// envSelection is what exec and env share: which entries to inject, how to
// name them, and where per-secret tokens come from.
type envSelection struct {
	prefix                 string
	release                string
	noSecrets              bool
	envPrefix              string
	allowIncompleteSecrets bool
	tokens                 secretTokenList
	tokenFiles             secretTokenList
}

// addEnvSelectionFlags registers the selection flags on fs.
func addEnvSelectionFlags(fs *flag.FlagSet, sel *envSelection) {
	fs.StringVar(&sel.prefix, "prefix", "", "inject only keys under this relative `prefix` (namespace mode)")
	fs.StringVar(&sel.release, "release", "", "inject the entries of the active release `NAME` (exact versions, verified digests) instead of the namespace's current values")
	fs.BoolVar(&sel.noSecrets, "no-secrets", false, "inject parameters only")
	fs.StringVar(&sel.envPrefix, "env-prefix", "", "prepend this `prefix` to every variable name")
	fs.BoolVar(&sel.allowIncompleteSecrets, "allow-incomplete-secrets", false, "namespace mode only: omit secrets that are bound or lack a required token, with a warning")
	fs.Var(&sel.tokens, "secret-token", "per-secret access token as `KEY=TOKEN` (repeatable; visible in the process list, prefer --secret-token-file)")
	fs.Var(&sel.tokenFiles, "secret-token-file", "read a per-secret access token from a private file, as `KEY=PATH` (repeatable)")
}

// secretTokenList is a repeatable KEY=VALUE flag. KEY names the secret: its
// relative key, its /env/app/key path, or (in release mode) its alias. String
// prints the keys only, so a token can never surface through flag help.
type secretTokenList struct {
	keys    []string
	values  map[string]string
	invalid string // fixed diagnostic only; never derived from raw flag input
}

func (l *secretTokenList) String() string { return strings.Join(l.keys, ",") }

func (l *secretTokenList) Set(raw string) error {
	key, value, ok := strings.Cut(raw, "=")
	if !ok || key == "" || value == "" {
		if l.invalid == "" {
			l.invalid = "must use KEY=VALUE with non-empty KEY and VALUE"
		}
		return nil
	}
	if l.values == nil {
		l.values = map[string]string{}
	}
	if _, dup := l.values[key]; dup {
		if l.invalid == "" {
			l.invalid = "names the same key more than once"
		}
		return nil
	}
	l.keys = append(l.keys, key)
	l.values[key] = value
	return nil
}

var _ flag.Value = (*secretTokenList)(nil)

// validate checks the selection flags that do not need the server.
func (sel *envSelection) validate() error {
	if sel.tokens.invalid != "" {
		return usageError("--secret-token " + sel.tokens.invalid)
	}
	if sel.tokenFiles.invalid != "" {
		return usageError("--secret-token-file " + sel.tokenFiles.invalid)
	}
	if sel.prefix != "" && sel.release != "" {
		return usageError("--prefix and --release are mutually exclusive: a release fixes its own entries")
	}
	if sel.prefix != "" {
		if err := keyutil.ValidateKey(strings.TrimSuffix(sel.prefix, "/")); err != nil {
			return usageError(fmt.Sprintf("invalid --prefix: %v", err))
		}
	}
	if sel.release != "" {
		if err := keyutil.ValidateKey(sel.release); err != nil {
			return usageError(fmt.Sprintf("invalid --release: %v", err))
		}
	}
	if sel.allowIncompleteSecrets && sel.release != "" {
		return usageError("--allow-incomplete-secrets cannot be used with --release: release resolution is atomic")
	}
	if sel.allowIncompleteSecrets && sel.noSecrets {
		return usageError("--allow-incomplete-secrets and --no-secrets are mutually exclusive")
	}
	if !envinject.ValidPrefix(sel.envPrefix) {
		return usageError(fmt.Sprintf("invalid --env-prefix %q: must match [A-Za-z_][A-Za-z0-9_]*", sel.envPrefix))
	}
	for _, key := range sel.tokens.keys {
		if _, dup := sel.tokenFiles.values[key]; dup {
			return usageError(fmt.Sprintf("--secret-token and --secret-token-file both name %s", key))
		}
	}
	return nil
}

// secretItem is a secret selected for injection, before its value is fetched.
type secretItem struct {
	ref             *kmsv1.ResourceRef
	alias           string // release mode only
	version         uint64 // release mode only; 0 = label current
	contentType     string // release mode only; "" = not recorded
	bound           bool
	needsToken      bool
	protectionKnown bool // namespace listings include exact version metadata; release entries do not
}

// names lists every spelling a token flag may use for this secret. Release
// pins are namespace-local, but keep the namespace check here so malformed
// inputs never gain a surprising relative-key spelling.
func (s secretItem) names(ns *kmsv1.NamespaceRef) []string {
	out := []string{displayPath(s.ref)}
	if s.alias != "" {
		out = append(out, s.alias)
	}
	if r := s.ref.GetNamespace(); r.GetEnv() == ns.GetEnv() && r.GetApp() == ns.GetApp() {
		out = append(out, s.ref.GetKey())
	}
	return out
}

// tokenEnvName is the KMS_SECRET_TOKEN_* variable that may carry this
// secret's token: the mapped variable name, before any --env-prefix.
func (s secretItem) tokenEnvName() (string, bool) {
	var name string
	var err error
	if s.alias != "" {
		name, err = envinject.MapAlias(s.alias)
	} else {
		name, err = envinject.MapKey(s.ref.GetKey())
	}
	if err != nil {
		return "", false
	}
	return secretTokenEnvPrefix + name, true
}

// resolvedEnvironment is what resolveEnvironment produces for both commands.
type resolvedEnvironment struct {
	vars             []envinject.Var
	notes            []envinject.Note
	unavailableNames []string // plain and _B64 names exec must remove from its inherited environment
	omitted          []omittedSecret
}

type omittedSecret struct {
	path   string
	reason string
	secret secretItem
}

type secretResolution struct {
	secret secretItem
	token  string
}

// resolveEnvironment fetches the selected entries and maps them to variables.
// Secret-inclusive resolution is atomic by default: an unavailable selected
// secret aborts before any output is printed or child is launched. Namespace
// mode may explicitly opt into omission with --allow-incomplete-secrets.
func (c *CLI) resolveEnvironment(ctx context.Context, conn *grpc.ClientConn, cf *connFlags, ns *kmsv1.NamespaceRef, sel *envSelection) (resolvedEnvironment, error) {
	var (
		items   []envinject.Item
		secrets []secretItem
		err     error
	)
	if sel.release != "" {
		items, secrets, err = c.resolveReleaseValues(ctx, conn, cf, ns, sel.release)
	} else {
		items, secrets, err = c.resolveNamespaceValues(ctx, conn, cf, ns, sel.prefix, !sel.noSecrets)
	}
	if err != nil {
		return resolvedEnvironment{}, err
	}

	tokens, err := c.secretTokens(sel)
	if err != nil {
		return resolvedEnvironment{}, err
	}
	var out resolvedEnvironment
	if !sel.noSecrets {
		client := kmsv1.NewSecretServiceClient(conn)
		plans := make([]secretResolution, 0, len(secrets))
		for _, selected := range secrets {
			s := selected
			path := displayPath(s.ref)
			if !s.protectionKnown {
				metadataResp, err := client.GetSecretMetadata(cf.authCtx(ctx), &kmsv1.GetSecretMetadataRequest{Ref: s.ref})
				if err != nil {
					return resolvedEnvironment{}, fmt.Errorf("secret %s metadata: %w", path, err)
				}
				metadata := metadataResp.GetSecret()
				if metadata == nil || !sameRef(metadata.GetRef(), s.ref) {
					return resolvedEnvironment{}, fmt.Errorf("secret %s metadata: server returned a different resource", path)
				}
				versionInfo, err := selectSecretVersion(metadata, s.version, "")
				if err != nil {
					return resolvedEnvironment{}, fmt.Errorf("secret %s metadata: %w", path, err)
				}
				s.bound = versionInfo.GetBound()
				s.needsToken = !s.bound && versionInfo.GetHasAccessToken()
				s.protectionKnown = true
			}
			token, err := tokens.lookup(s, ns, c)
			if err != nil {
				return resolvedEnvironment{}, err
			}
			if s.bound {
				out.omitted = append(out.omitted, omittedSecret{
					path: path, reason: "it is bound and bulk commands do not accept binding keys", secret: s,
				})
				continue
			}
			if s.needsToken && token == "" {
				out.omitted = append(out.omitted, omittedSecret{
					path: path, reason: "it requires a per-secret token and none was supplied", secret: s,
				})
				continue
			}
			plans = append(plans, secretResolution{secret: s, token: token})
		}
		if err := tokens.unused(); err != nil {
			return resolvedEnvironment{}, err
		}
		if len(out.omitted) != 0 && !sel.allowIncompleteSecrets {
			missing := out.omitted[0]
			return resolvedEnvironment{}, fmt.Errorf("secret %s cannot be materialized: %s", missing.path, missing.reason)
		}
		for _, plan := range plans {
			s := plan.secret
			path := displayPath(s.ref)
			req := &kmsv1.GetSecretRequest{Ref: s.ref, Version: s.version, SecretToken: plan.token}
			resp, err := client.GetSecret(cf.authCtx(ctx), req)
			if err != nil {
				return resolvedEnvironment{}, fmt.Errorf("secret %s: %w", path, redactSecretRPCError(err))
			}
			if s.version != 0 {
				if resp.GetVersion() != s.version {
					return resolvedEnvironment{}, fmt.Errorf("secret %s: server returned version %d, release pins %d", path, resp.GetVersion(), s.version)
				}
				if !sameRef(resp.GetRef(), s.ref) {
					return resolvedEnvironment{}, fmt.Errorf("secret %s: server returned a different resource", path)
				}
				if resp.GetContentType() != s.contentType {
					return resolvedEnvironment{}, fmt.Errorf("secret %s: content type %q does not match the release's %q", path, resp.GetContentType(), s.contentType)
				}
			}
			items = append(items, envinject.Item{
				Key: s.ref.GetKey(), Alias: s.alias, Value: resp.GetValue(), ContentType: resp.GetContentType(), Secret: true,
			})
		}
	} else if err := tokens.unused(); err != nil {
		return resolvedEnvironment{}, err
	}

	rules := envinject.Rules{
		Prefix: sel.envPrefix, MaxEntryBytes: maxEnvEntryBytes, MaxTotalBytes: maxEnvTotalBytes,
	}
	out.vars, out.notes, err = envinject.Resolve(items, rules)
	if err != nil {
		return resolvedEnvironment{}, err
	}
	seenUnavailable := make(map[string]string, 2*len(out.omitted))
	for _, missing := range out.omitted {
		item := envinject.Item{Key: missing.secret.ref.GetKey(), Alias: missing.secret.alias, Secret: true}
		vars, _, err := envinject.Resolve([]envinject.Item{item}, rules)
		if err != nil {
			return resolvedEnvironment{}, fmt.Errorf("mapping unavailable secret output: %w", err)
		}
		if len(vars) != 1 {
			return resolvedEnvironment{}, fmt.Errorf("mapping unavailable secret output: expected one variable, got %d", len(vars))
		}
		for _, name := range []string{vars[0].Name, vars[0].Name + "_B64"} {
			if previous, exists := seenUnavailable[name]; exists {
				return resolvedEnvironment{}, fmt.Errorf("unavailable secrets %s and %s may both map to environment variable %s", previous, missing.path, name)
			}
			for _, resolved := range out.vars {
				if resolved.Name == name {
					return resolvedEnvironment{}, fmt.Errorf("unavailable secret %s and another selected value both map to environment variable %s", missing.path, name)
				}
			}
			seenUnavailable[name] = missing.path
			out.unavailableNames = append(out.unavailableNames, name)
		}
	}
	return out, nil
}

// resolveNamespaceValues lists the namespace's current parameters (values
// travel inline) and, when requested, the secrets that will need a GetSecret
// call. Parameter-only mode never calls the secret service.
func (c *CLI) resolveNamespaceValues(ctx context.Context, conn *grpc.ClientConn, cf *connFlags, ns *kmsv1.NamespaceRef, prefix string, includeSecrets bool) ([]envinject.Item, []secretItem, error) {
	actx := cf.authCtx(ctx)
	var items []envinject.Item
	params := kmsv1.NewParameterServiceClient(conn)
	for token := ""; ; {
		resp, err := params.ListParameters(actx, &kmsv1.ListParametersRequest{Namespace: ns, KeyPrefix: prefix, PageSize: listPageSize, PageToken: token})
		if err != nil {
			return nil, nil, fmt.Errorf("list parameters: %w", err)
		}
		for _, p := range resp.GetParameters() {
			items = append(items, envinject.Item{Key: p.GetRef().GetKey(), Value: []byte(p.GetValue()), ContentType: p.GetContentType()})
		}
		if token = resp.GetNextPageToken(); token == "" {
			break
		}
	}
	if !includeSecrets {
		return items, nil, nil
	}
	var secrets []secretItem
	sc := kmsv1.NewSecretServiceClient(conn)
	for token := ""; ; {
		resp, err := sc.ListSecrets(actx, &kmsv1.ListSecretsRequest{Namespace: ns, KeyPrefix: prefix, PageSize: listPageSize, PageToken: token})
		if err != nil {
			return nil, nil, fmt.Errorf("list secrets: %w", err)
		}
		for _, s := range resp.GetSecrets() {
			versionInfo, err := selectSecretVersion(s, 0, "")
			if err != nil {
				return nil, nil, fmt.Errorf("secret %s metadata: %w", displayPath(s.GetRef()), err)
			}
			secrets = append(secrets, secretItem{
				ref:             s.GetRef(),
				version:         versionInfo.GetVersion(),
				contentType:     s.GetContentType(),
				bound:           versionInfo.GetBound(),
				needsToken:      !versionInfo.GetBound() && versionInfo.GetHasAccessToken(),
				protectionKnown: true,
			})
		}
		if token = resp.GetNextPageToken(); token == "" {
			break
		}
	}
	return items, secrets, nil
}

// resolveReleaseValues fetches the active release and every parameter it
// pins, verifying version, resource, content type and digest exactly as the
// SDK's release loader does, so that what the process sees is what the
// release recorded.
func (c *CLI) resolveReleaseValues(ctx context.Context, conn *grpc.ClientConn, cf *connFlags, ns *kmsv1.NamespaceRef, name string) ([]envinject.Item, []secretItem, error) {
	actx := cf.authCtx(ctx)
	active, err := kmsv1.NewConfigurationReleaseServiceClient(conn).GetActiveRelease(actx, &kmsv1.GetActiveReleaseRequest{Namespace: ns, Name: name})
	if err != nil {
		return nil, nil, fmt.Errorf("get active release %s: %w", name, err)
	}
	rel := active.GetRelease()
	if rel == nil {
		return nil, nil, fmt.Errorf("release %s has no active version", name)
	}
	if !sameNamespace(rel.GetNamespace(), ns) {
		return nil, nil, fmt.Errorf("release %s: server returned a different namespace", name)
	}
	if rel.GetName() != name {
		return nil, nil, fmt.Errorf("release %s: server returned a different release", name)
	}
	// Validate the whole manifest's namespace boundary before fetching any
	// resource. A malformed foreign pin must not become a resource-existence
	// oracle or leave the caller with a partially verified candidate.
	for _, e := range rel.GetEntries() {
		if e.GetRef() == nil || !sameNamespace(e.GetRef().GetNamespace(), rel.GetNamespace()) {
			return nil, nil, fmt.Errorf("release entry %s must reference its home namespace", e.GetAlias())
		}
		if e.GetKind() != "parameter" && e.GetKind() != "secret" {
			return nil, nil, fmt.Errorf("release entry %s has unknown kind %q", e.GetAlias(), e.GetKind())
		}
	}
	digest, err := configurationReleaseDigest(rel)
	if err != nil || rel.GetDigest() == "" || !strings.EqualFold(digest, rel.GetDigest()) {
		return nil, nil, fmt.Errorf("release %s: manifest digest mismatch", name)
	}
	params := kmsv1.NewParameterServiceClient(conn)
	var items []envinject.Item
	var secrets []secretItem
	for _, e := range rel.GetEntries() {
		path := displayPath(e.GetRef())
		switch e.GetKind() {
		case "parameter":
			resp, err := params.GetParameter(actx, &kmsv1.GetParameterRequest{Ref: e.GetRef(), Version: e.GetVersion()})
			if err != nil {
				return nil, nil, fmt.Errorf("parameter %s (alias %s): %w", path, e.GetAlias(), err)
			}
			p := resp.GetParameter()
			if p == nil || !sameRef(p.GetRef(), e.GetRef()) {
				return nil, nil, fmt.Errorf("parameter %s (alias %s): server returned a different resource", path, e.GetAlias())
			}
			if p.GetVersion() != e.GetVersion() {
				return nil, nil, fmt.Errorf("parameter %s (alias %s): server returned version %d, release pins %d", path, e.GetAlias(), p.GetVersion(), e.GetVersion())
			}
			sum := sha256.Sum256([]byte(p.GetValue()))
			if e.GetParameterDigest() == "" || !strings.EqualFold(hex.EncodeToString(sum[:]), e.GetParameterDigest()) {
				return nil, nil, fmt.Errorf("parameter %s (alias %s): value does not match the release digest", path, e.GetAlias())
			}
			if p.GetContentType() != e.GetContentType() {
				return nil, nil, fmt.Errorf("parameter %s (alias %s): content type %q does not match the release's %q", path, e.GetAlias(), p.GetContentType(), e.GetContentType())
			}
			items = append(items, envinject.Item{Alias: e.GetAlias(), Value: []byte(p.GetValue()), ContentType: p.GetContentType()})
		case "secret":
			secrets = append(secrets, secretItem{
				ref: e.GetRef(), alias: e.GetAlias(), version: e.GetVersion(), contentType: e.GetContentType(),
			})
		default:
			return nil, nil, fmt.Errorf("release entry %s has unknown kind %q", e.GetAlias(), e.GetKind())
		}
	}
	return items, secrets, nil
}

// configurationReleaseDigest mirrors the server and SDK immutable protobuf
// projection. Allocated release versions, timestamps, creator, and the digest
// field itself are intentionally excluded.
func configurationReleaseDigest(release *kmsv1.ConfigurationRelease) (string, error) {
	if release == nil || release.GetNamespace() == nil {
		return "", errors.New("release namespace is missing")
	}
	projection := &kmsv1.ConfigurationRelease{
		Namespace: &kmsv1.NamespaceRef{
			Env: release.GetNamespace().GetEnv(),
			App: release.GetNamespace().GetApp(),
		},
		Name:          release.GetName(),
		SchemaVersion: release.GetSchemaVersion(),
		MetadataJson:  release.GetMetadataJson(),
	}
	entries := append([]*kmsv1.ConfigurationReleaseEntry(nil), release.GetEntries()...)
	sort.Slice(entries, func(i, j int) bool { return entries[i].GetAlias() < entries[j].GetAlias() })
	for _, entry := range entries {
		if entry == nil || entry.GetRef() == nil || entry.GetRef().GetNamespace() == nil {
			return "", errors.New("release entry resource is missing")
		}
		projection.Entries = append(projection.Entries, &kmsv1.ConfigurationReleaseEntry{
			Alias: entry.GetAlias(),
			Kind:  entry.GetKind(),
			Ref: &kmsv1.ResourceRef{
				Namespace: &kmsv1.NamespaceRef{
					Env: entry.GetRef().GetNamespace().GetEnv(),
					App: entry.GetRef().GetNamespace().GetApp(),
				},
				Key: entry.GetRef().GetKey(),
			},
			Version:         entry.GetVersion(),
			ContentType:     entry.GetContentType(),
			MetadataJson:    entry.GetMetadataJson(),
			ParameterDigest: entry.GetParameterDigest(),
		})
	}
	encoded, err := (proto.MarshalOptions{Deterministic: true}).Marshal(projection)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

func sameRef(a, b *kmsv1.ResourceRef) bool {
	return a != nil && b != nil && a.GetKey() == b.GetKey() &&
		sameNamespace(a.GetNamespace(), b.GetNamespace())
}

func sameNamespace(a, b *kmsv1.NamespaceRef) bool {
	return a != nil && b != nil && a.GetEnv() == b.GetEnv() && a.GetApp() == b.GetApp()
}

// secretTokenSource holds the per-secret tokens the flags supplied, keyed by
// the spelling the operator used, and records which were consumed so a token
// for a secret that does not exist (or needs none) is reported as the typo
// it almost certainly is.
type secretTokenSource struct {
	byKey map[string]string
	used  map[string]bool
}

// secretTokens loads the flag-supplied tokens; files are read now so a
// missing or world-readable file fails before any RPC.
func (c *CLI) secretTokens(sel *envSelection) (*secretTokenSource, error) {
	src := &secretTokenSource{byKey: map[string]string{}, used: map[string]bool{}}
	for key, tok := range sel.tokens.values {
		src.byKey[key] = tok
	}
	for key, path := range sel.tokenFiles.values {
		tok, err := readTokenFile(path)
		if err != nil {
			return nil, fmt.Errorf("--secret-token-file %s: %w", key, err)
		}
		src.byKey[key] = tok
	}
	return src, nil
}

// lookup returns the token for s: a --secret-token or --secret-token-file
// under any accepted spelling of the secret, then the KMS_SECRET_TOKEN_<NAME>
// environment variable. Empty means none.
//
// The flags are checked before needsToken is consulted: a flag token that
// names a secret needing none is a typo (or a stale script) landing on the
// wrong secret. Two spellings of one secret are ambiguous even when they
// agree, so they are refused rather than resolved by spelling order.
// Environment tokens are ambient and may be leftovers, so they are only read
// when needed.
func (t *secretTokenSource) lookup(s secretItem, ns *kmsv1.NamespaceRef, c *CLI) (string, error) {
	var matched []string
	for _, name := range s.names(ns) {
		if _, ok := t.byKey[name]; ok {
			matched = append(matched, name)
			t.used[name] = true
		}
	}
	path := displayPath(s.ref)
	switch {
	case len(matched) > 1:
		return "", fmt.Errorf("secret %s is named by more than one token flag (%s); supply it once", path, strings.Join(matched, ", "))
	case len(matched) == 1 && !s.needsToken:
		return "", fmt.Errorf("secret %s does not require a per-secret token; remove --secret-token/--secret-token-file %s", path, matched[0])
	case len(matched) == 1:
		return t.byKey[matched[0]], nil
	case !s.needsToken:
		return "", nil
	}
	if envName, ok := s.tokenEnvName(); ok {
		if tok, ok := c.env(envName); ok && tok != "" {
			return tok, nil
		}
	}
	return "", nil
}

// unused reports flag-supplied tokens that matched no secret needing one.
func (t *secretTokenSource) unused() error {
	var stray []string
	for key := range t.byKey {
		if !t.used[key] {
			stray = append(stray, key)
		}
	}
	if len(stray) == 0 {
		return nil
	}
	sort.Strings(stray)
	return fmt.Errorf("--secret-token/--secret-token-file names %s, which is not a secret in the selection that requires a token", strings.Join(stray, ", "))
}

// printResolutionNotes reports non-fatal outcomes on stderr: secrets omitted
// by an explicit incomplete-mode request and values that were base64-encoded.
// Names only, never values. An omitted secret means the environment is
// incomplete, so its warning is not subject to --quiet.
func (c *CLI) printResolutionNotes(res resolvedEnvironment) {
	for _, missing := range res.omitted {
		_, _ = fmt.Fprintf(c.Stderr, "warning: omitted unavailable secret %s: %s (--allow-incomplete-secrets)\n", missing.path, missing.reason)
	}
	for _, n := range res.notes {
		if n.Kind == envinject.NoteBinaryEncoded {
			c.info("note: %s is not text; injected base64-encoded as %s", n.Key, n.Name)
		}
	}
}

// --- env -------------------------------------------------------------------

// envFormats lists the --format values in help order.
var envFormats = []string{"dotenv", "export", "json", "yaml"}

// envOutputAllowed applies get-secret's terminal rule to the whole
// environment: secret values may be written to a terminal only with --show,
// while --out and a pipe are always fine. Parameters alone (--no-secrets)
// never need it. The decision is taken before anything is fetched, so a
// refused invocation reads no secret and leaves no audit rows behind.
func envOutputAllowed(isTTY, mayHaveSecrets, show bool, out string) bool {
	return out != "" || show || !isTTY || !mayHaveSecrets
}

func (c *CLI) cmdEnv(args []string) int {
	fs := c.newFlags("env")
	cf := addConnFlags(c, fs)
	var sel envSelection
	addEnvSelectionFlags(fs, &sel)
	format := fs.String("format", "", "output `format`: "+strings.Join(envFormats, ", ")+" (default dotenv; -o json selects json)")
	show := fs.Bool("show", false, "allow printing secret values to a terminal")
	out := fs.String("out", "", "write to this private `file` (0600) instead of stdout; refuses to replace an existing file")
	force := fs.Bool("force", false, "replace an existing --out file")
	c.setUsage(fs, "env ENV/APP [flags]",
		"Print the namespace's parameters and secrets as environment variable assignments, for `source <(parameter-store env ENV/APP --format export)` or an EnvironmentFile=. Secret-inclusive resolution fails before output if any selected secret is unavailable; --no-secrets intentionally selects parameters only, while namespace mode may opt into warned omission with --allow-incomplete-secrets. Source incomplete output only into a clean environment because omitted assignments cannot unset inherited values. Same selection, naming and token flags as exec; only the injected variables are printed.", false)
	if !c.parseFlags(fs, args) {
		return 2
	}
	pos := c.args()
	if len(pos) < 1 || pos[0] == "" {
		return c.failUsage("env requires an env/app namespace argument")
	}
	if !c.rejectExtraPositionals(1) {
		return 2
	}
	ns, err := parseNamespaceProto(pos[0])
	if err != nil {
		return c.failUsage("invalid namespace: %v", err)
	}
	if err := sel.validate(); err != nil {
		return c.failUsage("%v", err)
	}
	switch {
	case *format == "" && c.jsonOutput():
		*format = "json"
	case *format == "":
		*format = "dotenv"
	case c.jsonOutput() && *format != "json":
		return c.failUsage("--output json and --format %s conflict", *format)
	}
	write, ok := envWriter(*format)
	if !ok {
		return c.failUsage("unknown --format %q (expected %s)", *format, strings.Join(envFormats, ", "))
	}
	if *force && *out == "" {
		return c.failUsage("--force requires --out")
	}
	if !envOutputAllowed(c.stdoutIsTTY(), !sel.noSecrets, *show, *out) {
		return c.fail("refusing to print secrets to a terminal; pass --show to print, --out FILE to save, or --no-secrets")
	}

	conn, err := c.dialConn(cf)
	if err != nil {
		return c.failErr("", err)
	}
	defer func() { _ = conn.Close() }()
	ctx, cancel := callContext()
	defer cancel()

	res, err := c.resolveEnvironment(ctx, conn, cf, ns, &sel)
	if err != nil {
		return c.failErr("env", err)
	}
	c.printResolutionNotes(res)

	if *out == "" {
		if err := write(c.Stdout, res.vars); err != nil {
			return c.fail("writing output: %v", err)
		}
		return 0
	}
	if err := writePrivateFile(*out, *force, func(w io.Writer) error { return write(w, res.vars) }); err != nil {
		return c.failErr("writing --out", err)
	}
	c.info("Wrote %d variables to %s", len(res.vars), *out)
	if c.jsonOutput() {
		// The variables went to the file, so stdout still carries exactly one
		// document: where they are and how many, never the values.
		return c.printJSON(envOutJSON{OutFile: *out, Variables: len(res.vars)})
	}
	return 0
}

// envOutJSON is the JSON form of `env --out FILE`: the file now holding the
// assignments and the number written, mirroring get-secret's out_file.
type envOutJSON struct {
	OutFile   string `json:"out_file"`
	Variables int    `json:"variables"`
}

// envWriter selects the formatter for --format.
func envWriter(format string) (func(io.Writer, []envinject.Var) error, bool) {
	switch format {
	case "dotenv":
		return envinject.WriteDotenv, true
	case "export":
		return envinject.WriteExport, true
	case "json":
		return envinject.WriteJSON, true
	case "yaml":
		return envinject.WriteYAML, true
	}
	return nil, false
}

// writePrivateFile writes the output to path through a 0600 staging file in
// the same directory, so the content is never observable at a wider mode and
// a failed write leaves nothing behind. Without replace an existing path is
// refused (exit 6, like backup --out); with it the rename swaps the file
// atomically and the old inode's permissions are irrelevant.
func writePrivateFile(path string, replace bool, write func(io.Writer) error) error {
	stable, err := fileutil.ResolveStablePath(path)
	if err != nil {
		return err
	}
	path = stable
	tmp, err := fileutil.CreatePrivateTemp(filepath.Dir(path), ".kms-env-")
	if err != nil {
		return err
	}
	staging := tmp.Name()
	// Harmless after a rename; required after the hard-link fallback.
	defer func() { _ = os.Remove(staging) }()
	if err := write(tmp); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if replace {
		return os.Rename(staging, path)
	}
	if err := fileutil.PublishNoReplace(staging, path); err != nil {
		if errors.Is(err, os.ErrExist) {
			// Wrapping keeps os.ErrExist reachable, so this exits 6 like
			// backup --out onto an existing file.
			return fmt.Errorf("%s already exists (pass --force to replace it): %w", path, os.ErrExist)
		}
		return err
	}
	return nil
}

// caseInsensitiveEnv reports whether the host treats variable names without
// regard to case.
func caseInsensitiveEnv() bool { return runtime.GOOS == "windows" }
