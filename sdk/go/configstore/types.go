package configstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/Suhaibinator/kms/sdk/go/paramstore"
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

// Options configures a managed configuration manager.
type Options struct {
	Release              string
	Contract             []ContractEntry
	AllowDefaultMismatch bool
	OnDefaultMismatch    func(DefaultMismatchReport)
	SecretTokenProvider  paramstore.SecretTokenProvider
	ReconcileInterval    time.Duration
	MaxConcurrentFetches int
	InstanceID           string
}

// PrepareFunc constructs a complete immutable candidate away from the active
// generation. Publish must perform the generated binding's atomic pointer
// swap; Abort releases any candidate-owned resources.
type PrepareFunc func(context.Context, paramstore.ReleaseSnapshot) (PreparedCandidate, error)

// PreparedCandidate is generated, validated state ready for policy admission.
// DefaultDifferences must contain non-secret canonical fields only.
type PreparedCandidate struct {
	Publish               func()
	Abort                 func()
	DefaultDifferences    []FieldDifference
	RestartRequiredFields []string
}

// MismatchPhase identifies when code defaults were compared with a candidate.
type MismatchPhase string

const (
	MismatchStartup MismatchPhase = "startup"
	MismatchRuntime MismatchPhase = "runtime"
)

// MismatchSeverity describes the required handling of a default mismatch.
type MismatchSeverity string

const (
	MismatchFatal MismatchSeverity = "fatal"
	MismatchError MismatchSeverity = "error"
)

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
	return json.Marshal(struct {
		Phase       MismatchPhase     `json:"phase"`
		Severity    MismatchSeverity  `json:"severity"`
		Release     ReleaseIdentity   `json:"release"`
		Differences []FieldDifference `json:"differences"`
	}{
		Phase:       r.phase,
		Severity:    r.severity,
		Release:     r.release,
		Differences: r.Fields(),
	})
}

// DefaultMismatchError is returned when initial code defaults and KMS values
// differ and startup admission was not explicitly bypassed.
type DefaultMismatchError struct {
	report *defaultMismatchReport
}

func newDefaultMismatchError(report *defaultMismatchReport) *DefaultMismatchError {
	return &DefaultMismatchError{report: report}
}

func (e *DefaultMismatchError) Error() string {
	if e == nil || e.report == nil {
		return "configstore: startup default mismatch"
	}
	return e.report.String()
}

func (e *DefaultMismatchError) String() string   { return e.Error() }
func (e *DefaultMismatchError) GoString() string { return e.Error() }

// Format prevents detailed reflection of the private report for %+v and %#v.
func (e *DefaultMismatchError) Format(f fmt.State, verb rune) {
	if verb == 'q' {
		_, _ = fmt.Fprintf(f, "%q", e.Error())
		return
	}
	_, _ = io.WriteString(f, e.Error())
}

func (e *DefaultMismatchError) Phase() MismatchPhase {
	if e == nil || e.report == nil {
		return MismatchStartup
	}
	return e.report.Phase()
}

func (e *DefaultMismatchError) Severity() MismatchSeverity {
	if e == nil || e.report == nil {
		return MismatchFatal
	}
	return e.report.Severity()
}

func (e *DefaultMismatchError) Release() ReleaseIdentity {
	if e == nil || e.report == nil {
		return ReleaseIdentity{}
	}
	return e.report.Release()
}

func (e *DefaultMismatchError) Fields() []FieldDifference {
	if e == nil || e.report == nil {
		return nil
	}
	return e.report.Fields()
}

// Report returns the same immutable report that was delivered to the startup
// callback.
func (e *DefaultMismatchError) Report() DefaultMismatchReport {
	if e == nil {
		return nil
	}
	return e.report
}

func (e *DefaultMismatchError) MarshalJSON() ([]byte, error) {
	if e == nil || e.report == nil {
		return []byte("null"), nil
	}
	return e.report.MarshalJSON()
}

// RejectionCategory is a bounded release acknowledgement and metrics label.
type RejectionCategory string

const (
	RejectConfigContractMismatch RejectionCategory = "config_contract_mismatch"
	RejectConfigDecodeFailed     RejectionCategory = "config_decode_failed"
	RejectConfigValidationFailed RejectionCategory = "config_validation_failed"
	RejectDefaultMismatch        RejectionCategory = "default_mismatch"
	RejectRestartRequired        RejectionCategory = "restart_required"
	RejectInternal               RejectionCategory = "internal"
)

// CandidateError classifies a candidate without exposing its underlying
// diagnostic through ordinary formatting. Unwrap remains available for
// intentional local inspection and errors.As/errors.Is.
type CandidateError struct {
	category RejectionCategory
	cause    error
}

// Reject returns a redacting classified candidate error.
func Reject(category RejectionCategory, cause error) error {
	if !validRejectionCategory(category) {
		category = RejectInternal
	}
	if cause == nil {
		cause = errors.New("configstore: candidate rejected")
	}
	return &CandidateError{category: category, cause: cause}
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

// ReleaseRejectionCategory is consumed by paramstore.ReleaseLoader without
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
	return json.Marshal(struct {
		Category RejectionCategory `json:"category"`
	}{Category: e.category})
}

// ReleaseIdentity contains immutable, non-sensitive identity copied from a
// resolved candidate. It never retains the snapshot or any resolved value.
type ReleaseIdentity struct {
	namespace          string
	name               string
	version            uint64
	activationRevision uint64
	schemaID           string
	schemaVersion      uint64
	digest             string
}

// ReleaseIdentityFromSnapshot copies safe identity fields from snapshot.
func ReleaseIdentityFromSnapshot(snapshot paramstore.ReleaseSnapshot) ReleaseIdentity {
	return ReleaseIdentity{
		namespace:          snapshot.Namespace(),
		name:               snapshot.Name(),
		version:            snapshot.Version(),
		activationRevision: snapshot.ActivationRevision(),
		schemaID:           snapshot.SchemaID(),
		schemaVersion:      snapshot.SchemaVersion(),
		digest:             snapshot.Digest(),
	}
}

func (r ReleaseIdentity) Namespace() string          { return r.namespace }
func (r ReleaseIdentity) Name() string               { return r.name }
func (r ReleaseIdentity) Version() uint64            { return r.version }
func (r ReleaseIdentity) ActivationRevision() uint64 { return r.activationRevision }
func (r ReleaseIdentity) SchemaID() string           { return r.schemaID }
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
	return json.Marshal(struct {
		Namespace          string `json:"namespace"`
		Name               string `json:"name"`
		Version            uint64 `json:"version"`
		ActivationRevision uint64 `json:"activation_revision"`
		SchemaID           string `json:"schema_id,omitempty"`
		SchemaVersion      uint64 `json:"schema_version,omitempty"`
		Digest             string `json:"digest"`
	}{
		Namespace:          r.namespace,
		Name:               r.name,
		Version:            r.version,
		ActivationRevision: r.activationRevision,
		SchemaID:           r.schemaID,
		SchemaVersion:      r.schemaVersion,
		Digest:             r.digest,
	})
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
		out[i] = FieldDifference{
			Path:     in[i].Path,
			Expected: cloneReportValue(in[i].Expected),
			Actual:   cloneReportValue(in[i].Actual),
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
