import { PRODUCTION_ENVIRONMENT } from "@/lib/readiness";

// Kept for older imports; new code calls isProductionEnvironment from lib/readiness.
export { PRODUCTION_ENVIRONMENT };

export const LIST_HEADERS = ["Application", "Environments", "Release", "Schema", "Contract"];

export interface QuickSecretSeed {
  environment: string;
  key: string;
  /** Seeded from a sibling environment's value for the same alias, when known. */
  contentType?: string;
}

/** What the Add-environment form hands over when the user picks "Copy values from…". */
export interface CloneSeed {
  source: string;
  target: string;
  description: string;
  methods: ("mtls" | "token")[];
}
