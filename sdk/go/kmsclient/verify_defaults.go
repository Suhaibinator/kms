package kmsclient

import (
	"context"
	"fmt"
	"strings"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
)

// Bounded verdicts returned by VerifyReleaseDefaults for one alias.
const (
	VerifyVerdictMatch                  = "match"
	VerifyVerdictDiffers                = "differs"
	VerifyVerdictMissingInRelease       = "missing_in_release"
	VerifyVerdictUnknownAlias           = "unknown_alias"
	VerifyVerdictSecretAlias            = "secret_alias"
	VerifyVerdictUnsupportedContentType = "unsupported_content_type"
)

// VerifyDefaultsEntry is one parameter alias with the lowercase hex SHA-256 of
// its canonical encoded value (configstore.ParameterHash). Secret aliases must
// not be sent; the server reports them as secret_alias without reading them.
type VerifyDefaultsEntry struct {
	Alias       string
	ContentType string
	SHA256      string
}

// VerifyReleaseDefaultsOptions describes one value-free comparison of
// source-owned defaults against the active release of Namespace.
type VerifyReleaseDefaultsOptions struct {
	// Namespace is the "env/app" whose active release is compared. It is
	// required: verification identities are typically unbound.
	Namespace string
	// Release is the release name; empty selects the application's configured
	// release name.
	Release string
	// Profile is an informational label carried with the request.
	Profile string
	// SchemaSHA256 is the generated contract's schema digest; empty skips the
	// schema check and leaves SchemaMatches false.
	SchemaSHA256 string
	Entries      []VerifyDefaultsEntry
}

// VerifyDefaultsVerdict is the server's bounded verdict for one alias.
type VerifyDefaultsVerdict struct {
	Alias   string
	Verdict string
}

// VerifyReleaseDefaultsResult is the validated, value-free server response.
type VerifyReleaseDefaultsResult struct {
	ReleaseName        string
	ReleaseVersion     uint64
	ActivationRevision uint64
	SchemaMatches      bool
	Entries            []VerifyDefaultsVerdict
	MatchCount         int
	DiffersCount       int
	MissingCount       int
	UnknownAliasCount  int
	SecretAliasCount   int
	UnsupportedCount   int
	// UnverifiedCount is the number of parameter aliases the release pins that
	// the request did not mention.
	UnverifiedCount int
}

// Passed reports whether the schema matched and every entry matched.
func (r VerifyReleaseDefaultsResult) Passed() bool {
	if !r.SchemaMatches {
		return false
	}
	for _, entry := range r.Entries {
		if entry.Verdict != VerifyVerdictMatch {
			return false
		}
	}
	return true
}

// VerifyReleaseDefaults asks the server which of the supplied alias hashes
// differ from the parameters pinned by the active release. Neither direction
// carries values. The call requires the configuration-release:verify-defaults
// operation and is rate limited per identity; ErrRateLimited is returned when
// a budget is exhausted.
func (c *Client) VerifyReleaseDefaults(
	ctx context.Context,
	options VerifyReleaseDefaultsOptions,
) (VerifyReleaseDefaultsResult, error) {
	namespace, err := parseNamespace(options.Namespace)
	if err != nil {
		return VerifyReleaseDefaultsResult{}, err
	}
	entries := make([]*kmsv1.VerifyEntry, 0, len(options.Entries))
	seen := make(map[string]struct{}, len(options.Entries))
	for index, entry := range options.Entries {
		alias := strings.TrimSpace(entry.Alias)
		if alias == "" {
			return VerifyReleaseDefaultsResult{}, fmt.Errorf("kmsclient: verify entry %d has an empty alias", index)
		}
		if _, duplicate := seen[alias]; duplicate {
			return VerifyReleaseDefaultsResult{}, fmt.Errorf("kmsclient: verify entry %q is duplicated", alias)
		}
		seen[alias] = struct{}{}
		if !validLowerHex64(entry.SHA256) {
			return VerifyReleaseDefaultsResult{}, fmt.Errorf("kmsclient: verify entry %q has an invalid sha256", alias)
		}
		entries = append(entries, &kmsv1.VerifyEntry{Alias: alias, ContentType: entry.ContentType, Sha256: entry.SHA256})
	}
	if options.SchemaSHA256 != "" && !validLowerHex64(options.SchemaSHA256) {
		return VerifyReleaseDefaultsResult{}, fmt.Errorf("kmsclient: invalid schema sha256")
	}
	cctx, cancel := c.callCtx(ctx, "")
	defer cancel()
	response, err := c.releases.VerifyReleaseDefaults(cctx, &kmsv1.VerifyReleaseDefaultsRequest{
		Namespace:    namespace.proto(),
		Name:         options.Release,
		Profile:      options.Profile,
		SchemaSha256: options.SchemaSHA256,
		Entries:      entries,
	})
	if err != nil {
		return VerifyReleaseDefaultsResult{}, mapError(err)
	}
	if response == nil {
		return VerifyReleaseDefaultsResult{}, fmt.Errorf("kmsclient: empty verify response")
	}
	result := VerifyReleaseDefaultsResult{
		ReleaseName:        response.GetName(),
		ReleaseVersion:     response.GetVersion(),
		ActivationRevision: response.GetActivationRevision(),
		SchemaMatches:      response.GetSchemaMatches(),
		Entries:            make([]VerifyDefaultsVerdict, 0, len(response.GetEntries())),
		MatchCount:         int(response.GetMatchCount()),
		DiffersCount:       int(response.GetDiffersCount()),
		MissingCount:       int(response.GetMissingInReleaseCount()),
		UnknownAliasCount:  int(response.GetUnknownAliasCount()),
		SecretAliasCount:   int(response.GetSecretAliasCount()),
		UnsupportedCount:   int(response.GetUnsupportedContentTypeCount()),
		UnverifiedCount:    int(response.GetUnverifiedCount()),
	}
	if len(response.GetEntries()) != len(entries) {
		return VerifyReleaseDefaultsResult{}, fmt.Errorf("kmsclient: verify response has %d verdicts for %d entries", len(response.GetEntries()), len(entries))
	}
	answered := make(map[string]struct{}, len(entries))
	for index, verdict := range response.GetEntries() {
		if verdict == nil {
			return VerifyReleaseDefaultsResult{}, fmt.Errorf("kmsclient: verify response entry %d is empty", index)
		}
		if _, known := seen[verdict.GetAlias()]; !known {
			return VerifyReleaseDefaultsResult{}, fmt.Errorf("kmsclient: verify response names unknown alias %q", verdict.GetAlias())
		}
		if _, duplicate := answered[verdict.GetAlias()]; duplicate {
			return VerifyReleaseDefaultsResult{}, fmt.Errorf("kmsclient: verify response repeats alias %q", verdict.GetAlias())
		}
		answered[verdict.GetAlias()] = struct{}{}
		switch verdict.GetVerdict() {
		case VerifyVerdictMatch, VerifyVerdictDiffers, VerifyVerdictMissingInRelease,
			VerifyVerdictUnknownAlias, VerifyVerdictSecretAlias, VerifyVerdictUnsupportedContentType:
		default:
			return VerifyReleaseDefaultsResult{}, fmt.Errorf("kmsclient: verify response entry %q has invalid verdict", verdict.GetAlias())
		}
		result.Entries = append(result.Entries, VerifyDefaultsVerdict{Alias: verdict.GetAlias(), Verdict: verdict.GetVerdict()})
	}
	return result, nil
}

func validLowerHex64(value string) bool {
	if len(value) != 64 {
		return false
	}
	for i := 0; i < len(value); i++ {
		character := value[i]
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}
