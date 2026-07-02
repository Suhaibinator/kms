// Single source of truth for talking to the KMS HTTP API (docs/http-api.md).
// Everything goes through fetch; there is no server runtime.

import type {
  ApiErrorEnvelope,
  AuditFilters,
  CreateIdentityResponse,
  CreateSecretRequest,
  CreateSecretResponse,
  HealthResponse,
  Identity,
  IdentityKind,
  KeysResponse,
  ListAuditResponse,
  ListIdentitiesResponse,
  ListNamespacesResponse,
  ListParametersResponse,
  ListPoliciesResponse,
  ListSecretsResponse,
  LoginResponse,
  Namespace,
  Parameter,
  ParameterMetadata,
  Policy,
  PromoteSecretResponse,
  PutParameterResponse,
  RevealSecretResponse,
  RevisionResponse,
  RotateIdentityResponse,
  SecretMetadata,
  SubscribersResponse,
} from "./types";

const API_BASE = "/api/v1";
const TOKEN_KEY = "kms_token";
const IDENTITY_KEY = "kms_identity";
export const UNAUTHORIZED_EVENT = "kms:unauthorized";

export class ApiError extends Error {
  readonly code: string;
  readonly status: number;

  constructor(code: string, message: string, status: number) {
    super(message);
    this.name = "ApiError";
    this.code = code;
    this.status = status;
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

interface FetchOptions {
  method?: "GET" | "POST" | "PUT" | "DELETE";
  body?: unknown;
  headers?: Record<string, string>;
  auth?: boolean;
}

async function apiFetch<T>(path: string, opts: FetchOptions = {}): Promise<T> {
  const { method = "GET", body, headers = {}, auth = true } = opts;
  const finalHeaders: Record<string, string> = { Accept: "application/json", ...headers };
  if (body !== undefined) finalHeaders["Content-Type"] = "application/json";
  if (auth) {
    const token = getToken();
    if (token) finalHeaders["Authorization"] = `Bearer ${token}`;
  }

  let res: Response;
  try {
    res = await fetch(`${API_BASE}${path}`, {
      method,
      headers: finalHeaders,
      body: body !== undefined ? JSON.stringify(body) : undefined,
    });
  } catch {
    throw new ApiError("unavailable", "Could not reach the server. Check your connection.", 0);
  }

  if (res.status === 401 && auth) {
    clearToken();
    if (typeof window !== "undefined") {
      window.dispatchEvent(new Event(UNAUTHORIZED_EVENT));
    }
  }

  const text = await res.text();
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
    throw new ApiError(code, message, res.status);
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

export const api = {
  // --- Auth & health ---
  login(token: string): Promise<LoginResponse> {
    return apiFetch<LoginResponse>("/auth/login", {
      method: "POST",
      body: { token },
      auth: false,
    });
  },
  health(): Promise<HealthResponse> {
    return apiFetch<HealthResponse>("/health", { auth: false });
  },

  // --- Namespaces ---
  listNamespaces(pageSize?: number, pageToken?: string): Promise<ListNamespacesResponse> {
    return apiFetch<ListNamespacesResponse>(
      `/namespaces${qs({ page_size: pageSize, page_token: pageToken })}`,
    );
  },
  createNamespace(path: string, description: string): Promise<{ namespace: Namespace }> {
    return apiFetch<{ namespace: Namespace }>("/namespaces", {
      method: "POST",
      body: { path, description },
    });
  },

  // --- Parameters ---
  listParameters(
    prefix?: string,
    pageSize?: number,
    pageToken?: string,
  ): Promise<ListParametersResponse> {
    return apiFetch<ListParametersResponse>(
      `/parameters${qs({ prefix, page_size: pageSize, page_token: pageToken })}`,
    );
  },
  getParameter(path: string, version?: number, label?: string): Promise<{ parameter: Parameter }> {
    return apiFetch<{ parameter: Parameter }>(
      `/parameters/get${qs({ path, version, label })}`,
    );
  },
  parameterMetadata(path: string): Promise<ParameterMetadata> {
    return apiFetch<ParameterMetadata>(`/parameters/metadata${qs({ path })}`);
  },
  putParameter(req: {
    path: string;
    value: string;
    content_type: string;
    metadata_json: string;
  }): Promise<PutParameterResponse> {
    return apiFetch<PutParameterResponse>("/parameters", { method: "PUT", body: req });
  },
  deleteParameter(path: string): Promise<RevisionResponse> {
    return apiFetch<RevisionResponse>(`/parameters${qs({ path })}`, { method: "DELETE" });
  },

  // --- Secrets ---
  listSecrets(prefix?: string, pageSize?: number, pageToken?: string): Promise<ListSecretsResponse> {
    return apiFetch<ListSecretsResponse>(
      `/secrets${qs({ prefix, page_size: pageSize, page_token: pageToken })}`,
    );
  },
  secretMetadata(path: string): Promise<{ secret: SecretMetadata }> {
    return apiFetch<{ secret: SecretMetadata }>(`/secrets/metadata${qs({ path })}`);
  },
  // Creating or updating a secret. For client-bound *updates* the caller must
  // supply the existing secret token, sent as X-KMS-Secret-Token.
  createSecret(
    req: CreateSecretRequest,
    secretToken?: string,
  ): Promise<CreateSecretResponse> {
    const headers = secretToken ? { "X-KMS-Secret-Token": secretToken } : undefined;
    return apiFetch<CreateSecretResponse>("/secrets", { method: "POST", body: req, headers });
  },
  revealSecret(path: string, version: number, label: string): Promise<RevealSecretResponse> {
    return apiFetch<RevealSecretResponse>("/secrets/reveal", {
      method: "POST",
      body: { path, version, label },
    });
  },
  disableSecret(path: string, version: number, enable: boolean): Promise<RevisionResponse> {
    return apiFetch<RevisionResponse>("/secrets/disable", {
      method: "POST",
      body: { path, version, enable },
    });
  },
  destroySecret(path: string, version: number): Promise<RevisionResponse> {
    return apiFetch<RevisionResponse>("/secrets/destroy", {
      method: "POST",
      body: { path, version },
    });
  },
  promoteSecret(path: string, version: number): Promise<PromoteSecretResponse> {
    return apiFetch<PromoteSecretResponse>("/secrets/promote", {
      method: "POST",
      body: { path, version },
    });
  },
  deleteSecret(path: string): Promise<RevisionResponse> {
    return apiFetch<RevisionResponse>(`/secrets${qs({ path })}`, { method: "DELETE" });
  },

  // --- Policies ---
  listPolicies(pageSize?: number, pageToken?: string): Promise<ListPoliciesResponse> {
    return apiFetch<ListPoliciesResponse>(
      `/policies${qs({ page_size: pageSize, page_token: pageToken })}`,
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
  listIdentities(pageSize?: number, pageToken?: string): Promise<ListIdentitiesResponse> {
    return apiFetch<ListIdentitiesResponse>(
      `/identities${qs({ page_size: pageSize, page_token: pageToken })}`,
    );
  },
  createIdentity(name: string, kind: IdentityKind): Promise<CreateIdentityResponse> {
    return apiFetch<CreateIdentityResponse>("/identities", {
      method: "POST",
      body: { name, kind },
    });
  },
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
  listAudit(filters: AuditFilters): Promise<ListAuditResponse> {
    return apiFetch<ListAuditResponse>(
      `/audit${qs({
        path_prefix: filters.path_prefix,
        actor: filters.actor,
        event_type: filters.event_type,
        from_unix_ms: filters.from_unix_ms,
        to_unix_ms: filters.to_unix_ms,
        page_size: filters.page_size,
        page_token: filters.page_token,
      })}`,
    );
  },

  // --- Subscribers ---
  subscribers(): Promise<SubscribersResponse> {
    return apiFetch<SubscribersResponse>("/subscribers");
  },

  // --- Keys ---
  keys(): Promise<KeysResponse> {
    return apiFetch<KeysResponse>("/keys");
  },
};
