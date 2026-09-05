package cli

import (
	"crypto/rand"
	"encoding/base64"
	"flag"
	"fmt"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/term"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/keyutil"
)

const (
	bindingKeyEnv    = "KMS_BINDING_KEY"
	newBindingKeyEnv = "KMS_NEW_BINDING_KEY"
	minBindingKeyLen = 32
)

// cmdBindingKey owns operator binding-key lifecycle commands. Keys are read
// only from an exact environment variable or a non-echoing terminal prompt.
func (c *CLI) cmdBindingKey(args []string) int {
	if len(args) == 0 {
		c.bindingKeyUsage()
		return exitUsage
	}
	switch args[0] {
	case "generate":
		return c.cmdBindingKeyGenerate(args[1:])
	case "rotate":
		return c.cmdBindingKeyRotate(args[1:])
	case "help", "-h", "--help":
		c.bindingKeyUsage()
		return exitOK
	default:
		return c.failUsage("unknown binding-key command %q", args[0])
	}
}

func (c *CLI) bindingKeyUsage() {
	_, _ = fmt.Fprint(c.Stderr, `Usage: parameter-store binding-key <command> [flags]

Commands:
  generate                  Write one new 256-bit Base64URL binding key to stdout.
  rotate PATH               Create a new current version protected by a new binding key.

Binding keys come from KMS_BINDING_KEY and, for rotation, KMS_NEW_BINDING_KEY.
When a required variable is absent, an interactive terminal is prompted without echo.
`)
}

func (c *CLI) cmdBindingKeyGenerate(args []string) int {
	fs := c.newFlags("binding-key generate")
	c.setUsage(fs, "binding-key generate", "Write exactly one new 256-bit Base64URL binding key to stdout.", false)
	if !c.parseFlags(fs, args) {
		return exitUsage
	}
	if !c.rejectPositionals() {
		return exitUsage
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return c.fail("generating binding key: %v", err)
	}
	encoded := base64.RawURLEncoding.EncodeToString(key)
	clear(key)
	_, err := fmt.Fprintln(c.Stdout, encoded)
	if err != nil {
		return c.fail("writing binding key: %v", err)
	}
	return exitOK
}

func (c *CLI) cmdSecret(args []string) int {
	if len(args) == 0 {
		c.secretUsage()
		return exitUsage
	}
	switch args[0] {
	case "bind":
		return c.cmdSecretBind(args[1:])
	case "unbind":
		return c.cmdSecretUnbind(args[1:])
	case "purge-binding-cohort":
		return c.cmdSecretPurgeBindingCohort(args[1:])
	case "purge-unbound-versions":
		return c.cmdSecretPurgeUnboundVersions(args[1:])
	case "help", "-h", "--help":
		c.secretUsage()
		return exitOK
	default:
		return c.failUsage("unknown secret command %q", args[0])
	}
}

func (c *CLI) secretUsage() {
	_, _ = fmt.Fprint(c.Stderr, `Usage: parameter-store secret <command> PATH [flags]

Commands:
  bind PATH                   Clone current into a new bound current version.
  unbind PATH                 Clone current into a new unbound current version.
  purge-binding-cohort PATH   Irreversibly purge a compromised contiguous cohort (admin only).
  purge-unbound-versions PATH Irreversibly purge every live unbound version (admin only).

Binding operations take their key from KMS_BINDING_KEY. A required key is prompted without
echo on an interactive terminal; purging unbound versions requires no binding key.
`)
}

func (c *CLI) cmdSecretBind(args []string) int {
	fs := c.newFlags("secret bind")
	cf := addConnFlags(c, fs)
	expectedCurrent := addExpectedCurrentVersionFlag(fs)
	c.setUsage(fs, "secret bind /env/app/key [flags]", "Create a new current version with binding-key protection.", false)
	if !c.parseFlags(fs, args) {
		return exitUsage
	}
	if !c.validateExpectedCurrentVersion("secret bind", expectedCurrent) {
		return exitUsage
	}
	ref, ok := c.bindingCommandRef("secret bind")
	if !ok {
		return exitUsage
	}
	conn, err := c.dialConn(cf)
	if err != nil {
		return c.failErr("", err)
	}
	defer func() { _ = conn.Close() }()
	client := kmsv1.NewSecretServiceClient(conn)
	current, code := c.resolveExpectedCurrentVersion(client, cf, ref, "secret bind", expectedCurrent)
	if code != exitOK {
		return code
	}
	bindingKey, err := c.requiredBindingKey(bindingKeyEnv, "New binding key for "+ref.String()+": ", true)
	if err != nil {
		return c.failUsage("secret bind: %v", err)
	}
	ctx, cancel := callContext()
	defer cancel()
	resp, err := client.BindSecret(cf.authCtx(ctx), &kmsv1.BindSecretRequest{
		Ref: protoRef(ref), ExpectedCurrentVersion: current, BindingKey: bindingKey,
	})
	if err != nil {
		return c.failSecretRPC("secret bind", err)
	}
	return c.printSecretTransition("Bound", ref.String(), resp)
}

func (c *CLI) cmdSecretUnbind(args []string) int {
	fs := c.newFlags("secret unbind")
	cf := addConnFlags(c, fs)
	expectedCurrent := addExpectedCurrentVersionFlag(fs)
	c.setUsage(fs, "secret unbind /env/app/key [flags]", "Create a new current version without binding-key protection.", false)
	if !c.parseFlags(fs, args) {
		return exitUsage
	}
	if !c.validateExpectedCurrentVersion("secret unbind", expectedCurrent) {
		return exitUsage
	}
	ref, ok := c.bindingCommandRef("secret unbind")
	if !ok {
		return exitUsage
	}
	conn, err := c.dialConn(cf)
	if err != nil {
		return c.failErr("", err)
	}
	defer func() { _ = conn.Close() }()
	client := kmsv1.NewSecretServiceClient(conn)
	current, code := c.resolveExpectedCurrentVersion(client, cf, ref, "secret unbind", expectedCurrent)
	if code != exitOK {
		return code
	}
	bindingKey, err := c.requiredBindingKey(bindingKeyEnv, "Binding key for "+ref.String()+": ", false)
	if err != nil {
		return c.failUsage("secret unbind: %v", err)
	}
	ctx, cancel := callContext()
	defer cancel()
	resp, err := client.UnbindSecret(cf.authCtx(ctx), &kmsv1.UnbindSecretRequest{
		Ref: protoRef(ref), ExpectedCurrentVersion: current, BindingKey: bindingKey,
	})
	if err != nil {
		return c.failSecretRPC("secret unbind", err)
	}
	return c.printSecretTransition("Unbound", ref.String(), resp)
}

func (c *CLI) cmdBindingKeyRotate(args []string) int {
	fs := c.newFlags("binding-key rotate")
	cf := addConnFlags(c, fs)
	expectedCurrent := addExpectedCurrentVersionFlag(fs)
	c.setUsage(fs, "binding-key rotate /env/app/key [flags]", "Create a new current version protected by a replacement binding key.", false)
	if !c.parseFlags(fs, args) {
		return exitUsage
	}
	if !c.validateExpectedCurrentVersion("binding-key rotate", expectedCurrent) {
		return exitUsage
	}
	ref, ok := c.bindingCommandRef("binding-key rotate")
	if !ok {
		return exitUsage
	}
	oldKey, err := c.requiredBindingKey(bindingKeyEnv, "Current binding key for "+ref.String()+": ", false)
	if err != nil {
		return c.failUsage("binding-key rotate: %v", err)
	}
	conn, err := c.dialConn(cf)
	if err != nil {
		return c.failErr("", err)
	}
	defer func() { _ = conn.Close() }()
	client := kmsv1.NewSecretServiceClient(conn)
	current, code := c.resolveExpectedCurrentVersion(client, cf, ref, "binding-key rotate", expectedCurrent)
	if code != exitOK {
		return code
	}
	newKey, err := c.requiredBindingKey(newBindingKeyEnv, "New binding key for "+ref.String()+": ", true)
	if err != nil {
		return c.failUsage("binding-key rotate: %v", err)
	}
	mutationCtx, cancelMutation := callContext()
	defer cancelMutation()
	resp, err := client.RotateSecretBindingKey(cf.authCtx(mutationCtx), &kmsv1.RotateSecretBindingKeyRequest{
		Ref:                    protoRef(ref),
		ExpectedCurrentVersion: current,
		BindingKey:             oldKey,
		NewBindingKey:          newKey,
	})
	if err != nil {
		return c.failSecretRPC("binding-key rotate", err)
	}
	return c.printSecretTransition("Rotated binding key for", ref.String(), resp)
}

func (c *CLI) cmdSecretPurgeBindingCohort(args []string) int {
	fs := c.newFlags("secret purge-binding-cohort")
	cf := addConnFlags(c, fs)
	version := fs.Uint64("version", 0, "anchor secret `version` (0 = current label)")
	c.setUsage(fs, "secret purge-binding-cohort /env/app/key [flags]", "Preview and irreversibly purge one compromised binding-key cohort. Administrator authentication is required.", false)
	if !c.parseFlags(fs, args) {
		return exitUsage
	}
	ref, ok := c.bindingCommandRef("secret purge-binding-cohort")
	if !ok {
		return exitUsage
	}
	bindingKey, err := c.requiredBindingKey(bindingKeyEnv, "Compromised binding key for "+ref.String()+": ", false)
	if err != nil {
		return c.failUsage("secret purge-binding-cohort: %v", err)
	}
	conn, err := c.dialConn(cf)
	if err != nil {
		return c.failErr("", err)
	}
	defer func() { _ = conn.Close() }()
	client := kmsv1.NewSecretServiceClient(conn)
	previewCtx, cancelPreview := callContext()
	preview, err := client.PreviewSecretBindingCohort(cf.authCtx(previewCtx), &kmsv1.PreviewSecretBindingCohortRequest{
		Ref: protoRef(ref), AnchorVersion: *version, BindingKey: bindingKey,
	})
	cancelPreview()
	if err != nil {
		return c.failSecretRPC("secret purge-binding-cohort preview", err)
	}
	if err := validateCohortPreview(preview); err != nil {
		return c.fail("secret purge-binding-cohort preview: %v", err)
	}
	c.printCohortPreview("Binding-cohort purge", ref.String(), preview)
	_, _ = fmt.Fprintf(c.Stderr, "IRREVERSIBLE ADMIN OPERATION: versions %s will be permanently destroyed, even if immutable releases reference them. This cannot be undone.\n", formatVersions(preview.GetAffectedVersions()))
	ok, code := c.confirmYesNo(fmt.Sprintf("permanently purge %s versions %s", ref, formatVersions(preview.GetAffectedVersions())))
	if !ok {
		return code
	}
	affected := append([]uint64(nil), preview.GetAffectedVersions()...)
	mutationCtx, cancelMutation := callContext()
	defer cancelMutation()
	resp, err := client.PurgeSecretBindingCohort(cf.authCtx(mutationCtx), &kmsv1.PurgeSecretBindingCohortRequest{
		Ref:                      protoRef(ref),
		AnchorVersion:            *version,
		BindingKey:               bindingKey,
		ExpectedRevision:         preview.GetRevision(),
		ExpectedAffectedVersions: affected,
	})
	if err != nil {
		return c.failPurgeSecretRPC("secret purge-binding-cohort", err)
	}
	return c.printCohortMutation("Purged binding-key cohort for", ref.String(), resp)
}

func (c *CLI) cmdSecretPurgeUnboundVersions(args []string) int {
	fs := c.newFlags("secret purge-unbound-versions")
	cf := addConnFlags(c, fs)
	c.setUsage(fs, "secret purge-unbound-versions /env/app/key [flags]", "Preview and irreversibly purge every non-destroyed unbound version. Administrator authentication is required.", false)
	if !c.parseFlags(fs, args) {
		return exitUsage
	}
	ref, ok := c.bindingCommandRef("secret purge-unbound-versions")
	if !ok {
		return exitUsage
	}
	conn, err := c.dialConn(cf)
	if err != nil {
		return c.failErr("", err)
	}
	defer func() { _ = conn.Close() }()
	client := kmsv1.NewSecretServiceClient(conn)
	previewCtx, cancelPreview := callContext()
	preview, err := client.PreviewSecretUnboundVersions(cf.authCtx(previewCtx), &kmsv1.PreviewSecretUnboundVersionsRequest{Ref: protoRef(ref)})
	cancelPreview()
	if err != nil {
		return c.failSecretRPC("secret purge-unbound-versions preview", err)
	}
	if err := validateVersionSet(preview); err != nil {
		return c.fail("secret purge-unbound-versions preview: %v", err)
	}
	c.printVersionSetPreview("Unbound-version purge", ref.String(), preview)
	_, _ = fmt.Fprintf(c.Stderr, "IRREVERSIBLE ADMIN OPERATION: unbound versions %s will be permanently destroyed, even if immutable releases reference them. This cannot be undone.\n", formatVersions(preview.GetAffectedVersions()))
	confirmed, code := c.confirmYesNo(fmt.Sprintf("permanently purge unbound versions of %s: %s", ref, formatVersions(preview.GetAffectedVersions())))
	if !confirmed {
		return code
	}
	mutationCtx, cancelMutation := callContext()
	defer cancelMutation()
	resp, err := client.PurgeSecretUnboundVersions(cf.authCtx(mutationCtx), &kmsv1.PurgeSecretUnboundVersionsRequest{
		Ref:                      protoRef(ref),
		ExpectedRevision:         preview.GetRevision(),
		ExpectedAffectedVersions: append([]uint64(nil), preview.GetAffectedVersions()...),
	})
	if err != nil {
		return c.failPurgeSecretRPC("secret purge-unbound-versions", err)
	}
	return c.printVersionSetMutation("Purged unbound versions for", ref.String(), resp)
}

func (c *CLI) readCurrentSecretVersion(client kmsv1.SecretServiceClient, cf *connFlags, ref domain.Ref, operation string) (uint64, int) {
	ctx, cancel := callContext()
	resp, err := client.GetSecretMetadata(cf.authCtx(ctx), &kmsv1.GetSecretMetadataRequest{Ref: protoRef(ref), Label: domain.LabelCurrent})
	cancel()
	if err != nil {
		return 0, c.failSecretRPC(operation+" metadata", err)
	}
	metadata := resp.GetSecret()
	if metadata == nil || !sameRef(metadata.GetRef(), protoRef(ref)) {
		return 0, c.fail("%s metadata: server returned a different resource", operation)
	}
	current := metadata.GetLabels()["current"]
	if current == 0 {
		return 0, c.fail("%s metadata: secret has no current version", operation)
	}
	return current, exitOK
}

func addExpectedCurrentVersionFlag(fs *flag.FlagSet) *optionalUint64 {
	expected := &optionalUint64{}
	fs.Var(expected, "expected-current-version", "expected current secret `version`; when omitted, read current metadata")
	return expected
}

func (c *CLI) validateExpectedCurrentVersion(operation string, expected *optionalUint64) bool {
	if expected.set && expected.value == 0 {
		c.failUsage("%s: expected-current-version must be greater than zero", operation)
		return false
	}
	return true
}

func (c *CLI) resolveExpectedCurrentVersion(client kmsv1.SecretServiceClient, cf *connFlags, ref domain.Ref, operation string, expected *optionalUint64) (uint64, int) {
	if expected.set {
		return expected.value, exitOK
	}
	return c.readCurrentSecretVersion(client, cf, ref, operation)
}

func (c *CLI) bindingCommandRef(command string) (domain.Ref, bool) {
	pos := c.args()
	if len(pos) < 1 || pos[0] == "" {
		c.failUsage("%s requires a /env/app/key argument", command)
		return domain.Ref{}, false
	}
	if !c.rejectExtraPositionals(1) {
		return domain.Ref{}, false
	}
	ref, err := keyutil.SplitDisplayPath(pos[0])
	if err != nil {
		c.failUsage("invalid path: %v", err)
		return domain.Ref{}, false
	}
	return ref, true
}

func (c *CLI) requiredBindingKey(envName, prompt string, confirm bool) (string, error) {
	if key, ok := c.env(envName); ok && key != "" {
		if err := validateBindingKeyInput(key); err != nil {
			return "", fmt.Errorf("%s: %w", envName, err)
		}
		return key, nil
	}
	if !c.stdinIsTTY() {
		return "", fmt.Errorf("%s is required on non-interactive stdin", envName)
	}
	key, err := c.readHidden(prompt)
	if err != nil {
		return "", err
	}
	if err := validateBindingKeyInput(key); err != nil {
		return "", err
	}
	if !confirm {
		return key, nil
	}
	again, err := c.readHidden("Confirm " + strings.ToLower(prompt[:1]) + prompt[1:])
	if err != nil {
		return "", err
	}
	if key != again {
		return "", fmt.Errorf("binding key confirmation does not match")
	}
	return key, nil
}

func validateBindingKeyInput(key string) error {
	if !utf8.ValidString(key) {
		return fmt.Errorf("binding key must be valid UTF-8")
	}
	if len(key) < minBindingKeyLen {
		return fmt.Errorf("binding key must contain at least %d UTF-8 bytes", minBindingKeyLen)
	}
	return nil
}

func (c *CLI) readHidden(prompt string) (string, error) {
	if c.Stdin == nil {
		return "", fmt.Errorf("terminal input is unavailable")
	}
	_, _ = fmt.Fprint(c.Stderr, prompt)
	reader := c.readPassword
	if reader == nil {
		reader = term.ReadPassword
	}
	raw, err := reader(int(c.Stdin.Fd()))
	_, _ = fmt.Fprintln(c.Stderr)
	if err != nil {
		return "", fmt.Errorf("reading binding key: %w", err)
	}
	key := string(raw)
	clear(raw)
	runtime.KeepAlive(raw)
	return key, nil
}

func validateCohortPreview(resp *kmsv1.SecretBindingCohortResponse) error {
	if resp == nil || resp.GetAnchorVersion() == 0 {
		return fmt.Errorf("server returned an empty anchor version")
	}
	if err := validatePreviewVersions(resp.GetRevision(), resp.GetAffectedVersions()); err != nil {
		return err
	}
	for _, version := range resp.GetAffectedVersions() {
		if version == resp.GetAnchorVersion() {
			return nil
		}
	}
	return fmt.Errorf("server returned affected versions that omit the anchor")
}

func validateVersionSet(resp *kmsv1.SecretVersionSetResponse) error {
	if resp == nil {
		return fmt.Errorf("server returned no affected versions")
	}
	return validatePreviewVersions(resp.GetRevision(), resp.GetAffectedVersions())
}

func validatePreviewVersions(revision uint64, versions []uint64) error {
	if revision == 0 {
		return fmt.Errorf("server returned an empty storage revision")
	}
	if len(versions) == 0 {
		return fmt.Errorf("server returned no affected versions")
	}
	for i, version := range versions {
		if version == 0 {
			return fmt.Errorf("server returned an empty affected version")
		}
		if i > 0 && version <= versions[i-1] {
			return fmt.Errorf("server returned affected versions that are not sorted and unique")
		}
	}
	return nil
}

func (c *CLI) printCohortPreview(action, path string, resp *kmsv1.SecretBindingCohortResponse) {
	_, _ = fmt.Fprintf(c.Stderr, "%s preview for %s\n", action, path)
	_, _ = fmt.Fprintf(c.Stderr, "  anchor version: %d\n", resp.GetAnchorVersion())
	_, _ = fmt.Fprintf(c.Stderr, "  affected versions: %s\n", formatVersions(resp.GetAffectedVersions()))
	_, _ = fmt.Fprintf(c.Stderr, "  storage revision: %d\n", resp.GetRevision())
}

func (c *CLI) printVersionSetPreview(action, path string, resp *kmsv1.SecretVersionSetResponse) {
	_, _ = fmt.Fprintf(c.Stderr, "%s preview for %s\n", action, path)
	_, _ = fmt.Fprintf(c.Stderr, "  affected versions: %s\n", formatVersions(resp.GetAffectedVersions()))
	_, _ = fmt.Fprintf(c.Stderr, "  storage revision: %d\n", resp.GetRevision())
}

func formatVersions(versions []uint64) string {
	parts := make([]string, len(versions))
	for i, version := range versions {
		parts[i] = strconv.FormatUint(version, 10)
	}
	return strings.Join(parts, ", ")
}

// selectSecretVersion resolves a caller's version/label selector entirely
// from live metadata and returns the exact version's protection flags. The
// subsequent read can therefore pin that version and cannot accidentally use
// the current version's protection summary for a historical release pin.
func selectSecretVersion(metadata *kmsv1.SecretMetadata, version uint64, label string) (*kmsv1.SecretVersionInfo, error) {
	if metadata == nil {
		return nil, fmt.Errorf("server returned empty secret metadata")
	}
	if version != 0 && label != "" {
		return nil, fmt.Errorf("version and label are mutually exclusive")
	}
	if version == 0 {
		if label == "" {
			label = "current"
		}
		var ok bool
		version, ok = metadata.GetLabels()[label]
		if !ok || version == 0 {
			return nil, fmt.Errorf("secret has no %q label", label)
		}
	}
	var selected *kmsv1.SecretVersionInfo
	for _, candidate := range metadata.GetVersions() {
		if candidate.GetVersion() == version {
			if selected != nil {
				return nil, fmt.Errorf("secret metadata contains version %d more than once", version)
			}
			selected = candidate
		}
	}
	if selected == nil {
		return nil, fmt.Errorf("secret metadata does not contain version %d", version)
	}
	if selected.GetState() != domain.StateEnabled || selected.GetDestroyedAtUnixMs() != 0 ||
		(selected.GetExpiresAtUnixMs() > 0 && selected.GetExpiresAtUnixMs() <= time.Now().UnixMilli()) {
		return nil, fmt.Errorf("secret version %d is unavailable", version)
	}
	return selected, nil
}

type secretMutationJSON struct {
	Key              string   `json:"key"`
	CurrentVersion   uint64   `json:"current_version,omitempty"`
	PreviousVersion  uint64   `json:"previous_version,omitempty"`
	AnchorVersion    uint64   `json:"anchor_version,omitempty"`
	AffectedVersions []uint64 `json:"affected_versions,omitempty"`
	Revision         uint64   `json:"revision"`
}

func (c *CLI) printSecretTransition(action, path string, resp *kmsv1.SecretVersionTransitionResponse) int {
	if resp == nil || resp.GetCurrentVersion() == 0 || resp.GetPreviousVersion() == 0 ||
		resp.GetRevision() == 0 || resp.GetCurrentVersion() <= resp.GetPreviousVersion() {
		return c.fail("server returned an invalid secret transition response")
	}
	if c.jsonOutput() {
		return c.printJSON(secretMutationJSON{
			Key: path, CurrentVersion: resp.GetCurrentVersion(),
			PreviousVersion: resp.GetPreviousVersion(), Revision: resp.GetRevision(),
		})
	}
	_, _ = fmt.Fprintf(c.Stdout, "%s %s as new version %d; previous version %d is unchanged (revision %d)\n", action, path, resp.GetCurrentVersion(), resp.GetPreviousVersion(), resp.GetRevision())
	return exitOK
}

func (c *CLI) printVersionSetMutation(action, path string, resp *kmsv1.SecretVersionSetResponse) int {
	if err := validateVersionSet(resp); err != nil {
		return c.fail("server returned an invalid version-set mutation response: %v", err)
	}
	if c.jsonOutput() {
		return c.printJSON(secretMutationJSON{
			Key: path, AffectedVersions: resp.GetAffectedVersions(), Revision: resp.GetRevision(),
		})
	}
	_, _ = fmt.Fprintf(c.Stdout, "%s %s versions %s (revision %d)\n", action, path, formatVersions(resp.GetAffectedVersions()), resp.GetRevision())
	return exitOK
}

func (c *CLI) printCohortMutation(action, path string, resp *kmsv1.SecretBindingCohortResponse) int {
	if err := validateCohortPreview(resp); err != nil {
		return c.fail("server returned an invalid cohort mutation response: %v", err)
	}
	if c.jsonOutput() {
		return c.printJSON(secretMutationJSON{
			Key: path, AnchorVersion: resp.GetAnchorVersion(),
			AffectedVersions: resp.GetAffectedVersions(), Revision: resp.GetRevision(),
		})
	}
	_, _ = fmt.Fprintf(c.Stdout, "%s %s versions %s (revision %d)\n", action, path, formatVersions(resp.GetAffectedVersions()), resp.GetRevision())
	return exitOK
}
