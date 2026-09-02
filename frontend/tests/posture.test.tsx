import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "@/lib/api";
import type { PostureResponse } from "@/lib/types";
import PosturePage from "@/pages/posture";

const mocks = vi.hoisted(() => ({
  query: {} as Record<string, string>,
  // Like the real router, a replace changes what the next render reads.
  replace: vi.fn(async (url: { query: Record<string, string> }) => {
    mocks.query = url.query;
    return true;
  }),
  toast: {
    error: vi.fn(),
    success: vi.fn(),
  },
}));

vi.mock("next/router", () => ({
  useRouter: () => ({
    pathname: "/posture",
    query: mocks.query,
    isReady: true,
    replace: mocks.replace,
  }),
}));
vi.mock("@/context/ToastContext", () => ({ useToast: () => mocks.toast }));

const HOUR = 3_600_000;

function iso(offsetMs: number): string {
  return new Date(Date.now() + offsetMs).toISOString();
}

function posture(overrides: Partial<PostureResponse> = {}): PostureResponse {
  return {
    generated_at: iso(0),
    windows: { cert: "720h0m0s", secret: "720h0m0s", admin_cert: "336h0m0s" },
    kek: {
      active_id: "kek-2026-01",
      created_at: iso(-48 * HOUR),
      age_seconds: 172_800,
      generations: 2,
    },
    auth: { tls_enabled: true, mtls_enabled: true, admin_client_cert_required: true },
    audit: { enabled: true, retain_duration: "2160h0m0s", archive_enabled: true },
    metrics_enabled: true,
    admin_certs: {
      lacking: ["ops-oncall"],
      expiring: [{ identity: "root", serial: "ADMIN-SERIAL-1", not_after: iso(72 * HOUR) }],
    },
    identity_certs_expiring: {
      items: [
        {
          identity: "billing-api",
          env: "prod",
          app: "billing",
          serial: "CERT-SERIAL-1",
          not_after: iso(24 * HOUR),
        },
      ],
      total: 1,
      truncated: false,
    },
    secret_versions_expiring: {
      items: [
        { env: "prod", app: "billing", key: "db/password", version: 3, expires_at: iso(12 * HOUR) },
      ],
      total: 1,
      truncated: false,
    },
    changelog: { rows: 412, last_revision: 900, oldest_revision: 488 },
    ...overrides,
  };
}

const empty = (): PostureResponse =>
  posture({
    admin_certs: { lacking: [], expiring: [] },
    identity_certs_expiring: { items: [], total: 0, truncated: false },
    secret_versions_expiring: { items: [], total: 0, truncated: false },
  });

beforeEach(() => {
  mocks.query = {};
  mocks.replace.mockClear();
  mocks.toast.error.mockReset();
  vi.spyOn(api, "posture").mockResolvedValue(posture());
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe("security posture", () => {
  it("renders the status cards and the three expiry tables", async () => {
    render(<PosturePage />);

    await screen.findByText("kek-2026-01");
    expect(screen.getByText("2 generations")).toBeVisible();
    expect(screen.getByText("current")).toBeVisible();
    expect(screen.getByText("client cert required")).toBeVisible();
    expect(screen.getByText("mTLS on")).toBeVisible();
    expect(screen.getByText("TLS on")).toBeVisible();
    // Go durations are rendered as the days they are: "2160h0m0s" is 90 days.
    expect(screen.getByText("90d")).toBeVisible();
    expect(screen.getByText("recording")).toBeVisible();
    expect(screen.getByText("exported")).toBeVisible();

    // An admin with no certificate cannot authenticate at all, so it leads the
    // admin table beside the one that is merely expiring.
    expect(screen.getByText("ops-oncall")).toBeVisible();
    expect(screen.getByText("no certificate")).toBeVisible();
    expect(screen.getByText("ADMIN-SERIAL-1")).toBeVisible();
    expect(screen.getByText("billing-api")).toBeVisible();
    expect(screen.getByText("prod/billing")).toBeVisible();
    expect(screen.getByText("CERT-SERIAL-1")).toBeVisible();
    expect(screen.getByText("/prod/billing/db/password")).toBeVisible();
    expect(screen.getByText("v3")).toBeVisible();
    // The fixed admin-certificate look-ahead is stated, not implied.
    expect(screen.getByText(/Fixed 14d look-ahead/)).toBeVisible();
  });

  it("warns when the active key is older than a year", async () => {
    vi.mocked(api.posture).mockResolvedValue(
      posture({
        kek: {
          active_id: "kek-2024-01",
          created_at: iso(-400 * 24 * HOUR),
          age_seconds: 400 * 24 * 3600,
          generations: 1,
        },
      }),
    );
    render(<PosturePage />);

    expect(await screen.findByText("rotation due")).toBeVisible();
    expect(screen.getByText("400d")).toBeVisible();
    expect(screen.getByText("never rotated")).toBeVisible();
  });

  it("refetches both windows and records the choice in the URL", async () => {
    const load = vi.mocked(api.posture);
    render(<PosturePage />);
    await screen.findByText("kek-2026-01");
    expect(load).toHaveBeenLastCalledWith(
      { cert_window: "30d", secret_window: "30d" },
      expect.anything(),
    );

    fireEvent.click(screen.getByRole("button", { name: "90 days" }));
    await waitFor(() =>
      expect(load).toHaveBeenLastCalledWith(
        { cert_window: "90d", secret_window: "90d" },
        expect.anything(),
      ),
    );
    expect(mocks.replace.mock.calls.at(-1)?.[0]).toEqual({
      pathname: "/posture",
      query: { window: "90d" },
    });
    expect(screen.getByRole("button", { name: "90 days" })).toHaveAttribute("aria-pressed", "true");

    // The default window is the absence of the parameter, not "window=30d".
    fireEvent.click(screen.getByRole("button", { name: "30 days" }));
    await waitFor(() =>
      expect(load).toHaveBeenLastCalledWith(
        { cert_window: "30d", secret_window: "30d" },
        expect.anything(),
      ),
    );
    expect(mocks.replace.mock.calls.at(-1)?.[0]).toEqual({ pathname: "/posture", query: {} });
  });

  it("restores the window from the URL", async () => {
    mocks.query = { window: "7d" };
    const load = vi.mocked(api.posture);
    render(<PosturePage />);

    await screen.findByText("kek-2026-01");
    expect(load).toHaveBeenCalledTimes(1);
    expect(load).toHaveBeenCalledWith(
      { cert_window: "7d", secret_window: "7d" },
      expect.anything(),
    );
    expect(screen.getByRole("button", { name: "7 days" })).toHaveAttribute("aria-pressed", "true");
  });

  it("says so when nothing is expiring", async () => {
    vi.mocked(api.posture).mockResolvedValue(empty());
    render(<PosturePage />);

    expect(await screen.findByText("Every admin has a valid certificate")).toBeVisible();
    expect(screen.getByText("No certificates expiring")).toBeVisible();
    expect(screen.getByText("No secret versions expiring")).toBeVisible();
  });

  it("reports a capped list rather than under-reporting it", async () => {
    vi.mocked(api.posture).mockResolvedValue(
      posture({
        identity_certs_expiring: {
          items: [
            {
              identity: "billing-api",
              env: "prod",
              app: "billing",
              serial: "CERT-SERIAL-1",
              not_after: iso(24 * HOUR),
            },
          ],
          total: 412,
          truncated: true,
        },
      }),
    );
    render(<PosturePage />);

    expect(await screen.findByText(/Showing the first 1 of 412/)).toBeVisible();
  });

  it("shows an error state with a retry instead of a stale snapshot", async () => {
    const load = vi.mocked(api.posture).mockRejectedValue(new Error("posture unavailable"));
    render(<PosturePage />);

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("Could not load the security posture.");
    expect(alert).toHaveTextContent("posture unavailable");
    expect(mocks.toast.error).toHaveBeenCalled();
    // The tables are gone: what a stale snapshot said was expiring may already
    // have been dealt with.
    expect(screen.queryByText("kek-2026-01")).toBeNull();

    load.mockResolvedValue(posture());
    fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    await screen.findByText("kek-2026-01");
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("renders nothing the contract does not define", async () => {
    // A server that grew extra fields — credential material among them — must
    // not have them appear on the page just because they arrived.
    const leaky = {
      ...posture(),
      kek: {
        ...posture().kek,
        material: "MATERIAL-MUST-NOT-RENDER",
      },
      admin_certs: {
        lacking: ["ops-oncall"],
        expiring: [
          {
            identity: "root",
            serial: "ADMIN-SERIAL-1",
            not_after: iso(72 * HOUR),
            cert_pem: "PEM-MUST-NOT-RENDER",
          },
        ],
      },
      identity_certs_expiring: {
        items: [
          {
            identity: "billing-api",
            env: "prod",
            app: "billing",
            serial: "CERT-SERIAL-1",
            not_after: iso(24 * HOUR),
            fingerprint: "FINGERPRINT-MUST-NOT-RENDER",
          },
        ],
        total: 1,
        truncated: false,
      },
      secret_versions_expiring: {
        items: [
          {
            env: "prod",
            app: "billing",
            key: "db/password",
            version: 3,
            expires_at: iso(12 * HOUR),
            value: "VALUE-MUST-NOT-RENDER",
            access_token: "TOKEN-MUST-NOT-RENDER",
          },
        ],
        total: 1,
        truncated: false,
      },
      admin_token: "ADMIN-TOKEN-MUST-NOT-RENDER",
    } as unknown as PostureResponse;
    vi.mocked(api.posture).mockResolvedValue(leaky);
    render(<PosturePage />);

    await screen.findByText("kek-2026-01");
    const rendered = document.body.textContent ?? "";
    for (const forbidden of [
      "MATERIAL-MUST-NOT-RENDER",
      "PEM-MUST-NOT-RENDER",
      "FINGERPRINT-MUST-NOT-RENDER",
      "VALUE-MUST-NOT-RENDER",
      "TOKEN-MUST-NOT-RENDER",
      "ADMIN-TOKEN-MUST-NOT-RENDER",
    ]) {
      expect(rendered).not.toContain(forbidden);
    }
  });

  it("re-runs the current window from the Refresh button", async () => {
    const load = vi.mocked(api.posture);
    render(<PosturePage />);
    await screen.findByText("kek-2026-01");
    expect(load).toHaveBeenCalledTimes(1);

    fireEvent.click(screen.getByRole("button", { name: "Refresh" }));
    await waitFor(() => expect(load).toHaveBeenCalledTimes(2));
    expect(load.mock.calls[1][0]).toEqual(load.mock.calls[0][0]);
  });
});
