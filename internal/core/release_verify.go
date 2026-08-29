package core

import (
	"context"
	"crypto/subtle"
	"errors"
	"strconv"

	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/keyutil"
	"github.com/Suhaibinator/kms/internal/ratelimit"
	"github.com/Suhaibinator/kms/internal/storage"
	"github.com/Suhaibinator/kms/sdk/go/configstore"
)

const (
	// maxVerifyEntries mirrors the release entry ceiling: a request can never
	// usefully mention more aliases than a release can pin.
	maxVerifyEntries      = maxReleaseEntries
	maxVerifyProfileBytes = 64
	sha256HexBytes        = 64
)

// VerifyDefaultsLimits are the per-identity budgets applied to
// VerifyReleaseDefaults. RequestsPerHour/Burst bound how often one identity
// may call the oracle; MismatchBudgetPerHour bounds how many non-matching
// verdicts (anything other than match) it may obtain per hour, which is what
// makes hash-guessing against the endpoint impractical.
type VerifyDefaultsLimits struct {
	RequestsPerHour       int
	Burst                 int
	MismatchBudgetPerHour int
}

// DefaultVerifyDefaultsLimits mirrors config.Default().
func DefaultVerifyDefaultsLimits() VerifyDefaultsLimits {
	return VerifyDefaultsLimits{RequestsPerHour: 60, Burst: 10, MismatchBudgetPerHour: 500}
}

// verifyLimiters holds the two buckets behind VerifyDefaultsLimits.
//
// Scope: both limiters are in-memory and process-local. Every server instance
// enforces the configured budget independently, and a restart resets the
// buckets; there is no shared or persisted counter across instances. That is
// the intended v1 behaviour and is documented in docs/security.md.
type verifyLimiters struct {
	requests   *ratelimit.Limiter
	mismatches *ratelimit.Limiter
}

func newVerifyLimiters(l VerifyDefaultsLimits) *verifyLimiters {
	def := DefaultVerifyDefaultsLimits()
	if l.RequestsPerHour <= 0 {
		l.RequestsPerHour = def.RequestsPerHour
	}
	if l.Burst <= 0 {
		l.Burst = def.Burst
	}
	if l.MismatchBudgetPerHour <= 0 {
		l.MismatchBudgetPerHour = def.MismatchBudgetPerHour
	}
	return &verifyLimiters{
		requests:   ratelimit.New(float64(l.RequestsPerHour)/60.0, float64(l.Burst)),
		mismatches: ratelimit.New(float64(l.MismatchBudgetPerHour)/60.0, float64(l.MismatchBudgetPerHour)),
	}
}

// SetVerifyDefaultsLimits replaces the VerifyReleaseDefaults budgets. It is
// called once at startup from the server configuration; non-positive values
// fall back to the defaults. Replacing the limits discards current bucket
// state.
func (s *Service) SetVerifyDefaultsLimits(l VerifyDefaultsLimits) {
	s.verifyLimits.Store(newVerifyLimiters(l))
}

// VerifyReleaseDefaults compares caller-supplied canonical content hashes of
// an application's source-owned defaults against the parameters pinned by
// the active release and returns one bounded, value-free verdict per alias.
//
// The operation is a deliberate oracle and is hardened accordingly: it never
// echoes a stored value, digest, or hash; secret aliases are answered
// structurally (secret_alias) without touching secret storage; comparisons
// are constant-time; every identity (admins included) is subject to a
// request budget and a mismatch budget; and the audit record carries counts
// only, never aliases, hashes, or the informational profile label.
func (s *Service) VerifyReleaseDefaults(ctx context.Context, pr Principal, in domain.VerifyReleaseDefaultsInput) (domain.VerifyReleaseDefaultsResult, error) {
	if err := validateVerifyDefaultsInput(in); err != nil {
		return domain.VerifyReleaseDefaultsResult{}, err
	}
	ctx, namespace, err := s.authorize(ctx, pr, domain.OpConfigurationReleaseVerifyDefaults, domain.ResourceConfigurationRelease, domain.Ref{NS: in.Namespace, Key: in.ReleaseName})
	if err != nil {
		return domain.VerifyReleaseDefaultsResult{}, err
	}
	limits := s.verifyLimits.Load()
	identity := pr.Identity.Name
	auditRef := domain.Ref{NS: in.Namespace, Key: in.ReleaseName}
	counts := verifyAuditCounts{entryCount: len(in.Entries), schemaMatches: false}
	if !limits.requests.Allow(identity) {
		counts.limited = true
		s.auditVerifyDefaults(ctx, pr, auditRef, namespace.ID, 0, "deny", counts)
		return domain.VerifyReleaseDefaultsResult{}, domain.Errorf(domain.ErrResourceExhausted, "verify-defaults request budget exhausted for identity")
	}

	rs, err := s.releaseStore()
	if err != nil {
		return domain.VerifyReleaseDefaultsResult{}, err
	}
	apps, err := s.applicationStore()
	if err != nil {
		return domain.VerifyReleaseDefaultsResult{}, err
	}
	app, err := apps.GetApplication(ctx, in.Namespace.App)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			s.auditVerifyDefaults(ctx, pr, auditRef, namespace.ID, 0, "error", counts)
		}
		return domain.VerifyReleaseDefaultsResult{}, err
	}
	releaseName := in.ReleaseName
	if releaseName == "" {
		releaseName = app.ReleaseName
	}
	if err := keyutil.ValidateKey(releaseName); err != nil {
		return domain.VerifyReleaseDefaultsResult{}, domain.Errorf(domain.ErrInvalidArgument, "invalid release name: %v", err)
	}
	auditRef.Key = releaseName
	active, err := rs.GetActiveConfigurationRelease(ctx, in.Namespace, releaseName)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			s.auditVerifyDefaults(ctx, pr, auditRef, namespace.ID, 0, "error", counts)
		}
		return domain.VerifyReleaseDefaultsResult{}, err
	}
	release := active.Release

	// Schema check against the application-pinned schema (falling back to the
	// release's own pin when the application has none). The generator's
	// schema_sha256 is sha256(json.Compact(schema)), which is exactly the
	// registry digest.
	schemaMatches := false
	if in.SchemaSHA256 != "" {
		schemaID, schemaVersion := app.SchemaID, app.SchemaVersion
		if schemaID == "" {
			schemaID, schemaVersion = release.SchemaID, release.SchemaVersion
		}
		if schemaID != "" {
			schema, err := rs.GetConfigurationSchema(ctx, schemaID, schemaVersion)
			if err != nil && !errors.Is(err, domain.ErrNotFound) {
				return domain.VerifyReleaseDefaultsResult{}, err
			}
			if err == nil {
				schemaMatches = subtle.ConstantTimeCompare([]byte(schema.Digest), []byte(in.SchemaSHA256)) == 1
			}
		}
	}

	releaseEntries := make(map[string]domain.ConfigurationReleaseEntry, len(release.Entries))
	for _, entry := range release.Entries {
		releaseEntries[entry.Alias] = entry
	}
	contract := make(map[string]struct{}, len(app.Contract))
	for _, field := range app.Contract {
		contract[field.Alias] = struct{}{}
	}

	var summary domain.VerifyDefaultsSummary
	verdicts := make([]domain.VerifyEntryVerdict, 0, len(in.Entries))
	mentioned := make(map[string]struct{}, len(in.Entries))
	for _, req := range in.Entries {
		mentioned[req.Alias] = struct{}{}
		verdict, err := s.verifyDefaultsEntry(ctx, req, releaseEntries, contract)
		if err != nil {
			return domain.VerifyReleaseDefaultsResult{}, err
		}
		verdicts = append(verdicts, domain.VerifyEntryVerdict{Alias: req.Alias, Verdict: verdict})
		switch verdict {
		case domain.VerifyVerdictMatch:
			summary.Match++
		case domain.VerifyVerdictDiffers:
			summary.Differs++
		case domain.VerifyVerdictMissingInRelease:
			summary.MissingInRelease++
		case domain.VerifyVerdictUnknownAlias:
			summary.UnknownAlias++
		case domain.VerifyVerdictSecretAlias:
			summary.SecretAlias++
		case domain.VerifyVerdictUnsupportedContentType:
			summary.UnsupportedContentType++
		}
	}
	for alias, entry := range releaseEntries {
		if entry.Kind != domain.ReleaseEntryParameter {
			continue
		}
		if _, ok := mentioned[alias]; !ok {
			summary.Unverified++
		}
	}
	counts.summary = summary
	counts.schemaMatches = schemaMatches

	// Every verdict other than match leaks one bit about the stored value
	// (or the release shape). Charge them against the identity's mismatch
	// budget atomically: either the whole response is affordable or the caller
	// learns nothing beyond "budget exhausted".
	if nonMatch := len(in.Entries) - summary.Match; nonMatch > 0 && !limits.mismatches.Take(identity, float64(nonMatch)) {
		counts.limited = true
		s.auditVerifyDefaults(ctx, pr, auditRef, namespace.ID, release.Version, "deny", counts)
		return domain.VerifyReleaseDefaultsResult{}, domain.Errorf(domain.ErrResourceExhausted, "verify-defaults mismatch budget exhausted for identity")
	}
	s.auditVerifyDefaults(ctx, pr, auditRef, namespace.ID, release.Version, "allow", counts)
	return domain.VerifyReleaseDefaultsResult{
		ReleaseName:        release.Name,
		ReleaseVersion:     release.Version,
		ActivationRevision: active.ActivationRevision,
		SchemaMatches:      schemaMatches,
		Entries:            verdicts,
		Summary:            summary,
	}, nil
}

// verifyDefaultsEntry produces the verdict for one requested alias. It reads
// parameters only; a secret alias is answered before any storage access.
func (s *Service) verifyDefaultsEntry(ctx context.Context, req domain.VerifyDefaultsEntry, releaseEntries map[string]domain.ConfigurationReleaseEntry, contract map[string]struct{}) (string, error) {
	entry, ok := releaseEntries[req.Alias]
	if !ok {
		if _, inContract := contract[req.Alias]; inContract {
			return domain.VerifyVerdictMissingInRelease, nil
		}
		return domain.VerifyVerdictUnknownAlias, nil
	}
	if entry.Kind != domain.ReleaseEntryParameter {
		return domain.VerifyVerdictSecretAlias, nil
	}
	entryCtx := ctx
	if entry.ResourceNamespaceID > 0 {
		bound, err := storage.BindNamespaceIncarnation(ctx, entry.Ref.NS, entry.ResourceNamespaceID)
		if err != nil {
			// The pinned namespace incarnation is gone: the pin no longer
			// resolves, which from the caller's perspective is a missing entry.
			return domain.VerifyVerdictMissingInRelease, nil
		}
		entryCtx = bound
	}
	p, err := s.store.GetParameter(entryCtx, entry.Ref, entry.Version, "")
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) || errors.Is(err, domain.ErrAborted) {
			return domain.VerifyVerdictMissingInRelease, nil
		}
		return "", err
	}
	// A pin whose stored bytes no longer match its digest, or whose content
	// type moved, cannot be "the same as the defaults" whatever the caller
	// sent; reporting differs keeps the verdict set closed.
	if sha256Hex([]byte(p.Value)) != entry.ParameterDigest {
		return domain.VerifyVerdictDiffers, nil
	}
	if p.ContentType != entry.ContentType || req.ContentType != entry.ContentType {
		return domain.VerifyVerdictDiffers, nil
	}
	stored, err := configstore.ParameterHash(entry.ContentType, []byte(p.Value))
	if err != nil {
		return domain.VerifyVerdictUnsupportedContentType, nil
	}
	if subtle.ConstantTimeCompare([]byte(stored), []byte(req.SHA256)) == 1 {
		return domain.VerifyVerdictMatch, nil
	}
	return domain.VerifyVerdictDiffers, nil
}

func validateVerifyDefaultsInput(in domain.VerifyReleaseDefaultsInput) error {
	if err := keyutil.ValidateNamespace(in.Namespace); err != nil {
		return domain.Errorf(domain.ErrInvalidArgument, "%v", err)
	}
	if in.ReleaseName != "" {
		if err := keyutil.ValidateKey(in.ReleaseName); err != nil {
			return domain.Errorf(domain.ErrInvalidArgument, "invalid release name: %v", err)
		}
	}
	if len(in.Profile) > maxVerifyProfileBytes {
		return domain.Errorf(domain.ErrInvalidArgument, "profile exceeds %d bytes", maxVerifyProfileBytes)
	}
	if in.SchemaSHA256 != "" && !isLowerHexSHA256(in.SchemaSHA256) {
		return domain.Errorf(domain.ErrInvalidArgument, "schema_sha256 must be 64 lowercase hex characters")
	}
	if len(in.Entries) > maxVerifyEntries {
		return domain.Errorf(domain.ErrInvalidArgument, "verify request may contain at most %d entries", maxVerifyEntries)
	}
	seen := make(map[string]struct{}, len(in.Entries))
	for _, entry := range in.Entries {
		if entry.Alias == "" || len(entry.Alias) > maxReleaseAliasBytes {
			return domain.Errorf(domain.ErrInvalidArgument, "verify entry alias must be between 1 and %d bytes", maxReleaseAliasBytes)
		}
		if _, dup := seen[entry.Alias]; dup {
			return domain.Errorf(domain.ErrInvalidArgument, "duplicate verify alias %q", entry.Alias)
		}
		seen[entry.Alias] = struct{}{}
		if !isLowerHexSHA256(entry.SHA256) {
			return domain.Errorf(domain.ErrInvalidArgument, "verify entry %q sha256 must be 64 lowercase hex characters", entry.Alias)
		}
	}
	return nil
}

// isLowerHexSHA256 accepts exactly 64 lowercase hexadecimal characters. Upper
// case is rejected rather than normalized so a single canonical encoding is
// ever compared.
func isLowerHexSHA256(v string) bool {
	if len(v) != sha256HexBytes {
		return false
	}
	for i := 0; i < len(v); i++ {
		c := v[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// verifyAuditCounts is the only material the verification audit record
// carries: counts, the schema outcome, and whether a budget refused the call.
type verifyAuditCounts struct {
	entryCount    int
	summary       domain.VerifyDefaultsSummary
	schemaMatches bool
	limited       bool
}

func (s *Service) auditVerifyDefaults(ctx context.Context, pr Principal, ref domain.Ref, namespaceID int64, version uint64, decision string, c verifyAuditCounts) {
	meta := map[string]string{
		"entry_count":         strconv.Itoa(c.entryCount),
		"match_count":         strconv.Itoa(c.summary.Match),
		"differs_count":       strconv.Itoa(c.summary.Differs),
		"missing_count":       strconv.Itoa(c.summary.MissingInRelease),
		"unknown_alias_count": strconv.Itoa(c.summary.UnknownAlias),
		"secret_alias_count":  strconv.Itoa(c.summary.SecretAlias),
		"unsupported_count":   strconv.Itoa(c.summary.UnsupportedContentType),
		"unverified_count":    strconv.Itoa(c.summary.Unverified),
		"schema_matches":      strconv.FormatBool(c.schemaMatches),
		"limited":             strconv.FormatBool(c.limited),
	}
	s.auditRefWithNamespaceID(ctx, pr, "configuration_release.verify_defaults", domain.ResourceConfigurationRelease, ref, namespaceID, version, decision, meta)
}
