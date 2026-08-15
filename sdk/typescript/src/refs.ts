import { ConfigError, NoNamespaceError } from "./errors.js";

/** A fixed `(environment, application)` namespace. */
export interface NamespaceRef {
  readonly env: string;
  readonly app: string;
}

/** A fully qualified resource reference. */
export interface ResourceRef {
  readonly namespace: NamespaceRef;
  readonly key: string;
}

/** Read version selection. `version` is always a bigint to preserve uint64. */
export interface VersionRef {
  readonly version?: bigint;
  readonly label?: string;
}

export const CURRENT_VERSION: Readonly<VersionRef> = Object.freeze({});
export const UINT64_MAX = (1n << 64n) - 1n;

function immutableNamespace(env: string, app: string): NamespaceRef {
  return Object.freeze({ env, app });
}

function immutableRef(namespace: NamespaceRef, key: string): ResourceRef {
  return Object.freeze({ namespace: immutableNamespace(namespace.env, namespace.app), key });
}

/**
 * Parse the configuration form `env/app`.
 *
 * Validation is deliberately structural only. The server remains authoritative
 * for its naming character set, allowing a newer server to accept new names.
 */
export function parseNamespace(value: string): NamespaceRef {
  const slash = value.indexOf("/");
  if (slash <= 0 || slash === value.length - 1 || value.indexOf("/", slash + 1) !== -1) {
    throw new ConfigError(`invalid namespace ${JSON.stringify(value)}; expected "env/app"`);
  }
  return immutableNamespace(value.slice(0, slash), value.slice(slash + 1));
}

/** Parse the display path `/env/app/key`, preserving interior slashes in key. */
export function splitDisplayPath(path: string): ResourceRef {
  if (!path.startsWith("/")) {
    throw new ConfigError(
      `absolute key ${JSON.stringify(path)} must start with "/" and have the form "/env/app/key"`,
    );
  }

  const first = path.indexOf("/", 1);
  const second = first < 0 ? -1 : path.indexOf("/", first + 1);
  if (first <= 1 || second <= first + 1 || second === path.length - 1) {
    throw new ConfigError(`absolute key ${JSON.stringify(path)} must have the form "/env/app/key"`);
  }
  return immutableRef(
    immutableNamespace(path.slice(1, first), path.slice(first + 1, second)),
    path.slice(second + 1),
  );
}

/** Render an absolute, unambiguous display path. */
export function displayPath(ref: ResourceRef): string {
  return `/${ref.namespace.env}/${ref.namespace.app}/${ref.key}`;
}

/** Render a namespace in the SDK configuration form. */
export function displayNamespace(namespace: NamespaceRef): string {
  return `${namespace.env}/${namespace.app}`;
}

/** Resolve relative SDK sugar or split an already absolute display path. */
export function resolveRef(key: string, namespace: NamespaceRef | string | undefined): ResourceRef {
  if (key.startsWith("/")) return splitDisplayPath(key);
  if (namespace === undefined) throw new NoNamespaceError(key);
  const ns = typeof namespace === "string" ? parseNamespace(namespace) : namespace;
  if (ns.env.length === 0 || ns.app.length === 0) throw new NoNamespaceError(key);
  return immutableRef(ns, key);
}

/** Parse a trusted display key; malformed input degrades to a key-only ref. */
export function refOf(path: string): ResourceRef | undefined {
  try {
    return splitDisplayPath(path);
  } catch {
    return undefined;
  }
}

export function namespaceEquals(a: NamespaceRef, b: NamespaceRef): boolean {
  return a.env === b.env && a.app === b.app;
}

export function namespaceKey(namespace: NamespaceRef): string {
  return `${namespace.env}\0${namespace.app}`;
}

/**
 * Normalize selectors so an exact version wins over a label, matching the Go
 * SDK's documented `WithVersion` precedence.
 */
export function normalizeVersionRef(selector: VersionRef = CURRENT_VERSION): {
  readonly version: bigint;
  readonly label: string;
} {
  const version = selector.version ?? 0n;
  if (typeof version !== "bigint" || version < 0n || version > UINT64_MAX) {
    throw new ConfigError("version must be a bigint in the uint64 range");
  }
  return Object.freeze({
    version,
    label: version === 0n ? (selector.label ?? "") : "",
  });
}
