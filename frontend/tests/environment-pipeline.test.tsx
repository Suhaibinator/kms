import { fireEvent, render, screen, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { EnvironmentCallbacks } from "@/components/applications/EnvironmentColumn";
import {
  EnvironmentPipeline,
  orderEnvironments,
} from "@/components/applications/EnvironmentPipeline";
import type { ApplicationOverview, EnvironmentOverview } from "@/lib/types";
import incidentJson from "./fixtures/backend/overview-incident.json";

const incident = incidentJson as unknown as ApplicationOverview;
const clone = <T,>(value: T): T => JSON.parse(JSON.stringify(value)) as T;

function env(overview: ApplicationOverview, name: string): EnvironmentOverview {
  const found = overview.environments.find((environment) => environment.namespace.env === name);
  if (!found) throw new Error(`fixture has no ${name} environment`);
  return found;
}

const callbacks: EnvironmentCallbacks = {
  onAddValue: vi.fn(),
  onAddSecret: vi.fn(),
  onShip: vi.fn(),
  onRollback: vi.fn(),
  onConnect: vi.fn(),
};

function renderPipeline(overview: ApplicationOverview, focusEnv?: string) {
  return render(
    <EnvironmentPipeline
      application={overview.application}
      environments={overview.environments}
      rows={overview.rows}
      focusEnv={focusEnv}
      callbacks={callbacks}
    />,
  );
}

describe("EnvironmentPipeline", () => {
  beforeEach(() => {
    for (const callback of Object.values(callbacks))
      (callback as ReturnType<typeof vi.fn>).mockClear();
    Element.prototype.scrollIntoView = vi.fn();
  });

  it("orders production environments last regardless of the server order", () => {
    const overview = clone(incident);
    overview.environments.reverse();
    expect(orderEnvironments(overview.environments).map((e) => e.namespace.env)).toEqual([
      "dev",
      "prod",
    ]);
    renderPipeline(overview);
    const columns = screen.getAllByRole("region", { name: /environment$/ });
    expect(columns.map((column) => column.getAttribute("data-env"))).toEqual(["dev", "prod"]);
    expect(columns[1]).toHaveClass("pipeline-column-prod");
    expect(within(columns[1]).getByText("production")).toBeVisible();
  });

  it("marks values newer than the active pins as unreleased, with the pin in the tooltip trigger", () => {
    renderPipeline(incident);
    const prod = screen.getByRole("region", { name: "prod environment" });
    const dev = screen.getByRole("region", { name: "dev environment" });
    const drifted = env(incident, "prod").values.find(
      (value) => value.present && (value.current_version ?? 0) > (value.pinned_version ?? 0),
    );
    if (!drifted) throw new Error("fixture has no drift");
    expect(within(prod).getByText(`v${drifted.current_version} unreleased`)).toBeVisible();
    expect(within(dev).queryByText(/unreleased/)).toBeNull();
    fireEvent.click(
      within(prod).getByRole("button", { name: `Edit & ship ${drifted.alias} in prod` }),
    );
    expect(callbacks.onShip).toHaveBeenCalledWith("prod", drifted.alias);
  });

  it("offers Add value / Add secret for missing aliases and counts other keys", () => {
    const overview = clone(incident);
    const dev = env(overview, "dev");
    for (const value of dev.values) {
      value.present = false;
      value.key = undefined;
    }
    overview.rows.push({
      key: "feature-flags",
      kind: "parameter",
      environments: { dev: { present: true, content_type: "json", version: 1 } },
    });
    renderPipeline(overview);
    const column = screen.getByRole("region", { name: "dev environment" });
    const parameters = dev.values.filter((value) => value.kind === "parameter").length;
    const secrets = dev.values.length - parameters;
    expect(within(column).getAllByRole("button", { name: "Add value" })).toHaveLength(parameters);
    expect(within(column).getAllByRole("button", { name: "Add secret" })).toHaveLength(secrets);
    fireEvent.click(within(column).getAllByRole("button", { name: "Add value" })[0]);
    expect(callbacks.onAddValue).toHaveBeenCalledWith("dev", dev.values[0].alias);
    fireEvent.click(within(column).getByRole("button", { name: "Add secret" }));
    expect(callbacks.onAddSecret).toHaveBeenCalledWith(
      "dev",
      dev.values.find((value) => value.kind === "secret")?.alias,
    );
    // Every contract key is now unresolved, so all present parameter rows count as "other".
    const other = overview.rows.filter(
      (row) => row.kind === "parameter" && row.environments.dev?.present,
    ).length;
    expect(
      within(column).getByRole("link", { name: `${other} other keys → Parameters` }),
    ).toHaveAttribute("href", `/parameters?env=dev&app=${overview.application.name}`);
  });

  it("links the active release and offers Roll back only when there is a previous version", () => {
    renderPipeline(incident);
    const prod = screen.getByRole("region", { name: "prod environment" });
    const active = env(incident, "prod").release.active;
    if (!active) throw new Error("fixture has no active release in prod");
    const key = `${active.name}@${active.version}`;
    expect(within(prod).getByRole("link", { name: key })).toHaveAttribute(
      "href",
      `/releases?app=${incident.application.name}&env=prod&name=${active.name}&release=${encodeURIComponent(key)}`,
    );
    expect(prod.querySelector(".ident-revision")).toHaveTextContent(
      `rev${active.activation_revision}`,
    );
    fireEvent.click(within(prod).getByRole("button", { name: "Roll back" }));
    expect(callbacks.onRollback).toHaveBeenCalledWith("prod");
    // dev has no previous version, so there is nothing to roll back to.
    const dev = screen.getByRole("region", { name: "dev environment" });
    expect(within(dev).queryByRole("button", { name: /Roll back|Re-activate/ })).toBeNull();
    expect(within(dev).getByText("Up to date")).toBeVisible();
  });

  it("relabels Roll back after a rollback and warns that the newer release is available", () => {
    const overview = clone(incident);
    const active = env(overview, "prod").release.active;
    if (!active) throw new Error("fixture has no active release in prod");
    active.version = 1;
    active.previous_version = 2;
    active.is_rolled_back = true;
    renderPipeline(overview);
    const prod = screen.getByRole("region", { name: "prod environment" });
    expect(within(prod).getByRole("button", { name: "Re-activate v2" })).toBeEnabled();
    expect(within(prod).getByText(/Rolled back\./)).toBeVisible();
  });

  it("phrases the call to action from the release and values state", () => {
    const overview = clone(incident);
    const dev = env(overview, "dev");
    dev.release = { latest_version: 0, release_count: 0 };
    dev.release_state = "none";
    dev.values_state = "incomplete";
    dev.values[0].present = false;
    renderPipeline(overview);
    const devColumn = screen.getByRole("region", { name: "dev environment" });
    expect(within(devColumn).getByRole("button", { name: "Create first release" })).toBeDisabled();
    expect(
      within(devColumn).getByText(`Add values for \`${dev.values[0].alias}\` first.`),
    ).toBeVisible();
    expect(within(devColumn).getByText("No release is active.")).toBeVisible();

    const prodColumn = screen.getByRole("region", { name: "prod environment" });
    const unreleased = env(overview, "prod").values.filter(
      (value) => value.present && (value.current_version ?? 0) > (value.pinned_version ?? 0),
    ).length;
    fireEvent.click(
      within(prodColumn).getByRole("button", {
        name: `${unreleased} unreleased ${unreleased === 1 ? "change" : "changes"} → Ship`,
      }),
    );
    expect(callbacks.onShip).toHaveBeenCalledWith("prod");
  });

  it("enables Create first release once every value exists", () => {
    const overview = clone(incident);
    const dev = env(overview, "dev");
    dev.release = { latest_version: 0, release_count: 0 };
    dev.release_state = "none";
    renderPipeline(overview);
    const column = screen.getByRole("region", { name: "dev environment" });
    fireEvent.click(within(column).getByRole("button", { name: "Create first release" }));
    expect(callbacks.onShip).toHaveBeenCalledWith("dev");
  });

  it("summarises subscribers and expands rejected instances with the category remediation", () => {
    renderPipeline(incident);
    const prod = screen.getByRole("region", { name: "prod environment" });
    const rollout = env(incident, "prod").rollout;
    expect(within(prod).getByText(`connected ${rollout.connected}`)).toBeVisible();
    expect(within(prod).getByText(`applied ${rollout.applied_current}`)).toBeVisible();
    expect(within(prod).getByText(`rejected ${rollout.rejected}`)).toBeVisible();
    const details = within(prod)
      .getByText(/rejected instance/)
      .closest("details");
    expect(details).not.toBeNull();
    const instance = rollout.rejected_instances[0];
    expect(
      within(details as HTMLElement).getByText(`${instance.client_name}/${instance.instance_id}`),
    ).toBeInTheDocument();
    expect(
      within(details as HTMLElement).getByText(instance.rejection_category),
    ).toBeInTheDocument();
    expect(
      within(details as HTMLElement).getByText(`still serving v${instance.release_version}`),
    ).toBeInTheDocument();
  });

  it("points an environment with no subscribers at Connect SDK and warns about other release names", () => {
    const overview = clone(incident);
    const dev = env(overview, "dev");
    dev.rollout = { ...dev.rollout, total: 0, connected: 0, applied_current: 0 };
    const prod = env(overview, "prod");
    prod.rollout.other_release_names = ["legacy"];
    renderPipeline(overview);
    const devColumn = screen.getByRole("region", { name: "dev environment" });
    expect(within(devColumn).getByText("No subscribers")).toBeVisible();
    fireEvent.click(within(devColumn).getByRole("button", { name: "Connect SDK" }));
    expect(callbacks.onConnect).toHaveBeenCalledWith("dev");
    const prodColumn = screen.getByRole("region", { name: "prod environment" });
    expect(within(prodColumn).getByText(/different release name/)).toBeVisible();
    expect(within(prodColumn).getByText("legacy")).toBeVisible();
  });

  it("scrolls the focused column into view", () => {
    renderPipeline(incident, "prod");
    expect(screen.getByRole("region", { name: "prod environment" })).toHaveClass(
      "pipeline-column-focused",
    );
    expect(Element.prototype.scrollIntoView).toHaveBeenCalledTimes(1);
  });

  it("offers the namespace pages from the column menu", async () => {
    renderPipeline(incident);
    fireEvent.click(screen.getByRole("button", { name: "More for prod" }));
    // Base UI names the popup after its trigger.
    const menu = await screen.findByRole("menu", { name: "More for prod" });
    const app = incident.application.name;
    expect(within(menu).getByRole("menuitem", { name: "Parameters" })).toHaveAttribute(
      "href",
      `/parameters?env=prod&app=${app}`,
    );
    expect(within(menu).getByRole("menuitem", { name: "Secrets" })).toHaveAttribute(
      "href",
      `/secrets?env=prod&app=${app}`,
    );
    expect(within(menu).getByRole("menuitem", { name: "Releases" })).toHaveAttribute(
      "href",
      `/releases?app=${app}&env=prod&name=${incident.application.release_name}`,
    );
  });
});
