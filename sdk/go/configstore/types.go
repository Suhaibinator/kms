package configstore

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/Suhaibinator/kms/sdk/go/kmsclient"
)

// Validatable is the minimal validation contract implemented by a managed
// configuration root.
type Validatable interface {
	Validate() error
}

// ContractKind identifies the resource kind assigned to a stable release
// alias.
type ContractKind string

const (
	ContractKindParameter ContractKind = "parameter"
	ContractKindSecret    ContractKind = "secret"
)

// ContractEntry describes one alias required by generated bindings. A
// parameter ContentType is exact; an empty secret ContentType is a wildcard.
type ContractEntry struct {
	Alias       string
	Kind        ContractKind
	ContentType string
}

// Callbacks groups the application-owned observers of a managed store.
// OnDefaultMismatch is required; the others are optional. Every callback runs
// synchronously on the loader goroutine and must not block; a panic is
// isolated and never affects candidate admission. SlogCallbacks returns a
// ready-made implementation.
type Callbacks struct {
	// OnDefaultMismatch reports every candidate whose non-secret values differ
	// from source defaults, at startup and on every reload. The candidate is
	// still applied; the report is the signal to reconcile code and KMS.
	OnDefaultMismatch func(DefaultMismatchReport)
	// OnApplied fires after each generation is published, including the
	// initial one, carrying the fields that changed since the previously
	// applied generation.
	OnApplied func(AppliedReport)
	// OnCandidateRejected reports a candidate that was not admitted.
	OnCandidateRejected func(CandidateRejectionReport)
}

// Options configures a managed configuration manager.
type Options struct {
	Release  string
	Contract []ContractEntry
	Callbacks
	SecretTokenProvider kmsclient.SecretTokenProvider
	// BindingKeys is an internal alias-keyed credential map assembled by
	// generated stores from declaration-only Secret.BindKey fields.
	BindingKeys          map[string]string
	ReconcileInterval    time.Duration
	MaxConcurrentFetches int
	InstanceID           string
}

// PrepareFunc constructs a complete immutable candidate away from the active
// generation. Publish must perform the generated binding's atomic pointer
// swap; Abort releases any candidate-owned resources.
type PrepareFunc func(context.Context, kmsclient.ReleaseSnapshot) (PreparedCandidate, error)

// PreparedCandidate is generated, validated state ready for policy admission.
// DefaultDifferences and Changed must contain non-secret canonical fields
// only (secret entries in Changed are path-only). Groups, when set, lazily
// encodes the candidate's non-secret parameter groups for observability.
type PreparedCandidate struct {
	Publish               func()
	Abort                 func()
	DefaultDifferences    []FieldDifference
	RestartRequiredFields []string
	// Changed lists fields that differ from the previously applied generation.
	// It is ignored for the initial generation.
	Changed []FieldChange
	// Groups returns the canonical non-secret parameter group documents of the
	// candidate, keyed by alias. It must never include secret values.
	Groups func() (map[string]jsontext.Value, error)
}

// Phase identifies whether a candidate was the initial generation or a reload.
type Phase string

const (
	PhaseStartup Phase = "startup"
	PhaseRuntime Phase = "runtime"
)

// MismatchPhase is retained for callback code written against earlier SDK
// versions; it is identical to Phase.
type MismatchPhase = Phase

const (
	MismatchStartup = PhaseStartup
	MismatchRuntime = PhaseRuntime
)

// MismatchSeverity describes the handling of a default mismatch. Since
// mismatches are applied and reported rather than refused, the only value is
// MismatchError; the type is retained so existing callback code keeps
// compiling.
type MismatchSeverity string

const (
	MismatchError MismatchSeverity = "error"
)

// FieldChange is one non-secret field that differs between the previously
// applied generation and a newly applied one. Secret rotations are reported
// path-only with nil Previous and Current.
type FieldChange struct {
	Path     string `json:"path"`
	Previous any    `json:"previous"`
	Current  any    `json:"current"`
}

// AppliedReport is an immutable view of one published generation. Changed and
// Groups return fresh copies on every call; Groups is empty when the
// generated binding did not supply group documents.
type AppliedReport interface {
	Phase() Phase
	Release() ReleaseIdentity
	DefaultDivergent() bool
	Changed() []FieldChange
	Groups() (map[string]jsontext.Value, error)
}

type appliedReport struct {
	phase     Phase
	release   ReleaseIdentity
	divergent bool
	changes   []FieldChange
	groups    func() (map[string]jsontext.Value, error)
}

func newAppliedReport(phase Phase, release ReleaseIdentity, divergent bool, changes []FieldChange, groups func() (map[string]jsontext.Value, error)) *appliedReport {
	return &appliedReport{phase: phase, release: release, divergent: divergent, changes: cloneChanges(changes), groups: groups}
}

func (r *appliedReport) Phase() Phase             { return r.phase }
func (r *appliedReport) Release() ReleaseIdentity { return r.release }
func (r *appliedReport) DefaultDivergent() bool   { return r.divergent }
func (r *appliedReport) Changed() []FieldChange   { return cloneChanges(r.changes) }

func (r *appliedReport) Groups() (map[string]jsontext.Value, error) {
	if r.groups == nil {
		return map[string]jsontext.Value{}, nil
	}
	groups, err := r.groups()
	if err != nil {
		return nil, err
	}
	out := make(map[string]jsontext.Value, len(groups))
	for alias, document := range groups {
		out[alias] = document.Clone()
	}
	return out, nil
}

// String lists only canonical field paths. Previous and current values remain
// available explicitly through Changed.
func (r *appliedReport) String() string {
	paths := make([]string, len(r.changes))
	for i := range r.changes {
		paths[i] = r.changes[i].Path
	}
	return fmt.Sprintf("configstore: applied (%s) %s divergent=%t changed=%s",
		r.phase, r.release.String(), r.divergent, strings.Join(paths, ","))
}

func (r *appliedReport) GoString() string { return r.String() }

func (r *appliedReport) Format(f fmt.State, verb rune) {
	if verb == 'q' {
		_, _ = fmt.Fprintf(f, "%q", r.String())
		return
	}
	_, _ = io.WriteString(f, r.String())
}

func (r *appliedReport) MarshalJSON() ([]byte, error) {
	return json.Marshal(r.jsonProjection())
}

type appliedReportJSON struct {
	Phase            Phase           `json:"phase"`
	Release          ReleaseIdentity `json:"release"`
	DefaultDivergent bool            `json:"default_divergent"`
	Changed          []FieldChange   `json:"changed"`
}

func (r *appliedReport) jsonProjection() appliedReportJSON {
	return appliedReportJSON{Phase: r.phase, Release: r.release, DefaultDivergent: r.divergent, Changed: r.changes}
}

// MarshalJSONTo streams the same safe report projection as MarshalJSON.
func (r *appliedReport) MarshalJSONTo(out *jsontext.Encoder) error {
	return json.MarshalEncode(out, r.jsonProjection())
}

// FieldDifference contains one canonical, non-secret default comparison.
type FieldDifference struct {
	Path     string `json:"path"`
	Expected any    `json:"expected"`
	Actual   any    `json:"actual"`
}

// DefaultMismatchReport is an immutable view of one candidate comparison.
// Fields returns a fresh deep copy on every call.
type DefaultMismatchReport interface {
	Phase() MismatchPhase
	Severity() MismatchSeverity
	Release() ReleaseIdentity
	Fields() []FieldDifference
}

type defaultMismatchReport struct {
	phase       MismatchPhase
	severity    MismatchSeverity
	release     ReleaseIdentity
	differences []FieldDifference
}

func newDefaultMismatchReport(
	phase MismatchPhase,
	severity MismatchSeverity,
	release ReleaseIdentity,
	differences []FieldDifference,
) *defaultMismatchReport {
	return &defaultMismatchReport{
		phase:       phase,
		severity:    severity,
		release:     release,
		differences: cloneDifferences(differences),
	}
}

func (r *defaultMismatchReport) Phase() MismatchPhase       { return r.phase }
func (r *defaultMismatchReport) Severity() MismatchSeverity { return r.severity }
func (r *defaultMismatchReport) Release() ReleaseIdentity   { return r.release }
func (r *defaultMismatchReport) Fields() []FieldDifference  { return cloneDifferences(r.differences) }

// String lists only canonical field paths. Expected and actual values remain
// available explicitly through Fields.
func (r *defaultMismatchReport) String() string {
	paths := make([]string, len(r.differences))
	for i := range r.differences {
		paths[i] = r.differences[i].Path
	}
	return fmt.Sprintf("configstore: default mismatch (%s/%s) for %s fields=%s",
		r.phase, r.severity, r.release.String(), strings.Join(paths, ","))
}

func (r *defaultMismatchReport) GoString() string { return r.String() }

func (r *defaultMismatchReport) Format(f fmt.State, verb rune) {
	if verb == 'q' {
		_, _ = fmt.Fprintf(f, "%q", r.String())
		return
	}
	_, _ = io.WriteString(f, r.String())
}

func (r *defaultMismatchReport) MarshalJSON() ([]byte, error) {
	return json.Marshal(r.jsonProjection())
}

type defaultMismatchReportJSON struct {
	Phase       MismatchPhase     `json:"phase"`
	Severity    MismatchSeverity  `json:"severity"`
	Release     ReleaseIdentity   `json:"release"`
	Differences []FieldDifference `json:"differences"`
}

func (r *defaultMismatchReport) jsonProjection() defaultMismatchReportJSON {
	return defaultMismatchReportJSON{
		Phase:       r.phase,
		Severity:    r.severity,
		Release:     r.release,
		Differences: r.differences,
	}
}

// MarshalJSONTo streams the same safe report projection as MarshalJSON.
func (r *defaultMismatchReport) MarshalJSONTo(out *jsontext.Encoder) error {
	return json.MarshalEncode(out, r.jsonProjection())
}

// RejectionCategory is a bounded release acknowledgement and metrics label.
type RejectionCategory string

const (
	RejectConfigContractMismatch RejectionCategory = "config_contract_mismatch"
	RejectConfigDecodeFailed     RejectionCategory = "config_decode_failed"
	RejectConfigValidationFailed RejectionCategory = "config_validation_failed"
	// RejectDefaultMismatch is a legacy category: current managers apply and
	// report divergent candidates instead of rejecting them. It remains a valid
	// bounded wire value so older acknowledgements keep their category.
	RejectDefaultMismatch RejectionCategory = "default_mismatch"
	RejectRestartRequired RejectionCategory = "restart_required"
	RejectInternal        RejectionCategory = "internal"
)

// CandidateError classifies a candidate without exposing its underlying
// diagnostic through ordinary formatting. Unwrap remains available for
// intentional local inspection and errors.As/errors.Is.
type CandidateError struct {
	category RejectionCategory
	cause    error
	paths    []string
}

// Reject returns a redacting classified candidate error.
func Reject(category RejectionCategory, cause error) error {
	return rejectWithPaths(category, cause, nil)
}

// RejectDecode classifies a DecodeGroup failure and, when the error came from
// the strict decoder, translates its private location into a safe canonical
// group path for OnCandidateRejected. Raw JSON property names are never used.
func RejectDecode(group string, cause error) error {
	var paths []string
	if validDiagnosticSegment(group) {
		if pathError, ok := errors.AsType[*decodePathError](cause); ok {
			switch {
			case pathError.path == "$":
				paths = []string{group}
			case strings.HasPrefix(pathError.path, "$."):
				paths = []string{group + strings.TrimPrefix(pathError.path, "$")}
			}
		}
	}
	return rejectWithPaths(RejectConfigDecodeFailed, cause, paths)
}

func rejectWithPaths(category RejectionCategory, cause error, paths []string) error {
	if !validRejectionCategory(category) {
		category = RejectInternal
	}
	if cause == nil {
		cause = errors.New("configstore: candidate rejected")
	}
	return &CandidateError{category: category, cause: cause, paths: sanitizeDiagnosticPaths(paths)}
}

func (e *CandidateError) pathsCopy() []string {
	if e == nil {
		return nil
	}
	return append([]string(nil), e.paths...)
}

func validRejectionCategory(category RejectionCategory) bool {
	switch category {
	case RejectConfigContractMismatch,
		RejectConfigDecodeFailed,
		RejectConfigValidationFailed,
		RejectDefaultMismatch,
		RejectRestartRequired,
		RejectInternal:
		return true
	default:
		return false
	}
}

func sanitizeDiagnosticPaths(paths []string) []string {
	if len(paths) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(paths))
	result := make([]string, 0, len(paths))
	for _, path := range paths {
		if !validDiagnosticPath(path) {
			continue
		}
		if _, duplicate := seen[path]; duplicate {
			continue
		}
		seen[path] = struct{}{}
		result = append(result, path)
	}
	return result
}

func validDiagnosticPath(path string) bool {
	if path == "" || len(path) > 512 {
		return false
	}
	segments := strings.Split(path, ".")
	if len(segments) > 32 {
		return false
	}
	for _, segment := range segments {
		base := segment
		for strings.HasSuffix(base, "[]") || strings.HasSuffix(base, "[*]") {
			if before, ok := strings.CutSuffix(base, "[]"); ok {
				base = before
			} else {
				base = strings.TrimSuffix(base, "[*]")
			}
		}
		if !validDiagnosticSegment(base) {
			return false
		}
	}
	return true
}

func validDiagnosticSegment(segment string) bool {
	if len(segment) == 0 || len(segment) > 64 || !isASCIIAlpha(segment[0]) {
		return false
	}
	for i := 1; i < len(segment); i++ {
		character := segment[i]
		if !isASCIIAlpha(character) && (character < '0' || character > '9') && character != '_' && character != '-' {
			return false
		}
	}
	return true
}

func isASCIIAlpha(character byte) bool {
	return character >= 'A' && character <= 'Z' || character >= 'a' && character <= 'z'
}

func (e *CandidateError) Error() string {
	if e == nil {
		return "configstore: candidate rejected"
	}
	return fmt.Sprintf("configstore: candidate rejected (%s)", e.category)
}

func (e *CandidateError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

// ReleaseRejectionCategory is consumed by kmsclient.ReleaseLoader without
// importing this package or logging the wrapped diagnostic.
func (e *CandidateError) ReleaseRejectionCategory() string {
	if e == nil {
		return ""
	}
	return string(e.category)
}

func (e *CandidateError) Format(f fmt.State, verb rune) {
	if verb == 'q' {
		_, _ = fmt.Fprintf(f, "%q", e.Error())
		return
	}
	_, _ = io.WriteString(f, e.Error())
}

func (e *CandidateError) MarshalJSON() ([]byte, error) {
	return json.Marshal(e.jsonProjection())
}

type candidateErrorJSON struct {
	Category RejectionCategory `json:"category"`
}

func (e *CandidateError) jsonProjection() candidateErrorJSON {
	return candidateErrorJSON{Category: e.category}
}

// MarshalJSONTo streams the same redacted projection as MarshalJSON.
func (e *CandidateError) MarshalJSONTo(out *jsontext.Encoder) error {
	return json.MarshalEncode(out, e.jsonProjection())
}

// CandidateRejectionReport is a value-free local preparation diagnostic. Paths
// contains only generated canonical field paths; validation errors carry no
// paths because application error text is not trusted to be secret-free.
type CandidateRejectionReport interface {
	Category() RejectionCategory
	Release() ReleaseIdentity
	Paths() []string
}

type candidateRejectionReport struct {
	category RejectionCategory
	release  ReleaseIdentity
	paths    []string
}

func newCandidateRejectionReport(category RejectionCategory, release ReleaseIdentity, paths []string) *candidateRejectionReport {
	return &candidateRejectionReport{category: category, release: release, paths: append([]string(nil), paths...)}
}

func (r *candidateRejectionReport) Category() RejectionCategory { return r.category }
func (r *candidateRejectionReport) Release() ReleaseIdentity    { return r.release }
func (r *candidateRejectionReport) Paths() []string             { return append([]string(nil), r.paths...) }

func (r *candidateRejectionReport) String() string {
	return fmt.Sprintf("configstore: candidate rejection (%s) for %s fields=%s", r.category, r.release.String(), strings.Join(r.paths, ","))
}

func (r *candidateRejectionReport) GoString() string { return r.String() }

func (r *candidateRejectionReport) Format(f fmt.State, verb rune) {
	if verb == 'q' {
		_, _ = fmt.Fprintf(f, "%q", r.String())
		return
	}
	_, _ = io.WriteString(f, r.String())
}

func (r *candidateRejectionReport) MarshalJSON() ([]byte, error) {
	return json.Marshal(r.jsonProjection())
}

type candidateRejectionReportJSON struct {
	Category RejectionCategory `json:"category"`
	Release  ReleaseIdentity   `json:"release"`
	Paths    []string          `json:"paths"`
}

func (r *candidateRejectionReport) jsonProjection() candidateRejectionReportJSON {
	return candidateRejectionReportJSON{Category: r.category, Release: r.release, Paths: r.paths}
}

// MarshalJSONTo streams the same value-free projection as MarshalJSON.
func (r *candidateRejectionReport) MarshalJSONTo(out *jsontext.Encoder) error {
	return json.MarshalEncode(out, r.jsonProjection())
}

// ReleaseIdentity contains immutable, non-sensitive identity copied from a
// resolved candidate. It never retains the snapshot or any resolved value.
type ReleaseIdentity struct {
	namespace          string
	name               string
	version            uint64
	activationRevision uint64
	schemaVersion      uint64
	digest             string
}

// ReleaseIdentityFromSnapshot copies safe identity fields from snapshot.
func ReleaseIdentityFromSnapshot(snapshot kmsclient.ReleaseSnapshot) ReleaseIdentity {
	return ReleaseIdentity{
		namespace:          snapshot.Namespace(),
		name:               snapshot.Name(),
		version:            snapshot.Version(),
		activationRevision: snapshot.ActivationRevision(),
		schemaVersion:      snapshot.SchemaVersion(),
		digest:             snapshot.Digest(),
	}
}

func releaseIdentityFromManifest(manifest kmsclient.ReleaseManifest) ReleaseIdentity {
	return ReleaseIdentity{
		namespace:          manifest.Namespace(),
		name:               manifest.Name(),
		version:            manifest.Version(),
		activationRevision: manifest.ActivationRevision(),
		schemaVersion:      manifest.SchemaVersion(),
		digest:             manifest.Digest(),
	}
}

func (r ReleaseIdentity) Namespace() string          { return r.namespace }
func (r ReleaseIdentity) Name() string               { return r.name }
func (r ReleaseIdentity) Version() uint64            { return r.version }
func (r ReleaseIdentity) ActivationRevision() uint64 { return r.activationRevision }
func (r ReleaseIdentity) SchemaVersion() uint64      { return r.schemaVersion }
func (r ReleaseIdentity) Digest() string             { return r.digest }

func (r ReleaseIdentity) IsZero() bool {
	return r.namespace == "" && r.name == "" && r.version == 0 &&
		r.activationRevision == 0 && r.digest == ""
}

func (r ReleaseIdentity) String() string {
	if r.namespace == "" && r.name == "" {
		return fmt.Sprintf("release@%d#%d", r.version, r.activationRevision)
	}
	return fmt.Sprintf("%s/%s@%d#%d", r.namespace, r.name, r.version, r.activationRevision)
}

func (r ReleaseIdentity) MarshalJSON() ([]byte, error) {
	return json.Marshal(r.jsonProjection())
}

type releaseIdentityJSON struct {
	Namespace          string `json:"namespace"`
	Name               string `json:"name"`
	Version            uint64 `json:"version"`
	ActivationRevision uint64 `json:"activation_revision"`
	SchemaVersion      uint64 `json:"schema_version,omitempty"`
	Digest             string `json:"digest"`
}

func (r ReleaseIdentity) jsonProjection() releaseIdentityJSON {
	return releaseIdentityJSON{
		Namespace:          r.namespace,
		Name:               r.name,
		Version:            r.version,
		ActivationRevision: r.activationRevision,
		SchemaVersion:      r.schemaVersion,
		Digest:             r.digest,
	}
}

// MarshalJSONTo streams the same identity-only projection as MarshalJSON.
func (r ReleaseIdentity) MarshalJSONTo(out *jsontext.Encoder) error {
	return json.MarshalEncode(out, r.jsonProjection())
}

// Status is a redacted point-in-time manager status. It contains no aliases,
// field paths, values, or secret metadata.
type Status struct {
	State                 string
	Ready                 bool
	Observed              ReleaseIdentity
	Applied               ReleaseIdentity
	DefaultDivergent      bool
	LastRejectionCategory RejectionCategory
	LastFailureAt         time.Time
	Reconnects            uint64
}

// Stats contains bounded counters suitable for metrics export.
type Stats struct {
	Candidates                uint64
	Applied                   uint64
	Rejected                  map[RejectionCategory]uint64
	Reconnects                uint64
	DefaultDivergent          bool
	AppliedReleaseVersion     uint64
	AppliedActivationRevision uint64
}

func cloneDifferences(in []FieldDifference) []FieldDifference {
	if in == nil {
		return nil
	}
	out := make([]FieldDifference, len(in))
	for i := range in {
		path := in[i].Path
		if !validDiagnosticPath(path) {
			path = "invalid_path"
		}
		out[i] = FieldDifference{
			Path:     path,
			Expected: cloneReportValue(in[i].Expected),
			Actual:   cloneReportValue(in[i].Actual),
		}
	}
	return out
}

func cloneChanges(in []FieldChange) []FieldChange {
	if in == nil {
		return nil
	}
	out := make([]FieldChange, len(in))
	for i := range in {
		path := in[i].Path
		if !validDiagnosticPath(path) {
			path = "invalid_path"
		}
		out[i] = FieldChange{
			Path:     path,
			Previous: cloneReportValue(in[i].Previous),
			Current:  cloneReportValue(in[i].Current),
		}
	}
	return out
}

func cloneReportValue(value any) any {
	if containsSecret(value) {
		return "[REDACTED]"
	}
	return Clone(value)
}
