import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import CreateApplicationWizard from "@/components/applications/CreateApplicationWizard";
import { ApiError } from "@/lib/api";
import type { Application } from "@/lib/types";
import { chooseSelectOption } from "./select-test-utils";

const mocks = vi.hoisted(() => ({
  createApplication: vi.fn(),
  createNamespace: vi.fn(),
  createSchema: vi.fn(),
  listSchemas: vi.fn(),
  toast: { success: vi.fn(), info: vi.fn(), error: vi.fn() },
}));

vi.mock("@/context/ToastContext", () => ({ useToast: () => mocks.toast }));
vi.mock("@/lib/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/api")>();
  return {
    ...actual,
    api: {
      ...actual.api,
      createApplication: mocks.createApplication,
      createNamespace: mocks.createNamespace,
      createSchema: mocks.createSchema,
      listSchemas: mocks.listSchemas,
    },
  };
});

const created: Application = {
  name: "payments-api",
  description: "",
  release_name: "runtime",
  schema_id: "",
  schema_version: 0,
  contract: [{ alias: "runtime", kind: "parameter", content_type: "json" }],
  created_by: "admin",
  created_at_unix_ms: 1,
  updated_at_unix_ms: 1,
  environment_count: 0,
};

const SCHEMA = JSON.stringify({
  $schema: "https://json-schema.org/draft/2020-12/schema",
  type: "object",
  additionalProperties: false,
  required: ["timeout", "database"],
  properties: { timeout: { type: "integer" }, database: { type: "object" } },
});

const dialog = () => screen.getByRole("dialog");
const next = () =>
  fireEvent.click(within(dialog()).getByRole("button", { name: /^(Next|Register and continue)$/ }));

async function fillBasics() {
  fireEvent.change(within(dialog()).getByLabelText("Application name"), {
    target: { value: "payments-api" },
  });
  next();
  await screen.findByLabelText("Schema");
}

async function toEnvironments() {
  next(); // schema: none
  await screen.findByRole("list", { name: "Contract aliases" });
  next(); // contract
  await screen.findByRole("button", { name: "Create application" });
}

describe("CreateApplicationWizard", () => {
  beforeEach(() => {
    mocks.createApplication.mockReset().mockResolvedValue({ application: created });
    mocks.createNamespace.mockReset().mockResolvedValue({ namespace: {} });
    mocks.createSchema.mockReset();
    mocks.listSchemas.mockReset().mockResolvedValue({ schemas: [], next_page_token: "" });
    mocks.toast.error.mockClear();
    mocks.toast.success.mockClear();
  });

  it("walks Basics → Schema → Contract → Environments and attaches existing namespaces", async () => {
    const onCreated = vi.fn();
    render(<CreateApplicationWizard open onClose={() => undefined} onCreated={onCreated} />);
    expect(within(dialog()).getByRole("button", { name: "Next" })).toBeDisabled();
    await fillBasics();
    await toEnvironments();
    // The default contract row carried through.
    expect(within(dialog()).getByLabelText("Environment")).toHaveValue("dev");
    fireEvent.click(within(dialog()).getByRole("button", { name: "Add environment" }));
    const inputs = within(dialog()).getAllByLabelText("Environment");
    fireEvent.change(inputs[1], { target: { value: "prod" } });
    expect(within(dialog()).getByText("production")).toBeVisible();

    mocks.createNamespace.mockImplementation(async (req: { env: string }) => {
      if (req.env === "prod") throw new ApiError("already_exists", "exists", 409);
      return { namespace: {} };
    });
    fireEvent.click(within(dialog()).getByRole("button", { name: "Create application" }));
    await waitFor(() => expect(onCreated).toHaveBeenCalledWith(created));
    expect(mocks.createSchema).not.toHaveBeenCalled();
    expect(mocks.createApplication).toHaveBeenCalledWith({
      name: "payments-api",
      description: "",
      release_name: "runtime",
      schema_id: "",
      schema_version: 0,
      contract: [{ alias: "runtime", kind: "parameter", content_type: "json" }],
    });
    expect(mocks.createApplication.mock.invocationCallOrder[0]).toBeLessThan(
      mocks.createNamespace.mock.invocationCallOrder[0],
    );
    expect(mocks.createNamespace.mock.calls.map((call) => call[0].env)).toEqual(["dev", "prod"]);
    expect(within(dialog()).getByText("attached")).toBeVisible();
  });

  it("registers a pasted schema before the contract step and derives the contract from it", async () => {
    mocks.createSchema.mockResolvedValue({
      schema: { id: "payments-api-runtime", version: 4, schema_json: SCHEMA },
    });
    render(<CreateApplicationWizard open onClose={() => undefined} onCreated={vi.fn()} />);
    await fillBasics();
    await chooseSelectOption(within(dialog()).getByLabelText("Schema"), "Register a new schema");
    expect(within(dialog()).getByLabelText("Schema ID")).toHaveValue("payments-api-runtime");
    fireEvent.change(within(dialog()).getByLabelText("Schema JSON"), { target: { value: SCHEMA } });
    next();
    await screen.findByRole("list", { name: "Contract aliases" });
    expect(mocks.createSchema).toHaveBeenCalledWith("payments-api-runtime", SCHEMA);
    expect(mocks.createApplication).not.toHaveBeenCalled();
    expect(within(dialog()).getByLabelText("Alias 1")).toHaveValue("timeout");
    expect(within(dialog()).getByLabelText("Alias 2")).toHaveValue("database");
    expect(within(dialog()).getByText("Aligned with the schema.")).toBeVisible();
    next();
    fireEvent.click(await screen.findByRole("button", { name: "Create application" }));
    await waitFor(() => expect(mocks.createApplication).toHaveBeenCalledTimes(1));
    expect(mocks.createApplication.mock.calls[0][0]).toMatchObject({
      schema_id: "payments-api-runtime",
      schema_version: 4,
      contract: [
        { alias: "timeout", kind: "parameter", content_type: "integer" },
        { alias: "database", kind: "parameter", content_type: "json" },
      ],
    });
  });

  it("keeps the wizard open with a retry when an environment fails, without recreating the app", async () => {
    const onCreated = vi.fn();
    mocks.createNamespace.mockRejectedValueOnce(new Error("quota exceeded"));
    render(<CreateApplicationWizard open onClose={() => undefined} onCreated={onCreated} />);
    await fillBasics();
    await toEnvironments();
    fireEvent.click(within(dialog()).getByRole("button", { name: "Create application" }));
    expect(await within(dialog()).findByText("failed")).toBeVisible();
    expect(within(dialog()).getByText("quota exceeded")).toBeVisible();
    expect(onCreated).not.toHaveBeenCalled();
    expect(mocks.toast.success).toHaveBeenCalledWith(
      "Application created",
      expect.stringContaining("payments-api"),
    );

    fireEvent.click(within(dialog()).getByRole("button", { name: "Retry" }));
    await waitFor(() => expect(onCreated).toHaveBeenCalledWith(created));
    expect(mocks.createApplication).toHaveBeenCalledTimes(1);
    expect(mocks.createNamespace).toHaveBeenCalledTimes(2);
  });

  it("does not register the schema when registration fails, and stays on the step", async () => {
    mocks.createSchema.mockRejectedValue(new Error("schema invalid at /properties"));
    render(<CreateApplicationWizard open onClose={() => undefined} onCreated={vi.fn()} />);
    await fillBasics();
    await chooseSelectOption(within(dialog()).getByLabelText("Schema"), "Register a new schema");
    fireEvent.change(within(dialog()).getByLabelText("Schema JSON"), { target: { value: SCHEMA } });
    next();
    expect(await within(dialog()).findByRole("alert")).toHaveTextContent("schema invalid");
    expect(within(dialog()).getByLabelText("Schema JSON")).toBeVisible();
    expect(mocks.createApplication).not.toHaveBeenCalled();
  });
});
