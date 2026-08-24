import { Menu } from "@base-ui/react/menu";
import Link from "next/link";
import type { ReactElement, ReactNode } from "react";

export interface ActionMenuItem {
  key: string;
  label: ReactNode;
  /** Renders a next/link item instead of a button item. */
  href?: string;
  onSelect?: () => void;
  disabled?: boolean;
}

/**
 * A small dropdown of actions or links on Base UI's Menu. `trigger` is the
 * element the menu attaches to (usually a Button); `label` names the popup for
 * assistive tech. Pass `open`/`onOpenChange` to drive it from the URL.
 */
export function ActionMenu({
  trigger,
  items,
  label,
  open,
  onOpenChange,
  align = "end",
}: {
  trigger: ReactElement;
  items: ActionMenuItem[];
  label: string;
  open?: boolean;
  onOpenChange?: (open: boolean) => void;
  align?: "start" | "end";
}) {
  return (
    <Menu.Root modal={false} open={open} onOpenChange={onOpenChange}>
      <Menu.Trigger render={trigger} />
      <Menu.Portal>
        <Menu.Positioner align={align} sideOffset={4} className="isolate z-50">
          <Menu.Popup className="menu-popup" aria-label={label}>
            {items.map((item) =>
              item.href ? (
                <Menu.LinkItem
                  key={item.key}
                  href={item.href}
                  className="menu-item"
                  render={<Link href={item.href} />}
                >
                  {item.label}
                </Menu.LinkItem>
              ) : (
                <Menu.Item
                  key={item.key}
                  className="menu-item"
                  disabled={item.disabled}
                  onClick={item.onSelect}
                >
                  {item.label}
                </Menu.Item>
              ),
            )}
          </Menu.Popup>
        </Menu.Positioner>
      </Menu.Portal>
    </Menu.Root>
  );
}
