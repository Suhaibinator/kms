import { useRouter } from "next/router";
import { useEffect } from "react";
import { useAuth } from "@/context/AuthContext";
import { useNamespaces } from "./hooks";
import { type NamespaceRef, rememberNamespace, useLastNamespace } from "./namespace-memory";
import type { Namespace } from "./types";
import { queryValue } from "./url";

/** An explicit route must never fall back to an unrelated remembered workspace. */
export function navigationNamespace(
  query: Record<string, string | string[] | undefined>,
  namespaces: readonly Namespace[],
  remembered: NamespaceRef | null,
): NamespaceRef | null {
  const app = queryValue(query.app);
  const env = queryValue(query.env);
  if (app && env) return namespaces.find((ns) => ns.app === app && ns.env === env) ?? null;
  if (env || (app && app !== remembered?.app)) return null;
  return remembered;
}

export function useNavigationNamespace(): NamespaceRef | null {
  const router = useRouter();
  const { identity } = useAuth();
  const { namespaces } = useNamespaces();
  const remembered = useLastNamespace();
  const scope =
    identity?.kind === "client"
      ? (identity.namespace ?? null)
      : router.isReady
        ? navigationNamespace(router.query, namespaces, remembered)
        : null;
  const app = scope?.app;
  const env = scope?.env;
  useEffect(() => {
    if (identity?.kind === "admin" && app && env) rememberNamespace({ app, env });
  }, [identity?.kind, app, env]);
  return scope;
}
