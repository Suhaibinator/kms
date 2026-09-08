import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { BulkParameterModal } from "@/components/applications/BulkParameterModal";
import { VALUE_EDITOR_MODE_STORAGE_KEY } from "@/components/SchemaForm";
import type { ApplicationConfigurationRow } from "@/lib/types";

const row: ApplicationConfigurationRow = {
  key: "settings",
  kind: "parameter",
  environments: {
    dev: { present: true, value: '{"replicas":3}', content_type: "json", version: 1 },
  },
};
const environments = ["dev"];
const schemaJson = JSON.stringify({
  type: "object",
  properties: { settings: { type: "object", properties: { replicas: { type: "number" } } } },
});

describe("bulk parameter draft safety", () => {
  beforeEach(() => window.localStorage.setItem(VALUE_EDITOR_MODE_STORAGE_KEY, "form"));

  it("does not apply the last valid value while a field has an incomplete draft", async () => {
    const onSave = vi.fn();
    render(
      <BulkParameterModal
        app="app"
        environments={environments}
        row={row}
        retryEnvironments={null}
        schemaJson={schemaJson}
        saving={false}
        onClose={vi.fn()}
        onSave={onSave}
      />,
    );
    const dialog = await screen.findByRole("dialog", { name: "Update settings" });
    const input = await within(dialog).findByRole("textbox", { name: "replicas" });
    fireEvent.change(input, { target: { value: "4" } });
    fireEvent.change(input, { target: { value: "4e" } });
    expect(within(dialog).getByRole("button", { name: "Apply to 1 environment" })).toBeDisabled();
    const form = dialog.querySelector("form");
    if (!form) throw new Error("Missing parameter form");
    fireEvent.submit(form);
    expect(onSave).not.toHaveBeenCalled();
    fireEvent.change(input, { target: { value: "4e2" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Apply to 1 environment" }));
    await waitFor(() =>
      expect(onSave).toHaveBeenCalledWith(
        expect.objectContaining({ value: '{"replicas":400}', environments: ["dev"] }),
      ),
    );
  });

  it("never substitutes all environments when an explicit clone target is not loaded", async () => {
    render(
      <BulkParameterModal
        app="app"
        environments={environments}
        row={row}
        initialEnvironments={["staging"]}
        retryEnvironments={null}
        schemaJson={schemaJson}
        saving={false}
        onClose={vi.fn()}
        onSave={vi.fn()}
      />,
    );
    const dialog = await screen.findByRole("dialog", { name: "Update settings" });
    expect(within(dialog).getByRole("button", { name: "Apply to 0 environments" })).toBeDisabled();
    expect(within(dialog).getByText("Choose at least one target environment.")).toBeVisible();
  });
});
