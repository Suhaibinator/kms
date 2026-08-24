import { fireEvent, render, screen, within } from "@testing-library/react";
import { useState } from "react";
import { describe, expect, it, vi } from "vitest";
import { ContractEditor, exportContract } from "@/components/applications/ContractEditor";
import type { ContractEntry, ParsedContractFile } from "@/lib/contract-derive";

vi.mock("@/context/ToastContext", () => ({
  useToast: () => ({ success: vi.fn(), info: vi.fn(), error: vi.fn() }),
}));

const SCHEMA = JSON.stringify({
  $schema: "https://json-schema.org/draft/2020-12/schema",
  type: "object",
  additionalProperties: false,
  required: ["database", "timeout"],
  properties: {
    database: { type: "object" },
    timeout: { type: "integer" },
  },
});

const ENVELOPE = JSON.stringify({
  format: "kms-config-contract/v1",
  schema_sha256: "ABCDEF0123",
  groups: [{ alias: "database", content_type: "json" }],
  secrets: [{ alias: "db_password" }],
});

function Harness({
  initial,
  schemaJson,
  onChange,
  onImport,
}: {
  initial: ContractEntry[];
  schemaJson?: string | null;
  onChange?: (next: ContractEntry[]) => void;
  onImport?: (parsed: ParsedContractFile) => void;
}) {
  const [value, setValue] = useState(initial);
  return (
    <ContractEditor
      value={value}
      schemaJson={schemaJson}
      onImport={onImport}
      onChange={(next) => {
        setValue(next);
        onChange?.(next);
      }}
    />
  );
}

const rows = () =>
  within(screen.getByRole("list", { name: "Contract aliases" })).getAllByRole("listitem");

describe("ContractEditor", () => {
  it("adds and removes rows", () => {
    const onChange = vi.fn();
    render(
      <Harness
        initial={[{ alias: "database", kind: "parameter", content_type: "json" }]}
        onChange={onChange}
      />,
    );
    fireEvent.click(screen.getByRole("button", { name: "Add alias" }));
    expect(rows()).toHaveLength(2);
    expect(onChange).toHaveBeenLastCalledWith([
      { alias: "database", kind: "parameter", content_type: "json" },
      { alias: "", kind: "parameter", content_type: "json" },
    ]);
    fireEvent.change(screen.getByLabelText("Alias 2"), { target: { value: "database" } });
    expect(screen.getByRole("alert")).toHaveTextContent("Duplicate contract alias");
    fireEvent.click(screen.getAllByRole("button", { name: "Remove database" })[1]);
    expect(rows()).toHaveLength(1);
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("imports the generated envelope, marks rows as from the artifact, and reports the schema hash", () => {
    const onImport = vi.fn();
    render(<Harness initial={[]} onImport={onImport} />);
    fireEvent.click(screen.getByRole("button", { name: "Import" }));
    fireEvent.change(screen.getByLabelText(/Paste a/), { target: { value: ENVELOPE } });
    fireEvent.click(screen.getByRole("button", { name: "Apply import" }));
    expect(onImport).toHaveBeenCalledWith(
      expect.objectContaining({ source: "envelope", schema_sha256: "abcdef0123" }),
    );
    const items = rows();
    expect(items).toHaveLength(2);
    expect(within(items[0]).getByText("from artifact")).toBeVisible();
    expect(within(items[1]).getByText("from artifact")).toBeVisible();
    expect(within(items[1]).getByText("no content type")).toBeVisible();
    // A hand edit diverges the row from its artifact snapshot.
    fireEvent.change(within(items[0]).getByLabelText("Alias 1"), { target: { value: "db" } });
    expect(within(rows()[0]).getByText("diverged")).toBeVisible();
    expect(within(rows()[1]).getByText("from artifact")).toBeVisible();
  });

  it("imports a bare array and rejects anything else with a message", () => {
    render(<Harness initial={[]} />);
    fireEvent.click(screen.getByRole("button", { name: "Import" }));
    const textarea = screen.getByLabelText(/Paste a/);
    fireEvent.change(textarea, { target: { value: '{"format":"other/v9"}' } });
    fireEvent.click(screen.getByRole("button", { name: "Apply import" }));
    expect(screen.getByRole("alert")).toHaveTextContent('Unsupported contract format "other/v9"');
    fireEvent.change(textarea, {
      target: {
        value: JSON.stringify([{ alias: "timeout", kind: "parameter", content_type: "integer" }]),
      },
    });
    fireEvent.click(screen.getByRole("button", { name: "Apply import" }));
    expect(rows()).toHaveLength(1);
    expect(screen.getByLabelText("Alias 1")).toHaveValue("timeout");
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("checks alignment against the schema and applies one-click fixes", () => {
    render(
      <Harness
        initial={[
          { alias: "database", kind: "parameter", content_type: "string" },
          { alias: "legacy", kind: "parameter", content_type: "json" },
          { alias: "db_password", kind: "secret" },
        ]}
        schemaJson={SCHEMA}
      />,
    );
    const alignment = screen.getByRole("region", { name: "Schema alignment" });
    expect(within(alignment).getByText(/`timeout`/)).toBeVisible();
    fireEvent.click(within(alignment).getByRole("button", { name: "Add timeout" }));
    expect(rows()).toHaveLength(4);
    fireEvent.click(within(alignment).getByRole("button", { name: "Remove legacy" }));
    expect(rows()).toHaveLength(3);
    fireEvent.click(within(alignment).getByRole("button", { name: "Use json" }));
    expect(within(alignment).getByText("Aligned with the schema.")).toBeVisible();
  });

  it("exports the envelope for the current rows", () => {
    const contract: ContractEntry[] = [
      { alias: "database", kind: "parameter", content_type: "json" },
      { alias: "db_password", kind: "secret" },
    ];
    render(<Harness initial={contract} />);
    fireEvent.click(screen.getByRole("button", { name: "Export" }));
    expect(screen.getByLabelText("Exported contract")).toHaveValue(exportContract(contract));
    expect(JSON.parse(exportContract(contract))).toEqual({
      format: "kms-config-contract/v1",
      groups: [{ alias: "database", kind: "parameter", content_type: "json" }],
      secrets: [{ alias: "db_password", kind: "secret" }],
    });
  });
});
