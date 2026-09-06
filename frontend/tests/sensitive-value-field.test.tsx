import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { useState } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { SensitiveValueField } from "@/components/SensitiveValueField";
import { Field } from "@/components/ui";

vi.mock("@/context/ToastContext", () => ({ useToast: () => ({ error: vi.fn() }) }));
afterEach(() => vi.restoreAllMocks());

function Input({ multiline = false, disabled = false, initial = "initial credential" }) {
  const [value, setValue] = useState(initial);
  return (
    <Field label="Credential" hint="Save this key" required>
      <SensitiveValueField
        value={value}
        onChange={setValue}
        multiline={multiline}
        disabled={disabled}
        controlLabel="credential"
      />
    </Field>
  );
}

describe("SensitiveValueField", () => {
  it.each([false, true])(
    "masks, reveals, and copies an accessible input (multiline: %s)",
    async (multiline) => {
      const copy = vi.spyOn(navigator.clipboard, "writeText").mockResolvedValue();
      render(<Input multiline={multiline} />);
      const input = screen.getByLabelText(/^Credential/);
      expect(input).toHaveAttribute("data-masked", "true");
      expect(input).toHaveAttribute("aria-required", "true");
      expect(input).toHaveAccessibleDescription("Save this key");
      if (!multiline) expect(input).toHaveAttribute("type", "password");
      fireEvent.click(screen.getByRole("button", { name: "Show credential" }));
      expect(input).toHaveAttribute("data-masked", "false");
      if (!multiline) expect(input).toHaveAttribute("type", "text");
      fireEvent.click(screen.getByRole("button", { name: "Copy credential" }));
      await waitFor(() => expect(copy).toHaveBeenCalledWith("initial credential"));
      fireEvent.click(screen.getByRole("button", { name: "Hide credential" }));
      expect(input).toHaveAttribute("data-masked", "true");
    },
  );

  it.each([
    [32, "base64url", /^[A-Za-z0-9_-]{43}$/],
    [64, "base64url", /^[A-Za-z0-9_-]{86}$/],
    [32, "hex", /^[0-9a-f]{64}$/],
    [64, "hex", /^[0-9a-f]{128}$/],
  ] as const)(
    "generates and reveals %s bytes as %s using Web Crypto",
    async (bytes, encoding, pattern) => {
      const random = vi.spyOn(crypto, "getRandomValues");
      render(<Input />);
      fireEvent.click(screen.getByRole("button", { name: "Generate credential" }));
      fireEvent.click(await screen.findByRole("menuitem", { name: `${bytes} bytes, ${encoding}` }));
      const input = screen.getByLabelText(/^Credential/) as HTMLInputElement;
      expect(input.value).toMatch(pattern);
      expect(random).toHaveBeenCalledWith(expect.any(Uint8Array));
      expect(random.mock.calls.at(-1)?.[0]?.byteLength).toBe(bytes);
      expect(input).toHaveAttribute("type", "text");
    },
  );

  it("keeps Copy mounted and disabled while the value is empty", () => {
    render(<Input initial="" />);
    // Mounting it on the first keystroke pushed the controls after it sideways
    // and could add a line to the field mid-edit.
    const copy = screen.getByRole("button", { name: "Copy credential" });
    expect(copy).toBeDisabled();
    fireEvent.change(screen.getByLabelText(/^Credential/), { target: { value: "s3cret" } });
    expect(screen.getByRole("button", { name: "Copy credential" })).toBeEnabled();
  });

  it("disables the input and all shared actions", () => {
    render(<Input disabled />);
    expect(screen.getByLabelText(/^Credential/)).toBeDisabled();
    for (const button of screen.getAllByRole("button")) expect(button).toBeDisabled();
  });
});
