import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { AddResourceButton } from "@/components/applications/AddResourceButton";
import { ResourceLink } from "@/components/applications/ResourceLink";
import { isUnreleased, UnreleasedBadge } from "@/components/applications/ValueBadges";
import { BindingKeyBadge, BindingModeBadge } from "@/components/secrets/SecretBadges";
import { links } from "@/lib/links";
import { aliasesByKey, countOtherKeys, valueFor } from "@/lib/overview";
import type { ApplicationConfigurationRow, ApplicationOverview, OverviewValue } from "@/lib/types";
import readyJson from "./fixtures/backend/overview-ready.json";

const ready = readyJson as unknown as ApplicationOverview;
const clone = <T,>(value: T): T => JSON.parse(JSON.stringify(value)) as T;

describe("SecretBadges", () => {
  it("names the two protection modes with one vocabulary", () => {
    render(
      <>
        <BindingModeBadge bound />
        <BindingModeBadge bound={false} />
      </>,
    );
    expect(screen.getByText("binding key")).toBeVisible();
    expect(screen.getByText("master key only")).toBeVisible();
    expect(screen.queryByText(/^bound$/)).toBeNull();
  });

  it("wraps the binding-key badge in an inline-flex tooltip trigger", () => {
    const { container } = render(<BindingKeyBadge version={4} />);
    expect(container.querySelectorAll(".badge-tip")).toHaveLength(1);
    expect(screen.getByText("binding key")).toBeVisible();
  });
});

describe("UnreleasedBadge", () => {
  const present: OverviewValue = {
    alias: "rate_limits",
    kind: "parameter",
    key: "rate_limits",
    present: true,
    current_version: 3,
    pinned_version: 2,
  };

  it("mirrors isUnreleased and names the pinned version", () => {
    expect(isUnreleased(present, true)).toBe(true);
    expect(isUnreleased(present, false)).toBe(false);
    expect(isUnreleased({ ...present, pinned_version: 3 }, true)).toBe(false);
    expect(isUnreleased({ ...present, present: false }, true)).toBe(false);
    render(<UnreleasedBadge value={present} hasActiveRelease />);
    expect(screen.getByText("v3 unreleased")).toBeVisible();
  });

  it("renders nothing for a value the active release already pins", () => {
    const { container } = render(
      <UnreleasedBadge value={{ ...present, pinned_version: 3 }} hasActiveRelease />,
    );
    expect(container).toBeEmptyDOMElement();
  });
});

describe("AddResourceButton", () => {
  it("labels by kind and forwards clicks", () => {
    const onClick = vi.fn();
    render(
      <>
        <AddResourceButton kind="secret" onClick={onClick} />
        <AddResourceButton kind="parameter" variant="ghost" onClick={onClick} />
      </>,
    );
    fireEvent.click(screen.getByRole("button", { name: "Add secret" }));
    fireEvent.click(screen.getByRole("button", { name: "Add value" }));
    expect(onClick).toHaveBeenCalledTimes(2);
    expect(screen.getByRole("button", { name: "Add value" })).toHaveAttribute(
      "data-variant",
      "ghost",
    );
  });
});

describe("ResourceLink", () => {
  const ref = { env: "prod", app: "gradethis", keyName: "db_password" };

  it("links to the detail page and opens the workspace on a plain click", () => {
    const onOpen = vi.fn();
    render(
      <ResourceLink kind="secret" {...ref} onOpen={onOpen}>
        Manage
      </ResourceLink>,
    );
    const link = screen.getByRole("link", { name: "Manage" });
    expect(link).toHaveAttribute(
      "href",
      links.secretDetail({ env: ref.env, app: ref.app, key: ref.keyName }),
    );
    fireEvent.click(link);
    expect(onOpen).toHaveBeenCalledWith("prod", "db_password");
  });

  it("stays a plain navigation for modifier clicks and when no opener is given", () => {
    const onOpen = vi.fn();
    render(
      <>
        <ResourceLink kind="parameter" {...ref} onOpen={onOpen} button aria-label="With opener">
          Open
        </ResourceLink>
        <ResourceLink kind="parameter" {...ref} aria-label="Without opener">
          Open
        </ResourceLink>
      </>,
    );
    const withOpener = screen.getByRole("link", { name: "With opener" });
    expect(withOpener).toHaveAttribute("data-slot", "button");
    expect(withOpener).toHaveAttribute(
      "href",
      links.parameterDetail({ env: ref.env, app: ref.app, key: ref.keyName }),
    );
    fireEvent.click(withOpener, { metaKey: true });
    fireEvent.click(screen.getByRole("link", { name: "Without opener" }));
    expect(onOpen).not.toHaveBeenCalled();
  });
});

describe("lib/overview", () => {
  const env = ready.environments[0];
  if (!env) throw new Error("fixture has no environments");

  it("finds a contract value by environment and alias", () => {
    const first = env.values[0];
    if (!first) throw new Error("fixture environment has no values");
    expect(valueFor(ready.environments, env.namespace.env, first.alias)).toEqual(first);
    expect(valueFor(ready.environments, "nowhere", first.alias)).toBeUndefined();
    expect(valueFor(ready.environments, env.namespace.env, "no_such_alias")).toBeUndefined();
  });

  it("maps resolved keys back to aliases per kind", () => {
    const map = aliasesByKey(env);
    for (const value of env.values) {
      if (!value.key) continue;
      expect(map.get(`${value.kind}:${value.key}`)).toBe(value.alias);
    }
  });

  it("counts present resources no alias resolves to, per kind, without mixing kinds", () => {
    const overview = clone(ready);
    const target = overview.environments[0];
    if (!target) throw new Error("fixture has no environments");
    const name = target.namespace.env;
    const secretAlias = target.values.find((value) => value.kind === "secret");
    if (!secretAlias?.key) throw new Error("fixture has no resolved secret alias");
    const cell = (present: boolean) => ({ present, content_type: "string", version: 1 });
    const rows: ApplicationConfigurationRow[] = [
      ...overview.rows,
      // A parameter whose key collides with a secret alias's key must still count.
      { key: secretAlias.key, kind: "parameter", environments: { [name]: cell(true) } },
      { key: "orphan_secret", kind: "secret", environments: { [name]: cell(true) } },
      { key: "absent", kind: "parameter", environments: { [name]: cell(false) } },
    ];
    const baseline = countOtherKeys(target, overview.rows);
    const counts = countOtherKeys(target, rows);
    expect(counts.parameters).toBe(baseline.parameters + 1);
    expect(counts.secrets).toBe(baseline.secrets + 1);
  });
});
