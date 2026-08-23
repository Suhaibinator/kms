import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "@/lib/api";
import type { Policy } from "@/lib/types";
import PoliciesPage from "@/pages/policies";

const mocks = vi.hoisted(() => ({
  toast: {
    error: vi.fn(),
    success: vi.fn(),
  },
}));

vi.mock("@/context/ToastContext", () => ({ useToast: () => mocks.toast }));

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

beforeEach(() => {
  mocks.toast.error.mockReset();
  mocks.toast.success.mockReset();
  vi.spyOn(api, "listPolicies").mockResolvedValue({ policies: [], next_page_token: "" });
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
      within(dialog).getByRole("button", { name: "Remove allow rule 1: secret:read" }),
    );

    const remaining = within(dialog).getAllByPlaceholderText("gradethis");
    expect(remaining).toHaveLength(2);
    expect(remaining[0]).toHaveValue("*");
    expect(remaining[0]).not.toHaveAttribute("aria-invalid");
    expect(remaining[1]).toHaveValue("bad app!");
    expect(remaining[1]).toHaveAttribute("aria-invalid", "true");
    expect(within(dialog).getAllByRole("alert")).toHaveLength(1);
    expect(
      within(dialog).getByRole("button", { name: "Remove allow rule 2: secret:read" }),
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
});
