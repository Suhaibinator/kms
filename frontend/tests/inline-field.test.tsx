import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { ContextBar } from "@/components/ContextBar";
import { InlineField } from "@/components/InlineField";
import { AppSelect } from "@/components/ui/app-select";

const OPTIONS = [
  { value: "dev", label: "dev" },
  { value: "prod", label: "prod · production" },
];

describe("InlineField", () => {
  it("names the control with the caption beside it", () => {
    render(
      <InlineField label="Environment" htmlFor="env">
        <AppSelect id="env" value="dev" onValueChange={() => {}} options={OPTIONS} />
      </InlineField>,
    );
    expect(screen.getByRole("combobox", { name: "Environment" })).toBeInTheDocument();
  });

  it("gives a control with no id of its own one, so the caption still reaches it", () => {
    render(
      <InlineField label="Lifecycle">
        <AppSelect value="dev" onValueChange={() => {}} options={OPTIONS} />
      </InlineField>,
    );
    const control = screen.getByRole("combobox", { name: "Lifecycle" });
    expect(control.id).not.toBe("");
  });

  it("keeps the control's own aria-label when it has one", () => {
    render(
      <InlineField label="Schema" htmlFor="track">
        <AppSelect
          id="track"
          aria-label="Schema version"
          value="dev"
          onValueChange={() => {}}
          options={OPTIONS}
        />
      </InlineField>,
    );
    expect(screen.getByRole("combobox", { name: "Schema version" })).toBeInTheDocument();
  });

  it("labels the control beside it, not by wrapping it", () => {
    // A wrapping label would also label Base UI's hidden input, so
    // getByLabelText would find two elements.
    render(
      <InlineField label="Lifecycle">
        <AppSelect value="dev" onValueChange={() => {}} options={OPTIONS} />
      </InlineField>,
    );
    expect(screen.getByLabelText("Lifecycle")).toHaveAttribute("role", "combobox");
  });

  it("renders no dangling label for a placeholder row", () => {
    const { container } = render(
      <InlineField as="span" label="Schema">
        <span data-testid="placeholder" />
      </InlineField>,
    );
    const field = container.querySelector(".inline-field");
    expect(field?.tagName).toBe("SPAN");
    expect(container.querySelector("label")).toBeNull();
    expect(screen.getByText("Schema")).toHaveClass("inline-field-label");
  });
});

describe("ContextBar", () => {
  it("wraps its children in the context row and keeps caller classes", () => {
    const { container } = render(
      <ContextBar className="extra">
        <span>child</span>
      </ContextBar>,
    );
    const bar = container.querySelector(".context-bar");
    expect(bar).toHaveClass("extra");
    expect(bar).toHaveTextContent("child");
  });
});
