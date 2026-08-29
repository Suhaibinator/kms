import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { useState } from "react";
import { describe, expect, it } from "vitest";
import { SchemaForm } from "@/components/SchemaForm";
import {
  aliasSchema,
  buildForm,
  extraKeys,
  initialValue,
  type JsonSchema,
  parseNumberDraft,
  setAt,
  validateValue,
} from "@/lib/schema-form";
import { chooseSelectOption } from "./select-test-utils";

const schema: JsonSchema = {
  type: "object",
  required: ["name", "replicas", "enabled", "limits"],
  additionalProperties: false,
  properties: {
    name: { type: "string", description: "Service name" },
    replicas: { type: "integer", minimum: 1 },
    enabled: { type: "boolean" },
    tier: { type: "string", enum: ["free", "pro"] },
    tags: { type: "array", items: { type: "string" } },
    limits: { type: "object", required: ["rps"], properties: { rps: { type: "number" } } },
    custom: { oneOf: [{ type: "string" }, { type: "number" }] },
  },
};

function Harness({ initial = "", disabled = false }: { initial?: string; disabled?: boolean }) {
  const [value, setValue] = useState(initial);
  return (
    <>
      <SchemaForm schema={schema} value={value} onChange={setValue} disabled={disabled} />
      <pre data-testid="out">{value}</pre>
    </>
  );
}

function out(): unknown {
  const text = screen.getByTestId("out").textContent ?? "";
  return text.trim() === "" ? undefined : JSON.parse(text);
}

describe("schema-form model", () => {
  it("builds typed fields and falls back to JSON for unsupported subtrees", () => {
    const root = buildForm(schema);
    expect(root?.kind).toBe("object");
    const kinds = Object.fromEntries((root?.fields ?? []).map((f) => [f.name, f.kind]));
    expect(kinds).toEqual({
      name: "string",
      replicas: "number",
      enabled: "boolean",
      tier: "string",
      tags: "list",
      limits: "object",
      custom: "json",
    });
    expect(root?.fields?.find((f) => f.name === "custom")?.reason).toBe("uses oneOf");
    expect(root?.fields?.find((f) => f.name === "replicas")?.integer).toBe(true);
    expect(root?.fields?.find((f) => f.name === "tier")?.enumValues).toEqual(["free", "pro"]);
    expect(root?.allowsExtra).toBe(false);
  });

  it("sees through the generator's nullable wrapper", () => {
    const wrapped: JsonSchema = {
      type: "object",
      properties: {
        max_idle: {
          anyOf: [{ type: "integer", minimum: 0 }, { type: "null" }],
          description: "nil means unlimited",
          default: null,
        },
        tags: { anyOf: [{ type: "array", items: { type: "string" } }, { type: "null" }] },
      },
    };
    const root = buildForm(wrapped);
    const maxIdle = root?.fields?.find((f) => f.name === "max_idle");
    expect(maxIdle?.kind).toBe("number");
    expect(maxIdle?.nullable).toBe(true);
    expect(maxIdle?.description).toBe("nil means unlimited");
    expect(root?.fields?.find((f) => f.name === "tags")?.kind).toBe("list");
    expect(validateValue(wrapped, { max_idle: null, tags: null })).toEqual([]);
    expect(validateValue(wrapped, { max_idle: -1 })[0]?.message).toBe("must be at least 0");
    expect(validateValue(wrapped, { max_idle: "x" })[0]?.message).toContain("must be integer");
  });

  it("returns null for a root that is not an object with properties", () => {
    expect(buildForm({ type: "object" })).toBeNull();
    expect(buildForm({ type: "integer" })).toBeNull();
    expect(buildForm(null)).toBeNull();
  });

  it("reads an alias sub-schema out of the pinned schema", () => {
    const pinned = JSON.stringify({ type: "object", properties: { app: schema } });
    expect(aliasSchema(pinned, "app")).toEqual(schema);
    expect(aliasSchema(pinned, "missing")).toBeNull();
    expect(aliasSchema("not json", "app")).toBeNull();
  });

  it("starts from defaults and empty required containers", () => {
    const root = buildForm(schema);
    expect(initialValue(root as NonNullable<typeof root>)).toEqual({ name: "", limits: {} });
    const withDefault = buildForm({
      type: "object",
      properties: { region: { type: "string", default: "eu" } },
    });
    expect(initialValue(withDefault as NonNullable<typeof withDefault>)).toEqual({});
  });

  it("validates the rendered subset", () => {
    const issues = validateValue(schema, {
      name: 3,
      replicas: 0,
      tier: "gold",
      tags: ["a", 1],
      limits: {},
      zzz: true,
    });
    const byPath = Object.fromEntries(issues.map((i) => [i.path.join("."), i.message]));
    expect(byPath.name).toContain("must be string");
    expect(byPath.replicas).toBe("must be at least 1");
    expect(byPath.enabled).toBe("is required");
    expect(byPath.tier).toContain('must be one of "free", "pro"');
    expect(byPath["tags.1"]).toContain("must be string");
    expect(byPath["limits.rps"]).toBe("is required");
    expect(byPath.zzz).toBe("is not a declared property");
  });

  it("parses number drafts leniently while typing", () => {
    expect(parseNumberDraft("", true)).toEqual({ value: undefined, error: null });
    expect(parseNumberDraft("42", true)).toEqual({ value: 42, error: null });
    expect(parseNumberDraft("4.5", true).error).toBe("must be a whole number");
    expect(parseNumberDraft("4.5", false)).toEqual({ value: 4.5, error: null });
    expect(parseNumberDraft("-", false).error).toBe("must be a number");
  });

  it("sets and removes nested values immutably", () => {
    const base = { limits: { rps: 1 }, name: "a" };
    const next = setAt(base, ["limits", "rps"], 2) as Record<string, unknown>;
    expect(next).toEqual({ limits: { rps: 2 }, name: "a" });
    expect(base.limits.rps).toBe(1);
    expect(setAt(next, ["limits", "rps"], undefined)).toEqual({ limits: {}, name: "a" });
    const root = buildForm(schema);
    expect(extraKeys(root as NonNullable<typeof root>, { name: "x", zzz: 1 })).toEqual(["zzz"]);
  });
});

describe("SchemaForm", () => {
  it("renders every schema property as a field and seeds required fields", async () => {
    render(<Harness />);
    expect(screen.getByRole("group", { name: "Value editor" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Form" })).toHaveAttribute("aria-pressed", "true");
    for (const label of ["name", "replicas", "tier", "tags", "custom"]) {
      expect(screen.getByText(label)).toBeInTheDocument();
    }
    expect(screen.getByText("Service name")).toBeInTheDocument();
    expect(screen.getByRole("group", { name: /^limits/ })).toBeInTheDocument();
    await waitFor(() => expect(out()).toEqual({ name: "", limits: {} }));
    expect(screen.getByTestId("schema-form-summary")).toHaveTextContent("3 schema issues");
    expect(screen.getByText("Edited as JSON — this property uses oneOf.")).toBeInTheDocument();
  });

  it("writes typed values back to the JSON text", async () => {
    render(<Harness initial='{"name":"api","limits":{"rps":1}}' />);
    fireEvent.change(screen.getByLabelText(/^name/), { target: { value: "web" } });
    expect(out()).toMatchObject({ name: "web" });

    const replicas = screen.getByLabelText(/^replicas/);
    fireEvent.change(replicas, { target: { value: "3" } });
    expect(out()).toMatchObject({ replicas: 3 });
    fireEvent.change(replicas, { target: { value: "x" } });
    expect(screen.getByText("must be a number")).toBeInTheDocument();
    expect(out()).toMatchObject({ replicas: 3 });

    fireEvent.click(screen.getByText("enabled"));
    await waitFor(() => expect(out()).toMatchObject({ enabled: true }));

    fireEvent.change(screen.getByLabelText(/^rps/), { target: { value: "2.5" } });
    expect(out()).toMatchObject({ limits: { rps: 2.5 } });

    await chooseSelectOption(screen.getByRole("combobox", { name: /^tier/ }), "pro");
    await waitFor(() => expect(out()).toMatchObject({ tier: "pro" }));

    fireEvent.click(screen.getByRole("button", { name: "Add tags item" }));
    fireEvent.change(screen.getByLabelText("tags item 1"), { target: { value: "blue" } });
    expect(out()).toMatchObject({ tags: ["blue"] });
    fireEvent.click(screen.getByRole("button", { name: "Remove tags item 1" }));
    expect(out()).toMatchObject({ tags: [] });

    fireEvent.change(screen.getByLabelText(/^custom/), { target: { value: "{bad" } });
    expect(screen.getByText("must be valid JSON")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText(/^custom/), { target: { value: '"ok"' } });
    expect(out()).toMatchObject({ custom: "ok" });
  });

  it("keeps undeclared keys editable as JSON and flags them", () => {
    render(<Harness initial='{"name":"a","limits":{"rps":1},"zzz":1}' />);
    const extras = screen.getByLabelText(/Other properties/);
    expect(extras).toHaveValue('{\n  "zzz": 1\n}');
    expect(screen.getByText(/zzz is not a declared property/)).toBeInTheDocument();
    fireEvent.change(extras, { target: { value: "{}" } });
    expect(out()).toEqual({ name: "a", limits: { rps: 1 } });
  });

  it("round-trips through the JSON editor and locks the form on invalid JSON", () => {
    render(<Harness initial='{"name":"a","limits":{"rps":1}}' />);
    fireEvent.click(screen.getByRole("button", { name: "JSON" }));
    const editor = screen.getByLabelText("Value");
    // Untouched source text is shown as-is; the form only re-serialises on edits.
    expect(editor).toHaveValue('{"name":"a","limits":{"rps":1}}');
    fireEvent.change(editor, { target: { value: "{" } });
    expect(screen.getByRole("button", { name: "Form" })).toBeDisabled();
    expect(screen.getByText("Fix the JSON to use the form.")).toBeInTheDocument();
    fireEvent.change(editor, { target: { value: '{"name":"b","limits":{"rps":2}}' } });
    fireEvent.click(screen.getByRole("button", { name: "Form" }));
    expect(screen.getByLabelText(/^name/)).toHaveValue("b");
    expect(screen.getByLabelText(/^rps/)).toHaveValue("2");
  });

  it("starts in JSON mode when the value is not an object", () => {
    render(<Harness initial="[1,2]" />);
    expect(screen.getByLabelText("Value")).toHaveValue("[1,2]");
    expect(screen.getByRole("button", { name: "Form" })).toBeDisabled();
  });

  it("disables every control when disabled", () => {
    render(<Harness initial='{"name":"a","limits":{"rps":1}}' disabled />);
    expect(screen.getByLabelText(/^name/)).toBeDisabled();
    expect(screen.getByRole("button", { name: "JSON" })).toBeDisabled();
  });
});

describe("SchemaForm as a labelled control", () => {
  it("forwards the field wiring to the JSON textarea", () => {
    render(
      <SchemaForm
        schema={schema}
        value="[1]"
        onChange={() => undefined}
        id="value-control"
        aria-invalid
        aria-describedby="value-hint"
        jsonLabel="Value"
      />,
    );
    const textarea = screen.getByRole("textbox", { name: "Value" });
    expect(textarea).toHaveAttribute("id", "value-control");
    expect(textarea).toHaveAttribute("aria-invalid", "true");
    expect(textarea.getAttribute("aria-describedby")).toContain("value-hint");
  });

  it("opens an inferred schema on the JSON view and says where the fields came from", () => {
    render(
      <SchemaForm
        schema={schema}
        value='{"name":"svc","replicas":2,"enabled":true,"limits":{"rps":1}}'
        onChange={() => undefined}
        captionSource="inferred"
      />,
    );
    expect(screen.getByRole("button", { name: "JSON" })).toHaveAttribute("aria-pressed", "true");
    fireEvent.click(screen.getByRole("button", { name: "Form" }));
    expect(screen.getByText(/Fields inferred from the current value/)).toBeVisible();
    expect(screen.queryByTestId("schema-form-summary")).toBeNull();
  });
});
