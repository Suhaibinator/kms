export const LIST_HEADERS = ["Application", "Environments", "Release", "Schema", "Contract"];

/** Matches `prod`, `prod-*`, and `production`, but not `reproduction` or `non-prod`. */
export const PRODUCTION_ENVIRONMENT = /^prod(-|$)|^production$/;

export interface QuickSecretSeed {
  environment: string;
  key: string;
}
