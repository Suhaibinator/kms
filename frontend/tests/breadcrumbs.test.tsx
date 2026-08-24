import { render, screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { Breadcrumbs } from "@/components/Breadcrumbs";
import { PageHeader } from "@/components/ui";
import { crumbs } from "@/lib/crumbs";

describe("crumbs", () => {
  it("builds the full trail for each level", () => {
    expect(crumbs.application("gradethis")).toEqual([
      { label: "Applications", href: "/applications" },
      { ident: { kind: "app", value: "gradethis" }, href: "/applications?app=gradethis" },
    ]);
    expect(crumbs.parameter({ env: "prod", app: "gradethis", key: "config/database" })).toEqual([
      { label: "Applications", href: "/applications" },
      { ident: { kind: "app", value: "gradethis" }, href: "/applications?app=gradethis" },
      { ident: { kind: "env", value: "prod" }, href: "/applications?app=gradethis&env=prod" },
      { label: "Parameters", href: "/parameters?env=prod&app=gradethis" },
      {
        ident: { kind: "key", value: "config/database" },
        href: "/parameters/detail?env=prod&app=gradethis&key=config%2Fdatabase",
      },
    ]);
    expect(crumbs.secret({ env: "dev", app: "gradethis", key: "db_password" }).slice(3)).toEqual([
      { label: "Secrets", href: "/secrets?env=dev&app=gradethis" },
      {
        ident: { kind: "key", value: "db_password" },
        href: "/secrets/detail?env=dev&app=gradethis&key=db_password",
      },
    ]);
    expect(crumbs.release({ env: "prod", app: "gradethis" }, "runtime", 12).slice(3)).toEqual([
      { label: "Releases", href: "/releases?app=gradethis&env=prod&name=runtime" },
      {
        ident: { kind: "release", value: "runtime@12" },
        href: "/releases?app=gradethis&env=prod&name=runtime&release=runtime%4012",
      },
    ]);
  });
});

describe("Breadcrumbs", () => {
  it("renders a labelled nav with typed chips, linking every crumb but the last", () => {
    render(<Breadcrumbs items={crumbs.environment({ env: "prod", app: "gradethis" })} />);
    const nav = screen.getByRole("navigation", { name: "Breadcrumb" });
    const items = within(nav).getAllByRole("listitem");
    expect(items).toHaveLength(3);
    expect(
      within(items[0] as HTMLElement).getByRole("link", { name: "Applications" }),
    ).toHaveAttribute("href", "/applications");
    expect(within(items[1] as HTMLElement).getByRole("link")).toHaveAttribute(
      "href",
      "/applications?app=gradethis",
    );
    expect(items[1]?.querySelector(".ident-app")).not.toBeNull();
    expect(items[2]).toHaveAttribute("aria-current", "page");
    expect(within(items[2] as HTMLElement).queryByRole("link")).toBeNull();
    expect(items[2]?.querySelector(".ident-env.ident-prod")).not.toBeNull();
  });

  it("renders nothing for an empty trail", () => {
    const { container } = render(<Breadcrumbs items={[]} />);
    expect(container.querySelector("nav")).toBeNull();
  });

  it("is rendered by PageHeader above the header", () => {
    const { container } = render(
      <PageHeader title="gradethis" breadcrumbs={crumbs.application("gradethis")} />,
    );
    const nav = container.querySelector("nav[aria-label='Breadcrumb']");
    const header = container.querySelector(".page-header");
    expect(nav).not.toBeNull();
    expect(nav?.compareDocumentPosition(header as Node) ?? 0).toBe(
      Node.DOCUMENT_POSITION_FOLLOWING,
    );
  });
});
