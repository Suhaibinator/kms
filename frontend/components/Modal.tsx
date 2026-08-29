import type { Dialog as DialogPrimitive } from "@base-ui/react/dialog";
import { type ReactNode, useCallback, useEffect, useId, useRef, useState } from "react";
import { Field } from "@/components/ui";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { cn } from "@/lib/utils";

export function Modal({
  open,
  title,
  description,
  onClose,
  children,
  footer,
  wide,
  workspace,
  dismissible = true,
  dirty = false,
  initialFocus,
}: {
  open: boolean;
  title: ReactNode;
  /** One sentence under the title; also the dialog's accessible description. */
  description?: ReactNode;
  onClose: () => void;
  children: ReactNode;
  /**
   * Footer content. The function form receives `close`, which honours `dirty`
   * — use it for Cancel buttons so they ask before discarding edits.
   */
  footer?: ReactNode | ((close: () => void) => ReactNode);
  wide?: boolean;
  /** Use the available viewport for data-heavy editors and inspectors. */
  workspace?: boolean;
  /**
   * Whether Escape, the backdrop, and the header close button may dismiss.
   * Pass `dismissible={!busy}` whenever the modal performs an async action, so
   * a half-finished save cannot be dismissed out from under itself.
   */
  dismissible?: boolean;
  /**
   * The form holds unsaved edits: every dismissal (Escape, backdrop, the
   * header close button, the footer's `close`) first asks "Discard changes?".
   */
  dirty?: boolean;
  /** Element focused when the dialog opens; defaults to Base UI's own choice. */
  initialFocus?: DialogPrimitive.Popup.Props["initialFocus"];
}) {
  const [confirmingDiscard, setConfirmingDiscard] = useState(false);
  useEffect(() => {
    if (!open) setConfirmingDiscard(false);
  }, [open]);

  const requestClose = useCallback(() => {
    if (dirty) setConfirmingDiscard(true);
    else onClose();
  }, [dirty, onClose]);

  return (
    <Dialog
      open={open}
      disablePointerDismissal={!dismissible}
      onOpenChange={(nextOpen) => {
        if (!nextOpen && dismissible) requestClose();
      }}
    >
      <DialogContent
        showCloseButton={dismissible}
        initialFocus={initialFocus}
        className={cn(
          // Header and footer are pinned rows; only the middle row scrolls.
          // Without this the action buttons scroll off the bottom of a long form.
          "grid max-h-[calc(100dvh-2rem)] grid-rows-[auto_minmax(0,1fr)_auto] gap-0 overflow-hidden p-0 sm:max-w-[560px]",
          wide && "sm:max-w-[720px]",
          // Workspace modals only change their footprint; the row layout is shared.
          workspace && "h-[calc(100dvh-2rem)] sm:max-w-[min(1200px,calc(100vw-2rem))]",
        )}
      >
        <DialogHeader className="border-b border-border px-5 py-4 pr-14">
          <DialogTitle>{title}</DialogTitle>
          {description ? <DialogDescription>{description}</DialogDescription> : null}
        </DialogHeader>
        <div data-modal-body className="min-h-0 overflow-y-auto p-5">
          {children}
        </div>
        {footer ? (
          <DialogFooter className="m-0 rounded-none px-5 py-4">
            {typeof footer === "function" ? footer(requestClose) : footer}
          </DialogFooter>
        ) : null}
        {/* Only mounted for dirty modals: the confirm is itself a Modal, so an
            unconditional render would recurse without end. Nested inside the
            popup so Base UI stacks it and returns focus to this dialog. */}
        {dirty || confirmingDiscard ? (
          <ConfirmDialog
            open={confirmingDiscard}
            title="Discard changes?"
            message="Your edits in this dialog have not been saved."
            danger
            confirmLabel="Discard"
            cancelLabel="Keep editing"
            onConfirm={() => {
              setConfirmingDiscard(false);
              onClose();
            }}
            onCancel={() => setConfirmingDiscard(false)}
          />
        ) : null}
      </DialogContent>
    </Dialog>
  );
}

export function ConfirmDialog({
  open,
  title,
  message,
  confirmLabel = "Confirm",
  cancelLabel = "Cancel",
  danger,
  busy,
  requireText,
  onConfirm,
  onCancel,
}: {
  open: boolean;
  title: ReactNode;
  message: ReactNode;
  confirmLabel?: string;
  cancelLabel?: string;
  danger?: boolean;
  busy?: boolean;
  /** Exact text the user must enter before an irreversible action is enabled. */
  requireText?: string;
  onConfirm: () => void;
  onCancel: () => void;
}) {
  const [typed, setTyped] = useState("");
  // The confirm button lives in the footer row, outside the form element; the
  // HTML `form` attribute is what still makes Enter in the body submit it.
  const formId = useId();
  const inputRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    if (open) setTyped("");
  }, [open]);

  const confirmDisabled = busy || (!!requireText && typed !== requireText);

  return (
    <Modal
      open={open}
      title={title}
      onClose={busy ? () => undefined : onCancel}
      dismissible={!busy}
      initialFocus={requireText ? inputRef : undefined}
      footer={
        <>
          <Button type="button" variant="outline" onClick={onCancel} disabled={busy}>
            {cancelLabel}
          </Button>
          <Button
            form={formId}
            type="submit"
            variant={danger ? "destructive-solid" : "default"}
            loading={busy}
            disabled={confirmDisabled}
          >
            {confirmLabel}
          </Button>
        </>
      }
    >
      <form
        id={formId}
        onSubmit={(event) => {
          event.preventDefault();
          if (!confirmDisabled) onConfirm();
        }}
      >
        <div className={danger ? "danger-panel" : "info-panel"}>{message}</div>
        {requireText ? (
          <Field
            label={
              <>
                Type <span className="mono">{requireText}</span> to confirm
              </>
            }
            className="mt-4"
          >
            <Input
              ref={inputRef}
              className="font-mono"
              value={typed}
              autoComplete="off"
              spellCheck={false}
              onChange={(event) => setTyped(event.target.value)}
            />
          </Field>
        ) : null}
      </form>
    </Modal>
  );
}
