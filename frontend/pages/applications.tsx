import Link from "next/link";
import { useRouter } from "next/router";
import { useCallback, useEffect, useMemo, useState } from "react";
import { ArrowLeft, Plus, RefreshCw, SlidersHorizontal } from "lucide-react";
import { Icon } from "@/components/icons";
import { Modal } from "@/components/Modal";
import { Badge, EmptyState, Field, PageHeader, Spinner, TableSkeleton } from "@/components/ui";
import { useToast } from "@/context/ToastContext";
import { api, isAbortError } from "@/lib/api";
import {
  PARAMETER_CONTENT_TYPES,
  type Application,
  type ApplicationConfigurationRow,
  type ApplicationContractField,
  type ApplicationDashboard,
} from "@/lib/types";

const EMPTY_CONTRACT = `[
  {"alias":"runtime","kind":"parameter","content_type":"json"}
]`;

function parseContract(raw: string): ApplicationContractField[] {
  const value: unknown = JSON.parse(raw);
  if (!Array.isArray(value)) throw new Error("Contract must be a JSON array.");
  return value as ApplicationContractField[];
}

export default function ApplicationsPage() {
  const router = useRouter();
  const toast = useToast();
  const selectedName = typeof router.query.app === "string" ? router.query.app : "";
  const [applications, setApplications] = useState<Application[]>([]);
  const [dashboard, setDashboard] = useState<ApplicationDashboard | null>(null);
  const [loading, setLoading] = useState(true);
  const [createOpen, setCreateOpen] = useState(false);
  const [editOpen, setEditOpen] = useState(false);
  const [environmentOpen, setEnvironmentOpen] = useState(false);
  const [writeRow, setWriteRow] = useState<ApplicationConfigurationRow | null>(null);
  const [saving, setSaving] = useState(false);

  const loadApplications = useCallback(async () => {
    setLoading(true);
    try {
      const response = await api.listApplications(200);
      setApplications(response.applications ?? []);
    } catch (error) {
      if (!isAbortError(error)) toast.error(error, "Failed to load applications");
    } finally {
      setLoading(false);
    }
  }, [toast]);

  const loadDashboard = useCallback(async () => {
    if (!selectedName) {
      setDashboard(null);
      return;
    }
    setLoading(true);
    try {
      setDashboard(await api.applicationDashboard(selectedName));
    } catch (error) {
      if (!isAbortError(error)) toast.error(error, "Failed to load application");
    } finally {
      setLoading(false);
    }
  }, [selectedName, toast]);

  useEffect(() => {
    if (!router.isReady) return;
    if (selectedName) void loadDashboard();
    else void loadApplications();
  }, [router.isReady, selectedName, loadApplications, loadDashboard]);

  async function selectApplication(name: string) {
    await router.push({ pathname: "/applications", query: { app: name } });
  }

  if (!selectedName) {
    return (
      <>
        <PageHeader
          title="Applications"
          subtitle="One application owns a shared configuration contract; each environment supplies isolated values."
          actions={
            <button className="btn btn-primary" onClick={() => setCreateOpen(true)}>
              <Plus size={15} />
              New application
            </button>
          }
        />
        <div className="info-panel mb-16">
          Create an application once, then add environments such as <code>dev</code>,{" "}
          <code>prod</code>, and <code>prod-gcp</code>. Environments never inherit values from one
          another; the application contract keeps their release shape consistent.
        </div>
        {loading ? (
          <TableSkeleton
            headers={["Application", "Environments", "Release", "Schema", "Contract"]}
          />
        ) : applications.length === 0 ? (
          <EmptyState
            icon={<Icon.application size={20} />}
            title="No applications yet"
            actions={
              <button className="btn btn-primary" onClick={() => setCreateOpen(true)}>
                New application
              </button>
            }
          >
            Define the application-owned shape before adding deployment environments.
          </EmptyState>
        ) : (
          <div className="table-wrap card-table">
            <table className="data">
              <thead>
                <tr>
                  <th>Application</th>
                  <th>Environments</th>
                  <th>Release</th>
                  <th>Schema</th>
                  <th>Contract</th>
                  <th />
                </tr>
              </thead>
              <tbody>
                {applications.map((app) => (
                  <tr key={app.name}>
                    <td>
                      <strong className="mono">{app.name}</strong>
                      <div className="faint text-sm">{app.description || "No description"}</div>
                    </td>
                    <td>{app.environment_count}</td>
                    <td className="mono">{app.release_name}</td>
                    <td className="mono">
                      {app.schema_id ? `${app.schema_id}@${app.schema_version}` : "—"}
                    </td>
                    <td>{app.contract.length} aliases</td>
                    <td>
                      <button
                        className="btn btn-sm"
                        onClick={() => void selectApplication(app.name)}
                      >
                        Manage
                      </button>
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

  const environments = dashboard?.environments ?? [];
  return (
    <>
      <PageHeader
        title={
          <span className="row-wrap">
            <button
              className="btn btn-ghost btn-sm"
              aria-label="Back to applications"
              onClick={() => void router.push("/applications")}
            >
              <ArrowLeft size={16} />
            </button>
            <span className="mono">{selectedName}</span>
          </span>
        }
        documentTitle={selectedName}
        subtitle={
          dashboard?.application.description || "Application configuration across environments."
        }
        actions={
          <>
            <button className="btn" onClick={() => void loadDashboard()} disabled={loading}>
              <RefreshCw size={15} />
              Refresh
            </button>
            <button className="btn" onClick={() => setEditOpen(true)} disabled={!dashboard}>
              <SlidersHorizontal size={15} />
              Edit contract
            </button>
            <button className="btn btn-primary" onClick={() => setEnvironmentOpen(true)}>
              <Plus size={15} />
              Add environment
            </button>
          </>
        }
      />
      {dashboard ? <ContractSummary application={dashboard.application} /> : null}
      {loading && !dashboard ? (
        <TableSkeleton headers={["Key", "Kind", "Environment"]} />
      ) : environments.length === 0 ? (
        <EmptyState
          icon={<Icon.namespace size={20} />}
          title="No environments"
          actions={
            <button className="btn btn-primary" onClick={() => setEnvironmentOpen(true)}>
              Add environment
            </button>
          }
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
            <button
              className="btn"
              onClick={() => setWriteRow({ key: "", kind: "parameter", environments: {} })}
            >
              <Plus size={15} />
              New parameter
            </button>
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
                {(dashboard?.rows ?? []).map((row) => (
                  <tr key={`${row.kind}:${row.key}`}>
                    <td className="mono matrix-key">{row.key}</td>
                    <td>
                      <Badge kind={row.kind === "secret" ? "warning" : "accent"}>{row.kind}</Badge>
                    </td>
                    {environments.map((env) => (
                      <td key={env.env}>
                        <MatrixCell row={row} environment={env.env} app={selectedName} />
                      </td>
                    ))}
                    <td>
                      {row.kind === "parameter" ? (
                        <button className="btn btn-sm" onClick={() => setWriteRow(row)}>
                          <SlidersHorizontal size={14} />
                          Edit
                        </button>
                      ) : null}
                    </td>
                  </tr>
                ))}
                {(dashboard?.rows ?? []).length === 0 ? (
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
        initial={dashboard?.application}
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
      <BulkParameterModal
        app={selectedName}
        environments={environments.map((env) => env.env)}
        row={writeRow}
        saving={saving}
        onClose={() => setWriteRow(null)}
        onSave={async (request) => {
          setSaving(true);
          try {
            const response = await api.putApplicationParameter(request);
            const failures = response.results.filter((result) => result.error);
            if (failures.length)
              toast.error(
                new Error(
                  failures.map((result) => `${result.environment}: ${result.error}`).join("; "),
                ),
                "Some environments failed",
              );
            else
              toast.success(
                "Values updated",
                `Created independent versions in ${response.results.length} environment(s).`,
              );
            if (failures.length === 0) setWriteRow(null);
            await loadDashboard();
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
}: {
  row: ApplicationConfigurationRow;
  environment: string;
  app: string;
}) {
  const cell = row.environments[environment];
  if (!cell?.present) return <span className="badge badge-danger">missing</span>;
  if (row.kind === "secret")
    return (
      <Link
        href={`/secrets/detail?env=${encodeURIComponent(environment)}&app=${encodeURIComponent(app)}&key=${encodeURIComponent(row.key)}`}
      >
        <span className="secret-cell">
          Secret v{cell.version}
          {cell.client_bound ? " · client-bound" : ""}
        </span>
      </Link>
    );
  return (
    <div className="matrix-value">
      <span className="mono" title={cell.value}>
        {cell.value === "" ? "(empty)" : cell.value}
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
  useEffect(() => {
    if (!open) return;
    setName(initial?.name ?? "");
    setDescription(initial?.description ?? "");
    setReleaseName(initial?.release_name ?? "runtime");
    setSchemaID(initial?.schema_id ?? "");
    setSchemaVersion(initial?.schema_version ? String(initial.schema_version) : "");
    setContract(initial ? JSON.stringify(initial.contract, null, 2) : EMPTY_CONTRACT);
    setError("");
  }, [open, initial]);
  return (
    <Modal
      open={open}
      title={initial ? `Edit ${initial.name}` : "New application"}
      onClose={onClose}
      wide
      footer={
        <>
          <button className="btn" onClick={onClose} disabled={saving}>
            Cancel
          </button>
          <button
            className="btn btn-primary"
            disabled={saving}
            onClick={() => {
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
            }}
          >
            {saving ? <Spinner /> : null}
            {initial ? "Save contract" : "Create application"}
          </button>
        </>
      }
    >
      <div className="info-panel mb-16">
        The application owns this shape. Every environment release must use the same release name,
        schema pin, aliases, kinds, and parameter content types.
      </div>
      <div className="form-row">
        <Field label="Application name" hint="Lowercase letters, digits, and hyphens.">
          <input
            className="input mono"
            value={name}
            disabled={Boolean(initial)}
            onChange={(event) => setName(event.target.value)}
            placeholder="payments-api"
          />
        </Field>
        <Field label="Release name">
          <input
            className="input mono"
            value={releaseName}
            onChange={(event) => setReleaseName(event.target.value)}
          />
        </Field>
      </div>
      <Field label="Description">
        <input
          className="input"
          value={description}
          onChange={(event) => setDescription(event.target.value)}
        />
      </Field>
      <div className="form-row">
        <Field label="Schema ID" hint="Optional; specify both ID and version.">
          <input
            className="input mono"
            value={schemaID}
            onChange={(event) => setSchemaID(event.target.value)}
          />
        </Field>
        <Field label="Schema version">
          <input
            className="input"
            type="number"
            min={1}
            value={schemaVersion}
            onChange={(event) => setSchemaVersion(event.target.value)}
          />
        </Field>
      </div>
      <Field
        label="Shared release contract"
        hint="JSON array of {alias, kind, content_type}. Secrets omit content_type."
      >
        <textarea
          className="input mono"
          rows={9}
          value={contract}
          onChange={(event) => setContract(event.target.value)}
        />
      </Field>
      {error ? <div className="danger-panel">{error}</div> : null}
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
  return (
    <Modal
      open={open}
      title={`Add environment to ${app}`}
      onClose={onClose}
      footer={
        <>
          <button className="btn" onClick={onClose} disabled={saving}>
            Cancel
          </button>
          <button
            className="btn btn-primary"
            disabled={saving || !environment}
            onClick={() =>
              void onSave(environment, description, token ? ["mtls", "token"] : ["mtls"])
            }
          >
            {saving ? <Spinner /> : null}Add environment
          </button>
        </>
      }
    >
      <Field label="Environment" hint="Examples: dev, staging, prod, prod-gcp">
        <input
          className="input mono"
          value={environment}
          onChange={(event) => setEnvironment(event.target.value)}
          placeholder="prod-gcp"
        />
      </Field>
      <Field label="Description">
        <input
          className="input"
          value={description}
          onChange={(event) => setDescription(event.target.value)}
        />
      </Field>
      <label className="checkbox-row">
        <input
          type="checkbox"
          checked={token}
          onChange={(event) => setToken(event.target.checked)}
        />
        <span>
          <strong>Also allow bearer tokens</strong>
          <span className="faint text-sm">mTLS is always enabled and recommended.</span>
        </span>
      </label>
    </Modal>
  );
}

function BulkParameterModal({
  app,
  environments,
  row,
  saving,
  onClose,
  onSave,
}: {
  app: string;
  environments: string[];
  row: ApplicationConfigurationRow | null;
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
  useEffect(() => {
    if (!row) return;
    setKey(row.key);
    const present = environments.filter((environment) => row.environments[environment]?.present);
    setSelected(present.length ? present : environments);
    const first = present.length ? row.environments[present[0]] : undefined;
    setValue(first?.value ?? "");
    setContentType(first?.content_type ?? "string");
  }, [row, environments]);
  const allSelected = selected.length === environments.length;
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
  return (
    <Modal
      open={row !== null}
      title={row?.key ? `Update ${row.key}` : "New parameter"}
      onClose={onClose}
      wide
      footer={
        <>
          <button className="btn" onClick={onClose} disabled={saving}>
            Cancel
          </button>
          <button
            className="btn btn-primary"
            disabled={saving || !key || selected.length === 0}
            onClick={() =>
              void onSave({
                application: app,
                key,
                value,
                content_type: contentType,
                metadata_json: "{}",
                environments: selected,
              })
            }
          >
            {saving ? <Spinner /> : null}Review and apply to {selected.length}
          </button>
        </>
      }
    >
      <div className="warn-panel mb-16">
        <strong>Separate versions will be created.</strong> This does not link environments or
        create shared mutable state. Verify production targets before applying.
        {differing
          ? " Existing values differ; the editor starts from the first selected environment."
          : ""}
      </div>
      <div className="form-row">
        <Field label="Key">
          <input
            className="input mono"
            value={key}
            disabled={Boolean(row?.key)}
            onChange={(event) => setKey(event.target.value)}
          />
        </Field>
        <Field label="Content type">
          <select
            className="select"
            value={contentType}
            onChange={(event) => setContentType(event.target.value)}
          >
            {PARAMETER_CONTENT_TYPES.map((type) => (
              <option key={type} value={type}>
                {type}
              </option>
            ))}
          </select>
        </Field>
      </div>
      <Field label="Value">
        <textarea
          className="input mono"
          rows={7}
          value={value}
          onChange={(event) => setValue(event.target.value)}
        />
      </Field>
      <Field label="Target environments">
        <label className="checkbox-row">
          <input
            type="checkbox"
            checked={allSelected}
            onChange={(event) => setSelected(event.target.checked ? environments : [])}
          />
          <strong>All environments</strong>
        </label>
        <div className="environment-check-grid">
          {environments.map((environment) => (
            <label className="checkbox-row" key={environment}>
              <input
                type="checkbox"
                checked={selected.includes(environment)}
                onChange={(event) =>
                  setSelected((current) =>
                    event.target.checked
                      ? [...current, environment]
                      : current.filter((item) => item !== environment),
                  )
                }
              />
              <span className="mono">{environment}</span>
              {environment.includes("prod") ? <Badge kind="warning">production</Badge> : null}
            </label>
          ))}
        </div>
      </Field>
    </Modal>
  );
}
