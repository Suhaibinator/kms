package cli

import (
	"errors"
	"flag"
	"fmt"
	"strings"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
)

// --- policies --------------------------------------------------------------

// cmdAdminPolicy manages namespace-level authorization policies on a running
// server. Rules are written OP@ENV/APP; either label may be "*", and a bare
// OP means every namespace (*/*).
func (c *CLI) cmdAdminPolicy(args []string) int {
	if len(args) == 0 {
		return c.policyUsageError("admin policy requires an action (create|list|delete)")
	}
	action, rest := args[0], args[1:]
	switch action {
	case "help", "-h", "--help":
		c.adminUsage()
		return 0
	case "create":
		return c.cmdPolicyCreate(rest)
	case "list":
		return c.cmdPolicyList(rest)
	case "delete":
		return c.cmdPolicyDelete(rest)
	default:
		return c.policyUsageError("unknown policy action %q", action)
	}
}

// policyRules is a repeatable --allow/--deny flag value.
type policyRules []*kmsv1.PolicyRule

func (r *policyRules) String() string {
	parts := make([]string, 0, len(*r))
	for _, rule := range *r {
		parts = append(parts, formatPolicyRule(rule))
	}
	return strings.Join(parts, ",")
}

func (r *policyRules) Set(raw string) error {
	rule, err := parsePolicyRule(raw)
	if err != nil {
		return err
	}
	*r = append(*r, rule)
	return nil
}

var _ flag.Value = (*policyRules)(nil)

// parsePolicyRule parses OP@ENV/APP. The operation is the text before the
// last "@" so operation names never need escaping; validation of the
// operation and labels is the server's (policy.ValidateRules).
func parsePolicyRule(raw string) (*kmsv1.PolicyRule, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("rule must be OP@ENV/APP")
	}
	op, scope := raw, "*/*"
	if i := strings.LastIndex(raw, "@"); i >= 0 {
		op, scope = raw[:i], raw[i+1:]
	}
	if op == "" {
		return nil, fmt.Errorf("rule %q has no operation", raw)
	}
	env, app := "*", "*"
	switch scope {
	case "*", "":
	default:
		parts := strings.Split(scope, "/")
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return nil, fmt.Errorf("rule %q scope must be ENV/APP (use * for either)", raw)
		}
		env, app = parts[0], parts[1]
	}
	return &kmsv1.PolicyRule{Operation: op, Env: env, App: app}, nil
}

func formatPolicyRule(rule *kmsv1.PolicyRule) string {
	return rule.GetOperation() + "@" + rule.GetEnv() + "/" + rule.GetApp()
}

func formatPolicyRules(rules []*kmsv1.PolicyRule) string {
	if len(rules) == 0 {
		return "-"
	}
	parts := make([]string, 0, len(rules))
	for _, rule := range rules {
		parts = append(parts, formatPolicyRule(rule))
	}
	return strings.Join(parts, ",")
}

func (c *CLI) policyUsageError(format string, args ...any) int {
	_, _ = fmt.Fprintf(c.Stderr, "error: "+format+"\n\n", args...)
	c.adminUsage()
	return 2
}

func (c *CLI) cmdPolicyCreate(args []string) int {
	fs := c.newFlags("policy create")
	cf := addConnFlags(c, fs)
	subject := fs.String("subject", "", "identity `name` the policy binds to, or * for every client")
	var allow, deny policyRules
	fs.Var(&allow, "allow", "allow `rule` OP@ENV/APP (repeatable)")
	fs.Var(&deny, "deny", "deny `rule` OP@ENV/APP (repeatable)")
	c.setUsage(fs, "admin policy create NAME --subject IDENTITY --allow OP@ENV/APP [flags]",
		"Create a namespace-level authorization policy from allow and deny rules.", false)
	if !c.parseFlags(fs, args) {
		return 2
	}
	pos := c.args()
	if len(pos) != 1 || pos[0] == "" {
		return c.policyUsageError("policy create requires a NAME argument")
	}
	if *subject == "" {
		return c.policyUsageError("policy create requires --subject")
	}
	if len(allow) == 0 && len(deny) == 0 {
		return c.policyUsageError("policy create requires at least one --allow or --deny rule")
	}
	policy := &kmsv1.Policy{Name: pos[0], Subject: *subject, Allow: allow, Deny: deny}

	conn, err := c.dialConn(cf)
	if err != nil {
		return c.failErr("", err)
	}
	defer func() { _ = conn.Close() }()
	ctx, cancel := callContext()
	defer cancel()
	resp, err := kmsv1.NewAdminServiceClient(conn).CreatePolicy(cf.authCtx(ctx), &kmsv1.CreatePolicyRequest{Policy: policy})
	if err != nil {
		return c.failErr("policy create", err)
	}
	created := resp.GetPolicy()
	if c.jsonOutput() {
		return c.printJSON(policyToJSON(created))
	}
	_, _ = fmt.Fprintf(c.Stdout, "Created policy %s for subject %s (allow: %s; deny: %s)\n",
		created.GetName(), created.GetSubject(), formatPolicyRules(created.GetAllow()), formatPolicyRules(created.GetDeny()))
	return 0
}

func (c *CLI) cmdPolicyList(args []string) int {
	fs := c.newFlags("policy list")
	cf := addConnFlags(c, fs)
	pageSize := fs.Int("page-size", 100, "result `count` per RPC")
	c.setUsage(fs, "admin policy list [flags]",
		"List policies with their subject and their allow and deny rules.", false)
	if !c.parseFlags(fs, args) || !c.rejectPositionals() {
		return 2
	}
	conn, err := c.dialConn(cf)
	if err != nil {
		return c.failErr("", err)
	}
	defer func() { _ = conn.Close() }()
	ctx, cancel := callContext()
	defer cancel()
	client := kmsv1.NewAdminServiceClient(conn)
	items := []policyJSON{}
	var rows [][]string
	for token := ""; ; {
		resp, err := client.ListPolicies(cf.authCtx(ctx), &kmsv1.ListPoliciesRequest{PageSize: int32(*pageSize), PageToken: token})
		if err != nil {
			return c.failErr("policy list", err)
		}
		for _, p := range resp.GetPolicies() {
			if c.jsonOutput() {
				items = append(items, policyToJSON(p))
				continue
			}
			rows = append(rows, []string{p.GetName(), p.GetSubject(), formatPolicyRules(p.GetAllow()), formatPolicyRules(p.GetDeny())})
		}
		if token = resp.GetNextPageToken(); token == "" {
			break
		}
	}
	if c.jsonOutput() {
		return c.printList(items, "")
	}
	c.printTable([]string{"NAME", "SUBJECT", "ALLOW", "DENY"}, rows)
	return 0
}

func (c *CLI) cmdPolicyDelete(args []string) int {
	fs := c.newFlags("policy delete")
	cf := addConnFlags(c, fs)
	c.setUsage(fs, "admin policy delete NAME [flags]", "Delete a policy by name.", false)
	if !c.parseFlags(fs, args) {
		return 2
	}
	pos := c.args()
	if len(pos) != 1 || pos[0] == "" {
		return c.policyUsageError("policy delete requires a NAME argument")
	}
	conn, err := c.dialConn(cf)
	if err != nil {
		return c.failErr("", err)
	}
	defer func() { _ = conn.Close() }()
	ctx, cancel := callContext()
	defer cancel()
	if _, err := kmsv1.NewAdminServiceClient(conn).DeletePolicy(cf.authCtx(ctx), &kmsv1.DeletePolicyRequest{Name: pos[0]}); err != nil {
		return c.failErr("policy delete", err)
	}
	if c.jsonOutput() {
		c.info("Deleted policy %s", pos[0])
		return c.printJSON(deletedPolicyJSON{Name: pos[0], Deleted: true})
	}
	_, _ = fmt.Fprintf(c.Stdout, "Deleted policy %s\n", pos[0])
	return 0
}

// --- JSON documents --------------------------------------------------------

// policyJSON renders a policy with its rules in the same OP@ENV/APP spelling
// the table prints and the --allow/--deny flags accept, so a listed rule can be
// fed straight back into a create.
type policyJSON struct {
	Name    string   `json:"name"`
	Subject string   `json:"subject"`
	Allow   []string `json:"allow"`
	Deny    []string `json:"deny"`
}

func policyToJSON(p *kmsv1.Policy) policyJSON {
	return policyJSON{
		Name:    p.GetName(),
		Subject: p.GetSubject(),
		Allow:   policyRuleStrings(p.GetAllow()),
		Deny:    policyRuleStrings(p.GetDeny()),
	}
}

// policyRuleStrings renders rules for JSON. Unlike formatPolicyRules it never
// substitutes "-" for an empty list: absent rules are an empty array.
func policyRuleStrings(rules []*kmsv1.PolicyRule) []string {
	out := make([]string, 0, len(rules))
	for _, rule := range rules {
		out = append(out, formatPolicyRule(rule))
	}
	return out
}

type deletedPolicyJSON struct {
	Name    string `json:"name"`
	Deleted bool   `json:"deleted"`
}
