import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { Ident, NamespaceIdent, ReleaseIdent } from "@/components/Ident";

describe("Ident", () => {
  it("renders the kind prefix and mono value with the typed classes", () => {
    const { container } = render(<Ident kind="env" value="staging" tooltip={false} />);
    const chip = container.querySelector(".ident");
    expect(chip).toHaveClass("ident-env");
    expect(chip).not.toHaveClass("ident-prod");
    expect(chip).toHaveAttribute("data-kind", "env");
    expect(chip?.querySelector(".ident-kind")).toHaveTextContent("env");
    expect(chip?.querySelector(".ident-kind")).toHaveAttribute("aria-hidden", "true");
    expect(chip?.querySelector(".ident-value")).toHaveTextContent("staging");
  });

  it("marks production environments from the name, or explicitly", () => {
    const { container } = render(
      <>
        <Ident kind="env" value="prod-eu" tooltip={false} />
        <Ident kind="env" value="reproduction" tooltip={false} />
        <Ident kind="alias" value="rate_limits" production tooltip={false} />
      </>,
    );
    const chips = container.querySelectorAll(".ident");
    expect(chips[0]).toHaveClass("ident-prod");
    expect(chips[1]).not.toHaveClass("ident-prod");
    expect(chips[2]).toHaveClass("ident-prod");
  });

  it("formats versions and revisions", () => {
    const { container } = render(
      <>
        <Ident kind="version" value="8" tooltip={false} />
        <Ident kind="version" value="v9" tooltip={false} />
        <Ident kind="revision" value="41" tooltip={false} />
      </>,
    );
    const chips = container.querySelectorAll(".ident");
    expect(chips[0]).toHaveTextContent(/^v8$/);
    expect(chips[0]?.querySelector(".ident-kind")).toBeNull();
    expect(chips[1]).toHaveTextContent(/^v9$/);
    expect(chips[2]?.querySelector(".ident-kind")).toHaveTextContent("rev");
    expect(chips[2]?.querySelector(".ident-value")).toHaveTextContent("41");
  });

  it("links when given an href", () => {
    render(
      <Ident kind="app" value="gradethis" href="/applications?app=gradethis" tooltip={false} />,
    );
    const link = screen.getByRole("link", { name: /gradethis/ });
    expect(link).toHaveAttribute("href", "/applications?app=gradethis");
    expect(link).toHaveClass("ident-link");
    expect(link.querySelector(".ident-app")).not.toBeNull();
  });

  it("wraps in a tooltip trigger by default and not when tooltip is false", () => {
    const { container, rerender } = render(<Ident kind="key" value="database" />);
    expect(container.querySelector('[data-slot="tooltip-trigger"]')).not.toBeNull();
    rerender(<Ident kind="key" value="database" tooltip={false} />);
    expect(container.querySelector('[data-slot="tooltip-trigger"]')).toBeNull();
  });

  it("ReleaseIdent and NamespaceIdent compose the value", () => {
    const { container } = render(
      <>
        <ReleaseIdent name="runtime" version={12} tooltip={false} />
        <NamespaceIdent ns={{ env: "prod", app: "gradethis" }} tooltip={false} />
      </>,
    );
    const chips = container.querySelectorAll(".ident");
    expect(chips[0]).toHaveClass("ident-release");
    expect(chips[0]?.querySelector(".ident-value")).toHaveTextContent("runtime@12");
    expect(chips[1]).toHaveClass("ident-ns");
    expect(chips[1]).toHaveClass("ident-prod");
    expect(chips[1]?.querySelector(".ident-value")).toHaveTextContent("prod/gradethis");
  });
});
