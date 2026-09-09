import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { SearchField } from "@/components/SearchField";

describe("SearchField", () => {
  it("focuses the input when '/' is pressed anywhere on the page", () => {
    const onChange = vi.fn();
    const onClear = vi.fn();
    render(<SearchField label="Search" value="" onChange={onChange} onClear={onClear} />);
    const input = screen.getByLabelText("Search");
    expect(input).not.toHaveFocus();

    // fireEvent returns false when the handler called preventDefault.
    expect(fireEvent.keyDown(document.body, { key: "/" })).toBe(false);
    expect(input).toHaveFocus();
  });

  it("does not steal focus when '/' is typed into another text field", () => {
    const onChange = vi.fn();
    const onClear = vi.fn();
    render(
      <>
        <input aria-label="Other field" />
        <SearchField label="Search" value="" onChange={onChange} onClear={onClear} />
      </>,
    );
    const other = screen.getByLabelText("Other field");
    other.focus();
    expect(other).toHaveFocus();

    // Not prevented: the "/" reaches the field as typed, same as any other key.
    expect(fireEvent.keyDown(other, { key: "/" })).toBe(true);
    expect(other).toHaveFocus();
    expect(screen.getByLabelText("Search")).not.toHaveFocus();
  });

  it("clears on Escape while focused with a non-empty value", () => {
    const onChange = vi.fn();
    const onClear = vi.fn();
    render(<SearchField label="Search" value="billing" onChange={onChange} onClear={onClear} />);
    const input = screen.getByLabelText("Search");
    input.focus();

    fireEvent.keyDown(input, { key: "Escape" });
    expect(onClear).toHaveBeenCalledTimes(1);
  });

  it("does not clear on Escape when the value is already empty", () => {
    const onChange = vi.fn();
    const onClear = vi.fn();
    render(<SearchField label="Search" value="" onChange={onChange} onClear={onClear} />);
    const input = screen.getByLabelText("Search");
    input.focus();

    fireEvent.keyDown(input, { key: "Escape" });
    expect(onClear).not.toHaveBeenCalled();
  });

  it("calls onChange as the operator types", () => {
    const onChange = vi.fn();
    const onClear = vi.fn();
    render(<SearchField label="Search" value="" onChange={onChange} onClear={onClear} />);
    fireEvent.change(screen.getByLabelText("Search"), { target: { value: "billing" } });
    expect(onChange).toHaveBeenCalledWith("billing");
  });
});
