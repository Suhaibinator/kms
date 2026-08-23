import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { useRef } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ConfirmDialog, Modal } from "@/components/Modal";

afterEach(cleanup);

function popupOf(name: string): HTMLElement {
  return screen.getByRole("dialog", { name });
}

function bodyOf(dialog: HTMLElement): HTMLElement {
  const body = dialog.querySelector<HTMLElement>("[data-modal-body]");
  if (!body) throw new Error("modal body not found");
  return body;
}

describe("Modal", () => {
  it("renders the title, children and footer", () => {
    render(
      <Modal
        open
        title="Edit"
        onClose={() => undefined}
        footer={<button type="button">Save</button>}
      >
        <p>Body copy</p>
      </Modal>,
    );

    const dialog = popupOf("Edit");
    expect(within(dialog).getByText("Body copy")).toBeInTheDocument();
    expect(within(dialog).getByRole("button", { name: "Save" })).toBeInTheDocument();
  });

  it("scrolls the body, not the popup", () => {
    render(
      <Modal open title="Edit" onClose={() => undefined}>
        <p>Body copy</p>
      </Modal>,
    );

    const dialog = popupOf("Edit");
    expect(dialog).toHaveClass("overflow-hidden");
    expect(dialog).toHaveClass("grid-rows-[auto_minmax(0,1fr)_auto]");
    expect(dialog).not.toHaveClass("overflow-y-auto");

    const body = bodyOf(dialog);
    expect(body).toHaveClass("overflow-y-auto");
    expect(body).toHaveClass("min-h-0");
  });

  it("keeps the row layout when workspace only changes the footprint", () => {
    render(
      <Modal open workspace title="Inspector" onClose={() => undefined}>
        <p>Body copy</p>
      </Modal>,
    );

    const dialog = popupOf("Inspector");
    expect(dialog).toHaveClass("h-[calc(100dvh-2rem)]");
    expect(dialog).toHaveClass("sm:max-w-[min(1200px,calc(100vw-2rem))]");
    expect(dialog).toHaveClass("overflow-hidden");
    expect(dialog).not.toHaveClass("overflow-y-auto");
    expect(bodyOf(dialog)).toHaveClass("overflow-y-auto");
  });

  it("focuses the element named by initialFocus", async () => {
    function Probe() {
      const ref = useRef<HTMLInputElement>(null);
      return (
        <Modal open title="Edit" onClose={() => undefined} initialFocus={ref}>
          <input aria-label="First" />
          <input aria-label="Second" ref={ref} />
        </Modal>
      );
    }
    render(<Probe />);

    await waitFor(() => expect(screen.getByLabelText("Second")).toHaveFocus());
  });

  it("hides the close button when it is not dismissible", () => {
    const { rerender } = render(
      <Modal open title="Edit" onClose={() => undefined}>
        <p>Body copy</p>
      </Modal>,
    );
    expect(within(popupOf("Edit")).getByRole("button", { name: "Dismiss dialog" })).toBeVisible();

    rerender(
      <Modal open dismissible={false} title="Edit" onClose={() => undefined}>
        <p>Body copy</p>
      </Modal>,
    );
    expect(
      within(popupOf("Edit")).queryByRole("button", { name: "Dismiss dialog" }),
    ).not.toBeInTheDocument();
  });
});

describe("ConfirmDialog", () => {
  function renderConfirm(props: Partial<Parameters<typeof ConfirmDialog>[0]> = {}) {
    const onConfirm = vi.fn();
    const onCancel = vi.fn();
    render(
      <ConfirmDialog
        open
        title="Delete secret"
        message="This cannot be undone."
        requireText="db-password"
        onConfirm={onConfirm}
        onCancel={onCancel}
        {...props}
      />,
    );
    const dialog = popupOf("Delete secret");
    const form = dialog.querySelector("form");
    if (!form) throw new Error("confirm form not found");
    return { onConfirm, onCancel, dialog, form };
  }

  it("confirms on submit once the required text matches", () => {
    const { onConfirm, dialog, form } = renderConfirm();

    const input = within(dialog).getByRole("textbox");
    fireEvent.change(input, { target: { value: "db-password" } });
    fireEvent.submit(form);

    expect(onConfirm).toHaveBeenCalledTimes(1);
  });

  it("confirms when the footer button submits the body form", () => {
    // The confirm button sits outside the <form>; only the `form` attribute
    // keeps every page's delete flow working.
    const { onConfirm, dialog } = renderConfirm({ requireText: undefined });

    fireEvent.click(within(dialog).getByRole("button", { name: "Confirm" }));

    expect(onConfirm).toHaveBeenCalledTimes(1);
  });

  it("ignores a submit while the required text does not match", () => {
    const { onConfirm, dialog, form } = renderConfirm();

    fireEvent.change(within(dialog).getByRole("textbox"), { target: { value: "db-passwor" } });
    fireEvent.submit(form);

    expect(onConfirm).not.toHaveBeenCalled();
    expect(within(dialog).getByRole("button", { name: "Confirm" })).toBeDisabled();
  });

  it("gives each mounted confirm input its own id", () => {
    render(
      <>
        <ConfirmDialog
          open
          title="Delete one"
          message="Gone forever."
          requireText="one"
          onConfirm={() => undefined}
          onCancel={() => undefined}
        />
        <ConfirmDialog
          open
          title="Delete two"
          message="Gone forever."
          requireText="two"
          onConfirm={() => undefined}
          onCancel={() => undefined}
        />
      </>,
    );

    // Base UI aria-hides the portal root while two dialogs are open at once, so
    // role queries find neither; the ids are what this guards, not the roles.
    const ids = Array.from(
      document.querySelectorAll<HTMLInputElement>("[data-modal-body] input"),
    ).map((input) => input.id);
    expect(ids).toHaveLength(2);
    expect(ids.every(Boolean)).toBe(true);
    expect(new Set(ids).size).toBe(2);
  });

  it("locks the dialog down while busy", () => {
    const { dialog } = renderConfirm({ busy: true, requireText: undefined });

    // The busy spinner contributes "Loading" to the button's accessible name.
    expect(within(dialog).getByRole("button", { name: /Confirm/ })).toBeDisabled();
    expect(within(dialog).getByRole("button", { name: "Cancel" })).toBeDisabled();
    expect(
      within(dialog).queryByRole("button", { name: "Dismiss dialog" }),
    ).not.toBeInTheDocument();
  });
});
