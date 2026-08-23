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

export const links = {
  applications: (): string => "/applications",
  application: (name: string): string => `/applications?app=${encodeURIComponent(name)}`,
  namespaces: (): string => "/namespaces",
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
  releases: (opts?: { app?: string; env?: string; name?: string; tab?: "schemas" }): string => {
    const params: string[] = [];
    if (opts?.app) params.push(`app=${encodeURIComponent(opts.app)}`);
    if (opts?.env) params.push(`env=${encodeURIComponent(opts.env)}`);
    if (opts?.name) params.push(`name=${encodeURIComponent(opts.name)}`);
    if (opts?.tab) params.push(`tab=${opts.tab}`);
    return params.length > 0 ? `/releases?${params.join("&")}` : "/releases";
  },
};
