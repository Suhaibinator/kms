import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "@/lib/api";
import type { Namespace } from "@/lib/types";
import { createStoredZip } from "@/lib/zip";
import IdentitiesPage from "@/pages/identities";

const mocks = vi.hoisted(() => ({
  namespaces: [] as Namespace[],
  namespacesLoading: false,
  namespacesError: null as unknown,
  reloadNamespaces: vi.fn(),
  toast: {
    error: vi.fn(),
    success: vi.fn(),
  },
}));

vi.mock("@/context/ToastContext", () => ({ useToast: () => mocks.toast }));
vi.mock("@/lib/hooks", () => ({
  useNamespaces: () => ({
    namespaces: mocks.namespaces,
    loading: mocks.namespacesLoading,
    error: mocks.namespacesError,
    reload: mocks.reloadNamespaces,
  }),
}));

function namespace(methods: Namespace["allowed_auth_methods"], app = "billing"): Namespace {
  return {
    env: "prod",
    app,
    description: "Billing service",
    allowed_auth_methods: methods,
    created_by: "admin",
    created_at_unix_ms: 1,
    parameter_count: 0,
    secret_count: 0,
  };
}

async function openWizard() {
  render(<IdentitiesPage />);
  await screen.findByText("No identities yet");
  fireEvent.click(screen.getAllByRole("button", { name: "Connect application" })[0]);
  return screen.getByRole("dialog", { name: "Connect application — choose application" });
}

function selectApplication(dialog: HTMLElement) {
  fireEvent.change(within(dialog).getByRole("combobox", { name: "Environment" }), {
    target: { value: "prod" },
  });
  fireEvent.change(within(dialog).getByRole("combobox", { name: "Application" }), {
    target: { value: "billing" },
  });
}

function centralDirectoryModes(buffer: ArrayBuffer): Map<string, { host: number; mode: number }> {
  const bytes = new Uint8Array(buffer);
  const view = new DataView(buffer);
  const modes = new Map<string, { host: number; mode: number }>();
  for (let offset = 0; offset <= bytes.byteLength - 46; offset += 1) {
    if (view.getUint32(offset, true) !== 0x02014b50) continue;
    const versionMadeBy = view.getUint16(offset + 4, true);
    const nameLength = view.getUint16(offset + 28, true);
    const name = new TextDecoder().decode(bytes.slice(offset + 46, offset + 46 + nameLength));
    const externalAttributes = view.getUint32(offset + 38, true);
    modes.set(name, { host: versionMadeBy >>> 8, mode: externalAttributes >>> 16 });
  }
  return modes;
}

beforeEach(() => {
  mocks.namespaces = [];
  mocks.namespacesLoading = false;
  mocks.namespacesError = null;
  mocks.reloadNamespaces.mockReset();
  mocks.toast.error.mockReset();
  mocks.toast.success.mockReset();
  vi.spyOn(api, "listIdentities").mockResolvedValue({ identities: [], next_page_token: "" });
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe("identity onboarding", () => {
  it("allows dotted identity filenames while rejecting path-like ZIP entries", () => {
    expect(() =>
      createStoredZip([
        { name: "service..blue..crt", content: "certificate", mode: 0o644 },
        { name: "service..blue..key", content: "key", mode: 0o600 },
      ]),
    ).not.toThrow();
    expect(() => createStoredZip([{ name: "..", content: "unsafe" }])).toThrow(/simple filenames/);
    expect(() => createStoredZip([{ name: "dir/key", content: "unsafe" }])).toThrow(
      /simple filenames/,
    );
  });

  it("keeps CA export and privileged identity types in clearly labelled advanced areas", async () => {
    vi.spyOn(api, "createIdentity").mockResolvedValue({
      identity: {
        name: "deploy-admin",
        kind: "admin",
        namespace: null,
        has_token: true,
        certs: [],
      },
      token: "kms_admin_once",
    });
    const dialog = await openWizard();

    expect(screen.queryByRole("button", { name: "Download CA cert" })).not.toBeInTheDocument();
    fireEvent.click(screen.getByText("Advanced identity and CA tools"));
    expect(screen.getByRole("button", { name: "Export client-issuing CA" })).toBeVisible();
    expect(screen.getByText(/applications must not use it to trust the KMS server/i)).toBeVisible();

    fireEvent.click(within(dialog).getByText("Advanced identity type"));
    const identityType = within(dialog).getByRole("combobox", { name: "Identity type" });
    expect(within(identityType).getByRole("option", { name: "Administrator" })).toBeVisible();

    fireEvent.change(identityType, { target: { value: "admin" } });
    fireEvent.change(within(dialog).getByRole("textbox", { name: "Application identity name" }), {
      target: { value: "deploy-admin" },
    });
    fireEvent.click(within(dialog).getByRole("button", { name: "Continue to authentication" }));

    const authDialog = screen.getByRole("dialog", {
      name: "Connect application — authentication",
    });
    expect(within(authDialog).getByRole("checkbox", { name: /Bearer token/ })).toBeChecked();
    expect(within(authDialog).getByRole("checkbox", { name: /Bearer token/ })).toBeDisabled();

    fireEvent.click(
      within(authDialog).getByRole("button", { name: "Create identity credentials" }),
    );
    const saveDialog = await screen.findByRole("dialog", { name: "Save these credentials now" });
    fireEvent.click(within(saveDialog).getByRole("button", { name: "I've saved it — continue" }));
    const adminDialog = screen.getByRole("dialog", { name: "Use the administrator identity" });
    expect(
      within(adminDialog).getByText(/No policy is required for an administrator/),
    ).toBeVisible();
    expect(
      within(adminDialog).queryByRole("link", { name: "Open policies" }),
    ).not.toBeInTheDocument();
  });

  it("shows namespace failures with a retry instead of claiming no namespaces exist", async () => {
    mocks.namespacesError = new Error("offline");
    const dialog = await openWizard();

    expect(within(dialog).getByText("Could not load application namespaces.")).toBeVisible();
    expect(
      within(dialog).queryByText(/No application namespaces are available/),
    ).not.toBeInTheDocument();
    const retry = within(dialog).getByRole("button", { name: "Retry namespace list" });
    fireEvent.click(retry);
    expect(mocks.reloadNamespaces).toHaveBeenCalledOnce();
    expect(
      within(dialog).getByRole("button", { name: "Continue to authentication" }),
    ).toBeDisabled();
  });

  it("disables credential operations rejected by an identity's bound namespace", async () => {
    mocks.namespaces = [namespace(["token"], "token-home"), namespace(["mtls"], "mtls-home")];
    vi.mocked(api.listIdentities).mockResolvedValue({
      identities: [
        {
          name: "cert-blocked",
          kind: "client",
          namespace: { env: "prod", app: "token-home" },
          has_token: true,
          certs: [],
        },
        {
          name: "token-blocked",
          kind: "client",
          namespace: { env: "prod", app: "mtls-home" },
          has_token: true,
          certs: [],
        },
      ],
      next_page_token: "",
    });
    const issueCert = vi.spyOn(api, "issueCert");
    const rotateIdentity = vi.spyOn(api, "rotateIdentity");

    render(<IdentitiesPage />);
    const certRow = (await screen.findByText("cert-blocked")).closest("tr");
    expect(certRow).not.toBeNull();
    fireEvent.click(within(certRow as HTMLElement).getByRole("button", { name: "Certificates" }));

    const certificateDialog = screen.getByRole("dialog", {
      name: "Certificates — cert-blocked",
    });
    expect(
      within(certificateDialog).getByText(/New certificate issuance is disabled/),
    ).toBeVisible();
    expect(
      within(certificateDialog).getByText(/bound namespace does not accept mTLS/),
    ).toBeVisible();
    expect(
      within(certificateDialog).getByRole("button", { name: "Issue new certificate" }),
    ).toBeDisabled();
    expect(issueCert).not.toHaveBeenCalled();

    fireEvent.click(within(certificateDialog).getByRole("button", { name: "Close" }));
    const tokenRow = screen.getByText("token-blocked").closest("tr");
    expect(tokenRow).not.toBeNull();
    expect(
      within(tokenRow as HTMLElement).getByRole("button", { name: "Rotate token" }),
    ).toBeDisabled();
    expect(
      within(tokenRow as HTMLElement).getByText(/Rotation unavailable.*does not accept token/),
    ).toBeVisible();
    expect(rotateIdentity).not.toHaveBeenCalled();
  });

  it("requires an explicit target namespace for unbound client integrations", async () => {
    vi.spyOn(api, "createIdentity").mockResolvedValue({
      identity: {
        name: "release-tool",
        kind: "client",
        namespace: null,
        has_token: true,
        certs: [],
      },
      token: "kms_tool_once",
    });
    const dialog = await openWizard();
    fireEvent.click(within(dialog).getByText("Advanced identity type"));
    fireEvent.change(within(dialog).getByRole("combobox", { name: "Identity type" }), {
      target: { value: "unbound" },
    });
    fireEvent.change(within(dialog).getByRole("textbox", { name: "Application identity name" }), {
      target: { value: "release-tool" },
    });
    fireEvent.click(within(dialog).getByRole("button", { name: "Continue to authentication" }));

    const authDialog = screen.getByRole("dialog", {
      name: "Connect application — authentication",
    });
    fireEvent.click(within(authDialog).getByRole("checkbox", { name: /mTLS client certificate/ }));
    fireEvent.click(within(authDialog).getByRole("checkbox", { name: /Bearer token/ }));
    fireEvent.click(
      within(authDialog).getByRole("button", { name: "Create identity credentials" }),
    );

    const saveDialog = await screen.findByRole("dialog", { name: "Save these credentials now" });
    fireEvent.click(within(saveDialog).getByRole("button", { name: "I've saved it — continue" }));
    const integrationDialog = screen.getByRole("dialog", { name: "Integrate the application" });
    expect(
      within(integrationDialog).getByText(/An unbound client has no default namespace/),
    ).toBeVisible();
    expect(within(integrationDialog).getByText(/absolute keys such as/)).toHaveTextContent(
      "/prod/app/key",
    );
    expect(within(integrationDialog).getByText(/Namespace: "prod\/app"/)).toBeVisible();
    expect(
      within(integrationDialog).getByText(/policy grants authorization but does not tell an SDK/),
    ).toBeVisible();
  });

  it("requires every selected method to be accepted and integrates a token-only app", async () => {
    mocks.namespaces = [namespace(["token"])];
    const createIdentity = vi.spyOn(api, "createIdentity").mockResolvedValue({
      identity: {
        name: "billing-api",
        kind: "client",
        namespace: { env: "prod", app: "billing" },
        has_token: true,
        certs: [],
      },
      token: "kms_token_once",
    });
    const dialog = await openWizard();
    fireEvent.change(within(dialog).getByRole("textbox", { name: "Application identity name" }), {
      target: { value: "billing-api" },
    });
    selectApplication(dialog);
    fireEvent.click(within(dialog).getByRole("button", { name: "Continue to authentication" }));

    const authDialog = screen.getByRole("dialog", {
      name: "Connect application — authentication",
    });
    expect(
      within(authDialog).getByText(/Every selected method must be accepted by this namespace/),
    ).toBeVisible();
    const create = within(authDialog).getByRole("button", {
      name: "Create application credentials",
    });
    expect(create).toBeDisabled();

    fireEvent.click(within(authDialog).getByRole("checkbox", { name: /Bearer token/ }));
    expect(create).toBeDisabled();
    expect(within(authDialog).getByText(/Deselect mtls/)).toBeVisible();

    fireEvent.click(within(authDialog).getByRole("checkbox", { name: /mTLS client certificate/ }));
    expect(create).toBeEnabled();
    fireEvent.click(create);

    const saveDialog = await screen.findByRole("dialog", { name: "Save these credentials now" });
    expect(within(saveDialog).queryByText(/client certificate \(PEM\)/i)).not.toBeInTheDocument();
    fireEvent.click(within(saveDialog).getByRole("button", { name: "I've saved it — continue" }));

    const integrationDialog = screen.getByRole("dialog", { name: "Integrate the application" });
    expect(within(integrationDialog).getByText("Token and server trust")).toBeVisible();
    expect(within(integrationDialog).getByText(/TLSFromFiles\("server-ca\.crt"\)/)).toBeVisible();
    expect(within(integrationDialog).getByText(/os\.Getenv\("KMS_TOKEN"\)/)).toBeVisible();

    const goTab = within(integrationDialog).getByRole("tab", { name: "Go" });
    const pythonTab = within(integrationDialog).getByRole("tab", { name: "Python" });
    const typescriptTab = within(integrationDialog).getByRole("tab", { name: "TypeScript" });
    expect(goTab).toHaveAttribute("tabindex", "0");
    expect(pythonTab).toHaveAttribute("tabindex", "-1");

    goTab.focus();
    fireEvent.keyDown(goTab, { key: "ArrowRight" });
    expect(pythonTab).toHaveFocus();
    expect(pythonTab).toHaveAttribute("aria-selected", "true");
    expect(within(integrationDialog).getByText(/tls_from_files\("server-ca\.crt"\)/)).toBeVisible();
    expect(within(integrationDialog).getByText(/with Client\(/)).toBeVisible();
    expect(within(integrationDialog).getByText(/Application work goes here/)).toBeVisible();

    fireEvent.keyDown(pythonTab, { key: "End" });
    expect(typescriptTab).toHaveFocus();
    expect(within(integrationDialog).getByText(/tlsFromFiles\("server-ca\.crt"\)/)).toBeVisible();
    expect(within(integrationDialog).getByText(/try \{/)).toBeVisible();
    expect(within(integrationDialog).getByText(/finally \{/)).toBeVisible();
    fireEvent.keyDown(typescriptTab, { key: "Home" });
    expect(goTab).toHaveFocus();

    expect(createIdentity).toHaveBeenCalledWith({
      name: "billing-api",
      kind: "client",
      namespace: { env: "prod", app: "billing" },
      auth_methods: ["token"],
      cert_ttl_seconds: 0,
    });
  });

  it("continues through one-time downloads, SDK snippets, server trust, and policies", async () => {
    mocks.namespaces = [namespace(["mtls"])];
    const objectUrl = vi.spyOn(URL, "createObjectURL").mockReturnValue("blob:kms-credentials");
    const downloadedNames: string[] = [];
    vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(function (
      this: HTMLAnchorElement,
    ) {
      downloadedNames.push(this.download);
    });
    const createIdentity = vi.spyOn(api, "createIdentity").mockResolvedValue({
      identity: {
        name: "billing-api",
        kind: "client",
        namespace: { env: "prod", app: "billing" },
        has_token: false,
        certs: [],
      },
      cert: {
        cert_pem: "-----BEGIN CERTIFICATE-----\nclient\n-----END CERTIFICATE-----",
        key_pem: "-----BEGIN PRIVATE KEY-----\nprivate\n-----END PRIVATE KEY-----",
        serial: "1234",
        not_after_unix_ms: 4_102_444_800_000,
      },
    });
    const dialog = await openWizard();
    fireEvent.change(within(dialog).getByRole("textbox", { name: "Application identity name" }), {
      target: { value: "billing-api" },
    });
    selectApplication(dialog);
    fireEvent.click(within(dialog).getByRole("button", { name: "Continue to authentication" }));
    fireEvent.click(
      screen.getByRole("button", {
        name: "Create application credentials",
      }),
    );

    const saveDialog = await screen.findByRole("dialog", { name: "Save these credentials now" });
    const progress = within(saveDialog).getByRole("list", {
      name: "Connect application progress",
    });
    expect(within(progress).getAllByRole("listitem")).toHaveLength(4);
    expect(within(progress).getByText("Save credentials").closest("li")).toHaveAttribute(
      "aria-current",
      "step",
    );
    const downloadBoth = within(saveDialog).getByRole("button", {
      name: "Download both (.zip)",
    });
    expect(downloadBoth).toBeVisible();
    expect(
      within(saveDialog).getByText(/Download and deploy the application credential pair/),
    ).toBeVisible();
    fireEvent.click(downloadBoth);
    expect(objectUrl).toHaveBeenCalledOnce();
    expect(downloadedNames).toEqual(["billing-api-mtls-credentials.zip"]);
    const archive = objectUrl.mock.calls[0][0] as Blob;
    expect(archive).toBeInstanceOf(Blob);
    expect(archive.type).toBe("application/zip");
    const archiveBuffer = await archive.arrayBuffer();
    const archiveText = new TextDecoder().decode(archiveBuffer);
    expect(archiveText).toContain("billing-api.crt");
    expect(archiveText).toContain("billing-api.key");
    expect(centralDirectoryModes(archiveBuffer)).toEqual(
      new Map([
        ["billing-api.crt", { host: 3, mode: 0o100644 }],
        ["billing-api.key", { host: 3, mode: 0o100600 }],
      ]),
    );
    expect(within(saveDialog).getByText(/ZIP itself contains the private key/)).toBeVisible();
    expect(within(saveDialog).getByText(/delete the ZIP after secure deployment/)).toBeVisible();

    expect(createIdentity).toHaveBeenCalledWith({
      name: "billing-api",
      kind: "client",
      namespace: { env: "prod", app: "billing" },
      auth_methods: ["mtls"],
      cert_ttl_seconds: 90 * 86_400,
    });

    fireEvent.click(within(saveDialog).getByRole("button", { name: "I've saved it — continue" }));
    const integrationDialog = screen.getByRole("dialog", { name: "Integrate the application" });
    expect(within(integrationDialog).getByText("Integrate").closest("li")).toHaveAttribute(
      "aria-current",
      "step",
    );
    expect(within(integrationDialog).getByText(/You still need the KMS server CA/)).toBeVisible();
    expect(within(integrationDialog).getByText(/MTLSFromFiles/)).toBeVisible();

    fireEvent.click(within(integrationDialog).getByRole("tab", { name: "Python" }));
    expect(within(integrationDialog).getByText(/from kms_paramstore import/)).toBeVisible();
    fireEvent.click(within(integrationDialog).getByRole("tab", { name: "TypeScript" }));
    expect(within(integrationDialog).getByText(/mtlsFromFiles/)).toBeVisible();

    expect(within(integrationDialog).getByText(/writes need a policy/i)).toBeVisible();
    expect(within(integrationDialog).getByRole("link", { name: "Open policies" })).toHaveAttribute(
      "href",
      "/policies",
    );

    await waitFor(() => expect(mocks.toast.success).toHaveBeenCalled());
  });
});
