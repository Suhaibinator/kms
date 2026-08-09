import {
  createContext,
  type ReactNode,
  useCallback,
  useContext,
  useMemo,
  useRef,
  useState,
} from "react";
import { ApiError } from "@/lib/api";

type ToastKind = "error" | "success" | "info";

interface Toast {
  id: number;
  kind: ToastKind;
  title: string;
  message?: string;
}

interface ToastApi {
  push: (kind: ToastKind, title: string, message?: string) => void;
  success: (title: string, message?: string) => void;
  info: (title: string, message?: string) => void;
  error: (err: unknown, fallbackTitle?: string) => void;
  dismiss: (id: number) => void;
}

const ToastContext = createContext<ToastApi | null>(null);

const TITLE_BY_CODE: Record<string, string> = {
  invalid_argument: "Invalid request",
  unauthenticated: "Not authenticated",
  permission_denied: "Permission denied",
  not_found: "Not found",
  already_exists: "Already exists",
  failed_precondition: "Not allowed",
  unavailable: "Service unavailable",
  internal: "Server error",
};

export function ToastProvider({ children }: { children: ReactNode }) {
  const [toasts, setToasts] = useState<Toast[]>([]);
  const nextId = useRef(1);

  const dismiss = useCallback((id: number) => {
    setToasts((prev) => prev.filter((t) => t.id !== id));
  }, []);

  const push = useCallback(
    (kind: ToastKind, title: string, message?: string) => {
      const id = nextId.current;
      nextId.current += 1;
      setToasts((prev) => [...prev, { id, kind, title, message }]);
      const ttl = kind === "error" ? 8000 : 4500;
      window.setTimeout(() => dismiss(id), ttl);
    },
    [dismiss],
  );

  const api = useMemo<ToastApi>(
    () => ({
      push,
      success: (title, message) => push("success", title, message),
      info: (title, message) => push("info", title, message),
      error: (err, fallbackTitle) => {
        // Server error messages never contain secret material (see contract),
        // so surfacing them is safe.
        if (err instanceof ApiError) {
          push("error", TITLE_BY_CODE[err.code] ?? fallbackTitle ?? "Error", err.message);
        } else if (err instanceof Error) {
          push("error", fallbackTitle ?? "Error", err.message);
        } else {
          push("error", fallbackTitle ?? "Error", "Something went wrong.");
        }
      },
      dismiss,
    }),
    [push, dismiss],
  );

  return (
    <ToastContext.Provider value={api}>
      {children}
      <div className="toast-stack">
        {toasts.map((t) => (
          <div
            key={t.id}
            className={`toast ${t.kind}`}
            role={t.kind === "error" ? "alert" : "status"}
            aria-live={t.kind === "error" ? "assertive" : "polite"}
            aria-atomic="true"
          >
            <div className="toast-body">
              <div className="toast-title">{t.title}</div>
              {t.message ? <div className="toast-msg">{t.message}</div> : null}
            </div>
            <button
              type="button"
              className="toast-close"
              aria-label={`Dismiss ${t.title} notification`}
              onClick={() => dismiss(t.id)}
            >
              ×
            </button>
          </div>
        ))}
      </div>
    </ToastContext.Provider>
  );
}

export function useToast(): ToastApi {
  const ctx = useContext(ToastContext);
  if (!ctx) throw new Error("useToast must be used within ToastProvider");
  return ctx;
}
