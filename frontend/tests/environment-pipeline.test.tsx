import { fireEvent, render, screen, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { EnvironmentCallbacks } from "@/components/applications/EnvironmentColumn";
import {
  EnvironmentPipeline,
  orderEnvironments,
} from "@/components/applications/EnvironmentPipeline";
import { formatRelative, formatUnixMs } from "@/lib/format";
import { links } from "@/lib/links";
import { findingCopy } from "@/lib/readiness";
import type { ApplicationOverview, EnvironmentOverview, Finding } from "@/lib/types";
import incidentJson from "./fixtures/backend/overview-incident.json";

// CopyButton (the row's Copy key) reports through the toast context.
vi.mock("@/context/ToastContext", () => ({
  useToast: () => ({ success: vi.fn(), info: vi.fn(), error: vi.fn(), dismiss: vi.fn() }),
}));

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
  onOpenSecret: vi.fn(),
  onOpenParameter: vi.fn(),
  onShip: vi.fn(),
  onRollback: vi.fn(),
  onConnect: vi.fn(),
  onImportDefaults: vi.fn(),
  onEditContract: vi.fn(),
  onFix: vi.fn(),
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
    expect(within(columns[1]).getByTitle(/\(production\)$/)).toBeVisible();
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

  it("scrolls the focused column into view without moving keyboard focus", () => {
    renderPipeline(incident, "prod");
    const prod = screen.getByRole("region", { name: "prod environment" });
    expect(prod).toHaveClass("pipeline-column-focused");
    expect(Element.prototype.scrollIntoView).toHaveBeenCalledTimes(1);
    // A `?env=` deep link is a place to look, not a request to move the cursor.
    expect(document.activeElement).not.toBe(prod);
  });

  it("lists the findings its sections do not already show, with their Fix action", () => {
    const overview = clone(incident);
    const prod = env(overview, "prod");
    const unreadable: Finding = {
      code: "secret_unreadable",
      severity: "blocking",
      scope: { env: "prod", alias: "db_password" },
      params: { alias: "db_password", state: "disabled" },
    };
    prod.findings.push(unreadable);
    renderPipeline(overview);
    const column = screen.getByRole("region", { name: "prod environment" });
    const item = within(column).getByText(findingCopy(unreadable)).closest("li");
    expect(item).toHaveClass("finding-blocking");
    fireEvent.click(within(item as HTMLElement).getByRole("button", { name: "Open secret" }));
    expect(callbacks.onFix).toHaveBeenCalledWith("open_secret", unreadable);
    // Drift, the rejected instance and the production notice are rendered by
    // the Values / Subscribers sections and the ring already — not repeated.
    for (const finding of prod.findings.filter((candidate) => candidate !== unreadable)) {
      expect(within(column).queryByText(findingCopy(finding))).toBeNull();
    }
    const dev = screen.getByRole("region", { name: "dev environment" });
    expect(dev.querySelector(".finding-list")).toBeNull();
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
    fireEvent.click(within(menu).getByRole("menuitem", { name: "Connect SDK" }));
    expect(callbacks.onConnect).toHaveBeenCalledWith("prod");
    fireEvent.click(screen.getByRole("button", { name: "More for prod" }));
    fireEvent.click(await screen.findByRole("menuitem", { name: "Import defaults to prod…" }));
    expect(callbacks.onImportDefaults).toHaveBeenCalledWith("prod");
  });

  it("marks secret rows with a lock glyph and offers Copy key and Manage on every present row", () => {
    renderPipeline(incident);
    const prod = screen.getByRole("region", { name: "prod environment" });
    const values = env(incident, "prod").values;
    const present = values.filter((value) => value.present);
    const secrets = values.filter((value) => value.kind === "secret");
    expect(within(prod).getAllByRole("img", { name: "Secret" })).toHaveLength(secrets.length);
    const copyButtons = within(prod).getAllByRole("button", { name: "Copy key" });
    expect(copyButtons).toHaveLength(present.length);
    // Icon-only so the three row actions fit the column's 266px content box;
    // the label stays the accessible name, visually hidden.
    for (const button of copyButtons) {
      expect(button).toHaveAttribute("data-size", "icon-sm");
      expect(within(button).getByText("Copy key")).toHaveClass("sr-only");
    }
    const app = incident.application.name;
    const secret = secrets[0];
    const manageSecret = within(prod).getByRole("link", {
      name: `Manage ${secret.alias} in prod`,
    });
    expect(manageSecret).toHaveAttribute(
      "href",
      links.secretDetail({ env: "prod", app, key: secret.key ?? secret.alias }),
    );
    fireEvent.click(manageSecret);
    expect(callbacks.onOpenSecret).toHaveBeenCalledWith("prod", secret.key ?? secret.alias);
    const parameter = values.find((value) => value.kind === "parameter" && value.present);
    if (!parameter) throw new Error("fixture has no present parameter");
    const manageParameter = within(prod).getByRole("link", {
      name: `Manage ${parameter.alias} in prod`,
    });
    expect(manageParameter).toHaveAttribute(
      "href",
      links.parameterDetail({ env: "prod", app, key: parameter.key ?? parameter.alias }),
    );
    fireEvent.click(manageParameter);
    expect(callbacks.onOpenParameter).toHaveBeenCalledWith(
      "prod",
      parameter.key ?? parameter.alias,
    );
  });

  it("shows the resolved key only when it differs from the alias, and the binding badge only when bound", () => {
    const overview = clone(incident);
    const prod = env(overview, "prod");
    const secret = prod.values.find((value) => value.kind === "secret");
    if (!secret) throw new Error("fixture has no secret alias");
    secret.key = `${secret.alias}-v2`;
    secret.bound = true;
    renderPipeline(overview);
    const column = screen.getByRole("region", { name: "prod environment" });
    const row = column.querySelector(`[data-alias="${secret.alias}"]`) as HTMLElement;
    expect(within(row).getByText(secret.key)).toHaveClass("ident-value");
    expect(within(row).getByText("binding key")).toBeVisible();
    expect(within(column).getAllByText("binding key")).toHaveLength(1);
    // Copy key copies the resolved key, not the alias.
    expect(within(row).getByRole("button", { name: "Copy key" })).toBeVisible();
    // Rows whose key equals the alias render one chip for it.
    for (const value of prod.values.filter((value) => value !== secret)) {
      const other = column.querySelector(`[data-alias="${value.alias}"]`) as HTMLElement;
      expect(within(other).getAllByText(value.alias)).toHaveLength(1);
    }
    const dev = screen.getByRole("region", { name: "dev environment" });
    expect(within(dev).queryByText("binding key")).toBeNull();
  });

  it("counts secrets no alias resolves to separately from parameters", () => {
    const overview = clone(incident);
    overview.rows.push({
      key: "legacy-token",
      kind: "secret",
      environments: { dev: { present: true, content_type: "text/plain", version: 1 } },
    });
    renderPipeline(overview);
    const column = screen.getByRole("region", { name: "dev environment" });
    expect(within(column).getByRole("link", { name: "1 other secret → Secrets" })).toHaveAttribute(
      "href",
      links.secrets({ env: "dev", app: overview.application.name }),
    );
    expect(within(column).queryByRole("link", { name: /other keys/ })).toBeNull();
  });

  it("offers Edit contract when the contract has no aliases", () => {
    const overview = clone(incident);
    env(overview, "dev").values = [];
    renderPipeline(overview);
    const column = screen.getByRole("region", { name: "dev environment" });
    expect(within(column).getByText("The contract has no aliases.")).toBeVisible();
    fireEvent.click(within(column).getByRole("button", { name: "Edit contract" }));
    expect(callbacks.onEditContract).toHaveBeenCalledWith("dev");
  });

  it("says when and by whom the active release shipped, and links a newer inactive release", () => {
    const overview = clone(incident);
    const prod = env(overview, "prod");
    const active = prod.release.active;
    if (!active) throw new Error("fixture has no active release in prod");
    prod.release.latest_version = active.version + 3;
    renderPipeline(overview);
    const column = screen.getByRole("region", { name: "prod environment" });
    const meta = within(column).getByText(
      `shipped ${formatRelative(active.created_at_unix_ms)} by ${active.created_by}`,
    );
    expect(meta).toHaveAttribute("title", formatUnixMs(active.created_at_unix_ms));
    const latest = `${active.name}@${active.version + 3}`;
    expect(
      within(column).getByRole("link", { name: `latest v${active.version + 3} not active` }),
    ).toHaveAttribute(
      "href",
      links.releases({
        app: overview.application.name,
        env: "prod",
        name: active.name,
        release: latest,
      }),
    );
    const dev = screen.getByRole("region", { name: "dev environment" });
    expect(within(dev).queryByText(/not active/)).toBeNull();
  });

  it("makes the scroller a focusable, labelled group", () => {
    renderPipeline(incident);
    const scroller = screen.getByRole("group", { name: "Environment pipeline" });
    expect(scroller).toHaveAttribute("tabindex", "0");
  });
});
