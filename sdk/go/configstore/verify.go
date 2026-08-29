package configstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/Suhaibinator/kms/sdk/go/kmsclient"
)

// VerifyInput is what a generated binding knows about its source-owned
// defaults: the contract, the schema digest, and the canonical non-secret
// parameter group documents keyed by alias (EncodeParameterGroups).
type VerifyInput struct {
	SchemaSHA256 string
	Contract     []ContractEntry
	Groups       map[string]json.RawMessage
}

// VerifyOptions addresses the release to compare against.
type VerifyOptions struct {
	// Namespace is the "env/app" whose active release is compared. Required.
	Namespace string
	// Release is the release name; empty selects the application's configured
	// release name on the server.
	Release string
	// Profile is an informational label sent with the request.
	Profile string
}

// VerifyEntryResult is the verdict for one parameter alias.
type VerifyEntryResult struct {
	Alias       string
	ContentType string
	Verdict     string
}

// VerifyResult is the value-free outcome of VerifyDefaults.
type VerifyResult struct {
	Namespace          string
	ReleaseName        string
	ReleaseVersion     uint64
	ActivationRevision uint64
	// SchemaMatches is true when the server's pinned application schema digest
	// equals the generated contract's schema digest.
	SchemaMatches bool
	Entries       []VerifyEntryResult
	// Unverified counts parameter aliases pinned by the release that the
	// contract did not mention.
	Unverified int
}

// Passed reports whether the schema matched and every alias matched.
func (r VerifyResult) Passed() bool {
	if !r.SchemaMatches {
		return false
	}
	for _, entry := range r.Entries {
		if entry.Verdict != kmsclient.VerifyVerdictMatch {
			return false
		}
	}
	return true
}

// Failures returns the entries whose verdict is not match, sorted by alias.
func (r VerifyResult) Failures() []VerifyEntryResult {
	failures := make([]VerifyEntryResult, 0)
	for _, entry := range r.Entries {
		if entry.Verdict != kmsclient.VerifyVerdictMatch {
			failures = append(failures, entry)
		}
	}
	sort.Slice(failures, func(i, j int) bool { return failures[i].Alias < failures[j].Alias })
	return failures
}

// Report renders a human-readable, value-free summary suitable for CI logs.
func (r VerifyResult) Report() string {
	var out strings.Builder
	schema := "differs"
	if r.SchemaMatches {
		schema = "match"
	}
	fmt.Fprintf(&out, "%s %s@%d#%d  schema: %s\n", r.Namespace, r.ReleaseName, r.ReleaseVersion, r.ActivationRevision, schema)
	entries := append([]VerifyEntryResult(nil), r.Entries...)
	sort.Slice(entries, func(i, j int) bool { return entries[i].Alias < entries[j].Alias })
	table := tabwriter.NewWriter(&out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(table, "VERDICT\tALIAS\tCONTENT_TYPE")
	counts := map[string]int{}
	for _, entry := range entries {
		counts[entry.Verdict]++
		fmt.Fprintf(table, "%s\t%s\t%s\n", entry.Verdict, entry.Alias, entry.ContentType)
	}
	_ = table.Flush()
	fmt.Fprintf(&out, "summary: match=%d differs=%d missing_in_release=%d unknown_alias=%d secret_alias=%d unsupported_content_type=%d unverified=%d\n",
		counts[kmsclient.VerifyVerdictMatch], counts[kmsclient.VerifyVerdictDiffers], counts[kmsclient.VerifyVerdictMissingInRelease],
		counts[kmsclient.VerifyVerdictUnknownAlias], counts[kmsclient.VerifyVerdictSecretAlias], counts[kmsclient.VerifyVerdictUnsupportedContentType], r.Unverified)
	if r.Passed() {
		out.WriteString("result: active release matches source defaults\n")
	} else {
		out.WriteString("result: active release differs from source defaults\n")
	}
	return out.String()
}

// VerifyClient is the subset of *kmsclient.Client used by VerifyDefaults.
type VerifyClient interface {
	VerifyReleaseDefaults(context.Context, kmsclient.VerifyReleaseDefaultsOptions) (kmsclient.VerifyReleaseDefaultsResult, error)
}

// VerifyDefaults hashes every parameter group in in.Groups with ParameterHash
// and asks the server which aliases of the active release differ. Secret
// contract entries are never sent. The returned result carries verdicts only.
func VerifyDefaults(ctx context.Context, client VerifyClient, in VerifyInput, opts VerifyOptions) (VerifyResult, error) {
	if client == nil {
		return VerifyResult{}, errors.New("configstore: verify requires a client")
	}
	if strings.TrimSpace(opts.Namespace) == "" {
		return VerifyResult{}, errors.New("configstore: verify requires VerifyOptions.Namespace")
	}
	entries := make([]kmsclient.VerifyDefaultsEntry, 0, len(in.Contract))
	contentTypes := make(map[string]string, len(in.Contract))
	for _, entry := range in.Contract {
		if entry.Kind != ContractKindParameter {
			continue
		}
		document, ok := in.Groups[entry.Alias]
		if !ok {
			return VerifyResult{}, fmt.Errorf("configstore: verify: missing encoded parameter group %s", entry.Alias)
		}
		hash, err := ParameterHash(entry.ContentType, document)
		if err != nil {
			return VerifyResult{}, fmt.Errorf("configstore: verify: hash parameter group %s: %w", entry.Alias, err)
		}
		alias := strings.TrimSpace(entry.Alias)
		entries = append(entries, kmsclient.VerifyDefaultsEntry{Alias: alias, ContentType: entry.ContentType, SHA256: hash})
		contentTypes[alias] = entry.ContentType
	}
	response, err := client.VerifyReleaseDefaults(ctx, kmsclient.VerifyReleaseDefaultsOptions{
		Namespace:    opts.Namespace,
		Release:      opts.Release,
		Profile:      opts.Profile,
		SchemaSHA256: in.SchemaSHA256,
		Entries:      entries,
	})
	if err != nil {
		if errors.Is(err, kmsclient.ErrRateLimited) {
			return VerifyResult{}, fmt.Errorf("%w (the per-identity verify budget is spent; wait for the window to reset instead of retrying)", err)
		}
		return VerifyResult{}, err
	}
	result := VerifyResult{
		Namespace:          opts.Namespace,
		ReleaseName:        response.ReleaseName,
		ReleaseVersion:     response.ReleaseVersion,
		ActivationRevision: response.ActivationRevision,
		SchemaMatches:      response.SchemaMatches,
		Entries:            make([]VerifyEntryResult, 0, len(response.Entries)),
		Unverified:         response.UnverifiedCount,
	}
	for _, verdict := range response.Entries {
		result.Entries = append(result.Entries, VerifyEntryResult{Alias: verdict.Alias, ContentType: contentTypes[verdict.Alias], Verdict: verdict.Verdict})
	}
	return result, nil
}
