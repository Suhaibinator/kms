import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { useState } from "react";
import { describe, expect, it, vi } from "vitest";
import { Field } from "@/components/ui";
import { AppSelect } from "@/components/ui/app-select";

const resources = [
  { value: "app_agents", label: "app_agents" },
  { value: "anthropic_api_key", label: "anthropic_api_key" },
  { value: "app_billing", label: "app_billing" },
];

function SearchableFixture({ onChange = vi.fn() }: { onChange?: (value: string) => void }) {
  const [value, setValue] = useState("");
  return (
    <Field label="Resource">
      <AppSelect
        value={value}
        onValueChange={(next) => {
          setValue(next);
          onChange(next);
        }}
        options={resources}
        searchable
        searchPlaceholder="Filter resources…"
        emptyMessage="No matching resources."
      />
    </Field>
  );
}

describe("AppSelect", () => {
  it("filters a searchable select locally and preserves the selected value", async () => {
    const onChange = vi.fn();
    render(<SearchableFixture onChange={onChange} />);

    fireEvent.click(screen.getByRole("combobox", { name: "Resource" }));
    const filter = await screen.findByRole("combobox", { name: "Filter resources…" });
    expect(screen.getAllByRole("option")).toHaveLength(3);

    fireEvent.change(filter, { target: { value: "API" } });
    await waitFor(() => expect(screen.getAllByRole("option")).toHaveLength(1));
    expect(screen.getByRole("option", { name: "anthropic_api_key" })).toBeVisible();
    expect(screen.queryByRole("option", { name: "app_agents" })).toBeNull();

    fireEvent.click(screen.getByRole("option", { name: "anthropic_api_key" }));
    expect(onChange).toHaveBeenCalledWith("anthropic_api_key");
    expect(screen.getByRole("combobox", { name: "Resource" })).toHaveTextContent(
      "anthropic_api_key",
    );

    fireEvent.click(screen.getByRole("combobox", { name: "Resource" }));
    expect(await screen.findByRole("combobox", { name: "Filter resources…" })).toHaveValue("");
    expect(screen.getAllByRole("option")).toHaveLength(3);
  });

  it("shows a useful empty state and supports keyboard selection", async () => {
    const onChange = vi.fn();
    render(<SearchableFixture onChange={onChange} />);

    fireEvent.click(screen.getByRole("combobox", { name: "Resource" }));
    let filter = await screen.findByRole("combobox", { name: "Filter resources…" });
    fireEvent.change(filter, { target: { value: "missing" } });
    expect(await screen.findByText("No matching resources.")).toBeVisible();
    expect(screen.queryByRole("option")).toBeNull();

    fireEvent.change(filter, { target: { value: "billing" } });
    await screen.findByRole("option", { name: "app_billing" });
    filter = screen.getByRole("combobox", { name: "Filter resources…" });
    fireEvent.keyDown(filter, { key: "ArrowDown" });
    fireEvent.keyDown(filter, { key: "Enter" });
    await waitFor(() => expect(onChange).toHaveBeenCalledWith("app_billing"));
  });

  it("defensively disables a select with no options", () => {
    render(
      <Field label="Empty">
        <AppSelect value="" onValueChange={vi.fn()} options={[]} placeholder="Nothing available" />
      </Field>,
    );

    const trigger = screen.getByRole("combobox", { name: "Empty" });
    expect(trigger).toBeDisabled();
    expect(trigger).toHaveTextContent("Nothing available");
    fireEvent.click(trigger);
    expect(screen.queryByRole("listbox")).toBeNull();
  });
});
