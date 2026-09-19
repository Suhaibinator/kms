import { useMemo, useState } from "react";
import { JsonLine } from "@/components/JsonHighlight";
import { countNoun } from "@/lib/format";
import { formatJson, tokenizeJson } from "@/lib/json-text";
import {
  describeLeafChange,
  formatValuePath,
  isSubtreeText,
  type StructuralDiff,
  type ValueChange,
} from "@/lib/value-diff";
import { Arrow, ScalarInline, Token } from "./tokens";

/** Fields listed before "Show all"; a subtree counts as one. */
const FIELD_CAP = 12;

const GLYPH: Record<ValueChange["kind"], string> = {
  added: "+",
  removed: "−",
  changed: "~",
  moved: "↷",
};

/** One field of the list, with its subtree text already pretty-printed and split. */
interface FieldEntry {
  change: ValueChange;
  key: string;
  /** Present when either side is an object or array: the lines of each side. */
  block?: { before: string[]; after: string[] };
}

function blockLines(text: string | undefined): string[] {
  if (text === undefined) return [];
  return (isSubtreeText(text) ? (formatJson(text) ?? text) : text).split("\n");
}

function entryOf(change: ValueChange): FieldEntry {
  const path = formatValuePath(change.path);
  const key =
    change.kind === "moved"
      ? `moved:${formatValuePath(change.fromPath ?? [])}>${path}`
      : `${change.kind}:${path}`;
  const subtree =
    (change.before !== undefined && isSubtreeText(change.before)) ||
    (change.after !== undefined && isSubtreeText(change.after));
  return subtree
    ? { change, key, block: { before: blockLines(change.before), after: blockLines(change.after) } }
    : { change, key };
}

/** `tok-string` / `tok-number` / … for a single raw JSON token, "" otherwise. */
function tokenClass(text: string): string {
  const tokens = tokenizeJson(text.trim()).filter((token) => token.kind !== "ws");
  return tokens.length === 1 ? `tok-${tokens[0].kind}` : "";
}

/**
 * The field changes of a JSON value, one line each: a gutter glyph, the
 * path, the value(s). Leaves show a typed change (`50 → 5 (−45, −90 %)`),
 * subtrees print pretty with one glyph per line, moves name both paths.
 * Twelve fields, then "Show all"; unchanged fields are a count below.
 */
export function FieldDiff({ alias, structural }: { alias: string; structural: StructuralDiff }) {
  const entries = useMemo(() => structural.changes.map(entryOf), [structural]);
  const [showAll, setShowAll] = useState(false);
  const total = entries.length;
  const shown = showAll ? entries : entries.slice(0, FIELD_CAP);
  const hidden = structural.unchangedLeaves;
  return (
    <div className="release-diff-fields" data-testid="release-diff-fields">
      <ol className="release-diff-field-list" aria-label={`Field changes in ${alias}`}>
        {shown.map((entry) => (
          <FieldLine key={entry.key} entry={entry} />
        ))}
      </ol>
      {total > shown.length ? (
        <button type="button" className="release-diff-fold-button" onClick={() => setShowAll(true)}>
          Show all {total} fields
        </button>
      ) : null}
      {hidden > 0 ? (
        <p className="release-diff-fold">
          {hidden} unchanged {countNoun(hidden, "fields")} not listed
        </p>
      ) : null}
      {structural.truncated ? (
        <p className="release-diff-fold">
          Listing stopped after {total} fields; Unified shows the rest.
        </p>
      ) : null}
    </div>
  );
}

function Gutter({ glyph }: { glyph: string }) {
  return (
    <span className="release-diff-field-gutter" aria-hidden>
      {glyph}
    </span>
  );
}

/** The pretty lines of one side, each with its own gutter glyph so column 1 reads down the block. */
function CodeBlock({
  lines,
  op,
  glyph,
  side,
}: {
  lines: string[];
  op?: "add" | "del";
  glyph: string;
  side: string;
}) {
  // Lines repeat within a side; the position is the identity here.
  return lines.map((line, index) => (
    <FragmentLine key={`${side}:${index}`} line={line} op={op} glyph={glyph} />
  ));
}

function FragmentLine({ line, op, glyph }: { line: string; op?: "add" | "del"; glyph: string }) {
  return (
    <>
      <Gutter glyph={glyph} />
      <span className="release-diff-field-code" data-op={op}>
        <JsonLine text={line} />
      </span>
    </>
  );
}

function FieldLine({ entry }: { entry: FieldEntry }) {
  const { change, block } = entry;
  const glyph = GLYPH[change.kind];
  const path = formatValuePath(change.path) || "(root)";
  const from = change.kind === "moved" ? formatValuePath(change.fromPath ?? []) || "(root)" : null;
  return (
    <li
      className="release-diff-field"
      data-change={change.kind}
      data-subtree={block ? "true" : undefined}
    >
      {block ? null : <Gutter glyph={glyph} />}
      <span className="sr-only">{change.kind}</span>
      <span className="release-diff-field-path">
        {from !== null ? (
          <>
            {from} <Arrow /> {path}
          </>
        ) : (
          path
        )}
      </span>
      {block ? (
        <SubtreeLines change={change} block={block} glyph={glyph} />
      ) : (
        <span className="release-diff-field-values">
          <LeafValues change={change} />
        </span>
      )}
    </li>
  );
}

function SubtreeLines({
  change,
  block,
  glyph,
}: {
  change: ValueChange;
  block: { before: string[]; after: string[] };
  glyph: string;
}) {
  if (change.kind === "moved") {
    return <CodeBlock lines={block.after} glyph={glyph} side="moved" />;
  }
  if (change.kind === "added") {
    return <CodeBlock lines={block.after} op="add" glyph={glyph} side="after" />;
  }
  if (change.kind === "removed") {
    return <CodeBlock lines={block.before} op="del" glyph={glyph} side="before" />;
  }
  // A kind mismatch (`{…}` → `[…]`, or a scalar became an object): the old
  // block with `−` gutters, then the new one with `+`.
  return (
    <>
      <CodeBlock lines={block.before} op="del" glyph={GLYPH.removed} side="before" />
      <CodeBlock lines={block.after} op="add" glyph={GLYPH.added} side="after" />
    </>
  );
}

function LeafValues({ change }: { change: ValueChange }) {
  if (change.kind === "moved") {
    const text = change.after ?? change.before ?? "";
    return <span className={`release-diff-moved ${tokenClass(text)}`}>{text}</span>;
  }
  if (change.kind === "added") {
    const text = change.after ?? "";
    return <Token text={text} op="add" className={tokenClass(text)} />;
  }
  if (change.kind === "removed") {
    const text = change.before ?? "";
    return <Token text={text} op="del" className={tokenClass(text)} />;
  }
  const leaf = describeLeafChange(change.before, change.after);
  if (leaf.kind === "duration") {
    // A JSON string that parses as a Go duration: keep it quoted like the
    // JSON it is (`ScalarInline` prints a string *parameter* bare) and add
    // the ratio.
    return (
      <>
        <Token text={JSON.stringify(leaf.before ?? "")} op="del" className="tok-string" />
        <Arrow />
        <Token text={JSON.stringify(leaf.after ?? "")} op="add" className="tok-string" />
        {leaf.ratio ? <span className="release-diff-delta">({leaf.ratio})</span> : null}
      </>
    );
  }
  if (
    leaf.kind === "boolean" ||
    leaf.kind === "number" ||
    leaf.kind === "string" ||
    leaf.kind === "binary"
  ) {
    return <ScalarInline change={leaf} />;
  }
  // `null` on a side, or two JSON kinds (`"30"` → `30`): two plain tokens.
  const before = change.before ?? "";
  const after = change.after ?? "";
  return (
    <>
      <Token text={before} op="del" className={tokenClass(before)} />
      <Arrow />
      <Token text={after} op="add" className={tokenClass(after)} />
    </>
  );
}
