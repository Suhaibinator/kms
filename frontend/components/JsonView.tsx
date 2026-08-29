import { WrapText } from "lucide-react";
import { useMemo, useState } from "react";
import CopyButton from "@/components/CopyButton";
import { JsonHighlight } from "@/components/JsonHighlight";
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
 * text is well-formed JSON.
 */
export function JsonView({
  raw,
  highlight = true,
  lineNumbers = true,
  wrap: initialWrap = true,
  copyLabel = "Copy",
  copyValue,
  tools,
  maxHeight,
  className,
}: JsonViewProps) {
  const [wrap, setWrap] = useState(initialWrap);
  const overCap = overHighlightCap(raw);
  const coloured = highlight && !overCap && checkJson(raw) === null;
  const lines = useMemo(() => lineCount(raw), [raw]);
  const bytes = useMemo(() => byteLength(raw), [raw]);
  return (
    <div className={cn("json-view", className)} data-wrap={wrap ? "on" : "off"}>
      <div className="json-view-toolbar">
        <div className="json-view-tools">
          {tools}
          <Button
            type="button"
            variant="ghost"
            size="sm"
            aria-pressed={wrap}
            onClick={() => setWrap((current) => !current)}
          >
            <WrapText size={14} aria-hidden /> Wrap
          </Button>
          {copyLabel !== false ? <CopyButton label={copyLabel} value={copyValue ?? raw} /> : null}
        </div>
        <span className="json-view-size faint text-xs">
          {lines} {lines === 1 ? "line" : "lines"} · {formatBytes(bytes)}
        </span>
      </div>
      <pre className="json-block json-view-body" style={maxHeight ? { maxHeight } : undefined}>
        {overCap ? raw : <JsonHighlight text={raw} lineNumbers={lineNumbers} plain={!coloured} />}
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
