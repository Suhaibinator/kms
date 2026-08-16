import { Button } from "@/components/ui/button";
import { ArrowLeft, Plus, RefreshCw, SlidersHorizontal } from "lucide-react";
import Link from "next/link";
import { useRouter } from "next/router";
import { useCallback, useEffect, useMemo, useState } from "react";
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
import { useToast } from "@/context/ToastContext";
import { api, isAbortError } from "@/lib/api";
import { utf8ToBase64 } from "@/lib/encoding";
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
  const selectedName = typeof router.query.app === "string" ? router.query.app : "";
  const [applications, setApplications] = useState<Application[]>([]);
  const [dashboard, setDashboard] = useState<ApplicationDashboard | null>(null);
  const [loading, setLoading] = useState(true);
  const [createOpen, setCreateOpen] = useState(false);
  const [editOpen, setEditOpen] = useState(false);
  const [environmentOpen, setEnvironmentOpen] = useState(false);
  const [secretSeed, setSecretSeed] = useState<QuickSecretSeed | null>(null);
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

  // Derived above the early return below: hooks must run in the same order on
  // every render, and the list view returns before reaching the detail view.
  const environments = dashboard?.environments ?? [];
  const environmentNames = useMemo(
    () => environments.map((environment) => environment.env),
    [environments],
  );

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
          <TableSkeleton
            headers={["Application", "Environments", "Release", "Schema", "Contract"]}
          />
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
                      <Button
                        variant="outline"
                        size="sm"
                        onClick={() => void selectApplication(app.name)}
                      >
                        Manage
                      </Button>
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
          dashboard?.application.description || "Application configuration across environments."
        }
        actions={
          <>
            <Button variant="outline" onClick={() => void loadDashboard()} disabled={loading}>
              <RefreshCw size={15} />
              Refresh
            </Button>
            <Button variant="outline" onClick={() => setEditOpen(true)} disabled={!dashboard}>
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
      {dashboard ? <ContractSummary application={dashboard.application} /> : null}
      {loading && !dashboard ? (
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
                onClick={() => setWriteRow({ key: "", kind: "parameter", environments: {} })}
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
                {(dashboard?.rows ?? []).map((row) => (
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
                        <Button variant="outline" size="sm" onClick={() => setWriteRow(row)}>
                          <SlidersHorizontal size={14} />
                          Edit
                        </Button>
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
  const [touched, setTouched] = useState<Partial<Record<CreateField, boolean>>>({});
  const [submitAttempted, setSubmitAttempted] = useState(false);
  useEffect(() => {
    if (!open) return;
    setName(initial?.name ?? "");
    setDescription(initial?.description ?? "");
    setReleaseName(initial?.release_name ?? "runtime");
    setSchemaID(initial?.schema_id ?? "");
    setSchemaVersion(initial?.schema_version ? String(initial.schema_version) : "");
    setContract(initial ? JSON.stringify(initial.contract, null, 2) : EMPTY_CONTRACT);
    setError("");
    setTouched({});
    setSubmitAttempted(false);
  }, [open, initial]);

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
  const show = (field: CreateField, problem: string | null) =>
    touched[field] || submitAttempted ? problem : null;
  const markTouched = (field: CreateField) => setTouched((t) => ({ ...t, [field]: true }));
  return (
    <Modal
      open={open}
      title={initial ? `Edit ${initial.name}` : "New application"}
      onClose={onClose}
      wide
      footer={
        <>
          <Button variant="outline" onClick={onClose} disabled={saving}>
            Cancel
          </Button>
          <Button
            disabled={saving || blocking !== null}
            onClick={() => {
              setSubmitAttempted(true);
              if (blocking) return;
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
          </Button>
        </>
      }
    >
      <div className="info-panel mb-16">
        The application owns this shape. Every environment release must use the same release name,
        schema pin, aliases, kinds, and parameter content types.
      </div>
      <div className="form-row">
        <Field
          label="Application name"
          hint="Lowercase letters, digits, and hyphens."
          error={initial ? null : show("name", nameProblem)}
        >
          <Input
            className="font-mono"
            value={name}
            disabled={Boolean(initial)}
            onChange={(event) => setName(event.target.value)}
            onBlur={() => markTouched("name")}
            placeholder="payments-api"
          />
        </Field>
        <Field
          label="Release name"
          hint="Defaults to runtime when left blank."
          error={show("releaseName", releaseNameProblem)}
        >
          <Input
            className="font-mono"
            value={releaseName}
            onChange={(event) => setReleaseName(event.target.value)}
            onBlur={() => markTouched("releaseName")}
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
          error={show("schemaPin", schemaPinProblem)}
        >
          <Input
            className="font-mono"
            value={schemaID}
            onChange={(event) => setSchemaID(event.target.value)}
            onBlur={() => markTouched("schemaPin")}
          />
        </Field>
        <Field label="Schema version">
          <Input
            type="number"
            min={1}
            value={schemaVersion}
            onChange={(event) => setSchemaVersion(event.target.value)}
            onBlur={() => markTouched("schemaPin")}
          />
        </Field>
      </div>
      <Field
        label="Shared release contract"
        hint="JSON array of {alias, kind, content_type}. Secrets omit content_type."
        error={show("contract", contractProblem)}
      >
        <Textarea
          className="font-mono"
          rows={9}
          value={contract}
          onChange={(event) => setContract(event.target.value)}
          onBlur={() => markTouched("contract")}
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
  const [environmentTouched, setEnvironmentTouched] = useState(false);
  // An environment is the env half of a namespace, so it follows the label rule.
  const environmentProblem = validateEnv(environment.trim());
  useEffect(() => {
    if (!open) return;
    setEnvironmentTouched(false);
  }, [open]);
  return (
    <Modal
      open={open}
      title={`Add environment to ${app}`}
      onClose={onClose}
      footer={
        <>
          <Button variant="outline" onClick={onClose} disabled={saving}>
            Cancel
          </Button>
          <Button
            disabled={saving || environmentProblem !== null}
            onClick={() => {
              setEnvironmentTouched(true);
              if (environmentProblem) return;
              void onSave(environment, description, token ? ["mtls", "token"] : ["mtls"]);
            }}
          >
            {saving ? <Spinner /> : null}Add environment
          </Button>
        </>
      }
    >
      <Field
        label="Environment"
        hint="Examples: dev, staging, prod, prod-gcp"
        error={environmentTouched ? environmentProblem : null}
      >
        <Input
          className="font-mono"
          value={environment}
          onChange={(event) => setEnvironment(event.target.value)}
          onBlur={() => setEnvironmentTouched(true)}
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
    </Modal>
  );
}

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
  const [touched, setTouched] = useState({ key: false, value: false });
  const [submitAttempted, setSubmitAttempted] = useState(false);

  useEffect(() => {
    if (!seed) return;
    setEnvironment(seed.environment || (environments.length === 1 ? environments[0] : ""));
    setKey(seed.key);
    setValue("");
    setContentType("text/plain");
    setTouched({ key: false, value: false });
    setSubmitAttempted(false);
  }, [seed, environments]);

  const keyProblem = validateKey(key.trim());
  const valueProblem = value ? validateValueSize(value) : "Secret value is required.";
  const showKeyProblem = touched.key || submitAttempted ? keyProblem : null;
  const showValueProblem = touched.value || submitAttempted ? valueProblem : null;
  const blocking =
    !environment || keyProblem !== null || valueProblem !== null || !contentType.trim();
  const advancedHref = {
    pathname: "/secrets/new",
    query: {
      ...(environment ? { env: environment } : {}),
      app,
      ...(key.trim() ? { key: key.trim() } : {}),
    },
  };

  return (
    <Modal
      open={seed !== null}
      title="New secret"
      onClose={onClose}
      footer={
        <>
          <Button variant="outline" onClick={onClose} disabled={saving}>
            Cancel
          </Button>
          <Button
            disabled={saving || blocking}
            onClick={() => {
              setSubmitAttempted(true);
              if (blocking) return;
              void onSave({
                environment,
                key: key.trim(),
                value,
                contentType: contentType.trim(),
              });
            }}
          >
            {saving ? <Spinner /> : null}
            Create secret
          </Button>
        </>
      }
    >
      <div className="form-row">
        <Field label="Application">
          <Input className="font-mono" value={app} disabled />
        </Field>
        <Field label="Environment">
          <AppSelect
            className="font-mono"
            value={environment}
            onValueChange={setEnvironment}
            placeholder="Select environment…"
            options={environments.map((item) => ({ value: item, label: item }))}
          />
        </Field>
      </div>
      <Field
        label="Secret key"
        hint="Examples: stripe-api-key or billing/webhook-secret"
        error={showKeyProblem}
      >
        <Input
          className="font-mono"
          value={key}
          onChange={(event) => setKey(event.target.value)}
          onBlur={() => setTouched((current) => ({ ...current, key: true }))}
          placeholder="stripe-api-key"
        />
      </Field>
      <Field
        label="Secret value"
        hint="The value is encrypted before it is stored and is never shown in the matrix."
        error={showValueProblem}
      >
        <Textarea
          className="font-mono"
          rows={5}
          value={value}
          onChange={(event) => setValue(event.target.value)}
          onBlur={() => setTouched((current) => ({ ...current, value: true }))}
          spellCheck={false}
          autoComplete="off"
        />
      </Field>
      <Field label="Content type">
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
  const [keyTouched, setKeyTouched] = useState(false);
  const [valueTouched, setValueTouched] = useState(false);
  useEffect(() => {
    if (!row) return;
    setKeyTouched(false);
    setValueTouched(false);
    setKey(row.key);
    const present = environments.filter((environment) => row.environments[environment]?.present);
    setSelected(present.length ? present : environments);
    const first = present.length ? row.environments[present[0]] : undefined;
    setValue(first?.value ?? "");
    setContentType(first?.content_type ?? "string");
  }, [row, environments]);
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
  return (
    <Modal
      open={row !== null}
      title={row?.key ? `Update ${row.key}` : "New parameter"}
      onClose={onClose}
      wide
      footer={
        <>
          <Button variant="outline" onClick={onClose} disabled={saving}>
            Cancel
          </Button>
          <Button
            disabled={saving || blocking !== null || selected.length === 0}
            onClick={() => {
              setKeyTouched(true);
              setValueTouched(true);
              if (blocking) return;
              void onSave({
                application: app,
                key,
                value,
                content_type: contentType,
                metadata_json: "{}",
                environments: selected,
              });
            }}
          >
            {saving ? <Spinner /> : null}Review and apply to {selected.length}
          </Button>
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
        <Field label="Key" error={row?.key ? null : keyTouched ? keyProblem : null}>
          <Input
            className="font-mono"
            value={key}
            disabled={Boolean(row?.key)}
            onChange={(event) => setKey(event.target.value)}
            onBlur={() => setKeyTouched(true)}
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
      <Field label="Value" error={valueTouched ? valueProblem : null}>
        <Textarea
          className="font-mono"
          rows={7}
          value={value}
          onChange={(event) => setValue(event.target.value)}
          onBlur={() => setValueTouched(true)}
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
              {environment.includes("prod") ? <Badge kind="warning">production</Badge> : null}
            </div>
          ))}
        </div>
      </Field>
    </Modal>
  );
}
