import type { StructuredSchemaDifference } from "./schema-diff";
import type {
  ApplicationContractField,
  ConfigurationReleaseEntry,
  ReleaseValidationError,
} from "./types";

export type UpgradeDraftField = ApplicationContractField & {
  id: number;
  fromAlias?: string;
  key: string;
  version?: number;
  versionText: string;
  value?: string;
  originalValue?: string;
  originalContentType?: string;
  loaded?: boolean;
  loading?: boolean;
  loadError?: string;
};
export interface UpgradeFieldChange {
  id: number;
  alias: string;
  labels: string[];
  paths: StructuredSchemaDifference[];
  problems: ReleaseValidationError[];
  attention: boolean;
  changed: boolean;
}
export function upgradeFieldChanges(
  fields: UpgradeDraftField[],
  contract: ApplicationContractField[],
  entries: ConfigurationReleaseEntry[],
  differences: StructuredSchemaDifference[],
  validation: ReleaseValidationError[],
): UpgradeFieldChange[] {
  return fields.map((field) => {
    const prior = contract.find((item) => item.alias === (field.fromAlias ?? field.alias));
    const pin = entries.find((item) => item.alias === field.fromAlias);
    const paths = differences.filter(
      (d) =>
        d.segments.length > 0 &&
        (d.segments[0] === field.alias || d.segments[0] === field.fromAlias),
    );
    const labels: string[] = [];
    if (!field.fromAlias) labels.push("Added");
    if (
      paths.length ||
      (prior &&
        (prior.kind !== field.kind || (prior.content_type ?? "") !== (field.content_type ?? "")))
    )
      labels.push("Schema changed");
    if (field.fromAlias && field.fromAlias !== field.alias) labels.push("Renamed");
    if (
      field.kind === "parameter" &&
      field.value !== undefined &&
      (field.originalValue !== undefined
        ? field.value !== field.originalValue ||
          (field.originalContentType !== undefined &&
            field.content_type !== field.originalContentType)
        : field.loaded === true)
    )
      labels.push("Value edited");
    if (pin && (pin.ref.key !== field.key || pin.version !== field.version))
      labels.push("Reference changed");
    const problems = validation.filter((p) => p.alias === field.alias);
    const badVersion = field.versionText
      ? !/^[1-9]\d*$/.test(field.versionText) || !Number.isSafeInteger(Number(field.versionText))
      : field.kind === "secret";
    const missing = field.kind === "parameter" && !field.version && !field.value;
    const attention = Boolean(
      problems.length ||
        field.loadError ||
        badVersion ||
        missing ||
        !field.alias.trim() ||
        !field.key.trim(),
    );
    if (attention) labels.unshift("Needs attention");
    return {
      id: field.id,
      alias: field.alias,
      labels,
      paths,
      problems,
      attention,
      changed: labels.length > 0,
    };
  });
}
export function removedUpgradeAliases(
  fields: UpgradeDraftField[],
  contract: ApplicationContractField[],
  entries: ConfigurationReleaseEntry[] = [],
): string[] {
  const retained = new Set(fields.map((f) => f.fromAlias ?? f.alias));
  return [...new Set([...contract, ...entries].map((field) => field.alias))].filter(
    (alias) => !retained.has(alias),
  );
}
export function orderUpgradeChanges(changes: UpgradeFieldChange[]): number[] {
  return [...changes]
    .sort(
      (a, b) =>
        Number(b.attention) - Number(a.attention) ||
        Number(b.changed) - Number(a.changed) ||
        a.alias.localeCompare(b.alias) ||
        a.id - b.id,
    )
    .map((c) => c.id);
}
