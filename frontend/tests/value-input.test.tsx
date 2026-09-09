import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { useState } from "react";
import { describe, expect, it, vi } from "vitest";
import { ContentTypeSelect, ParameterValueInput } from "@/components/ParameterValueInput";
import { Field } from "@/components/ui";
import { chooseSelectOption } from "./select-test-utils";

vi.mock("@/context/ToastContext", () => ({
  useToast: () => ({ error: vi.fn(), success: vi.fn() }),
}));

function Harness({
  contentType,
  initial = "",
  storedValue,
  onChange,
  onValidityChange,
}: {
  contentType: string;
  initial?: string;
  storedValue?: string;
  onChange?: (value: string) => void;
  onValidityChange?: (valid: boolean) => void;
}) {
  const [value, setValue] = useState(initial);
  return (
    <>
      <Field label="Value">
        <ParameterValueInput
          contentType={contentType}
          value={value}
          storedValue={storedValue}
          onValidityChange={onValidityChange}
          onChange={(next) => {
            setValue(next);
            onChange?.(next);
          }}
        />
      </Field>
      <output data-testid="out">{value}</output>
    </>
  );
}

describe("ParameterValueInput scalars", () => {
  it("opens a one-line string in a single-line input that can be widened", () => {
    render(<Harness contentType="string" initial="strict" />);
    const control = screen.getByRole("textbox", { name: "Value" });
    expect(control.tagName).toBe("INPUT");
    expect(control).toHaveAttribute("placeholder", "value");

    fireEvent.click(screen.getByRole("button", { name: "Edit on several lines" }));
    const wide = screen.getByRole("textbox", { name: "Value" });
    expect(wide.tagName).toBe("TEXTAREA");
    expect(wide).toHaveValue("strict");

    fireEvent.click(screen.getByRole("button", { name: "Edit on one line" }));
    expect(screen.getByRole("textbox", { name: "Value" }).tagName).toBe("INPUT");
  });

  it("keeps a string with line breaks in a textarea", () => {
    render(<Harness contentType="string" initial={"line one\nline two"} />);
    expect(screen.getByRole("textbox", { name: "Value" }).tagName).toBe("TEXTAREA");
    // There is no one-line form of a value that already holds a line break.
    expect(screen.getByRole("button", { name: "Edit on one line" })).toBeDisabled();
  });

  it("gives numbers a placeholder that matches the type", () => {
    const { unmount } = render(<Harness contentType="integer" />);
    expect(screen.getByRole("textbox", { name: "Value" })).toHaveAttribute("placeholder", "100");
    unmount();
    render(<Harness contentType="float" />);
    expect(screen.getByRole("textbox", { name: "Value" })).toHaveAttribute("placeholder", "1.5");
  });

  it("renders a boolean as a two-way switch that writes true or false", () => {
    const onChange = vi.fn();
    render(<Harness contentType="boolean" initial="true" onChange={onChange} />);
    expect(screen.getByRole("radiogroup", { name: "Value" })).toBeInTheDocument();
    expect(screen.getByRole("radio", { name: "true" })).toBeChecked();
    expect(screen.getByRole("radio", { name: "false" })).not.toBeChecked();

    fireEvent.click(screen.getByRole("radio", { name: "false" }));
    expect(onChange).toHaveBeenLastCalledWith("false");
    expect(screen.getByRole("radio", { name: "false" })).toBeChecked();
  });

  it("shows a stored 1/0/t/f literal beside the switch instead of a blank control", () => {
    render(<Harness contentType="boolean" initial="1" />);
    // Neither side claims the literal; the note says what it means.
    expect(screen.getByRole("radio", { name: "true" })).not.toBeChecked();
    expect(screen.getByRole("radio", { name: "false" })).not.toBeChecked();
    expect(screen.getByTestId("value-literal")).toHaveTextContent("Stored as 1 (true)");

    fireEvent.click(screen.getByRole("radio", { name: "true" }));
    expect(screen.getByTestId("out")).toHaveTextContent("true");
    expect(screen.getByRole("radio", { name: "true" })).toBeChecked();
    expect(screen.queryByTestId("value-literal")).toBeNull();
  });

  it("reports the decoded size of a binary value and uploads a file as base64", async () => {
    render(<Harness contentType="binary" initial="SGVsbG8sIHdvcmxkIQ==" />);
    expect(screen.getByRole("textbox", { name: "Value" }).tagName).toBe("TEXTAREA");
    expect(screen.getByTestId("value-binary-size")).toHaveTextContent("Decodes to 13 bytes.");

    fireEvent.change(screen.getByRole("textbox", { name: "Value" }), {
      target: { value: "not base64!" },
    });
    expect(screen.getByTestId("value-binary-size")).toHaveTextContent("Not valid base64 yet.");

    const picker = document.querySelector<HTMLInputElement>('input[type="file"]');
    if (!picker) throw new Error("file input not rendered");
    fireEvent.change(picker, { target: { files: [new File(["hi"], "blob.bin")] } });
    await waitFor(() => expect(screen.getByTestId("out")).toHaveTextContent("aGk="));
    expect(screen.getByTestId("value-binary-size")).toHaveTextContent("Decodes to 2 bytes.");
  });
});

describe("an empty string as an explicit choice", () => {
  const emptyBox = () => screen.getByRole("checkbox", { name: /Empty string/ });

  it("treats a blank string box as no value yet", () => {
    const validity = vi.fn();
    render(<Harness contentType="string" onValidityChange={validity} />);
    expect(validity).toHaveBeenLastCalledWith(false);
    expect(emptyBox()).not.toBeChecked();
    expect(screen.getByTestId("value-empty-hint")).toHaveTextContent(
      "Type a value, or tick Empty string.",
    );
    expect(screen.queryByText("(empty)")).toBeNull();
  });

  it("saves an empty string once the box is ticked", () => {
    const validity = vi.fn();
    const onChange = vi.fn();
    render(<Harness contentType="string" onValidityChange={validity} onChange={onChange} />);

    fireEvent.click(emptyBox());
    expect(onChange).toHaveBeenLastCalledWith("");
    expect(validity).toHaveBeenLastCalledWith(true);
    expect(emptyBox()).toBeChecked();
    // The stored value is settled, so the box is no longer an open question.
    expect(screen.getByRole("textbox", { name: "Value" })).toBeDisabled();
    expect(screen.getByText("(empty)")).toBeVisible();
    expect(screen.getByTestId("value-empty-hint")).toHaveTextContent("Saved as an empty string.");
  });

  it("unticks itself as soon as the box holds a value again", () => {
    const validity = vi.fn();
    render(<Harness contentType="string" onValidityChange={validity} />);
    fireEvent.click(emptyBox());
    fireEvent.click(emptyBox());
    expect(emptyBox()).not.toBeChecked();
    expect(validity).toHaveBeenLastCalledWith(false);

    const control = screen.getByRole("textbox", { name: "Value" });
    expect(control).toBeEnabled();
    fireEvent.change(control, { target: { value: "strict" } });
    expect(emptyBox()).not.toBeChecked();
    expect(validity).toHaveBeenLastCalledWith(true);
    // A value in the box answers the question; no hint is needed.
    expect(screen.queryByTestId("value-empty-hint")).toBeNull();

    // Clearing by hand is not a choice: blank means "no value yet" again.
    fireEvent.change(control, { target: { value: "" } });
    expect(emptyBox()).not.toBeChecked();
    expect(validity).toHaveBeenLastCalledWith(false);
  });

  it("opens ticked when the editor was prefilled with an empty value", () => {
    const validity = vi.fn();
    render(<Harness contentType="string" storedValue="" onValidityChange={validity} />);
    expect(emptyBox()).toBeChecked();
    expect(validity).toHaveBeenLastCalledWith(true);
    expect(screen.getByText("(empty)")).toBeVisible();
  });

  it("leaves a prefilled value alone", () => {
    const validity = vi.fn();
    render(
      <Harness
        contentType="string"
        initial="strict"
        storedValue="strict"
        onValidityChange={validity}
      />,
    );
    expect(emptyBox()).not.toBeChecked();
    expect(validity).toHaveBeenLastCalledWith(true);
    expect(screen.getByRole("textbox", { name: "Value" })).toBeEnabled();
  });

  it("keeps the tick out of every other content type", () => {
    for (const contentType of ["integer", "float", "boolean", "json", "binary"]) {
      const { unmount } = render(<Harness contentType={contentType} />);
      expect(screen.queryByRole("checkbox", { name: /Empty string/ })).toBeNull();
      expect(screen.queryByTestId("value-empty-hint")).toBeNull();
      unmount();
    }
  });

  it("reports the other content types' blank validity as before", () => {
    for (const [contentType, blankIsValid] of [
      ["integer", false],
      ["float", false],
      ["boolean", false],
      ["binary", true],
    ] as const) {
      const validity = vi.fn();
      const { unmount } = render(<Harness contentType={contentType} onValidityChange={validity} />);
      expect(validity).toHaveBeenLastCalledWith(blankIsValid);
      unmount();
    }
  });
});

describe("ContentTypeSelect", () => {
  function Probe({ initialValue }: { initialValue: string }) {
    const [contentType, setContentType] = useState("string");
    const [value, setValue] = useState(initialValue);
    return (
      <>
        <Field label="Content type">
          <ContentTypeSelect
            value={contentType}
            currentValue={value}
            onValueChange={setContentType}
            onClearValue={() => setValue("")}
          />
        </Field>
        <output data-testid="value">{value}</output>
      </>
    );
  }

  it("offers to clear a value the newly chosen type rejects", async () => {
    render(<Probe initialValue="abc" />);
    await chooseSelectOption(screen.getByRole("combobox", { name: "Content type" }), "integer");
    expect(screen.getByText(/The current value is not valid/)).toHaveTextContent("integer");
    fireEvent.click(screen.getByRole("button", { name: "Clear value" }));
    expect(screen.getByTestId("value")).toHaveTextContent("");
    expect(screen.queryByText(/The current value is not valid/)).toBeNull();
  });

  it("stays quiet when the value fits the new type", async () => {
    render(<Probe initialValue="12" />);
    await chooseSelectOption(screen.getByRole("combobox", { name: "Content type" }), "integer");
    expect(screen.queryByText(/The current value is not valid/)).toBeNull();
  });
});

it("forwards form draft validity through ParameterValueInput with inline parent callbacks", () => {
  function ValidityHarness() {
    const [value, setValue] = useState('{"count":3}');
    const [validity, setValidity] = useState({ valid: true });
    return (
      <>
        <ParameterValueInput
          contentType="json"
          value={value}
          onChange={setValue}
          schema={{ type: "object", properties: { count: { type: "number" } } }}
          preferForm
          onValidityChange={(valid) => setValidity({ valid })}
        />
        <button disabled={!validity.valid}>Save value</button>
      </>
    );
  }
  render(<ValidityHarness />);
  fireEvent.change(screen.getByLabelText("count"), { target: { value: "4e" } });
  expect(screen.getByRole("button", { name: "Save value" })).toBeDisabled();
  fireEvent.change(screen.getByLabelText("count"), { target: { value: "4" } });
  expect(screen.getByRole("button", { name: "Save value" })).toBeEnabled();
});
