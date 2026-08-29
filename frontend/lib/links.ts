// The console's internal URLs, built in one place so every page links to the
// same shape. Param order and encoding are load-bearing: tests assert the
// emitted strings verbatim.

export interface NamespaceRef {
  env: string;
  app: string;
}

export interface ResourceRef extends NamespaceRef {
  key: string;
}

function nsQuery(ns: NamespaceRef): string {
  return `env=${encodeURIComponent(ns.env)}&app=${encodeURIComponent(ns.app)}`;
}

function refQuery(ref: ResourceRef): string {
  return `${nsQuery(ref)}&key=${encodeURIComponent(ref.key)}`;
}

function listLink(base: string, ns?: NamespaceRef, keyPrefix?: string): string {
  if (!ns) return base;
  const prefix = keyPrefix ? `&key_prefix=${encodeURIComponent(keyPrefix)}` : "";
  return `${base}?${nsQuery(ns)}${prefix}`;
}

export interface ApplicationLinkOptions {
  /** Focus (scroll to) this environment's pipeline column. */
  env?: string;
  /** Open the Ship modal: an alias prefills a row, `true` opens it empty. */
  ship?: string | boolean;
  /** `matrix` swaps the pipeline for the classic per-key table. */
  tab?: "matrix";
  /** Open the Roll back environment menu. */
  rollback?: boolean;
}

export const links = {
  overview: (): string => "/",
  applications: (): string => "/applications",
  // Param order is app, env, ship, tab, rollback — the application page reads
  // all five and the palette/overview deep-link into every combination.
  application: (name: string, opts?: ApplicationLinkOptions): string => {
    const params = [`app=${encodeURIComponent(name)}`];
    if (opts?.env) params.push(`env=${encodeURIComponent(opts.env)}`);
    if (opts?.ship) {
      params.push(`ship=${opts.ship === true ? "1" : encodeURIComponent(opts.ship)}`);
    }
    if (opts?.tab) params.push(`tab=${opts.tab}`);
    if (opts?.rollback) params.push("rollback=1");
    return `/applications?${params.join("&")}`;
  },
  namespaces: (): string => "/namespaces",
  // `env`/`app` prefill the binding; `new` opens the create form directly;
  // `name` points at one identity (subscriber rows link here).
  identities: (opts?: { env?: string; app?: string; new?: boolean; name?: string }): string => {
    const params: string[] = [];
    if (opts?.env) params.push(`env=${encodeURIComponent(opts.env)}`);
    if (opts?.app) params.push(`app=${encodeURIComponent(opts.app)}`);
    if (opts?.new) params.push("new=1");
    if (opts?.name) params.push(`name=${encodeURIComponent(opts.name)}`);
    return params.length > 0 ? `/identities?${params.join("&")}` : "/identities";
  },
  subscribers: (): string => "/subscribers",
  health: (): string => "/health",
  audit: (): string => "/audit",
  /**
   * Where an audit row's resource lives, keyed off the backend's
   * `resource_type` (internal/domain: parameter, secret, configuration_release,
   * namespace, …). A namespace-only resource goes to the application page
   * focused on that environment; unknown shapes return null and render as text.
   */
  auditResource: (event: {
    resource_type?: string;
    resource_env?: string;
    resource_app?: string;
    resource_key?: string;
  }): string | null => {
    const env = event.resource_env ?? "";
    const app = event.resource_app ?? "";
    const key = event.resource_key ?? "";
    if (!env || !app) return null;
    const type = event.resource_type ?? "";
    if (key) {
      if (type === "secret") return links.secretDetail({ env, app, key });
      if (type === "parameter") return links.parameterDetail({ env, app, key });
      if (type === "configuration_release") return links.releases({ app, env, name: key });
      return null;
    }
    return links.application(app, { env });
  },
  secrets: (ns?: NamespaceRef, keyPrefix?: string): string => listLink("/secrets", ns, keyPrefix),
  secretDetail: (ref: ResourceRef): string => `/secrets/detail?${refQuery(ref)}`,
  newSecret: (ns?: NamespaceRef, key?: string): string => {
    if (!ns) return "/secrets/new";
    const keyPart = key ? `&key=${encodeURIComponent(key)}` : "";
    return `/secrets/new?${nsQuery(ns)}${keyPart}`;
  },
  parameters: (ns?: NamespaceRef, keyPrefix?: string): string =>
    listLink("/parameters", ns, keyPrefix),
  parameterDetail: (ref: ResourceRef): string => `/parameters/detail?${refQuery(ref)}`,
  releases: (opts?: {
    app?: string;
    env?: string;
    name?: string;
    tab?: "schemas";
    /** Open one release's workspace, as `name@version` (e.g. `runtime@12`). */
    release?: string;
  }): string => {
    const params: string[] = [];
    if (opts?.app) params.push(`app=${encodeURIComponent(opts.app)}`);
    if (opts?.env) params.push(`env=${encodeURIComponent(opts.env)}`);
    if (opts?.name) params.push(`name=${encodeURIComponent(opts.name)}`);
    if (opts?.tab) params.push(`tab=${opts.tab}`);
    if (opts?.release) params.push(`release=${encodeURIComponent(opts.release)}`);
    return params.length > 0 ? `/releases?${params.join("&")}` : "/releases";
  },
};
