import { useEffect, useState, type ReactNode } from "react";
import { Spinner } from "./ui";

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
  useEffect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent) => {
      if (dismissible && e.key === "Escape") onClose();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [open, onClose, dismissible]);

  if (!open) return null;

  return (
    <div className="modal-overlay" onMouseDown={dismissible ? onClose : undefined}>
      <div
        className={`modal ${wide ? "wide" : ""}`}
        role="dialog"
        aria-modal="true"
        onMouseDown={(e) => e.stopPropagation()}
      >
        <div className="modal-header">
          <div className="modal-title">{title}</div>
          {dismissible ? (
            <button className="toast-close" aria-label="Close" onClick={onClose}>
              ×
            </button>
          ) : null}
        </div>
        <div className="modal-body">{children}</div>
        {footer ? <div className="modal-footer">{footer}</div> : null}
      </div>
    </div>
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
  // When set, the user must type this exact string to enable the confirm
  // button (used for destructive, irreversible actions).
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
      footer={
        <>
          <button className="btn" onClick={onCancel} disabled={busy}>
            {cancelLabel}
          </button>
          <button
            className={`btn ${danger ? "btn-danger-solid" : "btn-primary"}`}
            onClick={onConfirm}
            disabled={confirmDisabled}
          >
            {busy ? <Spinner /> : null}
            {confirmLabel}
          </button>
        </>
      }
    >
      <div className={danger ? "danger-panel" : "info-panel"}>{message}</div>
      {requireText ? (
        <div className="field mt-16">
          <label className="field-label" htmlFor="confirm-text">
            Type <span className="mono">{requireText}</span> to confirm
          </label>
          <input
            id="confirm-text"
            className="input mono"
            value={typed}
            autoComplete="off"
            spellCheck={false}
            onChange={(e) => setTyped(e.target.value)}
          />
        </div>
      ) : null}
    </Modal>
  );
}
