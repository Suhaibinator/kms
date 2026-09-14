import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { SearchField } from "@/components/SearchField";
import { SectionHeader } from "@/components/SectionHeader";

describe("SectionHeader", () => {
  it("renders the title as a section heading with its toolbar beside it", () => {
    const { container } = render(
      <SectionHeader title="Values" actions={<button type="button">Add</button>} />,
    );
    const heading = screen.getByRole("heading", { level: 2, name: "Values" });
    expect(heading).toHaveClass("section-title");
    expect(container.querySelector(".section-head-tools")).toHaveTextContent("Add");
  });

  it("can render an h3 instead", () => {
    render(<SectionHeader as="h3" title="Versions" />);
    expect(screen.getByRole("heading", { level: 3, name: "Versions" })).toBeInTheDocument();
  });

  it("puts a non-heading title (a tab list) in the title slot untouched", () => {
    const { container } = render(
      <SectionHeader as="none" title={<div data-testid="tabs">tabs</div>} />,
    );
    expect(screen.queryByRole("heading")).toBeNull();
    expect(container.querySelector(".section-head-text")?.firstChild).toBe(
      screen.getByTestId("tabs"),
    );
  });

  it("omits the toolbar entirely when there is nothing in it", () => {
    const { container } = render(<SectionHeader title="Values" actions={null} />);
    expect(container.querySelector(".section-head-tools")).toBeNull();
  });

  it("keeps caller classes and can be hidden from assistive tech", () => {
    const { container } = render(
      <SectionHeader aria-hidden className="secret-workspace-toolbar" title="Overview" />,
    );
    const head = container.querySelector(".section-head");
    expect(head).toHaveClass("secret-workspace-toolbar");
    expect(head).toHaveAttribute("aria-hidden", "true");
  });

  it("renders a description that top-aligns the row", () => {
    const { container } = render(<SectionHeader title="Matrix" description="Two lines of it." />);
    expect(container.querySelector(".section-head-description")).toHaveTextContent(
      "Two lines of it.",
    );
  });
});

describe("SearchField labelHidden", () => {
  it("hides the caption but keeps the input labelled", () => {
    render(
      <SectionHeader
        title="Values"
        actions={
          <SearchField
            labelHidden
            label="Filter values"
            value=""
            onChange={() => {}}
            onClear={() => {}}
          />
        }
      />,
    );
    const input = screen.getByLabelText("Filter values");
    expect(input).toHaveAttribute("type", "search");
    expect(screen.getByText("Filter values")).toHaveClass("sr-only");
  });

  it("shows the caption by default, as the list pages want it", () => {
    render(<SearchField label="Filter values" value="" onChange={() => {}} onClear={() => {}} />);
    expect(screen.getByText("Filter values")).not.toHaveClass("sr-only");
  });
});
