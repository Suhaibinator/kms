package cli

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"flag"
	"fmt"
	"io"
	"os"
	"sync"
	"text/tabwriter"
	"time"

	"golang.org/x/term"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/keyutil"
)

// connFlags holds the shared gRPC connection flags for convenience and admin
// commands that talk to a running server.
type connFlags struct {
	// c supplies the environment lookup used by finalize; tests inject a map
	// so client behaviour never depends on the developer's shell.
	c    *CLI
	once sync.Once

	endpoint    string
	token       string
	secretToken string
	ca          string
	cert        string
	key         string
	insecure    bool
}

// defaultEndpoint is the server address used when neither --endpoint nor
// KMS_ENDPOINT names one.
const defaultEndpoint = "localhost:8443"

// connEnvFallback pairs a connection field with the environment variable that
// fills it when the flag was not given.
type connEnvFallback struct {
	field *string
	env   string
}

// envFallbacks lists every connection setting with its environment variable, in
// flag order. It is the single source for the fallbacks finalize applies and for
// the client-side KMS_* names, which are deliberately disjoint from the server
// settings registry in internal/config: these say which server to talk to and
// as whom, not how to run one.
func (cf *connFlags) envFallbacks() []connEnvFallback {
	return []connEnvFallback{
		{&cf.endpoint, "KMS_ENDPOINT"},
		{&cf.token, "KMS_TOKEN"},
		{&cf.ca, "KMS_CA_FILE"},
		{&cf.cert, "KMS_CLIENT_CERT_FILE"},
		{&cf.key, "KMS_CLIENT_KEY_FILE"},
	}
}

// addConnFlags registers the shared connection flags on fs. Every flag gets a
// literal empty default and names its environment variable in the usage text
// rather than defaulting to the variable's value: flag help prints non-empty
// string defaults, so a KMS_TOKEN-derived default would write the caller's
// bearer token to stderr on any "<command> -h". finalize applies the fallbacks
// after parsing instead.
func addConnFlags(c *CLI, fs *flag.FlagSet) *connFlags {
	cf := &connFlags{c: c}
	fs.StringVar(&cf.endpoint, "endpoint", "", "server gRPC `endpoint` host:port (env KMS_ENDPOINT; default "+defaultEndpoint+")")
	fs.StringVar(&cf.token, "token", "", "identity bearer `token` (env KMS_TOKEN)")
	fs.BoolVar(&cf.insecure, "insecure", false, "disable TLS (development only)")
	fs.StringVar(&cf.ca, "ca", "", "CA bundle `file` for verifying the server (env KMS_CA_FILE); this is the client-side trust store, not the server's client_ca_file")
	fs.StringVar(&cf.cert, "cert", "", "client certificate `file` for mTLS (env KMS_CLIENT_CERT_FILE)")
	fs.StringVar(&cf.key, "key", "", "client private key `file` for mTLS (env KMS_CLIENT_KEY_FILE)")
	return cf
}

// finalize resolves each connection setting to flag, then environment, then
// built-in default. Flags are parsed before either caller runs, so an explicitly
// set flag always wins. dial and authCtx call it, which keeps every command site
// unchanged; sync.Once makes the repeated calls free and idempotent.
func (cf *connFlags) finalize() {
	cf.once.Do(func() {
		for _, fallback := range cf.envFallbacks() {
			if *fallback.field != "" {
				continue
			}
			if v, ok := cf.c.env(fallback.env); ok {
				*fallback.field = v
			}
		}
		if cf.endpoint == "" {
			cf.endpoint = defaultEndpoint
		}
	})
}

func (cf *connFlags) dial() (*grpc.ClientConn, error) {
	cf.finalize()
	var creds credentials.TransportCredentials
	if cf.insecure {
		creds = insecure.NewCredentials()
	} else {
		tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}
		if cf.ca != "" {
			pem, err := os.ReadFile(cf.ca)
			if err != nil {
				return nil, fmt.Errorf("reading --ca: %w", err)
			}
			pool := x509.NewCertPool()
			if !pool.AppendCertsFromPEM(pem) {
				return nil, fmt.Errorf("no certificates found in --ca %s", cf.ca)
			}
			tlsCfg.RootCAs = pool
		}
		if cf.cert != "" || cf.key != "" {
			pair, err := tls.LoadX509KeyPair(cf.cert, cf.key)
			if err != nil {
				return nil, fmt.Errorf("loading client key pair: %w", err)
			}
			tlsCfg.Certificates = []tls.Certificate{pair}
		}
		creds = credentials.NewTLS(tlsCfg)
	}
	return grpc.NewClient(cf.endpoint, grpc.WithTransportCredentials(creds))
}

// authCtx attaches the identity token and optional per-secret token as gRPC
// metadata, matching the server's expected header names. mTLS callers omit the
// bearer token; the server derives their identity from the client certificate.
func (cf *connFlags) authCtx(ctx context.Context) context.Context {
	cf.finalize()
	var kvs []string
	if cf.token != "" {
		kvs = append(kvs, "authorization", "Bearer "+cf.token)
	}
	if cf.secretToken != "" {
		kvs = append(kvs, "x-kms-secret-token", cf.secretToken)
	}
	if len(kvs) == 0 {
		return ctx
	}
	return metadata.AppendToOutgoingContext(ctx, kvs...)
}

func callContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 30*time.Second)
}

// protoRef builds a wire ResourceRef from a domain Ref. The CLI is a client, so
// it may resolve the "/env/app/key" display form to explicit fields locally.
func protoRef(ref domain.Ref) *kmsv1.ResourceRef {
	return &kmsv1.ResourceRef{
		Namespace: &kmsv1.NamespaceRef{Env: ref.NS.Env, App: ref.NS.App},
		Key:       ref.Key,
	}
}

// displayPath renders a wire ResourceRef as "/env/app/key" for output.
func displayPath(ref *kmsv1.ResourceRef) string {
	if ref == nil {
		return ""
	}
	ns := ref.GetNamespace()
	return domain.Ref{
		NS:  domain.NamespaceRef{Env: ns.GetEnv(), App: ns.GetApp()},
		Key: ref.GetKey(),
	}.String()
}

// --- put-secret ------------------------------------------------------------

func (c *CLI) cmdPutSecret(args []string) int {
	fs := c.newFlags("put-secret")
	cf := addConnFlags(c, fs)
	valueFile := fs.String("value-file", "", "read the secret value from this `file` (default: stdin)")
	clientBound := fs.Bool("client-bound", false, "write a client-bound secret (new secrets also require --generate-token)")
	genToken := fs.Bool("generate-token", false, "mint or rotate a per-secret access token (shown once)")
	contentType := fs.String("content-type", "text/plain", "secret content `type`")
	fs.StringVar(&cf.secretToken, "secret-token", "", "existing per-secret `token` (client-bound updates)")
	c.setUsage(fs, "put-secret /env/app/key [flags]",
		"Store a secret value read from --value-file or standard input.", false)
	if !c.parseFlags(fs, args) {
		return 2
	}
	pos := c.args()
	if len(pos) < 1 || pos[0] == "" {
		return c.fail("put-secret requires a /env/app/key argument")
	}
	ref, err := keyutil.SplitDisplayPath(pos[0])
	if err != nil {
		return c.fail("invalid path: %v", err)
	}
	value, err := c.readValue(*valueFile)
	if err != nil {
		return c.fail("reading value: %v", err)
	}

	conn, err := cf.dial()
	if err != nil {
		return c.fail("%v", err)
	}
	defer func() { _ = conn.Close() }()
	ctx, cancel := callContext()
	defer cancel()

	resp, err := kmsv1.NewSecretServiceClient(conn).PutSecret(cf.authCtx(ctx), &kmsv1.PutSecretRequest{
		Ref:                 protoRef(ref),
		Value:               value,
		ContentType:         *contentType,
		ClientBound:         *clientBound,
		GenerateAccessToken: *genToken,
	})
	if err != nil {
		return c.fail("put-secret: %v", err)
	}
	if _, err := fmt.Fprintf(c.Stdout, "Stored %s version %d (revision %d)\n", ref, resp.Version, resp.Revision); err != nil {
		return c.fail("writing secret result: %v", err)
	}
	if resp.AccessToken != "" {
		if _, err := fmt.Fprintf(c.Stdout, "  access token: %s\n", resp.AccessToken); err != nil {
			return c.fail("writing one-time secret access token: %v", err)
		}
		if _, err := fmt.Fprintln(c.Stdout, "  WARNING: shown once; store it now."); err != nil {
			return c.fail("writing one-time secret access token warning: %v", err)
		}
	}
	return 0
}

// --- get-secret ------------------------------------------------------------

func (c *CLI) cmdGetSecret(args []string) int {
	fs := c.newFlags("get-secret")
	cf := addConnFlags(c, fs)
	show := fs.Bool("show", false, "allow printing the secret to a terminal")
	out := fs.String("out", "", "write the secret to this `file` instead of stdout")
	version := fs.Uint64("version", 0, "specific `version` (0 = current label)")
	label := fs.String("label", "", "version `label` (default: current)")
	fs.StringVar(&cf.secretToken, "secret-token", "", "per-secret access `token`")
	c.setUsage(fs, "get-secret /env/app/key [flags]",
		"Fetch a secret; writing it to a terminal requires --show or --out FILE.", false)
	if !c.parseFlags(fs, args) {
		return 2
	}
	pos := c.args()
	if len(pos) < 1 || pos[0] == "" {
		return c.fail("get-secret requires a /env/app/key argument")
	}
	ref, err := keyutil.SplitDisplayPath(pos[0])
	if err != nil {
		return c.fail("invalid path: %v", err)
	}

	conn, err := cf.dial()
	if err != nil {
		return c.fail("%v", err)
	}
	defer func() { _ = conn.Close() }()
	ctx, cancel := callContext()
	defer cancel()

	resp, err := kmsv1.NewSecretServiceClient(conn).GetSecret(cf.authCtx(ctx), &kmsv1.GetSecretRequest{
		Ref:     protoRef(ref),
		Version: *version,
		Label:   *label,
	})
	if err != nil {
		return c.fail("get-secret: %v", err)
	}

	switch {
	case *out != "":
		if err := os.WriteFile(*out, resp.Value, 0o600); err != nil {
			return c.fail("writing --out: %v", err)
		}
		_, _ = fmt.Fprintf(c.Stderr, "Wrote %d bytes to %s\n", len(resp.Value), *out)
	case *show || !c.stdoutIsTTY():
		// Piped or explicitly allowed: emit raw bytes with no trailing newline.
		if _, err := c.Stdout.Write(resp.Value); err != nil {
			return c.fail("writing output: %v", err)
		}
	default:
		return c.fail("refusing to print a secret to a terminal; pass --show to print or --out FILE to save")
	}
	return 0
}

// --- put-parameter ---------------------------------------------------------

func (c *CLI) cmdPutParameter(args []string) int {
	fs := c.newFlags("put-parameter")
	cf := addConnFlags(c, fs)
	contentType := fs.String("content-type", "string", "parameter content `type`")
	c.setUsage(fs, "put-parameter /env/app/key VALUE [flags]",
		"Store a non-secret parameter value.", false)
	if !c.parseFlags(fs, args) {
		return 2
	}
	pos := c.args()
	if len(pos) < 2 || pos[0] == "" {
		return c.fail("put-parameter requires /env/app/key and VALUE arguments")
	}
	ref, err := keyutil.SplitDisplayPath(pos[0])
	if err != nil {
		return c.fail("invalid path: %v", err)
	}
	value := pos[1]

	conn, err := cf.dial()
	if err != nil {
		return c.fail("%v", err)
	}
	defer func() { _ = conn.Close() }()
	ctx, cancel := callContext()
	defer cancel()

	resp, err := kmsv1.NewParameterServiceClient(conn).PutParameter(cf.authCtx(ctx), &kmsv1.PutParameterRequest{
		Ref:         protoRef(ref),
		Value:       value,
		ContentType: *contentType,
	})
	if err != nil {
		return c.fail("put-parameter: %v", err)
	}
	_, _ = fmt.Fprintf(c.Stdout, "Stored %s version %d (revision %d)\n", ref, resp.Version, resp.Revision)
	return 0
}

// --- list ------------------------------------------------------------------

func (c *CLI) cmdList(args []string) int {
	fs := c.newFlags("list")
	cf := addConnFlags(c, fs)
	keyPrefix := fs.String("prefix", "", "relative key `prefix` within the namespace")
	c.setUsage(fs, "list ENV/APP [flags]", "List the parameters and secrets in a namespace.", false)
	if !c.parseFlags(fs, args) {
		return 2
	}
	pos := c.args()
	if len(pos) < 1 || pos[0] == "" {
		return c.fail("list requires an env/app namespace argument")
	}
	ns, err := keyutil.ParseNamespace(pos[0])
	if err != nil {
		return c.fail("invalid namespace: %v", err)
	}
	pns := &kmsv1.NamespaceRef{Env: ns.Env, App: ns.App}

	conn, err := cf.dial()
	if err != nil {
		return c.fail("%v", err)
	}
	defer func() { _ = conn.Close() }()
	ctx, cancel := callContext()
	defer cancel()
	actx := cf.authCtx(ctx)

	paramClient := kmsv1.NewParameterServiceClient(conn)
	secretClient := kmsv1.NewSecretServiceClient(conn)

	tw := tabwriter.NewWriter(c.Stdout, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "TYPE\tPATH\tCURRENT\tNOTE")

	// Page through the full result set; a listing command that silently
	// truncated at the first page would give a misleading partial view.
	for token := ""; ; {
		resp, err := paramClient.ListParameters(actx, &kmsv1.ListParametersRequest{Namespace: pns, KeyPrefix: *keyPrefix, PageToken: token})
		if err != nil {
			return c.fail("list parameters: %v", err)
		}
		for _, p := range resp.Parameters {
			_, _ = fmt.Fprintf(tw, "parameter\t%s\t%d\t%s\n", displayPath(p.Ref), p.Version, p.ContentType)
		}
		if token = resp.NextPageToken; token == "" {
			break
		}
	}
	for token := ""; ; {
		resp, err := secretClient.ListSecrets(actx, &kmsv1.ListSecretsRequest{Namespace: pns, KeyPrefix: *keyPrefix, PageToken: token})
		if err != nil {
			return c.fail("list secrets: %v", err)
		}
		for _, s := range resp.Secrets {
			note := "standard"
			if s.ClientBound {
				note = "client-bound"
			}
			_, _ = fmt.Fprintf(tw, "secret\t%s\t%d\t%s\n", displayPath(s.Ref), s.Labels["current"], note)
		}
		if token = resp.NextPageToken; token == "" {
			break
		}
	}
	_ = tw.Flush()
	return 0
}

// --- helpers ---------------------------------------------------------------

// readValue reads a secret value from a file, or from stdin when file is empty.
func (c *CLI) readValue(file string) ([]byte, error) {
	if file != "" {
		return os.ReadFile(file)
	}
	if c.Stdin == nil {
		return nil, fmt.Errorf("no --value-file and no stdin available")
	}
	return io.ReadAll(c.Stdin)
}

// stdoutIsTTY reports whether output is an interactive terminal. A non-*os.File
// writer (e.g. a test buffer) is treated as non-interactive.
func (c *CLI) stdoutIsTTY() bool {
	f, ok := c.Stdout.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}
