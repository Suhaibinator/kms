import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { useState } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { SensitiveValueField } from "@/components/SensitiveValueField";
import { Field } from "@/components/ui";

vi.mock("@/context/ToastContext", () => ({ useToast: () => ({ error: vi.fn() }) }));
afterEach(() => vi.restoreAllMocks());

function Input({ multiline = false, disabled = false }) {
  const [value, setValue] = useState("initial credential");
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

  it("disables the input and all shared actions", () => {
    render(<Input disabled />);
    expect(screen.getByLabelText(/^Credential/)).toBeDisabled();
    for (const button of screen.getAllByRole("button")) expect(button).toBeDisabled();
  });
});
