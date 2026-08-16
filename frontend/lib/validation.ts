// Client-side mirrors of the server's input validators.
//
// Every rule here has a counterpart in Go, cited above the rule. The server
// remains the authority: these functions exist to fail fast in the form, not to
// gate anything. Two consequences follow, and both matter when editing:
//
//  1. Never make a rule STRICTER than its Go counterpart. A frontend-only
//     restriction silently blocks input the API would have accepted, and there
//     is no way for the user to route around it. When a Go rule is hard to
//     reproduce exactly (float syntax, say), prefer the permissive reading and
//     let the server reject.
//  2. When a Go rule changes, change it here too. The `tests/validation.test.ts`
//     table cases are copied from the Go tests for exactly this reason.
//
// Each validator returns a human-readable message, or null when the value is
// acceptable. Empty-string handling is deliberately per-field: some fields are
// optional, so "required" checks live at the call site unless the Go validator
// itself rejects empty.

/** Max bytes for an env or app label (`keyutil.MaxLabelLength`). */
export const MAX_LABEL_LENGTH = 64;
/** Max bytes for a relative key (`keyutil.MaxKeyLength`). */
export const MAX_KEY_LENGTH = 256;
/** Max bytes for a release alias (`core.maxReleaseAliasBytes`). */
export const MAX_ALIAS_LENGTH = 64;
/** Max bytes for an identity name (`core.identityNameRe`). */
export const MAX_IDENTITY_NAME_LENGTH = 128;
/** Max bytes for a parameter or secret value (`core.maxValueBytes`). */
export const MAX_VALUE_BYTES = 1 << 20;

/** UTF-8 byte length. The Go limits count bytes, not UTF-16 code units. */
export function byteLength(s: string): number {
  return new TextEncoder().encode(s).length;
}

// --- namespace labels -------------------------------------------------------

// `keyutil.validateLabel`: 1–64 chars of [a-z0-9-], not starting or ending '-'.
function validateLabel(value: string, what: string): string | null {
  if (value === "") return `${what} is required.`;
  if (byteLength(value) > MAX_LABEL_LENGTH) {
    return `${what} exceeds ${MAX_LABEL_LENGTH} characters.`;
  }
  if (value.startsWith("-") || value.endsWith("-")) {
    return `${what} must not start or end with '-'.`;
  }
  const bad = [...value].find((ch) => !/[a-z0-9-]/.test(ch));
  if (bad !== undefined) {
    return `${what} must use lowercase letters, digits, and '-' only (found '${bad}').`;
  }
  return null;
}

/** Validates an environment name (`keyutil.ValidateEnv`). */
export function validateEnv(env: string): string | null {
  return validateLabel(env, "Environment");
}

/** Validates an application name (`keyutil.ValidateApp`). */
export function validateApp(app: string): string | null {
  return validateLabel(app, "Application");
}

/**
 * Validates an application record's name. Applications are named by the same
 * rule as the app half of a namespace (`core.normalizeApplication` calls
 * `keyutil.ValidateApp`).
 */
export function validateApplicationName(name: string): string | null {
  return validateLabel(name, "Name");
}

// --- relative keys ----------------------------------------------------------

// `keyutil.ValidateKey`: 1–256 chars; '/'-separated segments of [a-z0-9._-];
// no leading, trailing, empty, "." or ".." segment.
/** Validates a relative key within a namespace (`keyutil.ValidateKey`). */
export function validateKey(key: string): string | null {
  if (key === "") return "Key is required.";
  if (byteLength(key) > MAX_KEY_LENGTH) {
    return `Key exceeds ${MAX_KEY_LENGTH} characters.`;
  }
  if (key.startsWith("/") || key.endsWith("/")) {
    return "Key must not start or end with '/'.";
  }
  for (const segment of key.split("/")) {
    if (segment === "") return "Key must not contain an empty segment ('//').";
    if (segment === "." || segment === "..") {
      return `Key must not contain a '${segment}' segment.`;
    }
    const bad = [...segment].find((ch) => !/[a-z0-9._-]/.test(ch));
    if (bad !== undefined) {
      return `Key must use lowercase letters, digits, '.', '_', '-', and '/' only (found '${bad}').`;
    }
  }
  return null;
}

/**
 * Validates an optional key prefix used to scope a list request
 * (`core.validateListScope`). Empty means "the whole namespace" and is legal;
 * anything else must be a legal key.
 */
export function validateKeyPrefix(prefix: string): string | null {
  if (prefix === "") return null;
  return validateKey(prefix);
}

/**
 * Validates a release name. `core.normalizeApplication` runs the release name
 * through `keyutil.ValidateKey`, so it follows the key rule, not the label rule.
 * Empty is accepted here because the server defaults it to "runtime".
 */
export function validateReleaseName(name: string): string | null {
  if (name === "") return null;
  return validateKey(name);
}

// --- release aliases --------------------------------------------------------

// `core.releaseAliasRE` = ^[A-Za-z][A-Za-z0-9_-]*$, 1–64 bytes.
const ALIAS_RE = /^[A-Za-z][A-Za-z0-9_-]*$/;

/** Validates a release/contract alias (`core.releaseAliasRE`). */
export function validateAlias(alias: string): string | null {
  if (alias === "") return "Alias is required.";
  if (byteLength(alias) > MAX_ALIAS_LENGTH) {
    return `Alias exceeds ${MAX_ALIAS_LENGTH} characters.`;
  }
  if (!ALIAS_RE.test(alias)) {
    return "Alias must start with a letter and use letters, digits, '_', and '-' only.";
  }
  return null;
}

// --- identities -------------------------------------------------------------

// `core.identityNameRe` = ^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$
const IDENTITY_NAME_RE = /^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$/;

/** Validates an identity name (`core.identityNameRe`). */
export function validateIdentityName(name: string): string | null {
  if (name === "") return "Name is required.";
  if (name.length > MAX_IDENTITY_NAME_LENGTH) {
    return `Name exceeds ${MAX_IDENTITY_NAME_LENGTH} characters.`;
  }
  if (!IDENTITY_NAME_RE.test(name)) {
    return "Name must start with a letter or digit and use letters, digits, '.', '_', and '-' only.";
  }
  return null;
}

/** Identity kinds the server accepts (`domain.IdentityKind*`). */
export const IDENTITY_KINDS = ["client", "admin"] as const;
/** Authentication methods the server accepts (`domain.AuthMethod*`). */
export const AUTH_METHODS = ["mtls", "token"] as const;

// --- parameter content types and values -------------------------------------

/** Parameter content types the server accepts (`core.validParameterContentTypes`). */
export const PARAMETER_CONTENT_TYPES = [
  "string",
  "integer",
  "float",
  "boolean",
  "json",
  "binary",
] as const;

export type ParameterContentType = (typeof PARAMETER_CONTENT_TYPES)[number];

/** Validates a parameter content type (`core.validParameterContentTypes`). */
export function validateContentType(contentType: string): string | null {
  if (!(PARAMETER_CONTENT_TYPES as readonly string[]).includes(contentType)) {
    return `Content type must be one of ${PARAMETER_CONTENT_TYPES.join(", ")}.`;
  }
  return null;
}

const INT64_MIN = -(2n ** 63n);
const INT64_MAX = 2n ** 63n - 1n;

// Go's strconv.ParseBool accepts exactly these spellings — not "yes"/"no".
const GO_BOOL_LITERALS = new Set([
  "1",
  "t",
  "T",
  "TRUE",
  "true",
  "True",
  "0",
  "f",
  "F",
  "FALSE",
  "false",
  "False",
]);

// Go's strconv.ParseFloat accepts these non-finite spellings, case-insensitively,
// with an optional sign. JS `Number()` does not recognise the short "inf" form.
const GO_FLOAT_NONFINITE_RE = /^[+-]?(inf|infinity|nan)$/i;

// Go's strconv.ParseFloat also accepts hexadecimal floats, where the 'p' binary
// exponent is mandatory (0x1p-2). JS `Number()` reads "0x10" as an integer but
// rejects the p-exponent form outright, so it needs its own pattern.
const GO_FLOAT_HEX_RE = /^[+-]?0[xX](?:[0-9a-fA-F]*\.?[0-9a-fA-F]+|[0-9a-fA-F]+\.?)[pP][+-]?\d+$/;

// Standard base64 with required padding, matching base64.StdEncoding.
const BASE64_STD_RE = /^(?:[A-Za-z0-9+/]{4})*(?:[A-Za-z0-9+/]{2}==|[A-Za-z0-9+/]{3}=)?$/;

/**
 * Validates that a value parses as its declared content type, mirroring
 * `core.parseParameterValue`. The server applies the same check on write, so a
 * typo cannot be pushed to every subscribed application.
 *
 * Note the leniency asymmetry: `float` accepts anything JS `Number()` parses,
 * which is a superset of Go's grammar (JS takes `0x10`; Go wants `0x1p4`).
 * That is the safe direction — the server rejects what we let through.
 */
export function validateParameterValue(value: string, contentType: string): string | null {
  switch (contentType) {
    case "string":
      return null;
    case "integer": {
      const trimmed = value.trim();
      if (!/^[+-]?\d+$/.test(trimmed)) {
        return "Value must be a whole number.";
      }
      const n = BigInt(trimmed);
      if (n < INT64_MIN || n > INT64_MAX) {
        return "Value is out of range for a 64-bit signed integer.";
      }
      return null;
    }
    case "float": {
      const trimmed = value.trim();
      if (trimmed === "") return "Value must be a number.";
      if (GO_FLOAT_NONFINITE_RE.test(trimmed) || GO_FLOAT_HEX_RE.test(trimmed)) return null;
      if (Number.isNaN(Number(trimmed))) return "Value must be a number.";
      return null;
    }
    case "boolean":
      return GO_BOOL_LITERALS.has(value.trim())
        ? null
        : "Value must be true or false (also accepted: 1, 0, t, f).";
    case "json":
      try {
        JSON.parse(value);
        return null;
      } catch (err) {
        return `Value must be valid JSON: ${err instanceof Error ? err.message : String(err)}`;
      }
    case "binary":
      // Go's base64 decoder ignores CR and LF, so wrapped base64 — how key and
      // certificate material is almost always pasted — is valid input. Strip
      // those before matching, but nothing else: the decoder rejects a leading
      // space, and this value is NOT trimmed before the server decodes it.
      return BASE64_STD_RE.test(value.replace(/[\r\n]/g, ""))
        ? null
        : "Value must be standard base64 (A–Z, a–z, 0–9, '+', '/', padded with '=').";
    default:
      return validateContentType(contentType);
  }
}

/** Rejects a value larger than the server's per-value cap (`core.maxValueBytes`). */
export function validateValueSize(value: string): string | null {
  const size = byteLength(value);
  if (size > MAX_VALUE_BYTES) {
    return `Value is ${formatBytes(size)}, over the ${formatBytes(MAX_VALUE_BYTES)} limit.`;
  }
  return null;
}

function formatBytes(n: number): string {
  if (n >= 1 << 20) return `${(n / (1 << 20)).toFixed(1)} MiB`;
  if (n >= 1 << 10) return `${(n / (1 << 10)).toFixed(1)} KiB`;
  return `${n} bytes`;
}

/**
 * Validates user-supplied metadata (`core.validateMetadataJSON`). Empty is legal
 * and normalizes to "{}" server-side. Anything else must be a JSON *object* —
 * arrays, scalars, and the literal `null` are all rejected there.
 */
export function validateMetadataJson(metadata: string): string | null {
  if (metadata.trim() === "") return null;
  let parsed: unknown;
  try {
    parsed = JSON.parse(metadata);
  } catch (err) {
    return `Metadata must be valid JSON: ${err instanceof Error ? err.message : String(err)}`;
  }
  if (parsed === null || typeof parsed !== "object" || Array.isArray(parsed)) {
    return "Metadata must be a JSON object.";
  }
  return null;
}

// --- policies ---------------------------------------------------------------

/** Every operation the policy engine knows (`policy.knownOperations`). */
export const KNOWN_OPERATIONS = [
  "parameter:read",
  "parameter:write",
  "parameter:list",
  "parameter:delete",
  "secret:read",
  "secret:write",
  "secret:list",
  "secret:disable",
  "secret:destroy",
  "secret:promote",
  "configuration-release:create",
  "configuration-release:read",
  "configuration-release:validate",
  "configuration-release:activate",
  "configuration-release:list",
  "configuration-release:watch",
  "admin:namespace:create",
  "admin:namespace:update",
  "admin:namespace:delete",
  "admin:identity:cert",
  "admin:policy:write",
  "admin:audit:read",
  "admin:key:rotate",
] as const;

/** Wildcard categories accepted as `<category>:*` (`policy.validOperationPattern`). */
export const OPERATION_WILDCARD_CATEGORIES = [
  "parameter",
  "secret",
  "configuration-release",
  "admin",
] as const;

/** Every legal operation pattern, for populating a picker. */
export const OPERATION_PATTERNS = [
  "*",
  ...OPERATION_WILDCARD_CATEGORIES.map((c) => `${c}:*`),
  ...KNOWN_OPERATIONS,
] as const;

/** Validates a policy rule's operation pattern (`policy.validOperationPattern`). */
export function validatePolicyOperation(operation: string): string | null {
  if (operation === "") return "Operation is required.";
  if (operation === "*") return null;
  if (operation.endsWith(":*")) {
    const category = operation.slice(0, -2);
    return (OPERATION_WILDCARD_CATEGORIES as readonly string[]).includes(category)
      ? null
      : `Unknown wildcard category '${category}'. Use one of ${OPERATION_WILDCARD_CATEGORIES.join(", ")}.`;
  }
  return (KNOWN_OPERATIONS as readonly string[]).includes(operation)
    ? null
    : `Unknown operation '${operation}'.`;
}

/**
 * Validates the env or app component of a policy rule (`policy.normalizeLabel`).
 * Empty and "*" both mean "any" and are legal; anything else must be a label.
 */
export function validatePolicyRuleEnv(env: string): string | null {
  if (env === "" || env === "*") return null;
  return validateEnv(env);
}

/** See {@link validatePolicyRuleEnv}. */
export function validatePolicyRuleApp(app: string): string | null {
  if (app === "" || app === "*") return null;
  return validateApp(app);
}

/** Validates a policy name (`policy.ValidateRules` requires it non-empty). */
export function validatePolicyName(name: string): string | null {
  return name.trim() === "" ? "Policy name is required." : null;
}

/**
 * Validates a policy subject (`policy.ValidateRules` requires it non-empty).
 * "*" matches every subject; otherwise it names an identity.
 */
export function validatePolicySubject(subject: string): string | null {
  if (subject.trim() === "") return "Subject is required.";
  if (subject === "*") return null;
  return validateIdentityName(subject);
}

// --- application contracts --------------------------------------------------

/** Contract entry kinds (`domain.ReleaseEntry*`). */
export const CONTRACT_KINDS = ["parameter", "secret"] as const;

/**
 * Validates one application contract field (`core.normalizeApplication`).
 * A parameter entry must pin a valid content type; a secret entry must not
 * carry one (the server clears it).
 */
export function validateContractField(field: {
  alias: string;
  kind: string;
  content_type?: string;
}): string | null {
  const aliasErr = validateAlias(field.alias);
  if (aliasErr) return aliasErr;
  if (field.kind === "parameter") {
    return validateContentType(field.content_type ?? "");
  }
  if (field.kind === "secret") return null;
  return `Kind must be 'parameter' or 'secret' (found '${field.kind}').`;
}

/**
 * Validates a whole contract, including the cross-entry duplicate-alias rule
 * the server enforces in `core.normalizeApplication`.
 */
export function validateContract(
  fields: Array<{ alias: string; kind: string; content_type?: string }>,
): string | null {
  const seen = new Set<string>();
  for (const field of fields) {
    const err = validateContractField(field);
    if (err) return err;
    if (seen.has(field.alias)) return `Duplicate contract alias '${field.alias}'.`;
    seen.add(field.alias);
  }
  return null;
}

/**
 * Validates the schema pin. `core.normalizeApplication` requires schema_id and
 * schema_version to be set together or not at all.
 */
export function validateSchemaPin(schemaId: string, schemaVersion: string): string | null {
  const hasId = schemaId.trim() !== "";
  const hasVersion = schemaVersion.trim() !== "" && schemaVersion.trim() !== "0";
  if (hasId === hasVersion) {
    if (hasVersion && !/^\d+$/.test(schemaVersion.trim())) {
      return "Schema version must be a positive whole number.";
    }
    return null;
  }
  return hasId
    ? "Schema version is required when a schema ID is set."
    : "Schema ID is required when a schema version is set.";
}

// --- helpers ----------------------------------------------------------------

/** Returns the first non-null message, or null when everything passes. */
export function firstError(...results: Array<string | null>): string | null {
  return results.find((r) => r !== null) ?? null;
}
