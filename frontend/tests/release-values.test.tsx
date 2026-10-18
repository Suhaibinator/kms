import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ReleaseValues } from "@/components/releases/ReleaseValues";
import { ReleaseWorkspace } from "@/components/releases/ReleaseWorkspace";
import { api } from "@/lib/api";
import type { ConfigurationRelease, Parameter } from "@/lib/types";

vi.mock("@/context/ToastContext", () => ({ useToast: () => ({ error: vi.fn() }) }));

const release: ConfigurationRelease = {
  namespace: { env: "prod", app: "payments" },
  name: "runtime",
  version: 4,
  schema_version: 1,
  entries: [
    {
      alias: "database",
      kind: "parameter",
      ref: { namespace: { env: "shared", app: "infrastructure" }, key: "db/config" },
      version: 2,
      content_type: "json",
      metadata_json: "{}",
      parameter_digest: "digest",
    },
    {
      alias: "greeting",
      kind: "parameter",
      ref: { namespace: { env: "prod", app: "payments" }, key: "greeting" },
      version: 1,
      content_type: "string",
      metadata_json: "{}",
      parameter_digest: "digest",
    },
    {
      alias: "password",
      kind: "secret",
      ref: { namespace: { env: "prod", app: "payments" }, key: "password" },
      version: 3,
      content_type: "text/plain",
      metadata_json: "{}",
      parameter_digest: "",
    },
  ],
  digest: "release-digest",
  metadata_json: "{}",
  created_by: "admin",
  created_at_unix_ms: 1,
};

const response = (value: string) => ({ parameter: { value } as Parameter });

describe("release values", () => {
  beforeEach(() => {
    vi.spyOn(api, "getParameter").mockResolvedValue(
      response('{"nested":{"enabled":false},"large":9007199254740993}'),
    );
  });

  it("loads exact manifest pins on demand and lets users explore nested JSON", async () => {
    render(<ReleaseValues release={release} />);
    expect(api.getParameter).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "View database value" }));
    const entry = screen.getByRole("region", { name: "database pinned value" });
    await within(entry).findByRole("button", { name: "Copy database value" });
    expect(api.getParameter).toHaveBeenCalledWith(
      { env: "shared", app: "infrastructure", key: "db/config" },
      2,
      undefined,
      { signal: expect.any(AbortSignal) },
    );
    expect(entry).toHaveTextContent("9007199254740993");
    expect(entry).toHaveTextContent("false");
    fireEvent.click(within(entry).getByRole("button", { name: "Collapse nested" }));
    expect(entry).not.toHaveTextContent("false");
    fireEvent.click(within(entry).getByRole("button", { name: "Expand nested" }));
    expect(entry).toHaveTextContent("false");

    fireEvent.click(screen.getByRole("button", { name: "Hide database value" }));
    fireEvent.click(screen.getByRole("button", { name: "View database value" }));
    expect(api.getParameter).toHaveBeenCalledTimes(1);
    expect(screen.queryByRole("button", { name: "View password value" })).not.toBeInTheDocument();
  });

  it("filters by reference and handles plain text and empty values", async () => {
    vi.mocked(api.getParameter).mockResolvedValue(response(""));
    render(<ReleaseValues release={release} />);
    const filter = screen.getByRole("searchbox", { name: "Filter parameters" });
    fireEvent.change(filter, { target: { value: "infrastructure" } });
    expect(screen.getByRole("button", { name: "View database value" })).toBeVisible();
    expect(screen.queryByRole("button", { name: "View greeting value" })).not.toBeInTheDocument();
    fireEvent.change(filter, { target: { value: "missing" } });
    expect(screen.getByRole("status")).toHaveTextContent("No parameters match");
    fireEvent.change(filter, { target: { value: "greeting" } });
    fireEvent.click(screen.getByRole("button", { name: "View greeting value" }));
    await waitFor(() =>
      expect(screen.queryByText("Loading pinned value…")).not.toBeInTheDocument(),
    );
    expect(screen.getByTitle("Empty string")).toHaveTextContent("(empty)");
  });

  it("keeps failures local and retries the pinned version without falling back to current", async () => {
    vi.mocked(api.getParameter)
      .mockRejectedValueOnce(new Error("Version is unavailable"))
      .mockResolvedValueOnce(response('{"recovered":true}'));
    render(<ReleaseValues release={release} />);
    fireEvent.click(screen.getByRole("button", { name: "View database value" }));
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "Could not load v2: Version is unavailable",
    );
    expect(screen.getByRole("button", { name: "View greeting value" })).toBeEnabled();
    fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    await screen.findByRole("button", { name: "Copy database value" });
    expect(api.getParameter).toHaveBeenCalledTimes(2);
    expect(vi.mocked(api.getParameter).mock.calls.every((call) => call[1] === 2)).toBe(true);
  });

  it("cancels a pending historical value when switching releases", async () => {
    let finish: (value: { parameter: Parameter }) => void = () => undefined;
    vi.mocked(api.getParameter).mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          finish = resolve;
        }),
    );
    const workspace = (selected: ConfigurationRelease) => (
      <ReleaseWorkspace
        summary={{ release: selected, current: false, previous: false, activation_revision: 0 }}
        releases={[]}
        initialSection="values"
        busyAction=""
        activationFailure={null}
        onDismissFailure={vi.fn()}
        onClose={vi.fn()}
        onValidate={vi.fn()}
        onActivate={vi.fn()}
      />
    );
    const { rerender } = render(workspace(release));
    fireEvent.click(screen.getByRole("button", { name: "View database value" }));
    const signal = vi.mocked(api.getParameter).mock.calls[0][3]?.signal;
    const next = { ...release, version: 5, entries: [{ ...release.entries[0], version: 3 }] };
    rerender(workspace(next));
    expect(signal?.aborted).toBe(true);
    await act(async () => finish(response('{"stale":true}')));
    expect(screen.queryByRole("button", { name: "Copy database value" })).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "View database value" }));
    await screen.findByRole("button", { name: "Copy database value" });
    expect(vi.mocked(api.getParameter).mock.calls.at(-1)?.[1]).toBe(3);
    expect(screen.getByRole("region", { name: "database pinned value" })).not.toHaveTextContent(
      "stale",
    );
  });

  it("shows an empty state for a release containing only secrets", () => {
    render(<ReleaseValues release={{ ...release, entries: [release.entries[2]] }} />);
    expect(screen.getByText("No parameter values")).toBeVisible();
    expect(api.getParameter).not.toHaveBeenCalled();
  });
});
