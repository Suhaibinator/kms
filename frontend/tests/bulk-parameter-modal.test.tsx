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

  const stringRow: ApplicationConfigurationRow = {
    key: "banner",
    kind: "parameter",
    environments: {
      dev: { present: true, value: "hold the line", content_type: "string", version: 1 },
    },
  };

  it("refuses a cleared string value until the empty string is explicit", async () => {
    const onSave = vi.fn();
    render(
      <BulkParameterModal
        app="app"
        environments={environments}
        row={stringRow}
        retryEnvironments={null}
        saving={false}
        onClose={vi.fn()}
        onSave={onSave}
      />,
    );
    const dialog = await screen.findByRole("dialog", { name: "Update banner" });
    const input = within(dialog).getByRole("textbox", { name: "Value" });
    fireEvent.change(input, { target: { value: "" } });

    const apply = within(dialog).getByRole("button", { name: "Apply to 1 environment" });
    expect(apply).toBeDisabled();
    expect(within(dialog).getByTestId("value-empty-hint")).toHaveTextContent(
      "Type a value, or tick Empty string.",
    );

    fireEvent.click(within(dialog).getByRole("checkbox", { name: /Empty string/ }));
    await waitFor(() => expect(apply).toBeEnabled());
    fireEvent.click(apply);
    await waitFor(() =>
      expect(onSave).toHaveBeenCalledWith(
        expect.objectContaining({ value: "", content_type: "string", environments: ["dev"] }),
      ),
    );
  });

  it("opens a stored empty string ticked", async () => {
    render(
      <BulkParameterModal
        app="app"
        environments={environments}
        row={{
          ...stringRow,
          environments: {
            dev: { present: true, value: "", content_type: "string", version: 1 },
          },
        }}
        retryEnvironments={null}
        saving={false}
        onClose={vi.fn()}
        onSave={vi.fn()}
      />,
    );
    const dialog = await screen.findByRole("dialog", { name: "Update banner" });
    expect(within(dialog).getByRole("checkbox", { name: /Empty string/ })).toBeChecked();
    expect(within(dialog).getByRole("textbox", { name: "Value" })).toBeDisabled();
    expect(within(dialog).getByTestId("value-empty-hint")).toHaveTextContent(
      "Saved as an empty string.",
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
