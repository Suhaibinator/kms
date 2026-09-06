import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { AUTO_COLLAPSE_CHILDREN } from "@/components/JsonTree";
import { ValueView } from "@/components/JsonView";
import { JsonView } from "@/components/ui";
import { formatJson } from "@/lib/json-text";

vi.mock("@/context/ToastContext", () => ({
  useToast: () => ({ error: vi.fn(), success: vi.fn() }),
}));

afterEach(cleanup);

/**
 * Twelve lines when everything is expanded:
 * 1 `{`, 2 name, 3-6 tags, 7-10 limits, 11 on, 12 `}`.
 */
const DOC =
  formatJson('{"name":"api","tags":["a","b"],"limits":{"rps":10,"burst":20},"on":true}') ?? "";

function block(): HTMLElement {
  const found = document.querySelector("pre.json-block");
  if (!found) throw new Error("no json block rendered");
  return found as HTMLElement;
}

function lineNumbers(): (string | null)[] {
  return Array.from(document.querySelectorAll(".json-tree-line")).map((line) =>
    line.getAttribute("data-line"),
  );
}

describe("JsonView", () => {
  it("colours a document and renders one row per line", () => {
    render(<JsonView raw={DOC} />);
    expect(block().textContent).toBe(DOC);
    expect(document.querySelectorAll(".json-tree-line")).toHaveLength(12);
    expect(document.querySelectorAll(".tok-key")).toHaveLength(6);
    expect(block().querySelector(".tok-boolean")?.textContent).toBe("true");
    expect(lineNumbers()).toEqual(["1", "2", "3", "4", "5", "6", "7", "8", "9", "10", "11", "12"]);
  });

  it("collapses a node behind a summary that keeps the bracket and comma", () => {
    render(<JsonView raw={DOC} />);
    const caret = screen.getByRole("button", { name: "Collapse limits" });
    expect(caret).toHaveAttribute("aria-expanded", "true");

    fireEvent.click(caret);

    expect(block().textContent).toBe(
      '{\n  "name": "api",\n  "tags": [\n    "a",\n    "b"\n  ],\n  "limits": { … 2 keys },\n  "on": true\n}',
    );
    expect(screen.queryByText("10")).toBeNull();
    const collapsed = screen.getByRole("button", { name: "Expand limits" });
    expect(collapsed).toHaveAttribute("aria-expanded", "false");
    // The four folded lines keep their numbers; the gap is the signal.
    expect(lineNumbers()).toEqual(["1", "2", "3", "4", "5", "6", "7", "11", "12"]);

    fireEvent.click(collapsed);
    expect(block().textContent).toBe(DOC);
  });

  it("names every caret by its path, including array items", () => {
    render(<JsonView raw={DOC} />);
    expect(screen.getByRole("button", { name: "Collapse root" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Collapse tags" })).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Collapse tags" }));
    expect(block().textContent).toContain('"tags": [ … 2 items ],');
  });

  it("expands and collapses everything from the toolbar", () => {
    render(<JsonView raw={DOC} />);
    const expandAll = screen.getByRole("button", { name: "Expand all" });
    const collapseAll = screen.getByRole("button", { name: "Collapse all" });
    expect(expandAll).toBeDisabled();
    expect(collapseAll).toBeEnabled();

    fireEvent.click(collapseAll);
    expect(document.querySelectorAll(".json-tree-line")).toHaveLength(1);
    expect(block().textContent).toBe("{ … 4 keys }");
    expect(collapseAll).toBeDisabled();
    expect(expandAll).toBeEnabled();

    fireEvent.click(expandAll);
    expect(block().textContent).toBe(DOC);
    expect(document.querySelectorAll(".json-tree-line")).toHaveLength(12);
    expect(expandAll).toBeDisabled();
  });

  it("keeps the collapse state per node and drops it when the text changes", () => {
    const { rerender } = render(<JsonView raw={DOC} />);
    fireEvent.click(screen.getByRole("button", { name: "Collapse limits" }));
    expect(document.querySelectorAll(".json-tree-line")).toHaveLength(9);

    rerender(<JsonView raw={formatJson('{"limits":{"rps":1}}') ?? ""} />);
    expect(screen.getByRole("button", { name: "Collapse limits" })).toBeInTheDocument();
    expect(block().textContent).toBe('{\n  "limits": {\n    "rps": 1\n  }\n}');
  });

  it("starts a very wide container collapsed", () => {
    const wide = JSON.stringify(
      { small: [1, 2], wide: Array.from({ length: AUTO_COLLAPSE_CHILDREN + 1 }, (_, i) => i) },
      null,
      2,
    );
    render(<JsonView raw={wide} />);
    expect(block().textContent).toContain(`"wide": [ … ${AUTO_COLLAPSE_CHILDREN + 1} items ]`);
    expect(block().textContent).toContain('"small": [\n    1,\n    2\n  ]');
    expect(screen.getByRole("button", { name: "Expand all" })).toBeEnabled();
  });

  it("leaves anything else as a plain highlighted block", () => {
    render(<JsonView raw="not json" />);
    expect(block().textContent).toBe("not json");
    expect(document.querySelector(".json-tree")).toBeNull();
    expect(document.querySelector(".json-highlight")).not.toBeNull();
    expect(block().querySelector("[class^=tok-]")).toBeNull();
    expect(screen.queryByRole("button", { name: "Expand all" })).toBeNull();
  });

  it("leaves a document with nothing to fold to the highlighted block", () => {
    render(<JsonView raw="{}" />);
    expect(block().textContent).toBe("{}");
    expect(document.querySelector(".json-tree")).toBeNull();
    expect(screen.queryByRole("button", { name: "Collapse all" })).toBeNull();
  });

  it("renders the plain highlight when collapsing is off", () => {
    render(<JsonView raw={DOC} collapsible={false} />);
    expect(document.querySelector(".json-tree")).toBeNull();
    expect(document.querySelector(".json-highlight")).toHaveAttribute("data-line-numbers", "true");
    expect(block().textContent).toBe(DOC);
    expect(screen.queryByRole("button", { name: "Expand all" })).toBeNull();
  });

  it("numbers the lines and offers wrap, copy and a size readout", () => {
    render(<JsonView raw={'{\n  "a": 1\n}'} copyLabel="Copy value" />);
    expect(document.querySelector(".json-tree")).toHaveAttribute("data-line-numbers", "true");
    expect(screen.getByText("3 lines · 12 bytes")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Copy value" })).toBeInTheDocument();

    const wrap = screen.getByRole("button", { name: "Wrap" });
    expect(wrap).toHaveAttribute("aria-pressed", "true");
    expect(document.querySelector(".json-view")).toHaveAttribute("data-wrap", "on");
    fireEvent.click(wrap);
    expect(document.querySelector(".json-view")).toHaveAttribute("data-wrap", "off");
  });

  it("drops the line numbers when they are off", () => {
    render(<JsonView raw={DOC} lineNumbers={false} />);
    expect(document.querySelector(".json-tree")).not.toHaveAttribute("data-line-numbers");
    expect(lineNumbers()).toEqual(Array.from({ length: 12 }, () => null));
  });
});

describe("JsonView copy", () => {
  const writeText = vi.fn(() => Promise.resolve());

  beforeEach(() => {
    writeText.mockClear();
    Object.defineProperty(navigator, "clipboard", { value: { writeText }, configurable: true });
  });

  it("copies the whole document even while nodes are collapsed", () => {
    render(<JsonView raw={DOC} />);
    fireEvent.click(screen.getByRole("button", { name: "Collapse all" }));
    fireEvent.click(screen.getByRole("button", { name: "Copy" }));
    expect(writeText).toHaveBeenCalledWith(DOC);
  });
});

describe("ValueView", () => {
  it("pretty-prints json and copies the stored form", () => {
    const stored = '{"a":1,"b":[2]}';
    render(<ValueView value={stored} contentType="json" />);
    expect(block().textContent).toBe(formatJson(stored));
    expect(document.querySelector(".tok-key")).not.toBeNull();
    expect(screen.getByRole("button", { name: "Collapse root" })).toBeInTheDocument();
  });

  it("shows other content types verbatim with line numbers and no colouring", () => {
    render(<ValueView value={"first\nsecond"} contentType="string" />);
    expect(block().textContent).toBe("first\nsecond");
    expect(block().querySelectorAll(".json-line")).toHaveLength(2);
    expect(block().querySelector("[class^=tok-]")).toBeNull();
    expect(screen.getByText("2 lines · 12 bytes")).toBeInTheDocument();
  });
});
