# Minimal serverful Next.js integration

This App Router sketch owns one Node KMS client and release loader, exposes only
`minLength` through an allowlisted Route Handler, validates passwords against
the same atomic snapshot in a Server Action, and lets an open browser recover
from `policy_changed` without a page reload.

Copy the files into a serverful Next.js application, adjust the aliases and
validation for the application's release, and set:

- `KMS_ENDPOINT`
- `KMS_CLIENT_CERT`
- `KMS_CLIENT_KEY`
- `KMS_SERVER_CA`
- optionally `KMS_PASSWORD_PEPPER_TOKEN` for a token-protected release secret

The deployment must use the Node runtime. Static export and Edge deployments
cannot host the KMS transport or bidirectional release watch. Production code
should also expose bounded loader-health metrics and decide whether a startup
without an initial policy should keep returning `503` or fail process
readiness. The instrumentation example owns termination explicitly: after the
adapter's cleanup attempt settles it exits with the conventional SIGINT or
SIGTERM status. Replace that callback only when your process supervisor has a
different documented termination contract.
