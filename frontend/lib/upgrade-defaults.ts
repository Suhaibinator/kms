import type { ApplicationContractField } from "./types";
import { validateContract } from "./validation";
export interface UpgradeDefaults {
  profile: string;
  schema_sha256: string;
  contract: ApplicationContractField[];
  parameters: Array<{ alias: string; content_type: string; value: string }>;
}
/** Decode only the envelope: encoded parameter strings retain exact numbers. */
export function parseUpgradeDefaults(text: string, schemaDigest: string): UpgradeDefaults {
  const raw: unknown = JSON.parse(text);
  const object = (v: unknown): v is Record<string, unknown> =>
    v !== null && typeof v === "object" && !Array.isArray(v);
  if (
    !object(raw) ||
    raw.format !== "kms-config-defaults/v1" ||
    typeof raw.profile !== "string" ||
    !Array.isArray(raw.contract) ||
    !Array.isArray(raw.parameters)
  )
    throw new Error("Choose a kms-config-defaults/v1 JSON artifact.");
  if (raw.schema_sha256 !== schemaDigest)
    throw new Error("The artifact schema digest does not match the selected schema.");
  const contract: ApplicationContractField[] = raw.contract.map((f: unknown) => {
    if (
      !object(f) ||
      typeof f.alias !== "string" ||
      (f.kind !== "parameter" && f.kind !== "secret") ||
      (f.kind === "parameter" && typeof f.content_type !== "string")
    )
      throw new Error("Invalid artifact contract.");
    return {
      alias: f.alias,
      kind: f.kind,
      ...(f.kind === "parameter" ? { content_type: f.content_type as string } : {}),
    };
  });
  const problem = validateContract(contract);
  if (problem) throw new Error(problem);
  const seen = new Set<string>();
  const parameters = raw.parameters.map((p: unknown) => {
    if (
      !object(p) ||
      typeof p.alias !== "string" ||
      typeof p.value !== "string" ||
      typeof p.content_type !== "string"
    )
      throw new Error("Artifact parameter values must be encoded strings.");
    const field = contract.find((f) => f.alias === p.alias);
    if (field?.kind !== "parameter" || field.content_type !== p.content_type || seen.has(p.alias))
      throw new Error(
        "Artifact parameter aliases must match the contract exactly; secrets cannot contain values.",
      );
    seen.add(p.alias);
    return { alias: p.alias, content_type: p.content_type, value: p.value };
  });
  if (parameters.length !== contract.filter((f) => f.kind === "parameter").length)
    throw new Error("The artifact is missing parameter values.");
  return { profile: raw.profile, schema_sha256: schemaDigest, contract, parameters };
}
