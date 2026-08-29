import { memo, useMemo } from "react";
import { overHighlightCap, tokenizeJson } from "@/lib/json-text";
import { cn } from "@/lib/utils";

export interface JsonHighlightProps {
  text: string;
  /** Number every logical line (CSS counters; aligned to the top of wrapped lines). */
  lineNumbers?: boolean;
  /** 1-based line to mark as the one holding a parse problem. */
  errorLine?: number | null;
  /**
   * Emit a line break after the last line too. The editor needs it so an empty
   * last row is as tall as the textarea's; read-only views leave it off so
   * `textContent` equals `text`.
   */
  trailingNewline?: boolean;
  className?: string;
}

const JsonLine = memo(function JsonLine({
  text,
  error,
  newline,
}: {
  text: string;
  error: boolean;
  newline: boolean;
}) {
  // Strings never span a line break, so each line tokenizes on its own and a
  // keystroke re-renders only the line it touched.
  const tokens = tokenizeJson(text);
  return (
    <span className={cn("json-line", error && "is-error")}>
      {tokens.map((token) => {
        const slice = text.slice(token.start, token.end);
        return token.kind === "ws" ? (
          slice
        ) : (
          <span key={token.start} className={`tok-${token.kind}`}>
            {slice}
          </span>
        );
      })}
      {newline ? "\n" : null}
    </span>
  );
});

/**
 * Colour-coded JSON (or JSON-ish) text. Tolerant of malformed input — it
 * renders whatever is there — and inert for assistive technology when used as
 * an editor overlay (the caller sets `aria-hidden`).
 */
export function JsonHighlight({
  text,
  lineNumbers = false,
  errorLine = null,
  trailingNewline = false,
  className,
}: JsonHighlightProps) {
  const lines = useMemo(() => text.split("\n"), [text]);
  const plain = overHighlightCap(text);
  const gutterChars = String(lines.length).length;
  const style = lineNumbers
    ? ({ "--json-gutter-chars": String(gutterChars) } as React.CSSProperties)
    : undefined;
  if (plain) {
    return (
      <span className={cn("json-highlight", className)} data-plain="true">
        {text}
        {trailingNewline ? "\n" : null}
      </span>
    );
  }
  return (
    <span
      className={cn("json-highlight", className)}
      data-line-numbers={lineNumbers ? "true" : undefined}
      style={style}
    >
      {lines.map((line, index) => (
        <JsonLine
          // Lines have no identity beyond their position.
          key={index}
          text={line}
          error={errorLine === index + 1}
          newline={index < lines.length - 1 || trailingNewline}
        />
      ))}
    </span>
  );
}
