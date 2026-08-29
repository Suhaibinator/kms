import { createContext, type ReactNode, useContext, useMemo } from "react";
import { toast } from "sonner";
import { Toaster } from "@/components/ui/sonner";
import { ApiError, isAbortError, isUnreachableError } from "@/lib/api";
import { useTheme } from "@/lib/theme";

type ToastKind = "error" | "success" | "info";

type ToastId = string | number;

export interface ToastOptions {
  /** Reuse an id to *replace* an existing toast instead of stacking a duplicate. */
  id?: ToastId;
  /** Milliseconds. Defaults to sonner's, or ERROR_DURATION_MS for errors. */
  duration?: number;
  action?: { label: string; onClick: () => void };
}

export interface ToastApi {
  push: (kind: ToastKind, title: string, message?: string, options?: ToastOptions) => ToastId;
  success: (title: string, message?: string, options?: ToastOptions) => ToastId;
  info: (title: string, message?: string, options?: ToastOptions) => ToastId;
  /** Returns undefined when the "error" was our own cancellation and nothing shown. */
  error: (error: unknown, fallbackTitle?: string, options?: ToastOptions) => ToastId | undefined;
  dismiss: (id: ToastId) => void;
}

const TITLE_BY_CODE: Record<string, string> = {
  permission_denied: "Permission denied",
  unauthenticated: "Session expired",
  invalid_argument: "Invalid request",
  not_found: "Not found",
  conflict: "Conflict",
  unavailable: "Service unavailable",
  failed_precondition: "Action blocked",
  already_exists: "Already exists",
  invalid_credentials: "Sign-in failed",
  internal: "Something went wrong",
};

// Four seconds is not long enough to read a server-supplied message.
const ERROR_DURATION_MS = 8_000;
const SESSION_EXPIRED_TOAST_ID = "session-expired";
const UNREACHABLE_TOAST_ID = "server-unreachable";

const ToastContext = createContext<ToastApi | null>(null);

function show(kind: ToastKind, title: string, message?: string, options?: ToastOptions): ToastId {
  return toast[kind](title, {
    ...(message ? { description: message } : null),
    ...(options?.id !== undefined ? { id: options.id } : null),
    ...(options?.duration !== undefined ? { duration: options.duration } : null),
    ...(options?.action
      ? { action: { label: options.action.label, onClick: options.action.onClick } }
      : null),
  });
}

export function ToastProvider({ children }: { children: ReactNode }) {
  const { resolved } = useTheme();
  const api = useMemo<ToastApi>(
    () => ({
      push: show,
      success: (title, message, options) => show("success", title, message, options),
      info: (title, message, options) => show("info", title, message, options),
      error: (error, fallbackTitle, options) => {
        // We abort in-flight requests on unmount and on filter changes; that is
        // not a failure the user should ever be told about. Guarding once here
        // means no page has to remember to.
        if (isAbortError(error)) return undefined;

        const opts: ToastOptions = { duration: ERROR_DURATION_MS, ...options };

        if (error instanceof ApiError && error.code === "unauthenticated") {
          // Every in-flight request fails at once when a token dies. A fixed id
          // makes sonner replace the toast instead of stacking N copies of it.
          return show("error", "Session expired", "Sign in again to continue.", {
            ...opts,
            id: opts.id ?? SESSION_EXPIRED_TOAST_ID,
          });
        }

        if (isUnreachableError(error)) {
          // Dropping the connection fails every in-flight request the same way
          // (a dashboard refresh alone is four calls, the fleet grid up to 25
          // more). One fixed id turns the wall of identical toasts into one.
          return show(
            "error",
            fallbackTitle ?? TITLE_BY_CODE.unavailable ?? "Error",
            error.message,
            {
              ...opts,
              id: opts.id ?? UNREACHABLE_TOAST_ID,
            },
          );
        }

        // The caller's title names the action that failed ("Delete failed"),
        // which is more useful than the error class ("Conflict"); the
        // code-derived title is the fallback.
        if (error instanceof ApiError) {
          return show(
            "error",
            fallbackTitle ?? TITLE_BY_CODE[error.code] ?? "Error",
            error.message,
            opts,
          );
        }
        if (error instanceof Error) {
          return show("error", fallbackTitle ?? "Error", error.message, opts);
        }
        return show("error", fallbackTitle ?? "Error", "Something went wrong.", opts);
      },
      dismiss: (id) => toast.dismiss(id),
    }),
    [],
  );

  return (
    <ToastContext.Provider value={api}>
      {children}
      <Toaster theme={resolved} position="top-right" closeButton richColors />
    </ToastContext.Provider>
  );
}

export function useToast(): ToastApi {
  const context = useContext(ToastContext);
  if (!context) throw new Error("useToast must be used within ToastProvider");
  return context;
}
