import { type ReactNode, useEffect, useState } from "react";
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
  dismissible = true,
}: {
  open: boolean;
  title: ReactNode;
  onClose: () => void;
  children: ReactNode;
  footer?: ReactNode;
  wide?: boolean;
  /** Whether Escape, the backdrop, and the header close button may dismiss. */
  dismissible?: boolean;
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
        className={cn(
          "max-h-[calc(100dvh-2rem)] gap-0 overflow-y-auto p-0 sm:max-w-[560px]",
          wide && "sm:max-w-[720px]",
        )}
      >
        <DialogHeader className="border-b border-border px-5 py-4 pr-14">
          <DialogTitle>{title}</DialogTitle>
        </DialogHeader>
        <div className="p-5">{children}</div>
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
            type="button"
            variant={danger ? "destructive-solid" : "default"}
            onClick={onConfirm}
            disabled={confirmDisabled}
          >
            {busy ? <Spinner /> : null}
            {confirmLabel}
          </Button>
        </>
      }
    >
      <div className={danger ? "danger-panel" : "info-panel"}>{message}</div>
      {requireText ? (
        <div className="field mt-16">
          <label className="field-label" htmlFor="confirm-text">
            Type <span className="mono">{requireText}</span> to confirm
          </label>
          <Input
            id="confirm-text"
            className="font-mono"
            value={typed}
            autoComplete="off"
            spellCheck={false}
            onChange={(event) => setTyped(event.target.value)}
          />
        </div>
      ) : null}
    </Modal>
  );
}
