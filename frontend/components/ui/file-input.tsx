import { Upload } from "lucide-react";
import { type DragEvent, type ReactNode, type Ref, useId, useRef, useState } from "react";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";

export interface FileInputProps {
  /** Lands on the real `<input type="file">`, so a wrapping `Field` labels it. */
  id?: string;
  accept?: string;
  disabled?: boolean;
  /**
   * The chosen file's name. Pass it to control the readout (a caller that
   * clears its selection also clears this); left undefined, the last picked
   * file's name is shown.
   */
  fileName?: string;
  /** Called with the picked or dropped file; `undefined` when the picker was cancelled. */
  onFile: (file: File | undefined) => void;
  buttonLabel?: string;
  /** The visible "Choose…" button, e.g. for a modal's `initialFocus`. */
  buttonRef?: Ref<HTMLButtonElement>;
  /** Shown while no file is chosen; defaults to a drop hint. */
  placeholder?: ReactNode;
  className?: string;
  "aria-label"?: string;
  "aria-describedby"?: string;
  "aria-invalid"?: boolean;
  "aria-required"?: boolean;
}

/**
 * A styled file picker: a button that opens the native dialog, the chosen
 * file's name beside it, and the whole control doubles as a drop target. The
 * native input stays in the DOM (visually hidden) as the labelled, focusable
 * control, so `Field` and tests address it like any other input.
 */
export function FileInput({
  id,
  accept,
  disabled = false,
  fileName,
  onFile,
  buttonLabel = "Choose file…",
  buttonRef,
  placeholder = "No file chosen — or drop one here",
  className,
  "aria-label": ariaLabel,
  "aria-describedby": ariaDescribedBy,
  "aria-invalid": ariaInvalid,
  "aria-required": ariaRequired,
}: FileInputProps) {
  const inputRef = useRef<HTMLInputElement>(null);
  const generatedId = useId();
  const inputId = id ?? `${generatedId}-file`;
  const nameId = `${generatedId}-name`;
  const [dragging, setDragging] = useState(false);
  const [pickedName, setPickedName] = useState("");
  const shownName = fileName ?? pickedName;

  function pick(file: File | undefined) {
    setPickedName(file?.name ?? "");
    onFile(file);
  }

  function onDrop(event: DragEvent<HTMLDivElement>) {
    event.preventDefault();
    setDragging(false);
    if (disabled) return;
    const file = event.dataTransfer?.files?.[0];
    if (file) pick(file);
  }

  return (
    // biome-ignore lint/a11y/noStaticElementInteractions: the drop target only augments the button and input inside it.
    <div
      className={cn(
        "flex min-h-(--control-h) flex-wrap items-center gap-3 rounded-md border border-dashed border-input px-3 py-2 transition-colors",
        dragging && !disabled && "border-ring bg-muted ring-3 ring-ring/40",
        disabled && "opacity-50",
        className,
      )}
      data-slot="file-input"
      data-dragging={dragging || undefined}
      onDragOver={(event) => {
        event.preventDefault();
        if (!disabled && !dragging) setDragging(true);
      }}
      onDragLeave={() => setDragging(false)}
      onDrop={onDrop}
    >
      <input
        ref={inputRef}
        id={inputId}
        type="file"
        className="sr-only"
        accept={accept}
        disabled={disabled}
        aria-label={ariaLabel}
        aria-describedby={
          [ariaDescribedBy, shownName ? nameId : null].filter(Boolean).join(" ") || undefined
        }
        aria-invalid={ariaInvalid}
        required={ariaRequired || undefined}
        onChange={(event) => {
          pick(event.currentTarget.files?.[0]);
          // Let the same file be picked again after the caller cleared it.
          event.currentTarget.value = "";
        }}
      />
      <Button
        ref={buttonRef}
        type="button"
        variant="outline"
        size="sm"
        disabled={disabled}
        onClick={() => inputRef.current?.click()}
      >
        <Upload size={14} aria-hidden />
        {buttonLabel}
      </Button>
      <span id={nameId} className={cn("min-w-0 flex-1 truncate text-sm", !shownName && "faint")}>
        {shownName || placeholder}
      </span>
    </div>
  );
}
