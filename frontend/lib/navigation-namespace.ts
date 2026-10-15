import { useRouter } from "next/router";
import { useEffect, useMemo, useRef, useState } from "react";
import { useAuth } from "@/context/AuthContext";
import { useNamespaces } from "./hooks";
import type { NamespaceRef, WorkspaceRef } from "./links";
import { lastNamespace, rememberNamespace, useLastNamespace } from "./namespace-memory";
import type { Identity, Namespace } from "./types";
import { queryValue } from "./url";

// The console's "current workspace" — the application (and environment, once
// chosen) an operator is working in — has one owner: this module. The URL is
// the source of truth on the pages below; session memory (lib/namespace-memory)
// carries the workspace across pages whose URL does not name one. Pages never
// write memory themselves: they put their selection in the URL, and the shell
// records it from there (see useNavigationNamespace).

/**
 * Pages whose `?app=&env=` names the workspace. Everywhere else those keys are
 * a filter or a prefill (the audit log, an identity binding) and must not
 * replace the workspace the operator was in.
 */
const WORKSPACE_ROUTES = ["/applications", "/parameters", "/secrets", "/releases"];

export function isWorkspaceRoute(pathname: string): boolean {
  return WORKSPACE_ROUTES.some((root) => pathname === root || pathname.startsWith(`${root}/`));
}

function clientBinding(identity: Identity | null | undefined): WorkspaceRef | null {
  const bound = identity?.kind === "client" ? identity.namespace : null;
  return bound ? { env: bound.env, app: bound.app } : null;
}

/**
 * The workspace a route is in. A workspace page that names an application is
 * followed (with its environment when it has one); a route that names none
 * falls back to memory. An explicit application that the loaded namespace
 * list does not contain yields null rather than an unrelated remembered
 * workspace. An empty list (still loading, failed, or nothing exists yet)
 * cannot contradict the URL, so the URL is trusted until it arrives.
 */
export function navigationNamespace(
  pathname: string,
  query: Record<string, string | string[] | undefined>,
  namespaces: readonly Namespace[],
  remembered: WorkspaceRef | null,
): WorkspaceRef | null {
  if (!isWorkspaceRoute(pathname)) return remembered;
  const app = queryValue(query.app);
  const env = queryValue(query.env);
  if (!app) return remembered;
  const known = namespaces.length > 0;
  const envs = namespaces.filter((ns) => ns.app === app).map((ns) => ns.env);
  if (known && envs.length === 0) return null;
  if (env) return !known || envs.includes(env) ? { env, app } : { app };
  // The same application without an environment in its URL (its overview
  // page, say) keeps the environment the operator was already in.
  if (remembered?.app === app && remembered.env && (!known || envs.includes(remembered.env))) {
    return remembered;
  }
  return { app };
}

/**
 * The current workspace, for links that should land where the operator was.
 * A client identity is bound to its namespace, so that binding always wins.
 * For an admin, the workspace the URL settles on is remembered here — the one
 * place memory is written while browsing — once the namespace list can
 * confirm it, so a mistyped link is never remembered.
 */
export function useNavigationNamespace(): WorkspaceRef | null {
  const router = useRouter();
  const { identity } = useAuth();
  const { namespaces } = useNamespaces();
  const remembered = useLastNamespace();
  const isClient = identity?.kind === "client";
  const scope = isClient
    ? clientBinding(identity)
    : router.isReady
      ? navigationNamespace(router.pathname ?? "", router.query, namespaces, remembered)
      : remembered;
  const app = scope?.app;
  const env = scope?.env;
  const confirmed = namespaces.length > 0;
  useEffect(() => {
    if (identity?.kind !== "admin" || !confirmed || !app) return;
    rememberNamespace(env ? { env, app } : { app });
  }, [identity?.kind, confirmed, app, env]);
  // One object per workspace, so consumers' memos do not churn every render.
  return useMemo(() => (app ? (env ? { env, app } : { app }) : null), [app, env]);
}

/**
 * The complete env/app pair of a workspace, or null while only an application
 * is chosen (key search needs both). Stable across renders.
 */
export function useCompleteNamespace(ws: WorkspaceRef | null): NamespaceRef | null {
  const app = ws?.app;
  const env = ws?.env;
  return useMemo(() => (app && env ? { env, app } : null), [app, env]);
}

/**
 * Resumes the current workspace on a bare arrival. When a workspace page opens
 * with neither `app` nor `env` in its URL (a typed address, a reload, a bare
 * "Back to…" link), the remembered workspace — or a client's binding — is
 * written into the URL once, with `router.replace`, and the page then seeds
 * from the URL exactly as it would for a deep link. This is the one sanctioned
 * effect-driven rewrite besides the schema-track pins in pages/applications:
 * it runs at most once per mount, before the operator has touched anything,
 * so it cannot fight a form (see lib/url.ts).
 *
 * Returns true while that rewrite is pending, so the page can hold its loading
 * state instead of flashing "Choose an environment".
 */
export function useRestoreWorkspace(pathname: string): boolean {
  const router = useRouter();
  const { identity } = useAuth();
  const remembered = useLastNamespace();
  const [done, setDone] = useState(false);
  const started = useRef(false);
  const bare = !queryValue(router.query.app) && !queryValue(router.query.env);
  const seed = identity?.kind === "client" ? clientBinding(identity) : remembered;
  useEffect(() => {
    if (started.current || !router.isReady) return;
    started.current = true;
    // Memory is read directly: the subscribed snapshot can still be the
    // server's (null) on the render where a static export's router turns ready.
    const target = identity?.kind === "client" ? clientBinding(identity) : lastNamespace();
    if (!bare || !target) {
      setDone(true);
      return;
    }
    const query: Record<string, string> = {};
    for (const [key, value] of Object.entries(router.query)) {
      const normalized = queryValue(value);
      if (normalized) query[key] = normalized;
    }
    query.app = target.app;
    if (target.env) query.env = target.env;
    void Promise.resolve(
      router.replace({ pathname, query }, undefined, { shallow: true, scroll: false }),
    )
      .catch(() => false)
      .finally(() => setDone(true));
  }, [bare, identity, pathname, router]);
  return !done && (!router.isReady || (bare && seed !== null));
}
