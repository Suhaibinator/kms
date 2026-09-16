import { Ident } from "@/components/Ident";
import { JsonDiff } from "@/components/JsonDiff";
import { JsonLine } from "@/components/JsonHighlight";
import { BindingModeBadge } from "@/components/secrets/SecretBadges";
import { Badge, SecretStateBadge } from "@/components/ui";
import { Button } from "@/components/ui/button";
import { formatRelative, formatUnixMs } from "@/lib/format";
import { formatJson } from "@/lib/json-text";
import { type DiffRowModel, elide } from "@/lib/release-diff";
import type { ReleaseDiffPin } from "@/lib/types";
import { formatBytes } from "@/lib/validation";
import type { FieldCounts, ValueChangeDescription } from "@/lib/value-diff";
import { FieldDiff } from "./FieldDiff";
import { Arrow, Missing, ScalarInline, Token } from "./tokens";
import type { DiffMode } from "./useDiffMode";

const CHIPS: ReadonlyArray<{
  kind: keyof FieldCounts;
  glyph: string;
  tone: "added" | "removed" | "changed" | "moved";
}> = [
  { kind: "added", glyph: "+", tone: "added" },
  { kind: "removed", glyph: "−", tone: "removed" },
  { kind: "changed", glyph: "~", tone: "changed" },
  { kind: "moved", glyph: "↷", tone: "moved" },
];

/** `+3 −21 ~1 ↷2` as tinted pills; zero kinds are omitted. */
function FieldChips({ counts }: { counts: FieldCounts }) {
  return (
    <span className="release-diff-chips">
      {CHIPS.filter((chip) => counts[chip.kind] > 0).map((chip) => (
        <span
          key={chip.kind}
          className="release-diff-chip"
          data-tone={chip.tone}
          title={`${counts[chip.kind]} ${counts[chip.kind] === 1 ? "field" : "fields"} ${chip.kind}`}
        >
          {chip.glyph}
          {counts[chip.kind]}
        </span>
      ))}
    </span>
  );
}

function JsonInline({
  description,
  fields,
}: {
  description: Extract<ValueChangeDescription, { kind: "json" }>;
  fields: FieldCounts | null;
}) {
  if (description.before === undefined || description.after === undefined) {
    const text = description.before ?? description.after ?? "";
    return (
      <>
        {description.before === undefined ? <Missing /> : null}
        <Token text={elide(text)} op={description.before === undefined ? "add" : "del"} />
        {description.after === undefined ? (
          <>
            <Arrow />
            <Missing />
          </>
        ) : null}
      </>
    );
  }
  const structural = description.structural;
  if (!structural || !fields) {
    return (
      <span className="faint">
        {description.oversize
          ? "Too large to compare structurally; open the line diff"
          : description.invalid
            ? "Stored value is not valid JSON on one side"
            : "Changed"}
      </span>
    );
  }
  if (structural.changes.length === 0) return <span className="faint">No leaf differences</span>;
  return <FieldChips counts={fields} />;
}

function SecretSide({ pin }: { pin: ReleaseDiffPin | undefined }) {
  if (!pin) return <Missing />;
  return (
    <span className="release-diff-secret-sides">
      <Ident kind="version" value={String(pin.version)} tooltip={false} />
      {pin.secret_state ? <SecretStateBadge state={pin.secret_state} /> : null}
      {pin.bound !== undefined ? <BindingModeBadge bound={pin.bound} /> : null}
      {pin.expires_at_unix_ms ? (
        <Badge kind="neutral" title={formatUnixMs(pin.expires_at_unix_ms)}>
          expires {formatRelative(pin.expires_at_unix_ms)}
        </Badge>
      ) : null}
    </span>
  );
}

/** The head column: what changed, on one line, by type. Never a secret value. */
export function ValueChangeInline({
  model,
  valuesIncluded,
  crossEnvironment,
  loadingValue,
  onLoadValue,
}: {
  model: DiffRowModel;
  valuesIncluded: boolean;
  crossEnvironment?: boolean;
  loadingValue?: boolean;
  onLoadValue?: () => void;
}) {
  const { row, description } = model;
  if (row.kind === "secret") {
    return (
      <>
        <SecretSide pin={row.from} />
        <Arrow />
        <SecretSide pin={row.to} />
        {crossEnvironment && row.change === "changed" ? (
          <span className="faint">different version (expected)</span>
        ) : null}
      </>
    );
  }
  if (description) {
    return description.kind === "json" ? (
      <JsonInline description={description} fields={model.fields} />
    ) : (
      <ScalarInline change={description} />
    );
  }
  const side = row.to ?? row.from;
  if (!valuesIncluded || !side || side.value_state === "omitted_request") {
    return (
      <>
        {row.from ? (
          <Ident kind="version" value={String(row.from.version)} tooltip={false} />
        ) : (
          <Missing />
        )}
        <Arrow />
        {row.to ? (
          <Ident kind="version" value={String(row.to.version)} tooltip={false} />
        ) : (
          <Missing />
        )}
        {valuesIncluded && onLoadValue ? (
          <Button variant="outline" size="xs" loading={loadingValue} onClick={onLoadValue}>
            Load value
          </Button>
        ) : null}
      </>
    );
  }
  if (side.value_state === "omitted_size") {
    return (
      <>
        <span className="faint">Too large to compare inline ({formatBytes(side.value_bytes)})</span>
        {onLoadValue ? (
          <Button variant="outline" size="xs" loading={loadingValue} onClick={onLoadValue}>
            Load value
          </Button>
        ) : null}
      </>
    );
  }
  if (side.value_state === "unavailable") {
    return <span className="faint">Value not readable with your permissions</span>;
  }
  if (side.value_state === "omitted_unchanged") return <span className="faint">unchanged</span>;
  return <span className="faint">{model.summary}</span>;
}

/** A single stored value, pretty-printed when it is JSON. */
export function ValuePre({
  text,
  op,
  label,
}: {
  text: string;
  op?: "del" | "add";
  label?: string;
}) {
  const pretty = formatJson(text) ?? text;
  return (
    <div>
      {label ? <span className="release-diff-side-label">{label}</span> : null}
      <pre className="release-diff-pre" data-op={op}>
        {formatJson(text) ? (
          pretty.split("\n").map((line, index) => (
            <span key={`${index}:${line}`}>
              <JsonLine text={line} />
              {"\n"}
            </span>
          ))
        ) : (
          <JsonLine text={text} plain />
        )}
      </pre>
    </div>
  );
}

/** Whether a row has anything to show under its head. */
export function hasBody(model: DiffRowModel): boolean {
  const { row, description } = model;
  if (row.kind === "secret") return false;
  if (row.change === "unchanged") return true;
  if (!description) return false;
  if (description.kind === "json") return true;
  if (description.kind === "string") {
    return description.long || description.before === undefined || description.after === undefined;
  }
  return false;
}

/**
 * The expanded body: the field list or a line diff for JSON (per the page's
 * value view), a line diff for long strings, one value for one-sided rows.
 */
export function ValueChangeBody({
  model,
  beforeLabel,
  afterLabel,
  mode,
  compact,
}: {
  model: DiffRowModel;
  beforeLabel: string;
  afterLabel: string;
  mode: DiffMode;
  compact?: boolean;
}) {
  const { description } = model;
  if (!description || description.kind === "binary") return null;
  const maxHeight = compact ? "40vh" : "60vh";
  // The field list has no line layout; when it is not available, Fields reads as Unified.
  const layout = mode === "split" ? "split" : "unified";
  if (description.kind === "json") {
    if (description.before === undefined || description.after === undefined) {
      const text = description.before ?? description.after ?? "";
      return (
        <ValuePre
          text={text}
          op={description.before === undefined ? "add" : "del"}
          label={description.before === undefined ? afterLabel : beforeLabel}
        />
      );
    }
    const structural = description.structural;
    if (mode === "fields" && structural) {
      return <FieldDiff alias={model.alias} structural={structural} />;
    }
    return (
      <>
        {mode === "fields" ? (
          <span className="info-panel">
            {description.oversize
              ? "Too large to compare structurally."
              : "Stored value is not valid JSON on one side."}
          </span>
        ) : null}
        <JsonDiff
          before={description.before}
          after={description.after}
          beforeLabel={beforeLabel}
          afterLabel={afterLabel}
          contentType={description.invalid ? undefined : "json"}
          layout={layout}
          fold
          maxHeight={maxHeight}
        />
      </>
    );
  }
  if (description.kind === "string") {
    if (description.before === undefined || description.after === undefined) {
      const text = description.before ?? description.after ?? "";
      return (
        <ValuePre
          text={text}
          op={description.before === undefined ? "add" : "del"}
          label={description.before === undefined ? afterLabel : beforeLabel}
        />
      );
    }
    return (
      <JsonDiff
        before={description.before}
        after={description.after}
        beforeLabel={beforeLabel}
        afterLabel={afterLabel}
        layout={layout}
        fold
        maxHeight={maxHeight}
      />
    );
  }
  return null;
}
