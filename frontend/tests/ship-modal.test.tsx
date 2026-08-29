import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { ShipModalProps } from "@/components/applications/contracts";
import { PREVIEW_DEBOUNCE_MS, SHIP_MODE_STORAGE_KEY } from "@/components/ship/model";
import ShipModal from "@/components/ship/ShipModal";
import { ApiError } from "@/lib/api";
import type {
  ApplicationOverview,
  EnvironmentOverview,
  ReleaseSubscriberState,
  ShipRequest,
  ShipResult,
} from "@/lib/types";
import incidentJson from "./fixtures/backend/overview-incident.json";
import conflictJson from "./fixtures/backend/ship-conflict.json";
import previewJson from "./fixtures/backend/ship-preview.json";

const mocks = vi.hoisted(() => ({
  ship: vi.fn(),
  getParameter: vi.fn(),
  activateRelease: vi.fn(),
  releaseSubscribers: vi.fn(),
  subscriberStream: vi.fn(),
  validateRelease: vi.fn(),
  rollbackRelease: vi.fn(),
}));

vi.mock("@/lib/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/api")>();
  return {
    ...actual,
    api: {
      ...actual.api,
      ship: mocks.ship,
      getParameter: mocks.getParameter,
      activateRelease: mocks.activateRelease,
      releaseSubscribers: mocks.releaseSubscribers,
      subscriberStream: mocks.subscriberStream,
      validateRelease: mocks.validateRelease,
      rollbackRelease: mocks.rollbackRelease,
    },
  };
});

const incident = incidentJson as unknown as ApplicationOverview;
const preview = previewJson as unknown as ShipResult;
const conflict = conflictJson as unknown as ShipResult;

// Everything below is derived from the fixtures so a regenerated backend
// fixture moves the expectations with it.
const app = incident.application;
const dev = incident.environments.find((env) => env.namespace.env === "dev") as EnvironmentOverview;
const prod = incident.environments.find(
  (env) => env.namespace.env === "prod",
) as EnvironmentOverview;
const releaseName = app.release_name;
const rateLimitsType =
  app.contract.find((field) => field.alias === "rate_limits")?.content_type ?? "string";
const sample = (contentType: string, seed: number): string =>
  contentType === "json"
    ? `{"per_minute": ${seed}}`
    : contentType === "boolean"
      ? seed % 2 === 0
        ? "true"
        : "false"
      : String(seed);
const EDIT_A = sample(rateLimitsType, 200);
const EDIT_B = sample(rateLimitsType, 300);
const CURRENT = sample(rateLimitsType, 100);
const base = preview.preview.base_version;
const next = base + 1;
const written = preview.preview.entries.find((entry) => entry.change === "edited")?.to_version ?? 0;
const prodRateLimits = prod.values.find((value) => value.alias === "rate_limits");
const conflictCurrent = conflict.error?.current_version ?? 0;
const conflictWritten = conflict.parameters[0]?.version ?? 0;
const conflictRelease = conflict.release?.version ?? 0;

const activated: ShipResult = {
  status: "activated",
  preview: preview.preview,
  parameters: [{ alias: "rate_limits", key: "rate_limits", version: written, revision: 118 }],
  release: { name: releaseName, version: next, digest: "sha256:abc" },
  activation: { activation_revision: 119, previous_version: base, changed: true },
};

const rejectedInstance: ReleaseSubscriberState = {
  namespace: { env: "dev", app: app.name },
  release_name: releaseName,
  client_name: "grader-api",
  instance_id: "grader-api-3",
  identity: "gradethis-dev",
  state: "rejected",
  release_version: base,
  activation_revision: 119,
  rejection_category: "config_validation_failed",
  diagnostic: "rate_limits.per_minute must be greater than zero",
  client_timestamp_unix_ms: 1,
  server_timestamp_unix_ms: 1,
  applied_divergent: false,
  divergent_field_count: 0,
  connected: true,
};

const appliedInstance: ReleaseSubscriberState = {
  ...rejectedInstance,
  instance_id: "grader-api-1",
  state: "applied",
  release_version: next,
  rejection_category: "",
  diagnostic: "",
};

function renderModal(overrides: Partial<ShipModalProps> = {}) {
  const props: ShipModalProps = {
    application: app,
    environments: incident.environments,
    initialEnvironment: "dev",
    initialAlias: "rate_limits",
    open: true,
    onClose: vi.fn(),
    onShipped: vi.fn(),
    onAddSecret: vi.fn(),
    ...overrides,
  };
  const view = render(<ShipModal {...props} />);
  return { ...view, props };
}

async function settlePreview() {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(PREVIEW_DEBOUNCE_MS);
  });
}

function dialog(): HTMLElement {
  return screen.getByRole("dialog");
}

function shipButton(): HTMLElement {
  return within(dialog()).getByTestId("ship-submit");
}

async function editRateLimits(value = EDIT_A) {
  const editor = await within(dialog()).findByRole("textbox", { name: "rate_limits value" });
  fireEvent.change(editor, { target: { value } });
  return editor;
}

function dryRuns(): ShipRequest[] {
  return mocks.ship.mock.calls
    .map(([request]) => request as ShipRequest)
    .filter((request) => request.dry_run === true);
}

function realShips(): ShipRequest[] {
  return mocks.ship.mock.calls
    .map(([request]) => request as ShipRequest)
    .filter((request) => request.dry_run !== true);
}

describe("ship editor rows", () => {
  beforeEach(() => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    window.localStorage.removeItem(SHIP_MODE_STORAGE_KEY);
    for (const mock of Object.values(mocks)) mock.mockReset();
    mocks.getParameter.mockResolvedValue({
      parameter: {
        env: "dev",
        app: app.name,
        key: "rate_limits",
        value: CURRENT,
        content_type: rateLimitsType,
        version: 9,
        metadata_json: "{}",
        created_by: "admin",
        created_at_unix_ms: 1,
        labels: {},
      },
    });
    mocks.ship.mockImplementation(async (request: ShipRequest) =>
      request.dry_run ? preview : activated,
    );
    mocks.releaseSubscribers.mockResolvedValue({
      subscribers: [],
      current_revision: 119,
      next_page_token: "",
    });
    mocks.subscriberStream.mockRejectedValue(new ApiError("unimplemented", "no stream", 404));
    mocks.validateRelease.mockResolvedValue({ valid: true, errors: [] });
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  /** An environment with no resources at all: every parameter alias becomes a fresh row. */
  function emptyEnvironments(): EnvironmentOverview[] {
    return [
      {
        ...dev,
        status: "empty",
        values_state: "empty",
        release_state: "none",
        rollout_state: "no_subscribers",
        release: { latest_version: 0, release_count: 0 },
        values: dev.values.map((value) => ({
          ...value,
          present: false,
          key: undefined,
          current_version: undefined,
          pinned_version: undefined,
        })),
      },
    ];
  }

  it("keeps a fresh row quiet until the operator types in it", async () => {
    renderModal({ environments: emptyEnvironments(), initialAlias: undefined });
    const row = within(dialog()).getByTestId("ship-row-rate_limits");
    expect(within(row).queryByRole("alert")).toBeNull();
    const editor = within(row).getByRole("textbox", { name: "rate_limits value" });
    fireEvent.change(editor, { target: { value: "x" } });
    fireEvent.change(editor, { target: { value: "" } });
    expect(within(row).getByRole("alert")).toBeInTheDocument();
  });

  it("marks an edited row, shows its diff against the current value, and reverts it", async () => {
    renderModal();
    const editor = await editRateLimits();
    const row = within(dialog()).getByTestId("ship-row-rate_limits");
    expect(row).toHaveAttribute("data-changed", "true");
    expect(within(row).getByText("changed")).toBeVisible();

    fireEvent.click(within(row).getByRole("button", { name: "Show diff for rate_limits" }));
    const diff = within(row).getByTestId("ship-row-diff-rate_limits");
    // The baseline is labelled with the overview's current version, not the fetch's.
    const devRateLimits = dev.values.find((value) => value.alias === "rate_limits");
    expect(diff).toHaveTextContent(`current v${devRateLimits?.current_version}`);
    expect(diff.querySelector('[data-op="del"].json-diff-text')).not.toBeNull();
    expect(diff.querySelector('[data-op="add"].json-diff-text')).not.toBeNull();

    fireEvent.click(within(row).getByRole("button", { name: "Revert rate_limits" }));
    expect(editor).toHaveValue(CURRENT);
    expect(row).toHaveAttribute("data-changed", "false");
    expect(within(row).queryByTestId("ship-row-diff-rate_limits")).toBeNull();
    expect(within(row).getByRole("button", { name: "Revert rate_limits" })).toBeDisabled();
  });

  it("folds rows to a one-line summary when there are more than three", async () => {
    const extras = ["extra_a", "extra_b"];
    const wide = {
      ...app,
      contract: [
        ...app.contract,
        ...extras.map((alias) => ({ alias, kind: "parameter" as const, content_type: "string" })),
      ],
    };
    renderModal({ application: wide, environments: emptyEnvironments(), initialAlias: undefined });
    const rows = within(dialog())
      .getAllByRole("listitem")
      .filter((item) => item.getAttribute("data-testid")?.startsWith("ship-row-"));
    expect(rows).toHaveLength(4);
    for (const row of rows) expect(row).toHaveAttribute("data-open", "false");
    expect(within(dialog()).queryByRole("textbox", { name: "extra_a value" })).toBeNull();

    const extraA = within(dialog()).getByTestId("ship-row-extra_a");
    fireEvent.click(within(extraA).getByRole("button", { name: "Edit" }));
    expect(extraA).toHaveAttribute("data-open", "true");
    const editor = within(extraA).getByRole("textbox", { name: "extra_a value" });
    fireEvent.change(editor, { target: { value: "hello" } });
    // A row with an edit in progress stays open; the others stay folded.
    expect(within(dialog()).getByTestId("ship-row-extra_b")).toHaveAttribute("data-open", "false");
    fireEvent.click(within(extraA).getByRole("button", { name: "Collapse" }));
    expect(extraA).toHaveAttribute("data-open", "false");
    expect(extraA).toHaveTextContent("hello");
  });
});

describe("ShipModal", () => {
  beforeEach(() => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    window.localStorage.removeItem(SHIP_MODE_STORAGE_KEY);
    for (const mock of Object.values(mocks)) mock.mockReset();
    mocks.getParameter.mockResolvedValue({
      parameter: {
        env: "dev",
        app: app.name,
        key: "rate_limits",
        value: CURRENT,
        content_type: rateLimitsType,
        version: 9,
        metadata_json: "{}",
        created_by: "admin",
        created_at_unix_ms: 1,
        labels: {},
      },
    });
    mocks.ship.mockImplementation(async (request: ShipRequest) =>
      request.dry_run ? preview : activated,
    );
    mocks.releaseSubscribers.mockResolvedValue({
      subscribers: [],
      current_revision: 119,
      next_page_token: "",
    });
    mocks.subscriberStream.mockRejectedValue(new ApiError("unimplemented", "no stream", 404));
    mocks.validateRelease.mockResolvedValue({ valid: true, errors: [] });
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("prefills the edited alias, dry-runs after 400 ms, and highlights the changed rows", async () => {
    renderModal();
    const editor = await editRateLimits();
    expect(mocks.getParameter).toHaveBeenCalledWith({
      env: "dev",
      app: app.name,
      key: "rate_limits",
    });
    expect(editor).toHaveValue(EDIT_A);
    expect(dryRuns()).toHaveLength(0);

    await settlePreview();
    await waitFor(() => expect(dryRuns()).toHaveLength(1));
    expect(dryRuns()[0]).toEqual({
      application: app.name,
      environment: "dev",
      changes: [{ alias: "rate_limits", value: EDIT_A, content_type: rateLimitsType }],
      dry_run: true,
    });

    const previewSection = await within(dialog()).findByTestId("ship-preview");
    await within(previewSection).findByText(`${releaseName}@${next}`);
    const table = within(previewSection).getByRole("table");
    expect(table.querySelector('tr[data-alias="rate_limits"]')).toHaveAttribute(
      "data-changed",
      "true",
    );
    expect(table.querySelector('tr[data-alias="database"]')).toHaveAttribute(
      "data-changed",
      "false",
    );
    expect(within(previewSection).getByTestId("ship-activation")).toHaveTextContent(
      `${releaseName}@${base} → @${next}`,
    );
  });

  it("ships a non-production environment in one click with the previewed version as the CAS guard", async () => {
    const { props } = renderModal();
    await editRateLimits();
    await settlePreview();
    await waitFor(() => expect(shipButton()).toBeEnabled());
    expect(within(dialog()).queryByTestId("ship-confirm-env")).toBeNull();

    fireEvent.click(shipButton());
    await waitFor(() => expect(realShips()).toHaveLength(1));
    expect(realShips()[0]).toEqual({
      application: app.name,
      environment: "dev",
      changes: [{ alias: "rate_limits", value: EDIT_A, content_type: rateLimitsType }],
      expected_active_version: base,
      request_id: expect.any(String),
    });

    expect(await within(dialog()).findByTestId("ship-rollout")).toBeVisible();
    expect(props.onShipped).toHaveBeenCalledWith(activated);
    expect(within(dialog()).getByTestId("ship-modal")).toHaveAttribute("data-phase", "rollout");
  });

  it("marks the preview stale on every edit and re-enables Ship only after the next dry run", async () => {
    renderModal();
    await editRateLimits();
    await settlePreview();
    await waitFor(() => expect(shipButton()).toBeEnabled());

    await editRateLimits(EDIT_B);
    expect(shipButton()).toBeDisabled();
    expect(within(dialog()).getByTestId("ship-preview")).toHaveAttribute("data-stale", "true");

    await settlePreview();
    await waitFor(() => expect(dryRuns()).toHaveLength(2));
    await waitFor(() => expect(shipButton()).toBeEnabled());
    expect(within(dialog()).getByTestId("ship-preview")).toHaveAttribute("data-stale", "false");
  });

  it("keeps Ship disabled on production until the environment name is typed exactly", async () => {
    renderModal({ initialEnvironment: "prod" });
    await editRateLimits();
    await settlePreview();
    await waitFor(() => expect(dryRuns()).toHaveLength(1));
    await waitFor(() =>
      expect(within(dialog()).getByTestId("ship-preview")).toHaveAttribute("data-stale", "false"),
    );

    const confirm = within(dialog()).getByTestId("ship-confirm-env");
    expect(shipButton()).toBeDisabled();
    fireEvent.change(confirm, { target: { value: "pro" } });
    expect(shipButton()).toBeDisabled();
    fireEvent.change(confirm, { target: { value: "prod" } });
    expect(shipButton()).toBeEnabled();
    fireEvent.change(confirm, { target: { value: "prod " } });
    expect(shipButton()).toBeDisabled();
  });

  it("shows violations and writes nothing when the ship is rejected", async () => {
    const rejected: ShipResult = {
      status: "rejected",
      preview: preview.preview,
      parameters: [],
      error: {
        code: "failed_precondition",
        message: "invalid",
        validation_errors: [
          {
            alias: "rate_limits",
            code: "schema_violation",
            schema_pointer: "/properties/rate_limits",
            message: "per_minute must be > 0",
          },
        ],
      },
    };
    mocks.ship.mockImplementation(async (request: ShipRequest) =>
      request.dry_run ? preview : rejected,
    );
    const { props } = renderModal();
    await editRateLimits();
    await settlePreview();
    await waitFor(() => expect(shipButton()).toBeEnabled());
    fireEvent.click(shipButton());

    const panel = await within(dialog()).findByTestId("ship-rejected");
    expect(panel).toHaveTextContent("Rejected before writing");
    expect(within(panel).getByText("per_minute must be > 0")).toBeVisible();
    expect(props.onShipped).not.toHaveBeenCalled();

    fireEvent.click(within(panel).getByRole("button", { name: "Edit changes" }));
    expect(within(dialog()).getByTestId("ship-modal")).toHaveAttribute("data-phase", "compose");
  });

  it("offers Fix and retry that reuses the written version, and Open in Releases", async () => {
    const notActivated: ShipResult = {
      status: "release_created_not_activated",
      preview: preview.preview,
      parameters: [{ alias: "rate_limits", key: "rate_limits", version: written, revision: 118 }],
      release: { name: releaseName, version: next, digest: "sha256:abc" },
      error: {
        code: "failed_precondition",
        message: "secret db_password is disabled",
        validation_errors: [
          {
            alias: "db_password",
            code: "secret_disabled",
            schema_pointer: "",
            message: "secret version 2 is disabled",
          },
        ],
      },
    };
    mocks.ship.mockImplementation(async (request: ShipRequest) =>
      request.dry_run ? preview : notActivated,
    );
    const { props } = renderModal();
    await editRateLimits();
    await settlePreview();
    await waitFor(() => expect(shipButton()).toBeEnabled());
    fireEvent.click(shipButton());

    const panel = await within(dialog()).findByTestId("ship-not-activated");
    expect(panel).toHaveTextContent(`${releaseName}@${next}`);
    expect(panel).toHaveTextContent("created, not activated");
    expect(within(panel).getByText("secret version 2 is disabled")).toBeVisible();
    expect(within(panel).queryByRole("button", { name: "Retry activation" })).toBeNull();
    expect(within(panel).getByRole("link", { name: `Open v${next} in Releases` })).toHaveAttribute(
      "href",
      `/releases?app=${app.name}&env=dev&name=${releaseName}&release=${encodeURIComponent(`${releaseName}@${next}`)}`,
    );
    expect(props.onShipped).toHaveBeenCalledWith(notActivated);

    fireEvent.click(within(panel).getByRole("button", { name: "Fix and retry" }));
    await settlePreview();
    await waitFor(() => expect(dryRuns()).toHaveLength(2));
    expect(dryRuns()[1].changes).toEqual([{ alias: "rate_limits", version: written }]);
    expect(within(dialog()).getByTestId("ship-row-rate_limits")).toHaveTextContent(
      `v${written} was already written`,
    );
  });

  it("retries activation with the CAS guard only when no violations were reported", async () => {
    const external: ShipResult = {
      status: "release_created_not_activated",
      preview: preview.preview,
      parameters: [{ alias: "rate_limits", key: "rate_limits", version: written, revision: 118 }],
      release: { name: releaseName, version: next, digest: "sha256:abc" },
      error: { code: "unavailable", message: "activation notifier timed out" },
    };
    mocks.ship.mockImplementation(async (request: ShipRequest) =>
      request.dry_run ? preview : external,
    );
    mocks.activateRelease.mockResolvedValue({
      release: { name: releaseName, version: next },
      activation_revision: 120,
      previous_version: base,
      changed: true,
    });
    renderModal();
    await editRateLimits();
    await settlePreview();
    await waitFor(() => expect(shipButton()).toBeEnabled());
    fireEvent.click(shipButton());

    const panel = await within(dialog()).findByTestId("ship-not-activated");
    fireEvent.click(within(panel).getByRole("button", { name: "Retry activation" }));
    await waitFor(() =>
      expect(mocks.activateRelease).toHaveBeenCalledWith(
        { env: "dev", app: app.name },
        releaseName,
        next,
        base,
      ),
    );
    expect(await within(dialog()).findByTestId("ship-rollout")).toBeVisible();
    expect(within(dialog()).getByTestId("rollout-progress")).toHaveTextContent(/rev\s*120/);
  });

  it("shows the conflict panel and re-previews against the new base reusing the written version", async () => {
    mocks.ship.mockImplementation(async (request: ShipRequest) =>
      request.dry_run ? preview : conflict,
    );
    const { props } = renderModal({ initialEnvironment: "prod" });
    await editRateLimits();
    await settlePreview();
    await waitFor(() => expect(dryRuns()).toHaveLength(1));
    fireEvent.change(within(dialog()).getByTestId("ship-confirm-env"), {
      target: { value: "prod" },
    });
    await waitFor(() => expect(shipButton()).toBeEnabled());
    fireEvent.click(shipButton());

    const panel = await within(dialog()).findByTestId("ship-conflict");
    expect(panel).toHaveTextContent(`${releaseName}@${conflictCurrent}`);
    expect(panel).toHaveTextContent("rate_limits");
    expect(panel).toHaveTextContent(`v${conflictWritten}`);
    expect(panel).toHaveTextContent(`${releaseName}@${conflictRelease}`);
    expect(panel).toHaveTextContent("created, not activated");
    expect(within(panel).queryByRole("button", { name: /activate anyway/i })).toBeNull();
    expect(props.onShipped).toHaveBeenCalledWith(conflict);

    fireEvent.click(
      within(panel).getByRole("button", { name: `Re-preview against @${conflictCurrent}` }),
    );
    await settlePreview();
    await waitFor(() => expect(dryRuns()).toHaveLength(2));
    expect(dryRuns()[1].changes).toEqual([{ alias: "rate_limits", version: conflictWritten }]);
    expect(realShips()).toHaveLength(1);
  });

  it("asks before closing a conflict that already wrote versions, then closes", async () => {
    mocks.ship.mockImplementation(async (request: ShipRequest) =>
      request.dry_run ? preview : conflict,
    );
    const { props } = renderModal();
    await editRateLimits();
    await settlePreview();
    await waitFor(() => expect(shipButton()).toBeEnabled());
    fireEvent.click(shipButton());
    const panel = await within(dialog()).findByTestId("ship-conflict");
    expect(within(panel).queryByRole("button", { name: "Discard" })).toBeNull();
    fireEvent.click(within(panel).getByRole("button", { name: "Close without re-previewing" }));
    // The written version is named so the user knows it survives the close.
    const confirm = await screen.findByRole("dialog", {
      name: "Close without re-previewing?",
      hidden: true,
    });
    expect(confirm).toHaveTextContent(`rate_limits v${conflictWritten}`);
    expect(props.onClose).not.toHaveBeenCalled();
    fireEvent.click(within(confirm).getByRole("button", { name: "Keep editing", hidden: true }));
    await waitFor(() =>
      expect(
        screen.queryByRole("dialog", { name: "Close without re-previewing?", hidden: true }),
      ).toBeNull(),
    );
    expect(props.onClose).not.toHaveBeenCalled();

    fireEvent.click(within(panel).getByRole("button", { name: "Close without re-previewing" }));
    const again = await screen.findByRole("dialog", {
      name: "Close without re-previewing?",
      hidden: true,
    });
    fireEvent.click(within(again).getByRole("button", { name: "Close", hidden: true }));
    expect(props.onClose).toHaveBeenCalledTimes(1);
  });

  it("closes a conflict directly when nothing was written", async () => {
    const preflight: ShipResult = { ...conflict, parameters: [], release: undefined };
    mocks.ship.mockImplementation(async (request: ShipRequest) =>
      request.dry_run ? preview : preflight,
    );
    const { props } = renderModal();
    await editRateLimits();
    await settlePreview();
    await waitFor(() => expect(shipButton()).toBeEnabled());
    fireEvent.click(shipButton());
    const panel = await within(dialog()).findByTestId("ship-conflict");
    expect(panel).toHaveTextContent("nothing was written");
    fireEvent.click(within(panel).getByRole("button", { name: "Close without re-previewing" }));
    expect(
      screen.queryByRole("dialog", { name: "Close without re-previewing?", hidden: true }),
    ).toBeNull();
    expect(props.onClose).toHaveBeenCalledTimes(1);
  });

  describe("disabled Ship explains itself", () => {
    const note = () => within(dialog()).queryByTestId("ship-blocked-reason");

    it("says the values must parse first, then that the preview is pending", async () => {
      renderModal();
      // The row is still loading its current value: nothing parses yet.
      expect(note()).toHaveTextContent("Fix the values above to ship.");
      expect(shipButton()).toBeDisabled();
      await editRateLimits();
      // Parsed, debounce running: the dry run has not been asked for yet.
      expect(note()).toHaveTextContent("Waiting for the preview.");
      let release: (value: ShipResult) => void = () => undefined;
      mocks.ship.mockImplementation(
        () =>
          new Promise<ShipResult>((resolve) => {
            release = resolve;
          }),
      );
      await settlePreview();
      await waitFor(() => expect(note()).toHaveTextContent("Previewing…"));
      expect(shipButton()).toBeDisabled();
      await act(async () => {
        release(preview);
      });
      await waitFor(() => expect(shipButton()).toBeEnabled());
      expect(note()).toBeNull();
    });

    it("names the missing secret", async () => {
      const withoutSecret: EnvironmentOverview[] = incident.environments.map((env) =>
        env.namespace.env === "prod"
          ? {
              ...env,
              values: env.values.map((value) =>
                value.alias === "db_password"
                  ? { ...value, present: false, key: undefined }
                  : value,
              ),
            }
          : env,
      );
      renderModal({ environments: withoutSecret, initialEnvironment: "prod" });
      await editRateLimits();
      expect(note()).toHaveTextContent("Add the missing secret first: db_password.");
      expect(shipButton()).toBeDisabled();
    });

    it("says the preview is stale after an edit", async () => {
      renderModal();
      await editRateLimits();
      await settlePreview();
      await waitFor(() => expect(shipButton()).toBeEnabled());
      await editRateLimits(EDIT_B);
      expect(note()).toHaveTextContent("Edited since the last preview; it re-runs automatically.");
      expect(shipButton()).toBeDisabled();
    });

    it("says the candidate release is invalid", async () => {
      const invalid: ShipResult = {
        ...preview,
        preview: {
          ...preview.preview,
          validation: {
            valid: false,
            errors: [
              {
                alias: "rate_limits",
                code: "schema_violation",
                schema_pointer: "/properties/rate_limits",
                message: "per_minute must be > 0",
              },
            ],
          },
        },
      };
      mocks.ship.mockImplementation(async () => invalid);
      renderModal();
      await editRateLimits();
      await settlePreview();
      await waitFor(() => expect(note()).toHaveTextContent("The candidate release is invalid."));
      expect(shipButton()).toBeDisabled();
    });

    it("says there is nothing to ship when no value changed", async () => {
      const unchanged: ShipResult = { ...preview, preview: { ...preview.preview } };
      mocks.ship.mockImplementation(async () => unchanged);
      // dev has an active release and every value present: no row opens.
      renderModal({ initialAlias: undefined });
      expect(within(dialog()).queryByRole("textbox")).toBeNull();
      await settlePreview();
      await waitFor(() => expect(dryRuns()).toHaveLength(1));
      await waitFor(() => expect(note()).toHaveTextContent("Nothing to ship: no value changed."));
      expect(shipButton()).toBeDisabled();
    });

    it("asks for the production name", async () => {
      renderModal({ initialEnvironment: "prod" });
      await editRateLimits();
      await settlePreview();
      await waitFor(() => expect(dryRuns()).toHaveLength(1));
      await waitFor(() => expect(note()).toHaveTextContent("Type prod to ship to production."));
      expect(shipButton()).toBeDisabled();
      fireEvent.change(within(dialog()).getByTestId("ship-confirm-env"), {
        target: { value: "prod" },
      });
      expect(shipButton()).toBeEnabled();
      expect(note()).toBeNull();
    });
  });

  it("asks before discarding an edited value", async () => {
    const { props } = renderModal();
    const editor = await editRateLimits();
    // The prefilled value is not an edit; only a change is.
    fireEvent.change(editor, { target: { value: CURRENT } });
    fireEvent.click(within(dialog()).getByRole("button", { name: "Cancel" }));
    expect(props.onClose).toHaveBeenCalledTimes(1);
    fireEvent.change(editor, { target: { value: EDIT_B } });
    fireEvent.click(within(dialog()).getByRole("button", { name: "Cancel" }));
    expect(props.onClose).toHaveBeenCalledTimes(1);
    const confirm = await screen.findByRole("dialog", { name: "Discard changes?", hidden: true });
    fireEvent.click(within(confirm).getByRole("button", { name: "Keep editing", hidden: true }));
    await waitFor(() =>
      expect(screen.queryByRole("dialog", { name: "Discard changes?", hidden: true })).toBeNull(),
    );
    expect(props.onClose).toHaveBeenCalledTimes(1);
    fireEvent.click(within(dialog()).getByRole("button", { name: "Cancel" }));
    const again = await screen.findByRole("dialog", { name: "Discard changes?", hidden: true });
    fireEvent.click(within(again).getByRole("button", { name: "Discard", hidden: true }));
    expect(props.onClose).toHaveBeenCalledTimes(2);
  });

  it("jumps from a rejected violation back to the value editor", async () => {
    const rejected: ShipResult = {
      status: "rejected",
      preview: preview.preview,
      parameters: [],
      error: {
        code: "failed_precondition",
        message: "invalid",
        validation_errors: [
          {
            alias: "rate_limits",
            code: "schema_violation",
            schema_pointer: "/properties/rate_limits",
            message: "per_minute must be > 0",
          },
        ],
      },
    };
    mocks.ship.mockImplementation(async (request: ShipRequest) =>
      request.dry_run ? preview : rejected,
    );
    Element.prototype.scrollIntoView ??= vi.fn();
    renderModal();
    await editRateLimits();
    await settlePreview();
    await waitFor(() => expect(shipButton()).toBeEnabled());
    fireEvent.click(shipButton());
    const panel = await within(dialog()).findByTestId("ship-rejected");
    const row = within(panel).getByRole("row", { name: /rate_limits/ });
    expect(within(row).getByRole("link", { name: "Open rate_limits" })).toHaveAttribute(
      "href",
      `/parameters/detail?env=dev&app=${encodeURIComponent(app.name)}&key=rate_limits`,
    );
    fireEvent.click(within(row).getByRole("button", { name: "Edit this value" }));
    expect(within(dialog()).getByTestId("ship-modal")).toHaveAttribute("data-phase", "compose");
    await waitFor(() =>
      expect(within(dialog()).getByTestId("ship-row-rate_limits")).toContainElement(
        document.activeElement as HTMLElement,
      ),
    );
  });

  it("lists unreleased changes as opt-ins that pin the current label", async () => {
    mocks.getParameter.mockResolvedValue({
      parameter: {
        env: "prod",
        app: app.name,
        key: "database",
        value: '{"host": "db"}',
        content_type: "json",
        version: 5,
        metadata_json: "{}",
        created_by: "admin",
        created_at_unix_ms: 1,
        labels: {},
      },
    });
    renderModal({ initialEnvironment: "prod", initialAlias: "database" });
    await within(dialog()).findByRole("textbox", { name: "database value" });
    await settlePreview();
    await waitFor(() => expect(dryRuns()).toHaveLength(1));

    const drift = await within(dialog()).findByTestId("ship-drift");
    const optIn = within(drift).getByRole("checkbox", {
      name: new RegExp(`include rate_limits v${prodRateLimits?.current_version}`),
    });
    expect(optIn).toHaveAttribute("aria-checked", "false");
    expect(drift).toHaveTextContent(`pinned v${prodRateLimits?.pinned_version}`);
    // Base UI toggles through the hidden input the label points at.
    fireEvent.click(within(drift).getByText("rate_limits").closest("label") as HTMLElement);
    expect(optIn).toHaveAttribute("aria-checked", "true");
    expect(shipButton()).toBeDisabled();

    await settlePreview();
    await waitFor(() => expect(dryRuns()).toHaveLength(2));
    expect(dryRuns()[1].changes).toEqual([
      // JSON values travel minified; the editor text keeps its whitespace.
      { alias: "database", value: '{"host":"db"}', content_type: "json" },
      { alias: "rate_limits", label: "current" },
    ]);
  });

  it("defaults to express once a release has ever been active, and guided otherwise", async () => {
    const { unmount } = renderModal();
    expect(within(dialog()).queryByTestId("ship-steps")).toBeNull();
    expect(within(dialog()).getByTestId("ship-modal")).toHaveAttribute("data-mode", "express");
    unmount();

    const fresh: EnvironmentOverview[] = incident.environments.map((env) => ({
      ...env,
      status: "unreleased",
      release_state: "none",
      rollout_state: "no_subscribers",
      release: { latest_version: 0, release_count: 0 },
      values: env.values.map((value) => ({ ...value, pinned_version: undefined })),
    }));
    renderModal({ environments: fresh });
    const steps = within(dialog()).getByTestId("ship-steps");
    expect(steps).toBeVisible();
    expect(within(steps).getAllByRole("listitem")).toHaveLength(4);
    expect(steps.querySelector('[data-step="change"]')).toHaveAttribute("aria-current", "step");
    expect(steps).toHaveTextContent("Pick the environment and edit values by alias");
  });

  it("persists the steps toggle in localStorage under kms-ship-mode", async () => {
    window.localStorage.setItem(SHIP_MODE_STORAGE_KEY, "guided");
    renderModal();
    expect(within(dialog()).getByTestId("ship-steps")).toBeVisible();

    fireEvent.click(within(dialog()).getByText("Show steps"));
    expect(within(dialog()).queryByTestId("ship-steps")).toBeNull();
    expect(window.localStorage.getItem(SHIP_MODE_STORAGE_KEY)).toBe("express");
  });

  it("renders a missing secret as a blocker row that hands off to Add secret", async () => {
    const withoutSecret: EnvironmentOverview[] = incident.environments.map((env) =>
      env.namespace.env === "prod"
        ? {
            ...env,
            values: env.values.map((value) =>
              value.alias === "db_password" ? { ...value, present: false, key: undefined } : value,
            ),
          }
        : env,
    );
    const { props } = renderModal({ environments: withoutSecret, initialEnvironment: "prod" });
    const blocker = await within(dialog()).findByTestId("ship-blocker-db_password");
    expect(blocker).toHaveTextContent("secret with no value");
    fireEvent.click(within(blocker).getByRole("button", { name: "Add secret" }));
    expect(props.onAddSecret).toHaveBeenCalledWith("prod", "db_password");
    // Secrets never become value rows.
    expect(within(dialog()).queryByRole("textbox", { name: "db_password value" })).toBeNull();
  });

  it("prefills every missing parameter alias for a first release", async () => {
    const empty: EnvironmentOverview[] = [
      {
        ...dev,
        status: "empty",
        values_state: "empty",
        release_state: "none",
        rollout_state: "no_subscribers",
        release: { latest_version: 0, release_count: 0 },
        values: dev.values.map((value) => ({
          ...value,
          present: false,
          key: undefined,
          current_version: undefined,
          pinned_version: undefined,
        })),
      },
    ];
    renderModal({ environments: empty, initialAlias: undefined });
    expect(within(dialog()).getByTestId("ship-row-database")).toBeVisible();
    expect(within(dialog()).getByTestId("ship-row-rate_limits")).toBeVisible();
    expect(mocks.getParameter).not.toHaveBeenCalled();
    // Nothing parses yet (the JSON row is empty), so no dry run is scheduled.
    await settlePreview();
    expect(dryRuns()).toHaveLength(0);
  });

  it("shows the rollout with rejected instances first and offers an inline rollback", async () => {
    mocks.releaseSubscribers.mockResolvedValue({
      subscribers: [appliedInstance, rejectedInstance],
      current_revision: 119,
      next_page_token: "",
    });
    mocks.rollbackRelease.mockResolvedValue({
      release: {
        ...dev.release.active,
        namespace: dev.namespace,
        name: releaseName,
        version: base,
      },
      activation_revision: 121,
      previous_version: next,
      rolled_back_from: next,
      changed: true,
    });
    renderModal();
    await editRateLimits();
    await settlePreview();
    await waitFor(() => expect(shipButton()).toBeEnabled());
    fireEvent.click(shipButton());

    const rollout = await within(dialog()).findByTestId("ship-rollout");
    await waitFor(() =>
      expect(within(rollout).getByTestId("rollout-progress")).toHaveTextContent("1/2 applied"),
    );
    const rows = within(rollout).getAllByTestId("rollout-instance");
    expect(rows[0]).toHaveAttribute("data-state", "rejected");
    expect(rows[0]).toHaveTextContent("config_validation_failed");
    expect(rows[0]).toHaveTextContent(`still serving v${base}`);
    expect(rows[0]).toHaveTextContent("rate_limits.per_minute must be greater than zero");
    expect(within(rollout).getByText(/Polling|Live|Stale/)).toBeVisible();
    expect(screen.getByRole("dialog", { name: /Shipped/ })).toHaveTextContent(
      `${releaseName}@${next}`,
    );

    fireEvent.click(within(dialog()).getByTestId("ship-rollback"));
    const rollback = await screen.findByRole("dialog", { name: "Roll back release?" });
    await waitFor(() =>
      expect(mocks.validateRelease).toHaveBeenCalledWith(
        { env: "dev", app: app.name },
        releaseName,
        base,
      ),
    );
    await waitFor(() => expect(within(rollback).getByTestId("rollback-confirm")).toBeEnabled());
    fireEvent.click(within(rollback).getByTestId("rollback-confirm"));
    await waitFor(() =>
      expect(mocks.rollbackRelease).toHaveBeenCalledWith({
        env: "dev",
        app: app.name,
        name: releaseName,
        expected_current_version: next,
      }),
    );
    expect(await screen.findByTestId("ship-rolled-back")).toHaveTextContent(
      `${releaseName}@${base}`,
    );
  });
});
