import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { render, screen } from "@testing-library/react";
import { useState } from "react";
import { describe, expect, it } from "vitest";
import { SchemaForm } from "@/components/SchemaForm";
import type { JsonSchema } from "@/lib/schema-form";

const css = readFileSync(resolve(process.cwd(), "styles", "globals.css"), "utf8").replace(
  /\/\*[\s\S]*?\*\//g,
  "",
);

/** Every declaration the sheet makes for `selector`, in source order. */
function declarations(selector: string): string {
  const found: string[] = [];
  for (const match of css.matchAll(/([^{}]+?)\{([^{}]*)\}/g)) {
    const selectors = (match[1] ?? "").split(",").map((part) => part.trim());
    if (selectors.includes(selector)) found.push(match[2] ?? "");
  }
  return found.join("\n");
}

/**
 * Every string a schema author supplies that the Form mode prints as prose. The
 * URL, the pattern and the default are each a single token with no break
 * opportunity in them, which is what pushed the form past its container.
 */
const schema: JsonSchema = {
  type: "object",
  description: "Root prose naming https://console.example.internal/docs/parameters/base-url-format",
  properties: {
    base_url: {
      type: "string",
      description: "Where the collector posts (for example http://glm-oc.observability.svc:4318).",
      pattern: "^https?://[a-z0-9-]+(\\.[a-z0-9-]+)*(:[0-9]+)?(/[A-Za-z0-9._~%!$&'()*+,;=:@-]*)*$",
      default: "http://glm-oc.observability.svc.cluster.local:4318/v1/traces",
    },
    oauth_device_authorization_endpoint: {
      type: "object",
      description: "Group prose naming https://accounts.example.internal/oauth2/device/authorize",
      properties: { audience: { type: "string" } },
    },
  },
};

function Harness() {
  const [value, setValue] = useState("{}");
  return <SchemaForm schema={schema} value={value} onChange={setValue} />;
}

describe("SchemaForm prose can wrap inside its container", () => {
  it("renders every schema-authored string in an element the wrap rule reaches", () => {
    const { container } = render(<Harness />);

    // The field hint: description, default and pattern joined into one line.
    const hint = screen.getByText(/Where the collector posts/);
    expect(hint.textContent).toContain("Default: ");
    expect(hint.textContent).toContain("Pattern: ");
    expect(hint.getAttribute("data-slot")).toBe("field-description");

    // The group's own description, and the root's above the field list.
    expect(screen.getByText(/Group prose naming/)).toHaveClass("schema-form-description");
    expect(screen.getByText(/Root prose naming/)).toHaveClass("schema-form-description");

    // The group name is a mono token from the schema, printed in a <legend>.
    const legend = container.querySelector(".schema-form-legend");
    expect(legend?.textContent).toContain("oauth_device_authorization_endpoint");
  });

  it("wraps hints, descriptions, errors and group names anywhere", () => {
    // `anywhere` and not `break-word`: only `anywhere` lowers min-content, and
    // min-content is the width a grid item reports to the dialog around it.
    for (const selector of [
      ".field-hint",
      ".field-error",
      '[data-slot="field-description"]',
      '[data-slot="field-error"]',
      ".schema-form-description",
      ".schema-form-legend",
    ]) {
      expect(declarations(selector), selector).toMatch(/overflow-wrap:\s*anywhere/);
    }
  });
});
