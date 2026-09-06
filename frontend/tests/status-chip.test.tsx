import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { StatusChip } from "@/components/StatusChip";

describe("StatusChip", () => {
  it("renders a badge with the status label and classes", () => {
    render(<StatusChip status="degraded" />);
    const chip = screen.getByText("Degraded");
    expect(chip).toHaveClass("status-chip");
    expect(chip).toHaveClass("status-degraded");
    expect(chip).not.toHaveClass("border-(--warning)");
    expect(chip).toHaveAttribute("data-slot", "badge");
  });

  // The marker is a utility, not a class the stylesheet styles: Badge's own
  // recipe carries `border-transparent`, and @layer components can never beat a
  // utility, so the `.status-chip.status-prod` rule this replaces never painted.
  it("marks production and names it for assistive tech", () => {
    render(<StatusChip status="ready" production />);
    const chip = screen.getByText("Ready");
    expect(chip).toHaveClass("border-(--warning)");
    expect(chip).not.toHaveClass("border-transparent");
    expect(chip).toHaveAttribute("title", "Ready (production)");
  });

  it("renders the dot form with an accessible name", () => {
    render(<StatusChip status="attention" production size="dot" />);
    const dot = screen.getByRole("img", { name: "Needs attention (production)" });
    expect(dot).toHaveClass("status-dot");
    expect(dot).toHaveClass("status-attention");
    expect(dot).toHaveClass("status-prod");
    expect(dot).toHaveTextContent("");
  });
});
