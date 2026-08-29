import { act, cleanup, fireEvent, render, renderHook, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { assignRef, focusFirstInvalid, useFocusFirstInvalid } from "@/lib/forms";
import {
  lastNamespace,
  rememberNamespace,
  resetNamespaceMemory,
  useLastNamespace,
} from "@/lib/namespace-memory";

afterEach(cleanup);

describe("assignRef", () => {
  it("writes to object refs and calls function refs", () => {
    const object = { current: null as HTMLElement | null };
    const node = document.createElement("input");
    assignRef(object, node);
    expect(object.current).toBe(node);

    let seen: HTMLElement | null = null;
    assignRef((element: HTMLElement | null) => {
      seen = element;
    }, node);
    expect(seen).toBe(node);

    expect(() => assignRef(undefined, node)).not.toThrow();
  });
});

describe("focusFirstInvalid", () => {
  it("focuses the first invalid control and returns it", () => {
    render(
      <form aria-label="probe">
        <input aria-label="Name" />
        <input aria-label="Key" aria-invalid="true" />
        <input aria-label="Other" aria-invalid="true" />
      </form>,
    );
    const focused = focusFirstInvalid(screen.getByRole("form", { name: "probe" }));
    expect(focused).toBe(screen.getByLabelText("Key"));
    expect(screen.getByLabelText("Key")).toHaveFocus();
  });

  it("descends into a flagged frame to reach its control", () => {
    render(
      <form aria-label="probe">
        <div data-invalid="">
          <button type="button">Format</button>
          <textarea aria-label="Value" />
        </div>
      </form>,
    );
    // The frame is not focusable itself; its first focusable child stands in.
    const focused = focusFirstInvalid(screen.getByRole("form", { name: "probe" }));
    expect(focused).toBe(screen.getByRole("button", { name: "Format" }));
  });

  it("returns null when nothing is invalid", () => {
    render(
      <form aria-label="probe">
        <input aria-label="Name" />
      </form>,
    );
    expect(focusFirstInvalid(screen.getByRole("form", { name: "probe" }))).toBeNull();
  });
});

describe("useFocusFirstInvalid", () => {
  function Probe() {
    const { formRef, requestFocus } = useFocusFirstInvalid();
    return (
      <form
        ref={formRef}
        onSubmit={(event) => {
          event.preventDefault();
          requestFocus();
        }}
      >
        <input aria-label="Name" />
        <input aria-label="Key" aria-invalid="true" />
        <button type="submit">Save</button>
      </form>
    );
  }

  it("moves focus to the first invalid field after a blocked submit", () => {
    render(<Probe />);
    expect(screen.getByLabelText("Key")).not.toHaveFocus();
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(screen.getByLabelText("Key")).toHaveFocus();
  });
});

describe("namespace memory", () => {
  beforeEach(() => resetNamespaceMemory());

  it("remembers the last namespace and notifies subscribers", () => {
    const { result } = renderHook(() => useLastNamespace());
    expect(result.current).toBeNull();

    act(() => rememberNamespace({ env: "prod", app: "billing" }));
    expect(result.current).toEqual({ env: "prod", app: "billing" });
    expect(lastNamespace()).toEqual({ env: "prod", app: "billing" });

    act(() => rememberNamespace(null));
    expect(result.current).toBeNull();
  });

  it("survives a reload through session storage", () => {
    rememberNamespace({ env: "staging", app: "search" });
    expect(sessionStorage.getItem("kms.lastNamespace")).toBe(
      JSON.stringify({ env: "staging", app: "search" }),
    );

    // A fresh module state (next page load) rehydrates from storage.
    resetNamespaceMemory();
    sessionStorage.setItem("kms.lastNamespace", JSON.stringify({ env: "staging", app: "search" }));
    expect(lastNamespace()).toEqual({ env: "staging", app: "search" });
  });

  it("ignores malformed storage", () => {
    sessionStorage.setItem("kms.lastNamespace", '{"env":1}');
    expect(lastNamespace()).toBeNull();
  });
});
