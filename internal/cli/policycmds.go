package cli

import (
	"errors"
	"flag"
	"fmt"
	"strings"
	"text/tabwriter"

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
		return c.fail("%v", err)
	}
	defer func() { _ = conn.Close() }()
	ctx, cancel := callContext()
	defer cancel()
	resp, err := kmsv1.NewAdminServiceClient(conn).CreatePolicy(cf.authCtx(ctx), &kmsv1.CreatePolicyRequest{Policy: policy})
	if err != nil {
		return c.fail("policy create: %v", err)
	}
	created := resp.GetPolicy()
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
	if !c.parseFlags(fs, args) {
		return 2
	}
	conn, err := c.dialConn(cf)
	if err != nil {
		return c.fail("%v", err)
	}
	defer func() { _ = conn.Close() }()
	ctx, cancel := callContext()
	defer cancel()
	client := kmsv1.NewAdminServiceClient(conn)
	tw := tabwriter.NewWriter(c.Stdout, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "NAME\tSUBJECT\tALLOW\tDENY")
	for token := ""; ; {
		resp, err := client.ListPolicies(cf.authCtx(ctx), &kmsv1.ListPoliciesRequest{PageSize: int32(*pageSize), PageToken: token})
		if err != nil {
			return c.fail("policy list: %v", err)
		}
		for _, p := range resp.GetPolicies() {
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", p.GetName(), p.GetSubject(), formatPolicyRules(p.GetAllow()), formatPolicyRules(p.GetDeny()))
		}
		if token = resp.GetNextPageToken(); token == "" {
			break
		}
	}
	_ = tw.Flush()
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
		return c.fail("%v", err)
	}
	defer func() { _ = conn.Close() }()
	ctx, cancel := callContext()
	defer cancel()
	if _, err := kmsv1.NewAdminServiceClient(conn).DeletePolicy(cf.authCtx(ctx), &kmsv1.DeletePolicyRequest{Name: pos[0]}); err != nil {
		return c.fail("policy delete: %v", err)
	}
	_, _ = fmt.Fprintf(c.Stdout, "Deleted policy %s\n", pos[0])
	return 0
}
