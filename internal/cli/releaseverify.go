package cli

import (
	"fmt"
	"io"
	"text/tabwriter"

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
// usage errors.
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
	printVerifyDefaults(c.Stdout, checkSchema, resp)
	if verifyDefaultsClean(checkSchema, resp) {
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

func printVerifyDefaults(w io.Writer, checkSchema bool, resp *kmsv1.VerifyReleaseDefaultsResponse) {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "ALIAS\tVERDICT")
	for _, entry := range resp.GetEntries() {
		_, _ = fmt.Fprintf(tw, "%s\t%s\n", entry.GetAlias(), entry.GetVerdict())
	}
	_ = tw.Flush()
	schema := "not checked"
	if checkSchema {
		schema = "mismatch"
		if resp.GetSchemaMatches() {
			schema = "match"
		}
	}
	_, _ = fmt.Fprintf(w, "Release %s version %d (revision %d): %d match, %d differs, %d missing_in_release, %d unknown_alias, %d secret_alias, %d unsupported_content_type, %d unverified; schema %s\n",
		resp.GetName(), resp.GetVersion(), resp.GetActivationRevision(),
		resp.GetMatchCount(), resp.GetDiffersCount(), resp.GetMissingInReleaseCount(), resp.GetUnknownAliasCount(),
		resp.GetSecretAliasCount(), resp.GetUnsupportedContentTypeCount(), resp.GetUnverifiedCount(), schema)
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
