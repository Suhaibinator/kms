import { useCallback, useEffect, useRef, useState } from "react";
import { useToast } from "@/context/ToastContext";

interface CopyButtonProps {
  // A getter so the caller controls exactly what is copied; sensitive values
  // are passed lazily and never rendered here.
  value: string | (() => string);
  label?: string;
  className?: string;
}

export default function CopyButton({ value, label = "Copy", className }: CopyButtonProps) {
  const toast = useToast();
  const [copied, setCopied] = useState(false);
  const timer = useRef<number | null>(null);

  useEffect(
    () => () => {
      if (timer.current) window.clearTimeout(timer.current);
    },
    [],
  );

  const onCopy = useCallback(async () => {
    try {
      const text = typeof value === "function" ? value() : value;
      if (navigator.clipboard?.writeText) {
        await navigator.clipboard.writeText(text);
      } else {
        const ta = document.createElement("textarea");
        ta.value = text;
        ta.style.position = "fixed";
        ta.style.opacity = "0";
        document.body.appendChild(ta);
        let copied = false;
        try {
          ta.select();
          copied = document.execCommand("copy");
        } finally {
          ta.remove();
        }
        if (!copied) throw new Error("Clipboard command was rejected");
      }
      setCopied(true);
      if (timer.current) window.clearTimeout(timer.current);
      timer.current = window.setTimeout(() => setCopied(false), 1800);
    } catch {
      // Clipboard blocked; leave the button state unchanged rather than
      // surfacing the (potentially sensitive) value anywhere.
      setCopied(false);
      toast.error(
        new Error("Your browser blocked clipboard access. Try again or select the text manually."),
        "Copy failed",
      );
    }
  }, [toast, value]);

  return (
    <button type="button" className={`btn btn-sm ${className ?? ""}`} onClick={onCopy}>
      {copied ? "Copied" : label}
      <span className="sr-only" aria-live="polite">
        {copied ? "Copied to clipboard" : ""}
      </span>
    </button>
  );
}
