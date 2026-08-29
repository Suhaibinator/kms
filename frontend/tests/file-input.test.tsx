import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { Field } from "@/components/ui";
import { FileInput } from "@/components/ui/file-input";

const file = (name: string) => new File(["{}"], name, { type: "application/json" });

describe("FileInput", () => {
  it("is labelled through Field and reports the chosen file's name", () => {
    const onFile = vi.fn();
    render(
      <Field label="Defaults artifact" hint="JSON only">
        <FileInput accept=".json" onFile={onFile} />
      </Field>,
    );
    const input = screen.getByLabelText("Defaults artifact");
    expect(input).toHaveAttribute("type", "file");
    expect(input).toHaveAttribute("accept", ".json");
    expect(screen.getByText("No file chosen — or drop one here")).toBeVisible();

    fireEvent.change(input, { target: { files: [file("defaults.json")] } });
    expect(onFile).toHaveBeenCalledWith(expect.objectContaining({ name: "defaults.json" }));
    expect(screen.getByText("defaults.json")).toBeVisible();
    expect(input).toHaveAccessibleDescription(/JSON only/);
  });

  it("opens the native picker from the button and accepts a dropped file", () => {
    const onFile = vi.fn();
    render(<FileInput aria-label="Schema file" onFile={onFile} buttonLabel="Load file…" />);
    const input = screen.getByLabelText("Schema file") as HTMLInputElement;
    const click = vi.spyOn(input, "click");
    fireEvent.click(screen.getByRole("button", { name: "Load file…" }));
    expect(click).toHaveBeenCalledOnce();

    const zone = input.parentElement as HTMLElement;
    fireEvent.dragOver(zone);
    expect(zone).toHaveAttribute("data-dragging", "true");
    fireEvent.drop(zone, { dataTransfer: { files: [file("schema.json")] } });
    expect(zone).not.toHaveAttribute("data-dragging");
    expect(onFile).toHaveBeenCalledWith(expect.objectContaining({ name: "schema.json" }));
  });

  it("shows the controlled file name and ignores drops while disabled", () => {
    const onFile = vi.fn();
    const { rerender } = render(
      <FileInput aria-label="Artifact" fileName="a.json" onFile={onFile} />,
    );
    expect(screen.getByText("a.json")).toBeVisible();
    rerender(<FileInput aria-label="Artifact" fileName="" onFile={onFile} disabled />);
    expect(screen.queryByText("a.json")).toBeNull();
    expect(screen.getByLabelText("Artifact")).toBeDisabled();
    expect(screen.getByRole("button", { name: "Choose file…" })).toBeDisabled();
    const zone = screen.getByLabelText("Artifact").parentElement as HTMLElement;
    fireEvent.drop(zone, { dataTransfer: { files: [file("b.json")] } });
    expect(onFile).not.toHaveBeenCalled();
  });
});
