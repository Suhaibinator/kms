import Link from "next/link";
import type { ComponentProps, MouseEvent, ReactNode } from "react";
import { ButtonLink } from "@/components/ui/button";
import { links } from "@/lib/links";
import type { ReleaseEntryKind } from "@/lib/types";
import { shouldOpenWorkspace } from "@/lib/workspace";

type ButtonVariant = ComponentProps<typeof ButtonLink>["variant"];
type ButtonSize = ComponentProps<typeof ButtonLink>["size"];

/**
 * A link to a parameter or secret detail page that opens the in-page
 * workspace on a plain click and stays a real link for modifier clicks,
 * middle clicks and "copy link". Renders a button-styled link when `button`
 * is set (row actions) and a plain anchor otherwise (table cells).
 */
export function ResourceLink({
  kind,
  env,
  app,
  keyName,
  onOpen,
  button,
  variant = "ghost",
  size = "sm",
  className,
  title,
  "aria-label": ariaLabel,
  children,
}: {
  kind: ReleaseEntryKind;
  env: string;
  app: string;
  keyName: string;
  /** Plain-click handler; when omitted every click navigates. */
  onOpen?: (env: string, key: string) => void;
  button?: boolean;
  variant?: ButtonVariant;
  size?: ButtonSize;
  className?: string;
  title?: string;
  "aria-label"?: string;
  children: ReactNode;
}) {
  const ref = { env, app, key: keyName };
  const href = kind === "secret" ? links.secretDetail(ref) : links.parameterDetail(ref);
  const onClick = (event: MouseEvent<HTMLAnchorElement>) => {
    if (onOpen && shouldOpenWorkspace(event)) onOpen(env, keyName);
  };
  if (button) {
    return (
      <ButtonLink
        href={href}
        variant={variant}
        size={size}
        className={className}
        title={title}
        aria-label={ariaLabel}
        onClick={onClick}
      >
        {children}
      </ButtonLink>
    );
  }
  return (
    <Link href={href} className={className} title={title} aria-label={ariaLabel} onClick={onClick}>
      {children}
    </Link>
  );
}
