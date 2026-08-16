import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
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

afterEach(cleanup);

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
});
