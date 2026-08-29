// Prop contracts shared by the application, ship, onboarding and palette
// lanes (plan §3.3). Frozen on day 0 so every lane compiles against the same
// shapes; the components themselves start as null-rendering stubs.

import type {
  Application,
  ApplicationOverview,
  CloneEnvironmentResponse,
  EnvironmentOverview,
  HealthResponse,
  NamespaceRef,
  OverviewActiveRelease,
  RollbackResponse,
  ShipResult,
} from "@/lib/types";
import { validateContractField } from "@/lib/validation";

/** What a setup-panel step (or a finding's Fix) asks the application page to do. */
export type SetupAction =
  | { kind: "create-app" }
  | { kind: "edit-definition" }
  | { kind: "register-schema" }
  | { kind: "add-environment" }
  | { kind: "fill-values"; env: string; alias?: string }
  | { kind: "ship"; env: string }
  | { kind: "connect"; env?: string };

export interface ShipModalProps {
  application: Application;
  environments: EnvironmentOverview[];
  /** Pinned schema JSON; enables the field-by-field editor for json aliases. */
  schemaJson?: string;
  /** Defaults to the focused column, else the first non-prod environment. */
  initialEnvironment?: string;
  /** Prefills one row (`?ship=alias`, the Values row's Edit & ship). */
  initialAlias?: string;
  open: boolean;
  onClose: () => void;
  /** Called on every terminal outcome so the page can reload its overview. */
  onShipped: (result: ShipResult) => void;
  /** A secret alias needs a value first; opens the quick-secret modal. */
  onAddSecret: (env: string, alias: string) => void;
}

export interface RollbackDialogProps {
  namespace: NamespaceRef;
  name: string;
  /** The active release as the caller last saw it (its version is the CAS guard). */
  active: OverviewActiveRelease | null;
  open: boolean;
  onClose: () => void;
  onDone: (result: RollbackResponse) => void;
}

export interface CloneEnvironmentModalProps {
  application: Application;
  environments: EnvironmentOverview[];
  open: boolean;
  onClose: () => void;
  onCreated: (result: CloneEnvironmentResponse) => void;
}

export interface CreateApplicationWizardProps {
  open: boolean;
  onClose: () => void;
  onCreated: (application: Application) => void;
}

export interface SetupPanelProps {
  overview: ApplicationOverview;
  onAction: (action: SetupAction) => void;
}

export interface ConnectSdkPanelProps {
  namespace: NamespaceRef;
  releaseName: string;
  /** Contract aliases; the snippet reads the first one. */
  aliases: string[];
  /** null while health has not loaded; `tls_enabled: false` shows a warning. */
  health: HealthResponse | null;
}

export interface CommandPaletteProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
}

// --- contract editor helpers ---------------------------------------------------

/**
 * One validation message per contract row, or null where the row is fine.
 * `validateContract` in lib/validation stops at the first failure; the editor
 * needs every row's problem so it can mark each offending input. A duplicate
 * alias is reported on the later row, the one that introduced it.
 */
export function contractProblems(
  fields: ReadonlyArray<{ alias: string; kind: string; content_type?: string }>,
): Array<string | null> {
  const seen = new Set<string>();
  return fields.map((field) => {
    const problem = validateContractField(field);
    if (problem) return problem;
    if (seen.has(field.alias)) return `Duplicate contract alias '${field.alias}'.`;
    seen.add(field.alias);
    return null;
  });
}
