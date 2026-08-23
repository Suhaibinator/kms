import { ArrowLeft, ChevronRight, Plus, RefreshCw, SlidersHorizontal } from "lucide-react";
import Link from "next/link";
import { useRouter } from "next/router";
import { useCallback, useEffect, useId, useMemo, useState } from "react";
import { Icon } from "@/components/icons";
import { Modal } from "@/components/Modal";
import {
  Badge,
  Checkbox,
  EmptyState,
  Field,
  Input,
  PageHeader,
  Spinner,
  TableSkeleton,
  Textarea,
} from "@/components/ui";
import { AppSelect } from "@/components/ui/app-select";
import { Button, ButtonLink } from "@/components/ui/button";
import { useToast } from "@/context/ToastContext";
import { ApiError, api, isAbortError } from "@/lib/api";
import { utf8ToBase64 } from "@/lib/encoding";
import { useFieldErrors, useLatestRequest } from "@/lib/hooks";
import { links } from "@/lib/links";
import {
  type Application,
  type ApplicationConfigurationRow,
  type ApplicationContractField,
  type ApplicationDashboard,
  PARAMETER_CONTENT_TYPES,
} from "@/lib/types";
import {
  firstError,
  validateApplicationName,
  validateContract,
  validateEnv,
  validateKey,
  validateParameterValue,
  validateReleaseName,
  validateSchemaPin,
  validateValueSize,
} from "@/lib/validation";

const EMPTY_CONTRACT = `[
  {"alias":"runtime","kind":"parameter","content_type":"json"}
]`;

const LIST_HEADERS = ["Application", "Environments", "Release", "Schema", "Contract"];

// A stable empty list: a fresh `[]` per render would re-fire every memo and
// effect keyed on the environment list whenever no dashboard is loaded.
const NO_ENVIRONMENTS: ApplicationDashboard["environments"] = [];

/** Matches `prod`, `prod-*`, and `production`, but not `reproduction` or `non-prod`. */
const PRODUCTION_ENVIRONMENT = /^prod(-|$)|^production$/;

/** Tooltips are not scroll containers; a megabyte JSON value is not a tooltip. */
const TITLE_MAX_CHARS = 200;

function parseContract(raw: string): ApplicationContractField[] {
  const value: unknown = JSON.parse(raw);
  if (!Array.isArray(value)) throw new Error("Contract must be a JSON array.");
  return value as ApplicationContractField[];
}

/** The fields of the application form that carry their own validation. */
type CreateField = "name" | "releaseName" | "schemaPin" | "contract";

interface QuickSecretSeed {
  environment: string;
  key: string;
}

type DashboardStatus = "loading" | "success" | "not-found" | "error";

/**
 * The dashboard is keyed by the application it belongs to, so a response that
 * lands after the user has switched to another application can never be
 * rendered under the new header. `data` survives a refresh so the matrix does
 * not collapse to a skeleton while a reload is in flight.
 */
interface DashboardSlot {
  name: string;
  status: DashboardStatus;
  data: ApplicationDashboard | null;
}

/**
 * Validates the contract textarea: first that it is a JSON array at all, then
 * that every entry satisfies the server's per-field and duplicate-alias rules.
 */
function validateContractText(raw: string): string | null {
  let fields: ApplicationContractField[];
  try {
    fields = parseContract(raw);
  } catch (cause) {
    return cause instanceof Error ? cause.message : "Contract must be a JSON array.";
  }
  return validateContract(fields);
}

export default function ApplicationsPage() {
  const router = useRouter();
  const toast = useToast();
  const request = useLatestRequest();
  const selectedName = typeof router.query.app === "string" ? router.query.app : "";
  const [applications, setApplications] = useState<Application[]>([]);
  const [dashboard, setDashboard] = useState<DashboardSlot | null>(null);
  const [loading, setLoading] = useState(true);
  const [createOpen, setCreateOpen] = useState(false);
  const [editOpen, setEditOpen] = useState(false);
  const [environmentOpen, setEnvironmentOpen] = useState(false);
  const [secretSeed, setSecretSeed] = useState<QuickSecretSeed | null>(null);
  const [writeRow, setWriteRow] = useState<ApplicationConfigurationRow | null>(null);
  const [retryEnvironments, setRetryEnvironments] = useState<string[] | null>(null);
  const [saving, setSaving] = useState(false);

  const loadApplications = useCallback(async () => {
    const run = request.begin();
    setLoading(true);
    try {
      const response = await api.listApplications(200, undefined, { signal: run.signal });
      if (!run.current) return;
      setApplications(response.applications ?? []);
    } catch (error) {
      if (run.current && !isAbortError(error)) toast.error(error, "Failed to load applications");
    } finally {
      if (run.current) setLoading(false);
    }
  }, [request, toast]);

  const loadDashboard = useCallback(async () => {
    if (!selectedName) return;
    const run = request.begin();
    setLoading(true);
    setDashboard((current) =>
      current?.name === selectedName
        ? { ...current, status: "loading" }
        : { name: selectedName, status: "loading", data: null },
    );
    try {
      const data = await api.applicationDashboard(selectedName, { signal: run.signal });
      if (!run.current) return;
      setDashboard({ name: selectedName, status: "success", data });
    } catch (error) {
      if (!run.current || isAbortError(error)) return;
      if (error instanceof ApiError && error.status === 404) {
        setDashboard({ name: selectedName, status: "not-found", data: null });
        return;
      }
      setDashboard((current) => ({
        name: selectedName,
        status: "error",
        data: current?.name === selectedName ? current.data : null,
      }));
      toast.error(error, "Failed to load application");
    } finally {
      if (run.current) setLoading(false);
    }
  }, [request, selectedName, toast]);

  useEffect(() => {
    if (!router.isReady) return;
    if (selectedName) void loadDashboard();
    else void loadApplications();
  }, [router.isReady, selectedName, loadApplications, loadDashboard]);

  async function selectApplication(name: string) {
    await router.push({ pathname: "/applications", query: { app: name } });
  }

  function openWrite(row: ApplicationConfigurationRow) {
    setRetryEnvironments(null);
    setWriteRow(row);
  }

  function closeWrite() {
    setWriteRow(null);
    // A partial failure still wrote the environments that succeeded; the
    // matrix is refreshed once the user is done retrying, not underneath them.
    if (retryEnvironments) {
      setRetryEnvironments(null);
      void loadDashboard();
    }
  }

  // Derived above the early returns below: hooks must run in the same order on
  // every render, and the list view returns before reaching the detail view.
  const activeSlot = dashboard?.name === selectedName ? dashboard : null;
  const activeDashboard = activeSlot?.data ?? null;
  const environments = activeDashboard?.environments ?? NO_ENVIRONMENTS;
  const environmentNames = useMemo(
    () => environments.map((environment) => environment.env),
    [environments],
  );

  // On a static export the query is empty until the client router hydrates, so
  // a deep link to one application would paint the list for a frame.
  if (!router.isReady) {
    return <TableSkeleton headers={LIST_HEADERS} />;
  }

  if (!selectedName) {
    return (
      <>
        <PageHeader
          title="Applications"
          subtitle="One application owns a shared configuration contract; each environment supplies isolated values."
          actions={
            <Button onClick={() => setCreateOpen(true)}>
              <Plus size={15} />
              New application
            </Button>
          }
        />
        <div className="info-panel mb-16">
          Create an application once, then add environments such as <code>dev</code>,{" "}
          <code>prod</code>, and <code>prod-gcp</code>. Environments never inherit values from one
          another; the application contract keeps their release shape consistent.
        </div>
        {loading ? (
          <TableSkeleton headers={LIST_HEADERS} />
        ) : applications.length === 0 ? (
          <EmptyState
            icon={<Icon.application size={20} />}
            title="No applications yet"
            actions={<Button onClick={() => setCreateOpen(true)}>New application</Button>}
          >
            Define the application-owned shape before adding deployment environments.
          </EmptyState>
        ) : (
          <div className="table-wrap card-table">
            <table className="data">
              <thead>
                <tr>
                  {LIST_HEADERS.map((header) => (
                    <th key={header}>{header}</th>
                  ))}
                  <th />
                </tr>
              </thead>
              <tbody>
                {applications.map((app) => (
                  <tr key={app.name} className="application-row">
                    <td>
                      <Link
                        className="application-row-link"
                        href={{ pathname: "/applications", query: { app: app.name } }}
                        aria-label={`Manage ${app.name}`}
                      >
                        <strong className="mono">{app.name}</strong>
                      </Link>
                      <div className="faint text-sm">{app.description || "No description"}</div>
                    </td>
                    <td>{app.environment_count}</td>
                    <td className="mono">{app.release_name}</td>
                    <td className="mono">
                      {app.schema_id ? `${app.schema_id}@${app.schema_version}` : "—"}
                    </td>
                    <td>{app.contract.length} aliases</td>
                    <td className="application-row-chevron" aria-hidden="true">
                      <ChevronRight size={18} />
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
        <CreateApplicationModal
          open={createOpen}
          saving={saving}
          onClose={() => setCreateOpen(false)}
          onSave={async (application) => {
            setSaving(true);
            try {
              const response = await api.createApplication(application);
              toast.success(
                "Application created",
                `${response.application.name} is ready for environments.`,
              );
              setCreateOpen(false);
              await selectApplication(response.application.name);
            } catch (error) {
              toast.error(error, "Failed to create application");
            } finally {
              setSaving(false);
            }
          }}
        />
      </>
    );
  }

  if (activeSlot?.status === "not-found") {
    return (
      <>
        <PageHeader
          title="Application not found"
          documentTitle={selectedName}
          actions={
            <ButtonLink variant="outline" href={links.applications()}>
              <ArrowLeft size={16} aria-hidden /> Back to applications
            </ButtonLink>
          }
        />
        <EmptyState icon={<Icon.application size={20} />} title="Not found">
          No application named <span className="mono">{selectedName}</span> exists.
        </EmptyState>
      </>
    );
  }

  if (activeSlot?.status === "error" && !activeDashboard) {
    return (
      <>
        <PageHeader
          title="Could not load application"
          documentTitle={selectedName}
          actions={<Button onClick={() => void loadDashboard()}>Try again</Button>}
        />
        <EmptyState icon={<Icon.application size={20} />} title="Application unavailable">
          The server could not load <span className="mono">{selectedName}</span>. Check the
          connection and try again.
        </EmptyState>
      </>
    );
  }

  return (
    <>
      <PageHeader
        title={
          <span className="row-wrap">
            <Button
              variant="ghost"
              size="sm"
              aria-label="Back to applications"
              onClick={() => void router.push("/applications")}
            >
              <ArrowLeft size={16} />
            </Button>
            <span className="mono">{selectedName}</span>
          </span>
        }
        documentTitle={selectedName}
        subtitle={
          activeDashboard?.application.description ||
          "Application configuration across environments."
        }
        actions={
          <>
            <Button variant="outline" onClick={() => void loadDashboard()} disabled={loading}>
              <RefreshCw size={15} />
              Refresh
            </Button>
            <Button variant="outline" onClick={() => setEditOpen(true)} disabled={!activeDashboard}>
              <SlidersHorizontal size={15} />
              Edit contract
            </Button>
            <Button onClick={() => setEnvironmentOpen(true)}>
              <Plus size={15} />
              Add environment
            </Button>
          </>
        }
      />
      {activeDashboard ? <ContractSummary application={activeDashboard.application} /> : null}
      {!activeDashboard ? (
        <TableSkeleton headers={["Key", "Kind", "Environment"]} />
      ) : environments.length === 0 ? (
        <EmptyState
          icon={<Icon.namespace size={20} />}
          title="No environments"
          actions={<Button onClick={() => setEnvironmentOpen(true)}>Add environment</Button>}
        >
          Add dev, staging, production, or a provider-specific environment to begin managing values.
        </EmptyState>
      ) : (
        <>
          <div className="between mb-8">
            <div>
              <h2>Configuration matrix</h2>
              <div className="faint text-sm">
                Parameters show current values; secrets show metadata only. A bulk parameter update
                creates an independent version in every selected environment.
              </div>
            </div>
            <div className="row-wrap">
              <Button variant="outline" onClick={() => setSecretSeed({ environment: "", key: "" })}>
                <Plus size={15} />
                New secret
              </Button>
              <Button
                variant="outline"
                onClick={() => openWrite({ key: "", kind: "parameter", environments: {} })}
              >
                <Plus size={15} />
                New parameter
              </Button>
            </div>
          </div>
          <div className="table-wrap application-matrix">
            <table className="data">
              <thead>
                <tr>
                  <th className="matrix-key">Key</th>
                  <th>Kind</th>
                  {environments.map((env) => (
                    <th key={env.env}>{env.env}</th>
                  ))}
                  <th />
                </tr>
              </thead>
              <tbody>
                {activeDashboard.rows.map((row) => (
                  <tr key={`${row.kind}:${row.key}`}>
                    <td className="mono matrix-key">{row.key}</td>
                    <td>
                      <Badge kind={row.kind === "secret" ? "warning" : "accent"}>{row.kind}</Badge>
                    </td>
                    {environments.map((env) => (
                      <td key={env.env}>
                        <MatrixCell
                          row={row}
                          environment={env.env}
                          app={selectedName}
                          onAddSecret={(environment, key) => setSecretSeed({ environment, key })}
                        />
                      </td>
                    ))}
                    <td>
                      {row.kind === "parameter" ? (
                        <Button variant="outline" size="sm" onClick={() => openWrite(row)}>
                          <SlidersHorizontal size={14} />
                          Edit
                        </Button>
                      ) : null}
                    </td>
                  </tr>
                ))}
                {activeDashboard.rows.length === 0 ? (
                  <tr>
                    <td colSpan={environments.length + 3} className="faint">
                      No parameters or secrets have been created.
                    </td>
                  </tr>
                ) : null}
              </tbody>
            </table>
          </div>
        </>
      )}
      <AddEnvironmentModal
        app={selectedName}
        open={environmentOpen}
        saving={saving}
        onClose={() => setEnvironmentOpen(false)}
        onSave={async (environment, description, methods) => {
          setSaving(true);
          try {
            await api.createNamespace({
              env: environment,
              app: selectedName,
              description,
              allowed_auth_methods: methods,
            });
            toast.success("Environment added", `${environment}/${selectedName} is ready.`);
            setEnvironmentOpen(false);
            await loadDashboard();
          } catch (error) {
            toast.error(error, "Failed to add environment");
          } finally {
            setSaving(false);
          }
        }}
      />
      <CreateApplicationModal
        open={editOpen}
        saving={saving}
        initial={activeDashboard?.application}
        onClose={() => setEditOpen(false)}
        onSave={async (application) => {
          setSaving(true);
          try {
            await api.updateApplication(application);
            toast.success("Application contract updated");
            setEditOpen(false);
            await loadDashboard();
          } catch (error) {
            toast.error(error, "Failed to update application");
          } finally {
            setSaving(false);
          }
        }}
      />
      <QuickSecretModal
        app={selectedName}
        environments={environmentNames}
        seed={secretSeed}
        saving={saving}
        onClose={() => setSecretSeed(null)}
        onSave={async (request) => {
          setSaving(true);
          try {
            const response = await api.createSecret({
              env: request.environment,
              app: selectedName,
              key: request.key,
              value_base64: utf8ToBase64(request.value),
              content_type: request.contentType,
              metadata_json: "{}",
              client_bound: false,
              generate_access_token: false,
              expires_at_unix_ms: 0,
            });
            toast.success(
              `Secret created (version ${response.version})`,
              `${selectedName} · ${request.environment} · ${request.key}`,
            );
            setSecretSeed(null);
            await loadDashboard();
          } catch (error) {
            toast.error(error, "Failed to create secret");
          } finally {
            setSaving(false);
          }
        }}
      />
      <BulkParameterModal
        app={selectedName}
        environments={environmentNames}
        row={writeRow}
        retryEnvironments={retryEnvironments}
        saving={saving}
        onClose={closeWrite}
        onSave={async (request) => {
          setSaving(true);
          try {
            const response = await api.putApplicationParameter(request);
            const failures = response.results.filter((result) => result.error);
            if (failures.length === 0) {
              toast.success(
                "Values updated",
                `Created independent versions in ${response.results.length} environment(s).`,
              );
              setWriteRow(null);
              setRetryEnvironments(null);
              await loadDashboard();
              return;
            }
            toast.error(
              new Error(
                failures.map((result) => `${result.environment}: ${result.error}`).join("; "),
              ),
              "Some environments failed",
            );
            // Keep the modal and its edits; narrow the targets to what failed.
            setRetryEnvironments(failures.map((result) => result.environment));
          } catch (error) {
            toast.error(error, "Failed to update values");
          } finally {
            setSaving(false);
          }
        }}
      />
    </>
  );
}

function ContractSummary({ application }: { application: Application }) {
  return (
    <div className="card mb-16 application-contract">
      <div>
        <span className="faint text-sm">Canonical release</span>
        <strong className="mono">{application.release_name}</strong>
      </div>
      <div>
        <span className="faint text-sm">Schema</span>
        <strong className="mono">
          {application.schema_id
            ? `${application.schema_id}@${application.schema_version}`
            : "Not pinned"}
        </strong>
      </div>
      <div>
        <span className="faint text-sm">Shared shape</span>
        <div className="row-wrap">
          {application.contract.length ? (
            application.contract.map((field) => (
              <Badge key={field.alias} kind={field.kind === "secret" ? "warning" : "neutral"}>
                {field.alias}: {field.kind}
                {field.content_type ? `/${field.content_type}` : ""}
              </Badge>
            ))
          ) : (
            <span className="faint">No enforced aliases</span>
          )}
        </div>
      </div>
    </div>
  );
}

function MatrixCell({
  row,
  environment,
  app,
  onAddSecret,
}: {
  row: ApplicationConfigurationRow;
  environment: string;
  app: string;
  onAddSecret: (environment: string, key: string) => void;
}) {
  const cell = row.environments[environment];
  if (!cell?.present) {
    if (row.kind === "secret") {
      return (
        <Button
          type="button"
          variant="ghost"
          size="sm"
          onClick={() => onAddSecret(environment, row.key)}
        >
          <Plus size={13} />
          Add secret
        </Button>
      );
    }
    return <Badge kind="danger">missing</Badge>;
  }
  if (row.kind === "secret")
    return (
      <Link href={links.secretDetail({ env: environment, app, key: row.key })}>
        <span className="secret-cell">
          Secret v{cell.version}
          {cell.client_bound ? " · client-bound" : ""}
        </span>
      </Link>
    );
  const value = cell.value ?? "";
  const title = value.length > TITLE_MAX_CHARS ? `${value.slice(0, TITLE_MAX_CHARS)}…` : value;
  return (
    <div className="matrix-value">
      <span className="mono" title={title}>
        {value === "" ? "(empty)" : value}
      </span>
      <span className="faint text-sm">
        v{cell.version} · {cell.content_type}
      </span>
    </div>
  );
}

function CreateApplicationModal({
  open,
  saving,
  initial,
  onClose,
  onSave,
}: {
  open: boolean;
  saving: boolean;
  initial?: Application | null;
  onClose: () => void;
  onSave: (app: {
    name: string;
    description: string;
    release_name: string;
    schema_id: string;
    schema_version: number;
    contract: ApplicationContractField[];
  }) => Promise<void>;
}) {
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [releaseName, setReleaseName] = useState("runtime");
  const [schemaID, setSchemaID] = useState("");
  const [schemaVersion, setSchemaVersion] = useState("");
  const [contract, setContract] = useState(EMPTY_CONTRACT);
  const [error, setError] = useState("");
  const { touch, markAllTouched, reset, shown } = useFieldErrors<CreateField>();
  // The submit button lives in the modal footer, outside the form element; the
  // HTML `form` attribute is what still makes Enter in the body submit it.
  const formId = useId();
  useEffect(() => {
    if (!open) return;
    setName(initial?.name ?? "");
    setDescription(initial?.description ?? "");
    setReleaseName(initial?.release_name ?? "runtime");
    setSchemaID(initial?.schema_id ?? "");
    setSchemaVersion(initial?.schema_version ? String(initial.schema_version) : "");
    setContract(initial ? JSON.stringify(initial.contract, null, 2) : EMPTY_CONTRACT);
    setError("");
    reset();
  }, [open, initial, reset]);

  // Three different naming rules meet on this form, so each field is checked
  // against its own: the application name is a namespace label, the release
  // name is a relative key, and the contract aliases have their own grammar.
  const nameProblem = validateApplicationName(name.trim());
  const releaseNameProblem = validateReleaseName(releaseName.trim());
  const schemaPinProblem = validateSchemaPin(schemaID, schemaVersion);
  const contractProblem = validateContractText(contract);
  const blocking = firstError(
    // An existing application's name is fixed and its input disabled, so a
    // legacy name that predates the current rule cannot block an edit here.
    initial ? null : nameProblem,
    releaseNameProblem,
    schemaPinProblem,
    contractProblem,
  );
  const shownSchemaPinProblem = shown("schemaPin", schemaPinProblem);

  function submit() {
    markAllTouched();
    if (saving || blocking) return;
    try {
      setError("");
      void onSave({
        name,
        description,
        release_name: releaseName,
        schema_id: schemaID,
        schema_version: Number(schemaVersion || 0),
        contract: parseContract(contract),
      });
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "Invalid contract");
    }
  }

  return (
    <Modal
      open={open}
      title={initial ? `Edit ${initial.name}` : "New application"}
      onClose={onClose}
      dismissible={!saving}
      wide
      footer={
        <>
          <Button type="button" variant="outline" onClick={onClose} disabled={saving}>
            Cancel
          </Button>
          <Button form={formId} type="submit" disabled={saving || blocking !== null}>
            {saving ? <Spinner /> : null}
            {initial ? "Save contract" : "Create application"}
          </Button>
        </>
      }
    >
      <form
        id={formId}
        onSubmit={(event) => {
          event.preventDefault();
          submit();
        }}
      >
        <div className="info-panel mb-16">
          The application owns this shape. Every environment release must use the same release name,
          schema pin, aliases, kinds, and parameter content types.
        </div>
        <div className="form-row">
          <Field
            label="Application name"
            hint="Lowercase letters, digits, and hyphens."
            error={initial ? null : shown("name", nameProblem)}
          >
            <Input
              className="font-mono"
              value={name}
              disabled={Boolean(initial)}
              onChange={(event) => setName(event.target.value)}
              onBlur={() => touch("name")}
              placeholder="payments-api"
            />
          </Field>
          <Field
            label="Release name"
            hint="Defaults to runtime when left blank."
            error={shown("releaseName", releaseNameProblem)}
          >
            <Input
              className="font-mono"
              value={releaseName}
              onChange={(event) => setReleaseName(event.target.value)}
              onBlur={() => touch("releaseName")}
            />
          </Field>
        </div>
        <Field label="Description">
          <Input value={description} onChange={(event) => setDescription(event.target.value)} />
        </Field>
        <div className="form-row">
          <Field
            label="Schema ID"
            hint="Optional; specify both ID and version."
            error={shownSchemaPinProblem}
          >
            <Input
              className="font-mono"
              value={schemaID}
              onChange={(event) => setSchemaID(event.target.value)}
              onBlur={() => touch("schemaPin")}
            />
          </Field>
          <Field label="Schema version">
            <Input
              type="number"
              min={1}
              value={schemaVersion}
              // The pin is one rule across two inputs; the message sits under
              // Schema ID, but both controls are part of the invalid pair.
              aria-invalid={shownSchemaPinProblem ? true : undefined}
              onChange={(event) => setSchemaVersion(event.target.value)}
              onBlur={() => touch("schemaPin")}
            />
          </Field>
        </div>
        <Field
          label="Shared release contract"
          hint="JSON array of {alias, kind, content_type}. Secrets omit content_type."
          error={shown("contract", contractProblem)}
        >
          <Textarea
            className="font-mono"
            rows={9}
            value={contract}
            onChange={(event) => setContract(event.target.value)}
            onBlur={() => touch("contract")}
          />
        </Field>
        {error ? <div className="danger-panel">{error}</div> : null}
      </form>
    </Modal>
  );
}

function AddEnvironmentModal({
  app,
  open,
  saving,
  onClose,
  onSave,
}: {
  app: string;
  open: boolean;
  saving: boolean;
  onClose: () => void;
  onSave: (
    environment: string,
    description: string,
    methods: ("mtls" | "token")[],
  ) => Promise<void>;
}) {
  const [environment, setEnvironment] = useState("");
  const [description, setDescription] = useState("");
  const [token, setToken] = useState(false);
  const { touch, markAllTouched, reset, shown } = useFieldErrors<"environment">();
  const formId = useId();
  // An environment is the env half of a namespace, so it follows the label rule.
  const environmentProblem = validateEnv(environment.trim());
  useEffect(() => {
    if (!open) return;
    setEnvironment("");
    setDescription("");
    setToken(false);
    reset();
  }, [open, reset]);

  function submit() {
    markAllTouched();
    if (saving || environmentProblem) return;
    void onSave(environment, description, token ? ["mtls", "token"] : ["mtls"]);
  }

  return (
    <Modal
      open={open}
      title={`Add environment to ${app}`}
      onClose={onClose}
      dismissible={!saving}
      footer={
        <>
          <Button type="button" variant="outline" onClick={onClose} disabled={saving}>
            Cancel
          </Button>
          <Button form={formId} type="submit" disabled={saving || environmentProblem !== null}>
            {saving ? <Spinner /> : null}Add environment
          </Button>
        </>
      }
    >
      <form
        id={formId}
        onSubmit={(event) => {
          event.preventDefault();
          submit();
        }}
      >
        <Field
          label="Environment"
          hint="Examples: dev, staging, prod, prod-gcp"
          error={shown("environment", environmentProblem)}
        >
          <Input
            className="font-mono"
            value={environment}
            onChange={(event) => setEnvironment(event.target.value)}
            onBlur={() => touch("environment")}
            placeholder="prod-gcp"
          />
        </Field>
        <Field label="Description">
          <Input value={description} onChange={(event) => setDescription(event.target.value)} />
        </Field>
        <div className="checkbox-row">
          <Checkbox id="allow-environment-token" checked={token} onCheckedChange={setToken} />
          <label htmlFor="allow-environment-token">
            <strong>Also allow bearer tokens</strong>
            <span className="faint text-sm">mTLS is always enabled and recommended.</span>
          </label>
        </div>
      </form>
    </Modal>
  );
}

type QuickSecretField = "environment" | "key" | "value";

function QuickSecretModal({
  app,
  environments,
  seed,
  saving,
  onClose,
  onSave,
}: {
  app: string;
  environments: string[];
  seed: QuickSecretSeed | null;
  saving: boolean;
  onClose: () => void;
  onSave: (request: {
    environment: string;
    key: string;
    value: string;
    contentType: string;
  }) => Promise<void>;
}) {
  const [environment, setEnvironment] = useState("");
  const [key, setKey] = useState("");
  const [value, setValue] = useState("");
  const [contentType, setContentType] = useState("text/plain");
  const { touch, markAllTouched, reset, shown } = useFieldErrors<QuickSecretField>();
  const formId = useId();

  useEffect(() => {
    if (!seed) return;
    setEnvironment(seed.environment || (environments.length === 1 ? environments[0] : ""));
    setKey(seed.key);
    setValue("");
    setContentType("text/plain");
    reset();
  }, [seed, environments, reset]);

  const environmentProblem = environment ? null : "Choose an environment.";
  const keyProblem = validateKey(key.trim());
  const valueProblem = value ? validateValueSize(value) : "Secret value is required.";
  const blocking = firstError(environmentProblem, keyProblem, valueProblem);
  const advancedHref = {
    pathname: "/secrets/new",
    query: {
      ...(environment ? { env: environment } : {}),
      app,
      ...(key.trim() ? { key: key.trim() } : {}),
    },
  };

  function submit() {
    markAllTouched();
    if (saving || blocking) return;
    void onSave({
      environment,
      key: key.trim(),
      value,
      // The server defaults a blank content type; the form does not second-guess it.
      contentType: contentType.trim() || "text/plain",
    });
  }

  return (
    <Modal
      open={seed !== null}
      title="New secret"
      onClose={onClose}
      dismissible={!saving}
      footer={
        <>
          <Button type="button" variant="outline" onClick={onClose} disabled={saving}>
            Cancel
          </Button>
          <Button form={formId} type="submit" disabled={saving || blocking !== null}>
            {saving ? <Spinner /> : null}
            Create secret
          </Button>
        </>
      }
    >
      <form
        id={formId}
        onSubmit={(event) => {
          event.preventDefault();
          submit();
        }}
      >
        <div className="form-row">
          <Field label="Application">
            <Input className="font-mono" value={app} disabled />
          </Field>
          <Field label="Environment" error={shown("environment", environmentProblem)}>
            <AppSelect
              className="font-mono"
              value={environment}
              onValueChange={setEnvironment}
              onBlur={() => touch("environment")}
              placeholder="Select environment…"
              options={environments.map((item) => ({ value: item, label: item }))}
            />
          </Field>
        </div>
        <Field
          label="Secret key"
          hint="Examples: stripe-api-key or billing/webhook-secret"
          error={shown("key", keyProblem)}
        >
          <Input
            className="font-mono"
            value={key}
            onChange={(event) => setKey(event.target.value)}
            onBlur={() => touch("key")}
            placeholder="stripe-api-key"
          />
        </Field>
        <Field
          label="Secret value"
          hint="The value is encrypted before it is stored and is never shown in the matrix."
          error={shown("value", valueProblem)}
        >
          <Textarea
            className="font-mono"
            rows={5}
            value={value}
            onChange={(event) => setValue(event.target.value)}
            onBlur={() => touch("value")}
            spellCheck={false}
            autoComplete="off"
          />
        </Field>
        <Field label="Content type" hint="Defaults to text/plain when left blank.">
          <Input
            className="font-mono"
            value={contentType}
            onChange={(event) => setContentType(event.target.value)}
          />
        </Field>
        <div className="quick-secret-advanced text-sm">
          Need expiration, metadata, an access token, or client-bound protection?{" "}
          <Link href={advancedHref}>Open advanced secret options</Link>.
        </div>
      </form>
    </Modal>
  );
}

function BulkParameterModal({
  app,
  environments,
  row,
  retryEnvironments,
  saving,
  onClose,
  onSave,
}: {
  app: string;
  environments: string[];
  row: ApplicationConfigurationRow | null;
  /** After a partial failure: the environments still to write. Narrows the selection only. */
  retryEnvironments: string[] | null;
  saving: boolean;
  onClose: () => void;
  onSave: (request: {
    application: string;
    key: string;
    value: string;
    content_type: string;
    metadata_json: string;
    environments: string[];
  }) => Promise<void>;
}) {
  const [key, setKey] = useState("");
  const [value, setValue] = useState("");
  const [contentType, setContentType] = useState("string");
  const [selected, setSelected] = useState<string[]>([]);
  const { touch, markAllTouched, reset, shown } = useFieldErrors<"key" | "value">();
  const formId = useId();
  useEffect(() => {
    if (!row) return;
    reset();
    setKey(row.key);
    const present = environments.filter((environment) => row.environments[environment]?.present);
    setSelected(present.length ? present : environments);
    const first = present.length ? row.environments[present[0]] : undefined;
    setValue(first?.value ?? "");
    setContentType(first?.content_type ?? "string");
  }, [row, environments, reset]);
  useEffect(() => {
    if (retryEnvironments) setSelected(retryEnvironments);
  }, [retryEnvironments]);
  const allSelected = selected.length === environments.length;
  // The same value is written to every selected environment, so it only has to
  // parse once. Memoised because a JSON document may run to a megabyte.
  const keyProblem = validateKey(key.trim());
  const valueProblem = useMemo(
    () => firstError(validateValueSize(value), validateParameterValue(value, contentType)),
    [value, contentType],
  );
  // An existing key's input is disabled, so a legacy key cannot block an edit.
  const blocking = firstError(row?.key ? null : keyProblem, valueProblem);
  const differing = useMemo(
    () =>
      row
        ? new Set(
            environments
              .map((environment) => row.environments[environment]?.value)
              .filter((item) => item !== undefined),
          ).size > 1
        : false,
    [row, environments],
  );

  function submit() {
    markAllTouched();
    if (saving || blocking || selected.length === 0) return;
    void onSave({
      application: app,
      key,
      value,
      content_type: contentType,
      metadata_json: "{}",
      environments: selected,
    });
  }

  return (
    <Modal
      open={row !== null}
      title={row?.key ? `Update ${row.key}` : "New parameter"}
      onClose={onClose}
      dismissible={!saving}
      wide
      footer={
        <>
          <Button type="button" variant="outline" onClick={onClose} disabled={saving}>
            Cancel
          </Button>
          <Button
            form={formId}
            type="submit"
            disabled={saving || blocking !== null || selected.length === 0}
          >
            {saving ? <Spinner /> : null}Apply to {selected.length} environment(s)
          </Button>
        </>
      }
    >
      <form
        id={formId}
        onSubmit={(event) => {
          event.preventDefault();
          submit();
        }}
      >
        <div className="warn-panel mb-16">
          <strong>Separate versions will be created.</strong> This does not link environments or
          create shared mutable state. Verify production targets before applying.
          {differing
            ? " Existing values differ; the editor starts from the first selected environment."
            : ""}
        </div>
        <div className="form-row">
          <Field label="Key" error={row?.key ? null : shown("key", keyProblem)}>
            <Input
              className="font-mono"
              value={key}
              disabled={Boolean(row?.key)}
              onChange={(event) => setKey(event.target.value)}
              onBlur={() => touch("key")}
            />
          </Field>
          <Field label="Content type">
            <AppSelect
              value={contentType}
              onValueChange={setContentType}
              options={PARAMETER_CONTENT_TYPES.map((type) => ({ value: type, label: type }))}
            />
          </Field>
        </div>
        <Field label="Value" error={shown("value", valueProblem)}>
          <Textarea
            className="font-mono"
            rows={7}
            value={value}
            onChange={(event) => setValue(event.target.value)}
            onBlur={() => touch("value")}
          />
        </Field>
        <Field label="Target environments">
          <div className="checkbox-row">
            <Checkbox
              id="all-target-environments"
              checked={allSelected}
              onCheckedChange={(checked) => setSelected(checked ? environments : [])}
            />
            <label htmlFor="all-target-environments">
              <strong>All environments</strong>
            </label>
          </div>
          <div className="environment-check-grid">
            {environments.map((environment) => (
              <div className="checkbox-row" key={environment}>
                <Checkbox
                  id={`target-environment-${environment}`}
                  checked={selected.includes(environment)}
                  onCheckedChange={(checked) =>
                    setSelected((current) =>
                      checked
                        ? [...current, environment]
                        : current.filter((item) => item !== environment),
                    )
                  }
                />
                <label className="mono" htmlFor={`target-environment-${environment}`}>
                  {environment}
                </label>
                {PRODUCTION_ENVIRONMENT.test(environment) ? (
                  <Badge kind="warning">production</Badge>
                ) : null}
              </div>
            ))}
          </div>
        </Field>
      </form>
    </Modal>
  );
}
