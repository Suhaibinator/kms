import { fireEvent, render, screen, within } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { ActionMenu } from "@/components/applications/ActionMenu";

describe("ActionMenu", () => {
  it("disables the trigger instead of opening an empty menu", () => {
    const onOpenChange = vi.fn();
    render(
      <ActionMenu
        label="No actions"
        trigger={<button type="button">More</button>}
        items={[]}
        onOpenChange={onOpenChange}
      />,
    );

    const trigger = screen.getByRole("button", { name: "More" });
    expect(trigger).toBeDisabled();
    expect(trigger).toHaveAttribute("aria-disabled", "true");

    fireEvent.click(trigger);
    expect(screen.queryByRole("menu")).not.toBeInTheDocument();
    expect(onOpenChange).not.toHaveBeenCalled();
  });

  it("renders an empty submenu as a disabled item", async () => {
    render(
      <ActionMenu
        label="More actions"
        trigger={<button type="button">More</button>}
        items={[{ key: "connect", label: "Connect SDK", children: [] }]}
      />,
    );

    fireEvent.click(screen.getByRole("button", { name: "More" }));
    const menu = await screen.findByRole("menu", { name: "More" });
    const item = within(menu).getByRole("menuitem", { name: "Connect SDK" });
    expect(item).toHaveAttribute("aria-disabled", "true");
    expect(item).not.toHaveAttribute("aria-haspopup");

    fireEvent.click(item);
    expect(screen.getAllByRole("menu")).toHaveLength(1);
  });

  it("renders actions, links and a submenu that picks an environment", async () => {
    const onEdit = vi.fn();
    const onPick = vi.fn();
    render(
      <ActionMenu
        label="More actions"
        trigger={<button type="button">More</button>}
        items={[
          { key: "edit", label: "Edit definition", onSelect: onEdit },
          { key: "docs", label: "Docs", href: "/docs" },
          {
            key: "connect",
            label: "Connect SDK",
            children: [
              { key: "dev", label: "dev", onSelect: () => onPick("dev") },
              { key: "prod", label: "prod", onSelect: () => onPick("prod") },
            ],
          },
        ]}
      />,
    );
    fireEvent.click(screen.getByRole("button", { name: "More" }));
    const menu = await screen.findByRole("menu", { name: "More" });
    expect(within(menu).getByRole("menuitem", { name: "Docs" })).toHaveAttribute("href", "/docs");
    const trigger = within(menu).getByRole("menuitem", { name: "Connect SDK" });
    expect(trigger).toHaveAttribute("aria-haspopup", "menu");
    fireEvent.click(trigger);
    const submenu = await screen.findByRole("menu", { name: "Connect SDK" });
    fireEvent.click(within(submenu).getByRole("menuitem", { name: "prod" }));
    expect(onPick).toHaveBeenCalledWith("prod");
    expect(onEdit).not.toHaveBeenCalled();
  });
});
