import { createContext, type ReactNode, useContext, useMemo } from "react";
import { toast } from "sonner";
import { Toaster } from "@/components/ui/sonner";
import { ApiError } from "@/lib/api";

type ToastKind = "error" | "success" | "info";

interface ToastApi {
  push: (kind: ToastKind, title: string, message?: string) => void;
  success: (title: string, message?: string) => void;
  info: (title: string, message?: string) => void;
  error: (error: unknown, fallbackTitle?: string) => void;
  dismiss: (id: number | string) => void;
}

const TITLE_BY_CODE: Record<string, string> = {
  permission_denied: "Permission denied",
  unauthenticated: "Session expired",
  invalid_argument: "Invalid request",
  not_found: "Not found",
  conflict: "Conflict",
  unavailable: "Service unavailable",
  failed_precondition: "Action blocked",
};

const ToastContext = createContext<ToastApi | null>(null);

function show(kind: ToastKind, title: string, message?: string) {
  const options = message ? { description: message } : undefined;
  return toast[kind](title, options);
}

export function ToastProvider({ children }: { children: ReactNode }) {
  const api = useMemo<ToastApi>(
    () => ({
      push: show,
      success: (title, message) => show("success", title, message),
      info: (title, message) => show("info", title, message),
      error: (error, fallbackTitle) => {
        if (error instanceof ApiError) {
          show("error", TITLE_BY_CODE[error.code] ?? fallbackTitle ?? "Error", error.message);
        } else if (error instanceof Error) {
          show("error", fallbackTitle ?? "Error", error.message);
        } else {
          show("error", fallbackTitle ?? "Error", "Something went wrong.");
        }
      },
      dismiss: (id) => toast.dismiss(id),
    }),
    [],
  );

  return (
    <ToastContext.Provider value={api}>
      {children}
      <Toaster position="top-right" closeButton richColors />
    </ToastContext.Provider>
  );
}

export function useToast(): ToastApi {
  const context = useContext(ToastContext);
  if (!context) throw new Error("useToast must be used within ToastProvider");
  return context;
}
