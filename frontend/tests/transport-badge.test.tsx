import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { TransportBadge } from "@/components/TransportBadge";
import { formatUnixMs } from "@/lib/format";

describe("TransportBadge", () => {
  it("names the transport and shows how old the data is, with the absolute time in the title", () => {
    const at = Date.now() - 2 * 60_000;
    render(<TransportBadge transport="manual" stale={false} lastUpdatedAt={at} />);
    const badge = screen.getByRole("status");
    expect(badge).toHaveTextContent(/^Loaded· 2m ago$/);
    expect(badge).toHaveClass("transport-manual");
    expect(badge.getAttribute("title")).toContain(formatUnixMs(at));
    expect(badge.getAttribute("title")).toContain("Refresh");
  });

  it("reads Stale with the caller's explanation when behind", () => {
    render(
      <TransportBadge
        transport="poll"
        stale
        lastUpdatedAt={Date.now()}
        staleTitle="A release was activated since this loaded."
      />,
    );
    const badge = screen.getByRole("status");
    expect(badge).toHaveTextContent(/^Stale· just now$/);
    expect(badge).toHaveClass("is-stale");
    expect(badge.getAttribute("title")).toContain("A release was activated since this loaded.");
  });

  it("omits the time before the first load", () => {
    render(<TransportBadge transport="poll" stale={false} lastUpdatedAt={null} title="Custom." />);
    const badge = screen.getByRole("status");
    expect(badge).toHaveTextContent(/^Polling$/);
    expect(badge).toHaveAttribute("title", "Custom.");
  });
});
