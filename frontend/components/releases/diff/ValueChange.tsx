import { useEffect, useState } from "react";
import { Ident } from "@/components/Ident";
import { JsonDiff } from "@/components/JsonDiff";
import { JsonLine } from "@/components/JsonHighlight";
import { BindingModeBadge } from "@/components/secrets/SecretBadges";
import { Badge, SecretStateBadge } from "@/components/ui";
import { Button } from "@/components/ui/button";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { formatRelative, formatUnixMs } from "@/lib/format";
import { formatJson } from "@/lib/json-text";
import { type DiffRowModel, elide } from "@/lib/release-diff";
import type { ReleaseDiffPin } from "@/lib/types";
import { formatBytes } from "@/lib/validation";
import { formatValuePath, type ScalarChange, type ValueChangeDescription } from "@/lib/value-diff";
import { StructuralDiff } from "./StructuralDiff";

export type ValueViewMode = "structural" | "side";
const MODE_KEY = "kms-release-diff-mode";

/** The structural / side-by-side choice persists per browser; a broken store falls back to structural. */
export function useValueViewMode(): [ValueViewMode, (mode: ValueViewMode) => void] {
  const [mode, setMode] = useState<ValueViewMode>("structural");
  useEffect(() => {
    try {
      const stored = window.localStorage.getItem(MODE_KEY);
      if (stored === "side" || stored === "structural") setMode(stored);
    } catch {
      // Private mode or blocked storage: keep the default.
    }
  }, []);
  const update = (next: ValueViewMode) => {
    setMode(next);
    try {
      window.localStorage.setItem(MODE_KEY, next);
    } catch {
      // Same: the choice just does not persist.
    }
  };
  return [mode, update];
}

const Arrow = () => (
  <span className="release-diff-arrow" aria-hidden>
    →
  </span>
);
const Missing = () => <span className="release-diff-missing">—</span>;

function Token({ text, op, className }: { text: string; op: "del" | "add"; className?: string }) {
  return (
    <span
      className={`${op === "del" ? "release-diff-old" : "release-diff-new"} ${className ?? ""}`}
    >
      {text}
    </span>
  );
}

/** A short string with the shared ends dimmed and the differing span marked. */
function StringToken({
  text,
  op,
  common,
}: {
  text: string;
  op: "del" | "add";
  common: { prefix: number; suffix: number } | null;
}) {
  if (!common || (common.prefix === 0 && common.suffix === 0)) {
    return <Token text={JSON.stringify(text)} op={op} className="tok-string" />;
  }
  const head = text.slice(0, common.prefix);
  const mid = text.slice(common.prefix, text.length - common.suffix);
  const tail = text.slice(text.length - common.suffix);
  return (
    <span className={`${op === "del" ? "release-diff-old" : "release-diff-new"} tok-string`}>
      "<span className="release-diff-str-common">{head}</span>
      <span className="release-diff-str-diff">{mid}</span>
      <span className="release-diff-str-common">{tail}</span>"
    </span>
  );
}

function ScalarInline({ change }: { change: ScalarChange }) {
  if (change.kind === "binary") {
    const l = change.beforeBytes === undefined ? undefined : formatBytes(change.beforeBytes);
    const r = change.afterBytes === undefined ? undefined : formatBytes(change.afterBytes);
    return (
      <>
        {l ? <Token text={l} op="del" /> : <Missing />}
        <Arrow />
        {r ? <Token text={r} op="add" /> : <Missing />}
      </>
    );
  }
  const tokenClass =
    change.kind === "boolean" ? "tok-boolean" : change.kind === "number" ? "tok-number" : "";
  const before = change.before;
  const after = change.after;
  const side = (text: string | undefined, op: "del" | "add") => {
    if (text === undefined) return <Missing />;
    if (change.kind === "string") {
      return change.long ? (
        <Token text={elide(text.replace(/\s+/g, " "), 60)} op={op} className="tok-string" />
      ) : (
        <StringToken text={text} op={op} common={change.common} />
      );
    }
    return <Token text={text} op={op} className={tokenClass} />;
  };
  const delta =
    change.kind === "number" && change.delta
      ? `(${change.delta}${change.percent ? `, ${change.percent}` : ""})`
      : change.kind === "duration" && change.ratio
        ? `(${change.ratio})`
        : null;
  return (
    <>
      {side(before, "del")}
      <Arrow />
      {side(after, "add")}
      {delta ? <span className="release-diff-delta">{delta}</span> : null}
    </>
  );
}

function JsonInline({
  description,
}: {
  description: Extract<ValueChangeDescription, { kind: "json" }>;
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
  if (!structural) {
    return (
      <span className="faint">
        {description.oversize
          ? "Too large to compare structurally; open side-by-side"
          : description.invalid
            ? "Stored value is not valid JSON on one side"
            : "Changed"}
      </span>
    );
  }
  if (structural.changes.length === 0) return <span className="faint">No leaf differences</span>;
  const shown = structural.changes.slice(0, 2);
  const rest = structural.changes.length - shown.length;
  return (
    <>
      {shown.map((change, index) => (
        <span key={formatValuePath(change.path)} className="release-diff-leaf-values">
          {index > 0 ? <span className="faint">,</span> : null}
          <span className="tok-key">{formatValuePath(change.path) || "(root)"}</span>
          {change.before !== undefined ? <Token text={elide(change.before, 40)} op="del" /> : null}
          {change.before !== undefined && change.after !== undefined ? <Arrow /> : null}
          {change.after !== undefined ? <Token text={elide(change.after, 40)} op="add" /> : null}
        </span>
      ))}
      {rest > 0 ? (
        <span className="faint">
          , +{rest} more{structural.truncated ? "+" : ""}
        </span>
      ) : null}
    </>
  );
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
      <JsonInline description={description} />
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

/** The expanded body: structural leaves or side-by-side lines for JSON, a line diff for long strings, one value for one-sided rows. */
export function ValueChangeBody({
  model,
  beforeLabel,
  afterLabel,
  mode,
  onModeChange,
  compact,
}: {
  model: DiffRowModel;
  beforeLabel: string;
  afterLabel: string;
  mode: ValueViewMode;
  onModeChange: (mode: ValueViewMode) => void;
  compact?: boolean;
}) {
  const { description } = model;
  if (!description || description.kind === "binary") return null;
  const maxHeight = compact ? "40vh" : "60vh";
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
    const canStructural = structural !== null;
    const effective: ValueViewMode = canStructural ? mode : "side";
    return (
      <>
        <div className="release-diff-mode">
          {canStructural ? (
            <Tabs
              value={effective}
              onValueChange={(value) => onModeChange(value === "side" ? "side" : "structural")}
            >
              <TabsList variant="line" aria-label="Value comparison mode">
                <TabsTrigger value="structural">Structural</TabsTrigger>
                <TabsTrigger value="side">
                  {/* The ≤768px tab reset allows wrapping; the label breaks at its
                      hyphens in a 360px row body without its own nowrap. */}
                  <span className="whitespace-nowrap">Side-by-side</span>
                </TabsTrigger>
              </TabsList>
            </Tabs>
          ) : (
            <span className="info-panel">
              {description.oversize
                ? "Too large to compare structurally."
                : "Stored value is not valid JSON on one side."}
            </span>
          )}
        </div>
        {effective === "structural" && structural ? (
          <StructuralDiff structural={structural} />
        ) : (
          <JsonDiff
            before={description.before}
            after={description.after}
            beforeLabel={beforeLabel}
            afterLabel={afterLabel}
            contentType={description.invalid ? undefined : "json"}
            fold
            maxHeight={maxHeight}
          />
        )}
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
        fold
        maxHeight={maxHeight}
      />
    );
  }
  return null;
}
