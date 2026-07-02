import { useCallback, useEffect, useRef, useState } from "react";

interface CopyButtonProps {
  // A getter so the caller controls exactly what is copied; sensitive values
  // are passed lazily and never rendered here.
  value: string | (() => string);
  label?: string;
  className?: string;
}

export default function CopyButton({ value, label = "Copy", className }: CopyButtonProps) {
  const [copied, setCopied] = useState(false);
  const timer = useRef<number | null>(null);

  useEffect(
    () => () => {
      if (timer.current) window.clearTimeout(timer.current);
    },
    [],
  );

  const onCopy = useCallback(async () => {
    const text = typeof value === "function" ? value() : value;
    try {
      if (navigator.clipboard?.writeText) {
        await navigator.clipboard.writeText(text);
      } else {
        const ta = document.createElement("textarea");
        ta.value = text;
        ta.style.position = "fixed";
        ta.style.opacity = "0";
        document.body.appendChild(ta);
        ta.select();
        document.execCommand("copy");
        document.body.removeChild(ta);
      }
      setCopied(true);
      if (timer.current) window.clearTimeout(timer.current);
      timer.current = window.setTimeout(() => setCopied(false), 1800);
    } catch {
      // Clipboard blocked; leave the button state unchanged rather than
      // surfacing the (potentially sensitive) value anywhere.
      setCopied(false);
    }
  }, [value]);

  return (
    <button type="button" className={`btn btn-sm ${className ?? ""}`} onClick={onCopy}>
      {copied ? "Copied" : label}
    </button>
  );
}
