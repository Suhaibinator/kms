package cli

import (
	"fmt"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/sdk/go/configstore"
)

// cmdReleaseVerifyDefaults hashes every parameter in a generated defaults
// artifact locally and asks the server for value-free verdicts against the
// active release. No parameter value ever leaves the process: only aliases,
// content types, and canonical SHA-256 digests are sent.
//
// Exit status: 0 when every alias matches (and the schema matches when the
// artifact carries a schema digest), 1 on any non-match or RPC failure, 2 on
// usage errors. This command deliberately keeps the three-way status rather
// than the shared classified exit codes: callers branch on "verified" versus
// "not verified", and an RPC failure is simply "not verified".
func (c *CLI) cmdReleaseVerifyDefaults(args []string) int {
	fs := c.newFlags("release verify-defaults")
	cf := addConnFlags(c, fs)
	artifactPath := fs.String("artifact", "", "generated defaults artifact `file` ('-' for stdin)")
	releaseName := fs.String("release", "", "release `name` (default: the application's release name)")
	c.setUsage(fs, "release verify-defaults ENV/APP --artifact FILE|- [flags]",
		"Compare a generated defaults artifact with the active release by hash alone; no parameter value leaves the process.", false)
	if !c.parseFlags(fs, args) {
		return 2
	}
	pos := c.args()
	if len(pos) != 1 {
		return c.releaseUsageError("release verify-defaults requires ENV/APP")
	}
	if *artifactPath == "" {
		return c.releaseUsageError("release verify-defaults requires --artifact FILE|-")
	}
	ns, err := parseNamespaceProto(pos[0])
	if err != nil {
		return c.releaseUsageError("invalid namespace %q: %v", pos[0], err)
	}
	raw, err := c.readDefaultsArtifact(*artifactPath)
	if err != nil {
		return c.fail("reading defaults artifact: %v", err)
	}
	artifact, err := configstore.ParseDefaultsArtifact(raw)
	if err != nil {
		return c.fail("invalid defaults artifact: %v", err)
	}
	req, err := verifyDefaultsRequest(ns, *releaseName, artifact)
	if err != nil {
		return c.fail("release verify-defaults: %v", err)
	}

	conn, err := c.dialConn(cf)
	if err != nil {
		return c.fail("%v", err)
	}
	defer func() { _ = conn.Close() }()
	ctx, cancel := callContext()
	defer cancel()
	resp, err := kmsv1.NewConfigurationReleaseServiceClient(conn).VerifyReleaseDefaults(cf.authCtx(ctx), req)
	if err != nil {
		return c.fail("release verify-defaults: %v", err)
	}
	checkSchema := artifact.SchemaSHA256 != ""
	clean := verifyDefaultsClean(checkSchema, resp)
	if c.jsonOutput() {
		if code := c.printJSON(verifyDefaultsJSONOf(checkSchema, clean, resp)); code != exitOK {
			return code
		}
	} else {
		c.printVerifyDefaults(checkSchema, resp)
	}
	if clean {
		return 0
	}
	return 1
}

func (c *CLI) releaseUsageError(format string, args ...any) int {
	_, _ = fmt.Fprintf(c.Stderr, "error: "+format+"\n\n", args...)
	c.releaseUsage()
	return 2
}

// verifyDefaultsRequest builds the wire request from a parsed artifact. Each
// parameter is hashed with the shared canonical rule (sorted-key compact JSON
// for the json content type, exact bytes otherwise) so the digest matches what
// the server computes for the pinned value.
func verifyDefaultsRequest(ns *kmsv1.NamespaceRef, releaseName string, artifact configstore.DefaultsArtifact) (*kmsv1.VerifyReleaseDefaultsRequest, error) {
	entries := make([]*kmsv1.VerifyEntry, 0, len(artifact.Parameters))
	for _, parameter := range artifact.Parameters {
		sum, err := configstore.ParameterHash(parameter.ContentType, []byte(parameter.Value))
		if err != nil {
			return nil, fmt.Errorf("hashing parameter %q: %w", parameter.Alias, err)
		}
		entries = append(entries, &kmsv1.VerifyEntry{Alias: parameter.Alias, ContentType: parameter.ContentType, Sha256: sum})
	}
	return &kmsv1.VerifyReleaseDefaultsRequest{
		Namespace: ns, Name: releaseName, Profile: artifact.Profile,
		SchemaSha256: artifact.SchemaSHA256, Entries: entries,
	}, nil
}

// verifyDefaultsSchemaText is the schema verdict both renderers print: an
// artifact without a schema digest asked for no schema comparison at all.
func verifyDefaultsSchemaText(checkSchema bool, resp *kmsv1.VerifyReleaseDefaultsResponse) string {
	if !checkSchema {
		return "not checked"
	}
	if resp.GetSchemaMatches() {
		return "match"
	}
	return "mismatch"
}

// releaseVerifyEntryJSON is one alias verdict; values are never part of it.
type releaseVerifyEntryJSON struct {
	Alias   string `json:"alias"`
	Verdict string `json:"verdict"`
}

// releaseVerifyCountsJSON is the summary line the table prints as prose.
type releaseVerifyCountsJSON struct {
	Match                  uint32 `json:"match"`
	Differs                uint32 `json:"differs"`
	MissingInRelease       uint32 `json:"missing_in_release"`
	UnknownAlias           uint32 `json:"unknown_alias"`
	SecretAlias            uint32 `json:"secret_alias"`
	UnsupportedContentType uint32 `json:"unsupported_content_type"`
	Unverified             uint32 `json:"unverified"`
}

// releaseVerifyDefaultsJSON is the machine-readable mismatch report. clean
// mirrors the exit status (0 when true, 1 when false) so a caller that already
// parsed the document does not have to re-derive the verdict.
type releaseVerifyDefaultsJSON struct {
	Name               string                   `json:"name"`
	Version            uint64                   `json:"version"`
	ActivationRevision uint64                   `json:"activation_revision"`
	Schema             string                   `json:"schema"`
	Clean              bool                     `json:"clean"`
	Entries            []releaseVerifyEntryJSON `json:"entries"`
	Counts             releaseVerifyCountsJSON  `json:"counts"`
}

func verifyDefaultsJSONOf(checkSchema, clean bool, resp *kmsv1.VerifyReleaseDefaultsResponse) releaseVerifyDefaultsJSON {
	entries := make([]releaseVerifyEntryJSON, 0, len(resp.GetEntries()))
	for _, entry := range resp.GetEntries() {
		entries = append(entries, releaseVerifyEntryJSON{Alias: entry.GetAlias(), Verdict: entry.GetVerdict()})
	}
	return releaseVerifyDefaultsJSON{
		Name:               resp.GetName(),
		Version:            resp.GetVersion(),
		ActivationRevision: resp.GetActivationRevision(),
		Schema:             verifyDefaultsSchemaText(checkSchema, resp),
		Clean:              clean,
		Entries:            entries,
		Counts: releaseVerifyCountsJSON{
			Match:                  resp.GetMatchCount(),
			Differs:                resp.GetDiffersCount(),
			MissingInRelease:       resp.GetMissingInReleaseCount(),
			UnknownAlias:           resp.GetUnknownAliasCount(),
			SecretAlias:            resp.GetSecretAliasCount(),
			UnsupportedContentType: resp.GetUnsupportedContentTypeCount(),
			Unverified:             resp.GetUnverifiedCount(),
		},
	}
}

func (c *CLI) printVerifyDefaults(checkSchema bool, resp *kmsv1.VerifyReleaseDefaultsResponse) {
	rows := make([][]string, 0, len(resp.GetEntries()))
	for _, entry := range resp.GetEntries() {
		rows = append(rows, []string{entry.GetAlias(), entry.GetVerdict()})
	}
	c.printTable([]string{"ALIAS", "VERDICT"}, rows)
	_, _ = fmt.Fprintf(c.Stdout, "Release %s version %d (revision %d): %d match, %d differs, %d missing_in_release, %d unknown_alias, %d secret_alias, %d unsupported_content_type, %d unverified; schema %s\n",
		resp.GetName(), resp.GetVersion(), resp.GetActivationRevision(),
		resp.GetMatchCount(), resp.GetDiffersCount(), resp.GetMissingInReleaseCount(), resp.GetUnknownAliasCount(),
		resp.GetSecretAliasCount(), resp.GetUnsupportedContentTypeCount(), resp.GetUnverifiedCount(), verifyDefaultsSchemaText(checkSchema, resp))
}

// verifyDefaultsClean reports whether every requested alias matched and, when
// the artifact carried a schema digest, the schema matched too. Unverified
// aliases (pinned by the release but absent from the artifact) are reported
// but do not fail the check: the artifact is the caller's view of its own
// defaults, not a claim about the whole release.
func verifyDefaultsClean(checkSchema bool, resp *kmsv1.VerifyReleaseDefaultsResponse) bool {
	if checkSchema && !resp.GetSchemaMatches() {
		return false
	}
	for _, entry := range resp.GetEntries() {
		if entry.GetVerdict() != domain.VerifyVerdictMatch {
			return false
		}
	}
	nonMatch := resp.GetDiffersCount() + resp.GetMissingInReleaseCount() + resp.GetUnknownAliasCount() + resp.GetSecretAliasCount() + resp.GetUnsupportedContentTypeCount()
	return nonMatch == 0
}
