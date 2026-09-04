package cli

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"runtime"
	"strconv"
	"strings"
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
  rotate PATH               Rotate the contiguous cohort around --version (0 = current).

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
  bind PATH                   Bind one exact version in place (0 = current).
  unbind PATH                 Unbind one exact version in place (0 = current).
  purge-binding-cohort PATH   Irreversibly purge a compromised contiguous cohort (admin only).

The operation's primary key comes from KMS_BINDING_KEY. A required key is prompted without
echo on an interactive terminal.
`)
}

func (c *CLI) cmdSecretBind(args []string) int {
	fs := c.newFlags("secret bind")
	cf := addConnFlags(c, fs)
	version := fs.Uint64("version", 0, "exact secret `version` (0 = current label)")
	c.setUsage(fs, "secret bind /env/app/key [flags]", "Add binding-key protection to one secret version in place.", false)
	if !c.parseFlags(fs, args) {
		return exitUsage
	}
	ref, ok := c.bindingCommandRef("secret bind")
	if !ok {
		return exitUsage
	}
	bindingKey, err := c.requiredBindingKey(bindingKeyEnv, "New binding key for "+ref.String()+": ", true)
	if err != nil {
		return c.failUsage("secret bind: %v", err)
	}
	conn, err := c.dialConn(cf)
	if err != nil {
		return c.failErr("", err)
	}
	defer func() { _ = conn.Close() }()
	ctx, cancel := callContext()
	defer cancel()
	resp, err := kmsv1.NewSecretServiceClient(conn).BindSecret(cf.authCtx(ctx), &kmsv1.BindSecretRequest{
		Ref: protoRef(ref), Version: *version, BindingKey: bindingKey,
	})
	if err != nil {
		return c.failErr("secret bind", err)
	}
	return c.printSecretMutation("Bound", ref.String(), resp)
}

func (c *CLI) cmdSecretUnbind(args []string) int {
	fs := c.newFlags("secret unbind")
	cf := addConnFlags(c, fs)
	version := fs.Uint64("version", 0, "exact secret `version` (0 = current label)")
	c.setUsage(fs, "secret unbind /env/app/key [flags]", "Remove binding-key protection from one secret version in place.", false)
	if !c.parseFlags(fs, args) {
		return exitUsage
	}
	ref, ok := c.bindingCommandRef("secret unbind")
	if !ok {
		return exitUsage
	}
	bindingKey, err := c.requiredBindingKey(bindingKeyEnv, "Binding key for "+ref.String()+": ", false)
	if err != nil {
		return c.failUsage("secret unbind: %v", err)
	}
	conn, err := c.dialConn(cf)
	if err != nil {
		return c.failErr("", err)
	}
	defer func() { _ = conn.Close() }()
	ctx, cancel := callContext()
	defer cancel()
	resp, err := kmsv1.NewSecretServiceClient(conn).UnbindSecret(cf.authCtx(ctx), &kmsv1.UnbindSecretRequest{
		Ref: protoRef(ref), Version: *version, BindingKey: bindingKey,
	})
	if err != nil {
		return c.failErr("secret unbind", err)
	}
	return c.printSecretMutation("Unbound", ref.String(), resp)
}

func (c *CLI) cmdBindingKeyRotate(args []string) int {
	fs := c.newFlags("binding-key rotate")
	cf := addConnFlags(c, fs)
	version := fs.Uint64("version", 0, "anchor secret `version` (0 = current label)")
	c.setUsage(fs, "binding-key rotate /env/app/key [flags]", "Preview, confirm, and rotate one contiguous binding-key cohort.", false)
	if !c.parseFlags(fs, args) {
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
	previewCtx, cancelPreview := callContext()
	preview, err := client.PreviewSecretBindingCohort(cf.authCtx(previewCtx), &kmsv1.PreviewSecretBindingCohortRequest{
		Ref: protoRef(ref), AnchorVersion: *version, BindingKey: oldKey,
	})
	cancelPreview()
	if err != nil {
		return c.failErr("binding-key rotate preview", err)
	}
	if err := validateCohortPreview(preview); err != nil {
		return c.fail("binding-key rotate preview: %v", err)
	}
	c.printCohortPreview("Binding-key rotation", ref.String(), preview)
	ok, code := c.confirmYesNo(fmt.Sprintf("rotate binding key for %s versions %s", ref, formatVersions(preview.GetAffectedVersions())))
	if !ok {
		return code
	}
	newKey, err := c.requiredBindingKey(newBindingKeyEnv, "New binding key for "+ref.String()+": ", true)
	if err != nil {
		return c.failUsage("binding-key rotate: %v", err)
	}
	revision := preview.GetRevision()
	affected := append([]uint64(nil), preview.GetAffectedVersions()...)
	mutationCtx, cancelMutation := callContext()
	defer cancelMutation()
	resp, err := client.RotateSecretBindingKey(cf.authCtx(mutationCtx), &kmsv1.RotateSecretBindingKeyRequest{
		Ref:                      protoRef(ref),
		AnchorVersion:            *version,
		BindingKey:               oldKey,
		NewBindingKey:            newKey,
		ExpectedRevision:         &revision,
		ExpectedAffectedVersions: affected,
	})
	if err != nil {
		return c.failErr("binding-key rotate", err)
	}
	return c.printCohortMutation("Rotated binding key for", ref.String(), resp)
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
		return c.failErr("secret purge-binding-cohort preview", err)
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
	revision := preview.GetRevision()
	affected := append([]uint64(nil), preview.GetAffectedVersions()...)
	mutationCtx, cancelMutation := callContext()
	defer cancelMutation()
	resp, err := client.PurgeSecretBindingCohort(cf.authCtx(mutationCtx), &kmsv1.PurgeSecretBindingCohortRequest{
		Ref:                      protoRef(ref),
		AnchorVersion:            *version,
		BindingKey:               bindingKey,
		ExpectedRevision:         &revision,
		ExpectedAffectedVersions: affected,
	})
	if err != nil {
		return c.failErr("secret purge-binding-cohort", err)
	}
	return c.printCohortMutation("Purged binding-key cohort for", ref.String(), resp)
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
	if len(resp.GetAffectedVersions()) == 0 {
		return fmt.Errorf("server returned no affected versions")
	}
	return nil
}

func (c *CLI) printCohortPreview(action, path string, resp *kmsv1.SecretBindingCohortResponse) {
	_, _ = fmt.Fprintf(c.Stderr, "%s preview for %s\n", action, path)
	_, _ = fmt.Fprintf(c.Stderr, "  anchor version: %d\n", resp.GetAnchorVersion())
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
	for _, candidate := range metadata.GetVersions() {
		if candidate.GetVersion() == version {
			return candidate, nil
		}
	}
	return nil, fmt.Errorf("secret metadata does not contain version %d", version)
}

type secretMutationJSON struct {
	Key              string   `json:"key"`
	AnchorVersion    uint64   `json:"anchor_version"`
	AffectedVersions []uint64 `json:"affected_versions"`
	Revision         uint64   `json:"revision"`
}

func (c *CLI) printSecretMutation(action, path string, resp *kmsv1.SecretVersionMutationResponse) int {
	if resp == nil || resp.GetAnchorVersion() == 0 || len(resp.GetAffectedVersions()) == 0 {
		return c.fail("server returned an invalid secret mutation response")
	}
	if c.jsonOutput() {
		return c.printJSON(secretMutationJSON{
			Key: path, AnchorVersion: resp.GetAnchorVersion(),
			AffectedVersions: resp.GetAffectedVersions(), Revision: resp.GetRevision(),
		})
	}
	_, _ = fmt.Fprintf(c.Stdout, "%s %s version %d (revision %d)\n", action, path, resp.GetAnchorVersion(), resp.GetRevision())
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
