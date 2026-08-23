import type { Dialog as DialogPrimitive } from "@base-ui/react/dialog";
import { type ReactNode, useEffect, useId, useState } from "react";
import { Field } from "@/components/ui";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Spinner } from "@/components/ui/spinner";
import { cn } from "@/lib/utils";

export function Modal({
  open,
  title,
  onClose,
  children,
  footer,
  wide,
  workspace,
  dismissible = true,
  initialFocus,
}: {
  open: boolean;
  title: ReactNode;
  onClose: () => void;
  children: ReactNode;
  footer?: ReactNode;
  wide?: boolean;
  /** Use the available viewport for data-heavy editors and inspectors. */
  workspace?: boolean;
  /**
   * Whether Escape, the backdrop, and the header close button may dismiss.
   * Pass `dismissible={!busy}` whenever the modal performs an async action, so
   * a half-finished save cannot be dismissed out from under itself.
   */
  dismissible?: boolean;
  /** Element focused when the dialog opens; defaults to Base UI's own choice. */
  initialFocus?: DialogPrimitive.Popup.Props["initialFocus"];
}) {
  return (
    <Dialog
      open={open}
      disablePointerDismissal={!dismissible}
      onOpenChange={(nextOpen) => {
        if (!nextOpen && dismissible) onClose();
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
        </DialogHeader>
        <div data-modal-body className="min-h-0 overflow-y-auto p-5">
          {children}
        </div>
        {footer ? (
          <DialogFooter className="m-0 rounded-none px-5 py-4">{footer}</DialogFooter>
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
      footer={
        <>
          <Button type="button" variant="outline" onClick={onCancel} disabled={busy}>
            {cancelLabel}
          </Button>
          <Button
            form={formId}
            type="submit"
            variant={danger ? "destructive-solid" : "default"}
            disabled={confirmDisabled}
          >
            {busy ? <Spinner /> : null}
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
