// Types mirror the HTTP API contract in docs/http-api.md exactly.
// Field names must match the JSON wire format; do not rename.

export type IdentityKind = "admin" | "client";

export interface Identity {
  name: string;
  kind: IdentityKind;
  disabled?: boolean;
  created_at_unix_ms?: number;
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
  path: string;
  description: string;
  created_by: string;
  created_at_unix_ms: number;
}

export interface ListNamespacesResponse {
  namespaces: Namespace[];
  next_page_token: string;
}

// --- Parameters ---

export interface ParameterLabels {
  [label: string]: number;
}

export interface Parameter {
  path: string;
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
  path: string;
  content_type: string;
  metadata_json: string;
  created_at_unix_ms: number;
  updated_at_unix_ms: number;
  labels: ParameterLabels;
  versions: ParameterVersionMeta[];
}

export interface PutParameterRequest {
  path: string;
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
  path: string;
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
  path: string;
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

export interface RevealSecretRequest {
  path: string;
  version: number;
  label: string;
}

export interface RevealSecretResponse {
  path: string;
  version: number;
  value_base64: string;
  content_type: string;
}

export interface DisableSecretRequest {
  path: string;
  version: number;
  enable: boolean;
}

export interface DestroySecretRequest {
  path: string;
  version: number;
}

export interface PromoteSecretRequest {
  path: string;
  version: number;
}

export interface PromoteSecretResponse {
  current_version: number;
  previous_version: number;
  revision: number;
}

// --- Policies ---

export interface PolicyRule {
  operation: string;
  path: string;
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

export interface CreateIdentityResponse {
  identity: Identity;
  token: string;
}

export interface RotateIdentityResponse {
  token: string;
}

// --- Audit ---

export interface AuditEvent {
  id: number;
  event_type: string;
  actor_identity: string;
  actor_type: string;
  resource_type: string;
  resource_path: string;
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
  path_prefix?: string;
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
  paths: string[];
  remote_addr: string;
  connected_at_unix_ms: number;
  last_heartbeat_unix_ms: number;
  last_acked_revision: number;
}

export interface SubscribersResponse {
  subscribers: Subscriber[];
  current_revision: number;
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

// The operation identifiers recognized by the policy engine (plan 16.1).
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
  "admin:*",
  "admin:namespace:create",
  "admin:policy:write",
  "admin:audit:read",
  "admin:key:rotate",
];
