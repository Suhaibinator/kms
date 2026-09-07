import { ChevronsDownUp, ChevronsUpDown, WrapText } from "lucide-react";
import { useMemo, useState } from "react";
import CopyButton from "@/components/CopyButton";
import { JsonHighlight } from "@/components/JsonHighlight";
import { JsonTree, useJsonTree } from "@/components/JsonTree";
import { Button } from "@/components/ui/button";
import { checkJson, formatJson, lineCount, overHighlightCap } from "@/lib/json-text";
import { cn } from "@/lib/utils";
import { byteLength, formatBytes } from "@/lib/validation";

export interface JsonViewProps {
  raw: string;
  /** Colour the text when it is well-formed JSON. Off for values that are not JSON. */
  highlight?: boolean;
  /** Number the lines. */
  lineNumbers?: boolean;
  /** Start with soft wrapping on. */
  wrap?: boolean;
  /**
   * Let the operator fold objects and arrays. On by default, and used whenever
   * the text is well-formed JSON under the highlight cap; anything else falls
   * back to the plain highlighted block.
   */
  collapsible?: boolean;
  /** Offer a copy button in the toolbar; pass a label to name what is copied. */
  copyLabel?: string | false;
  /** What the copy button puts on the clipboard; defaults to `raw`. */
  copyValue?: string;
  /** Extra toolbar content, rendered before the readout. */
  tools?: React.ReactNode;
  /** CSS length; the block scrolls beyond it. */
  maxHeight?: string;
  className?: string;
}

/**
 * A read-only text block for JSON and other values: line numbers, a wrap
 * toggle, a copy button and a `lines · bytes` readout, colour-coded when the
 * text is well-formed JSON and foldable node by node.
 */
export function JsonView({
  raw,
  highlight = true,
  lineNumbers = true,
  wrap: initialWrap = true,
  collapsible = true,
  copyLabel = "Copy",
  copyValue,
  tools,
  maxHeight,
  className,
}: JsonViewProps) {
  const [wrap, setWrap] = useState(initialWrap);
  const overCap = overHighlightCap(raw);
  const coloured = highlight && !overCap && checkJson(raw) === null;
  const tree = useJsonTree(raw, collapsible && coloured);
  const lines = useMemo(() => lineCount(raw), [raw]);
  const bytes = useMemo(() => byteLength(raw), [raw]);
  let body: React.ReactNode = raw;
  if (!overCap) {
    body = tree.tree ? (
      <JsonTree state={tree} lineNumbers={lineNumbers} />
    ) : (
      <JsonHighlight text={raw} lineNumbers={lineNumbers} plain={!coloured} />
    );
  }
  return (
    <div className={cn("json-view", className)} data-wrap={wrap ? "on" : "off"}>
      <div className="json-view-toolbar">
        {/* One variant across the row: `ghost`, which reads better than four
            boxed buttons directly above a code block. `CopyButton` defaults to
            `outline` and opts in here. */}
        <div className="json-view-tools">
          {tools}
          {tree.tree ? (
            <>
              <Button
                type="button"
                variant="ghost"
                size="sm"
                disabled={!tree.canExpand}
                onClick={tree.expandAll}
              >
                <ChevronsUpDown size={14} aria-hidden /> Expand all
              </Button>
              <Button
                type="button"
                variant="ghost"
                size="sm"
                disabled={!tree.canCollapse}
                onClick={tree.collapseAll}
              >
                <ChevronsDownUp size={14} aria-hidden /> Collapse all
              </Button>
            </>
          ) : null}
          <Button
            type="button"
            variant="ghost"
            size="sm"
            aria-pressed={wrap}
            onClick={() => setWrap((current) => !current)}
          >
            <WrapText size={14} aria-hidden /> Wrap
          </Button>
          {copyLabel !== false ? (
            <CopyButton variant="ghost" label={copyLabel} value={copyValue ?? raw} />
          ) : null}
        </div>
        <span className="json-view-size faint text-xs">
          {lines} {lines === 1 ? "line" : "lines"} · {formatBytes(bytes)}
        </span>
      </div>
      <pre className="json-block json-view-body" style={maxHeight ? { maxHeight } : undefined}>
        {body}
      </pre>
    </div>
  );
}

/**
 * A parameter value of any content type: JSON is pretty-printed and coloured,
 * everything else is shown verbatim with the same line numbers, wrap toggle,
 * copy button and size readout.
 */
export function ValueView({
  value,
  contentType,
  ...rest
}: Omit<JsonViewProps, "raw" | "highlight"> & { value: string; contentType: string }) {
  const json = contentType === "json";
  const raw = json ? (formatJson(value) ?? value) : value;
  // The clipboard gets the value as stored, not the pretty-printed display.
  return <JsonView raw={raw} highlight={json} copyValue={value} {...rest} />;
}
