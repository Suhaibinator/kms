// The console's vocabulary, in one place: what each typed identifier chip
// means (tooltips), the concepts the guided ship flow explains, and the
// bounded rejection categories a managed client can report. Copy follows
// docs/configuration-releases.md and docs/managed-go-configuration.md.

export type IdentKind =
  | "app"
  | "env"
  | "ns"
  | "alias"
  | "key"
  | "release"
  | "schema"
  | "version"
  | "revision"
  | "identity"
  | "instance";

export interface IdentGlossaryEntry {
  /** Short prefix rendered before the value (`env prod`); "" for none. */
  prefix: string;
  /** The term's display name. */
  term: string;
  /** One line, in the voice of the concept doc. */
  definition: string;
}

export const IDENT_GLOSSARY: Record<IdentKind, IdentGlossaryEntry> = {
  app: {
    prefix: "app",
    term: "Application",
    definition:
      "The environment-independent owner of a configuration shape: a release name, an optional schema pin, and the contract of aliases it reads.",
  },
  env: {
    prefix: "env",
    term: "Environment",
    definition:
      "One isolated deployment of an application (dev, staging, prod…). Environments never inherit values or share versions.",
  },
  ns: {
    prefix: "ns",
    term: "Namespace",
    definition:
      "An (env, app) pair — the unit of isolation and authorization. Every parameter, secret and release belongs to exactly one.",
  },
  alias: {
    prefix: "alias",
    term: "Alias",
    definition:
      "The name an application reads a value by. The contract lists every alias; a release maps each one to an exact resource version.",
  },
  key: {
    prefix: "key",
    term: "Key",
    definition:
      "A parameter or secret's address inside its namespace. Usually equal to the alias, but a release may point an alias at any key.",
  },
  release: {
    prefix: "release",
    term: "Release",
    definition:
      "An immutable manifest, name@version, pinning every contract alias to an exact resource version. Activating it is what clients see.",
  },
  schema: {
    prefix: "schema",
    term: "Schema",
    definition:
      "A registered JSON Schema (id@version) a release is validated against before activation. Only parameter aliases enter the validated object.",
  },
  version: {
    prefix: "",
    term: "Version",
    definition:
      "An immutable numbered snapshot of a parameter, secret or release. Writing never changes an existing version; it creates the next one.",
  },
  revision: {
    prefix: "rev",
    term: "Activation revision",
    definition:
      "The store-wide change counter at the moment a release was activated. Subscribers acknowledge against it, so it tells you who has caught up.",
  },
  identity: {
    prefix: "identity",
    term: "Identity",
    definition:
      "Who is calling: an admin, or a client identity bound to one namespace and authenticated by token or mTLS.",
  },
  instance: {
    prefix: "instance",
    term: "Instance",
    definition:
      "One running process of a subscribed client, named by client name and instance id. Each reports its own lifecycle state per release.",
  },
};

export type ConceptKey =
  | "contract"
  | "alias"
  | "key"
  | "schema"
  | "release"
  | "pin"
  | "activation_revision"
  | "current_label"
  | "previous_label"
  | "drift"
  | "subscriber"
  | "instance"
  | "applied"
  | "rejected"
  | "binding_key"
  | "rollback"
  | "cas";

export interface Concept {
  term: string;
  definition: string;
}

export const CONCEPTS: Record<ConceptKey, Concept> = {
  contract: {
    term: "Contract",
    definition:
      "The list of aliases an application reads, each with a kind (parameter or secret) and, for parameters, a content type. Every release must match it exactly.",
  },
  alias: {
    term: "Alias",
    definition: IDENT_GLOSSARY.alias.definition,
  },
  key: {
    term: "Key",
    definition: IDENT_GLOSSARY.key.definition,
  },
  schema: {
    term: "Schema",
    definition: IDENT_GLOSSARY.schema.definition,
  },
  release: {
    term: "Release",
    definition: IDENT_GLOSSARY.release.definition,
  },
  pin: {
    term: "Pin",
    definition:
      "A release entry's exact version of a resource. Pins never move: a newer parameter version is only served once a new release pins it.",
  },
  activation_revision: {
    term: "Activation revision",
    definition: IDENT_GLOSSARY.revision.definition,
  },
  current_label: {
    term: "current",
    definition:
      "The movable label on a resource's newest enabled version. Ship uses it as the base when nothing is active yet.",
  },
  previous_label: {
    term: "previous",
    definition:
      "The movable label on the version that was current before the last write or promotion. Roll back re-activates the previous release, not this label.",
  },
  drift: {
    term: "Unreleased change",
    definition:
      "A resource has a newer version than the one the active release pins. Clients keep serving the pinned version until a release includes the change.",
  },
  subscriber: {
    term: "Subscriber",
    definition:
      "A client streaming a release name from one namespace. It receives every activation and reports how far it got with each.",
  },
  instance: {
    term: "Instance",
    definition: IDENT_GLOSSARY.instance.definition,
  },
  applied: {
    term: "Applied",
    definition:
      "The instance verified the release digest, fetched every pin, validated the candidate and swapped it in. It is now serving this release.",
  },
  rejected: {
    term: "Rejected",
    definition:
      "The instance could not adopt the release and keeps serving its last-known-good. The bounded category says why; the diagnostic is local detail.",
  },
  binding_key: {
    term: "Binding key",
    definition:
      "An operator-owned string that adds a second wrapping layer to one secret version. KMS never stores, hashes, or fingerprints it.",
  },
  rollback: {
    term: "Roll back",
    definition:
      "Re-activate the previous release version for this name. Nothing is deleted; the rolled-back version stays available to re-activate.",
  },
  cas: {
    term: "Expected version",
    definition:
      "Activation and rollback carry the version you saw as active. If someone else activated meanwhile, the server refuses (409) instead of overwriting their change.",
  },
};

export type RejectionCategory =
  | "resolution_failed"
  | "token_unavailable"
  | "version_mismatch"
  | "digest_mismatch"
  | "prepare_failed"
  | "config_contract_mismatch"
  | "config_decode_failed"
  | "config_validation_failed"
  | "default_mismatch"
  | "restart_required"
  | "superseded"
  | "active_check_failed"
  | "internal";

export interface RejectionGuidance {
  /** What went wrong, in one line. */
  summary: string;
  /** Operator response, from docs/managed-go-configuration.md §Diagnose. */
  response: string;
}

export const REJECTION_CATEGORIES: Record<RejectionCategory, RejectionGuidance> = {
  resolution_failed: {
    summary: "A pinned resource could not be fetched.",
    response:
      "Revalidate that every exact pin exists, is readable, and is authorized for the application identity.",
  },
  token_unavailable: {
    summary: "A protected secret needs a local credential the instance does not have.",
    response:
      "Provision the exact version's missing access token through SecretTokenProvider or its binding key through the secret declaration; never put either credential in the release.",
  },
  version_mismatch: {
    summary: "A fetched resource did not match the pinned version.",
    response:
      "Treat the pin or returned resource as inconsistent; validate again and investigate the server or storage before activating another release.",
  },
  digest_mismatch: {
    summary: "A fetched parameter did not match the pinned digest.",
    response:
      "Treat the pin or returned resource as inconsistent; validate again and investigate the server or storage before activating another release.",
  },
  prepare_failed: {
    summary: "Preparation failed without a more specific category.",
    response:
      "Inspect local application logs and the deployed build; managed configuration normally emits a more specific category.",
  },
  config_contract_mismatch: {
    summary: "The release shape does not match the generated contract.",
    response:
      "Compare aliases, kinds, and literal json content types with the generated contract. This check happens before resource fetches.",
  },
  config_decode_failed: {
    summary: "A group document could not be decoded into the application's types.",
    response:
      "Publish a new complete group document fixing missing, unknown, duplicate, mistyped, out-of-range, or noncanonical values.",
  },
  config_validation_failed: {
    summary: "The application's own Validate rejected the candidate.",
    response:
      "Check the application's local Validate diagnostic and fix either the complete candidate or application validation.",
  },
  default_mismatch: {
    summary: "Source-owned defaults differ from what the release carries.",
    response:
      "The instance keeps running the release. Adopt the release values in code (the CI defaults check flags the drift) or restore the source-owned defaults with a new release.",
  },
  restart_required: {
    summary: "The change is restart-bound; running replicas keep last-known-good.",
    response:
      "Keep last-known-good serving and roll or restart replicas that are intended to adopt the restart-bound change.",
  },
  superseded: {
    summary: "A newer activation replaced this candidate before it finished.",
    response: "Usually no action; a newer activation replaced preparation of this candidate.",
  },
  active_check_failed: {
    summary: "The final freshness read against KMS failed.",
    response:
      "Check KMS connectivity and authorization for the final freshness read. After readiness the loader reconciles again; if startup returned an error, restart Start after recovery.",
  },
  internal: {
    summary: "An internal error in the managed loader.",
    response:
      "Inspect local application logs and the deployed build; managed configuration normally emits a more specific category.",
  },
};

export function rejectionGuidance(category: string): RejectionGuidance {
  return (
    (REJECTION_CATEGORIES as Record<string, RejectionGuidance>)[category] ?? {
      summary: "The instance rejected the release.",
      response: "Inspect the instance's diagnostic and local application logs.",
    }
  );
}

export type ShipStepId = "change" | "preview" | "ship" | "rollout";

export interface ShipStep {
  id: ShipStepId;
  title: string;
  blurb: string;
}

export const SHIP_STEPS: readonly ShipStep[] = [
  {
    id: "change",
    title: "Change",
    blurb:
      "Pick the environment and edit values by alias. Each edit becomes a new immutable parameter version; secrets are pinned, never typed here.",
  },
  {
    id: "preview",
    title: "Preview",
    blurb:
      "A dry run builds the next release from the active pins plus your changes and validates it against the schema. Nothing is written yet.",
  },
  {
    id: "ship",
    title: "Ship",
    blurb:
      "Writes the values, creates the release and activates it — guarded by the version you previewed against, so a concurrent change is refused, not overwritten.",
  },
  {
    id: "rollout",
    title: "Rollout",
    blurb:
      "Subscribed instances fetch, validate and apply the release. Watch them reach applied; a rejected instance keeps serving the previous release.",
  },
];
