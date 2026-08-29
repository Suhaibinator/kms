import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import NamespacePicker from "@/components/NamespacePicker";
import type { Namespace } from "@/lib/types";
import { chooseSelectOption, visibleSelectOptions } from "./select-test-utils";

function namespace(app: string, env: string): Namespace {
  return {
    app,
    env,
    description: "",
    allowed_auth_methods: ["mtls"],
    created_by: "admin",
    created_at_unix_ms: 1,
    parameter_count: 0,
    secret_count: 0,
  };
}

const namespaces = [
  namespace("billing", "dev"),
  namespace("billing", "prod"),
  namespace("search", "preview"),
];

describe("NamespacePicker", () => {
  it("selects an application before offering only its environments", async () => {
    const onChange = vi.fn();
    const { rerender } = render(
      <NamespacePicker namespaces={namespaces} value={{ app: "", env: "" }} onChange={onChange} />,
    );

    const app = screen.getByRole("combobox", { name: "Application" });
    const env = screen.getByRole("combobox", { name: "Environment" });
    expect(app).toBeEnabled();
    expect(env).toBeDisabled();
    expect(await visibleSelectOptions(app)).toEqual(["billing", "search"]);

    await chooseSelectOption(app, "billing");
    expect(onChange).toHaveBeenLastCalledWith({ app: "billing", env: "" });

    rerender(
      <NamespacePicker
        namespaces={namespaces}
        value={{ app: "billing", env: "" }}
        onChange={onChange}
      />,
    );
    expect(screen.getByRole("combobox", { name: "Environment" })).toBeEnabled();
    expect(
      await visibleSelectOptions(screen.getByRole("combobox", { name: "Environment" })),
    ).toEqual(["dev", "prod"]);
  });

  it("clears an environment that does not belong to the new application", async () => {
    const onChange = vi.fn();
    render(
      <NamespacePicker
        namespaces={namespaces}
        value={{ app: "billing", env: "prod" }}
        onChange={onChange}
      />,
    );

    await chooseSelectOption(screen.getByRole("combobox", { name: "Application" }), "search");
    expect(onChange).toHaveBeenCalledWith({ app: "search", env: "" });
  });

  it("keeps a value that is not in the list selected and labels it as not found", async () => {
    render(
      <NamespacePicker
        namespaces={namespaces}
        value={{ app: "ghost", env: "staging" }}
        onChange={vi.fn()}
      />,
    );

    const app = screen.getByRole("combobox", { name: "Application" });
    const env = screen.getByRole("combobox", { name: "Environment" });
    expect(app).toHaveTextContent("ghost (not found)");
    expect(env).toHaveTextContent("staging (not found)");
    expect(await visibleSelectOptions(app)).toEqual(["ghost (not found)", "billing", "search"]);
    expect(await visibleSelectOptions(env)).toEqual(["staging (not found)"]);
  });

  it("says the list is loading instead of looking empty or not found", () => {
    const { rerender } = render(
      <NamespacePicker namespaces={[]} value={{ app: "", env: "" }} onChange={vi.fn()} loading />,
    );
    const app = screen.getByRole("combobox", { name: "Application" });
    const env = screen.getByRole("combobox", { name: "Environment" });
    expect(app).toBeDisabled();
    expect(env).toBeDisabled();
    expect(app).toHaveTextContent("Loading applications…");
    expect(env).toHaveTextContent("Loading environments…");

    // A deep-linked value is shown as-is while the list that would confirm it loads.
    rerender(
      <NamespacePicker
        namespaces={[]}
        value={{ app: "billing", env: "prod" }}
        onChange={vi.fn()}
        loading
      />,
    );
    expect(screen.getByRole("combobox", { name: "Application" })).toHaveTextContent("billing");
    expect(screen.getByRole("combobox", { name: "Application" })).not.toHaveTextContent(
      "not found",
    );
    expect(screen.getByRole("combobox", { name: "Environment" })).not.toHaveTextContent(
      "not found",
    );
  });

  it("renders field errors inline and marks the control invalid", () => {
    render(
      <NamespacePicker
        namespaces={namespaces}
        value={{ app: "billing", env: "" }}
        onChange={vi.fn()}
        envError="Choose an environment."
      />,
    );

    expect(screen.getByRole("alert")).toHaveTextContent("Choose an environment.");
    expect(screen.getByRole("combobox", { name: "Environment" })).toHaveAttribute(
      "aria-invalid",
      "true",
    );
    expect(screen.getByRole("combobox", { name: "Application" })).not.toHaveAttribute(
      "aria-invalid",
    );
  });
});
