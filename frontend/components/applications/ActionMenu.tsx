import { Menu } from "@base-ui/react/menu";
import { ChevronRight } from "lucide-react";
import Link from "next/link";
import { cloneElement, type ReactElement, type ReactNode } from "react";

export interface ActionMenuItem {
  key: string;
  label: ReactNode;
  /** Renders a next/link item instead of a button item. */
  href?: string;
  onSelect?: () => void;
  disabled?: boolean;
  /** Renders a submenu of these items instead of acting itself. */
  children?: ActionMenuItem[];
}

function renderItems(items: ActionMenuItem[]): ReactNode {
  return items.map((item) => {
    if (item.children) {
      if (item.children.length === 0) {
        return (
          <Menu.Item key={item.key} className="menu-item" disabled>
            {item.label}
          </Menu.Item>
        );
      }
      return (
        <Menu.SubmenuRoot key={item.key} disabled={item.disabled}>
          <Menu.SubmenuTrigger className="menu-item menu-item-submenu">
            {item.label}
            <ChevronRight size={14} aria-hidden className="menu-item-chevron" />
          </Menu.SubmenuTrigger>
          <Menu.Portal>
            <Menu.Positioner sideOffset={4} className="isolate z-50">
              <Menu.Popup className="menu-popup">{renderItems(item.children)}</Menu.Popup>
            </Menu.Positioner>
          </Menu.Portal>
        </Menu.SubmenuRoot>
      );
    }
    if (item.href) {
      return (
        <Menu.LinkItem
          key={item.key}
          href={item.href}
          className="menu-item"
          render={<Link href={item.href} />}
        >
          {item.label}
        </Menu.LinkItem>
      );
    }
    return (
      <Menu.Item
        key={item.key}
        className="menu-item"
        disabled={item.disabled}
        onClick={item.onSelect}
      >
        {item.label}
      </Menu.Item>
    );
  });
}

/**
 * A small dropdown of actions or links on Base UI's Menu. `trigger` is the
 * element the menu attaches to (usually a Button); `label` names the popup for
 * assistive tech. Pass `open`/`onOpenChange` to drive it from the URL. An item
 * with `children` opens a submenu (e.g. an action that needs an environment).
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
  if (items.length === 0) {
    return cloneElement(
      trigger as ReactElement<{ "aria-disabled"?: boolean; disabled?: boolean }>,
      {
        "aria-disabled": true,
        disabled: true,
      },
    );
  }

  return (
    <Menu.Root modal={false} open={open} onOpenChange={onOpenChange}>
      <Menu.Trigger render={trigger} />
      <Menu.Portal>
        <Menu.Positioner align={align} sideOffset={4} className="isolate z-50">
          <Menu.Popup className="menu-popup" aria-label={label}>
            {renderItems(items)}
          </Menu.Popup>
        </Menu.Positioner>
      </Menu.Portal>
    </Menu.Root>
  );
}
