package cli

import (
	"fmt"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (c *CLI) cmdReleasePin(args []string, unpin bool) int {
	name := "pin"
	if unpin {
		name = "unpin"
	}
	fs := c.newFlags("release " + name)
	cf := addConnFlags(c, fs)
	var schema, expected optionalUint64
	fs.Var(&schema, "schema-version", "required schema track")
	fs.Var(&expected, "expected-pin-revision", "required pin revision from subscribers (0 for never pinned)")
	identity := fs.String("identity", "", "subscriber identity")
	client := fs.String("client", "", "subscriber client name")
	instance := fs.String("instance", "", "subscriber instance ID")
	session := fs.String("session", "", "exact client process session ID")
	c.setUsage(fs, "release "+name+" ENV/APP NAME [VERSION] [flags]", "Assign an exact release to one client process; unpin follows the active track.", false)
	if !c.parseFlags(fs, args) {
		return 2
	}
	pos := c.args()
	want := 3
	if unpin {
		want = 2
	}
	if len(pos) != want || !schema.set || !expected.set || *identity == "" || *client == "" || *instance == "" || *session == "" {
		return c.failUsage("%s requires ENV/APP NAME, schema-version, expected-pin-revision, identity, client, instance and session", name)
	}
	ns, err := parseNamespaceProto(pos[0])
	if err != nil {
		return c.failUsage("invalid namespace: %v", err)
	}
	var version uint64
	if !unpin {
		version, err = parseVersion(pos[2])
		if err != nil || version == 0 {
			return c.failUsage("invalid release version")
		}
	}
	conn, err := c.dialConn(cf)
	if err != nil {
		return c.failErr("", err)
	}
	defer func() { _ = conn.Close() }()
	ctx, cancel := callContext()
	defer cancel()
	rpc := kmsv1.NewConfigurationReleaseServiceClient(conn)
	if version > 0 {
		validation, err := rpc.ValidateRelease(cf.authCtx(ctx), &kmsv1.ValidateReleaseRequest{Namespace: ns, Name: pos[1], Version: version, SchemaVersion: &schema.value})
		if err != nil {
			return c.failErr("validate pin", err)
		}
		if !validation.Valid {
			return c.failErr("validate pin", status.Error(codes.FailedPrecondition, "selected release is invalid"))
		}
	}
	action := fmt.Sprintf("%s instance %s/%s session %s in %s schema %d to release %d", name, *client, *instance, *session, pos[0], schema.value, version)
	if ok, code := c.confirmDestructive(action, namespaceDisplay(ns)); !ok {
		return code
	}
	out, err := rpc.SetReleasePin(cf.authCtx(ctx), &kmsv1.SetReleasePinRequest{Session: &kmsv1.ReleaseSessionRef{Namespace: ns, Name: pos[1], SchemaVersion: &schema.value, Identity: *identity, ClientName: *client, InstanceId: *instance, SessionId: *session}, Version: version, ExpectedPinRevision: &expected.value})
	if err != nil {
		return c.failErr("release "+name, err)
	}
	if c.jsonOutput() {
		return c.printJSON(map[string]any{"schema_version": schema.value, "activation_revision": out.ActivationRevision, "session_id": *session, "target_version": out.GetRelease().GetVersion(), "target_revision": out.TargetRevision, "pin_revision": out.PinRevision, "pinned": out.Pinned})
	}
	c.info("Assigned schema %d release %d to process %s; application acknowledgement is pending", schema.value, out.GetRelease().GetVersion(), *session)
	return 0
}
