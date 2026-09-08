/** JSON and URL schema selectors must survive JavaScript number conversion exactly. */
export function schemaVersionError(value: unknown): string | null {
  return typeof value === "number" && Number.isSafeInteger(value) && value >= 0
    ? null
    : "Schema version must be a nonnegative safe integer (0 selects schema-free).";
}

export function parseSchemaVersion(value: string | null | undefined): number | undefined {
  if (value === null || value === undefined || value === "") return undefined;
  if (!/^\d+$/.test(value) || schemaVersionError(Number(value))) return undefined;
  return Number(value);
}
