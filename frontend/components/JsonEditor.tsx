import { AlignLeft, Check, Copy, Minimize2, WrapText } from "lucide-react";
import {
  type KeyboardEvent,
  useCallback,
  useDeferredValue,
  useEffect,
  useId,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import { JsonHighlight } from "@/components/JsonHighlight";
import { Button } from "@/components/ui/button";
import { assignRef } from "@/lib/forms";
import {
  checkJson,
  formatJson,
  HIGHLIGHT_DEFER_BYTES,
  HIGHLIGHT_MAX_BYTES,
  lineCount,
  minifyJson,
} from "@/lib/json-text";
import { cn } from "@/lib/utils";
import { byteLength, formatBytes } from "@/lib/validation";

export interface JsonEditorProps {
  value: string;
  onChange: (text: string) => void;
  /** These land on the `<textarea>`, so a wrapping `Field` labels the real control. */
  id?: string;
  "aria-label"?: string;
  "aria-describedby"?: string;
  "aria-invalid"?: boolean;
  "aria-required"?: boolean;
  name?: string;
  /** The `<textarea>`, e.g. for a modal's `initialFocus`. */
  inputRef?: React.Ref<HTMLElement>;
  disabled?: boolean;
  readOnly?: boolean;
  placeholder?: string;
  className?: string;
  /** Minimum height in lines. */
  rows?: number;
  /** CSS length; the body scrolls beyond it. */
  maxHeight?: string;
  /** `full`: Format, Minify, Wrap, Copy and a size readout. `minimal`: Format and the readout. */
  toolbar?: "full" | "minimal" | "none";
  onBlur?: () => void;
  /** Cmd/Ctrl+Enter. Defaults to submitting the enclosing form. */
  onSubmit?: () => void;
}

const INDENT = "  ";

/**
 * A JSON editor built from a transparent `<textarea>` stacked over a
 * highlighted copy of its text. Both share one grid cell and one scroll
 * container, so they can never drift apart; the textarea stays the only
 * focusable, labelled control. Typing is passed through untouched — only the
 * toolbar reformats.
 */
export function JsonEditor({
  value,
  onChange,
  id,
  "aria-label": ariaLabel,
  "aria-describedby": ariaDescribedBy,
  "aria-invalid": ariaInvalid,
  "aria-required": ariaRequired,
  name,
  disabled = false,
  readOnly = false,
  placeholder,
  className,
  rows = 12,
  maxHeight = "60vh",
  toolbar = "full",
  onBlur,
  onSubmit,
  inputRef,
}: JsonEditorProps) {
  const hintId = useId();
  const textareaRef = useRef<HTMLTextAreaElement | null>(null);
  const pendingCaret = useRef<number | null>(null);
  const [wrap, setWrap] = useState(true);
  const [copied, setCopied] = useState(false);
  const copyTimer = useRef<number | null>(null);
  useEffect(
    () => () => {
      if (copyTimer.current) window.clearTimeout(copyTimer.current);
    },
    [],
  );
  async function copy() {
    try {
      await navigator.clipboard.writeText(value);
      setCopied(true);
      if (copyTimer.current) window.clearTimeout(copyTimer.current);
      copyTimer.current = window.setTimeout(() => setCopied(false), 1500);
    } catch {
      // Clipboard access denied: the text is still selectable by hand.
    }
  }

  const bytes = useMemo(() => byteLength(value), [value]);
  const plain = bytes > HIGHLIGHT_MAX_BYTES;
  const deferred = useDeferredValue(value);
  const highlightText = bytes > HIGHLIGHT_DEFER_BYTES ? deferred : value;
  const problem = useMemo(() => (value.trim() === "" ? null : checkJson(value)), [value]);
  const lines = useMemo(() => lineCount(value), [value]);

  // Runs after every commit: a caret request is only ever queued alongside an
  // onChange, so the next render is the one that must place it.
  useLayoutEffect(() => {
    const caret = pendingCaret.current;
    if (caret === null || !textareaRef.current) return;
    pendingCaret.current = null;
    textareaRef.current.setSelectionRange(caret, caret);
  });

  const insertAtCaret = useCallback(
    (textarea: HTMLTextAreaElement, insert: string) => {
      const { selectionStart, selectionEnd } = textarea;
      // execCommand keeps native undo intact; happy-dom and some browsers lack it.
      if (typeof document.execCommand === "function") {
        textarea.focus();
        try {
          if (document.execCommand("insertText", false, insert)) return;
        } catch {
          // fall through to the manual path
        }
      }
      const next = value.slice(0, selectionStart) + insert + value.slice(selectionEnd);
      pendingCaret.current = selectionStart + insert.length;
      onChange(next);
    },
    [value, onChange],
  );

  function onKeyDown(event: KeyboardEvent<HTMLTextAreaElement>) {
    const textarea = event.currentTarget;
    if (event.key === "Enter" && (event.metaKey || event.ctrlKey)) {
      event.preventDefault();
      if (onSubmit) onSubmit();
      else textarea.form?.requestSubmit();
      return;
    }
    if (readOnly || disabled) return;
    if (
      event.key === "Tab" &&
      !event.shiftKey &&
      !event.altKey &&
      !event.ctrlKey &&
      !event.metaKey
    ) {
      event.preventDefault();
      insertAtCaret(textarea, INDENT);
      return;
    }
    if (event.key === "Enter" && !event.shiftKey && !event.altKey) {
      const before = value.slice(0, textarea.selectionStart);
      const lineStart = before.lastIndexOf("\n") + 1;
      const indent = /^[ \t]*/.exec(before.slice(lineStart))?.[0] ?? "";
      const opener = /[{[]\s*$/.test(before);
      event.preventDefault();
      insertAtCaret(textarea, `\n${indent}${opener ? INDENT : ""}`);
    }
  }

  function rewrite(transform: (text: string) => string | null) {
    const next = transform(value);
    if (next !== null && next !== value) {
      pendingCaret.current = 0;
      onChange(next);
    }
  }

  const invalid = ariaInvalid === true;
  const showToolbar = toolbar !== "none";
  const describedBy = [ariaDescribedBy, hintId].filter(Boolean).join(" ");

  return (
    <div
      className={cn("json-editor", className)}
      data-wrap={wrap ? "on" : "off"}
      data-disabled={disabled ? "true" : undefined}
      data-invalid={invalid ? "true" : undefined}
      data-plain={plain ? "true" : undefined}
    >
      {showToolbar ? (
        <div className="json-editor-toolbar">
          <div className="json-editor-tools">
            <Button
              type="button"
              variant="ghost"
              size="sm"
              disabled={disabled || readOnly || problem !== null || value.trim() === ""}
              onClick={() => rewrite(formatJson)}
            >
              <AlignLeft size={14} aria-hidden /> Format
            </Button>
            {toolbar === "full" ? (
              <>
                <Button
                  type="button"
                  variant="ghost"
                  size="sm"
                  disabled={disabled || readOnly || problem !== null || value.trim() === ""}
                  onClick={() => rewrite(minifyJson)}
                >
                  <Minimize2 size={14} aria-hidden /> Minify
                </Button>
                <Button
                  type="button"
                  variant="ghost"
                  size="sm"
                  aria-pressed={wrap}
                  onClick={() => setWrap((current) => !current)}
                >
                  <WrapText size={14} aria-hidden /> Wrap
                </Button>
                <Button
                  type="button"
                  variant="ghost"
                  size="sm"
                  aria-label={copied ? "Copied" : "Copy"}
                  onClick={() => void copy()}
                >
                  {copied ? <Check size={14} aria-hidden /> : <Copy size={14} aria-hidden />}
                  {copied ? "Copied" : "Copy"}
                </Button>
              </>
            ) : null}
          </div>
          <span className="json-editor-size faint text-xs">
            {lines} {lines === 1 ? "line" : "lines"} · {formatBytes(bytes)}
          </span>
        </div>
      ) : null}
      <div
        className="json-editor-body"
        style={{ "--json-rows": rows, maxHeight } as React.CSSProperties}
      >
        <div
          className="json-editor-stack"
          style={{ "--json-gutter-chars": String(String(lines).length) } as React.CSSProperties}
        >
          {plain ? null : (
            <pre className="json-editor-highlight" aria-hidden="true">
              <JsonHighlight
                text={highlightText}
                lineNumbers
                errorLine={problem?.line ?? null}
                trailingNewline
              />
            </pre>
          )}
          <textarea
            ref={(node) => {
              textareaRef.current = node;
              assignRef(inputRef, node);
            }}
            id={id}
            name={name}
            className="json-editor-input"
            value={value}
            disabled={disabled}
            readOnly={readOnly}
            placeholder={placeholder}
            spellCheck={false}
            autoComplete="off"
            autoCapitalize="off"
            autoCorrect="off"
            wrap={wrap ? "soft" : "off"}
            aria-label={ariaLabel}
            aria-describedby={describedBy || undefined}
            aria-invalid={ariaInvalid}
            aria-required={ariaRequired}
            onChange={(event) => onChange(event.target.value)}
            onKeyDown={onKeyDown}
            onBlur={onBlur}
          />
        </div>
      </div>
      <div className="json-editor-status" role="status">
        {problem ? (
          <span className="json-editor-problem">
            line {problem.line}, col {problem.column}: {problem.message}
          </span>
        ) : plain ? (
          <span className="faint">
            Highlighting is off above {formatBytes(HIGHLIGHT_MAX_BYTES)}.
          </span>
        ) : null}
      </div>
      <span id={hintId} className="sr-only">
        Tab inserts two spaces. Press Shift+Tab to move focus out.
      </span>
    </div>
  );
}
