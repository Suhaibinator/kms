import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "@/lib/api";
import type { Namespace, Policy } from "@/lib/types";
import PoliciesPage, { effectiveSummary } from "@/pages/policies";
import { chooseSelectOption } from "./select-test-utils";

const mocks = vi.hoisted(() => ({
  namespaces: [] as Namespace[],
  namespacesLoading: false,
  toast: {
    error: vi.fn(),
    success: vi.fn(),
  },
}));

vi.mock("@/context/ToastContext", () => ({ useToast: () => mocks.toast }));
vi.mock("@/lib/hooks", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/hooks")>();
  return {
    ...actual,
    useNamespaces: () => ({
      namespaces: mocks.namespaces,
      loading: mocks.namespacesLoading,
      error: null,
      reload: vi.fn(),
    }),
  };
});

function namespace(app: string, env: string): Namespace {
  return {
    env,
    app,
    description: "",
    allowed_auth_methods: ["mtls"],
    created_by: "admin",
    created_at_unix_ms: 1,
    parameter_count: 0,
    secret_count: 0,
  };
}

function policy(name: string): Policy {
  return {
    name,
    subject: "*",
    allow: [{ operation: "secret:read", env: "*", app: "*" }],
    deny: [],
    created_at_unix_ms: 1,
    updated_at_unix_ms: 1,
  };
}

async function openEditor() {
  render(<PoliciesPage />);
  await screen.findByText("No policies yet");
  fireEvent.click(screen.getAllByRole("button", { name: "New policy" })[0]);
  return screen.getByRole("dialog", { name: "New policy" });
}

/** The option values of the datalist an input points at. */
function datalistValues(input: HTMLElement): string[] {
  const list = document.getElementById(input.getAttribute("list") ?? "");
  return Array.from(list?.querySelectorAll("option") ?? [], (option) => option.value);
}

beforeEach(() => {
  mocks.namespaces = [];
  mocks.namespacesLoading = false;
  mocks.toast.error.mockReset();
  mocks.toast.success.mockReset();
  vi.spyOn(api, "listPolicies").mockResolvedValue({ policies: [], next_page_token: "" });
  vi.spyOn(api, "listIdentities").mockResolvedValue({
    identities: [
      { name: "gradethis-be", kind: "client", namespace: { env: "prod", app: "gradethis" } },
      { name: "deploy-admin", kind: "admin", namespace: null },
    ],
    next_page_token: "",
  });
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe("policy list", () => {
  it("steps back to page 1 after deleting the last policy on page 2", async () => {
    let deleted = false;
    const listPolicies = vi.mocked(api.listPolicies).mockImplementation(async (_size, token) => {
      if (token === "page-2") {
        return { policies: deleted ? [] : [policy("second")], next_page_token: "" };
      }
      return { policies: [policy("first")], next_page_token: deleted ? "" : "page-2" };
    });
    const deletePolicy = vi.spyOn(api, "deletePolicy").mockImplementation(async () => {
      deleted = true;
      return {};
    });
    render(<PoliciesPage />);
    await screen.findByText("first");
    fireEvent.click(screen.getByRole("button", { name: "Next page" }));
    const row = (await screen.findByText("second")).closest("tr") as HTMLElement;
    expect(screen.getByText("Page 2")).toBeVisible();

    fireEvent.click(within(row).getByRole("button", { name: "Delete" }));
    const confirm = screen.getByRole("dialog", { name: "Delete policy?" });
    expect(confirm).toHaveTextContent("Delete policy second?");
    fireEvent.click(within(confirm).getByRole("button", { name: "Delete policy" }));

    await waitFor(() => expect(deletePolicy).toHaveBeenCalledWith("second"));
    await screen.findByText("first");
    await waitFor(() => expect(screen.queryByText("Page 2")).not.toBeInTheDocument());
    expect(listPolicies).toHaveBeenLastCalledWith(100, undefined, expect.anything());
  });
});

describe("policy editor", () => {
  it("keeps a rule's error on that rule after an earlier rule is removed", async () => {
    const dialog = await openEditor();
    const addAllowRule = within(dialog).getAllByRole("button", { name: "Add rule" })[0];
    fireEvent.click(addAllowRule);
    fireEvent.click(addAllowRule);
    fireEvent.click(addAllowRule);
    const appInputs = within(dialog).getAllByPlaceholderText("gradethis");
    expect(appInputs).toHaveLength(3);

    fireEvent.change(appInputs[2], { target: { value: "bad app!" } });
    fireEvent.blur(appInputs[2]);
    expect(within(dialog).getAllByRole("alert")).toHaveLength(1);
    expect(appInputs[2]).toHaveAttribute("aria-invalid", "true");

    fireEvent.click(
      within(dialog).getByRole("button", { name: "Remove allow rule 1: unset operation" }),
    );

    const remaining = within(dialog).getAllByPlaceholderText("gradethis");
    expect(remaining).toHaveLength(2);
    expect(remaining[0]).toHaveValue("*");
    expect(remaining[0]).not.toHaveAttribute("aria-invalid");
    expect(remaining[1]).toHaveValue("bad app!");
    expect(remaining[1]).toHaveAttribute("aria-invalid", "true");
    expect(within(dialog).getAllByRole("alert")).toHaveLength(1);
    expect(
      within(dialog).getByRole("button", { name: "Remove allow rule 2: unset operation" }),
    ).toBeVisible();
  });

  it("submits on Enter and reveals every problem on a failed attempt", async () => {
    const createPolicy = vi
      .spyOn(api, "createPolicy")
      .mockResolvedValue({ policy: policy("read-all") });
    const dialog = await openEditor();
    const form = dialog.querySelector("form") as HTMLFormElement;
    expect(form).not.toBeNull();

    // A pristine editor shows nothing; a failed submit shows both required fields.
    expect(within(dialog).queryByRole("alert")).not.toBeInTheDocument();
    fireEvent.submit(form);
    expect(createPolicy).not.toHaveBeenCalled();
    expect(within(dialog).getByText("Policy name is required.")).toBeVisible();
    expect(within(dialog).getByText("Subject is required.")).toBeVisible();
    // The blocked submit moves focus to the first invalid control.
    expect(within(dialog).getByPlaceholderText("gradethis-read")).toHaveFocus();

    fireEvent.change(within(dialog).getByPlaceholderText("gradethis-read"), {
      target: { value: "read-all" },
    });
    fireEvent.change(within(dialog).getByPlaceholderText("gradethis-be"), {
      target: { value: "*" },
    });
    fireEvent.submit(form);
    await waitFor(() => expect(createPolicy).toHaveBeenCalledOnce());
    expect(createPolicy).toHaveBeenCalledWith(
      expect.objectContaining({ name: "read-all", subject: "*", allow: [], deny: [] }),
    );
    expect(mocks.toast.error).not.toHaveBeenCalled();
  });

  it("starts a new rule without an operation and refuses to save until one is chosen", async () => {
    const createPolicy = vi.spyOn(api, "createPolicy");
    const dialog = await openEditor();
    fireEvent.change(within(dialog).getByPlaceholderText("gradethis-read"), {
      target: { value: "read-all" },
    });
    fireEvent.change(within(dialog).getByPlaceholderText("gradethis-be"), {
      target: { value: "*" },
    });
    fireEvent.click(within(dialog).getAllByRole("button", { name: "Add rule" })[0]);
    const operation = within(dialog).getByRole("combobox", { name: "Operation" });
    expect(operation).toHaveTextContent("Select operation…");

    fireEvent.click(within(dialog).getByRole("button", { name: "Create policy" }));
    expect(within(dialog).getByText("Operation is required.")).toBeVisible();
    expect(createPolicy).not.toHaveBeenCalled();

    await chooseSelectOption(operation, "secret:read");
    expect(within(dialog).queryByText("Operation is required.")).not.toBeInTheDocument();
    expect(
      within(dialog).getByRole("button", { name: "Remove allow rule 1: secret:read" }),
    ).toBeVisible();
  });

  it("duplicates a rule with its app and env", async () => {
    const dialog = await openEditor();
    fireEvent.click(within(dialog).getAllByRole("button", { name: "Add rule" })[0]);
    await chooseSelectOption(
      within(dialog).getByRole("combobox", { name: "Operation" }),
      "secret:read",
    );
    fireEvent.change(within(dialog).getByPlaceholderText("gradethis"), {
      target: { value: "payments" },
    });
    fireEvent.change(within(dialog).getByPlaceholderText("prod"), {
      target: { value: "staging" },
    });

    fireEvent.click(within(dialog).getByRole("button", { name: "Duplicate allow rule 1" }));
    const apps = within(dialog).getAllByPlaceholderText("gradethis");
    const envs = within(dialog).getAllByPlaceholderText("prod");
    expect(apps).toHaveLength(2);
    expect(apps[1]).toHaveValue("payments");
    expect(envs[1]).toHaveValue("staging");
    expect(
      within(dialog).getByRole("button", { name: "Remove allow rule 2: secret:read" }),
    ).toBeVisible();
  });

  it("offers namespaces and identities as suggestions and warns about unknown namespaces", async () => {
    mocks.namespaces = [namespace("payments", "prod"), namespace("payments", "dev")];
    const dialog = await openEditor();
    const subject = within(dialog).getByPlaceholderText("gradethis-be");
    await waitFor(() =>
      expect(datalistValues(subject)).toEqual(["*", "deploy-admin", "gradethis-be"]),
    );

    fireEvent.click(within(dialog).getAllByRole("button", { name: "Add rule" })[0]);
    const app = within(dialog).getByPlaceholderText("gradethis");
    const env = within(dialog).getByPlaceholderText("prod");
    expect(datalistValues(app)).toEqual(["*", "payments"]);
    expect(datalistValues(env)).toEqual(["*", "dev", "prod"]);
    expect(within(dialog).queryByText(/will match nothing/)).not.toBeInTheDocument();

    fireEvent.change(app, { target: { value: "gradethis" } });
    expect(
      within(dialog).getByText(/No namespace for application gradethis exists yet/),
    ).toBeVisible();
    // A warning is not an error: the field stays valid and the alert slot empty.
    expect(app).not.toHaveAttribute("aria-invalid");
    expect(within(dialog).queryByRole("alert")).not.toBeInTheDocument();

    fireEvent.change(app, { target: { value: "*" } });
    expect(within(dialog).queryByText(/will match nothing/)).not.toBeInTheDocument();

    fireEvent.change(app, { target: { value: "payments" } });
    fireEvent.change(env, { target: { value: "staging" } });
    expect(
      within(dialog).getByText(/No namespace named staging\/payments exists yet/),
    ).toBeVisible();
  });

  it("summarises what the policy grants and which grants a deny cancels", async () => {
    const dialog = await openEditor();
    const summary = within(dialog).getByRole("region", { name: "Effective permissions" });
    expect(summary).toHaveTextContent("No rules yet.");

    const [addAllow, addDeny] = within(dialog).getAllByRole("button", { name: "Add rule" });
    fireEvent.click(addAllow);
    await chooseSelectOption(
      within(dialog).getByRole("combobox", { name: "Operation" }),
      "secret:read",
    );
    fireEvent.change(within(dialog).getByPlaceholderText("gradethis"), {
      target: { value: "gradethis" },
    });
    fireEvent.change(within(dialog).getByPlaceholderText("prod"), { target: { value: "prod" } });
    expect(summary).toHaveTextContent("Allows on prod/gradethis: secret:read");
    expect(summary).not.toHaveTextContent("cancelled by deny");

    fireEvent.click(addDeny);
    await chooseSelectOption(
      within(dialog).getAllByRole("combobox", { name: "Operation" })[1],
      "secret:*",
    );
    expect(summary).toHaveTextContent("secret:read (cancelled by deny)");
    expect(summary).toHaveTextContent("Denied (overrides allow) on any/any: secret:*");
  });

  it("asks before discarding an edited policy", async () => {
    const dialog = await openEditor();
    fireEvent.change(within(dialog).getByPlaceholderText("gradethis-read"), {
      target: { value: "read-all" },
    });
    fireEvent.click(within(dialog).getByRole("button", { name: "Cancel" }));
    const confirm = await screen.findByRole("dialog", { name: "Discard changes?", hidden: true });
    fireEvent.click(within(confirm).getByRole("button", { name: "Keep editing", hidden: true }));
    await waitFor(() =>
      expect(
        screen.queryByRole("dialog", { name: "Discard changes?", hidden: true }),
      ).not.toBeInTheDocument(),
    );
    expect(screen.getByRole("dialog", { name: "New policy" })).toBeInTheDocument();
    expect(screen.getByPlaceholderText("gradethis-read")).toHaveValue("read-all");
  });

  it("closes an untouched editor without asking and focuses the name on open", async () => {
    const dialog = await openEditor();
    await waitFor(() =>
      expect(within(dialog).getByPlaceholderText("gradethis-read")).toHaveFocus(),
    );
    fireEvent.click(within(dialog).getByRole("button", { name: "Cancel" }));
    await waitFor(() =>
      expect(screen.queryByRole("dialog", { name: "New policy" })).not.toBeInTheDocument(),
    );
  });
});

describe("effectiveSummary", () => {
  it("cancels allows covered by a wider deny and skips unset operations", () => {
    const summary = effectiveSummary({
      allow: [
        { operation: "secret:read", env: "prod", app: "gradethis" },
        { operation: "parameter:read", env: "prod", app: "gradethis" },
        { operation: "", env: "*", app: "*" },
      ],
      deny: [{ operation: "secret:*", env: "*", app: "*" }],
    });
    expect(summary).toEqual({
      allows: [
        {
          scope: "prod/gradethis",
          operations: [
            { operation: "secret:read", cancelled: true },
            { operation: "parameter:read", cancelled: false },
          ],
        },
      ],
      denies: [{ scope: "any/any", operations: [{ operation: "secret:*", cancelled: false }] }],
    });
  });

  it("does not let a narrower deny cancel a wider allow", () => {
    const summary = effectiveSummary({
      allow: [{ operation: "secret:read", env: "*", app: "*" }],
      deny: [{ operation: "secret:read", env: "prod", app: "gradethis" }],
    });
    expect(summary.allows[0].operations[0].cancelled).toBe(false);
  });
});
