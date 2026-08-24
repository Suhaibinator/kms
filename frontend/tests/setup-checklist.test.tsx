import { fireEvent, render, screen, within } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import FirstRunChecklist from "@/components/onboarding/FirstRunChecklist";
import SetupChecklist from "@/components/onboarding/SetupChecklist";
import SetupPanel from "@/components/onboarding/SetupPanel";
import { deriveSetupSteps } from "@/lib/setup-steps";
import type { ApplicationOverview } from "@/lib/types";
import readyJson from "./fixtures/backend/overview-ready.json";
import setupJson from "./fixtures/backend/overview-setup.json";

const ready = readyJson as unknown as ApplicationOverview;
const setup = setupJson as unknown as ApplicationOverview;

describe("SetupChecklist", () => {
  it("renders an ordered list reusing the wizard step circles, with the current step marked", () => {
    const steps = deriveSetupSteps({ applicationCount: 0, namespaceCount: 0, overview: null });
    const { container } = render(<SetupChecklist steps={steps} onAction={vi.fn()} />);

    const list = screen.getByRole("list", { name: "Setup steps" });
    expect(list.tagName).toBe("OL");
    expect(list).toHaveClass("setup-steps");
    const items = within(list).getAllByRole("listitem");
    expect(items).toHaveLength(9);

    // The informational token note carries an info mark, not a number.
    expect(items[0]).toHaveAttribute("data-step", "token");
    expect(items[0]).toHaveClass("wizard-step-complete");
    expect(items[0]?.querySelector(".wizard-step-number")).toHaveClass("setup-step-info");

    // Numbering skips the informational step: "Create an application" is 1.
    expect(items[1]).toHaveAttribute("aria-current", "step");
    expect(items[1]).toHaveClass("wizard-step-current", "setup-step-current");
    expect(items[1]?.querySelector(".wizard-step-number")).toHaveTextContent("1");
    expect(items[2]).toHaveClass("wizard-step-upcoming", "setup-step-todo");
    expect(items[2]?.querySelector(".wizard-step-number")).toHaveTextContent("2");
    expect(container.querySelectorAll("[aria-current='step']")).toHaveLength(1);

    // Optional steps are tagged so they never read as blocking.
    expect(within(items[3] as HTMLElement).getByText("optional")).toBeVisible();
  });

  it("only offers the current step's action, dispatching the SetupAction", () => {
    const onAction = vi.fn();
    const steps = deriveSetupSteps({ applicationCount: 1, namespaceCount: 0, overview: setup });
    render(<SetupChecklist steps={steps} onAction={onAction} />);

    fireEvent.click(screen.getByRole("button", { name: "Add environment" }));
    expect(onAction).toHaveBeenCalledWith({ kind: "add-environment" });
    // Optional schema step keeps its outline action; todo steps have none.
    fireEvent.click(screen.getByRole("button", { name: "Register schema" }));
    expect(onAction).toHaveBeenCalledWith({ kind: "register-schema" });
    expect(screen.queryByRole("button", { name: "Fill values" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Ship" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Create application" })).toBeNull();
  });

  it("renders per-environment rows under the values step with their own actions", () => {
    const onAction = vi.fn();
    const overview: ApplicationOverview = {
      ...ready,
      environments: ready.environments.map((env) =>
        env.namespace.env === "prod"
          ? {
              ...env,
              values_state: "incomplete",
              values: env.values.map((value) =>
                value.alias === "database" ? { ...value, present: false } : value,
              ),
            }
          : env,
      ),
    };
    const steps = deriveSetupSteps({ applicationCount: 1, namespaceCount: 2, overview });
    render(<SetupChecklist steps={steps} onAction={onAction} />);

    const values = document.querySelector("[data-step='values']") as HTMLElement;
    const rows = values.querySelectorAll(".setup-item");
    expect(rows).toHaveLength(2);
    expect(rows[0]).toHaveClass("setup-item-done");
    expect(rows[0]).toHaveTextContent("dev");
    expect(rows[1]).not.toHaveClass("setup-item-done");
    const total = ready.environments[0]?.values.length ?? 0;
    expect(rows[1]).toHaveTextContent(`${total - 1} of ${total} set`);
    expect(rows[1]?.querySelector(".ident-env")).toHaveClass("ident-prod");

    fireEvent.click(within(rows[1] as HTMLElement).getByRole("button", { name: "Fill values" }));
    expect(onAction).toHaveBeenCalledWith({ kind: "fill-values", env: "prod", alias: "database" });
  });

  it("renders backticked names in step copy as code", () => {
    const steps = deriveSetupSteps({ applicationCount: 1, namespaceCount: 0, overview: setup });
    render(<SetupChecklist steps={steps} onAction={vi.fn()} />);
    const application = document.querySelector("[data-step='application']") as HTMLElement;
    const codes = Array.from(application.querySelectorAll("code.setup-code")).map(
      (node) => node.textContent,
    );
    expect(codes).toEqual([setup.application.name, setup.application.release_name]);
  });
});

describe("FirstRunChecklist", () => {
  it("wires step 1 to the create wizard", () => {
    const onCreate = vi.fn();
    render(<FirstRunChecklist namespaceCount={0} onCreateApplication={onCreate} />);
    expect(screen.getByRole("heading", { name: "Set up your first application" })).toBeVisible();
    fireEvent.click(screen.getByRole("button", { name: "Create application" }));
    expect(onCreate).toHaveBeenCalledTimes(1);
  });

  it("switches to the adopt variant when namespaces already exist", () => {
    render(<FirstRunChecklist namespaceCount={2} onCreateApplication={vi.fn()} />);
    expect(screen.getByRole("heading", { name: "Adopt your existing environments" })).toBeVisible();
    expect(screen.getByText(/2 environment namespaces exist without an application/)).toBeVisible();
    expect(screen.getByText("Create the application for your environments")).toBeVisible();
  });
});

describe("SetupPanel", () => {
  it("is an open advanced panel summarising progress while steps remain", () => {
    const onAction = vi.fn();
    const { container } = render(<SetupPanel overview={setup} onAction={onAction} />);
    const details = container.querySelector("details.setup-panel") as HTMLDetailsElement;
    expect(details).toHaveClass("advanced-panel");
    expect(details.open).toBe(true);
    expect(details.querySelector("summary")).toHaveTextContent("Setup · 2 of 7 done");
    expect(within(details).getByRole("list", { name: "Setup steps" })).toHaveClass(
      "setup-steps-compact",
    );

    fireEvent.click(within(details).getByRole("button", { name: "Add environment" }));
    expect(onAction).toHaveBeenCalledWith({ kind: "add-environment" });
  });

  it("collapses once every step is done", () => {
    const { container } = render(<SetupPanel overview={ready} onAction={vi.fn()} />);
    const details = container.querySelector("details.setup-panel") as HTMLDetailsElement;
    expect(details.open).toBe(false);
    expect(details).toHaveAttribute("data-complete", "true");
    expect(details.querySelector("summary")).toHaveTextContent("Setup · complete");
  });
});
