import { Check, Copy } from "lucide-react";
import { useCallback, useEffect, useRef, useState } from "react";
import { Button } from "@/components/ui/button";
import { useToast } from "@/context/ToastContext";

interface CopyButtonProps {
  // A getter so the caller controls exactly what is copied; sensitive values
  // are passed lazily and never rendered here.
  value: string | (() => string);
  label?: string;
  /** `icon-sm` hides the label (it stays the accessible name) for rows too
   *  narrow to spend ~90px on a word every reader already knows. */
  size?: "sm" | "icon-sm";
  /** A toolbar wears one variant. `ghost` is for the tool rows above a code
   *  block, where the other buttons are unboxed and a lone outlined Copy reads
   *  as a different kind of control. */
  variant?: "outline" | "ghost";
  className?: string;
  disabled?: boolean;
}

export default function CopyButton({
  value,
  label = "Copy",
  size = "sm",
  variant = "outline",
  className,
  disabled,
}: CopyButtonProps) {
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
    <Button
      type="button"
      variant={variant}
      size={size}
      className={className}
      disabled={disabled}
      title={size === "icon-sm" ? label : undefined}
      onClick={onCopy}
    >
      {copied ? <Check aria-hidden /> : <Copy aria-hidden />}
      {/* The label never changes, so the button never changes width; the icon
          swap and the live region carry the confirmation. Icon-only keeps the
          same accessible name, visually hidden. */}
      {size === "icon-sm" ? <span className="sr-only">{label}</span> : label}
      <span className="sr-only" aria-live="polite">
        {copied ? "Copied to clipboard" : ""}
      </span>
    </Button>
  );
}
