import { ChevronDown, ExternalLink } from "lucide-react";
import { useId } from "react";
import { Highlight } from "@/components/Highlight";
import { Icon } from "@/components/icons";
import { Badge, type BadgeKind } from "@/components/ui";
import { Button, ButtonLink } from "@/components/ui/button";
import type { ResourceRef } from "@/lib/api";
import { formatRelative, formatUnixMs } from "@/lib/format";
import { matchRanges } from "@/lib/fuzzy";
import { type DiffRowModel, reasonLabel } from "@/lib/release-diff";
import type { ReleaseDiffPin, ReleaseEntryKind } from "@/lib/types";
import {
  hasBody,
  ValueChangeBody,
  ValueChangeInline,
  ValuePre,
  type ValueViewMode,
} from "./ValueChange";

const CHANGE_BADGE: Record<DiffRowModel["change"], BadgeKind> = {
  changed: "accent",
  added: "success",
  removed: "danger",
  unchanged: "neutral",
};

function SideMeta({ pin, now }: { pin: ReleaseDiffPin | undefined; now: number }) {
  if (!pin) return <span className="release-diff-missing">—</span>;
  return (
    <span>
      <span className="mono">v{pin.version}</span>
      {pin.created_by ? ` by ${pin.created_by}` : ""}
      {pin.created_at_unix_ms ? (
        <>
          {" "}
          <span title={formatUnixMs(pin.created_at_unix_ms)}>
            {formatRelative(pin.created_at_unix_ms, now)}
          </span>
        </>
      ) : null}
    </span>
  );
}

export interface ReleaseDiffRowProps {
  model: DiffRowModel;
  valuesIncluded: boolean;
  q: string;
  expanded: boolean;
  onToggle: () => void;
  beforeLabel: string;
  afterLabel: string;
  mode: ValueViewMode;
  onModeChange: (mode: ValueViewMode) => void;
  now: number;
  compact?: boolean;
  /** Prod-vs-staging: a parameter's version number is expected to differ, so "pin only" is not a change reason worth a badge. */
  crossEnvironment?: boolean;
  href?: string | null;
  onOpen?: (ref: ResourceRef, kind: ReleaseEntryKind) => void;
  /** An unchanged row's value once loaded (the two sides share it). */
  loadedValue?: string;
  loadingValue?: boolean;
  onLoadValue?: () => void;
}

/**
 * One alias: a head with the identifier, the inline change and the badges,
 * a meta line naming who wrote each version, and an expandable body.
 */
export function ReleaseDiffRow({
  model,
  valuesIncluded,
  q,
  expanded,
  onToggle,
  beforeLabel,
  afterLabel,
  mode,
  onModeChange,
  now,
  compact,
  crossEnvironment,
  href,
  onOpen,
  loadedValue,
  loadingValue,
  onLoadValue,
}: ReleaseDiffRowProps) {
  const { row } = model;
  const bodyId = useId();
  const expandable = hasBody(model) || (row.change === "unchanged" && valuesIncluded);
  const side = row.to ?? row.from;
  const ref: ResourceRef | null = side
    ? { env: side.ref.namespace.env, app: side.ref.namespace.app, key: side.ref.key }
    : null;
  const extraReasons = row.reasons.filter(
    (reason) => reason !== "value" && !(crossEnvironment && reason === "pin"),
  );

  return (
    <li>
      <article
        className="release-diff-row"
        data-testid="release-diff-row"
        data-alias={row.alias}
        data-change={row.change}
        data-kind={row.kind}
        data-attention={model.attention ? "true" : undefined}
        data-flags={model.flags.length ? model.flags.join(" ") : undefined}
      >
        <div className="release-diff-row-head">
          <div className="release-diff-row-ident">
            {row.kind === "secret" ? (
              <span className="kind-glyph" role="img" aria-label="Secret">
                <Icon.secret size={13} />
              </span>
            ) : null}
            <span className="ident ident-alias" data-kind="alias">
              <span className="ident-kind" aria-hidden="true">
                alias
              </span>
              <span className="ident-value" title={row.alias}>
                <Highlight text={row.alias} ranges={matchRanges(row.alias, q)} />
              </span>
            </span>
            {model.key && model.key !== row.alias ? (
              <span className="ident ident-key faint" data-kind="key">
                <span className="ident-kind" aria-hidden="true">
                  key
                </span>
                <span className="ident-value" title={model.key}>
                  <Highlight text={model.key} ranges={matchRanges(model.key, q)} />
                </span>
              </span>
            ) : null}
          </div>
          <div className="release-diff-row-change">
            <ValueChangeInline
              model={model}
              valuesIncluded={valuesIncluded}
              crossEnvironment={crossEnvironment}
              loadingValue={loadingValue}
              onLoadValue={onLoadValue}
            />
          </div>
          <div className="release-diff-row-aside">
            <Badge kind={CHANGE_BADGE[row.change]}>{row.change}</Badge>
            {model.contentType && row.kind === "parameter" ? (
              <Badge kind="neutral">{model.contentType}</Badge>
            ) : null}
            {extraReasons.map((reason) => (
              <Badge key={reason} kind="neutral">
                {reasonLabel(reason)}
              </Badge>
            ))}
            {model.attention
              ? model.attentionReasons
                  .filter((reason) => !extraReasons.some((r) => reasonLabel(r) === reason))
                  .map((reason) => (
                    <Badge key={reason} kind="warning">
                      {reason}
                    </Badge>
                  ))
              : null}
            {model.flags.map((flag) => (
              <Badge key={flag} kind="warning">
                {flag}
              </Badge>
            ))}
            {ref && onOpen ? (
              <Button
                variant="ghost"
                size="xs"
                onClick={() => onOpen(ref, row.kind)}
                aria-label={`Open ${row.kind} ${ref.key}`}
              >
                <ExternalLink aria-hidden />
                Open
              </Button>
            ) : href ? (
              <ButtonLink
                href={href}
                variant="ghost"
                size="xs"
                aria-label={`Open ${row.kind} ${ref?.key ?? row.alias}`}
              >
                <ExternalLink aria-hidden />
                Open
              </ButtonLink>
            ) : null}
            {expandable ? (
              <Button
                variant="ghost"
                size="icon-xs"
                aria-expanded={expanded}
                aria-controls={bodyId}
                aria-label={expanded ? `Collapse ${row.alias}` : `Expand ${row.alias}`}
                onClick={onToggle}
              >
                <ChevronDown
                  aria-hidden
                  style={{ transform: expanded ? "rotate(180deg)" : undefined }}
                />
              </Button>
            ) : null}
          </div>
        </div>
        {row.from || row.to ? (
          <div className="release-diff-row-meta">
            <SideMeta pin={row.from} now={now} />
            <span className="release-diff-arrow" aria-hidden>
              →
            </span>
            <SideMeta pin={row.to} now={now} />
          </div>
        ) : null}
        {expanded && expandable ? (
          <div className="release-diff-row-body" id={bodyId}>
            {row.change === "unchanged" ? (
              loadedValue !== undefined ? (
                <ValuePre text={loadedValue} label={`v${side?.version ?? ""}`} />
              ) : (
                <div className="release-diff-mode">
                  <span className="faint text-sm">Both releases pin the same version.</span>
                  {onLoadValue ? (
                    <Button
                      variant="outline"
                      size="xs"
                      loading={loadingValue}
                      onClick={onLoadValue}
                    >
                      Load value
                    </Button>
                  ) : null}
                </div>
              )
            ) : (
              <ValueChangeBody
                model={model}
                beforeLabel={beforeLabel}
                afterLabel={afterLabel}
                mode={mode}
                onModeChange={onModeChange}
                compact={compact}
              />
            )}
          </div>
        ) : null}
      </article>
    </li>
  );
}
