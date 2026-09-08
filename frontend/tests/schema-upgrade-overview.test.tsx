import { fireEvent, render, screen } from "@testing-library/react";
import { expect, it, vi } from "vitest";
import { DefinitionCard } from "@/components/applications/DefinitionCard";
import { EnvironmentColumn } from "@/components/applications/EnvironmentColumn";
import type { ApplicationOverview } from "@/lib/types";
import fixture from "./fixtures/backend/overview-incident.json";

vi.mock("@/context/ToastContext", () => ({
  useToast: () => ({ success: vi.fn(), error: vi.fn() }),
}));
const overview = fixture as unknown as ApplicationOverview;
it("keeps the contract collapsed and exposes current/latest schema with an upgrade action", () => {
  const upgrade = vi.fn();
  const { container } = render(
    <DefinitionCard
      overview={overview}
      onManageReleases={vi.fn()}
      onDeriveSchema={vi.fn()}
      latestSchemaVersion={8}
      onUpgrade={upgrade}
    />,
  );
  expect(screen.getByText(/parameters · .* secrets/)).toBeVisible();
  expect(container.querySelector("details")).not.toHaveAttribute("open");
  expect(screen.getByText(/Latest: v8/)).toBeVisible();
  fireEvent.click(screen.getByRole("button", { name: "Upgrade schema…" }));
  expect(upgrade).toHaveBeenCalledOnce();
});
it("groups stale instances without hiding other findings", () => {
  const environment = structuredClone(overview.environments[0]);
  environment.findings = [1, 2, 3].map((n) => ({
    code: "instance_stale",
    severity: "info",
    scope: { env: environment.namespace.env, instance: `instance-${n}` },
    params: {},
  }));
  const { container } = render(
    <EnvironmentColumn
      application={overview.application}
      environment={environment}
      rows={overview.rows}
      focused={false}
      callbacks={{
        onAddValue: vi.fn(),
        onAddSecret: vi.fn(),
        onShip: vi.fn(),
        onRollback: vi.fn(),
        onConnect: vi.fn(),
        onFix: vi.fn(),
      }}
    />,
  );
  expect(screen.getByText("3 stale instances · View details")).toBeVisible();
  expect(container.querySelector("details")).not.toHaveAttribute("open");
});
