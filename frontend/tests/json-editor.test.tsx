import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { useState } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { JsonEditor } from "@/components/JsonEditor";
import { ValueView } from "@/components/JsonView";
import { Field, JsonView } from "@/components/ui";
import { formatJson, HIGHLIGHT_MAX_BYTES } from "@/lib/json-text";
import { validateParameterValue } from "@/lib/validation";

vi.mock("@/context/ToastContext", () => ({
  useToast: () => ({ error: vi.fn(), success: vi.fn() }),
}));

afterEach(cleanup);

function Harness({
  initial,
  onSubmit,
  toolbar,
}: {
  initial: string;
  onSubmit?: () => void;
  toolbar?: "full" | "minimal";
}) {
  const [value, setValue] = useState(initial);
  return (
    <Field label="Value" error={value.includes("bad") ? "Bad value" : null}>
      <JsonEditor value={value} onChange={setValue} onSubmit={onSubmit} toolbar={toolbar} />
    </Field>
  );
}

function textbox(): HTMLTextAreaElement {
  return screen.getByRole("textbox", { name: "Value" });
}

describe("JsonEditor", () => {
  it("keeps the textarea as the labelled control and hides the highlight layer", () => {
    render(<Harness initial='{"a": 1}' />);
    const control = textbox();
    expect(control.tagName).toBe("TEXTAREA");
    expect(control).toHaveAttribute("id");
    expect(control).toHaveAccessibleDescription(/Tab inserts two spaces/);
    const highlight = document.querySelector(".json-editor-highlight");
    expect(highlight).toHaveAttribute("aria-hidden", "true");
    expect(highlight?.querySelector(".tok-key")?.textContent).toBe('"a"');
    expect(highlight?.querySelector(".tok-number")?.textContent).toBe("1");
  });

  it("propagates the field's error state to the frame", () => {
    render(<Harness initial='"bad"' />);
    expect(textbox()).toHaveAttribute("aria-invalid", "true");
    expect(document.querySelector(".json-editor")).toHaveAttribute("data-invalid", "true");
  });

  it("formats and minifies through onChange, and disables both while the JSON is broken", () => {
    render(<Harness initial='{"a":1,"b":[1,2]}' />);
    fireEvent.click(screen.getByRole("button", { name: "Format" }));
    expect(textbox()).toHaveValue('{\n  "a": 1,\n  "b": [\n    1,\n    2\n  ]\n}');
    fireEvent.click(screen.getByRole("button", { name: "Minify" }));
    expect(textbox()).toHaveValue('{"a":1,"b":[1,2]}');

    fireEvent.change(textbox(), { target: { value: '{"a":1,}' } });
    expect(screen.getByRole("button", { name: "Format" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Minify" })).toBeDisabled();
    expect(screen.getByRole("status")).toHaveTextContent(
      'line 1, col 8: Trailing comma before "}"',
    );
    expect(document.querySelector(".json-line.is-error")).not.toBeNull();
  });

  it("toggles wrapping", () => {
    render(<Harness initial="{}" />);
    const wrap = screen.getByRole("button", { name: "Wrap" });
    expect(wrap).toHaveAttribute("aria-pressed", "true");
    expect(document.querySelector(".json-editor")).toHaveAttribute("data-wrap", "on");
    fireEvent.click(wrap);
    expect(wrap).toHaveAttribute("aria-pressed", "false");
    expect(document.querySelector(".json-editor")).toHaveAttribute("data-wrap", "off");
  });

  it("inserts two spaces on Tab but leaves Shift+Tab alone", () => {
    render(<Harness initial="ab" />);
    const control = textbox();
    control.setSelectionRange(1, 1);
    const tab = fireEvent.keyDown(control, { key: "Tab" });
    expect(tab).toBe(false);
    expect(control).toHaveValue("a  b");
    expect(control.selectionStart).toBe(3);
    const shiftTab = fireEvent.keyDown(control, { key: "Tab", shiftKey: true });
    expect(shiftTab).toBe(true);
  });

  it("keeps the indent on Enter and deepens it after an opener", () => {
    render(<Harness initial={'{\n  "a": {'} />);
    const control = textbox();
    control.setSelectionRange(control.value.length, control.value.length);
    fireEvent.keyDown(control, { key: "Enter" });
    expect(control).toHaveValue('{\n  "a": {\n    ');
  });

  it("submits on Cmd/Ctrl+Enter", () => {
    const onSubmit = vi.fn();
    render(<Harness initial="{}" onSubmit={onSubmit} />);
    fireEvent.keyDown(textbox(), { key: "Enter", ctrlKey: true });
    fireEvent.keyDown(textbox(), { key: "Enter", metaKey: true });
    expect(onSubmit).toHaveBeenCalledTimes(2);
  });

  it("shows only Format in the minimal toolbar", () => {
    render(<Harness initial="{}" toolbar="minimal" />);
    expect(screen.getByRole("button", { name: "Format" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Minify" })).toBeNull();
    expect(screen.getByText(/1 line · 2 bytes/)).toBeInTheDocument();
  });

  it("shows the key hints in the status slot while focused and valid", () => {
    render(<Harness initial="{}" />);
    expect(screen.getByRole("status")).toHaveTextContent("");
    fireEvent.focus(textbox());
    expect(screen.getByRole("status")).toHaveTextContent(/Tab inserts two spaces/);
    // A problem takes the slot back; the hint returns once the JSON is whole.
    fireEvent.change(textbox(), { target: { value: "{" } });
    expect(screen.getByRole("status")).toHaveTextContent(/Unexpected end of input/);
    fireEvent.blur(textbox());
    fireEvent.change(textbox(), { target: { value: "{}" } });
    expect(screen.getByRole("status")).toHaveTextContent("");
  });

  it("yields its problem line to a Field that is already showing it", () => {
    function Validated() {
      const [value, setValue] = useState('{"a":1,}');
      return (
        <Field label="Value" error={validateParameterValue(value, "json")}>
          <JsonEditor value={value} onChange={setValue} />
        </Field>
      );
    }
    render(<Validated />);
    // One message: the Field's, carrying the editor's line and column.
    expect(screen.getByRole("alert")).toHaveTextContent(
      'Value must be valid JSON (line 1, col 8: Trailing comma before "}").',
    );
    expect(screen.getByRole("status")).toHaveTextContent("");
    // The gutter still marks the line.
    expect(document.querySelector(".json-line.is-error")).not.toBeNull();
  });

  it("drops the highlight layer above the size cap", () => {
    const big = `["${"x".repeat(HIGHLIGHT_MAX_BYTES + 10)}"]`;
    render(<Harness initial={big} />);
    expect(document.querySelector(".json-editor")).toHaveAttribute("data-plain", "true");
    expect(document.querySelector(".json-editor-highlight")).toBeNull();
    expect(screen.getByRole("status")).toHaveTextContent("Highlighting is off above 200.0 KiB");
  });

  it("disables the control and the tools together", () => {
    render(<JsonEditor value="{}" onChange={() => undefined} aria-label="Value" disabled />);
    expect(textbox()).toBeDisabled();
    expect(screen.getByRole("button", { name: "Format" })).toBeDisabled();
    expect(document.querySelector(".json-editor")).toHaveAttribute("data-disabled", "true");
  });
});

describe("JsonView", () => {
  it("colours well-formed JSON without changing the text", () => {
    const raw = '{\n  "a": [1, true, null]\n}';
    render(<JsonView raw={raw} />);
    const block = document.querySelector(".json-block");
    expect(block?.textContent).toBe(raw);
    expect(block?.querySelectorAll(".tok-key")).toHaveLength(1);
    expect(block?.querySelector(".tok-boolean")?.textContent).toBe("true");
    expect(block?.querySelector(".tok-null")?.textContent).toBe("null");
  });

  it("leaves anything else as plain text", () => {
    render(<JsonView raw="not json" />);
    const block = document.querySelector(".json-block");
    expect(block?.textContent).toBe("not json");
    expect(block?.querySelector("[class^=tok-]")).toBeNull();
  });

  it("numbers the lines and offers wrap, copy and a size readout", () => {
    render(<JsonView raw={'{\n  "a": 1\n}'} copyLabel="Copy value" />);
    expect(document.querySelector(".json-highlight")).toHaveAttribute("data-line-numbers", "true");
    expect(screen.getByText("3 lines · 12 bytes")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Copy value" })).toBeInTheDocument();

    const wrap = screen.getByRole("button", { name: "Wrap" });
    expect(wrap).toHaveAttribute("aria-pressed", "true");
    expect(document.querySelector(".json-view")).toHaveAttribute("data-wrap", "on");
    fireEvent.click(wrap);
    expect(document.querySelector(".json-view")).toHaveAttribute("data-wrap", "off");
  });
});

describe("ValueView", () => {
  it("pretty-prints json and copies the stored form", () => {
    const stored = '{"a":1,"b":[2]}';
    render(<ValueView value={stored} contentType="json" />);
    expect(document.querySelector(".json-block")?.textContent).toBe(formatJson(stored));
    expect(document.querySelector(".tok-key")).not.toBeNull();
  });

  it("shows other content types verbatim with line numbers and no colouring", () => {
    render(<ValueView value={"first\nsecond"} contentType="string" />);
    const block = document.querySelector(".json-block");
    expect(block?.textContent).toBe("first\nsecond");
    expect(block?.querySelectorAll(".json-line")).toHaveLength(2);
    expect(block?.querySelector("[class^=tok-]")).toBeNull();
    expect(screen.getByText("2 lines · 12 bytes")).toBeInTheDocument();
  });
});
