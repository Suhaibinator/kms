import { render } from "@testing-library/react";
import { useEffect } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { type ToastApi, ToastProvider, useToast } from "@/context/ToastContext";
import { ApiError } from "@/lib/api";

// Stubbing the module rather than the provider keeps ToastContext's real
// option-shaping logic under test; `components/ui/sonner` resolves to the same
// stub, so <Toaster /> renders nothing.
const sonnerMocks = vi.hoisted(() => ({
  error: vi.fn((_title: string, _options?: Record<string, unknown>) => "id-error"),
  success: vi.fn((_title: string, _options?: Record<string, unknown>) => "id-success"),
  info: vi.fn((_title: string, _options?: Record<string, unknown>) => "id-info"),
  dismiss: vi.fn(),
}));
vi.mock("sonner", () => ({
  toast: sonnerMocks,
  Toaster: () => null,
}));

function Probe({ run }: { run: (toast: ToastApi) => void }) {
  const toast = useToast();
  useEffect(() => {
    run(toast);
  }, [run, toast]);
  return null;
}

/** Renders inside a real ToastProvider and returns whatever `run` returned. */
function withToast<T>(run: (toast: ToastApi) => T): T {
  let result!: T;
  render(
    <ToastProvider>
      <Probe
        run={(toast) => {
          result = run(toast);
        }}
      />
    </ToastProvider>,
  );
  return result;
}

describe("ToastContext", () => {
  beforeEach(() => {
    sonnerMocks.error.mockClear();
    sonnerMocks.success.mockClear();
    sonnerMocks.info.mockClear();
    sonnerMocks.dismiss.mockClear();
  });

  it("stays silent when the failure is our own cancellation", () => {
    // Pages abort in-flight requests on unmount and on filter changes; that is
    // never something to tell the user about.
    const id = withToast((toast) => toast.error(new DOMException("Aborted", "AbortError")));

    expect(id).toBeUndefined();
    expect(sonnerMocks.error).not.toHaveBeenCalled();
  });

  it("collapses simultaneous session expiries onto one toast", () => {
    withToast((toast) => {
      toast.error(new ApiError("unauthenticated", "token expired", 401));
      toast.error(new ApiError("unauthenticated", "token expired", 401));
    });

    expect(sonnerMocks.error).toHaveBeenCalledTimes(2);
    for (const call of sonnerMocks.error.mock.calls) {
      expect(call[0]).toBe("Session expired");
      expect(call[1]).toMatchObject({
        description: "Sign in again to continue.",
        id: "session-expired",
      });
    }
  });

  it("collapses every request that failed to reach the server onto one toast", () => {
    // Dropping the connection fails a dashboard refresh (4 calls) and the fleet
    // grid (up to 25) at once; a fixed id makes sonner replace, not stack.
    withToast((toast) => {
      toast.error(new ApiError("unavailable", "Could not reach the server.", 0));
      toast.error(
        new ApiError("unavailable", "The server took too long to respond.", 0),
        "Load failed",
      );
      toast.error(new ApiError("unavailable", "Could not reach the server.", 0), undefined, {
        id: "mine",
      });
    });

    expect(sonnerMocks.error).toHaveBeenCalledTimes(3);
    expect(sonnerMocks.error.mock.calls[0]).toEqual([
      "Service unavailable",
      expect.objectContaining({
        id: "server-unreachable",
        description: "Could not reach the server.",
      }),
    ]);
    // The caller's title still wins; only the id is pinned.
    expect(sonnerMocks.error.mock.calls[1]).toEqual([
      "Load failed",
      expect.objectContaining({
        id: "server-unreachable",
        description: "The server took too long to respond.",
        duration: 8_000,
      }),
    ]);
    // An explicit id is respected.
    expect(sonnerMocks.error.mock.calls[2]?.[1]).toMatchObject({ id: "mine" });
  });

  it("does not collapse a server-side 503 with a network failure", () => {
    withToast((toast) => toast.error(new ApiError("unavailable", "maintenance", 503)));
    expect(sonnerMocks.error).toHaveBeenCalledWith(
      "Service unavailable",
      expect.not.objectContaining({ id: expect.anything() }),
    );
  });

  it("prefers the caller's title over the error code's", () => {
    withToast((toast) => toast.error(new ApiError("conflict", "dup", 409), "Delete failed"));

    expect(sonnerMocks.error).toHaveBeenCalledWith(
      "Delete failed",
      expect.objectContaining({ description: "dup" }),
    );
  });

  it("falls back to the code-derived title when the caller gives none", () => {
    withToast((toast) => toast.error(new ApiError("conflict", "dup", 409)));

    expect(sonnerMocks.error).toHaveBeenCalledWith(
      "Conflict",
      expect.objectContaining({ description: "dup" }),
    );
  });

  it("describes a non-Error rejection generically", () => {
    withToast((toast) => toast.error("boom"));

    expect(sonnerMocks.error).toHaveBeenCalledWith(
      "Error",
      expect.objectContaining({ description: "Something went wrong." }),
    );
  });

  it("gives errors a readable default duration that callers can override", () => {
    withToast((toast) => {
      toast.error(new Error("slow"), "Load failed");
      toast.error(new Error("quick"), "Load failed", { duration: 1_000 });
    });

    expect(sonnerMocks.error.mock.calls[0][1]).toMatchObject({ duration: 8_000 });
    expect(sonnerMocks.error.mock.calls[1][1]).toMatchObject({ duration: 1_000 });
  });

  it("forwards an action so a toast can offer a retry", () => {
    const onClick = vi.fn();
    withToast((toast) =>
      toast.error(new Error("offline"), "Could not verify your session", {
        id: "session-check",
        action: { label: "Retry", onClick },
      }),
    );

    expect(sonnerMocks.error).toHaveBeenCalledWith(
      "Could not verify your session",
      expect.objectContaining({
        id: "session-check",
        action: { label: "Retry", onClick },
      }),
    );
  });

  it("returns the sonner id so a caller can dismiss its own toast", () => {
    const id = withToast((toast) => toast.success("Saved"));

    expect(id).toBe("id-success");
    expect(sonnerMocks.success).toHaveBeenCalledWith("Saved", {});
  });
});
