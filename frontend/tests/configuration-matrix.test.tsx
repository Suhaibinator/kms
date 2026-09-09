import { fireEvent, render, screen, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import {
  ConfigurationMatrix,
  type ConfigurationMatrixProps,
} from "@/components/applications/ConfigurationMatrix";
import { links } from "@/lib/links";
import type { ApplicationOverview } from "@/lib/types";
import incidentJson from "./fixtures/backend/overview-incident.json";
import readyJson from "./fixtures/backend/overview-ready.json";

const mocks = vi.hoisted(() => ({
  query: {} as Record<string, string>,
  replace: vi.fn(),
}));

// CopyButton reports through the toast context.
vi.mock("@/context/ToastContext", () => ({
  useToast: () => ({ success: vi.fn(), error: vi.fn(), info: vi.fn() }),
}));
vi.mock("next/router", () => ({
  useRouter: () => ({
    query: mocks.query,
    pathname: "/applications",
    isReady: true,
    push: vi.fn(),
    replace: mocks.replace,
  }),
}));

const ready = readyJson as unknown as ApplicationOverview;
const incident = incidentJson as unknown as ApplicationOverview;
const clone = <T,>(value: T): T => JSON.parse(JSON.stringify(value)) as T;

function propsFor(
  overview: ApplicationOverview,
  props: Partial<ConfigurationMatrixProps> = {},
): ConfigurationMatrixProps {
  return {
    app: overview.application.name,
    environments: overview.environments.map((environment) => ({
      env: environment.namespace.env,
      production: environment.production,
    })),
    overview: overview.environments,
    rows: overview.rows,
    onAddSecret: vi.fn(),
    onAddValue: vi.fn(),
    onOpenSecret: vi.fn(),
    onOpenParameter: vi.fn(),
    onEdit: vi.fn(),
    ...props,
  };
}

function renderMatrix(
  overview: ApplicationOverview,
  props: Partial<ConfigurationMatrixProps> = {},
): ConfigurationMatrixProps {
  const all = propsFor(overview, props);
  render(<ConfigurationMatrix {...all} />);
  return all;
}

/** The first environment (by overview order) whose cell for `key` is present. */
function presentEnv(overview: ApplicationOverview, key: string): string {
  const row = overview.rows.find((candidate) => candidate.key === key);
  const env = overview.environments.find(
    (environment) => row?.environments[environment.namespace.env]?.present,
  );
  if (!row || !env) throw new Error(`no present cell for ${key}`);
  return env.namespace.env;
}

describe("ConfigurationMatrix", () => {
  beforeEach(() => {
    mocks.query = {};
    mocks.replace.mockReset();
  });

  it("renders a secret cell as a labelled link with the lock glyph and the binding badge", () => {
    const overview = clone(ready);
    const secret = overview.rows.find((row) => row.kind === "secret");
    if (!secret) throw new Error("fixture has no secret row");
    const env = presentEnv(overview, secret.key);
    secret.environments[env].bound = true;
    renderMatrix(overview);

    const table = screen.getByRole("table");
    const link = within(table).getByRole("link", { name: `Open ${secret.key} in ${env}` });
    expect(link).toHaveAttribute(
      "href",
      links.secretDetail({ env, app: overview.application.name, key: secret.key }),
    );
    expect(link).toHaveTextContent(`Secret v${secret.environments[env].version}`);
    expect(within(link).getByRole("img", { name: "Secret" })).toBeInTheDocument();
    expect(table.querySelector(".secret-cell")).toBeNull();

    // Exactly one cell was bound, so exactly one badge, in the one vocabulary.
    expect(within(table).getAllByText("binding key")).toHaveLength(1);
    expect(within(table).queryByText(/bound/)).toBeNull();
    const glyphs = within(table).getAllByRole("img", { name: "Secret" });
    const presentSecretCells = overview.rows
      .filter((row) => row.kind === "secret")
      .flatMap((row) => Object.values(row.environments).filter((cell) => cell.present));
    expect(glyphs).toHaveLength(presentSecretCells.length);
  });

  it("opens the secret workspace on a plain click", () => {
    const overview = clone(ready);
    const secret = overview.rows.find((row) => row.kind === "secret");
    if (!secret) throw new Error("fixture has no secret row");
    const env = presentEnv(overview, secret.key);
    const props = renderMatrix(overview);
    fireEvent.click(screen.getByRole("link", { name: `Open ${secret.key} in ${env}` }));
    expect(props.onOpenSecret).toHaveBeenCalledWith(env, secret.key);
  });

  it("shows the contract alias under a key only when it differs from the key", () => {
    const overview = clone(ready);
    const [first] = overview.environments;
    const value = first.values.find((candidate) => candidate.present && candidate.key);
    if (!value?.key) throw new Error("fixture has no resolved value");
    // The fixture's aliases equal their keys, so nothing is repeated under the key.
    const { unmount } = render(<ConfigurationMatrix {...propsFor(overview)} />);
    expect(document.querySelector(".matrix-alias")).toBeNull();
    unmount();

    const renamed = `${value.alias}_alias`;
    value.alias = renamed;
    renderMatrix(overview);
    const aliases = document.querySelectorAll(".matrix-alias");
    expect(aliases).toHaveLength(1);
    expect(aliases[0].closest("td")).toHaveTextContent(value.key);
    expect(within(aliases[0] as HTMLElement).getByText(renamed)).toBeInTheDocument();
  });

  it("renders the pipeline's drift badge from the overview value", () => {
    const overview = clone(incident);
    const drifted = overview.environments.flatMap((environment) =>
      environment.values
        .filter(
          (value) =>
            value.present &&
            environment.release.active &&
            (value.pinned_version === undefined ||
              (value.current_version ?? 0) > value.pinned_version),
        )
        .map((value) => ({ env: environment.namespace.env, value })),
    );
    expect(drifted.length).toBeGreaterThan(0);
    renderMatrix(overview);
    const badges = screen.getAllByText(/unreleased$/);
    expect(badges).toHaveLength(drifted.length);
    for (const { value } of drifted) {
      expect(screen.getByText(`v${value.current_version} unreleased`)).toBeVisible();
    }
  });

  it("offers Add value on a missing parameter cell and reports it", () => {
    const overview = clone(ready);
    const parameter = overview.rows.find((row) => row.kind === "parameter");
    if (!parameter) throw new Error("fixture has no parameter row");
    const env = presentEnv(overview, parameter.key);
    parameter.environments[env] = { present: false, content_type: "", version: 0 };
    const props = renderMatrix(overview);

    expect(screen.queryByText("missing")).toBeNull();
    const add = screen.getByRole("button", { name: "Add value" });
    fireEvent.click(add);
    expect(props.onAddValue).toHaveBeenCalledWith(env, parameter.key);
    expect(screen.queryByRole("link", { name: `Open ${parameter.key} in ${env}` })).toBeNull();
  });

  it("offers Add secret on a missing secret cell", () => {
    const overview = clone(ready);
    const secret = overview.rows.find((row) => row.kind === "secret");
    if (!secret) throw new Error("fixture has no secret row");
    const env = presentEnv(overview, secret.key);
    secret.environments[env] = { present: false, content_type: "", version: 0 };
    const props = renderMatrix(overview);
    fireEvent.click(screen.getByRole("button", { name: "Add secret" }));
    expect(props.onAddSecret).toHaveBeenCalledWith(env, secret.key);
  });

  it("disables Add value until the page wires the callback", () => {
    const overview = clone(ready);
    const parameter = overview.rows.find((row) => row.kind === "parameter");
    if (!parameter) throw new Error("fixture has no parameter row");
    parameter.environments[presentEnv(overview, parameter.key)] = {
      present: false,
      content_type: "",
      version: 0,
    };
    renderMatrix(overview, { onAddValue: undefined });
    expect(screen.getByRole("button", { name: "Add value" })).toBeDisabled();
  });

  it("filters rows by key and by alias", () => {
    const overview = clone(ready);
    const [first] = overview.environments;
    const [target, ...others] = overview.rows;
    expect(others.length).toBeGreaterThan(0);
    const value = first.values.find(
      (candidate) => candidate.kind === target.kind && candidate.key === target.key,
    );
    if (!value) throw new Error("fixture row is not in the contract");
    value.alias = "zzz_contract_name";
    // The box itself lives on the application page now, beside the tabs, so
    // one filter narrows both this table and the pipeline.
    const props = propsFor(overview, { filter: target.key.toUpperCase() });
    const { rerender } = render(<ConfigurationMatrix {...props} />);

    let table = screen.getByRole("table");
    expect(within(table).getByText(target.key)).toBeVisible();
    for (const row of others.filter((row) => !row.key.includes(target.key))) {
      expect(within(table).queryByText(row.key)).toBeNull();
    }

    rerender(<ConfigurationMatrix {...props} filter="zzz_contract" />);
    table = screen.getByRole("table");
    expect(within(table).getByText(target.key)).toBeVisible();
    expect(within(table).getAllByRole("row")).toHaveLength(2); // header + the one match

    rerender(<ConfigurationMatrix {...props} filter="no-such-key-anywhere" />);
    expect(screen.getByText("No rows match the filter.")).toBeVisible();
  });

  it("toggles to incomplete rows only and counts what is missing per environment", () => {
    const overview = clone(ready);
    const [incomplete, ...complete] = overview.rows;
    expect(complete.length).toBeGreaterThan(0);
    const env = presentEnv(overview, incomplete.key);
    incomplete.environments[env] = { present: false, content_type: "", version: 0 };
    renderMatrix(overview);

    const table = screen.getByRole("table");
    const footer = table.querySelector("tfoot");
    expect(footer).not.toBeNull();
    const cells = within(footer as HTMLElement).getAllByRole("cell");
    // Key, kind, one per environment, actions gutter.
    expect(cells).toHaveLength(overview.environments.length + 3);
    overview.environments.forEach((environment, index) => {
      const cell = cells[index + 2];
      expect(cell).toHaveTextContent(environment.namespace.env === env ? "1 missing" : "");
    });

    fireEvent.click(screen.getByRole("checkbox", { name: "Only incomplete rows" }));
    expect(within(table).getByText(incomplete.key)).toBeVisible();
    for (const row of complete) {
      expect(within(table).queryByText(row.key)).toBeNull();
    }
  });

  it("hides the footer when nothing is missing", () => {
    renderMatrix(clone(ready));
    expect(screen.getByRole("table").querySelector("tfoot")).toBeNull();
  });

  it("sorts by key through the URL and honours the order it asks for", () => {
    const overview = clone(ready);
    renderMatrix(overview);
    fireEvent.click(screen.getByRole("button", { name: "Key" }));
    expect(mocks.replace).toHaveBeenCalledWith(
      { pathname: links.applications(), query: { sort: "key", dir: "asc" } },
      undefined,
      { shallow: true, scroll: false },
    );
  });

  it("orders rows by the sort in the URL", () => {
    const overview = clone(ready);
    mocks.query = { sort: "key", dir: "desc" };
    renderMatrix(overview);
    const keys = Array.from(document.querySelectorAll("tbody td.matrix-key")).map(
      (cell) => cell.firstChild?.textContent,
    );
    const expected = overview.rows
      .map((row) => row.key)
      .sort((a, b) => b.localeCompare(a, undefined, { numeric: true, sensitivity: "base" }));
    expect(keys).toEqual(expected);
    expect(screen.getByRole("columnheader", { name: "Key" })).toHaveAttribute(
      "aria-sort",
      "descending",
    );
  });

  it("names the action column and scopes the comparison width floor to environment cells", () => {
    const overview = clone(ready);
    renderMatrix(overview);
    // The trailing column was an unnamed <th> that screen readers announced as
    // a blank, and that inherited the 160px floor meant for value comparison.
    expect(screen.getByRole("columnheader", { name: "Actions" })).toHaveClass("matrix-actions");

    const envCount = overview.environments.length;
    const headers = document.querySelectorAll("thead th.matrix-env");
    expect(headers).toHaveLength(envCount);
    // Key, Kind and the action cell must not carry the floor.
    for (const row of document.querySelectorAll("tbody tr")) {
      expect(row.querySelectorAll("td.matrix-env")).toHaveLength(envCount);
      expect(row.querySelector("td.matrix-key")).not.toHaveClass("matrix-env");
    }
  });

  it("links each environment header to that environment on the application page", () => {
    const overview = clone(ready);
    renderMatrix(overview);
    for (const environment of overview.environments) {
      const env = environment.namespace.env;
      const header = screen.getByRole("columnheader", { name: env });
      expect(within(header).getByRole("link")).toHaveAttribute(
        "href",
        links.application(overview.application.name, { env }),
      );
    }
  });
});
