// Single source of truth for talking to the KMS HTTP API (docs/http-api.md).
// Everything goes through fetch; there is no server runtime.

import type {
  Application,
  ApplicationContractField,
  ApplicationDashboard,
  ApplicationWriteResult,
  ApiErrorEnvelope,
  AuditFilters,
  CaResponse,
  ConfigurationRelease,
  ConfigurationSchema,
  CreateIdentityRequest,
  CreateIdentityResponse,
  CreateNamespaceRequest,
  CreateReleaseRequest,
  CreateSecretRequest,
  CreateSecretResponse,
  HealthResponse,
  Identity,
  IssueCertResponse,
  KeysResponse,
  ListAuditResponse,
  ListIdentitiesResponse,
  ListNamespacesResponse,
  ListParametersResponse,
  ListPoliciesResponse,
  ListSecretsResponse,
  LoginResponse,
  Namespace,
  NamespaceRef,
  Parameter,
  ParameterMetadata,
  Policy,
  PromoteSecretResponse,
  PutParameterRequest,
  PutParameterResponse,
  ReleaseSubscriberState,
  ReleaseSummary,
  ReleaseValidationError,
  RevealSecretResponse,
  RevisionResponse,
  RotateIdentityResponse,
  SecretMetadata,
  SubscribersResponse,
  UpdateNamespaceRequest,
  ValidateReleaseResponse,
  WhoAmIResponse,
} from "./types";

const API_BASE = "/api/v1";
const TOKEN_KEY = "kms_token";
const IDENTITY_KEY = "kms_identity";
export const UNAUTHORIZED_EVENT = "kms:unauthorized";

export class ApiError extends Error {
  readonly code: string;
  readonly status: number;
  readonly validationErrors: ReleaseValidationError[];

  constructor(
    code: string,
    message: string,
    status: number,
    validationErrors: ReleaseValidationError[] = [],
  ) {
    super(message);
    this.name = "ApiError";
    this.code = code;
    this.status = status;
    this.validationErrors = validationErrors;
  }
}

// --- Token + cached identity (memory + sessionStorage) ---

let memToken: string | null = null;

export function setToken(token: string): void {
  memToken = token;
  try {
    sessionStorage.setItem(TOKEN_KEY, token);
  } catch {
    /* storage unavailable; memory copy still works for this tab */
  }
}

export function getToken(): string | null {
  if (memToken) return memToken;
  try {
    memToken = sessionStorage.getItem(TOKEN_KEY);
  } catch {
    memToken = null;
  }
  return memToken;
}

export function clearToken(): void {
  memToken = null;
  try {
    sessionStorage.removeItem(TOKEN_KEY);
    sessionStorage.removeItem(IDENTITY_KEY);
  } catch {
    /* ignore */
  }
}

export function storeIdentity(identity: Identity): void {
  try {
    sessionStorage.setItem(IDENTITY_KEY, JSON.stringify(identity));
  } catch {
    /* ignore */
  }
}

export function loadIdentity(): Identity | null {
  try {
    const raw = sessionStorage.getItem(IDENTITY_KEY);
    if (!raw) return null;
    return JSON.parse(raw) as Identity;
  } catch {
    return null;
  }
}

function httpStatusToCode(status: number): string {
  switch (status) {
    case 400:
      return "invalid_argument";
    case 401:
      return "unauthenticated";
    case 403:
      return "permission_denied";
    case 404:
      return "not_found";
    case 409:
      return "already_exists";
    case 412:
      return "failed_precondition";
    case 503:
      return "unavailable";
    default:
      return "internal";
  }
}

export interface ApiRequestOptions {
  signal?: AbortSignal;
  timeoutMs?: number;
}

interface FetchOptions extends ApiRequestOptions {
  method?: "GET" | "POST" | "PUT" | "PATCH" | "DELETE";
  body?: unknown;
  headers?: Record<string, string>;
  auth?: boolean;
}

const DEFAULT_TIMEOUT_MS = 15_000;

export function isAbortError(error: unknown): boolean {
  return typeof DOMException !== "undefined" && error instanceof DOMException
    ? error.name === "AbortError"
    : error instanceof Error && error.name === "AbortError";
}

export async function apiFetch<T>(path: string, opts: FetchOptions = {}): Promise<T> {
  const {
    method = "GET",
    body,
    headers = {},
    auth = true,
    signal,
    timeoutMs = DEFAULT_TIMEOUT_MS,
  } = opts;
  const finalHeaders: Record<string, string> = { Accept: "application/json", ...headers };
  if (body !== undefined) finalHeaders["Content-Type"] = "application/json";
  if (auth) {
    const token = getToken();
    if (token) finalHeaders.Authorization = `Bearer ${token}`;
  }

  const controller = new AbortController();
  let timedOut = false;
  const abortFromCaller = () => controller.abort(signal?.reason);
  if (signal?.aborted) abortFromCaller();
  else signal?.addEventListener("abort", abortFromCaller, { once: true });
  const timeout =
    timeoutMs > 0
      ? globalThis.setTimeout(() => {
          timedOut = true;
          controller.abort();
        }, timeoutMs)
      : undefined;

  let res: Response;
  let text: string;
  try {
    res = await fetch(`${API_BASE}${path}`, {
      method,
      headers: finalHeaders,
      body: body !== undefined ? JSON.stringify(body) : undefined,
      cache: "no-store",
      signal: controller.signal,
    });
    text = await res.text();
  } catch (err) {
    if (timedOut) {
      throw new ApiError("unavailable", "The server took too long to respond.", 0);
    }
    if (controller.signal.aborted) throw err;
    throw new ApiError("unavailable", "Could not reach the server. Check your connection.", 0);
  } finally {
    if (timeout !== undefined) globalThis.clearTimeout(timeout);
    signal?.removeEventListener("abort", abortFromCaller);
  }

  if (res.status === 401 && auth) {
    clearToken();
    if (typeof window !== "undefined") {
      window.dispatchEvent(new Event(UNAUTHORIZED_EVENT));
    }
  }

  let data: unknown = null;
  if (text) {
    try {
      data = JSON.parse(text);
    } catch {
      data = null;
    }
  }

  if (!res.ok) {
    const env = data as ApiErrorEnvelope | null;
    const code = env?.error?.code ?? httpStatusToCode(res.status);
    const message = env?.error?.message ?? res.statusText ?? "Request failed";
    const validationErrors = Array.isArray(env?.error?.validation_errors)
      ? env.error.validation_errors
      : [];
    throw new ApiError(code, message, res.status, validationErrors);
  }

  return data as T;
}

function qs(params: Record<string, string | number | undefined | null>): string {
  const parts: string[] = [];
  for (const [key, value] of Object.entries(params)) {
    if (value === undefined || value === null || value === "") continue;
    parts.push(`${encodeURIComponent(key)}=${encodeURIComponent(String(value))}`);
  }
  return parts.length ? `?${parts.join("&")}` : "";
}

// A namespace + key address; the shape point ops and detail views pass around.
export interface ResourceRef {
  env: string;
  app: string;
  key: string;
}

export const api = {
  // --- Auth & health ---
  login(token: string): Promise<LoginResponse> {
    return apiFetch<LoginResponse>("/auth/login", {
      method: "POST",
      body: { token },
      auth: false,
    });
  },
  health(request?: ApiRequestOptions): Promise<HealthResponse> {
    return apiFetch<HealthResponse>("/health", { ...request, auth: false });
  },
  whoami(request?: ApiRequestOptions): Promise<WhoAmIResponse> {
    return apiFetch<WhoAmIResponse>("/whoami", request);
  },
  // Public: the built-in client CA, for validating KMS-issued client certs.
  ca(request?: ApiRequestOptions): Promise<CaResponse> {
    return apiFetch<CaResponse>("/ca", { ...request, auth: false });
  },

  // --- Applications ---
  listApplications(
    pageSize?: number,
    pageToken?: string,
    request?: ApiRequestOptions,
  ): Promise<{ applications: Application[]; next_page_token: string }> {
    return apiFetch(`/applications${qs({ page_size: pageSize, page_token: pageToken })}`, request);
  },
  createApplication(req: {
    name: string;
    description: string;
    release_name: string;
    schema_id: string;
    schema_version: number;
    contract: ApplicationContractField[];
  }): Promise<{ application: Application }> {
    return apiFetch("/applications", { method: "POST", body: req });
  },
  updateApplication(req: {
    name: string;
    description: string;
    release_name: string;
    schema_id: string;
    schema_version: number;
    contract: ApplicationContractField[];
  }): Promise<{ application: Application }> {
    return apiFetch("/applications", { method: "PATCH", body: req });
  },
  applicationDashboard(name: string, request?: ApiRequestOptions): Promise<ApplicationDashboard> {
    return apiFetch(`/applications/dashboard${qs({ name })}`, request);
  },
  putApplicationParameter(req: {
    application: string;
    key: string;
    value: string;
    content_type: string;
    metadata_json: string;
    environments: string[];
  }): Promise<{ results: ApplicationWriteResult[] }> {
    return apiFetch("/applications/parameters", { method: "PUT", body: req });
  },

  // --- Namespaces ---
  listNamespaces(
    pageSize?: number,
    pageToken?: string,
    request?: ApiRequestOptions,
  ): Promise<ListNamespacesResponse> {
    return apiFetch<ListNamespacesResponse>(
      `/namespaces${qs({ page_size: pageSize, page_token: pageToken })}`,
      request,
    );
  },
  createNamespace(req: CreateNamespaceRequest): Promise<{ namespace: Namespace }> {
    return apiFetch<{ namespace: Namespace }>("/namespaces", {
      method: "POST",
      body: req,
    });
  },
  updateNamespace(req: UpdateNamespaceRequest): Promise<{ namespace: Namespace }> {
    return apiFetch<{ namespace: Namespace }>("/namespaces", {
      method: "PATCH",
      body: req,
    });
  },
  deleteNamespace(ns: NamespaceRef): Promise<Record<string, never>> {
    return apiFetch<Record<string, never>>(`/namespaces${qs({ env: ns.env, app: ns.app })}`, {
      method: "DELETE",
    });
  },

  // --- Parameters ---
  listParameters(
    ns: NamespaceRef,
    keyPrefix?: string,
    pageSize?: number,
    pageToken?: string,
    request?: ApiRequestOptions,
  ): Promise<ListParametersResponse> {
    return apiFetch<ListParametersResponse>(
      `/parameters${qs({
        env: ns.env,
        app: ns.app,
        key_prefix: keyPrefix,
        page_size: pageSize,
        page_token: pageToken,
      })}`,
      request,
    );
  },
  getParameter(
    ref: ResourceRef,
    version?: number,
    label?: string,
    request?: ApiRequestOptions,
  ): Promise<{ parameter: Parameter }> {
    return apiFetch<{ parameter: Parameter }>(
      `/parameters/get${qs({ env: ref.env, app: ref.app, key: ref.key, version, label })}`,
      request,
    );
  },
  parameterMetadata(ref: ResourceRef, request?: ApiRequestOptions): Promise<ParameterMetadata> {
    return apiFetch<ParameterMetadata>(
      `/parameters/metadata${qs({ env: ref.env, app: ref.app, key: ref.key })}`,
      request,
    );
  },
  putParameter(req: PutParameterRequest): Promise<PutParameterResponse> {
    return apiFetch<PutParameterResponse>("/parameters", { method: "PUT", body: req });
  },
  deleteParameter(ref: ResourceRef): Promise<RevisionResponse> {
    return apiFetch<RevisionResponse>(
      `/parameters${qs({ env: ref.env, app: ref.app, key: ref.key })}`,
      { method: "DELETE" },
    );
  },

  // --- Secrets ---
  listSecrets(
    ns: NamespaceRef,
    keyPrefix?: string,
    pageSize?: number,
    pageToken?: string,
    request?: ApiRequestOptions,
  ): Promise<ListSecretsResponse> {
    return apiFetch<ListSecretsResponse>(
      `/secrets${qs({
        env: ns.env,
        app: ns.app,
        key_prefix: keyPrefix,
        page_size: pageSize,
        page_token: pageToken,
      })}`,
      request,
    );
  },
  secretMetadata(
    ref: ResourceRef,
    request?: ApiRequestOptions,
  ): Promise<{ secret: SecretMetadata }> {
    return apiFetch<{ secret: SecretMetadata }>(
      `/secrets/metadata${qs({ env: ref.env, app: ref.app, key: ref.key })}`,
      request,
    );
  },
  // Creating or updating a secret. For client-bound *updates* the caller must
  // supply the existing secret token, sent as X-KMS-Secret-Token.
  createSecret(req: CreateSecretRequest, secretToken?: string): Promise<CreateSecretResponse> {
    const headers = secretToken ? { "X-KMS-Secret-Token": secretToken } : undefined;
    return apiFetch<CreateSecretResponse>("/secrets", { method: "POST", body: req, headers });
  },
  revealSecret(ref: ResourceRef, version: number, label: string): Promise<RevealSecretResponse> {
    return apiFetch<RevealSecretResponse>("/secrets/reveal", {
      method: "POST",
      body: { env: ref.env, app: ref.app, key: ref.key, version, label },
    });
  },
  disableSecret(ref: ResourceRef, version: number, enable: boolean): Promise<RevisionResponse> {
    return apiFetch<RevisionResponse>("/secrets/disable", {
      method: "POST",
      body: { env: ref.env, app: ref.app, key: ref.key, version, enable },
    });
  },
  destroySecret(ref: ResourceRef, version: number): Promise<RevisionResponse> {
    return apiFetch<RevisionResponse>("/secrets/destroy", {
      method: "POST",
      body: { env: ref.env, app: ref.app, key: ref.key, version },
    });
  },
  promoteSecret(ref: ResourceRef, version: number): Promise<PromoteSecretResponse> {
    return apiFetch<PromoteSecretResponse>("/secrets/promote", {
      method: "POST",
      body: { env: ref.env, app: ref.app, key: ref.key, version },
    });
  },
  deleteSecret(ref: ResourceRef): Promise<RevisionResponse> {
    return apiFetch<RevisionResponse>(
      `/secrets${qs({ env: ref.env, app: ref.app, key: ref.key })}`,
      { method: "DELETE" },
    );
  },

  // --- Policies ---
  listPolicies(
    pageSize?: number,
    pageToken?: string,
    request?: ApiRequestOptions,
  ): Promise<ListPoliciesResponse> {
    return apiFetch<ListPoliciesResponse>(
      `/policies${qs({ page_size: pageSize, page_token: pageToken })}`,
      request,
    );
  },
  createPolicy(policy: Policy): Promise<{ policy: Policy }> {
    return apiFetch<{ policy: Policy }>("/policies", { method: "POST", body: { policy } });
  },
  updatePolicy(policy: Policy): Promise<{ policy: Policy }> {
    return apiFetch<{ policy: Policy }>("/policies", { method: "PUT", body: { policy } });
  },
  deletePolicy(name: string): Promise<Record<string, never>> {
    return apiFetch<Record<string, never>>(`/policies${qs({ name })}`, { method: "DELETE" });
  },

  // --- Identities ---
  listIdentities(
    pageSize?: number,
    pageToken?: string,
    request?: ApiRequestOptions,
  ): Promise<ListIdentitiesResponse> {
    return apiFetch<ListIdentitiesResponse>(
      `/identities${qs({ page_size: pageSize, page_token: pageToken })}`,
      request,
    );
  },
  createIdentity(req: CreateIdentityRequest): Promise<CreateIdentityResponse> {
    return apiFetch<CreateIdentityResponse>("/identities", {
      method: "POST",
      body: req,
    });
  },
  issueCert(name: string, ttlSeconds: number): Promise<IssueCertResponse> {
    return apiFetch<IssueCertResponse>("/identities/issue-cert", {
      method: "POST",
      body: { name, ttl_seconds: ttlSeconds },
    });
  },
  revokeCert(name: string, serial: string): Promise<Record<string, never>> {
    return apiFetch<Record<string, never>>("/identities/revoke-cert", {
      method: "POST",
      body: { name, serial },
    });
  },
  // Rotate a token identity's bearer token: mints a new one and invalidates
  // the old. Shown once, like a create-time token. mTLS identities rotate via
  // issueCert instead.
  rotateIdentity(name: string): Promise<RotateIdentityResponse> {
    return apiFetch<RotateIdentityResponse>("/identities/rotate", {
      method: "POST",
      body: { name },
    });
  },
  revokeIdentity(name: string): Promise<Record<string, never>> {
    return apiFetch<Record<string, never>>("/identities/revoke", {
      method: "POST",
      body: { name },
    });
  },

  // --- Audit ---
  listAudit(filters: AuditFilters, request?: ApiRequestOptions): Promise<ListAuditResponse> {
    return apiFetch<ListAuditResponse>(
      `/audit${qs({
        env: filters.env,
        app: filters.app,
        key_prefix: filters.key_prefix,
        actor: filters.actor,
        event_type: filters.event_type,
        from_unix_ms: filters.from_unix_ms,
        to_unix_ms: filters.to_unix_ms,
        page_size: filters.page_size,
        page_token: filters.page_token,
      })}`,
      request,
    );
  },

  // --- Subscribers ---
  subscribers(request?: ApiRequestOptions): Promise<SubscribersResponse> {
    return apiFetch<SubscribersResponse>("/subscribers", request);
  },

  // --- Configuration releases ---
  listReleases(
    ns: NamespaceRef,
    name?: string,
    pageSize?: number,
    pageToken?: string,
    request?: ApiRequestOptions,
  ): Promise<{ releases: ReleaseSummary[]; next_page_token: string }> {
    return apiFetch(
      `/releases${qs({ env: ns.env, app: ns.app, name, page_size: pageSize, page_token: pageToken })}`,
      request,
    );
  },
  createRelease(req: CreateReleaseRequest): Promise<{ release: ConfigurationRelease }> {
    return apiFetch("/releases", { method: "POST", body: req });
  },
  getRelease(
    ns: NamespaceRef,
    name: string,
    version: number,
    request?: ApiRequestOptions,
  ): Promise<{ release: ConfigurationRelease }> {
    return apiFetch(`/releases/get${qs({ env: ns.env, app: ns.app, name, version })}`, request);
  },
  getActiveRelease(
    ns: NamespaceRef,
    name: string,
    request?: ApiRequestOptions,
  ): Promise<{
    release: ConfigurationRelease;
    activation_revision: number;
    previous_version: number;
  }> {
    return apiFetch(`/releases/active${qs({ env: ns.env, app: ns.app, name })}`, request);
  },
  validateRelease(
    ns: NamespaceRef,
    name: string,
    version: number,
  ): Promise<ValidateReleaseResponse> {
    return apiFetch("/releases/validate", {
      method: "POST",
      body: { namespace: ns, name, version },
    });
  },
  activateRelease(
    ns: NamespaceRef,
    name: string,
    version: number,
    expected?: number,
  ): Promise<{
    release: ConfigurationRelease;
    activation_revision: number;
    previous_version: number;
    changed: boolean;
  }> {
    return apiFetch("/releases/activate", {
      method: "POST",
      body: { namespace: ns, name, version, expected_current_version: expected },
    });
  },
  releaseSubscribers(
    ns: NamespaceRef,
    name: string,
    pageSize?: number,
    pageToken?: string,
    request?: ApiRequestOptions,
  ): Promise<ReleaseSubscribersPage> {
    return apiFetch(
      `/release-subscribers${qs({ env: ns.env, app: ns.app, name, page_size: pageSize, page_token: pageToken })}`,
      request,
    );
  },
  listSchemas(
    id?: string,
    pageToken?: string,
    request?: ApiRequestOptions,
  ): Promise<{ schemas: ConfigurationSchema[]; next_page_token: string }> {
    return apiFetch(`/configuration-schemas${qs({ id, page_token: pageToken })}`, request);
  },
  createSchema(
    id: string,
    schemaJson: string,
    metadataJson = "{}",
  ): Promise<{ schema: ConfigurationSchema }> {
    return apiFetch("/configuration-schemas", {
      method: "POST",
      body: { id, schema_json: schemaJson, metadata_json: metadataJson },
    });
  },

  // --- Keys ---
  keys(request?: ApiRequestOptions): Promise<KeysResponse> {
    return apiFetch<KeysResponse>("/keys", request);
  },
};

export interface ReleaseSubscribersPage {
  subscribers: ReleaseSubscriberState[];
  current_revision: number;
  next_page_token: string;
}
