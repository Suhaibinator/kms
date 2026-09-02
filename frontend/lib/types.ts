// Types mirror the HTTP API contract in docs/http-api.md exactly.
// Field names must match the JSON wire format; do not rename.
//
// Namespace-native model: a resource is addressed by a namespace (env, app)
// plus a relative key. The old flat `path` string is gone from the wire;
// `/env/app/key` survives only as a display format (see lib/format.ts).

import {
  PARAMETER_CONTENT_TYPES as CONTENT_TYPES,
  KNOWN_OPERATIONS,
  OPERATION_WILDCARD_CATEGORIES,
} from "@/lib/validation";

export type IdentityKind = "admin" | "client";

// Authentication methods a namespace admits and an identity may present.
export type AuthMethod = "mtls" | "token";

// A namespace address. Both fields are required to name a namespace.
export interface NamespaceRef {
  env: string;
  app: string;
}

export interface Identity {
  name: string;
  kind: IdentityKind;
  disabled?: boolean;
  created_at_unix_ms?: number;
  // Bound namespace, or null/absent for unbound (admin/tooling) identities.
  namespace?: NamespaceRef | null;
  // How the current session authenticated ("mtls" | "token"); only set on the
  // signed-in identity, from /whoami.
  auth_method?: string;
  has_token?: boolean;
  certs?: IdentityCert[];
}

export interface IdentityCert {
  serial: string;
  fingerprint: string;
  not_after_unix_ms: number;
  // Zero when the cert is still valid; a timestamp once revoked.
  revoked_at_unix_ms: number;
  created_at_unix_ms: number;
}

// One-time PEM bundle returned when a client certificate is issued. The
// private key is shown exactly once and never stored server-side.
export interface CertBundle {
  cert_pem: string;
  key_pem: string;
  serial: string;
  not_after_unix_ms: number;
}

export interface LoginResponse {
  identity: {
    name: string;
    kind: IdentityKind;
  };
  // How the credentials presented at login were accepted ("mtls" | "token").
  // Absent on servers older than admin client-certificate support.
  auth_method?: string;
}

export interface HealthResponse {
  healthy: boolean;
  ready: boolean;
  version: string;
  current_revision: number;
  // The gRPC listener SDKs connect to, and whether it serves TLS. Surfaced by
  // the Connect SDK panel; `tls_enabled: false` is a warning, not a detail.
  grpc_addr: string;
  tls_enabled: boolean;
  // Whether the server makes admins present a client certificate on top of
  // their token, and whether *this* connection presented a chain-verified one.
  // Both are unauthenticated, so the login page can explain a missing
  // certificate without becoming an oracle for token validity.
  admin_client_cert_required: boolean;
  client_cert_presented: boolean;
}

export interface ApiErrorEnvelope {
  error: {
    code: string;
    message: string;
    validation_errors?: ReleaseValidationError[];
  };
}

// --- Namespaces ---

export interface Namespace {
  env: string;
  app: string;
  description: string;
  allowed_auth_methods: AuthMethod[];
  created_by: string;
  created_at_unix_ms: number;
  parameter_count: number;
  secret_count: number;
}

export interface ListNamespacesResponse {
  namespaces: Namespace[];
  next_page_token: string;
}

export interface CreateNamespaceRequest {
  env: string;
  app: string;
  description: string;
  allowed_auth_methods: AuthMethod[];
}

// PATCH body: env/app identify the namespace; description + auth methods are
// a full replacement.
export interface UpdateNamespaceRequest {
  env: string;
  app: string;
  description: string;
  allowed_auth_methods: AuthMethod[];
}

// --- Applications ---

export interface ApplicationContractField {
  alias: string;
  kind: "parameter" | "secret";
  content_type?: string;
}

export interface Application {
  name: string;
  description: string;
  release_name: string;
  schema_id: string;
  schema_version: number;
  contract: ApplicationContractField[];
  created_by: string;
  created_at_unix_ms: number;
  updated_at_unix_ms: number;
  environment_count: number;
}

export interface ApplicationConfigurationCell {
  present: boolean;
  value?: string;
  content_type: string;
  version: number;
  client_bound?: boolean;
  has_access_token?: boolean;
}

export interface ApplicationConfigurationRow {
  key: string;
  kind: "parameter" | "secret";
  environments: Record<string, ApplicationConfigurationCell>;
}

export interface ApplicationDashboard {
  application: Application;
  environments: Namespace[];
  rows: ApplicationConfigurationRow[];
}

export interface ApplicationWriteResult {
  environment: string;
  version: number;
  revision: number;
  error?: string;
}

// --- Parameters ---

export interface ParameterLabels {
  [label: string]: number;
}

export interface Parameter {
  env: string;
  app: string;
  key: string;
  value: string;
  content_type: string;
  version: number;
  metadata_json: string;
  created_by: string;
  created_at_unix_ms: number;
  labels: ParameterLabels;
}

export interface ListParametersResponse {
  parameters: Parameter[];
  next_page_token: string;
}

export interface ParameterVersionMeta {
  version: number;
  content_type: string;
  state: string;
  created_by: string;
  created_at_unix_ms: number;
  metadata_json: string;
}

export interface ParameterMetadata {
  env: string;
  app: string;
  key: string;
  content_type: string;
  metadata_json: string;
  created_at_unix_ms: number;
  updated_at_unix_ms: number;
  labels: ParameterLabels;
  versions: ParameterVersionMeta[];
}

export interface PutParameterRequest {
  env: string;
  app: string;
  key: string;
  value: string;
  content_type: string;
  metadata_json: string;
}

export interface PutParameterResponse {
  version: number;
  revision: number;
}

export interface RevisionResponse {
  revision: number;
}

// --- Secrets ---

export type SecretVersionState = "enabled" | "disabled" | "destroyed";

export interface SecretVersion {
  version: number;
  state: SecretVersionState;
  created_by: string;
  created_at_unix_ms: number;
  destroyed_at_unix_ms: number;
  expires_at_unix_ms: number;
  metadata_json: string;
}

export interface SecretMetadata {
  env: string;
  app: string;
  key: string;
  content_type: string;
  client_bound: boolean;
  has_access_token: boolean;
  metadata_json: string;
  created_at_unix_ms: number;
  updated_at_unix_ms: number;
  labels: ParameterLabels;
  versions: SecretVersion[];
}

export interface ListSecretsResponse {
  secrets: SecretMetadata[];
  next_page_token: string;
}

export interface CreateSecretRequest {
  env: string;
  app: string;
  key: string;
  value_base64: string;
  content_type: string;
  metadata_json: string;
  client_bound: boolean;
  generate_access_token: boolean;
  expires_at_unix_ms: number;
}

export interface CreateSecretResponse {
  version: number;
  revision: number;
  access_token?: string;
}

export interface RevealSecretResponse {
  env: string;
  app: string;
  key: string;
  version: number;
  value_base64: string;
  content_type: string;
}

export interface PromoteSecretResponse {
  current_version: number;
  previous_version: number;
  revision: number;
}

// --- Policies ---

// A rule grants an operation over a whole namespace. env/app are exact or "*".
// There is no key field: the namespace (env, app) is the unit of authorization,
// so a grant applies to every key in the matched namespace.
export interface PolicyRule {
  operation: string;
  env: string;
  app: string;
}

export interface Policy {
  name: string;
  subject: string;
  allow: PolicyRule[];
  deny: PolicyRule[];
  created_at_unix_ms: number;
  updated_at_unix_ms: number;
}

export interface ListPoliciesResponse {
  policies: Policy[];
  next_page_token: string;
}

// --- Identities ---

export interface ListIdentitiesResponse {
  identities: Identity[];
  next_page_token: string;
}

export interface CreateIdentityRequest {
  name: string;
  kind: IdentityKind;
  // null = unbound. When set, the identity gets the implicit home-namespace
  // grant for reads/lists within it.
  namespace: NamespaceRef | null;
  auth_methods: AuthMethod[];
  // Only meaningful when auth_methods includes "mtls".
  cert_ttl_seconds: number;
}

// token present only when auth_methods included "token"; cert present only
// when it included "mtls". Both are shown exactly once.
export interface CreateIdentityResponse {
  identity: Identity;
  token?: string;
  cert?: CertBundle;
}

export interface IssueCertResponse {
  cert: CertBundle;
}

// One-time new bearer token from rotating a token identity's credential.
export interface RotateIdentityResponse {
  token: string;
}

export interface WhoAmIResponse {
  name: string;
  kind: IdentityKind;
  namespace: NamespaceRef | null;
  auth_method: string;
}

export interface CaResponse {
  cert_pem: string;
}

// --- Audit ---

export interface AuditEvent {
  id: number;
  event_type: string;
  actor_identity: string;
  actor_type: string;
  resource_type: string;
  resource_env: string;
  resource_app: string;
  resource_key: string;
  resource_version: number;
  resource_namespace_id: number;
  decision: string;
  source_ip: string;
  user_agent: string;
  request_id: string;
  created_at_unix_ms: number;
  metadata_json: string;
}

export interface ListAuditResponse {
  events: AuditEvent[];
  next_page_token: string;
}

export interface AuditFilters {
  env?: string;
  app?: string;
  key_prefix?: string;
  actor?: string;
  event_type?: string;
  from_unix_ms?: number;
  to_unix_ms?: number;
  page_size?: number;
  page_token?: string;
}

// --- Subscribers ---

export interface Subscriber {
  client_name: string;
  instance_id: string;
  identity: string;
  // The namespaces this stream is subscribed to. A subscriber receives every
  // change in each of these namespaces; there is no per-key filtering on the
  // wire (any narrower interest is applied client-side in the callback).
  namespaces: NamespaceRef[];
  remote_addr: string;
  connected_at_unix_ms: number;
  last_heartbeat_unix_ms: number;
  last_acked_revision: number;
}

export interface SubscribersResponse {
  subscribers: Subscriber[];
  current_revision: number;
}

// --- Configuration releases ------------------------------------------------

export type ReleaseEntryKind = "parameter" | "secret";

export interface ReleaseEntrySelector {
  alias: string;
  kind: ReleaseEntryKind;
  ref: ResourceReference;
  version?: number;
  label?: string;
}

export interface ResourceReference {
  namespace: NamespaceRef;
  key: string;
}

export interface ConfigurationReleaseEntry {
  alias: string;
  kind: ReleaseEntryKind;
  ref: ResourceReference;
  version: number;
  content_type: string;
  metadata_json: string;
  parameter_digest: string;
  client_bound: boolean;
  has_access_token: boolean;
}

export interface ConfigurationRelease {
  namespace: NamespaceRef;
  name: string;
  version: number;
  schema_id: string;
  schema_version: number;
  entries: ConfigurationReleaseEntry[];
  digest: string;
  metadata_json: string;
  created_by: string;
  created_at_unix_ms: number;
}

export interface ReleaseSummary {
  release: ConfigurationRelease;
  current: boolean;
  previous: boolean;
  activation_revision: number;
}

export interface CreateReleaseRequest {
  namespace: NamespaceRef;
  name: string;
  schema_id?: string;
  schema_version?: number;
  entries: ReleaseEntrySelector[];
  metadata_json?: string;
}

export interface ReleaseValidationError {
  alias: string;
  code: string;
  schema_pointer: string;
  message: string;
}

export interface ValidateReleaseResponse {
  valid: boolean;
  errors: ReleaseValidationError[];
}

export interface ConfigurationSchema {
  id: string;
  version: number;
  schema_json: string;
  digest: string;
  metadata_json: string;
  created_by: string;
  created_at_unix_ms: number;
}

export interface ActivateReleaseResponse {
  release: ConfigurationRelease;
  activation_revision: number;
  previous_version: number;
  changed: boolean;
}

export interface ReleaseSubscriberState {
  namespace: NamespaceRef;
  release_name: string;
  client_name: string;
  instance_id: string;
  identity: string;
  state: "" | "received" | "prepared" | "applied" | "rejected"; // empty = transport registered, no lifecycle state yet
  release_version: number;
  activation_revision: number;
  rejection_category: string;
  diagnostic: string;
  client_timestamp_unix_ms: number;
  server_timestamp_unix_ms: number;
  connected: boolean;
  // Only meaningful for `applied`: the generation the instance runs differs
  // from the application's source-owned defaults (a hot override, or code
  // that has not adopted the release values yet).
  applied_divergent: boolean;
  divergent_field_count: number;
}

// --- Keys ---

export interface KeyMetadata {
  id: string;
  source: string;
  state: string;
  created_at_unix_ms: number;
}

export interface KeysResponse {
  keys: KeyMetadata[];
}

// The operation identifiers recognized by the policy engine,
// and the content types the server accepts for a parameter value.
//
// Both sets are defined once, in lib/validation.ts, alongside the validators
// that enforce them — a picker offering an option the validator rejects (or
// omitting one it accepts) is the drift this indirection exists to prevent.
// They are re-exported here because that is where the UI has always imported
// them from.
//
// The operation list is ordered for display: the global wildcard, then each
// category's wildcard followed by its own operations. Membership in it is
// exactly `policy.validOperationPattern`.
export const POLICY_OPERATIONS: string[] = [
  "*",
  ...OPERATION_WILDCARD_CATEGORIES.flatMap((category) => [
    `${category}:*`,
    ...KNOWN_OPERATIONS.filter((op) => op.startsWith(`${category}:`)),
  ]),
];

export const PARAMETER_CONTENT_TYPES: string[] = [...CONTENT_TYPES];

// --- Console aggregates ------------------------------------------------------
//
// Read-model endpoints the console composes its application, fleet and ship
// surfaces from. The backend computes every state and finding (readiness state
// machine); the frontend only renders. Copy for finding codes lives
// in lib/readiness.ts, keyed by `code` — `params` carry numbers and names,
// never values.

export type AppStatus = "blocked" | "setup" | "attention" | "ready";

export type EnvStatus =
  | "blocked"
  | "empty"
  | "incomplete"
  | "unreleased"
  | "degraded"
  | "rolling"
  | "drift"
  | "ready";

export type ValuesState = "empty" | "incomplete" | "complete";

export type ReleaseState = "none" | "active" | "drift" | "blocked";

export type RolloutState = "no_subscribers" | "applied" | "rolling" | "degraded" | "stale";

export type FindingCode =
  | "no_environments"
  | "contract_empty"
  | "schema_unpinned"
  | "schema_missing"
  | "schema_property_missing_alias"
  | "schema_required_missing_alias"
  | "alias_not_in_schema"
  | "contract_type_mismatch"
  | "contract_release_mismatch"
  | "release_pin_stale"
  | "resource_missing"
  | "kind_mismatch"
  | "content_type_mismatch"
  | "secret_unreadable"
  | "secret_token_required"
  | "no_active_release"
  | "unreleased_changes"
  | "alias_not_in_release"
  | "no_subscribers"
  | "subscriber_other_release"
  | "instance_rejected"
  | "instance_divergent"
  | "instance_pending"
  | "instance_stale"
  | "rolled_back"
  | "previous_unavailable"
  | "production"
  | "insecure_listener";

export type FindingSeverity = "blocking" | "warning" | "info";

export interface Finding {
  code: FindingCode;
  severity: FindingSeverity;
  scope: {
    env?: string;
    alias?: string;
    instance?: string;
  };
  params: Record<string, string | number>;
}

// One contract alias resolved against an environment. `key` is absent when
// the alias resolved to nothing.
export interface OverviewValue {
  alias: string;
  kind: ReleaseEntryKind;
  key?: string;
  present: boolean;
  content_type?: string;
  current_version?: number;
  pinned_version?: number;
  client_bound?: boolean;
}

export interface OverviewActiveRelease {
  name: string;
  version: number;
  activation_revision: number;
  previous_version: number;
  created_by: string;
  created_at_unix_ms: number;
  is_rolled_back: boolean;
  schema_id: string;
  schema_version: number;
  digest: string;
  entries: ConfigurationReleaseEntry[];
}

// The effective lifecycle row for one (identity, client, instance) triple —
// see lib/subscribers.ts for how the raw state rows collapse into it.
export interface SubscriberInstance {
  identity: string;
  client_name: string;
  instance_id: string;
  state: ReleaseSubscriberState["state"];
  release_version: number;
  activation_revision: number;
  rejection_category: string;
  diagnostic: string;
  connected: boolean;
  server_timestamp_unix_ms: number;
  applied_divergent: boolean;
  divergent_field_count: number;
}

export interface OverviewRollout {
  total: number;
  connected: number;
  applied_current: number;
  // Applied at the current revision but diverging from source defaults; a
  // warning, never a rollout failure.
  applied_divergent: number;
  rejected: number;
  pending: number;
  stale: number;
  other_release_names: string[];
  // At most 50; `truncated` says whether more were dropped.
  rejected_instances: SubscriberInstance[];
  truncated: boolean;
}

export interface EnvironmentOverview {
  namespace: Namespace;
  production: boolean;
  status: EnvStatus;
  values_state: ValuesState;
  release_state: ReleaseState;
  // Only meaningful while release_state is active or drift.
  rollout_state: RolloutState;
  values: OverviewValue[];
  release: {
    active?: OverviewActiveRelease;
    latest_version: number;
    release_count: number;
  };
  rollout: OverviewRollout;
  findings: Finding[];
}

export interface ApplicationOverview {
  application: Application;
  status: AppStatus;
  findings: Finding[];
  environments: EnvironmentOverview[];
  // The matrix tab's rows, unchanged from ApplicationDashboard.
  rows: ApplicationConfigurationRow[];
  schema_json?: string;
}

export type DefaultsApplyStatus = "create" | "unchanged" | "update" | "blocked";

/** Opaque artifact bytes. Consumers must not decode or normalize this payload. */
export type DefaultsArtifactBody = ArrayBuffer;

export interface DefaultsApplyEntry {
  alias: string;
  key: string;
  content_type: string;
  status: DefaultsApplyStatus;
  current_version: number;
  applied_version: number;
  revision: number;
}

/** Value-free preview/result returned by the managed-defaults import endpoint. */
export interface DefaultsApplyResponse {
  profile: string;
  schema_sha256: string;
  artifact_digest: string;
  plan_digest: string;
  entries: DefaultsApplyEntry[];
  missing_secrets: string[];
  executed: boolean;
  definition_changed: boolean;
  definition_updated: boolean;
}

export interface FleetEnvironment {
  env: string;
  status: EnvStatus;
  production: boolean;
}

export interface FleetApplication {
  application: Application;
  status: AppStatus;
  environments: FleetEnvironment[];
}

export interface FleetOverview {
  applications: FleetApplication[];
}

// One row of a ship request. `value` writes a new parameter version;
// `version`/`label` pins an existing one without writing (drift opt-in, retry
// reuse). Secrets accept version/label only — their values never travel here.
export interface ShipChange {
  alias: string;
  value?: string;
  content_type?: string;
  version?: number;
  label?: string;
}

export interface ShipRequest {
  application: string;
  environment: string;
  changes: ShipChange[];
  metadata_json?: string;
  dry_run?: boolean;
  expected_active_version?: number;
  request_id?: string;
}

export type ShipEntryChange = "edited" | "pinned" | "included" | "missing";

export interface ShipPreviewEntry {
  alias: string;
  kind: ReleaseEntryKind;
  key: string;
  from_version?: number;
  to_version?: number;
  change: ShipEntryChange;
}

export interface ShipPreview {
  // The release the preview was built on: the active pins, or 0 when nothing
  // is active yet (then the base is `current`).
  base_version: number;
  release_name: string;
  schema_id: string;
  schema_version: number;
  entries: ShipPreviewEntry[];
  validation: ValidateReleaseResponse;
  warnings: Finding[];
}

export type ShipStatus =
  | "preview"
  | "activated"
  | "rejected"
  | "release_created_not_activated"
  | "conflict";

export interface ShipResult {
  // 200 for every "we evaluated it" outcome; the status says what happened.
  status: ShipStatus;
  preview: ShipPreview;
  parameters: Array<{ alias: string; key: string; version: number; revision: number }>;
  release?: { name: string; version: number; digest: string };
  activation?: { activation_revision: number; previous_version: number; changed: boolean };
  error?: {
    code: string;
    message: string;
    validation_errors?: ReleaseValidationError[];
    current_version?: number;
  };
}

export interface CloneEnvironmentRequest {
  application: string;
  source_env: string;
  target_env: string;
  copy_values: boolean;
  auth_methods?: AuthMethod[];
  description?: string;
}

export type CloneItemAction = "copied" | "needs_value" | "exists" | "missing_in_source" | "error";

export interface CloneEnvironmentItem {
  alias: string;
  key: string;
  kind: ReleaseEntryKind;
  // Existing target keys are never overwritten → "exists". Secrets are never
  // copied → "needs_value".
  action: CloneItemAction;
  source_version?: number;
  target_version?: number;
  error?: string;
}

export interface CloneEnvironmentResponse {
  namespace: Namespace;
  namespace_created: boolean;
  items: CloneEnvironmentItem[];
  needs_value: string[];
}

export interface RollbackRequest {
  env: string;
  app: string;
  name: string;
  expected_current_version?: number;
}

export interface RollbackResponse {
  release: ConfigurationRelease;
  activation_revision: number;
  previous_version: number;
  rolled_back_from: number;
  changed: boolean;
}

// One `snapshot` frame on GET /release-subscribers/stream.
export interface SubscriberStreamSnapshot {
  summary: OverviewRollout;
  subscribers: ReleaseSubscriberState[];
  current_revision: number;
  server_time_unix_ms: number;
}
