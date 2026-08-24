import Link from "next/link";
import type { ReactNode } from "react";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { IDENT_GLOSSARY, type IdentKind } from "@/lib/glossary";
import { isProductionEnvironment } from "@/lib/readiness";
import type { NamespaceRef } from "@/lib/types";
import { cn } from "@/lib/utils";

export interface IdentProps {
  kind: IdentKind;
  value: string;
  /** Wraps the chip in a next/link. */
  href?: string;
  /** Replaces the glossary definition; `false` renders no tooltip. */
  tooltip?: ReactNode | false;
  /** Adds the `ident-prod` modifier. Inferred for env chips from the name. */
  production?: boolean;
  className?: string;
}

function displayValue(kind: IdentKind, value: string): string {
  if (kind === "version") return /^v\d/.test(value) ? value : `v${value}`;
  return value;
}

/**
 * A typed identifier chip: `<span class="ident ident-env">` with a kind prefix
 * and a mono value. The prefix is decorative — screen readers get the value and
 * the tooltip's term.
 */
export function Ident({ kind, value, href, tooltip, production, className }: IdentProps) {
  const entry = IDENT_GLOSSARY[kind];
  const prod = production ?? (kind === "env" && isProductionEnvironment(value));
  const classes = cn("ident", `ident-${kind}`, prod && "ident-prod", className);
  const body = (
    <>
      {entry.prefix ? (
        <span className="ident-kind" aria-hidden="true">
          {entry.prefix}
        </span>
      ) : null}
      <span className="ident-value">{displayValue(kind, value)}</span>
    </>
  );

  const chip = href ? (
    <Link href={href} className="ident-link">
      <span className={classes} data-kind={kind}>
        {body}
      </span>
    </Link>
  ) : (
    <span className={classes} data-kind={kind}>
      {body}
    </span>
  );

  if (tooltip === false) return chip;
  return (
    <Tooltip>
      <TooltipTrigger render={<span className="ident-tip" />}>{chip}</TooltipTrigger>
      <TooltipContent>
        {tooltip ?? (
          <span>
            <strong>{entry.term}.</strong> {entry.definition}
          </span>
        )}
      </TooltipContent>
    </Tooltip>
  );
}

/** `runtime@12` — a release name and version as one chip. */
export function ReleaseIdent({
  name,
  version,
  href,
  tooltip,
  className,
}: {
  name: string;
  version: number;
  href?: string;
  tooltip?: ReactNode | false;
  className?: string;
}) {
  return (
    <Ident
      kind="release"
      value={`${name}@${version}`}
      href={href}
      tooltip={tooltip}
      className={className}
    />
  );
}

/** `prod/gradethis` — an (env, app) pair; marked prod from the env name. */
export function NamespaceIdent({
  ns,
  href,
  tooltip,
  className,
}: {
  ns: NamespaceRef;
  href?: string;
  tooltip?: ReactNode | false;
  className?: string;
}) {
  return (
    <Ident
      kind="ns"
      value={`${ns.env}/${ns.app}`}
      href={href}
      tooltip={tooltip}
      production={isProductionEnvironment(ns.env)}
      className={className}
    />
  );
}
