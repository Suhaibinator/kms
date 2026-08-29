// Which credential methods an identity can actually use, given the namespace
// it is bound to. Shared by the identities page (disables Rotate / Issue with
// a reason) and the namespace editor (warns before an auth method is removed
// out from under the identities relying on it).

import type { AuthMethod, Identity, Namespace, NamespaceRef } from "./types";

export type MethodAvailability = "allowed" | "rejected" | "unknown";

/** Certificates that are neither revoked nor expired. */
export function validCertCount(identity: Identity, now = Date.now()): number {
  return (identity.certs ?? []).filter(
    (cert) =>
      cert.revoked_at_unix_ms === 0 &&
      (cert.not_after_unix_ms === 0 || cert.not_after_unix_ms > now),
  ).length;
}

export function identityMethodAvailability(
  identity: Identity,
  method: AuthMethod,
  namespaces: Namespace[],
  namespacesLoading: boolean,
  namespacesError: unknown,
): MethodAvailability {
  if (identity.kind === "admin") return "allowed";
  if (!identity.namespace) return "allowed";
  if (namespacesLoading || namespacesError) return "unknown";
  const home = namespaces.find(
    (namespace) =>
      namespace.env === identity.namespace?.env && namespace.app === identity.namespace?.app,
  );
  if (!home) return "unknown";
  return home.allowed_auth_methods.includes(method) ? "allowed" : "rejected";
}

export function unavailableMethodReason(
  method: AuthMethod,
  availability: MethodAvailability,
): string {
  if (availability === "rejected") {
    return `The bound namespace does not accept ${methodLabel(method)} authentication.`;
  }
  if (availability === "unknown") {
    return "KMS Console could not verify the bound namespace authentication methods.";
  }
  return "";
}

export function methodLabel(method: AuthMethod): string {
  return method === "mtls" ? "mTLS" : "token";
}

/** Whether an identity holds a live credential of this method. */
export function identityUsesMethod(identity: Identity, method: AuthMethod): boolean {
  if (identity.disabled) return false;
  return method === "token" ? Boolean(identity.has_token) : validCertCount(identity) > 0;
}

/**
 * The active identities bound to `namespace` that would stop authenticating
 * if `method` were removed from its allowed methods. Admin and unbound
 * identities are never affected: the namespace rule only binds its own.
 */
export function identitiesRelyingOn(
  identities: readonly Identity[],
  namespace: NamespaceRef,
  method: AuthMethod,
): Identity[] {
  return identities.filter(
    (identity) =>
      identity.kind !== "admin" &&
      identity.namespace?.env === namespace.env &&
      identity.namespace?.app === namespace.app &&
      identityUsesMethod(identity, method),
  );
}
