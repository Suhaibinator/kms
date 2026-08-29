import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { Button, Field, Pagination, SKELETON_ROWS_DEFAULT, TableSkeleton } from "@/components/ui";

afterEach(cleanup);

describe("Field", () => {
  it("associates a generated id and hint with a direct form control", () => {
    render(
      <Field label="Release name" hint="Used by subscribed clients.">
        <input />
      </Field>,
    );

    const input = screen.getByRole("textbox", { name: "Release name" });
    expect(input).toHaveAccessibleDescription("Used by subscribed clients.");
    expect(input).toHaveAttribute("id");
  });

  it("preserves an explicit control id", () => {
    render(
      <Field label="Environment">
        <select id="environment">
          <option>Production</option>
        </select>
      </Field>,
    );

    expect(screen.getByRole("combobox", { name: "Environment" })).toHaveAttribute(
      "id",
      "environment",
    );
  });

  it("marks a required control without reading the asterisk aloud", () => {
    render(
      <Field label="Release name" required>
        <input />
      </Field>,
    );

    const input = screen.getByRole("textbox", { name: "Release name" });
    expect(input).toHaveAttribute("aria-required", "true");
    expect(screen.getByText("*", { exact: false, selector: "span" })).toHaveAttribute(
      "aria-hidden",
      "true",
    );
  });

  it("accepts a composed label", () => {
    render(
      <Field
        label={
          <>
            Type <span className="mono">prod</span> to confirm
          </>
        }
      >
        <input />
      </Field>,
    );

    expect(screen.getByRole("textbox", { name: "Type prod to confirm" })).toBeInTheDocument();
  });

  it("labels composite fields as accessible groups", () => {
    render(
      <Field label="Authentication methods" hint="Choose at least one method.">
        <div>
          <label>
            <input type="checkbox" /> Token
          </label>
        </div>
      </Field>,
    );

    expect(
      screen.getByRole("group", { name: "Authentication methods" }),
    ).toHaveAccessibleDescription("Choose at least one method.");
  });
});

describe("Button", () => {
  it("marks an in-flight action busy and keeps its label", () => {
    render(<Button loading>Save</Button>);
    const button = screen.getByRole("button", { name: /Save/ });
    expect(button).toBeDisabled();
    expect(button).toHaveAttribute("aria-busy", "true");
    expect(button).toHaveAttribute("data-loading", "true");
    expect(button.querySelector("[data-slot=spinner]")).not.toBeNull();
    expect(button).toHaveTextContent("Save");
  });

  it("renders plainly when not loading", () => {
    render(<Button>Save</Button>);
    const button = screen.getByRole("button", { name: "Save" });
    expect(button).toBeEnabled();
    expect(button).not.toHaveAttribute("aria-busy");
    expect(button.querySelector("[data-slot=spinner]")).toBeNull();
  });
});

describe("Pagination", () => {
  it("renders nothing when there is nothing to page or count", () => {
    const { container } = render(<Pagination hasNext={false} onNext={() => undefined} />);
    expect(container.firstChild).toBeNull();
    const { container: empty } = render(
      <Pagination hasNext={false} onNext={() => undefined} count={0} page={1} />,
    );
    expect(empty.firstChild).toBeNull();
  });

  it("shows the row count beside the page and announces it politely", () => {
    render(<Pagination hasNext onNext={() => undefined} page={2} count={12} noun="events" />);
    expect(screen.getByText("12 events")).toBeVisible();
    expect(screen.getByText("Page 2")).toBeVisible();
    expect(screen.getByRole("status")).toHaveTextContent("12 events, page 2");
    expect(screen.getByRole("button", { name: "Next page" })).toBeEnabled();
  });

  it("singularises the noun for one row", () => {
    render(<Pagination hasNext={false} onNext={() => undefined} count={1} noun="entries" />);
    // Once visibly, once in the polite status line.
    expect(screen.getAllByText("1 entry")).toHaveLength(2);
    expect(screen.getByText("End of results")).toBeVisible();
  });

  it("withholds End of results and the count while loading", () => {
    render(<Pagination hasNext={false} onNext={() => undefined} page={3} loading count={4} />);
    expect(screen.queryByText("End of results")).toBeNull();
    expect(screen.getByRole("status")).toHaveTextContent("Loading…");
    expect(screen.getByText("Page 3")).toBeVisible();
  });
});

describe("TableSkeleton", () => {
  it("reserves a list-sized number of rows by default", () => {
    const { container } = render(<TableSkeleton headers={["A", "B"]} />);
    expect(container.querySelectorAll(".skeleton-row")).toHaveLength(SKELETON_ROWS_DEFAULT);
    expect(SKELETON_ROWS_DEFAULT).toBeGreaterThanOrEqual(20);
  });

  it("honours an explicit row count", () => {
    const { container } = render(<TableSkeleton headers={["A"]} rows={3} />);
    expect(container.querySelectorAll(".skeleton-row")).toHaveLength(3);
  });
});
