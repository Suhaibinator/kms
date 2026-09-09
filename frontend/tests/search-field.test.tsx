import { readFileSync } from "node:fs";
import { resolve } from "node:path";
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

  // The icon and the `/` hint are absolutely positioned inside the box, so the
  // text has to start clear of them. The insets have to be utilities: the Input
  // primitive's own px-3 is one, and it beat the component-layer rule this
  // replaces, leaving 12px of padding under a 15px icon.
  it("insets the text past the icon with utilities the primitive's px-3 cannot outrank", () => {
    const onChange = vi.fn();
    const onClear = vi.fn();
    render(<SearchField label="Search" value="" onChange={onChange} onClear={onClear} />);
    const classes = screen.getByLabelText("Search").className.split(/\s+/);
    expect(classes).toContain("pl-[calc(var(--space-2)*2_+_15px)]");
    expect(classes).toContain("pr-[calc(var(--space-2)*2_+_12px)]");

    const css = readFileSync(resolve(process.cwd(), "styles", "globals.css"), "utf8");
    const rules = [...css.matchAll(/([^{}]+?)\{([^{}]*)\}/g)].filter((match) =>
      (match[1] ?? "").split(",").some((part) => part.trim() === ".search-field-input"),
    );
    expect(rules.map((match) => match[2] ?? "").join("\n")).not.toMatch(/padding/);
  });

  it("calls onChange as the operator types", () => {
    const onChange = vi.fn();
    const onClear = vi.fn();
    render(<SearchField label="Search" value="" onChange={onChange} onClear={onClear} />);
    fireEvent.change(screen.getByLabelText("Search"), { target: { value: "billing" } });
    expect(onChange).toHaveBeenCalledWith("billing");
  });
});
