import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { StatusChip } from "@/components/StatusChip";

describe("StatusChip", () => {
  it("renders a badge with the status label and classes", () => {
    render(<StatusChip status="degraded" />);
    const chip = screen.getByText("Degraded");
    expect(chip).toHaveClass("status-chip");
    expect(chip).toHaveClass("status-degraded");
    expect(chip).not.toHaveClass("status-prod");
    expect(chip).toHaveAttribute("data-slot", "badge");
  });

  it("marks production and names it for assistive tech", () => {
    render(<StatusChip status="ready" production />);
    const chip = screen.getByText("Ready");
    expect(chip).toHaveClass("status-prod");
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
