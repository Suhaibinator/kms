// The inline value tokens the release diff shares between a row's head
// (ValueChange.tsx) and its field list (FieldDiff.tsx): old/new tints, the
// arrow, the missing-side dash and the typed scalar line.

import { elide } from "@/lib/release-diff";
import { formatBytes } from "@/lib/validation";
import type { ScalarChange } from "@/lib/value-diff";

export const Arrow = () => (
  <span className="release-diff-arrow" aria-hidden>
    →
  </span>
);

export const Missing = () => <span className="release-diff-missing">—</span>;

export function Token({
  text,
  op,
  className,
}: {
  text: string;
  op: "del" | "add";
  className?: string;
}) {
  return (
    <span
      className={`${op === "del" ? "release-diff-old" : "release-diff-new"} ${className ?? ""}`}
    >
      {text}
    </span>
  );
}

/** A short string with the shared ends dimmed and the differing span marked. */
export function StringToken({
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

/** `old → new (delta)` for a typed scalar; a missing side is a dash. */
export function ScalarInline({ change }: { change: ScalarChange }) {
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
