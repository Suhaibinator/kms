package cli

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/term"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/fileutil"
	"github.com/Suhaibinator/kms/internal/keyutil"
)

// connFlags holds the shared gRPC connection flags for convenience and admin
// commands that talk to a running server.
type connFlags struct {
	// c supplies the environment lookup used by finalize; tests inject a map
	// so client behaviour never depends on the developer's shell.
	c    *CLI
	once sync.Once

	endpoint        string
	token           string
	tokenFile       string
	secretToken     string
	secretTokenFile string
	// secretTokenFlags records that addSecretTokenFlags ran, which is what
	// makes KMS_SECRET_TOKEN_FILE apply: a per-secret token belongs only on
	// the RPCs of the two commands that read or update a token-gated secret,
	// never on every call a shell with the variable exported happens to make.
	secretTokenFlags bool
	ca               string
	cert             string
	key              string
	insecure         bool
	// finalizeErr is the error finalize produced, replayed by later callers
	// (sync.Once runs the body only once).
	finalizeErr error
}

// defaultEndpoint is the server address used when neither --endpoint nor
// KMS_ENDPOINT names one.
const defaultEndpoint = "localhost:8443"

// connEnvFallback pairs a connection field with the environment variable that
// fills it when the flag was not given. A variable whose flag the command did
// not register is ignored rather than applied.
type connEnvFallback struct {
	field      *string
	env        string
	registered bool
}

// envFallbacks lists every connection setting with its environment variable, in
// flag order. It is the single source for the fallbacks finalize applies and for
// the client-side KMS_* names, which are deliberately disjoint from the server
// settings registry in internal/config: these say which server to talk to and
// as whom, not how to run one.
func (cf *connFlags) envFallbacks() []connEnvFallback {
	return []connEnvFallback{
		{&cf.endpoint, "KMS_ENDPOINT", true},
		{&cf.token, "KMS_TOKEN", true},
		{&cf.tokenFile, "KMS_TOKEN_FILE", true},
		{&cf.secretTokenFile, "KMS_SECRET_TOKEN_FILE", cf.secretTokenFlags},
		{&cf.ca, "KMS_CA_FILE", true},
		{&cf.cert, "KMS_CLIENT_CERT_FILE", true},
		{&cf.key, "KMS_CLIENT_KEY_FILE", true},
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
	fs.StringVar(&cf.token, "token", "", "identity bearer `token` (env KMS_TOKEN); visible to other local users in the process list, prefer --token-file")
	fs.StringVar(&cf.tokenFile, "token-file", "", "read the identity bearer token from this private `file` (env KMS_TOKEN_FILE)")
	fs.BoolVar(&cf.insecure, "insecure", false, "disable TLS (development only)")
	fs.StringVar(&cf.ca, "ca", "", "CA bundle `file` for verifying the server (env KMS_CA_FILE); this is the client-side trust store, not the server's client_ca_file")
	fs.StringVar(&cf.cert, "cert", "", "client certificate `file` for mTLS (env KMS_CLIENT_CERT_FILE)")
	fs.StringVar(&cf.key, "key", "", "client private key `file` for mTLS (env KMS_CLIENT_KEY_FILE)")
	return cf
}

// addSecretTokenFlags registers the per-secret token flags for commands that
// read or update token-gated secrets.
func addSecretTokenFlags(fs *flag.FlagSet, cf *connFlags, usage string) {
	cf.secretTokenFlags = true
	fs.StringVar(&cf.secretToken, "secret-token", "", usage+" (visible in the process list, prefer --secret-token-file)")
	fs.StringVar(&cf.secretTokenFile, "secret-token-file", "", "read the per-secret token from this private `file` (env KMS_SECRET_TOKEN_FILE)")
}

// finalize resolves each connection setting to flag, then environment, then
// built-in default, and loads token files. Flags are parsed before either
// caller runs, so an explicitly set flag always wins. dial and authCtx call
// it; sync.Once makes the repeated calls free and idempotent, and the first
// error is replayed to every caller.
//
// A token given both inline and as a file is a usage error rather than a
// precedence question: the two sources come from different places (a shell
// history or CI variable versus a mounted credential file) and silently
// picking one would let a stale inline token shadow a rotated file.
func (cf *connFlags) finalize() error {
	cf.once.Do(func() {
		for _, fallback := range cf.envFallbacks() {
			if !fallback.registered || *fallback.field != "" {
				continue
			}
			if v, ok := cf.c.env(fallback.env); ok {
				*fallback.field = v
			}
		}
		if cf.endpoint == "" {
			cf.endpoint = defaultEndpoint
		}
		if cf.token != "" && cf.tokenFile != "" {
			cf.finalizeErr = usageError("--token and --token-file (or KMS_TOKEN and KMS_TOKEN_FILE) are mutually exclusive")
			return
		}
		if cf.secretToken != "" && cf.secretTokenFile != "" {
			cf.finalizeErr = usageError("--secret-token and --secret-token-file (or KMS_SECRET_TOKEN_FILE) are mutually exclusive")
			return
		}
		if cf.tokenFile != "" {
			tok, err := readTokenFile(cf.tokenFile)
			if err != nil {
				cf.finalizeErr = fmt.Errorf("--token-file: %w", err)
				return
			}
			cf.token = tok
		}
		if cf.secretTokenFile != "" {
			tok, err := readTokenFile(cf.secretTokenFile)
			if err != nil {
				cf.finalizeErr = fmt.Errorf("--secret-token-file: %w", err)
				return
			}
			cf.secretToken = tok
		}
	})
	return cf.finalizeErr
}

// usageError marks an error that should exit with the usage code.
type usageError string

func (e usageError) Error() string { return string(e) }

// readTokenFile reads a bearer token from a file that must already be
// private to the current user (no group/other bits, owner-only, a regular
// file, not a symlink); the file is opened read-only and never modified, so a
// 0400 credential on a read-only mount works. One trailing newline is
// tolerated because editors add it; any other whitespace or an empty file is
// rejected so a truncated or misnamed file cannot silently turn into an
// anonymous call.
func readTokenFile(path string) (string, error) {
	raw, err := fileutil.ReadPrivateFile(path)
	if err != nil {
		return "", err
	}
	tok := strings.TrimSuffix(strings.TrimSuffix(string(raw), "\n"), "\r")
	if tok == "" {
		return "", fmt.Errorf("%s is empty", path)
	}
	if strings.ContainsAny(tok, " \t\r\n") {
		return "", fmt.Errorf("%s must contain exactly one token", path)
	}
	return tok, nil
}

func (cf *connFlags) dial() (*grpc.ClientConn, error) {
	if err := cf.finalize(); err != nil {
		return nil, err
	}
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
	return grpc.NewClient(cf.endpoint,
		grpc.WithTransportCredentials(creds),
		grpc.WithUserAgent("parameter-store-cli/"+Version),
	)
}

// authCtx attaches the standard identity token as gRPC metadata. Per-secret
// credentials are fields on only the requests that consume them. mTLS callers
// omit the bearer token; the server derives their identity from the client
// certificate.
func (cf *connFlags) authCtx(ctx context.Context) context.Context {
	// dial has already surfaced any finalize error, and every caller returns
	// on it before reaching here; the tokens are whatever finalize left.
	_ = cf.finalize()
	var kvs []string
	if cf.token != "" {
		kvs = append(kvs, "authorization", "Bearer "+cf.token)
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

// --- whoami ----------------------------------------------------------------

// whoAmIJSON is the JSON form of the calling identity as the server resolved
// it from the presented credential.
type whoAmIJSON struct {
	Name       string            `json:"name"`
	Kind       string            `json:"kind"`
	Namespace  *namespaceRefJSON `json:"namespace"`
	AuthMethod string            `json:"auth_method"`
}

// cmdWhoAmI reports the identity the server derives from the credential this
// invocation presents. It is the first command to run when a token or client
// certificate does not behave as expected: it answers "who does the server
// think I am, and how did it decide?" without needing any permission.
func (c *CLI) cmdWhoAmI(args []string) int {
	fs := c.newFlags("whoami")
	cf := addConnFlags(c, fs)
	c.setUsage(fs, "whoami [flags]",
		"Print the identity the server resolves from the presented credential: its name, kind, namespace binding, and the authentication method that was used.", false)
	if !c.parseFlags(fs, args) {
		return 2
	}
	if !c.rejectPositionals() {
		return 2
	}

	conn, err := c.dialConn(cf)
	if err != nil {
		return c.failErr("", err)
	}
	defer func() { _ = conn.Close() }()
	ctx, cancel := callContext()
	defer cancel()

	resp, err := kmsv1.NewAdminServiceClient(conn).WhoAmI(cf.authCtx(ctx), &kmsv1.WhoAmIRequest{})
	if err != nil {
		return c.failErr("whoami", err)
	}
	if c.jsonOutput() {
		return c.printJSON(whoAmIJSON{
			Name:       resp.GetName(),
			Kind:       resp.GetKind(),
			Namespace:  namespaceRefToJSON(resp.GetNamespace()),
			AuthMethod: resp.GetAuthMethod(),
		})
	}
	// "(unbound)" rather than an empty field: a blank namespace line reads like
	// output the command failed to fill in.
	namespace := "(unbound)"
	if ns := resp.GetNamespace(); ns != nil {
		namespace = ns.GetEnv() + "/" + ns.GetApp()
	}
	_, _ = fmt.Fprintf(c.Stdout, "name: %s\nkind: %s\nnamespace: %s\nauth_method: %s\n",
		resp.GetName(), resp.GetKind(), namespace, resp.GetAuthMethod())
	return 0
}

// --- put-secret ------------------------------------------------------------

func (c *CLI) cmdPutSecret(args []string) int {
	fs := c.newFlags("put-secret")
	cf := addConnFlags(c, fs)
	valueFile := fs.String("value-file", "", "read the secret value from this `file` (default: stdin)")
	genToken := fs.Bool("generate-token", false, "mint or rotate a per-secret access token (shown once)")
	contentType := fs.String("content-type", "text/plain", "secret content `type`")
	c.setUsage(fs, "put-secret /env/app/key [flags]",
		"Store a secret value read from --value-file or standard input. KMS_BINDING_KEY binds the new version when set.", false)
	if !c.parseFlags(fs, args) {
		return 2
	}
	pos := c.args()
	if len(pos) < 1 || pos[0] == "" {
		return c.failUsage("put-secret requires a /env/app/key argument")
	}
	if !c.rejectExtraPositionals(1) {
		return 2
	}
	ref, err := keyutil.SplitDisplayPath(pos[0])
	if err != nil {
		return c.failUsage("invalid path: %v", err)
	}
	bindingKey := ""
	if key, ok := c.env(bindingKeyEnv); ok && key != "" {
		if err := validateBindingKeyInput(key); err != nil {
			return c.failUsage("put-secret: %s: %v", bindingKeyEnv, err)
		}
		bindingKey = key
	}
	value, err := c.readValue(*valueFile)
	if err != nil {
		return c.fail("reading value: %v", err)
	}

	conn, err := c.dialConn(cf)
	if err != nil {
		return c.failErr("", err)
	}
	defer func() { _ = conn.Close() }()
	ctx, cancel := callContext()
	defer cancel()

	resp, err := kmsv1.NewSecretServiceClient(conn).PutSecret(cf.authCtx(ctx), &kmsv1.PutSecretRequest{
		Ref:                 protoRef(ref),
		Value:               value,
		ContentType:         *contentType,
		BindingKey:          bindingKey,
		GenerateAccessToken: *genToken,
	})
	if err != nil {
		return c.failSecretRPC("put-secret", err)
	}
	if c.jsonOutput() {
		// The one-time warning is security-relevant, so it goes to stderr
		// unsilenced while the token itself appears once inside the document.
		if resp.AccessToken != "" {
			_, _ = fmt.Fprintln(c.Stderr, "WARNING: the access token is shown once; store it now.")
		}
		return c.printJSON(putSecretJSON{
			Key:         ref.String(),
			Version:     resp.Version,
			Revision:    resp.Revision,
			AccessToken: resp.AccessToken,
		})
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

// putSecretJSON is the JSON form of a stored secret version. access_token is
// present only when --generate-token minted one, and only ever here.
type putSecretJSON struct {
	Key         string `json:"key"`
	Version     uint64 `json:"version"`
	Revision    uint64 `json:"revision"`
	AccessToken string `json:"access_token,omitempty"`
}

// --- get-secret ------------------------------------------------------------

func (c *CLI) cmdGetSecret(args []string) int {
	fs := c.newFlags("get-secret")
	cf := addConnFlags(c, fs)
	show := fs.Bool("show", false, "allow printing the secret to a terminal")
	out := fs.String("out", "", "write the secret to this `file` instead of stdout")
	version := fs.Uint64("version", 0, "specific `version` (0 = current label)")
	label := fs.String("label", "", "version `label` (default: current)")
	addSecretTokenFlags(fs, cf, "per-secret access `token`")
	c.setUsage(fs, "get-secret /env/app/key [flags]",
		"Fetch a secret; writing it to a terminal requires --show or --out FILE.", false)
	if !c.parseFlags(fs, args) {
		return 2
	}
	pos := c.args()
	if len(pos) < 1 || pos[0] == "" {
		return c.failUsage("get-secret requires a /env/app/key argument")
	}
	if !c.rejectExtraPositionals(1) {
		return 2
	}
	ref, err := keyutil.SplitDisplayPath(pos[0])
	if err != nil {
		return c.failUsage("invalid path: %v", err)
	}
	if *version != 0 && *label != "" {
		return c.failUsage("get-secret: --version and --label are mutually exclusive")
	}

	conn, err := c.dialConn(cf)
	if err != nil {
		return c.failErr("", err)
	}
	defer func() { _ = conn.Close() }()

	client := kmsv1.NewSecretServiceClient(conn)
	metadataLabel := *label
	if *version == 0 && metadataLabel == "" {
		metadataLabel = "current"
	}
	metadataCtx, cancelMetadata := callContext()
	metadataResp, err := client.GetSecretMetadata(cf.authCtx(metadataCtx), &kmsv1.GetSecretMetadataRequest{Ref: protoRef(ref), Version: *version, Label: metadataLabel})
	cancelMetadata()
	if err != nil {
		return c.failErr("get-secret", err)
	}
	metadata := metadataResp.GetSecret()
	if metadata == nil || !sameRef(metadata.GetRef(), protoRef(ref)) {
		return c.fail("get-secret metadata: server returned a different resource")
	}
	selected, err := selectSecretVersion(metadata, *version, *label)
	if err != nil {
		return c.fail("get-secret metadata: %v", err)
	}
	bindingKey := ""
	if selected.GetBound() {
		bindingKey, err = c.requiredBindingKey(bindingKeyEnv, "Binding key for "+ref.String()+": ", false)
		if err != nil {
			return c.failUsage("get-secret: %v", err)
		}
	}
	readCtx, cancelRead := callContext()
	defer cancelRead()
	resp, err := client.GetSecret(cf.authCtx(readCtx), &kmsv1.GetSecretRequest{
		Ref:         protoRef(ref),
		Version:     selected.GetVersion(),
		SecretToken: cf.secretToken,
		BindingKey:  bindingKey,
	})
	if err != nil {
		return c.failSecretRPC("get-secret", err)
	}
	if resp.GetVersion() != selected.GetVersion() || !sameRef(resp.GetRef(), protoRef(ref)) {
		return c.fail("get-secret: server returned a different secret version")
	}

	// The destination rules are the same in both output modes: --out wins, a
	// terminal still needs --show, and only then does the value leave the
	// process. JSON mode differs solely in how the result is rendered.
	document := getSecretJSON{
		Key:         displayPath(resp.Ref),
		Version:     resp.Version,
		ContentType: resp.ContentType,
		CreatedAt:   jsonTime(resp.CreatedAtUnixMs),
	}
	if document.Key == "" {
		document.Key = ref.String()
	}
	switch {
	case *out != "":
		if err := os.WriteFile(*out, resp.Value, 0o600); err != nil {
			return c.failErr("writing --out", err)
		}
		c.info("Wrote %d bytes to %s", len(resp.Value), *out)
		document.OutFile = *out
	case *show || !c.stdoutIsTTY():
		if c.jsonOutput() {
			// A secret that is not valid UTF-8 has no JSON string form; --out
			// saves it verbatim instead of corrupting it.
			if !utf8.Valid(resp.Value) {
				return c.fail("secret value is not valid UTF-8 and cannot be rendered as JSON; use --out FILE")
			}
			value := string(resp.Value)
			document.Value = &value
			break
		}
		// Piped or explicitly allowed: emit raw bytes with no trailing newline.
		if _, err := c.Stdout.Write(resp.Value); err != nil {
			return c.fail("writing output: %v", err)
		}
	default:
		return c.fail("refusing to print a secret to a terminal; pass --show to print or --out FILE to save")
	}
	if c.jsonOutput() {
		return c.printJSON(document)
	}
	return 0
}

// getSecretJSON is the JSON form of a fetched secret. value is null when the
// bytes went to --out instead of stdout, in which case out_file names the file
// that now holds them.
type getSecretJSON struct {
	Key         string  `json:"key"`
	Version     uint64  `json:"version"`
	Value       *string `json:"value"`
	ContentType string  `json:"content_type"`
	CreatedAt   *string `json:"created_at"`
	OutFile     string  `json:"out_file,omitempty"`
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
		return c.failUsage("put-parameter requires /env/app/key and VALUE arguments")
	}
	if !c.rejectExtraPositionals(2) {
		return 2
	}
	ref, err := keyutil.SplitDisplayPath(pos[0])
	if err != nil {
		return c.failUsage("invalid path: %v", err)
	}
	value := pos[1]

	conn, err := c.dialConn(cf)
	if err != nil {
		return c.failErr("", err)
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
		return c.failErr("put-parameter", err)
	}
	if c.jsonOutput() {
		return c.printJSON(putParameterJSON{Key: ref.String(), Version: resp.Version, Revision: resp.Revision})
	}
	_, _ = fmt.Fprintf(c.Stdout, "Stored %s version %d (revision %d)\n", ref, resp.Version, resp.Revision)
	return 0
}

// putParameterJSON is the JSON form of a stored parameter version.
type putParameterJSON struct {
	Key      string `json:"key"`
	Version  uint64 `json:"version"`
	Revision uint64 `json:"revision"`
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
		return c.failUsage("list requires an env/app namespace argument")
	}
	if !c.rejectExtraPositionals(1) {
		return 2
	}
	ns, err := keyutil.ParseNamespace(pos[0])
	if err != nil {
		return c.failUsage("invalid namespace: %v", err)
	}
	pns := &kmsv1.NamespaceRef{Env: ns.Env, App: ns.App}

	conn, err := c.dialConn(cf)
	if err != nil {
		return c.failErr("", err)
	}
	defer func() { _ = conn.Close() }()
	ctx, cancel := callContext()
	defer cancel()
	actx := cf.authCtx(ctx)

	paramClient := kmsv1.NewParameterServiceClient(conn)
	secretClient := kmsv1.NewSecretServiceClient(conn)

	items := []listItemJSON{}

	// Page through the full result set; a listing command that silently
	// truncated at the first page would give a misleading partial view.
	for token := ""; ; {
		resp, err := paramClient.ListParameters(actx, &kmsv1.ListParametersRequest{Namespace: pns, KeyPrefix: *keyPrefix, PageToken: token})
		if err != nil {
			return c.failErr("list parameters", err)
		}
		for _, p := range resp.Parameters {
			items = append(items, listItemJSON{
				Type: "parameter", Path: displayPath(p.Ref), Current: p.Version, Note: p.ContentType,
			})
		}
		if token = resp.NextPageToken; token == "" {
			break
		}
	}
	for token := ""; ; {
		resp, err := secretClient.ListSecrets(actx, &kmsv1.ListSecretsRequest{Namespace: pns, KeyPrefix: *keyPrefix, PageToken: token})
		if err != nil {
			return c.failErr("list secrets", err)
		}
		for _, s := range resp.Secrets {
			note := "standard"
			if s.GetBound() {
				note = "bound"
			}
			items = append(items, listItemJSON{
				Type: "secret", Path: displayPath(s.Ref), Current: s.Labels["current"],
				Note: note, Bound: s.GetBound(),
			})
		}
		if token = resp.NextPageToken; token == "" {
			break
		}
	}

	if c.jsonOutput() {
		// The command drains every page itself, so the envelope's
		// next_page_token is always empty (and therefore omitted): the items
		// array is the complete result.
		return c.printList(items, "")
	}
	rows := make([][]string, 0, len(items))
	for _, it := range items {
		rows = append(rows, []string{it.Type, it.Path, strconv.FormatUint(it.Current, 10), it.Note})
	}
	c.printTable([]string{"TYPE", "PATH", "CURRENT", "NOTE"}, rows)
	return 0
}

// listItemJSON is one row of the namespace listing. It carries every table
// column plus bound, so a consumer need not parse the human-readable note.
type listItemJSON struct {
	Type    string `json:"type"` // parameter | secret
	Path    string `json:"path"` // /env/app/key
	Current uint64 `json:"current"`
	Note    string `json:"note"`  // content type (parameter) or standard|bound (secret)
	Bound   bool   `json:"bound"` // always false for a parameter
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
