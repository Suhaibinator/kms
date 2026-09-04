package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
	"google.golang.org/protobuf/proto"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/keyutil"
	"github.com/Suhaibinator/kms/internal/storage"
)

const (
	maxReleaseEntries       = 256
	maxReleaseAliasBytes    = 64
	maxReleaseMetadataBytes = 64 << 10
	maxSchemaBytes          = 1 << 20
	maxAckDiagnosticBytes   = 1024
	maxReleaseClientIDBytes = 128
	maxDivergentFieldCount  = 65535
)

var releaseAliasRE = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]*$`)

func (s *Service) releaseStore() (storage.ReleaseStore, error) {
	rs, ok := s.store.(storage.ReleaseStore)
	if !ok {
		return nil, domain.Errorf(domain.ErrNotReady, "configuration release storage is unavailable")
	}
	return rs, nil
}

func (s *Service) auditProtectedReleaseReference(ctx context.Context, pr Principal, ref domain.Ref, namespaceID int64, kind string, version uint64, operation string) {
	rs, ok := s.store.(storage.ReleaseStore)
	if !ok {
		return
	}
	if _, err := rs.FindProtectedReleaseReference(ctx, ref, kind, version); err != nil {
		return
	}
	s.auditRefWithNamespaceID(ctx, pr, "configuration_release.reference_blocked", kind, ref, namespaceID, version, "deny", map[string]string{"operation": operation})
}

func validateReleaseAddress(ns domain.NamespaceRef, name string) error {
	if err := keyutil.ValidateNamespace(ns); err != nil {
		return domain.Errorf(domain.ErrInvalidArgument, "%v", err)
	}
	if err := keyutil.ValidateKey(name); err != nil {
		return domain.Errorf(domain.ErrInvalidArgument, "invalid release name: %v", err)
	}
	return nil
}

// releaseCandidateValue substitutes an unsaved parameter value for one alias
// during in-memory validation so a dry-run can check what a caller intends to
// write without persisting it.
type releaseCandidateValue struct {
	value       []byte
	contentType string
}

func (s *Service) CreateConfigurationRelease(ctx context.Context, pr Principal, in domain.CreateConfigurationReleaseInput) (domain.ConfigurationRelease, error) {
	if err := validateReleaseAddress(in.Namespace, in.Name); err != nil {
		return domain.ConfigurationRelease{}, err
	}
	if len(in.Entries) == 0 || len(in.Entries) > maxReleaseEntries {
		return domain.ConfigurationRelease{}, domain.Errorf(domain.ErrInvalidArgument, "release must contain between 1 and %d entries", maxReleaseEntries)
	}
	metadata, err := validateReleaseMetadata(in.Metadata)
	if err != nil {
		return domain.ConfigurationRelease{}, err
	}
	ctx, namespace, err := s.authorize(ctx, pr, domain.OpConfigurationReleaseCreate, domain.ResourceConfigurationRelease, domain.Ref{NS: in.Namespace, Key: in.Name})
	if err != nil {
		return domain.ConfigurationRelease{}, err
	}
	rs, err := s.releaseStore()
	if err != nil {
		return domain.ConfigurationRelease{}, err
	}
	in.Metadata = metadata
	ctx, release, _, err := s.resolveReleaseCandidate(ctx, pr, rs, in, false)
	if err != nil {
		return domain.ConfigurationRelease{}, err
	}
	var out domain.ConfigurationRelease
	if in.RequireFirst {
		firstStore, ok := rs.(storage.FirstReleaseStore)
		if !ok {
			return domain.ConfigurationRelease{}, domain.Errorf(domain.ErrFailedPrecondition, "atomic first-release creation is unavailable")
		}
		out, err = firstStore.CreateFirstConfigurationRelease(ctx, release)
	} else {
		out, err = rs.CreateConfigurationRelease(ctx, release)
	}
	if err != nil {
		return domain.ConfigurationRelease{}, err
	}
	s.auditRefWithNamespaceID(ctx, pr, "configuration_release.create", domain.ResourceConfigurationRelease, domain.Ref{NS: in.Namespace, Key: in.Name}, namespace.ID, out.Version, "allow", nil)
	return out, nil
}

// resolveReleaseCandidate turns entry selectors into an exactly pinned,
// digested, contract-checked release WITHOUT persisting it. The caller must
// already hold an authorized context for the release itself; per-entry read
// authorization happens here and the returned context carries every namespace
// incarnation binding the persist step relies on.
//
// With collectErrors=false the first per-alias resolution failure aborts (the
// historical CreateConfigurationRelease behaviour). With collectErrors=true
// per-alias failures (missing, unreadable, denied, destroyed, oversized) are
// reported as sanitized ReleaseValidationErrors instead; when any were
// collected the contract check is skipped and the partially resolved candidate
// is returned alongside them. collectErrors=true is the dry-run mode, so the
// contract check also runs with adopt=false and nothing is ever written.
// Structural input errors (bad alias, duplicate, unknown kind, invalid ref)
// always abort.
func (s *Service) resolveReleaseCandidate(ctx context.Context, pr Principal, rs storage.ReleaseStore, in domain.CreateConfigurationReleaseInput, collectErrors bool) (context.Context, domain.ConfigurationRelease, []domain.ReleaseValidationError, error) {
	ctx, entries, validation, err := s.resolveReleaseEntries(ctx, pr, in, collectErrors)
	if err != nil {
		return ctx, domain.ConfigurationRelease{}, nil, err
	}
	release := domain.ConfigurationRelease{Namespace: in.Namespace, Name: in.Name, SchemaVersion: in.SchemaVersion, Entries: entries, Metadata: in.Metadata, CreatedBy: pr.Identity.Name}
	if len(validation) > 0 {
		return ctx, release, validation, nil
	}
	if err := s.validateApplicationReleaseContract(ctx, in.Namespace.App, in.Name, in.SchemaVersion, entries, !collectErrors); err != nil {
		return ctx, domain.ConfigurationRelease{}, nil, err
	}
	if in.SchemaVersion != 0 {
		if _, err := rs.GetConfigurationSchema(ctx, in.Namespace.App, in.Name, in.SchemaVersion); err != nil {
			return ctx, domain.ConfigurationRelease{}, nil, err
		}
	}
	release.Digest, err = releaseDigest(release)
	if err != nil {
		return ctx, domain.ConfigurationRelease{}, nil, fmt.Errorf("calculate configuration release digest: %w", err)
	}
	return ctx, release, nil, nil
}

// resolveReleaseEntries is the selector → exact pin step of
// resolveReleaseCandidate without the contract check or digest, so callers
// assembling a candidate from mixed sources (ship: stored pins plus unsaved
// values) can reuse it. Entries come back sorted by alias.
func (s *Service) resolveReleaseEntries(ctx context.Context, pr Principal, in domain.CreateConfigurationReleaseInput, collectErrors bool) (context.Context, []domain.ConfigurationReleaseEntry, []domain.ReleaseValidationError, error) {
	var validation []domain.ReleaseValidationError
	collect := func(alias string, verr domain.ReleaseValidationError, err error) error {
		if !collectErrors {
			return err
		}
		verr.Alias = alias
		validation = append(validation, verr)
		return nil
	}
	seen := make(map[string]struct{}, len(in.Entries))
	entries := make([]domain.ConfigurationReleaseEntry, 0, len(in.Entries))
	for _, sel := range in.Entries {
		if len(sel.Alias) == 0 || len(sel.Alias) > maxReleaseAliasBytes || !releaseAliasRE.MatchString(sel.Alias) {
			return ctx, nil, nil, domain.Errorf(domain.ErrInvalidArgument, "invalid release alias %q", sel.Alias)
		}
		if _, ok := seen[sel.Alias]; ok {
			return ctx, nil, nil, domain.Errorf(domain.ErrInvalidArgument, "duplicate release alias %q", sel.Alias)
		}
		seen[sel.Alias] = struct{}{}
		if sel.Version > 0 && sel.Label != "" {
			return ctx, nil, nil, domain.Errorf(domain.ErrInvalidArgument, "release alias %q specifies both version and label", sel.Alias)
		}
		if sel.Ref.NS == (domain.NamespaceRef{}) {
			sel.Ref.NS = in.Namespace
		}
		if err := validateRef(sel.Ref); err != nil {
			return ctx, nil, nil, err
		}
		// A configuration release is a complete, namespace-owned unit. Cross-
		// namespace pins would let an otherwise immutable manifest depend on a
		// separately administered namespace and are forbidden for both parameters
		// and secrets, including for administrators.
		if sel.Ref.NS != in.Namespace {
			return ctx, nil, nil, domain.Errorf(domain.ErrInvalidArgument,
				"release alias %q must reference its release namespace %s", sel.Alias, in.Namespace)
		}
		label := sel.Label
		if sel.Version == 0 && label == "" {
			label = domain.LabelCurrent
		}
		switch sel.Kind {
		case domain.ReleaseEntryParameter:
			authorizedCtx, _, err := s.authorize(ctx, pr, domain.OpParameterRead, domain.ResourceParameter, sel.Ref)
			if err != nil {
				if err := collect(sel.Alias, validationAuthError(sel.Alias, err), err); err != nil {
					return ctx, nil, nil, err
				}
				continue
			}
			ctx = authorizedCtx
			p, err := s.store.GetParameter(ctx, sel.Ref, sel.Version, label)
			if err != nil {
				if err := collect(sel.Alias, validationReadError(sel.Alias, err), err); err != nil {
					return ctx, nil, nil, err
				}
				continue
			}
			if len(p.Metadata) > maxReleaseMetadataBytes {
				err := domain.Errorf(domain.ErrFailedPrecondition, "release alias %q metadata exceeds release limit", sel.Alias)
				if err := collect(sel.Alias, domain.ReleaseValidationError{Code: domain.ReleaseValidationUnreadable, Message: "resource metadata exceeds release limit"}, err); err != nil {
					return ctx, nil, nil, err
				}
				continue
			}
			entries = append(entries, domain.ConfigurationReleaseEntry{Alias: sel.Alias, Kind: sel.Kind, Ref: sel.Ref, Version: p.Version, ContentType: p.ContentType, Metadata: p.Metadata, ParameterDigest: sha256Hex([]byte(p.Value))})
		case domain.ReleaseEntrySecret:
			authorizedCtx, _, err := s.authorize(ctx, pr, domain.OpSecretRead, domain.ResourceSecret, sel.Ref)
			if err != nil {
				if err := collect(sel.Alias, validationAuthError(sel.Alias, err), err); err != nil {
					return ctx, nil, nil, err
				}
				continue
			}
			ctx = authorizedCtx
			_, ver, err := s.store.GetSecretVersion(ctx, sel.Ref, sel.Version, label)
			if err != nil {
				if err := collect(sel.Alias, validationReadError(sel.Alias, err), err); err != nil {
					return ctx, nil, nil, err
				}
				continue
			}
			if ver.State == domain.StateDestroyed {
				err := domain.Errorf(domain.ErrFailedPrecondition, "release alias %q references a destroyed secret version", sel.Alias)
				if err := collect(sel.Alias, domain.ReleaseValidationError{Code: domain.ReleaseValidationUnreadable, Message: "secret version is not readable"}, err); err != nil {
					return ctx, nil, nil, err
				}
				continue
			}
			if len(ver.Metadata) > maxReleaseMetadataBytes {
				err := domain.Errorf(domain.ErrFailedPrecondition, "release alias %q metadata exceeds release limit", sel.Alias)
				if err := collect(sel.Alias, domain.ReleaseValidationError{Code: domain.ReleaseValidationUnreadable, Message: "resource metadata exceeds release limit"}, err); err != nil {
					return ctx, nil, nil, err
				}
				continue
			}
			entries = append(entries, domain.ConfigurationReleaseEntry{Alias: sel.Alias, Kind: sel.Kind, Ref: sel.Ref, Version: ver.Version, ContentType: ver.ContentType, Metadata: ver.Metadata})
		default:
			return ctx, nil, nil, domain.Errorf(domain.ErrInvalidArgument, "release alias %q has unknown kind %q", sel.Alias, sel.Kind)
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Alias < entries[j].Alias })
	return ctx, entries, validation, nil
}

// validateApplicationReleaseContract checks a release against its
// application's contract. When the application has no contract yet, adopt=true
// derives one from the entries (the first-release adoption rule); adopt=false
// treats the entries as matching without writing anything so dry-run paths
// never mutate the application.
func (s *Service) validateApplicationReleaseContract(ctx context.Context, appName, releaseName string, schemaVersion uint64, entries []domain.ConfigurationReleaseEntry, adopt bool) error {
	store, ok := s.store.(storage.ApplicationStore)
	if !ok {
		return nil
	}
	app, err := store.GetApplication(ctx, appName)
	if err != nil {
		return err
	}
	if app.ReleaseName != "" && releaseName != app.ReleaseName {
		return domain.Errorf(domain.ErrFailedPrecondition, "application %s requires release name %q", appName, app.ReleaseName)
	}
	if !app.ArchivedAt.IsZero() {
		return domain.Errorf(domain.ErrFailedPrecondition, "application %s is archived", appName)
	}
	if schemaVersion != app.SchemaVersion {
		return domain.Errorf(domain.ErrFailedPrecondition, "application %s requires schema %s/%s@%d", appName, appName, app.ReleaseName, app.SchemaVersion)
	}
	if len(app.Contract) == 0 {
		if !adopt {
			return nil
		}
		fields := make([]domain.ApplicationContractField, 0, len(entries))
		for _, entry := range entries {
			field := domain.ApplicationContractField{Alias: entry.Alias, Kind: entry.Kind}
			if entry.Kind == domain.ReleaseEntryParameter {
				field.ContentType = entry.ContentType
			}
			fields = append(fields, field)
		}
		app, err = store.AdoptApplicationContract(ctx, appName, fields)
		if err != nil {
			return err
		}
	}
	if len(entries) != len(app.Contract) {
		return domain.Errorf(domain.ErrFailedPrecondition, "release does not match application %s contract", appName)
	}
	for i, field := range app.Contract {
		entry := entries[i]
		if field.Alias != entry.Alias || field.Kind != entry.Kind ||
			(field.Kind == domain.ReleaseEntryParameter && field.ContentType != entry.ContentType) {
			return domain.Errorf(domain.ErrFailedPrecondition, "release alias %q does not match application %s contract", entry.Alias, appName)
		}
	}
	return nil
}

func (s *Service) GetConfigurationRelease(ctx context.Context, pr Principal, ns domain.NamespaceRef, name string, version uint64) (domain.ConfigurationRelease, error) {
	if err := validateReleaseAddress(ns, name); err != nil {
		return domain.ConfigurationRelease{}, err
	}
	if version == 0 {
		return domain.ConfigurationRelease{}, domain.Errorf(domain.ErrInvalidArgument, "version is required")
	}
	ctx, _, err := s.authorize(ctx, pr, domain.OpConfigurationReleaseRead, domain.ResourceConfigurationRelease, domain.Ref{NS: ns, Key: name})
	if err != nil {
		return domain.ConfigurationRelease{}, err
	}
	rs, err := s.releaseStore()
	if err != nil {
		return domain.ConfigurationRelease{}, err
	}
	return rs.GetConfigurationRelease(ctx, ns, name, version)
}

func (s *Service) GetActiveConfigurationRelease(ctx context.Context, pr Principal, ns domain.NamespaceRef, name string) (domain.ActiveConfigurationRelease, error) {
	if err := validateReleaseAddress(ns, name); err != nil {
		return domain.ActiveConfigurationRelease{}, err
	}
	ctx, _, err := s.authorize(ctx, pr, domain.OpConfigurationReleaseRead, domain.ResourceConfigurationRelease, domain.Ref{NS: ns, Key: name})
	if err != nil {
		return domain.ActiveConfigurationRelease{}, err
	}
	rs, err := s.releaseStore()
	if err != nil {
		return domain.ActiveConfigurationRelease{}, err
	}
	return rs.GetActiveConfigurationRelease(ctx, ns, name)
}

func (s *Service) ListConfigurationReleases(ctx context.Context, pr Principal, ns domain.NamespaceRef, name string, page storage.ListPage) ([]domain.ConfigurationReleaseSummary, string, error) {
	if err := keyutil.ValidateNamespace(ns); err != nil {
		return nil, "", domain.Errorf(domain.ErrInvalidArgument, "%v", err)
	}
	if name != "" {
		if err := keyutil.ValidateKey(name); err != nil {
			return nil, "", domain.Errorf(domain.ErrInvalidArgument, "invalid release name: %v", err)
		}
	}
	key := name
	if key == "" {
		key = "releases"
	}
	ctx, _, err := s.authorize(ctx, pr, domain.OpConfigurationReleaseList, domain.ResourceConfigurationRelease, domain.Ref{NS: ns, Key: key})
	if err != nil {
		return nil, "", err
	}
	rs, err := s.releaseStore()
	if err != nil {
		return nil, "", err
	}
	return rs.ListConfigurationReleases(ctx, ns, name, page)
}

func (s *Service) ValidateConfigurationRelease(ctx context.Context, pr Principal, ns domain.NamespaceRef, name string, version uint64) ([]domain.ReleaseValidationError, error) {
	if err := validateReleaseAddress(ns, name); err != nil {
		return nil, err
	}
	if version == 0 {
		return nil, domain.Errorf(domain.ErrInvalidArgument, "version is required")
	}
	ctx, namespace, err := s.authorize(ctx, pr, domain.OpConfigurationReleaseValidate, domain.ResourceConfigurationRelease, domain.Ref{NS: ns, Key: name})
	if err != nil {
		return nil, err
	}
	rs, err := s.releaseStore()
	if err != nil {
		return nil, err
	}
	validation, err := s.validateConfigurationRelease(ctx, pr, rs, ns, name, version, true)
	if err != nil {
		return nil, err
	}
	decision := "allow"
	if len(validation) > 0 {
		decision = "error"
	}
	s.auditRefWithNamespaceID(ctx, pr, "configuration_release.validate", domain.ResourceConfigurationRelease, domain.Ref{NS: ns, Key: name}, namespace.ID, version, decision, map[string]string{"error_count": strconv.Itoa(len(validation))})
	return validation, nil
}

// validateConfigurationRelease validates an already-authorized immutable
// release. Keeping this separate from the public validation operation lets
// activation enforce the same checks without requiring a second, unrelated
// configuration_release.validate permission.
func (s *Service) validateConfigurationRelease(ctx context.Context, pr Principal, rs storage.ReleaseStore, ns domain.NamespaceRef, name string, version uint64, authorizeEntries bool) ([]domain.ReleaseValidationError, error) {
	rel, err := rs.GetConfigurationRelease(ctx, ns, name, version)
	if err != nil {
		return nil, err
	}
	return s.validateReleaseEntries(ctx, pr, rs, rel, nil, authorizeEntries, true)
}

// validateReleaseEntries validates an in-memory release: every pinned entry
// must still resolve to the same content, and the assembled parameter object
// must satisfy the release schema. overrides substitutes unsaved values for
// parameter aliases (dry-run of an edit) — those aliases skip the stored
// lookup and digest check and the override is what the schema sees. adopt is
// forwarded to the contract check; dry-run callers pass false so validation
// never writes. Entries with a zero ResourceNamespaceID (never persisted) are
// read through the caller's already-bound context instead of re-binding.
func (s *Service) validateReleaseEntries(ctx context.Context, pr Principal, rs storage.ReleaseStore, rel domain.ConfigurationRelease, overrides map[string]releaseCandidateValue, authorizeEntries, adopt bool) ([]domain.ReleaseValidationError, error) {
	if err := s.validateApplicationReleaseContract(ctx, rel.Namespace.App, rel.Name, rel.SchemaVersion, rel.Entries, adopt); err != nil {
		return nil, err
	}
	validation := make([]domain.ReleaseValidationError, 0)
	obj := map[string]any{}
	for _, entry := range rel.Entries {
		if entry.Ref.NS != rel.Namespace {
			validation = append(validation, domain.ReleaseValidationError{
				Alias: entry.Alias, Code: domain.ReleaseValidationUnreadable,
				Message: "release entry is outside the release namespace",
			})
			continue
		}
		entryCtx := ctx
		if entry.ResourceNamespaceID > 0 {
			bound, bindErr := storage.BindNamespaceIncarnation(ctx, entry.Ref.NS, entry.ResourceNamespaceID)
			if bindErr != nil {
				validation = append(validation, domain.ReleaseValidationError{
					Alias: entry.Alias, Code: domain.ReleaseValidationNotFound,
					Message: "release entry references a missing or replaced namespace",
				})
				continue
			}
			entryCtx = bound
		}
		switch entry.Kind {
		case domain.ReleaseEntryParameter:
			if authorizeEntries {
				authorizedCtx, _, authErr := s.authorize(entryCtx, pr, domain.OpParameterRead, domain.ResourceParameter, entry.Ref)
				if authErr != nil {
					validation = append(validation, validationAuthError(entry.Alias, authErr))
					continue
				}
				entryCtx = authorizedCtx
			}
			var rawValue, contentType string
			if override, ok := overrides[entry.Alias]; ok {
				rawValue, contentType = string(override.value), override.contentType
			} else {
				p, err := s.store.GetParameter(entryCtx, entry.Ref, entry.Version, "")
				if err != nil {
					validation = append(validation, validationReadError(entry.Alias, err))
					continue
				}
				if got := sha256Hex([]byte(p.Value)); got != entry.ParameterDigest {
					validation = append(validation, domain.ReleaseValidationError{Alias: entry.Alias, Code: domain.ReleaseValidationDigest, Message: "parameter digest does not match release pin"})
					continue
				}
				rawValue, contentType = p.Value, p.ContentType
			}
			if contentType != entry.ContentType {
				validation = append(validation, domain.ReleaseValidationError{Alias: entry.Alias, Code: domain.ReleaseValidationContentType, Message: "parameter content type does not match release pin"})
				continue
			}
			value, err := parameterSchemaValue(rawValue, entry.ContentType)
			if err != nil {
				code := domain.ReleaseValidationContentType
				if contentType == "json" {
					code = domain.ReleaseValidationMalformedJSON
				}
				validation = append(validation, domain.ReleaseValidationError{Alias: entry.Alias, Code: code, Message: "parameter does not match its declared content type"})
				continue
			}
			obj[entry.Alias] = value
		case domain.ReleaseEntrySecret:
			if authorizeEntries {
				authorizedCtx, _, authErr := s.authorize(entryCtx, pr, domain.OpSecretRead, domain.ResourceSecret, entry.Ref)
				if authErr != nil {
					validation = append(validation, validationAuthError(entry.Alias, authErr))
					continue
				}
				entryCtx = authorizedCtx
			}
			_, ver, err := s.store.GetSecretVersion(entryCtx, entry.Ref, entry.Version, "")
			if err != nil {
				validation = append(validation, validationReadError(entry.Alias, err))
				continue
			}
			if err := s.checkVersionReadable(ver); err != nil {
				validation = append(validation, domain.ReleaseValidationError{Alias: entry.Alias, Code: domain.ReleaseValidationUnreadable, Message: "secret version is not readable"})
				continue
			}
			if ver.ContentType != entry.ContentType {
				validation = append(validation, domain.ReleaseValidationError{Alias: entry.Alias, Code: domain.ReleaseValidationContentType, Message: "secret content type does not match release pin"})
				continue
			}
		default:
			validation = append(validation, domain.ReleaseValidationError{Alias: entry.Alias, Code: domain.ReleaseValidationUnreadable, Message: "release entry kind is invalid"})
		}
	}
	if len(validation) == 0 && rel.SchemaVersion != 0 {
		schrec, err := rs.GetConfigurationSchema(ctx, rel.Namespace.App, rel.Name, rel.SchemaVersion)
		if err != nil {
			validation = append(validation, domain.ReleaseValidationError{Code: domain.ReleaseValidationNotFound, Message: "configuration schema is unavailable"})
		} else if sch, err := compileSchema(schrec.Schema); err != nil {
			validation = append(validation, domain.ReleaseValidationError{Code: domain.ReleaseValidationSchema, Message: "configuration schema is invalid"})
		} else if err := sch.Validate(obj); err != nil {
			validation = append(validation, sanitizeSchemaErrors(err)...)
		}
	}
	return validation, nil
}

func (s *Service) ActivateConfigurationRelease(ctx context.Context, pr Principal, ns domain.NamespaceRef, name string, version uint64, expectedCurrent *uint64) (domain.ActiveConfigurationRelease, bool, error) {
	if err := validateReleaseAddress(ns, name); err != nil {
		return domain.ActiveConfigurationRelease{}, false, err
	}
	if version == 0 {
		return domain.ActiveConfigurationRelease{}, false, domain.Errorf(domain.ErrInvalidArgument, "version is required")
	}
	ctx, namespace, err := s.authorize(ctx, pr, domain.OpConfigurationReleaseActivate, domain.ResourceConfigurationRelease, domain.Ref{NS: ns, Key: name})
	if err != nil {
		return domain.ActiveConfigurationRelease{}, false, err
	}
	rs, err := s.releaseStore()
	if err != nil {
		return domain.ActiveConfigurationRelease{}, false, err
	}
	validation, err := s.validateConfigurationRelease(ctx, pr, rs, ns, name, version, false)
	if err != nil {
		s.m().ReleaseOutcome(ReleaseOutcomeError)
		s.auditRefWithNamespaceID(ctx, pr, "configuration_release.activate", domain.ResourceConfigurationRelease, domain.Ref{NS: ns, Key: name}, namespace.ID, version, "error", nil)
		return domain.ActiveConfigurationRelease{}, false, err
	}
	if len(validation) > 0 {
		s.m().ReleaseOutcome(ReleaseOutcomeValidationFailed)
		s.auditRefWithNamespaceID(ctx, pr, "configuration_release.activate", domain.ResourceConfigurationRelease, domain.Ref{NS: ns, Key: name}, namespace.ID, version, "deny", map[string]string{"error_count": strconv.Itoa(len(validation)), "reason": "validation_failed"})
		return domain.ActiveConfigurationRelease{}, false, domain.NewReleaseValidationFailedError(validation)
	}
	active, changed, err := rs.ActivateConfigurationRelease(ctx, ns, name, version, expectedCurrent)
	if err != nil {
		decision := "error"
		event := "configuration_release.activate"
		outcome := ReleaseOutcomeError
		metadata := map[string]string(nil)
		if validationFailed, ok := errors.AsType[*domain.ReleaseValidationFailedError](err); ok {
			decision = "deny"
			outcome = ReleaseOutcomeValidationFailed
			metadata = map[string]string{
				"error_count": strconv.Itoa(len(validationFailed.Violations())),
				"reason":      "validation_failed",
			}
		}
		if errors.Is(err, domain.ErrAborted) {
			event = "configuration_release.cas_conflict"
			decision = "deny"
			outcome = ReleaseOutcomeCASConflict
		}
		s.m().ReleaseOutcome(outcome)
		s.auditRefWithNamespaceID(ctx, pr, event, domain.ResourceConfigurationRelease, domain.Ref{NS: ns, Key: name}, namespace.ID, version, decision, metadata)
		return domain.ActiveConfigurationRelease{}, false, err
	}
	if changed {
		event := "configuration_release.activate"
		outcome := ReleaseOutcomeActivated
		if active.PreviousVersion > 0 && version < active.PreviousVersion {
			event = "configuration_release.rollback"
			outcome = ReleaseOutcomeRolledBack
		}
		s.m().ReleaseOutcome(outcome)
		s.auditRefWithNamespaceID(ctx, pr, event, domain.ResourceConfigurationRelease, domain.Ref{NS: ns, Key: name}, namespace.ID, version, "allow", map[string]string{"previous_version": strconv.FormatUint(active.PreviousVersion, 10)})
		s.getHub().Wake()
		s.notifyReleaseSubscribers(ns, name)
	}
	return active, changed, nil
}

// RollbackConfigurationRelease re-activates the previous release of a name,
// guarded by an optional expectation on the currently active version. It is
// authorized like activation and audited by activation's event classification
// (configuration_release.rollback).
func (s *Service) RollbackConfigurationRelease(ctx context.Context, pr Principal, ns domain.NamespaceRef, name string, expectedCurrent *uint64) (domain.RollbackResult, error) {
	if err := validateReleaseAddress(ns, name); err != nil {
		return domain.RollbackResult{}, err
	}
	ctx, namespace, err := s.authorize(ctx, pr, domain.OpConfigurationReleaseActivate, domain.ResourceConfigurationRelease, domain.Ref{NS: ns, Key: name})
	if err != nil {
		return domain.RollbackResult{}, err
	}
	rs, err := s.releaseStore()
	if err != nil {
		return domain.RollbackResult{}, err
	}
	active, err := rs.GetActiveConfigurationRelease(ctx, ns, name)
	if errors.Is(err, domain.ErrNotFound) {
		return domain.RollbackResult{}, domain.Errorf(domain.ErrFailedPrecondition, "release %s has no active version to roll back", name)
	}
	if err != nil {
		return domain.RollbackResult{}, err
	}
	if active.PreviousVersion == 0 {
		return domain.RollbackResult{}, domain.Errorf(domain.ErrFailedPrecondition, "release %s has no previous version to roll back to", name)
	}
	current := active.Release.Version
	if expectedCurrent != nil && *expectedCurrent != current {
		s.m().ReleaseOutcome(ReleaseOutcomeCASConflict)
		s.auditRefWithNamespaceID(ctx, pr, "configuration_release.cas_conflict", domain.ResourceConfigurationRelease, domain.Ref{NS: ns, Key: name}, namespace.ID, active.PreviousVersion, "deny", map[string]string{"reason": "rollback"})
		return domain.RollbackResult{}, domain.Errorf(domain.ErrAborted, "release %s is at version %d, expected %d", name, current, *expectedCurrent)
	}
	next, changed, err := s.ActivateConfigurationRelease(ctx, pr, ns, name, active.PreviousVersion, &current)
	if err != nil {
		return domain.RollbackResult{}, err
	}
	return domain.RollbackResult{Active: next, RolledBackFrom: current, Changed: changed}, nil
}

func (s *Service) AuthorizeReleaseWatch(ctx context.Context, pr Principal, ns domain.NamespaceRef, name string) error {
	_, err := s.AuthorizeReleaseWatchContext(ctx, pr, ns, name)
	return err
}

// AuthorizeReleaseWatchContext returns the namespace-incarnation-bound context
// that must be used for the initial release snapshot and connection lifecycle.
func (s *Service) AuthorizeReleaseWatchContext(ctx context.Context, pr Principal, ns domain.NamespaceRef, name string) (context.Context, error) {
	if err := validateReleaseAddress(ns, name); err != nil {
		return ctx, err
	}
	bound, namespace, err := s.authorize(ctx, pr, domain.OpConfigurationReleaseWatch, domain.ResourceConfigurationRelease, domain.Ref{NS: ns, Key: name})
	if err != nil {
		return ctx, err
	}
	if namespace.ID == 0 {
		return ctx, domain.Errorf(domain.ErrNotFound, "namespace %s", ns)
	}
	return bound, nil
}

func (s *Service) ReauthorizeReleaseWatch(ctx context.Context, pr Principal, ns domain.NamespaceRef, name string) error {
	if err := s.ReauthorizeWatch(ctx, pr); err != nil {
		return err
	}
	return s.AuthorizeReleaseWatch(ctx, pr, ns, name)
}

func (s *Service) AcknowledgeConfigurationRelease(ctx context.Context, pr Principal, ack domain.ReleaseAcknowledgement) error {
	if err := validateReleaseAddress(ack.Namespace, ack.ReleaseName); err != nil {
		return err
	}
	ctx, namespace, err := s.authorize(ctx, pr, domain.OpConfigurationReleaseWatch, domain.ResourceConfigurationRelease, domain.Ref{NS: ack.Namespace, Key: ack.ReleaseName})
	if err != nil {
		return err
	}
	if ack.ReleaseVersion == 0 || ack.ActivationRevision == 0 || ack.ClientName == "" || ack.InstanceID == "" || ack.ConnectionID == "" {
		return domain.Errorf(domain.ErrInvalidArgument, "release acknowledgement is incomplete")
	}
	if len(ack.ClientName) > maxReleaseClientIDBytes || len(ack.InstanceID) > maxReleaseClientIDBytes {
		return domain.Errorf(domain.ErrInvalidArgument, "release client_name or instance_id is too long")
	}
	if len(ack.Diagnostic) > maxAckDiagnosticBytes {
		return domain.Errorf(domain.ErrInvalidArgument, "release diagnostic exceeds %d bytes", maxAckDiagnosticBytes)
	}
	if !validReleaseState(ack.State) {
		return domain.Errorf(domain.ErrInvalidArgument, "invalid release acknowledgement state")
	}
	if ack.State == domain.ReleaseStateRejected {
		if !validRejectCategory(ack.RejectionCategory) {
			return domain.Errorf(domain.ErrInvalidArgument, "invalid rejection category")
		}
	} else if ack.RejectionCategory != "" {
		return domain.Errorf(domain.ErrInvalidArgument, "rejection category is only valid for rejected state")
	}
	// Divergence is a property of an applied generation only: it reports that
	// the running configuration differs from source-owned defaults, never that
	// a candidate was refused. The count is bounded so the column stays small.
	if (ack.AppliedDivergent || ack.DivergentFieldCount > 0) && ack.State != domain.ReleaseStateApplied {
		return domain.Errorf(domain.ErrInvalidArgument, "applied_divergent is only valid for applied state")
	}
	if ack.DivergentFieldCount > 0 && !ack.AppliedDivergent {
		return domain.Errorf(domain.ErrInvalidArgument, "divergent_field_count requires applied_divergent")
	}
	if ack.DivergentFieldCount > maxDivergentFieldCount {
		return domain.Errorf(domain.ErrInvalidArgument, "divergent_field_count exceeds %d", maxDivergentFieldCount)
	}
	ack.Diagnostic = sanitizeDiagnostic(ack.Diagnostic)
	ack.Identity = pr.Identity.Name
	ack.ServerTimestamp = s.now()
	if ack.ClientTimestamp.IsZero() {
		ack.ClientTimestamp = ack.ServerTimestamp
	}
	rs, err := s.releaseStore()
	if err != nil {
		return err
	}
	if _, err := rs.GetConfigurationRelease(ctx, ack.Namespace, ack.ReleaseName, ack.ReleaseVersion); err != nil {
		return err
	}
	exists, err := rs.ConfigurationReleaseActivationExists(ctx, ack.Namespace, ack.ReleaseName, ack.ReleaseVersion, ack.ActivationRevision)
	if err != nil {
		return err
	}
	if !exists {
		return domain.Errorf(domain.ErrFailedPrecondition, "acknowledgement does not match an authoritative release activation")
	}
	if err := rs.UpsertReleaseAcknowledgement(ctx, ack); err != nil {
		return err
	}
	s.notifyReleaseSubscribers(ack.Namespace, ack.ReleaseName)
	s.auditRefWithNamespaceID(ctx, pr, "configuration_release.acknowledge", domain.ResourceConfigurationRelease, domain.Ref{NS: ack.Namespace, Key: ack.ReleaseName}, namespace.ID, ack.ReleaseVersion, "allow", map[string]string{"state": ack.State, "category": ack.RejectionCategory, "client_name": ack.ClientName, "instance_id": ack.InstanceID, "divergent": strconv.FormatBool(ack.AppliedDivergent)})
	return nil
}

func (s *Service) SetReleaseSubscriberConnected(ctx context.Context, ns domain.NamespaceRef, name, clientName, instanceID, identity, connectionID string, connected bool) error {
	rs, err := s.releaseStore()
	if err != nil {
		return err
	}
	if err := rs.SetReleaseInstanceConnected(ctx, domain.ReleaseSubscriberConnection{
		Namespace: ns, ReleaseName: name, ClientName: clientName, InstanceID: instanceID,
		Identity: identity, ConnectionID: connectionID, Connected: connected, ServerTimestamp: s.now(),
	}); err != nil {
		return err
	}
	s.notifyReleaseSubscribers(ns, name)
	return nil
}

// ResetReleaseSubscriberConnections clears transport liveness left by an
// unclean prior server process. Lifecycle rows remain intact.
func (s *Service) ResetReleaseSubscriberConnections(ctx context.Context) error {
	rs, err := s.releaseStore()
	if err != nil {
		return nil // release storage is additive for legacy Store implementations
	}
	return rs.ResetReleaseInstanceConnections(ctx, s.now())
}

func (s *Service) ListReleaseSubscribers(ctx context.Context, pr Principal, ns domain.NamespaceRef, name string, page storage.ListPage) ([]domain.ReleaseAcknowledgement, string, uint64, error) {
	if err := s.requireAdmin(ctx, pr, "configuration_release.subscribers", domain.ResourceConfigurationRelease, name); err != nil {
		return nil, "", 0, err
	}
	if err := keyutil.ValidateNamespace(ns); err != nil {
		return nil, "", 0, domain.Errorf(domain.ErrInvalidArgument, "%v", err)
	}
	rs, err := s.releaseStore()
	if err != nil {
		return nil, "", 0, err
	}
	rows, next, err := rs.ListReleaseAcknowledgements(ctx, ns, name, page)
	if err != nil {
		return nil, "", 0, err
	}
	if name == "" {
		// A cross-release listing has no single meaningful active revision.
		return rows, next, 0, nil
	}
	active, err := rs.GetActiveConfigurationRelease(ctx, ns, name)
	if errors.Is(err, domain.ErrNotFound) {
		return rows, next, 0, nil
	}
	if err != nil {
		return nil, "", 0, err
	}
	return rows, next, active.ActivationRevision, nil
}

func (s *Service) CreateConfigurationSchema(ctx context.Context, pr Principal, application, schemaJSON, metadata string) (domain.ConfigurationSchema, error) {
	if err := keyutil.ValidateApp(application); err != nil {
		return domain.ConfigurationSchema{}, domain.Errorf(domain.ErrInvalidArgument, "%v", err)
	}
	if err := s.requireAdmin(ctx, pr, "configuration_schema.create", domain.ResourceConfigurationSchema, application); err != nil {
		return domain.ConfigurationSchema{}, err
	}
	apps, err := s.applicationStore()
	if err != nil {
		return domain.ConfigurationSchema{}, err
	}
	app, err := apps.GetApplication(ctx, application)
	if err != nil {
		return domain.ConfigurationSchema{}, err
	}
	if !app.ArchivedAt.IsZero() {
		return domain.ConfigurationSchema{}, domain.Errorf(domain.ErrFailedPrecondition, "application %s is archived", application)
	}
	schema, err := normalizeConfigurationSchema(application, app.ReleaseName, schemaJSON, metadata)
	if err != nil {
		return domain.ConfigurationSchema{}, err
	}
	schema.CreatedBy = pr.Identity.Name
	rs, err := s.releaseStore()
	if err != nil {
		return domain.ConfigurationSchema{}, err
	}
	out, err := rs.CreateConfigurationSchema(ctx, schema)
	if err == nil {
		resource := application + "/" + app.ReleaseName
		s.auditName(ctx, pr, "configuration_schema.create", domain.ResourceConfigurationSchema, resource, "allow", map[string]string{"version": strconv.FormatUint(out.Version, 10)})
	}
	return out, err
}

func normalizeConfigurationSchema(application, releaseName, schemaJSON, metadata string) (domain.ConfigurationSchema, error) {
	if err := keyutil.ValidateApp(application); err != nil {
		return domain.ConfigurationSchema{}, domain.Errorf(domain.ErrInvalidArgument, "%v", err)
	}
	if err := keyutil.ValidateKey(releaseName); err != nil {
		return domain.ConfigurationSchema{}, domain.Errorf(domain.ErrInvalidArgument, "invalid release name: %v", err)
	}
	if len(schemaJSON) == 0 || len(schemaJSON) > maxSchemaBytes {
		return domain.ConfigurationSchema{}, domain.Errorf(domain.ErrInvalidArgument, "schema must contain between 1 and %d bytes", maxSchemaBytes)
	}
	metadata, err := validateReleaseMetadata(metadata)
	if err != nil {
		return domain.ConfigurationSchema{}, err
	}
	compactSchema := jsontext.Value(schemaJSON)
	if err := compactSchema.Compact(
		jsontext.AllowDuplicateNames(false),
		jsontext.AllowInvalidUTF8(false),
	); err != nil {
		return domain.ConfigurationSchema{}, domain.Errorf(domain.ErrInvalidArgument, "invalid Draft 2020-12 JSON Schema")
	}
	schemaJSON = string(compactSchema)
	if _, err := compileSchema(schemaJSON); err != nil {
		return domain.ConfigurationSchema{}, domain.Errorf(domain.ErrInvalidArgument, "invalid Draft 2020-12 JSON Schema")
	}
	return domain.ConfigurationSchema{Application: application, ReleaseName: releaseName, Schema: schemaJSON, Digest: sha256Hex([]byte(schemaJSON)), Metadata: metadata}, nil
}

func (s *Service) GetConfigurationSchema(ctx context.Context, pr Principal, application, releaseName string, version uint64) (domain.ConfigurationSchema, error) {
	if err := keyutil.ValidateApp(application); err != nil {
		return domain.ConfigurationSchema{}, domain.Errorf(domain.ErrInvalidArgument, "%v", err)
	}
	if err := keyutil.ValidateKey(releaseName); err != nil {
		return domain.ConfigurationSchema{}, domain.Errorf(domain.ErrInvalidArgument, "invalid release name: %v", err)
	}
	if version == 0 {
		return domain.ConfigurationSchema{}, domain.Errorf(domain.ErrInvalidArgument, "version is required")
	}
	resource := application + "/" + releaseName
	if err := s.requireAdmin(ctx, pr, "configuration_schema.read", domain.ResourceConfigurationSchema, resource); err != nil {
		return domain.ConfigurationSchema{}, err
	}
	rs, err := s.releaseStore()
	if err != nil {
		return domain.ConfigurationSchema{}, err
	}
	return rs.GetConfigurationSchema(ctx, application, releaseName, version)
}
func (s *Service) ListConfigurationSchemas(ctx context.Context, pr Principal, application, releaseName string, page storage.ListPage) ([]domain.ConfigurationSchema, string, error) {
	if application == "" && releaseName != "" {
		return nil, "", domain.Errorf(domain.ErrInvalidArgument, "application is required when release_name is set")
	}
	if application != "" {
		if err := keyutil.ValidateApp(application); err != nil {
			return nil, "", domain.Errorf(domain.ErrInvalidArgument, "%v", err)
		}
	}
	if releaseName != "" {
		if err := keyutil.ValidateKey(releaseName); err != nil {
			return nil, "", domain.Errorf(domain.ErrInvalidArgument, "invalid release name: %v", err)
		}
	}
	resource := strings.Trim(application+"/"+releaseName, "/")
	if err := s.requireAdmin(ctx, pr, "configuration_schema.list", domain.ResourceConfigurationSchema, resource); err != nil {
		return nil, "", err
	}
	rs, err := s.releaseStore()
	if err != nil {
		return nil, "", err
	}
	return rs.ListConfigurationSchemas(ctx, application, releaseName, page)
}

func validateReleaseMetadata(v string) (string, error) {
	if len(v) > maxReleaseMetadataBytes {
		return "", domain.Errorf(domain.ErrInvalidArgument, "metadata exceeds %d bytes", maxReleaseMetadataBytes)
	}
	return validateMetadataJSON(v)
}
func sha256Hex(v []byte) string { sum := sha256.Sum256(v); return hex.EncodeToString(sum[:]) }
func releaseDigest(r domain.ConfigurationRelease) (string, error) {
	entries := append([]domain.ConfigurationReleaseEntry(nil), r.Entries...)
	sort.Slice(entries, func(i, j int) bool { return entries[i].Alias < entries[j].Alias })
	pb := &kmsv1.ConfigurationRelease{Namespace: &kmsv1.NamespaceRef{Env: r.Namespace.Env, App: r.Namespace.App}, Name: r.Name, SchemaVersion: r.SchemaVersion, MetadataJson: r.Metadata}
	for _, e := range entries {
		pb.Entries = append(pb.Entries, &kmsv1.ConfigurationReleaseEntry{Alias: e.Alias, Kind: e.Kind, Ref: &kmsv1.ResourceRef{Namespace: &kmsv1.NamespaceRef{Env: e.Ref.NS.Env, App: e.Ref.NS.App}, Key: e.Ref.Key}, Version: e.Version, ContentType: e.ContentType, MetadataJson: e.Metadata, ParameterDigest: e.ParameterDigest})
	}
	b, err := (proto.MarshalOptions{Deterministic: true}).Marshal(pb)
	if err != nil {
		return "", err
	}
	return sha256Hex(b), nil
}
func parameterSchemaValue(value, contentType string) (any, error) {
	if contentType == "json" {
		return decodeStrictJSON(value)
	}
	return parseParameterValue(value, contentType)
}
func validationAuthError(alias string, err error) domain.ReleaseValidationError {
	code := domain.ReleaseValidationPermissionDenied
	if errors.Is(err, domain.ErrNotFound) || errors.Is(err, domain.ErrAborted) {
		code = domain.ReleaseValidationNotFound
	}
	return domain.ReleaseValidationError{Alias: alias, Code: code, Message: "referenced resource is not readable"}
}
func validationReadError(alias string, err error) domain.ReleaseValidationError {
	code := domain.ReleaseValidationUnreadable
	if errors.Is(err, domain.ErrNotFound) || errors.Is(err, domain.ErrAborted) {
		code = domain.ReleaseValidationNotFound
	}
	return domain.ReleaseValidationError{Alias: alias, Code: code, Message: "referenced resource is unavailable"}
}
func compileSchema(raw string) (*jsonschema.Schema, error) {
	doc, err := decodeStrictJSON(raw)
	if err != nil {
		return nil, err
	}
	if obj, ok := doc.(map[string]any); ok {
		if dialect, ok := obj["$schema"].(string); ok && strings.TrimSuffix(dialect, "#") != "https://json-schema.org/draft/2020-12/schema" {
			return nil, fmt.Errorf("unsupported JSON Schema dialect")
		}
	}
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	configureKMSFormats(c, doc)
	const u = "https://kms.local/configuration-schema.json"
	if err := c.AddResource(u, doc); err != nil {
		return nil, err
	}
	return c.Compile(u)
}
func sanitizeSchemaErrors(err error) []domain.ReleaseValidationError {
	var ve *jsonschema.ValidationError
	if !errors.As(err, &ve) {
		return []domain.ReleaseValidationError{{Code: domain.ReleaseValidationSchema, Message: "configuration does not satisfy schema"}}
	}
	leaves := schemaErrorLeaves(ve, nil)
	if len(leaves) > 64 {
		leaves = leaves[:64]
	}
	out := make([]domain.ReleaseValidationError, 0, len(leaves))
	for _, e := range leaves {
		alias := ""
		if len(e.InstanceLocation) > 0 {
			alias = e.InstanceLocation[0]
		}
		ptr := "/" + strings.Join(e.ErrorKind.KeywordPath(), "/")
		if ptr == "/" {
			ptr = ""
		}
		out = append(out, domain.ReleaseValidationError{Alias: alias, Code: domain.ReleaseValidationSchema, SchemaPointer: ptr, Message: "configuration value does not satisfy schema"})
	}
	return out
}
func schemaErrorLeaves(e *jsonschema.ValidationError, out []*jsonschema.ValidationError) []*jsonschema.ValidationError {
	if len(e.Causes) == 0 {
		return append(out, e)
	}
	for _, c := range e.Causes {
		out = schemaErrorLeaves(c, out)
	}
	return out
}
func validReleaseState(v string) bool {
	return v == domain.ReleaseStateReceived || v == domain.ReleaseStatePrepared || v == domain.ReleaseStateApplied || v == domain.ReleaseStateRejected
}
func validRejectCategory(v string) bool {
	switch v {
	case domain.ReleaseRejectResolutionFailed,
		domain.ReleaseRejectTokenUnavailable,
		domain.ReleaseRejectVersionMismatch,
		domain.ReleaseRejectDigestMismatch,
		domain.ReleaseRejectPrepareFailed,
		domain.ReleaseRejectConfigContractMismatch,
		domain.ReleaseRejectConfigDecodeFailed,
		domain.ReleaseRejectConfigValidationFailed,
		domain.ReleaseRejectDefaultMismatch,
		domain.ReleaseRejectRestartRequired,
		domain.ReleaseRejectSuperseded,
		domain.ReleaseRejectActiveCheck,
		domain.ReleaseRejectInternal:
		return true
	}
	return false
}
func sanitizeDiagnostic(v string) string {
	if v == "" {
		return ""
	}
	// Application text is untrusted and can accidentally contain a resolved
	// secret. KMS cannot reliably distinguish prose from secret material, so v1
	// persists only a fixed redaction marker; operators still have the bounded
	// rejection category for diagnosis.
	return "[redacted]"
}
