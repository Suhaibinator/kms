import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import NamespacePicker, { type NamespaceSelection } from "@/components/NamespacePicker";
import { Badge, EmptyState, Loading, PageHeader } from "@/components/ui";
import { useToast } from "@/context/ToastContext";
import { api, ApiError } from "@/lib/api";
import { displayPath } from "@/lib/format";
import { useNamespaces } from "@/lib/hooks";
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
  const [name, setName] = useState("");
  const [releases, setReleases] = useState<ReleaseSummary[]>([]);
  const [schemas, setSchemas] = useState<ConfigurationSchema[]>([]);
  const [subscribers, setSubscribers] = useState<ReleaseSubscriberState[]>([]);
	const [subscriberRevision, setSubscriberRevision] = useState(0);
	const [subscriberNextPageToken, setSubscriberNextPageToken] = useState("");
	const [subscriberCursor, setSubscriberCursor] = useState<{ scope: string; tokens: string[]; index: number }>({ scope: "", tokens: [""], index: 0 });
	const [loading, setLoading] = useState(false);
	const refreshGeneration = useRef(0);
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
	const subscriberScope = hasNS && name ? JSON.stringify([ns.env, ns.app, name]) : "";
	const activeSubscriberCursor = subscriberCursor.scope === subscriberScope
		? subscriberCursor
		: { scope: subscriberScope, tokens: [""], index: 0 };
	const subscriberPageToken = activeSubscriberCursor.tokens[activeSubscriberCursor.index] ?? "";

  useEffect(() => {
    if (namespaceError) toast.error(namespaceError, "Failed to load namespaces");
  }, [namespaceError, toast]);

  useEffect(() => {
    setActivationFailure(null);
  }, [ns.env, ns.app, name]);

	const refresh = useCallback(async () => {
		const generation = ++refreshGeneration.current;
		if (!hasNS) {
			setReleases([]);
			setSchemas([]);
			setSubscribers([]);
			setSubscriberRevision(0);
			setSubscriberNextPageToken("");
			setLoading(false);
			return;
    }
    setLoading(true);
    try {
      const [releaseResult, schemaResult] = await Promise.all([
        api.listReleases(ns, name || undefined, 100),
        api.listSchemas(),
      ]);
		if (generation !== refreshGeneration.current) return;
		setReleases(releaseResult.releases ?? []);
		setSchemas(schemaResult.schemas ?? []);
		if (name) {
			const status = await api.releaseSubscribers(ns, name, 1000, subscriberPageToken || undefined);
			if (generation !== refreshGeneration.current) return;
			setSubscribers(status.subscribers ?? []);
			setSubscriberRevision(status.current_revision ?? 0);
			setSubscriberNextPageToken(status.next_page_token ?? "");
			setSubscriberCursor((current) => current.scope === subscriberScope
				? current
				: { scope: subscriberScope, tokens: [""], index: 0 });
		} else {
			setSubscribers([]);
			setSubscriberRevision(0);
			setSubscriberNextPageToken("");
		}
	} catch (err) {
		if (generation === refreshGeneration.current) toast.error(err, "Failed to load releases");
	} finally {
		if (generation === refreshGeneration.current) setLoading(false);
	}
	}, [hasNS, name, ns, subscriberPageToken, subscriberScope, toast]);

	useEffect(() => {
		void refresh();
	}, [refresh]);

	function previousSubscriberPage() {
		setSubscriberCursor({ ...activeSubscriberCursor, index: Math.max(0, activeSubscriberCursor.index - 1) });
	}

	function nextSubscriberPage() {
		if (!subscriberNextPageToken || subscriberNextPageToken === subscriberPageToken) return;
		const tokens = activeSubscriberCursor.tokens.slice(0, activeSubscriberCursor.index + 1);
		tokens.push(subscriberNextPageToken);
		setSubscriberCursor({ ...activeSubscriberCursor, tokens, index: activeSubscriberCursor.index + 1 });
	}

  async function createRelease() {
    if (!hasNS) return;
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
      setName(result.release.name);
      await refresh();
    } catch (err) {
      toast.error(err, "Could not create release");
    }
  }

  async function validate(release: ConfigurationRelease) {
    try {
      const result = await api.validateRelease(ns, release.name, release.version);
      if (result.valid) toast.success(`${releaseKey(release)} is valid`);
      else toast.error(new Error(result.errors.map((e) => `${e.alias || "release"}: ${e.message}`).join("; ")), "Validation failed");
    } catch (err) {
      toast.error(err, "Validation failed");
    }
  }

  async function activate(summary: ReleaseSummary) {
    setActivationFailure(null);
    try {
      let expected = 0;
      try {
        const active = await api.getActiveRelease(ns, summary.release.name);
        expected = active.release.version;
      } catch (err) {
        // No active release is represented by the presence-aware CAS value 0.
        if (!(err instanceof ApiError) || err.code !== "not_found") throw err;
      }
      await api.activateRelease(ns, summary.release.name, summary.release.version, expected);
      toast.success(`Activated ${releaseKey(summary.release)}`);
      await refresh();
    } catch (err) {
      const violations = activationViolations(err);
      if (violations) {
        setActivationFailure({
          operation: "Activation",
          target: releaseKey(summary.release),
          violations,
        });
        return;
      }
      toast.error(err, "Activation failed");
    }
  }

  async function rollback() {
    if (!name) return;
    setActivationFailure(null);
    let target = name;
    try {
      const active = await api.getActiveRelease(ns, name);
      if (!active.previous_version) throw new Error("No previous release is available");
      target = `${name}@${active.previous_version}`;
      await api.activateRelease(ns, name, active.previous_version, active.release.version);
      toast.success(`Rolled back ${name} to version ${active.previous_version}`);
      await refresh();
    } catch (err) {
      const violations = activationViolations(err);
      if (violations) {
        setActivationFailure({
          operation: "Rollback",
          target,
          violations,
        });
        return;
      }
      toast.error(err, "Rollback failed");
    }
  }

  async function createSchema() {
    try {
      JSON.parse(schemaJSON);
      const result = await api.createSchema(schemaID.trim(), schemaJSON);
      toast.success(`Created schema ${result.schema.id}@${result.schema.version}`);
      setSchemaID("");
      await refresh();
    } catch (err) {
      toast.error(err, "Could not create schema");
    }
  }

  const diff = useMemo(() => {
    const from = releases.find((r) => releaseKey(r.release) === diffFrom)?.release;
    const to = releases.find((r) => releaseKey(r.release) === diffTo)?.release;
    if (!from || !to) return [];
    const aliases = new Set([...from.entries.map((e) => e.alias), ...to.entries.map((e) => e.alias)]);
    return [...aliases].sort().flatMap((alias) => {
      const a = from.entries.find((e) => e.alias === alias);
      const b = to.entries.find((e) => e.alias === alias);
      const left = a ? `${a.kind} ${refText(a)}@${a.version}${a.parameter_digest ? ` ${a.parameter_digest.slice(0, 12)}` : ""}` : "—";
      const right = b ? `${b.kind} ${refText(b)}@${b.version}${b.parameter_digest ? ` ${b.parameter_digest.slice(0, 12)}` : ""}` : "—";
      return left === right ? [] : [{ alias, left, right }];
    });
  }, [diffFrom, diffTo, releases]);

  const subscriberInstances = useMemo(() => {
    const grouped = new Map<string, {
      identity: string;
      client: string;
      instance: string;
      connected: boolean;
      states: Partial<Record<"received" | "prepared" | "applied" | "rejected", ReleaseSubscriberState>>;
      latestRevision: number;
    }>();
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
    return [...grouped.values()].sort((a, b) => a.identity.localeCompare(b.identity) || a.client.localeCompare(b.client) || a.instance.localeCompare(b.instance));
  }, [subscribers]);

  const selectedRelease = releases.find((summary) => releaseKey(summary.release) === selectedReleaseKey)?.release;

  function lifecycleCell(state: ReleaseSubscriberState | undefined) {
    if (!state) return <span className="faint">—</span>;
    const detail = state.state === "rejected" && state.rejection_category
      ? `${state.rejection_category} · `
      : "";
    return <span title={state.diagnostic || undefined}>{detail}v{state.release_version} · r{state.activation_revision}</span>;
  }

  return (
    <>
      <PageHeader
        title="Configuration releases"
        subtitle="Atomically activate exact parameter and secret versions. Secret values and tokens are never displayed."
        actions={<button className="btn" onClick={() => void refresh()}>Refresh</button>}
      />

      <div className="filters">
        <NamespacePicker namespaces={namespaces} value={ns} onChange={setNS} />
        <div className="field filter-grow">
          <label className="field-label" htmlFor="release-name">Release name</label>
          <input id="release-name" className="input mono" value={name} onChange={(e) => setName(e.target.value)} placeholder="runtime" disabled={!hasNS} />
        </div>
        <button className="btn" disabled={!hasNS || !name} onClick={() => void rollback()}>Rollback to previous</button>
      </div>

      {activationFailure ? (
        <section className="danger-panel mb-16" role="alert" aria-labelledby="activation-failure-title">
          <div className="between">
            <div>
              <h2 id="activation-failure-title" className="text-danger">
                {activationFailure.operation} blocked for <span className="mono">{activationFailure.target}</span>
              </h2>
              <div className="text-sm mt-8">
                The active release and activation revision were not changed. Resolve the violations below and try again.
              </div>
            </div>
            <button className="btn btn-sm" onClick={() => setActivationFailure(null)}>Dismiss</button>
          </div>
          <div className="table-wrap activation-violations mt-12">
            <table className="data">
              <thead><tr><th>Alias</th><th>Code</th><th>Schema pointer</th><th>Message</th></tr></thead>
              <tbody>{activationFailure.violations.map((violation, index) => (
                <tr key={`${violation.alias}-${violation.code}-${violation.schema_pointer}-${index}`}>
                  <td className="mono">{violation.alias || <span className="faint">release</span>}</td>
                  <td><Badge kind="danger">{violation.code}</Badge></td>
                  <td className="mono">{violation.schema_pointer || <span className="faint">—</span>}</td>
                  <td>{violation.message}</td>
                </tr>
              ))}</tbody>
            </table>
          </div>
        </section>
      ) : null}

      {!hasNS ? (
        <EmptyState title="Choose a namespace">Select a namespace to manage its releases.</EmptyState>
      ) : loading ? <Loading /> : releases.length === 0 ? (
        <EmptyState title="No releases found">Create an immutable release using the definition editor below.</EmptyState>
      ) : (
        <div className="table-wrap card-table mb-16">
          <table className="data">
            <thead><tr><th>Release</th><th>State</th><th>Schema</th><th>Entries</th><th>Digest</th><th>Actions</th></tr></thead>
            <tbody>{releases.map((summary) => {
              const r = summary.release;
              return <tr key={releaseKey(r)}>
                <td className="mono">{releaseKey(r)}</td>
                <td>{summary.current ? <Badge kind="success">current · rev {summary.activation_revision}</Badge> : summary.previous ? <Badge kind="warning">previous</Badge> : <Badge>inactive</Badge>}</td>
                <td className="mono">{r.schema_id ? `${r.schema_id}@${r.schema_version}` : <span className="faint">none</span>}</td>
                <td>{r.entries.length}</td>
                <td className="mono" title={r.digest}>{r.digest.slice(0, 16)}…</td>
                <td className="row-wrap"><button className="btn btn-sm" onClick={() => setSelectedReleaseKey(releaseKey(r))}>View</button><button className="btn btn-sm" onClick={() => void validate(r)}>Validate</button><button className="btn btn-sm btn-primary" disabled={summary.current} onClick={() => void activate(summary)}>Activate</button></td>
              </tr>;
            })}</tbody>
          </table>
        </div>
      )}

      {selectedRelease ? <section className="card mb-16">
        <h2>{releaseKey(selectedRelease)} details</h2>
        <div className="text-sm faint mb-12">Digest <span className="mono">{selectedRelease.digest}</span>{selectedRelease.schema_id ? <> · Schema <span className="mono">{selectedRelease.schema_id}@{selectedRelease.schema_version}</span></> : null}</div>
        <div className="table-wrap"><table className="data"><thead><tr><th>Alias</th><th>Kind</th><th>Reference</th><th>Version</th><th>Content type</th><th>Parameter digest</th><th>Secret protection</th><th>Metadata</th></tr></thead><tbody>{selectedRelease.entries.map((entry) => <tr key={entry.alias}><td className="mono">{entry.alias}</td><td>{entry.kind}</td><td className="mono">{refText(entry)}</td><td>{entry.version}</td><td>{entry.content_type || <span className="faint">—</span>}</td><td className="mono">{entry.parameter_digest || <span className="faint">—</span>}</td><td>{entry.kind === "secret" ? [entry.has_access_token ? "token" : "no token", entry.client_bound ? "client-bound" : "shared"].join(" · ") : <span className="faint">—</span>}</td><td className="mono">{entry.metadata_json || "{}"}</td></tr>)}</tbody></table></div>
      </section> : null}

      <div className="card-grid mb-16">
        <section className="card">
          <h2>Create release</h2>
          <p className="text-sm faint">Paste a JSON definition. Relative references should use the selected namespace.</p>
          <textarea className="input mono" rows={14} value={definition} onChange={(e) => setDefinition(e.target.value)} />
          <div className="mt-12"><button className="btn btn-primary" disabled={!hasNS} onClick={() => void createRelease()}>Create immutable version</button></div>
        </section>
        <section className="card">
          <h2>Register JSON Schema</h2>
          <input className="input mono mb-8" placeholder="go-common/runtime" value={schemaID} onChange={(e) => setSchemaID(e.target.value)} />
          <textarea className="input mono" rows={11} value={schemaJSON} onChange={(e) => setSchemaJSON(e.target.value)} />
          <div className="mt-12"><button className="btn" disabled={!schemaID.trim()} onClick={() => void createSchema()}>Register schema version</button></div>
          <div className="text-sm faint mt-12">{schemas.length} registered schema versions</div>
          {schemas.length ? <div className="table-wrap mt-12"><table className="data"><thead><tr><th>Schema</th><th>Digest</th></tr></thead><tbody>{schemas.map((schema) => <tr key={`${schema.id}@${schema.version}`}><td className="mono" title={schema.schema_json}>{schema.id}@{schema.version}</td><td className="mono" title={schema.digest}>{schema.digest.slice(0, 12)}…</td></tr>)}</tbody></table></div> : null}
        </section>
      </div>

      <section className="card mb-16">
        <h2>Diff versions</h2>
        <div className="row-wrap mb-12">
          <select className="select" value={diffFrom} onChange={(e) => setDiffFrom(e.target.value)}><option value="">From…</option>{releases.map((r) => <option key={`f-${releaseKey(r.release)}`} value={releaseKey(r.release)}>{releaseKey(r.release)}</option>)}</select>
          <select className="select" value={diffTo} onChange={(e) => setDiffTo(e.target.value)}><option value="">To…</option>{releases.map((r) => <option key={`t-${releaseKey(r.release)}`} value={releaseKey(r.release)}>{releaseKey(r.release)}</option>)}</select>
        </div>
        {diffFrom && diffTo && diff.length === 0 ? <div className="faint">No manifest differences.</div> : diff.length ? <div className="table-wrap"><table className="data"><thead><tr><th>Alias</th><th>From</th><th>To</th></tr></thead><tbody>{diff.map((d) => <tr key={d.alias}><td className="mono">{d.alias}</td><td className="mono">{d.left}</td><td className="mono">{d.right}</td></tr>)}</tbody></table></div> : null}
      </section>

	<section className="card">
		<h2>Release subscribers</h2>
		{name ? <div className="row-wrap mb-12">
			<button className="btn btn-sm" disabled={activeSubscriberCursor.index === 0} onClick={previousSubscriberPage}>Previous page</button>
			<button className="btn btn-sm" disabled={!subscriberNextPageToken || subscriberNextPageToken === subscriberPageToken} onClick={nextSubscriberPage}>Next page</button>
			<span className="text-sm faint">Page {activeSubscriberCursor.index + 1} · up to 1,000 state rows</span>
		</div> : null}
		{!name ? <div className="faint">Enter a release name to view per-instance state.</div> : subscriberInstances.length === 0 ? <div className="faint">No subscriber state recorded.</div> : <div className="table-wrap"><table className="data"><thead><tr><th>Identity</th><th>Client</th><th>Instance</th><th>Received</th><th>Prepared</th><th>Applied</th><th>Rejected</th><th>Connection</th><th>Lag</th></tr></thead><tbody>{subscriberInstances.map((instance) => <tr key={JSON.stringify([instance.identity, instance.client, instance.instance])}><td>{instance.identity}</td><td>{instance.client}</td><td className="mono">{instance.instance}</td><td className="mono">{lifecycleCell(instance.states.received)}</td><td className="mono">{lifecycleCell(instance.states.prepared)}</td><td className="mono">{lifecycleCell(instance.states.applied)}</td><td className="mono">{lifecycleCell(instance.states.rejected)}</td><td><Badge kind={instance.connected ? "success" : "neutral"}>{instance.connected ? "connected" : "disconnected"}</Badge></td><td>{Math.max(0, subscriberRevision - instance.latestRevision)}</td></tr>)}</tbody></table></div>}
      </section>
    </>
  );
}
