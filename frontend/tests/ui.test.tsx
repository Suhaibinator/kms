import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { Field } from "@/components/ui";

afterEach(cleanup);

describe("Field", () => {
  it("associates a generated id and hint with a direct form control", () => {
    render(
      <Field label="Release name" hint="Used by subscribed clients.">
        <input />
      </Field>,
    );

    const input = screen.getByRole("textbox", { name: "Release name" });
    expect(input).toHaveAccessibleDescription("Used by subscribed clients.");
    expect(input).toHaveAttribute("id");
  });

  it("preserves an explicit control id", () => {
    render(
      <Field label="Environment">
        <select id="environment">
          <option>Production</option>
        </select>
      </Field>,
    );

    expect(screen.getByRole("combobox", { name: "Environment" })).toHaveAttribute(
      "id",
      "environment",
    );
  });

  it("marks a required control without reading the asterisk aloud", () => {
    render(
      <Field label="Release name" required>
        <input />
      </Field>,
    );

    const input = screen.getByRole("textbox", { name: "Release name" });
    expect(input).toHaveAttribute("aria-required", "true");
    expect(screen.getByText("*", { exact: false, selector: "span" })).toHaveAttribute(
      "aria-hidden",
      "true",
    );
  });

  it("accepts a composed label", () => {
    render(
      <Field
        label={
          <>
            Type <span className="mono">prod</span> to confirm
          </>
        }
      >
        <input />
      </Field>,
    );

    expect(screen.getByRole("textbox", { name: "Type prod to confirm" })).toBeInTheDocument();
  });

  it("labels composite fields as accessible groups", () => {
    render(
      <Field label="Authentication methods" hint="Choose at least one method.">
        <div>
          <label>
            <input type="checkbox" /> Token
          </label>
        </div>
      </Field>,
    );

    expect(
      screen.getByRole("group", { name: "Authentication methods" }),
    ).toHaveAccessibleDescription("Choose at least one method.");
  });
});
