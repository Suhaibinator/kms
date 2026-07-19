// Types mirror the HTTP API contract in docs/http-api.md exactly.
// Field names must match the JSON wire format; do not rename.
//
// Namespace-native model: a resource is addressed by a namespace (env, app)
// plus a relative key. The old flat `path` string is gone from the wire;
// `/env/app/key` survives only as a display format (see lib/format.ts).

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
}

export interface HealthResponse {
  healthy: boolean;
  ready: boolean;
  version: string;
  current_revision: number;
}

export interface ApiErrorEnvelope {
  error: {
    code: string;
    message: string;
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

// The operation identifiers recognized by the policy engine (plan §6, §16.1).
export const POLICY_OPERATIONS: string[] = [
  "*",
  "parameter:*",
  "parameter:read",
  "parameter:write",
  "parameter:list",
  "parameter:delete",
  "secret:*",
  "secret:read",
  "secret:write",
  "secret:list",
  "secret:disable",
  "secret:destroy",
  "secret:promote",
  "configuration-release:*",
  "configuration-release:create",
  "configuration-release:read",
  "configuration-release:validate",
  "configuration-release:activate",
  "configuration-release:list",
  "configuration-release:watch",
  "admin:*",
  "admin:namespace:create",
  "admin:namespace:update",
  "admin:namespace:delete",
  "admin:identity:cert",
  "admin:policy:write",
  "admin:audit:read",
  "admin:key:rotate",
];

// The content types the server accepts for a parameter value. Anything outside
// this allowlist is rejected with invalid_argument, so the UI must offer only
// these. "string" is the default.
export const PARAMETER_CONTENT_TYPES: string[] = [
  "string",
  "integer",
  "float",
  "boolean",
  "json",
  "binary",
];
