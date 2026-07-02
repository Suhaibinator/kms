package cli

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"flag"
	"fmt"
	"io"
	"os"
	"text/tabwriter"
	"time"

	"golang.org/x/term"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
)

// connFlags holds the shared gRPC connection flags for convenience commands.
type connFlags struct {
	endpoint    string
	token       string
	secretToken string
	ca          string
	cert        string
	key         string
	insecure    bool
}

func addConnFlags(fs *flag.FlagSet) *connFlags {
	cf := &connFlags{}
	fs.StringVar(&cf.endpoint, "endpoint", "localhost:8443", "server gRPC endpoint (host:port)")
	fs.StringVar(&cf.token, "token", os.Getenv("KMS_TOKEN"), "identity bearer token (env KMS_TOKEN)")
	fs.BoolVar(&cf.insecure, "insecure", false, "disable TLS (development only)")
	fs.StringVar(&cf.ca, "ca", "", "CA bundle for server verification")
	fs.StringVar(&cf.cert, "cert", "", "client certificate for mTLS")
	fs.StringVar(&cf.key, "key", "", "client private key for mTLS")
	return cf
}

func (cf *connFlags) dial() (*grpc.ClientConn, error) {
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
// metadata, matching the server's expected header names.
func (cf *connFlags) authCtx(ctx context.Context) context.Context {
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

// --- put-secret ------------------------------------------------------------

func (c *CLI) cmdPutSecret(args []string) int {
	fs := c.newFlags("put-secret")
	cf := addConnFlags(fs)
	valueFile := fs.String("value-file", "", "read the secret value from this file (default: stdin)")
	clientBound := fs.Bool("client-bound", false, "create a client-bound secret")
	genToken := fs.Bool("generate-token", false, "mint a per-secret access token (shown once)")
	contentType := fs.String("content-type", "text/plain", "secret content type")
	fs.StringVar(&cf.secretToken, "secret-token", "", "existing per-secret token (client-bound updates)")
	if !c.parseFlags(fs, args) {
		return 2
	}
	pos := c.args()
	if len(pos) < 1 || pos[0] == "" {
		return c.fail("put-secret requires a PATH argument")
	}
	path := pos[0]
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
		Path:                path,
		Value:               value,
		ContentType:         *contentType,
		ClientBound:         *clientBound,
		GenerateAccessToken: *genToken,
	})
	if err != nil {
		return c.fail("put-secret: %v", err)
	}
	_, _ = fmt.Fprintf(c.Stdout, "Stored %s version %d (revision %d)\n", path, resp.Version, resp.Revision)
	if resp.AccessToken != "" {
		_, _ = fmt.Fprintf(c.Stdout, "  access token: %s\n", resp.AccessToken)
		_, _ = fmt.Fprintln(c.Stdout, "  WARNING: shown once; store it now.")
	}
	return 0
}

// --- get-secret ------------------------------------------------------------

func (c *CLI) cmdGetSecret(args []string) int {
	fs := c.newFlags("get-secret")
	cf := addConnFlags(fs)
	show := fs.Bool("show", false, "allow printing the secret to a terminal")
	out := fs.String("out", "", "write the secret to this file instead of stdout")
	version := fs.Uint64("version", 0, "specific version (0 = current label)")
	label := fs.String("label", "", "version label (default: current)")
	fs.StringVar(&cf.secretToken, "secret-token", "", "per-secret access token")
	if !c.parseFlags(fs, args) {
		return 2
	}
	pos := c.args()
	if len(pos) < 1 || pos[0] == "" {
		return c.fail("get-secret requires a PATH argument")
	}
	path := pos[0]

	conn, err := cf.dial()
	if err != nil {
		return c.fail("%v", err)
	}
	defer func() { _ = conn.Close() }()
	ctx, cancel := callContext()
	defer cancel()

	resp, err := kmsv1.NewSecretServiceClient(conn).GetSecret(cf.authCtx(ctx), &kmsv1.GetSecretRequest{
		Path:    path,
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
	cf := addConnFlags(fs)
	contentType := fs.String("content-type", "string", "parameter content type")
	if !c.parseFlags(fs, args) {
		return 2
	}
	pos := c.args()
	if len(pos) < 2 || pos[0] == "" {
		return c.fail("put-parameter requires PATH and VALUE arguments")
	}
	path, value := pos[0], pos[1]

	conn, err := cf.dial()
	if err != nil {
		return c.fail("%v", err)
	}
	defer func() { _ = conn.Close() }()
	ctx, cancel := callContext()
	defer cancel()

	resp, err := kmsv1.NewParameterServiceClient(conn).PutParameter(cf.authCtx(ctx), &kmsv1.PutParameterRequest{
		Path:        path,
		Value:       value,
		ContentType: *contentType,
	})
	if err != nil {
		return c.fail("put-parameter: %v", err)
	}
	_, _ = fmt.Fprintf(c.Stdout, "Stored %s version %d (revision %d)\n", path, resp.Version, resp.Revision)
	return 0
}

// --- list ------------------------------------------------------------------

func (c *CLI) cmdList(args []string) int {
	fs := c.newFlags("list")
	cf := addConnFlags(fs)
	if !c.parseFlags(fs, args) {
		return 2
	}
	var prefix string
	if pos := c.args(); len(pos) > 0 {
		prefix = pos[0]
	}

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
		resp, err := paramClient.ListParameters(actx, &kmsv1.ListParametersRequest{PathPrefix: prefix, PageToken: token})
		if err != nil {
			return c.fail("list parameters: %v", err)
		}
		for _, p := range resp.Parameters {
			_, _ = fmt.Fprintf(tw, "parameter\t%s\t%d\t%s\n", p.Path, p.Version, p.ContentType)
		}
		if token = resp.NextPageToken; token == "" {
			break
		}
	}
	for token := ""; ; {
		resp, err := secretClient.ListSecrets(actx, &kmsv1.ListSecretsRequest{PathPrefix: prefix, PageToken: token})
		if err != nil {
			return c.fail("list secrets: %v", err)
		}
		for _, s := range resp.Secrets {
			note := "standard"
			if s.ClientBound {
				note = "client-bound"
			}
			_, _ = fmt.Fprintf(tw, "secret\t%s\t%d\t%s\n", s.Path, s.Labels["current"], note)
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
