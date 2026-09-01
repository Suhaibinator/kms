// Presentation of the readiness state machine. The backend
// computes every state and finding; this module only maps codes to labels,
// tones, copy and the action that fixes them. Copy never includes values —
// findings carry names and numbers only.

import type { BadgeKind } from "@/components/ui";
import type { AppStatus, EnvStatus, Finding, FindingCode } from "@/lib/types";

/** Matches `prod`, `prod-*`, and `production`, but not `reproduction` or `non-prod`. */
export const PRODUCTION_ENVIRONMENT = /^prod(-|$)|^production$/;

export function isProductionEnvironment(env: string): boolean {
  return PRODUCTION_ENVIRONMENT.test(env);
}

export const STATUS_LABEL: Record<AppStatus | EnvStatus, string> = {
  blocked: "Blocked",
  setup: "Setup",
  attention: "Needs attention",
  ready: "Ready",
  empty: "No values",
  incomplete: "Incomplete",
  unreleased: "Unreleased",
  degraded: "Degraded",
  rolling: "Rolling out",
  drift: "Unreleased changes",
};

export const STATUS_TONE: Record<AppStatus | EnvStatus, BadgeKind> = {
  blocked: "danger",
  setup: "neutral",
  attention: "warning",
  ready: "success",
  empty: "neutral",
  incomplete: "warning",
  unreleased: "accent",
  degraded: "danger",
  rolling: "accent",
  drift: "warning",
};

export type FixAction =
  | "add_environment"
  | "edit_contract"
  | "pin_schema"
  | "open_release"
  | "ship"
  | "create_parameter"
  | "create_secret"
  | "open_resource"
  | "open_secret"
  | "connect_sdk"
  | "open_subscribers"
  | "open_health";

export const FIX_LABEL: Record<FixAction, string> = {
  add_environment: "Add environment",
  edit_contract: "Edit contract",
  pin_schema: "Pin schema",
  open_release: "Open release",
  ship: "Ship",
  create_parameter: "Create parameter",
  create_secret: "Add secret",
  open_resource: "Open resource",
  open_secret: "Open secret",
  connect_sdk: "Connect SDK",
  open_subscribers: "Open subscribers",
  open_health: "Open health",
};

// The primary fix per code. `resource_missing` defaults to a parameter;
// fixActionFor() switches to create_secret from the finding's `kind` param.
export const FIX_FOR: Record<FindingCode, FixAction | null> = {
  no_environments: "add_environment",
  contract_empty: "edit_contract",
  schema_unpinned: "pin_schema",
  schema_missing: "pin_schema",
  schema_property_missing_alias: "edit_contract",
  schema_required_missing_alias: "edit_contract",
  alias_not_in_schema: "edit_contract",
  contract_type_mismatch: "edit_contract",
  contract_release_mismatch: "edit_contract",
  release_pin_stale: "ship",
  resource_missing: "create_parameter",
  kind_mismatch: "open_resource",
  content_type_mismatch: "open_resource",
  secret_unreadable: "open_secret",
  secret_token_required: "open_secret",
  no_active_release: "ship",
  unreleased_changes: "ship",
  alias_not_in_release: "ship",
  no_subscribers: "connect_sdk",
  subscriber_other_release: "connect_sdk",
  instance_rejected: "open_subscribers",
  instance_divergent: "open_subscribers",
  instance_pending: "open_subscribers",
  instance_stale: "open_subscribers",
  rolled_back: "open_release",
  previous_unavailable: null,
  production: null,
  insecure_listener: "open_health",
};

export function fixActionFor(finding: Finding): FixAction | null {
  if (finding.code === "resource_missing" && finding.params.kind === "secret") {
    return "create_secret";
  }
  return FIX_FOR[finding.code] ?? null;
}

type Params = Finding["params"];

const str = (p: Params, key: string): string | null => {
  const v = p[key];
  return v === undefined || v === null || v === "" ? null : String(v);
};
const alias = (p: Params): string => (str(p, "alias") ? `\`${str(p, "alias")}\`` : "an alias");
const plural = (n: number, one: string, many = `${one}s`): string => `${n} ${n === 1 ? one : many}`;

// Human copy per code. Every function must render something sensible with an
// empty params object — the backend may omit any param.
export const FINDING_COPY: Record<FindingCode, (p: Params) => string> = {
  no_environments: () =>
    "This application has no environments yet. Add one to start setting values.",
  contract_empty: () =>
    "The contract lists no aliases, so nothing can be shipped. Define the aliases the application reads, or derive them from an active release.",
  schema_unpinned: () =>
    "No schema is pinned. Releases will not be validated against a shape before activation.",
  schema_missing: (p) => {
    const id = str(p, "schema_id");
    const pin = id ? ` ${id}@${str(p, "schema_version") ?? "?"}` : "";
    return `The pinned schema${pin} does not exist in the registry, so no release can be validated or activated.`;
  },
  schema_property_missing_alias: (p) =>
    `The schema has no property for ${alias(p)}, so the value is never validated.`,
  schema_required_missing_alias: (p) =>
    `The schema requires ${alias(p)}, which is not in the contract. Validation will always fail.`,
  alias_not_in_schema: (p) =>
    `Contract alias ${alias(p)} is not a schema property. Its value will be rejected by additionalProperties.`,
  contract_type_mismatch: (p) =>
    `Contract alias ${alias(p)} is ${str(p, "content_type") ?? "one type"} but the schema expects ${str(p, "schema_type") ?? "another"}.`,
  contract_release_mismatch: () =>
    "The contract was edited after the active release was created. New releases must match the contract; the active one no longer does.",
  release_pin_stale: (p) =>
    `The active release pins ${alias(p)} to a version that no longer exists or is disabled. Ship a new release.`,
  resource_missing: (p) =>
    `No ${str(p, "kind") ?? "resource"} exists for ${alias(p)} in this environment.`,
  kind_mismatch: (p) =>
    `${alias(p)} is a ${str(p, "kind") ?? "different kind"} in the contract but the resource is a ${str(p, "found") ?? "different kind"}.`,
  content_type_mismatch: (p) =>
    `${alias(p)} is ${str(p, "content_type") ?? "one content type"} in the contract but the parameter is ${str(p, "found") ?? "another"}.`,
  secret_unreadable: (p) =>
    `The pinned version of secret ${alias(p)} is ${str(p, "state") ?? "not readable"}; clients cannot fetch it.`,
  secret_token_required: (p) =>
    `Secret ${alias(p)} is token-protected. Each client needs the local secret token to read it.`,
  no_active_release: () =>
    "No release is active in this environment. Clients receive nothing until one is shipped.",
  unreleased_changes: (p) => {
    const current = str(p, "current");
    const pinned = str(p, "pinned");
    return current && pinned
      ? `${alias(p)} has version ${current} but the active release pins ${pinned}.`
      : `${alias(p)} has changed since the active release was created.`;
  },
  alias_not_in_release: (p) =>
    `Contract alias ${alias(p)} is not in the active release. Clients do not receive it.`,
  no_subscribers: () => "No client is subscribed to this release name in this environment.",
  subscriber_other_release: (p) => {
    const n = Number(p.count ?? 0);
    return `${n > 0 ? plural(n, "instance subscribes", "instances subscribe") : "Some instances subscribe"} to a different release name than the application's.`;
  },
  instance_rejected: (p) =>
    `An instance rejected the active release (${str(p, "category") ?? "unknown category"}) and is still serving the previous one.`,
  instance_divergent: (p) => {
    const n = Number(p.divergent_fields ?? 0);
    return `An instance applied the active release but runs values that differ from its source-owned defaults${n ? ` (${plural(n, "field", "fields")})` : ""}. Adopt the release values in code or restore the defaults.`;
  },
  instance_pending: () => "An instance is connected but has not applied the active release yet.",
  instance_stale: () => "An instance disconnected before applying the active release.",
  rolled_back: (p) =>
    `This environment was rolled back${str(p, "from") ? ` from version ${str(p, "from")}` : ""}. The newer release is still available to re-activate.`,
  previous_unavailable: () => "There is no previous release to roll back to.",
  production: () =>
    "Production environment: shipping and rolling back ask you to type the environment name.",
  insecure_listener: () =>
    "The gRPC listener serves without TLS. Client tokens and values travel in clear text.",
};

export function findingCopy(finding: Finding): string {
  const render = FINDING_COPY[finding.code];
  return render ? render(finding.params ?? {}) : `Finding ${finding.code}.`;
}

/** True when any finding is blocking — Ship must stay disabled. */
export function findingsBlockShip(findings: readonly Finding[]): boolean {
  return findings.some((finding) => finding.severity === "blocking");
}
