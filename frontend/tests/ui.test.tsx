import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { Field } from "@/components/ui";

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
