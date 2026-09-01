package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/fileutil"
	"github.com/Suhaibinator/kms/internal/keyutil"
)

// cmdAdmin dispatches the administrative subcommands that talk to a running
// server over gRPC (namespace/identity/ca management). Unlike the offline
// commands (init, backup, ...), these require a reachable, unsealed server and
// an admin credential.
func (c *CLI) cmdAdmin(args []string) int {
	if len(args) == 0 {
		c.adminUsage()
		return 2
	}
	group, rest := args[0], args[1:]
	switch group {
	case "namespace", "ns":
		return c.cmdAdminNamespace(rest)
	case "identity", "id":
		return c.cmdAdminIdentity(rest)
	case "policy":
		return c.cmdAdminPolicy(rest)
	case "ca":
		return c.cmdAdminCA(rest)
	case "help", "-h", "--help":
		c.adminUsage()
		return 0
	default:
		_, _ = fmt.Fprintf(c.Stderr, "unknown admin subcommand %q\n\n", group)
		c.adminUsage()
		return 2
	}
}

func (c *CLI) adminUsage() {
	_, _ = fmt.Fprint(c.Stderr, `parameter-store admin — connect applications and manage a running server over gRPC

Usage:
  parameter-store admin <group> <action> [args] [flags]

Namespaces:
  namespace create --env E --app A   Create a namespace (--description, --auth-methods).
  namespace update --env E --app A   Replace description and allowed auth methods.
  namespace delete --env E --app A   Delete an empty namespace.
  namespace list                     List namespaces with parameter/secret counts.

Identities:
  identity create NAME       Create application credentials; mTLS by default
                             (--kind, --namespace, --auth, --ttl, --out).
  identity issue-cert NAME   Issue replacement/additional application mTLS credentials
                             (--ttl, --out).
  identity revoke-cert NAME  Revoke one certificate (--serial).
  identity rotate NAME       Rotate a token identity's bearer token.
  identity revoke NAME       Disable an identity (invalidates all its certs).
  identity list              List identities.

Policies:
  policy create NAME --subject IDENTITY --allow OP@ENV/APP [--allow ...] [--deny OP@ENV/APP ...]
                             Create a namespace-level policy. Either label may be *, and a
                             bare OP means every namespace. Example: grant a CI identity the
                             verification oracle only:
                               --allow configuration-release:verify-defaults@prod/gradethis
  policy list                List policies with their allow and deny rules.
  policy delete NAME         Delete a policy.

CA:
  ca show                    Export the built-in client-issuing CA for inspection or
                             out-of-band verification (--out FILE).

Certificate roles:
  Applications present NAME.crt and prove possession with NAME.key; the key stays local.
  Applications verify the operator-provided KMS server certificate with its CA bundle.
  "ca show" is NOT that server-trust CA; it exports the CA KMS uses to issue client certs.
  Admin identities always receive a one-time bearer token, even with --auth mtls or both.

Connection flags (--endpoint, --token, --ca, --cert, --key, --insecure) are shared
by every command here and documented in "admin <group> <action> -h"; each has a
KMS_* environment fallback.
`)
}

// --- namespaces ------------------------------------------------------------

func (c *CLI) cmdAdminNamespace(args []string) int {
	if len(args) == 0 {
		return c.fail("admin namespace requires an action (create|update|delete|list)")
	}
	action, rest := args[0], args[1:]
	switch action {
	case "create":
		return c.cmdNamespaceWrite(rest, false)
	case "update":
		return c.cmdNamespaceWrite(rest, true)
	case "delete":
		return c.cmdNamespaceDelete(rest)
	case "list":
		return c.cmdNamespaceList(rest)
	default:
		return c.fail("unknown namespace action %q", action)
	}
}

// cmdNamespaceWrite handles create and update (same flags; update is a full
// replace of description and auth methods).
func (c *CLI) cmdNamespaceWrite(args []string, update bool) int {
	name, summary := "namespace create", "Create a namespace with its description and allowed authentication methods."
	if update {
		name = "namespace update"
		summary = "Replace a namespace's description and allowed authentication methods; both are set from the flags rather than merged."
	}
	fs := c.newFlags(name)
	cf := addConnFlags(c, fs)
	env := fs.String("env", "", "namespace `environment` (e.g. prod)")
	app := fs.String("app", "", "namespace `application` (e.g. gradethis)")
	description := fs.String("description", "", "namespace `description`")
	authMethods := fs.String("auth-methods", "", "comma-separated allowed auth `methods` (mtls,token); default mtls")
	c.setUsage(fs, "admin "+name+" --env ENV --app APP [flags]", summary, false)
	if !c.parseFlags(fs, args) {
		return 2
	}
	ns, err := namespaceFromFlags(*env, *app)
	if err != nil {
		return c.fail("%s: %v", name, err)
	}
	methods, err := parseAuthMethods(*authMethods)
	if err != nil {
		return c.fail("%v", err)
	}
	pns := &kmsv1.NamespaceRef{Env: ns.Env, App: ns.App}

	conn, err := cf.dial()
	if err != nil {
		return c.fail("%v", err)
	}
	defer func() { _ = conn.Close() }()
	ctx, cancel := callContext()
	defer cancel()
	client := kmsv1.NewAdminServiceClient(conn)

	var out *kmsv1.Namespace
	if update {
		resp, uerr := client.UpdateNamespace(cf.authCtx(ctx), &kmsv1.UpdateNamespaceRequest{
			Ref: pns, Description: *description, AllowedAuthMethods: methods,
		})
		if uerr != nil {
			return c.fail("%s: %v", name, uerr)
		}
		out = resp.Namespace
	} else {
		resp, cerr := client.CreateNamespace(cf.authCtx(ctx), &kmsv1.CreateNamespaceRequest{
			Ref: pns, Description: *description, AllowedAuthMethods: methods,
		})
		if cerr != nil {
			return c.fail("%s: %v", name, cerr)
		}
		out = resp.Namespace
	}
	_, _ = fmt.Fprintf(c.Stdout, "%s/%s (auth: %s)\n", out.GetRef().GetEnv(), out.GetRef().GetApp(),
		strings.Join(out.GetAllowedAuthMethods(), ","))
	return 0
}

func (c *CLI) cmdNamespaceDelete(args []string) int {
	fs := c.newFlags("namespace delete")
	cf := addConnFlags(c, fs)
	env := fs.String("env", "", "namespace `environment` (e.g. prod)")
	app := fs.String("app", "", "namespace `application` (e.g. gradethis)")
	c.setUsage(fs, "admin namespace delete --env ENV --app APP [flags]",
		"Delete a namespace that holds no parameters or secrets.", false)
	if !c.parseFlags(fs, args) {
		return 2
	}
	ns, err := namespaceFromFlags(*env, *app)
	if err != nil {
		return c.fail("namespace delete: %v", err)
	}

	conn, err := cf.dial()
	if err != nil {
		return c.fail("%v", err)
	}
	defer func() { _ = conn.Close() }()
	ctx, cancel := callContext()
	defer cancel()

	if _, err := kmsv1.NewAdminServiceClient(conn).DeleteNamespace(cf.authCtx(ctx), &kmsv1.DeleteNamespaceRequest{
		Ref: &kmsv1.NamespaceRef{Env: ns.Env, App: ns.App},
	}); err != nil {
		return c.fail("namespace delete: %v", err)
	}
	_, _ = fmt.Fprintf(c.Stdout, "Deleted namespace %s\n", ns)
	return 0
}

func (c *CLI) cmdNamespaceList(args []string) int {
	fs := c.newFlags("namespace list")
	cf := addConnFlags(c, fs)
	c.setUsage(fs, "admin namespace list [flags]",
		"List namespaces with their allowed auth methods and parameter/secret counts.", false)
	if !c.parseFlags(fs, args) {
		return 2
	}
	conn, err := cf.dial()
	if err != nil {
		return c.fail("%v", err)
	}
	defer func() { _ = conn.Close() }()
	ctx, cancel := callContext()
	defer cancel()
	client := kmsv1.NewAdminServiceClient(conn)

	tw := tabwriter.NewWriter(c.Stdout, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "NAMESPACE\tAUTH\tPARAMS\tSECRETS\tDESCRIPTION")
	for token := ""; ; {
		resp, err := client.ListNamespaces(cf.authCtx(ctx), &kmsv1.ListNamespacesRequest{PageToken: token})
		if err != nil {
			return c.fail("namespace list: %v", err)
		}
		for _, ns := range resp.Namespaces {
			_, _ = fmt.Fprintf(tw, "%s/%s\t%s\t%d\t%d\t%s\n",
				ns.GetRef().GetEnv(), ns.GetRef().GetApp(),
				strings.Join(ns.GetAllowedAuthMethods(), ","),
				ns.GetParameterCount(), ns.GetSecretCount(), ns.GetDescription())
		}
		if token = resp.NextPageToken; token == "" {
			break
		}
	}
	_ = tw.Flush()
	return 0
}

// --- identities ------------------------------------------------------------

func (c *CLI) cmdAdminIdentity(args []string) int {
	if len(args) == 0 {
		return c.fail("admin identity requires an action (create|issue-cert|revoke-cert|rotate|revoke|list)")
	}
	action, rest := args[0], args[1:]
	switch action {
	case "create":
		return c.cmdIdentityCreate(rest)
	case "issue-cert":
		return c.cmdIdentityIssueCert(rest)
	case "revoke-cert":
		return c.cmdIdentityRevokeCert(rest)
	case "rotate":
		return c.cmdIdentityRotate(rest)
	case "revoke":
		return c.cmdIdentityRevoke(rest)
	case "list":
		return c.cmdIdentityList(rest)
	default:
		return c.fail("unknown identity action %q", action)
	}
}

func (c *CLI) cmdIdentityCreate(args []string) int {
	fs := c.newFlags("identity create")
	cf := addConnFlags(c, fs)
	kind := fs.String("kind", "client", "identity `kind` (client|admin); admin always also receives a one-time bearer token")
	namespace := fs.String("namespace", "", "home namespace `env/app` (optional)")
	auth := fs.String("auth", "mtls", "credential `method` to create for the application: mtls, token, or both")
	ttl := fs.String("ttl", "", "certificate `lifetime` (e.g. 90d, 720h); default 90d")
	outDir := fs.String("out", "", "`directory` for one-time application client credentials (NAME.crt/NAME.key); recommended")
	c.setUsage(fs, "admin identity create NAME [flags]",
		"Create an application identity and its one-time credentials.", false)
	if !c.parseFlags(fs, args) {
		return 2
	}
	pos := c.args()
	if len(pos) < 1 || pos[0] == "" {
		return c.fail("identity create requires a NAME argument")
	}
	name := pos[0]

	methods, err := authFlagToMethods(*auth)
	if err != nil {
		return c.fail("%v", err)
	}
	ttlSeconds, err := parseTTLSeconds(*ttl)
	if err != nil {
		return c.fail("%v", err)
	}
	req := &kmsv1.CreateIdentityRequest{
		Name:           name,
		Kind:           *kind,
		AuthMethods:    methods,
		CertTtlSeconds: ttlSeconds,
	}
	if *namespace != "" {
		ns, perr := keyutil.ParseNamespace(*namespace)
		if perr != nil {
			return c.fail("invalid --namespace: %v", perr)
		}
		req.Namespace = &kmsv1.NamespaceRef{Env: ns.Env, App: ns.App}
	}

	conn, err := cf.dial()
	if err != nil {
		return c.fail("%v", err)
	}
	defer func() { _ = conn.Close() }()
	ctx, cancel := callContext()
	defer cancel()

	client := kmsv1.NewAdminServiceClient(conn)
	create := func(certOutput *reservedCertBundle) error {
		resp, createErr := client.CreateIdentity(cf.authCtx(ctx), req)
		if createErr != nil {
			return fmt.Errorf("identity create: %w", createErr)
		}
		return c.writeCreatedIdentityResult(name, *kind, methods, certOutput, resp)
	}
	if hasAuthMethod(methods, "mtls") {
		err = c.withReservedCertBundle(*outDir, name, create)
	} else {
		err = create(nil)
	}
	if err != nil {
		return c.fail("%v", err)
	}
	return 0
}

func (c *CLI) writeCreatedIdentityResult(name, kind string, methods []string, certOutput *reservedCertBundle, resp *kmsv1.CreateIdentityResponse) error {
	if resp == nil {
		return fmt.Errorf("server returned no identity result")
	}
	// Persist one-time private-key material before writing informational
	// stdout. A broken pipe must not discard an already-enrolled key when a
	// healthy file sink was reserved before the RPC.
	if hasAuthMethod(methods, "mtls") && resp.Cert == nil {
		return fmt.Errorf("server returned no certificate bundle")
	}
	if resp.Cert != nil {
		if err := c.writeCertBundleToOutput(certOutput, resp.Cert); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(c.Stdout, "Created identity %q (kind %s).\n", name, kind); err != nil {
		return fmt.Errorf("writing identity output: %w", err)
	}
	if resp.Token != "" {
		if _, err := fmt.Fprintf(c.Stdout, "  token: %s\n", resp.Token); err != nil {
			return fmt.Errorf("writing one-time identity token: %w", err)
		}
		if _, err := fmt.Fprintln(c.Stdout, "  WARNING: the token is shown once and cannot be recovered."); err != nil {
			return fmt.Errorf("writing one-time identity token warning: %w", err)
		}
	}
	if hasAuthMethod(methods, "mtls") {
		if err := c.writeMTLSCredentialNextSteps(certOutput); err != nil {
			return err
		}
	}
	return nil
}

func (c *CLI) cmdIdentityIssueCert(args []string) int {
	fs := c.newFlags("identity issue-cert")
	cf := addConnFlags(c, fs)
	ttl := fs.String("ttl", "", "certificate `lifetime` (e.g. 90d, 720h); default 90d")
	outDir := fs.String("out", "", "`directory` for one-time application client credentials (NAME.crt/NAME.key); recommended")
	c.setUsage(fs, "admin identity issue-cert NAME [flags]",
		"Issue additional or replacement mTLS credentials for an existing identity.", false)
	if !c.parseFlags(fs, args) {
		return 2
	}
	pos := c.args()
	if len(pos) < 1 || pos[0] == "" {
		return c.fail("identity issue-cert requires a NAME argument")
	}
	name := pos[0]
	ttlSeconds, err := parseTTLSeconds(*ttl)
	if err != nil {
		return c.fail("%v", err)
	}

	conn, err := cf.dial()
	if err != nil {
		return c.fail("%v", err)
	}
	defer func() { _ = conn.Close() }()
	ctx, cancel := callContext()
	defer cancel()

	client := kmsv1.NewAdminServiceClient(conn)
	err = c.withReservedCertBundle(*outDir, name, func(certOutput *reservedCertBundle) error {
		resp, issueErr := client.IssueIdentityCertificate(cf.authCtx(ctx), &kmsv1.IssueIdentityCertificateRequest{
			Name: name, TtlSeconds: ttlSeconds,
		})
		if issueErr != nil {
			return fmt.Errorf("identity issue-cert: %w", issueErr)
		}
		return c.writeIssuedIdentityCertificateResult(name, certOutput, resp.Cert)
	})
	if err != nil {
		return c.fail("%v", err)
	}
	return 0
}

func (c *CLI) writeIssuedIdentityCertificateResult(name string, certOutput *reservedCertBundle, bundle *kmsv1.CertBundle) error {
	// As with initial identity creation, publish the one-time private key before
	// emitting status or guidance that could fail on a broken stdout.
	if err := c.writeCertBundleToOutput(certOutput, bundle); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(c.Stdout, "Issued new mTLS credentials for identity %q.\n", name); err != nil {
		return fmt.Errorf("writing certificate output: %w", err)
	}
	return c.writeMTLSCredentialNextSteps(certOutput)
}

func (c *CLI) cmdIdentityRevokeCert(args []string) int {
	fs := c.newFlags("identity revoke-cert")
	cf := addConnFlags(c, fs)
	serial := fs.String("serial", "", "certificate `serial` to revoke")
	c.setUsage(fs, "admin identity revoke-cert NAME --serial SERIAL [flags]",
		"Revoke a single issued certificate by serial number.", false)
	if !c.parseFlags(fs, args) {
		return 2
	}
	pos := c.args()
	if len(pos) < 1 || pos[0] == "" {
		return c.fail("identity revoke-cert requires a NAME argument")
	}
	if *serial == "" {
		return c.fail("--serial is required")
	}

	conn, err := cf.dial()
	if err != nil {
		return c.fail("%v", err)
	}
	defer func() { _ = conn.Close() }()
	ctx, cancel := callContext()
	defer cancel()

	if _, err := kmsv1.NewAdminServiceClient(conn).RevokeIdentityCertificate(cf.authCtx(ctx), &kmsv1.RevokeIdentityCertificateRequest{
		Name: pos[0], Serial: *serial,
	}); err != nil {
		return c.fail("identity revoke-cert: %v", err)
	}
	_, _ = fmt.Fprintf(c.Stdout, "Revoked certificate %s for identity %q\n", *serial, pos[0])
	return 0
}

func (c *CLI) cmdIdentityRotate(args []string) int {
	fs := c.newFlags("identity rotate")
	cf := addConnFlags(c, fs)
	c.setUsage(fs, "admin identity rotate NAME [flags]",
		"Rotate a token identity's bearer token and print the new one once.", false)
	if !c.parseFlags(fs, args) {
		return 2
	}
	pos := c.args()
	if len(pos) < 1 || pos[0] == "" {
		return c.fail("identity rotate requires a NAME argument")
	}

	conn, err := cf.dial()
	if err != nil {
		return c.fail("%v", err)
	}
	defer func() { _ = conn.Close() }()
	ctx, cancel := callContext()
	defer cancel()

	resp, err := kmsv1.NewAdminServiceClient(conn).RotateIdentityToken(cf.authCtx(ctx), &kmsv1.RotateIdentityTokenRequest{Name: pos[0]})
	if err != nil {
		return c.fail("identity rotate: %v", err)
	}
	if err := printTokenOnce(c.Stdout, "identity", pos[0], resp.Token); err != nil {
		return c.fail("writing one-time identity token: %v", err)
	}
	return 0
}

func (c *CLI) cmdIdentityRevoke(args []string) int {
	fs := c.newFlags("identity revoke")
	cf := addConnFlags(c, fs)
	c.setUsage(fs, "admin identity revoke NAME [flags]",
		"Disable an identity, invalidating every certificate it holds.", false)
	if !c.parseFlags(fs, args) {
		return 2
	}
	pos := c.args()
	if len(pos) < 1 || pos[0] == "" {
		return c.fail("identity revoke requires a NAME argument")
	}

	conn, err := cf.dial()
	if err != nil {
		return c.fail("%v", err)
	}
	defer func() { _ = conn.Close() }()
	ctx, cancel := callContext()
	defer cancel()

	if _, err := kmsv1.NewAdminServiceClient(conn).RevokeIdentity(cf.authCtx(ctx), &kmsv1.RevokeIdentityRequest{Name: pos[0]}); err != nil {
		return c.fail("identity revoke: %v", err)
	}
	_, _ = fmt.Fprintf(c.Stdout, "Revoked identity %q (all its certificates are now invalid)\n", pos[0])
	return 0
}

func (c *CLI) cmdIdentityList(args []string) int {
	fs := c.newFlags("identity list")
	cf := addConnFlags(c, fs)
	c.setUsage(fs, "admin identity list [flags]",
		"List identities with their kind, home namespace, and credential state.", false)
	if !c.parseFlags(fs, args) {
		return 2
	}
	conn, err := cf.dial()
	if err != nil {
		return c.fail("%v", err)
	}
	defer func() { _ = conn.Close() }()
	ctx, cancel := callContext()
	defer cancel()
	client := kmsv1.NewAdminServiceClient(conn)

	tw := tabwriter.NewWriter(c.Stdout, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "NAME\tKIND\tNAMESPACE\tTOKEN\tCERTS\tDISABLED")
	for token := ""; ; {
		resp, err := client.ListIdentities(cf.authCtx(ctx), &kmsv1.ListIdentitiesRequest{PageToken: token})
		if err != nil {
			return c.fail("identity list: %v", err)
		}
		for _, id := range resp.Identities {
			ns := "-"
			if n := id.GetNamespace(); n != nil {
				ns = n.GetEnv() + "/" + n.GetApp()
			}
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%t\t%d\t%t\n",
				id.GetName(), id.GetKind(), ns, id.GetHasToken(), len(id.GetCerts()), id.GetDisabled())
		}
		if token = resp.NextPageToken; token == "" {
			break
		}
	}
	_ = tw.Flush()
	return 0
}

// --- CA --------------------------------------------------------------------

func (c *CLI) cmdAdminCA(args []string) int {
	if len(args) == 0 || args[0] != "show" {
		return c.fail("admin ca supports only: ca show")
	}
	fs := c.newFlags("ca show")
	cf := addConnFlags(c, fs)
	out := fs.String("out", "", "export the built-in client-issuing CA to this `file` (not the KMS server-trust CA)")
	c.setUsage(fs, "admin ca show [flags]",
		"Export the built-in client-issuing CA certificate, which is not the CA that signs the server certificate.", false)
	if !c.parseFlags(fs, args[1:]) {
		return 2
	}
	conn, err := cf.dial()
	if err != nil {
		return c.fail("%v", err)
	}
	defer func() { _ = conn.Close() }()
	ctx, cancel := callContext()
	defer cancel()

	// GetCACertificate is public; no credential is attached.
	resp, err := kmsv1.NewAdminServiceClient(conn).GetCACertificate(ctx, &kmsv1.GetCACertificateRequest{})
	if err != nil {
		return c.fail("ca show: %v", err)
	}
	if *out != "" {
		if err := os.WriteFile(*out, []byte(resp.CertPem), 0o644); err != nil {
			return c.fail("writing --out: %v", err)
		}
		_, _ = fmt.Fprintf(c.Stderr, "Wrote built-in client-issuing CA certificate to %s\n", *out)
		_, _ = fmt.Fprintln(c.Stderr, "This is not the CA bundle applications use to verify the KMS server certificate.")
		return 0
	}
	_, _ = fmt.Fprint(c.Stdout, resp.CertPem)
	return 0
}

// --- shared helpers --------------------------------------------------------

// reservedCertBundle holds exclusive reservations for the two output paths.
// Commands reserve before invoking an RPC that returns a one-time private key,
// so a local path collision cannot consume an otherwise unrecoverable key.
type reservedCertBundle struct {
	certPath string
	keyPath  string
	certFile *os.File
	keyFile  *os.File
	// published becomes true only after both one-time credential files have
	// been completely written and closed. Later status-output failures must not
	// tell the operator to remove a successfully published, unrecoverable key.
	published bool
}

var certOutputNameRE = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`)

func reserveCertBundle(outDir, name string) (*reservedCertBundle, error) {
	// Match the server's identity-name grammar before using the name as a path
	// component. In particular, never transiently reserve ../NAME outside outDir
	// only to have the server reject the identity later.
	if !certOutputNameRE.MatchString(name) {
		return nil, fmt.Errorf("invalid identity name %q for certificate output", name)
	}
	// Resolve the caller's output-directory spelling once and use only that
	// canonical, validated parent from this point onward. Resolving the key path
	// independently after reserving the certificate would let a concurrent
	// symlink replacement split the one-time bundle across two directories and
	// make later status/error paths refer to a different entry.
	certPath, err := fileutil.ResolveStablePath(filepath.Join(outDir, name+".crt"))
	if err != nil {
		return nil, fmt.Errorf("writing certificate: %w", err)
	}
	keyPath := filepath.Join(filepath.Dir(certPath), name+".key")
	certFile, err := os.OpenFile(certPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return nil, fmt.Errorf("writing certificate: %w", err)
	}
	keyFile, err := fileutil.OpenPrivateExclusive(keyPath)
	if err != nil {
		_ = certFile.Close()
		// Do not unlink by pathname: another writer with access to outDir could
		// replace the reservation between OpenFile and cleanup. Leaving the empty
		// certificate placeholder is fail-safe and makes operator cleanup explicit.
		return nil, fmt.Errorf("writing private key: %w (certificate reservation left at %s; inspect and remove it before retrying)", err, certPath)
	}
	return &reservedCertBundle{
		certPath: certPath,
		keyPath:  keyPath,
		certFile: certFile,
		keyFile:  keyFile,
	}, nil
}

func (output *reservedCertBundle) cleanup() {
	if output == nil {
		return
	}
	_ = output.certFile.Close()
	_ = output.keyFile.Close()
}

func (c *CLI) withReservedCertBundle(outDir, name string, use func(*reservedCertBundle) error) error {
	if outDir == "" {
		return use(nil)
	}
	output, err := reserveCertBundle(outDir, name)
	if err != nil {
		return err
	}
	defer output.cleanup()
	if err := use(output); err != nil {
		if output.published {
			return fmt.Errorf("%w (one-time credentials were fully written to %s and %s; preserve them and verify the identity/certificate state on the server before retrying)", err, output.certPath, output.keyPath)
		}
		// Reservations and any partial key remain in place. Removing them by name
		// would be unsafe in a shared writable directory because those names can be
		// unlinked and replaced while the RPC is in flight.
		return fmt.Errorf("%w (certificate reservations left at %s and %s; inspect and remove them before retrying)", err, output.certPath, output.keyPath)
	}
	return nil
}

func (c *CLI) writeCertBundleToOutput(output *reservedCertBundle, bundle *kmsv1.CertBundle) error {
	if bundle == nil {
		return fmt.Errorf("server returned no certificate bundle")
	}
	if output == nil {
		if _, err := fmt.Fprintf(c.Stdout, "  certificate (serial %s, expires %s):\n", bundle.Serial,
			time.UnixMilli(bundle.NotAfterUnixMs).UTC().Format(time.RFC3339)); err != nil {
			return fmt.Errorf("writing certificate output: %w", err)
		}
		for _, text := range []string{
			bundle.CertPem,
			bundle.KeyPem,
			"  WARNING: the private key is shown once and is never stored server-side.\n",
		} {
			if _, err := io.WriteString(c.Stdout, text); err != nil {
				return fmt.Errorf("writing certificate output: %w", err)
			}
		}
		return nil
	}

	if _, err := output.certFile.Write([]byte(bundle.CertPem)); err != nil {
		return fmt.Errorf("writing certificate: %w", err)
	}
	if _, err := output.keyFile.Write([]byte(bundle.KeyPem)); err != nil {
		return fmt.Errorf("writing private key: %w", err)
	}
	if err := output.certFile.Close(); err != nil {
		return fmt.Errorf("closing certificate: %w", err)
	}
	if err := output.keyFile.Close(); err != nil {
		return fmt.Errorf("closing private key: %w", err)
	}
	output.published = true
	if _, err := fmt.Fprintf(c.Stdout, "  wrote %s and %s (serial %s)\n", output.certPath, output.keyPath, bundle.Serial); err != nil {
		return fmt.Errorf("writing certificate status: %w", err)
	}
	if _, err := fmt.Fprintln(c.Stdout, "  WARNING: the private key is written once and is never stored server-side."); err != nil {
		return fmt.Errorf("writing certificate warning: %w", err)
	}
	return nil
}

// writeMTLSCredentialNextSteps explains the two independent trust directions
// in mTLS without reprinting any one-time private-key material. The application
// presents the generated pair to KMS; a separate operator-provided CA bundle
// lets the application verify the KMS server certificate.
func (c *CLI) writeMTLSCredentialNextSteps(output *reservedCertBundle) error {
	if output == nil {
		if _, err := fmt.Fprint(c.Stdout, `
Next steps:
  1. Save the client certificate and private key printed above now; the private key cannot be recovered.
  2. Deploy both credentials securely to the application.
  3. Configure the application with a CA bundle that trusts the operator-provided KMS server certificate.
     Do not use "parameter-store admin ca show" for server trust; that built-in CA issues client certificates.
`); err != nil {
			return fmt.Errorf("writing certificate guidance: %w", err)
		}
		return nil
	}

	if _, err := fmt.Fprintf(c.Stdout, `
Application mTLS credentials:
  client certificate: %s
  client private key: %s

Next steps:
  1. Deploy both files securely to the application.
  2. Configure the application with a CA bundle that trusts the operator-provided KMS server certificate.
     Do not use "parameter-store admin ca show" for server trust; that built-in CA issues client certificates.
`, output.certPath, output.keyPath); err != nil {
		return fmt.Errorf("writing certificate guidance: %w", err)
	}
	return nil
}

func hasAuthMethod(methods []string, want string) bool {
	return slices.Contains(methods, want)
}

// namespaceFromFlags validates and assembles a NamespaceRef from --env/--app.
func namespaceFromFlags(env, app string) (domain.NamespaceRef, error) {
	if env == "" || app == "" {
		return domain.NamespaceRef{}, fmt.Errorf("both --env and --app are required")
	}
	ns := domain.NamespaceRef{Env: env, App: app}
	if err := keyutil.ValidateNamespace(ns); err != nil {
		return domain.NamespaceRef{}, fmt.Errorf("invalid namespace: %v", err)
	}
	return ns, nil
}

// parseAuthMethods parses a comma-separated allowed-auth-methods list. Empty
// yields nil (the server defaults to mTLS-only).
func parseAuthMethods(s string) ([]string, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	var out []string
	for part := range strings.SplitSeq(s, ",") {
		m := strings.TrimSpace(part)
		if m != "mtls" && m != "token" {
			return nil, fmt.Errorf("unknown auth method %q (want mtls or token)", m)
		}
		out = append(out, m)
	}
	return out, nil
}

// authFlagToMethods maps the identity-create --auth shorthand to a wire
// auth-method list.
func authFlagToMethods(auth string) ([]string, error) {
	switch auth {
	case "mtls", "":
		return []string{"mtls"}, nil
	case "token":
		return []string{"token"}, nil
	case "both":
		return []string{"mtls", "token"}, nil
	default:
		return nil, fmt.Errorf("--auth must be mtls, token, or both")
	}
}

// parseTTLSeconds parses a certificate lifetime. It accepts a Go duration
// ("720h") or a bare "Nd" day count ("90d"). Empty yields 0 (server default).
func parseTTLSeconds(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	if days, ok := strings.CutSuffix(s, "d"); ok {
		n, err := strconv.Atoi(days)
		if err != nil || n <= 0 {
			return 0, fmt.Errorf("invalid --ttl %q", s)
		}
		return int64(n) * 24 * 3600, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("invalid --ttl %q (use e.g. 90d or 720h)", s)
	}
	return int64(d.Seconds()), nil
}
