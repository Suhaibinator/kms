import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { RefreshControl } from "@/components/RefreshControl";

describe("RefreshControl", () => {
  it("is a plain Refresh button when the page has no freshness to report", () => {
    const onRefresh = vi.fn();
    render(<RefreshControl onRefresh={onRefresh} />);

    expect(screen.queryByRole("status")).toBeNull();
    const button = screen.getByRole("button", { name: "Refresh" });
    fireEvent.click(button);
    expect(onRefresh).toHaveBeenCalledOnce();
  });

  it("is named Refreshing… and disabled while a refresh is in flight", () => {
    const { rerender } = render(<RefreshControl onRefresh={() => {}} />);
    expect(screen.getByRole("button", { name: "Refresh" })).toBeEnabled();

    rerender(<RefreshControl onRefresh={() => {}} loading />);
    const button = screen.getByRole("button", { name: "Refreshing…" });
    expect(button).toBeDisabled();
    expect(button).toHaveAttribute("aria-busy", "true");
  });

  it("keeps the ghost label out of the accessible name so the box cannot resize", () => {
    render(<RefreshControl onRefresh={() => {}} />);
    // Both strings are in the DOM — the hidden one only holds the width — but
    // only the visible one names the button.
    expect(screen.getByRole("button", { name: "Refresh" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Refreshing…" })).toBeNull();
  });

  it("fuses the freshness badge to an icon-only button when freshness is given", () => {
    const onRefresh = vi.fn();
    const { container } = render(
      <RefreshControl
        onRefresh={onRefresh}
        freshness={{ transport: "poll", stale: false, lastUpdatedAt: null }}
      />,
    );

    const control = container.querySelector(".refresh-control");
    expect(control).not.toBeNull();
    expect(control).toHaveAttribute("data-size", "default");

    // The badge keeps its own live region; the pages' tests query it by role.
    const badge = screen.getByRole("status");
    expect(badge).toHaveTextContent(/^Polling$/);
    expect(badge.parentElement).toBe(control);

    const button = screen.getByRole("button", { name: "Refresh" });
    expect(button).toHaveAttribute("title", "Refresh");
    // Icon-only: no visible label to read out, so the name is the aria-label.
    expect(button).toHaveTextContent("");
    fireEvent.click(button);
    expect(onRefresh).toHaveBeenCalledOnce();
  });

  it("renames the icon-only button while loading and stays disabled when told to", () => {
    const { rerender } = render(
      <RefreshControl
        onRefresh={() => {}}
        loading
        freshness={{ transport: "stream", stale: false, lastUpdatedAt: null }}
      />,
    );
    expect(screen.getByRole("button", { name: "Refreshing…" })).toBeDisabled();

    rerender(
      <RefreshControl
        onRefresh={() => {}}
        disabled
        freshness={{ transport: "stream", stale: false, lastUpdatedAt: null }}
      />,
    );
    expect(screen.getByRole("button", { name: "Refresh" })).toBeDisabled();
  });

  it("carries the size down to both segments", () => {
    const { container } = render(
      <RefreshControl
        size="sm"
        onRefresh={() => {}}
        freshness={{ transport: "off", stale: true, lastUpdatedAt: null }}
      />,
    );
    expect(container.querySelector(".refresh-control")).toHaveAttribute("data-size", "sm");
    expect(screen.getByRole("button", { name: "Refresh" })).toHaveAttribute("data-size", "icon-sm");
  });
});
