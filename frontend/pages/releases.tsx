import { RefreshCw } from "lucide-react";
import { type FormEvent, useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Icon } from "@/components/icons";
import { ConfirmDialog } from "@/components/Modal";
import NamespacePicker, { type NamespaceSelection } from "@/components/NamespacePicker";
import { Badge, EmptyState, PageHeader, Pagination, Spinner, TableSkeleton } from "@/components/ui";
import { useToast } from "@/context/ToastContext";
import { ApiError, api, isAbortError } from "@/lib/api";
import { displayPath } from "@/lib/format";
import { useCursorPagination, useNamespaces } from "@/lib/hooks";
import type {
  ConfigurationRelease,
  ConfigurationSchema,
  CreateReleaseRequest,
  ReleaseSubscriberState,
  ReleaseSummary,
  ReleaseValidationError,
} from "@/lib/types";

const NO_NS: NamespaceSelection = { env: "", app: "" };

interface ActivationFailure {
  operation: "Activation" | "Rollback";
  target: string;
  violations: ReleaseValidationError[];
}

type PendingReleaseAction =
  | { kind: "activate"; summary: ReleaseSummary }
  | { kind: "rollback"; name: string; current: ReleaseSummary; previous: ReleaseSummary };

type BusyReleaseAction =
  | ""
  | "activate"
  | "rollback"
  | "create-release"
  | "create-schema"
  | `validate:${string}`;

function releaseKey(r: ConfigurationRelease): string {
  return `${r.name}@${r.version}`;
}

function refText(entry: ConfigurationRelease["entries"][number]): string {
  const ns = entry.ref.namespace;
  return displayPath({ env: ns.env, app: ns.app, key: entry.ref.key });
}

function activationViolations(err: unknown): ReleaseValidationError[] | null {
  if (!(err instanceof ApiError) || err.code !== "failed_precondition") return null;
  return err.validationErrors.length > 0 ? err.validationErrors : null;
}

export default function ReleasesPage() {
  const toast = useToast();
  const { namespaces, error: namespaceError } = useNamespaces();
  const [ns, setNS] = useState<NamespaceSelection>(NO_NS);
  const [nameDraft, setNameDraft] = useState("");
  const [name, setName] = useState("");
  const [releases, setReleases] = useState<ReleaseSummary[]>([]);
  const [schemas, setSchemas] = useState<ConfigurationSchema[]>([]);
  const [schemasLoading, setSchemasLoading] = useState(true);
  const [subscribers, setSubscribers] = useState<ReleaseSubscriberState[]>([]);
  const [subscriberRevision, setSubscriberRevision] = useState(0);
  const [releasesLoading, setReleasesLoading] = useState(false);
  const [subscribersLoading, setSubscribersLoading] = useState(false);
  const [busyAction, setBusyAction] = useState<BusyReleaseAction>("");
  const [pendingAction, setPendingAction] = useState<PendingReleaseAction | null>(null);
  const refreshGeneration = useRef(0);
  const schemaGeneration = useRef(0);
  const loadedReleaseScope = useRef("");
  const refreshController = useRef<AbortController | null>(null);
  const schemaController = useRef<AbortController | null>(null);
  const [definition, setDefinition] = useState(`{
  "name": "runtime",
  "entries": []
}`);
  const [schemaID, setSchemaID] = useState("");
  const [schemaJSON, setSchemaJSON] = useState(`{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object"
}`);
  const [diffFrom, setDiffFrom] = useState("");
  const [diffTo, setDiffTo] = useState("");
  const [selectedReleaseKey, setSelectedReleaseKey] = useState("");
  const [activationFailure, setActivationFailure] = useState<ActivationFailure | null>(null);

  const hasNS = Boolean(ns.env && ns.app);
  const releaseScope = hasNS ? JSON.stringify([ns.env, ns.app, name]) : "";
  const subscriberScope = hasNS && name ? JSON.stringify([ns.env, ns.app, name]) : "";
  const releasePaging = useCursorPagination(releaseScope);
  const schemaPaging = useCursorPagination("schemas");
  const subscriberPaging = useCursorPagination(subscriberScope);
  const releaseRequestScope = JSON.stringify([releaseScope, releasePaging.pageToken]);

  useEffect(() => {
    if (namespaceError) toast.error(namespaceError, "Failed to load namespaces");
  }, [namespaceError, toast]);

  const loadSchemas = useCallback(async () => {
    schemaController.current?.abort();
    const controller = new AbortController();
    schemaController.current = controller;
    const generation = ++schemaGeneration.current;
    setSchemasLoading(true);
    try {
      const page = await api.listSchemas(undefined, schemaPaging.pageToken || undefined, {
        signal: controller.signal,
      });
      if (generation === schemaGeneration.current) {
        setSchemas(page.schemas ?? []);
        schemaPaging.setNextToken(page.next_page_token ?? "");
      }
    } catch (err) {
      if (generation === schemaGeneration.current && !isAbortError(err)) {
        toast.error(err, "Failed to load schemas");
      }
    } finally {
      if (generation === schemaGeneration.current) {
        schemaController.current = null;
        setSchemasLoading(false);
      }
    }
  }, [schemaPaging.pageToken, schemaPaging.setNextToken, toast]);

  useEffect(() => {
    void loadSchemas();
    return () => schemaController.current?.abort();
  }, [loadSchemas]);

  const refresh = useCallback(
    async (forceReleases = false) => {
      refreshController.current?.abort();
      const generation = ++refreshGeneration.current;
      if (!hasNS) {
        refreshController.current = null;
        loadedReleaseScope.current = "";
        setReleases([]);
        setSubscribers([]);
        setSubscriberRevision(0);
        releasePaging.setNextToken("");
        subscriberPaging.setNextToken("");
        setReleasesLoading(false);
        setSubscribersLoading(false);
        return;
      }
      const controller = new AbortController();
      refreshController.current = controller;
      const shouldLoadReleases =
        forceReleases || loadedReleaseScope.current !== releaseRequestScope;
      if (shouldLoadReleases) setReleasesLoading(true);
      setSubscribersLoading(Boolean(name));
      try {
        const [nextReleases, status] = await Promise.all([
          shouldLoadReleases
            ? api.listReleases(ns, name || undefined, 100, releasePaging.pageToken || undefined, {
                signal: controller.signal,
              })
            : Promise.resolve(null),
          name
            ? api.releaseSubscribers(ns, name, 1000, subscriberPaging.pageToken || undefined, {
                signal: controller.signal,
              })
            : Promise.resolve(null),
        ]);
        if (generation !== refreshGeneration.current) return;
        if (nextReleases) {
          loadedReleaseScope.current = releaseRequestScope;
          setReleases(nextReleases.releases ?? []);
          releasePaging.setNextToken(nextReleases.next_page_token ?? "");
        }
        if (status) {
          setSubscribers(status.subscribers ?? []);
          setSubscriberRevision(status.current_revision ?? 0);
          subscriberPaging.setNextToken(status.next_page_token ?? "");
        } else {
          setSubscribers([]);
          setSubscriberRevision(0);
          subscriberPaging.setNextToken("");
        }
      } catch (err) {
        if (generation === refreshGeneration.current && !isAbortError(err)) {
          toast.error(err, "Failed to load releases");
        }
      } finally {
        if (generation === refreshGeneration.current) {
          refreshController.current = null;
          if (shouldLoadReleases) setReleasesLoading(false);
          setSubscribersLoading(false);
        }
      }
    },
    [
      hasNS,
      name,
      ns,
      releasePaging.pageToken,
      releasePaging.setNextToken,
      releaseRequestScope,
      subscriberPaging.pageToken,
      subscriberPaging.setNextToken,
      toast,
    ],
  );

  useEffect(() => {
    void refresh();
    return () => refreshController.current?.abort();
  }, [refresh]);

  function applyNameFilter(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setActivationFailure(null);
    setPendingAction(null);
    setName(nameDraft.trim());
  }

  function changeNamespace(next: NamespaceSelection) {
    setActivationFailure(null);
    setPendingAction(null);
    setNS(next);
  }

  async function createRelease() {
    if (!hasNS || busyAction) return;
    setBusyAction("create-release");
    try {
      const parsed = JSON.parse(definition) as Partial<CreateReleaseRequest>;
      if (!parsed.name || !Array.isArray(parsed.entries)) {
        throw new Error("Definition requires name and entries");
      }
      const req: CreateReleaseRequest = {
        namespace: ns,
        name: parsed.name,
        schema_id: parsed.schema_id,
        schema_version: parsed.schema_version,
        entries: parsed.entries,
        metadata_json: parsed.metadata_json ?? "{}",
      };
      const result = await api.createRelease(req);
      toast.success(`Created ${releaseKey(result.release)}`);
      setNameDraft(result.release.name);
      setActivationFailure(null);
      setPendingAction(null);
      setName(result.release.name);
      if (name === result.release.name) await refresh(true);
    } catch (err) {
      toast.error(err, "Could not create release");
    } finally {
      setBusyAction("");
    }
  }

  async function validate(release: ConfigurationRelease) {
    if (busyAction) return;
    setBusyAction(`validate:${releaseKey(release)}`);
    try {
      const result = await api.validateRelease(ns, release.name, release.version);
      if (result.valid) toast.success(`${releaseKey(release)} is valid`);
      else
        toast.error(
          new Error(result.errors.map((e) => `${e.alias || "release"}: ${e.message}`).join("; ")),
          "Validation failed",
        );
    } catch (err) {
      toast.error(err, "Validation failed");
    } finally {
      setBusyAction("");
    }
  }

  async function performPendingAction() {
    if (!pendingAction || busyAction) return;
    const action = pendingAction;
    setBusyAction(action.kind);
    setActivationFailure(null);
    let target =
      action.kind === "activate"
        ? releaseKey(action.summary.release)
        : releaseKey(action.previous.release);
    try {
      const releaseName = action.kind === "activate" ? action.summary.release.name : action.name;
      const active = await api.getActiveRelease(ns, releaseName).catch((err: unknown) => {
        if (action.kind === "activate" && err instanceof ApiError && err.code === "not_found") {
          return null;
        }
        throw err;
      });
      const expectedVersion = active?.release.version ?? 0;
      const targetVersion =
        action.kind === "activate" ? action.summary.release.version : active?.previous_version;
      if (!targetVersion) throw new Error("No previous release is available");
      target = `${releaseName}@${targetVersion}`;
      await api.activateRelease(ns, releaseName, targetVersion, expectedVersion);
      toast.success(
        action.kind === "activate"
          ? `Activated ${target}`
          : `Rolled back ${releaseName} to version ${targetVersion}`,
      );
      setPendingAction(null);
      await refresh(true);
    } catch (err) {
      const violations = activationViolations(err);
      if (violations) {
        setActivationFailure({
          operation: action.kind === "activate" ? "Activation" : "Rollback",
          target,
          violations,
        });
        setPendingAction(null);
      } else {
        toast.error(err, action.kind === "activate" ? "Activation failed" : "Rollback failed");
      }
    } finally {
      setBusyAction("");
    }
  }

  async function createSchema() {
    if (busyAction) return;
    setBusyAction("create-schema");
    try {
      JSON.parse(schemaJSON);
      const result = await api.createSchema(schemaID.trim(), schemaJSON);
      toast.success(`Created schema ${result.schema.id}@${result.schema.version}`);
      setSchemaID("");
      if (schemaPaging.page === 1) await loadSchemas();
      else schemaPaging.reset();
    } catch (err) {
      toast.error(err, "Could not create schema");
    } finally {
      setBusyAction("");
    }
  }

  const diff = useMemo(() => {
    const from = releases.find((r) => releaseKey(r.release) === diffFrom)?.release;
    const to = releases.find((r) => releaseKey(r.release) === diffTo)?.release;
    if (!from || !to) return [];
    const fromByAlias = new Map(from.entries.map((entry) => [entry.alias, entry]));
    const toByAlias = new Map(to.entries.map((entry) => [entry.alias, entry]));
    const aliases = new Set([...fromByAlias.keys(), ...toByAlias.keys()]);
    return [...aliases].sort().flatMap((alias) => {
      const a = fromByAlias.get(alias);
      const b = toByAlias.get(alias);
      const left = a
        ? `${a.kind} ${refText(a)}@${a.version}${a.parameter_digest ? ` ${a.parameter_digest.slice(0, 12)}` : ""}`
        : "—";
      const right = b
        ? `${b.kind} ${refText(b)}@${b.version}${b.parameter_digest ? ` ${b.parameter_digest.slice(0, 12)}` : ""}`
        : "—";
      return left === right ? [] : [{ alias, left, right }];
    });
  }, [diffFrom, diffTo, releases]);

  const subscriberInstances = useMemo(() => {
    const grouped = new Map<
      string,
      {
        identity: string;
        client: string;
        instance: string;
        connected: boolean;
        states: Partial<
          Record<"received" | "prepared" | "applied" | "rejected", ReleaseSubscriberState>
        >;
        latestRevision: number;
      }
    >();
    for (const state of subscribers) {
      const key = JSON.stringify([state.identity, state.client_name, state.instance_id]);
      const row = grouped.get(key) ?? {
        identity: state.identity,
        client: state.client_name,
        instance: state.instance_id,
        connected: false,
        states: {},
        latestRevision: 0,
      };
      if (["received", "prepared", "applied", "rejected"].includes(state.state)) {
        row.states[state.state as keyof typeof row.states] = state;
      }
      row.connected ||= state.connected;
      row.latestRevision = Math.max(row.latestRevision, state.activation_revision);
      grouped.set(key, row);
    }
    return [...grouped.values()].sort(
      (a, b) =>
        a.identity.localeCompare(b.identity) ||
        a.client.localeCompare(b.client) ||
        a.instance.localeCompare(b.instance),
    );
  }, [subscribers]);

  const selectedRelease = releases.find(
    (summary) => releaseKey(summary.release) === selectedReleaseKey,
  )?.release;
  const currentNamedRelease = releases.find(
    (summary) => summary.current && summary.release.name === name,
  );
  const previousNamedRelease = releases.find(
    (summary) => summary.previous && summary.release.name === name,
  );
  const pendingCurrentRelease =
    pendingAction?.kind === "activate"
      ? releases.find(
          (summary) =>
            summary.current && summary.release.name === pendingAction.summary.release.name,
        )
      : pendingAction?.current;

  function lifecycleCell(state: ReleaseSubscriberState | undefined) {
    if (!state) return <span className="faint">—</span>;
    const detail =
      state.state === "rejected" && state.rejection_category
        ? `${state.rejection_category} · `
        : "";
    const summary = (
      <>
        {detail}v{state.release_version} · r{state.activation_revision}
      </>
    );
    if (!state.diagnostic) return <span>{summary}</span>;
    return (
      <details>
        <summary>{summary}</summary>
        <div className="text-sm">{state.diagnostic}</div>
      </details>
    );
  }

  return (
    <>
      <PageHeader
        title="Configuration releases"
        subtitle="Atomically activate exact parameter and secret versions. Secret values and tokens are never displayed."
        actions={
          <button
            type="button"
            className="btn"
            disabled={releasesLoading || subscribersLoading || Boolean(busyAction)}
            onClick={() => void refresh(true)}
          >
            {releasesLoading || subscribersLoading ? <Spinner /> : null}
            {!releasesLoading && !subscribersLoading ? <RefreshCw size={16} aria-hidden /> : null}
            Refresh
          </button>
        }
      />

      <div className="filters">
        <NamespacePicker
          namespaces={namespaces}
          value={ns}
          onChange={changeNamespace}
          disabled={Boolean(busyAction)}
        />
        <form className="row-wrap filter-grow" onSubmit={applyNameFilter}>
          <div className="field filter-grow">
            <label className="field-label" htmlFor="release-name">
              Release name
            </label>
            <input
              id="release-name"
              className="input mono"
              value={nameDraft}
              onChange={(event) => setNameDraft(event.target.value)}
              placeholder="runtime"
              disabled={!hasNS || Boolean(busyAction)}
            />
          </div>
          <button
            className="btn"
            type="submit"
            disabled={!hasNS || nameDraft.trim() === name || releasesLoading || Boolean(busyAction)}
          >
            Apply filter
          </button>
        </form>
        <button
          type="button"
          className="btn"
          disabled={!currentNamedRelease || !previousNamedRelease || Boolean(busyAction)}
          onClick={() => {
            if (currentNamedRelease && previousNamedRelease) {
              setPendingAction({
                kind: "rollback",
                name,
                current: currentNamedRelease,
                previous: previousNamedRelease,
              });
            }
          }}
        >
          Rollback to previous
        </button>
      </div>

      {activationFailure ? (
        <section
          className="danger-panel mb-16"
          role="alert"
          aria-labelledby="activation-failure-title"
        >
          <div className="between">
            <div>
              <h2 id="activation-failure-title" className="text-danger">
                {activationFailure.operation} blocked for{" "}
                <span className="mono">{activationFailure.target}</span>
              </h2>
              <div className="text-sm mt-8">
                The active release and activation revision were not changed. Resolve the violations
                below and try again.
              </div>
            </div>
            <button type="button" className="btn btn-sm" onClick={() => setActivationFailure(null)}>
              Dismiss
            </button>
          </div>
          <div className="table-wrap activation-violations mt-12">
            <table className="data">
              <thead>
                <tr>
                  <th>Alias</th>
                  <th>Code</th>
                  <th>Schema pointer</th>
                  <th>Message</th>
                </tr>
              </thead>
              <tbody>
                {activationFailure.violations.map((violation) => (
                  <tr key={JSON.stringify(violation)}>
                    <td className="mono">
                      {violation.alias || <span className="faint">release</span>}
                    </td>
                    <td>
                      <Badge kind="danger">{violation.code}</Badge>
                    </td>
                    <td className="mono">
                      {violation.schema_pointer || <span className="faint">—</span>}
                    </td>
                    <td>{violation.message}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </section>
      ) : null}

      {!hasNS ? (
        <EmptyState icon={<Icon.namespace size={20} />} title="Choose a namespace">
          Select a namespace to manage its releases.
        </EmptyState>
      ) : releasesLoading ? (
        <TableSkeleton headers={["Release", "State", "Schema", "Entries", "Digest", "Actions"]} />
      ) : releases.length === 0 ? (
        <EmptyState icon={<Icon.parameter size={20} />} title="No releases found">
          Create an immutable release using the definition editor below.
        </EmptyState>
      ) : (
        <div className="table-wrap card-table mb-16">
          <table className="data">
            <thead>
              <tr>
                <th>Release</th>
                <th>State</th>
                <th>Schema</th>
                <th>Entries</th>
                <th>Digest</th>
                <th>Actions</th>
              </tr>
            </thead>
            <tbody>
              {releases.map((summary) => {
                const r = summary.release;
                return (
                  <tr key={releaseKey(r)}>
                    <td className="mono" data-label="Release">
                      {releaseKey(r)}
                    </td>
                    <td data-label="State">
                      {summary.current ? (
                        <Badge kind="success">current · rev {summary.activation_revision}</Badge>
                      ) : summary.previous ? (
                        <Badge kind="warning">previous</Badge>
                      ) : (
                        <Badge>inactive</Badge>
                      )}
                    </td>
                    <td className="mono" data-label="Schema">
                      {r.schema_id ? (
                        `${r.schema_id}@${r.schema_version}`
                      ) : (
                        <span className="faint">none</span>
                      )}
                    </td>
                    <td data-label="Entries">{r.entries.length}</td>
                    <td className="mono" data-label="Digest">
                      <details>
                        <summary aria-label={`Digest ${r.digest}`}>
                          {r.digest.slice(0, 16)}…
                        </summary>
                        <div className="text-sm">{r.digest}</div>
                      </details>
                    </td>
                    <td className="row-wrap row-actions" data-label="Actions">
                      <button
                        type="button"
                        className="btn btn-sm"
                        disabled={Boolean(busyAction)}
                        onClick={() => setSelectedReleaseKey(releaseKey(r))}
                      >
                        View
                      </button>
                      <button
                        type="button"
                        className="btn btn-sm"
                        disabled={Boolean(busyAction)}
                        onClick={() => void validate(r)}
                      >
                        {busyAction === `validate:${releaseKey(r)}` ? <Spinner /> : null}
                        Validate
                      </button>
                      <button
                        type="button"
                        className="btn btn-sm btn-primary"
                        disabled={summary.current || Boolean(busyAction)}
                        onClick={() => setPendingAction({ kind: "activate", summary })}
                      >
                        Activate
                      </button>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
      {hasNS && !releasesLoading ? (
        <Pagination
          hasNext={releasePaging.hasNext}
          onNext={releasePaging.next}
          hasPrevious={releasePaging.hasPrevious}
          onPrevious={releasePaging.previous}
          onReset={releasePaging.reset}
          showReset={releasePaging.hasPrevious}
          page={releasePaging.page}
        />
      ) : null}

      {selectedRelease ? (
        <section className="card mb-16">
          <h2>{releaseKey(selectedRelease)} details</h2>
          <div className="text-sm faint mb-12">
            Digest <span className="mono">{selectedRelease.digest}</span>
            {selectedRelease.schema_id ? (
              <>
                {" "}
                · Schema{" "}
                <span className="mono">
                  {selectedRelease.schema_id}@{selectedRelease.schema_version}
                </span>
              </>
            ) : null}
          </div>
          <div className="table-wrap">
            <table className="data">
              <thead>
                <tr>
                  <th>Alias</th>
                  <th>Kind</th>
                  <th>Reference</th>
                  <th>Version</th>
                  <th>Content type</th>
                  <th>Parameter digest</th>
                  <th>Secret protection</th>
                  <th>Metadata</th>
                </tr>
              </thead>
              <tbody>
                {selectedRelease.entries.map((entry) => (
                  <tr key={entry.alias}>
                    <td className="mono">{entry.alias}</td>
                    <td>{entry.kind}</td>
                    <td className="mono">{refText(entry)}</td>
                    <td>{entry.version}</td>
                    <td>{entry.content_type || <span className="faint">—</span>}</td>
                    <td className="mono">
                      {entry.parameter_digest || <span className="faint">—</span>}
                    </td>
                    <td>
                      {entry.kind === "secret" ? (
                        [
                          entry.has_access_token ? "token" : "no token",
                          entry.client_bound ? "client-bound" : "shared",
                        ].join(" · ")
                      ) : (
                        <span className="faint">—</span>
                      )}
                    </td>
                    <td className="mono">{entry.metadata_json || "{}"}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </section>
      ) : null}

      <div className="card-grid mb-16">
        <section className="card">
          <h2>Create release</h2>
          <p className="text-sm faint">
            Paste a JSON definition. Relative references should use the selected namespace.
          </p>
          <label className="field-label" htmlFor="release-definition">
            Release definition
          </label>
          <textarea
            id="release-definition"
            className="input mono"
            rows={14}
            value={definition}
            onChange={(event) => setDefinition(event.target.value)}
          />
          <div className="mt-12">
            <button
              type="button"
              className="btn btn-primary"
              disabled={!hasNS || Boolean(busyAction)}
              onClick={() => void createRelease()}
            >
              {busyAction === "create-release" ? <Spinner /> : null}
              Create immutable version
            </button>
          </div>
        </section>
        <section className="card">
          <h2>Register JSON Schema</h2>
          <label className="field-label" htmlFor="schema-id">
            Schema ID
          </label>
          <input
            id="schema-id"
            className="input mono mb-8"
            placeholder="go-common/runtime"
            value={schemaID}
            onChange={(event) => setSchemaID(event.target.value)}
          />
          <label className="field-label" htmlFor="schema-definition">
            JSON Schema definition
          </label>
          <textarea
            id="schema-definition"
            className="input mono"
            rows={11}
            value={schemaJSON}
            onChange={(event) => setSchemaJSON(event.target.value)}
          />
          <div className="mt-12">
            <button
              type="button"
              className="btn"
              disabled={!schemaID.trim() || Boolean(busyAction)}
              onClick={() => void createSchema()}
            >
              {busyAction === "create-schema" ? <Spinner /> : null}
              Register schema version
            </button>
          </div>
          <div className="text-sm faint mt-12" aria-live="polite">
            {schemasLoading
              ? "Loading registered schemas…"
              : `${schemas.length} registered schema versions on this page`}
          </div>
          {schemas.length ? (
            <div className="table-wrap mt-12">
              <table className="data">
                <thead>
                  <tr>
                    <th>Schema</th>
                    <th>Digest</th>
                  </tr>
                </thead>
                <tbody>
                  {schemas.map((schema) => (
                    <tr key={`${schema.id}@${schema.version}`}>
                      <td className="mono">
                        <details>
                          <summary>
                            {schema.id}@{schema.version}
                          </summary>
                          <pre className="text-sm">{schema.schema_json}</pre>
                        </details>
                      </td>
                      <td className="mono">
                        <details>
                          <summary aria-label={`Digest ${schema.digest}`}>
                            {schema.digest.slice(0, 12)}…
                          </summary>
                          <div className="text-sm">{schema.digest}</div>
                        </details>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          ) : null}
          {!schemasLoading ? (
            <Pagination
              hasNext={schemaPaging.hasNext}
              onNext={schemaPaging.next}
              hasPrevious={schemaPaging.hasPrevious}
              onPrevious={schemaPaging.previous}
              onReset={schemaPaging.reset}
              showReset={schemaPaging.hasPrevious}
              page={schemaPaging.page}
            />
          ) : null}
        </section>
      </div>

      <section className="card mb-16">
        <h2>Diff versions</h2>
        <div className="row-wrap mb-12">
          <div className="field">
            <label className="field-label" htmlFor="diff-from">
              From release
            </label>
            <select
              id="diff-from"
              className="select"
              value={diffFrom}
              onChange={(event) => setDiffFrom(event.target.value)}
            >
              <option value="">From…</option>
              {releases.map((r) => (
                <option key={`f-${releaseKey(r.release)}`} value={releaseKey(r.release)}>
                  {releaseKey(r.release)}
                </option>
              ))}
            </select>
          </div>
          <div className="field">
            <label className="field-label" htmlFor="diff-to">
              To release
            </label>
            <select
              id="diff-to"
              className="select"
              value={diffTo}
              onChange={(event) => setDiffTo(event.target.value)}
            >
              <option value="">To…</option>
              {releases.map((r) => (
                <option key={`t-${releaseKey(r.release)}`} value={releaseKey(r.release)}>
                  {releaseKey(r.release)}
                </option>
              ))}
            </select>
          </div>
        </div>
        {diffFrom && diffTo && diff.length === 0 ? (
          <div className="faint">No manifest differences.</div>
        ) : diff.length ? (
          <div className="table-wrap">
            <table className="data">
              <thead>
                <tr>
                  <th>Alias</th>
                  <th>From</th>
                  <th>To</th>
                </tr>
              </thead>
              <tbody>
                {diff.map((d) => (
                  <tr key={d.alias}>
                    <td className="mono">{d.alias}</td>
                    <td className="mono">{d.left}</td>
                    <td className="mono">{d.right}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        ) : null}
      </section>

      <section className="card">
        <h2>Release subscribers</h2>
        {name ? (
          <>
            <Pagination
              hasNext={!subscribersLoading && subscriberPaging.hasNext}
              onNext={subscriberPaging.next}
              hasPrevious={!subscribersLoading && subscriberPaging.hasPrevious}
              onPrevious={subscriberPaging.previous}
              onReset={subscriberPaging.reset}
              showReset={!subscribersLoading && subscriberPaging.hasPrevious}
              page={subscriberPaging.page}
            />
            <div className="text-sm faint mb-12">Up to 1,000 state rows per page</div>
          </>
        ) : null}
        {!name ? (
          <div className="faint">Apply a release-name filter to view per-instance state.</div>
        ) : subscribersLoading ? (
          <div className="loading-block" role="status">
            <Spinner /> Loading subscriber state…
          </div>
        ) : subscriberInstances.length === 0 ? (
          <div className="faint">No subscriber state recorded.</div>
        ) : (
          <div className="table-wrap card-table">
            <table className="data">
              <thead>
                <tr>
                  <th>Identity</th>
                  <th>Client</th>
                  <th>Instance</th>
                  <th>Received</th>
                  <th>Prepared</th>
                  <th>Applied</th>
                  <th>Rejected</th>
                  <th>Connection</th>
                  <th>Lag</th>
                </tr>
              </thead>
              <tbody>
                {subscriberInstances.map((instance) => (
                  <tr key={JSON.stringify([instance.identity, instance.client, instance.instance])}>
                    <td data-label="Identity">{instance.identity}</td>
                    <td data-label="Client">{instance.client}</td>
                    <td className="mono" data-label="Instance">
                      {instance.instance}
                    </td>
                    <td className="mono" data-label="Received">
                      {lifecycleCell(instance.states.received)}
                    </td>
                    <td className="mono" data-label="Prepared">
                      {lifecycleCell(instance.states.prepared)}
                    </td>
                    <td className="mono" data-label="Applied">
                      {lifecycleCell(instance.states.applied)}
                    </td>
                    <td className="mono" data-label="Rejected">
                      {lifecycleCell(instance.states.rejected)}
                    </td>
                    <td data-label="Connection">
                      <Badge kind={instance.connected ? "success" : "neutral"}>
                        {instance.connected ? "connected" : "disconnected"}
                      </Badge>
                    </td>
                    <td data-label="Lag">
                      {Math.max(0, subscriberRevision - instance.latestRevision)}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </section>

      <ConfirmDialog
        open={pendingAction?.kind === "activate"}
        title="Activate release?"
        message={
          pendingAction?.kind === "activate" ? (
            <>
              Activate <span className="mono">{releaseKey(pendingAction.summary.release)}</span>
              {pendingCurrentRelease ? (
                <>
                  {" "}
                  in place of the current{" "}
                  <span className="mono">{releaseKey(pendingCurrentRelease.release)}</span>
                </>
              ) : (
                <> as the first active release for this name</>
              )}
              ? Subscribers will receive a new activation revision.
            </>
          ) : null
        }
        confirmLabel="Activate release"
        busy={busyAction === "activate"}
        onConfirm={() => void performPendingAction()}
        onCancel={() => setPendingAction(null)}
      />

      <ConfirmDialog
        open={pendingAction?.kind === "rollback"}
        title="Rollback release?"
        message={
          pendingAction?.kind === "rollback" ? (
            <>
              Replace current{" "}
              <span className="mono">{releaseKey(pendingAction.current.release)}</span> with
              previous <span className="mono">{releaseKey(pendingAction.previous.release)}</span>?{" "}
              Subscribers will receive a new activation revision; no immutable release version will
              be deleted.
            </>
          ) : null
        }
        confirmLabel="Rollback release"
        busy={busyAction === "rollback"}
        onConfirm={() => void performPendingAction()}
        onCancel={() => setPendingAction(null)}
      />
    </>
  );
}
