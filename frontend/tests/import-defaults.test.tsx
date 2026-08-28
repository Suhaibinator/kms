import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ImportDefaultsModal } from "@/components/applications/ImportDefaultsModal";
import { ApiError } from "@/lib/api";
import type { DefaultsApplyResponse, DefaultsApplyStatus } from "@/lib/types";

const mocks = vi.hoisted(() => ({
  importDefaults: vi.fn(),
  toast: { success: vi.fn(), info: vi.fn(), error: vi.fn() },
}));

vi.mock("@/context/ToastContext", () => ({ useToast: () => mocks.toast }));
vi.mock("@/lib/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/api")>();
  return {
    ...actual,
    api: { ...actual.api, importApplicationDefaults: mocks.importDefaults },
  };
});

const SECRET_SENTINEL = "DO-NOT-RENDER-THIS-PARAMETER-VALUE";
const artifactText = JSON.stringify({
  format: "kms-config-defaults/v1",
  profile: "dev",
  schema_sha256: "a".repeat(64),
  contract: [
    { alias: "runtime", kind: "parameter", content_type: "json" },
    { alias: "database_password", kind: "secret", content_type: "string" },
  ],
  parameters: [{ alias: "runtime", content_type: "json", value: SECRET_SENTINEL }],
});
const artifactJSONBytes = new TextEncoder().encode(artifactText);
const artifactFileBytes = new Uint8Array(3 + artifactJSONBytes.length + 3);
artifactFileBytes.set([0xef, 0xbb, 0xbf], 0); // UTF-8 BOM must survive selection.
artifactFileBytes.set(artifactJSONBytes, 3);
artifactFileBytes.set([0xc3, 0x28, 0xff], 3 + artifactJSONBytes.length); // Invalid UTF-8.

function expectArtifactBytes(call: number) {
  const request = mocks.importDefaults.mock.calls.at(call)?.[0] as
    | { artifact?: ArrayBuffer }
    | undefined;
  expect(request?.artifact).toBeInstanceOf(ArrayBuffer);
  if (!request?.artifact) throw new Error("defaults request did not include artifact bytes");
  expect(Array.from(new Uint8Array(request.artifact))).toEqual(Array.from(artifactFileBytes));
}

function response(
  status: DefaultsApplyStatus = "create",
  overrides: Partial<DefaultsApplyResponse> = {},
): DefaultsApplyResponse {
  return {
    profile: "dev",
    schema_sha256: "a".repeat(64),
    artifact_digest: "artifact-digest",
    plan_digest: `plan-${status}`,
    entries: [
      {
        alias: "runtime",
        key: "runtime-config",
        content_type: "json",
        status,
        current_version: status === "create" ? 0 : 3,
        applied_version: 0,
        revision: 0,
      },
    ],
    missing_secrets: ["database_password"],
    executed: false,
    definition_changed: false,
    definition_updated: false,
    ...overrides,
  };
}

function renderModal(props: Partial<React.ComponentProps<typeof ImportDefaultsModal>> = {}) {
  const onClose = vi.fn();
  const onImported = vi.fn(async () => undefined);
  render(
    <ImportDefaultsModal
      application="gradethis"
      environment="dev"
      production={false}
      open
      onClose={onClose}
      onImported={onImported}
      {...props}
    />,
  );
  return { onClose, onImported };
}

async function uploadArtifact() {
  const file = new File([artifactFileBytes], "defaults.json", { type: "application/json" });
  fireEvent.change(screen.getByLabelText("Defaults artifact"), { target: { files: [file] } });
  await screen.findByLabelText("Defaults import preview");
}

describe("ImportDefaultsModal", () => {
  beforeEach(() => {
    mocks.importDefaults.mockReset();
    mocks.toast.success.mockReset();
    mocks.toast.error.mockReset();
  });

  it("preserves BOM and invalid UTF-8 bytes without rendering parameter values", async () => {
    mocks.importDefaults.mockResolvedValue(response());
    renderModal();

    await uploadArtifact();

    expect(mocks.importDefaults).toHaveBeenCalledWith({
      env: "dev",
      app: "gradethis",
      artifact: expect.any(ArrayBuffer),
      overwrite: false,
      updateDefinition: false,
    });
    expectArtifactBytes(0);
    const preview = screen.getByLabelText("Defaults import preview");
    expect(within(preview).getByText("dev")).toBeVisible();
    expect(within(preview).getByText("runtime")).toBeVisible();
    expect(within(preview).getByText("runtime-config")).toBeVisible();
    expect(within(preview).getByText("database_password")).toBeVisible();
    expect(within(preview).getByText("Create")).toBeVisible();
    expect(document.body).not.toHaveTextContent(SECRET_SENTINEL);
    expect(document.body).not.toHaveTextContent(artifactText);
  });

  it("runs a fresh preview when overwrite is enabled", async () => {
    mocks.importDefaults
      .mockResolvedValueOnce(response("blocked"))
      .mockResolvedValueOnce(response("update"));
    renderModal();
    await uploadArtifact();
    expect(screen.getByText("Blocked")).toBeVisible();

    fireEvent.click(screen.getByRole("checkbox", { name: /Overwrite differing/ }));

    expect(await screen.findByText("Update")).toBeVisible();
    expect(mocks.importDefaults).toHaveBeenLastCalledWith({
      env: "dev",
      app: "gradethis",
      artifact: expect.any(ArrayBuffer),
      overwrite: true,
      updateDefinition: false,
    });
    expectArtifactBytes(-1);
    expect(mocks.importDefaults).toHaveBeenCalledTimes(2);
  });

  it("requires the exact environment name before importing to production", async () => {
    mocks.importDefaults.mockResolvedValue(response());
    renderModal({ environment: "prod", production: true });
    await uploadArtifact();
    const button = screen.getByRole("button", { name: "Import defaults" });
    const confirmation = screen.getByLabelText(/Type prod to confirm production import/);

    expect(button).toBeDisabled();
    fireEvent.change(confirmation, { target: { value: "production" } });
    expect(button).toBeDisabled();
    fireEvent.change(confirmation, { target: { value: "prod" } });
    expect(button).toBeEnabled();
  });

  it("invalidates a stale plan and requires another preview", async () => {
    mocks.importDefaults
      .mockResolvedValueOnce(response())
      .mockRejectedValueOnce(new ApiError("aborted", "defaults plan is stale", 409));
    renderModal();
    await uploadArtifact();

    fireEvent.click(screen.getByRole("button", { name: "Import defaults" }));

    expect(await screen.findByRole("alert")).toHaveTextContent(/plan is stale/i);
    expect(screen.queryByLabelText("Defaults import preview")).toBeNull();
    expect(screen.getByRole("button", { name: "Preview again" })).toBeEnabled();
  });

  it("executes with the current digest, refreshes, and closes", async () => {
    const preview = response();
    mocks.importDefaults.mockResolvedValueOnce(preview).mockResolvedValueOnce(
      response("create", {
        executed: true,
        entries: [{ ...preview.entries[0], applied_version: 1, revision: 12 }],
      }),
    );
    const { onClose, onImported } = renderModal();
    await uploadArtifact();

    fireEvent.click(screen.getByRole("button", { name: "Import defaults" }));

    await waitFor(() => expect(onImported).toHaveBeenCalledTimes(1));
    expect(mocks.importDefaults).toHaveBeenLastCalledWith({
      env: "dev",
      app: "gradethis",
      artifact: expect.any(ArrayBuffer),
      overwrite: false,
      updateDefinition: false,
      execute: true,
      planDigest: preview.plan_digest,
    });
    expectArtifactBytes(-1);
    expect(mocks.toast.success).toHaveBeenCalledWith(
      "Defaults imported",
      "dev/gradethis: 1 value(s) written.",
    );
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("requires a fresh opt-in preview before updating the application definition", async () => {
    mocks.importDefaults
      .mockResolvedValueOnce(response("unchanged", { definition_changed: true }))
      .mockResolvedValueOnce(response("unchanged", { definition_changed: true }));
    renderModal();
    await uploadArtifact();

    fireEvent.click(screen.getByRole("checkbox", { name: /Update application definition/ }));

    await waitFor(() => expect(mocks.importDefaults).toHaveBeenCalledTimes(2));
    expect(mocks.importDefaults).toHaveBeenLastCalledWith({
      env: "dev",
      app: "gradethis",
      artifact: expect.any(ArrayBuffer),
      overwrite: false,
      updateDefinition: true,
    });
  });
});
