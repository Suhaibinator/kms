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
