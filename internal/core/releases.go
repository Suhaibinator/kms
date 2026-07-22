package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	maxSchemaBytes          = 256 << 10
	maxAckDiagnosticBytes   = 1024
	maxReleaseClientIDBytes = 128
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
	if (in.SchemaID == "") != (in.SchemaVersion == 0) {
		return domain.ConfigurationRelease{}, domain.Errorf(domain.ErrInvalidArgument, "schema_id and schema_version must be specified together")
	}
	ctx, namespace, err := s.authorize(ctx, pr, domain.OpConfigurationReleaseCreate, domain.ResourceConfigurationRelease, domain.Ref{NS: in.Namespace, Key: in.Name})
	if err != nil {
		return domain.ConfigurationRelease{}, err
	}
	rs, err := s.releaseStore()
	if err != nil {
		return domain.ConfigurationRelease{}, err
	}
	if in.SchemaID != "" {
		if _, err := rs.GetConfigurationSchema(ctx, in.SchemaID, in.SchemaVersion); err != nil {
			return domain.ConfigurationRelease{}, err
		}
	}

	seen := make(map[string]struct{}, len(in.Entries))
	entries := make([]domain.ConfigurationReleaseEntry, 0, len(in.Entries))
	for _, sel := range in.Entries {
		if len(sel.Alias) == 0 || len(sel.Alias) > maxReleaseAliasBytes || !releaseAliasRE.MatchString(sel.Alias) {
			return domain.ConfigurationRelease{}, domain.Errorf(domain.ErrInvalidArgument, "invalid release alias %q", sel.Alias)
		}
		if _, ok := seen[sel.Alias]; ok {
			return domain.ConfigurationRelease{}, domain.Errorf(domain.ErrInvalidArgument, "duplicate release alias %q", sel.Alias)
		}
		seen[sel.Alias] = struct{}{}
		if sel.Version > 0 && sel.Label != "" {
			return domain.ConfigurationRelease{}, domain.Errorf(domain.ErrInvalidArgument, "release alias %q specifies both version and label", sel.Alias)
		}
		if sel.Ref.NS == (domain.NamespaceRef{}) {
			sel.Ref.NS = in.Namespace
		}
		if err := validateRef(sel.Ref); err != nil {
			return domain.ConfigurationRelease{}, err
		}
		label := sel.Label
		if sel.Version == 0 && label == "" {
			label = domain.LabelCurrent
		}
		switch sel.Kind {
		case domain.ReleaseEntryParameter:
			ctx, _, err = s.authorize(ctx, pr, domain.OpParameterRead, domain.ResourceParameter, sel.Ref)
			if err != nil {
				return domain.ConfigurationRelease{}, err
			}
			p, err := s.store.GetParameter(ctx, sel.Ref, sel.Version, label)
			if err != nil {
				return domain.ConfigurationRelease{}, err
			}
			if len(p.Metadata) > maxReleaseMetadataBytes {
				return domain.ConfigurationRelease{}, domain.Errorf(domain.ErrFailedPrecondition, "release alias %q metadata exceeds release limit", sel.Alias)
			}
			entries = append(entries, domain.ConfigurationReleaseEntry{Alias: sel.Alias, Kind: sel.Kind, Ref: sel.Ref, Version: p.Version, ContentType: p.ContentType, Metadata: p.Metadata, ParameterDigest: sha256Hex([]byte(p.Value))})
		case domain.ReleaseEntrySecret:
			ctx, _, err = s.authorize(ctx, pr, domain.OpSecretRead, domain.ResourceSecret, sel.Ref)
			if err != nil {
				return domain.ConfigurationRelease{}, err
			}
			_, ver, err := s.store.GetSecretVersion(ctx, sel.Ref, sel.Version, label)
			if err != nil {
				return domain.ConfigurationRelease{}, err
			}
			if ver.State == domain.StateDestroyed {
				return domain.ConfigurationRelease{}, domain.Errorf(domain.ErrFailedPrecondition, "release alias %q references a destroyed secret version", sel.Alias)
			}
			if len(ver.Metadata) > maxReleaseMetadataBytes {
				return domain.ConfigurationRelease{}, domain.Errorf(domain.ErrFailedPrecondition, "release alias %q metadata exceeds release limit", sel.Alias)
			}
			entries = append(entries, domain.ConfigurationReleaseEntry{Alias: sel.Alias, Kind: sel.Kind, Ref: sel.Ref, Version: ver.Version, ContentType: ver.ContentType, Metadata: ver.Metadata, ClientBound: ver.ClientBound, HasAccessToken: ver.HasAccessToken})
		default:
			return domain.ConfigurationRelease{}, domain.Errorf(domain.ErrInvalidArgument, "release alias %q has unknown kind %q", sel.Alias, sel.Kind)
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Alias < entries[j].Alias })
	release := domain.ConfigurationRelease{Namespace: in.Namespace, Name: in.Name, SchemaID: in.SchemaID, SchemaVersion: in.SchemaVersion, Entries: entries, Metadata: metadata, CreatedBy: pr.Identity.Name}
	release.Digest, err = releaseDigest(release)
	if err != nil {
		return domain.ConfigurationRelease{}, fmt.Errorf("calculate configuration release digest: %w", err)
	}
	out, err := rs.CreateConfigurationRelease(ctx, release)
	if err != nil {
		return domain.ConfigurationRelease{}, err
	}
	s.auditRefWithNamespaceID(ctx, pr, "configuration_release.create", domain.ResourceConfigurationRelease, domain.Ref{NS: in.Namespace, Key: in.Name}, namespace.ID, out.Version, "allow", nil)
	return out, nil
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
	rel, err := rs.GetConfigurationRelease(ctx, ns, name, version)
	if err != nil {
		return nil, err
	}
	validation := make([]domain.ReleaseValidationError, 0)
	obj := map[string]any{}
	for _, entry := range rel.Entries {
		switch entry.Kind {
		case domain.ReleaseEntryParameter:
			entryCtx, _, authErr := s.authorize(ctx, pr, domain.OpParameterRead, domain.ResourceParameter, entry.Ref)
			if authErr != nil {
				validation = append(validation, validationAuthError(entry.Alias, authErr))
				continue
			}
			ctx = entryCtx
			p, err := s.store.GetParameter(ctx, entry.Ref, entry.Version, "")
			if err != nil {
				validation = append(validation, validationReadError(entry.Alias, err))
				continue
			}
			if got := sha256Hex([]byte(p.Value)); got != entry.ParameterDigest {
				validation = append(validation, domain.ReleaseValidationError{Alias: entry.Alias, Code: domain.ReleaseValidationDigest, Message: "parameter digest does not match release pin"})
				continue
			}
			if p.ContentType != entry.ContentType {
				validation = append(validation, domain.ReleaseValidationError{Alias: entry.Alias, Code: domain.ReleaseValidationContentType, Message: "parameter content type does not match release pin"})
				continue
			}
			value, err := parameterSchemaValue(p.Value, entry.ContentType)
			if err != nil {
				code := domain.ReleaseValidationContentType
				if p.ContentType == "json" {
					code = domain.ReleaseValidationMalformedJSON
				}
				validation = append(validation, domain.ReleaseValidationError{Alias: entry.Alias, Code: code, Message: "parameter does not match its declared content type"})
				continue
			}
			obj[entry.Alias] = value
		case domain.ReleaseEntrySecret:
			entryCtx, _, authErr := s.authorize(ctx, pr, domain.OpSecretRead, domain.ResourceSecret, entry.Ref)
			if authErr != nil {
				validation = append(validation, validationAuthError(entry.Alias, authErr))
				continue
			}
			ctx = entryCtx
			_, ver, err := s.store.GetSecretVersion(ctx, entry.Ref, entry.Version, "")
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
			if ver.ClientBound != entry.ClientBound || ver.HasAccessToken != entry.HasAccessToken {
				validation = append(validation, domain.ReleaseValidationError{Alias: entry.Alias, Code: domain.ReleaseValidationUnreadable, Message: "secret protection metadata does not match release pin"})
			}
		default:
			validation = append(validation, domain.ReleaseValidationError{Alias: entry.Alias, Code: domain.ReleaseValidationUnreadable, Message: "release entry kind is invalid"})
		}
	}
	if len(validation) == 0 && rel.SchemaID != "" {
		schrec, err := rs.GetConfigurationSchema(ctx, rel.SchemaID, rel.SchemaVersion)
		if err != nil {
			validation = append(validation, domain.ReleaseValidationError{Code: domain.ReleaseValidationNotFound, Message: "configuration schema is unavailable"})
		} else if sch, err := compileSchema(schrec.Schema); err != nil {
			validation = append(validation, domain.ReleaseValidationError{Code: domain.ReleaseValidationSchema, Message: "configuration schema is invalid"})
		} else if err := sch.Validate(obj); err != nil {
			validation = append(validation, sanitizeSchemaErrors(err)...)
		}
	}
	decision := "allow"
	if len(validation) > 0 {
		decision = "error"
	}
	s.auditRefWithNamespaceID(ctx, pr, "configuration_release.validate", domain.ResourceConfigurationRelease, domain.Ref{NS: ns, Key: name}, namespace.ID, version, decision, map[string]string{"error_count": strconv.Itoa(len(validation))})
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
	active, changed, err := rs.ActivateConfigurationRelease(ctx, ns, name, version, expectedCurrent)
	if err != nil {
		decision := "error"
		event := "configuration_release.activate"
		if errors.Is(err, domain.ErrAborted) {
			event = "configuration_release.cas_conflict"
			decision = "deny"
		}
		s.auditRefWithNamespaceID(ctx, pr, event, domain.ResourceConfigurationRelease, domain.Ref{NS: ns, Key: name}, namespace.ID, version, decision, nil)
		return domain.ActiveConfigurationRelease{}, false, err
	}
	if changed {
		event := "configuration_release.activate"
		if active.PreviousVersion > 0 && version < active.PreviousVersion {
			event = "configuration_release.rollback"
		}
		s.auditRefWithNamespaceID(ctx, pr, event, domain.ResourceConfigurationRelease, domain.Ref{NS: ns, Key: name}, namespace.ID, version, "allow", map[string]string{"previous_version": strconv.FormatUint(active.PreviousVersion, 10)})
		s.getHub().Wake()
	}
	return active, changed, nil
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
	s.auditRefWithNamespaceID(ctx, pr, "configuration_release.acknowledge", domain.ResourceConfigurationRelease, domain.Ref{NS: ack.Namespace, Key: ack.ReleaseName}, namespace.ID, ack.ReleaseVersion, "allow", map[string]string{"state": ack.State, "category": ack.RejectionCategory, "client_name": ack.ClientName, "instance_id": ack.InstanceID})
	return nil
}

func (s *Service) SetReleaseSubscriberConnected(ctx context.Context, ns domain.NamespaceRef, name, clientName, instanceID, identity, connectionID string, connected bool) error {
	rs, err := s.releaseStore()
	if err != nil {
		return err
	}
	return rs.SetReleaseInstanceConnected(ctx, domain.ReleaseSubscriberConnection{
		Namespace: ns, ReleaseName: name, ClientName: clientName, InstanceID: instanceID,
		Identity: identity, ConnectionID: connectionID, Connected: connected, ServerTimestamp: s.now(),
	})
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

func (s *Service) CreateConfigurationSchema(ctx context.Context, pr Principal, id, schemaJSON, metadata string) (domain.ConfigurationSchema, error) {
	if err := s.requireAdmin(ctx, pr, "configuration_schema.create", domain.ResourceConfigurationSchema, id); err != nil {
		return domain.ConfigurationSchema{}, err
	}
	if len(id) == 0 || len(id) > 256 || strings.ContainsAny(id, " \t\r\n") {
		return domain.ConfigurationSchema{}, domain.Errorf(domain.ErrInvalidArgument, "invalid schema id")
	}
	if len(schemaJSON) == 0 || len(schemaJSON) > maxSchemaBytes {
		return domain.ConfigurationSchema{}, domain.Errorf(domain.ErrInvalidArgument, "schema must contain between 1 and %d bytes", maxSchemaBytes)
	}
	metadata, err := validateReleaseMetadata(metadata)
	if err != nil {
		return domain.ConfigurationSchema{}, err
	}
	if _, err := compileSchema(schemaJSON); err != nil {
		return domain.ConfigurationSchema{}, domain.Errorf(domain.ErrInvalidArgument, "invalid Draft 2020-12 JSON Schema")
	}
	rs, err := s.releaseStore()
	if err != nil {
		return domain.ConfigurationSchema{}, err
	}
	out, err := rs.CreateConfigurationSchema(ctx, domain.ConfigurationSchema{ID: id, Schema: schemaJSON, Digest: sha256Hex([]byte(schemaJSON)), Metadata: metadata, CreatedBy: pr.Identity.Name})
	if err == nil {
		s.auditName(ctx, pr, "configuration_schema.create", domain.ResourceConfigurationSchema, id, "allow", map[string]string{"version": strconv.FormatUint(out.Version, 10)})
	}
	return out, err
}

func (s *Service) GetConfigurationSchema(ctx context.Context, pr Principal, id string, version uint64) (domain.ConfigurationSchema, error) {
	if err := s.requireAdmin(ctx, pr, "configuration_schema.read", domain.ResourceConfigurationSchema, id); err != nil {
		return domain.ConfigurationSchema{}, err
	}
	rs, err := s.releaseStore()
	if err != nil {
		return domain.ConfigurationSchema{}, err
	}
	return rs.GetConfigurationSchema(ctx, id, version)
}
func (s *Service) ListConfigurationSchemas(ctx context.Context, pr Principal, id string, page storage.ListPage) ([]domain.ConfigurationSchema, string, error) {
	if err := s.requireAdmin(ctx, pr, "configuration_schema.list", domain.ResourceConfigurationSchema, id); err != nil {
		return nil, "", err
	}
	rs, err := s.releaseStore()
	if err != nil {
		return nil, "", err
	}
	return rs.ListConfigurationSchemas(ctx, id, page)
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
	pb := &kmsv1.ConfigurationRelease{Namespace: &kmsv1.NamespaceRef{Env: r.Namespace.Env, App: r.Namespace.App}, Name: r.Name, SchemaId: r.SchemaID, SchemaVersion: r.SchemaVersion, MetadataJson: r.Metadata}
	for _, e := range entries {
		pb.Entries = append(pb.Entries, &kmsv1.ConfigurationReleaseEntry{Alias: e.Alias, Kind: e.Kind, Ref: &kmsv1.ResourceRef{Namespace: &kmsv1.NamespaceRef{Env: e.Ref.NS.Env, App: e.Ref.NS.App}, Key: e.Ref.Key}, Version: e.Version, ContentType: e.ContentType, MetadataJson: e.Metadata, ParameterDigest: e.ParameterDigest, ClientBound: e.ClientBound, HasAccessToken: e.HasAccessToken})
	}
	b, err := (proto.MarshalOptions{Deterministic: true}).Marshal(pb)
	if err != nil {
		return "", err
	}
	return sha256Hex(b), nil
}
func parameterSchemaValue(value, contentType string) (any, error) {
	return parseParameterValue(value, contentType)
}
func validationAuthError(alias string, err error) domain.ReleaseValidationError {
	code := domain.ReleaseValidationPermissionDenied
	if errors.Is(err, domain.ErrNotFound) {
		code = domain.ReleaseValidationNotFound
	}
	return domain.ReleaseValidationError{Alias: alias, Code: code, Message: "referenced resource is not readable"}
}
func validationReadError(alias string, err error) domain.ReleaseValidationError {
	code := domain.ReleaseValidationUnreadable
	if errors.Is(err, domain.ErrNotFound) {
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
