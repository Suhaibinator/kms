import Link from "next/link";
import { useCallback, useEffect, useRef, useState } from "react";
import { Filter, X } from "lucide-react";
import { Icon } from "@/components/icons";
import NamespacePicker, { type NamespaceSelection } from "@/components/NamespacePicker";
import { Badge, EmptyState, PageHeader, Pagination, TableSkeleton } from "@/components/ui";
import { useToast } from "@/context/ToastContext";
import { api, isAbortError } from "@/lib/api";
import { formatUnixMs } from "@/lib/format";
import { useNamespaces, useQueryParams } from "@/lib/hooks";
import type { SecretMetadata } from "@/lib/types";

function secretLink(ref: { env: string; app: string; key: string }): string {
  return `/secrets/detail?env=${encodeURIComponent(ref.env)}&app=${encodeURIComponent(
    ref.app,
  )}&key=${encodeURIComponent(ref.key)}`;
}

function currentVersion(s: SecretMetadata): number | null {
  const c = s.labels?.current;
  return typeof c === "number" ? c : null;
}

const NO_NS: NamespaceSelection = { env: "", app: "" };

export default function SecretsPage() {
  const toast = useToast();
  const { namespaces, error: nsError } = useNamespaces();
  const { values: queryValues, ready: queryReady } = useQueryParams(["env", "app", "key_prefix"]);

  const [ns, setNs] = useState<NamespaceSelection>(NO_NS);
  const [prefixInput, setPrefixInput] = useState("");
  const [prefix, setPrefix] = useState("");

  const [secrets, setSecrets] = useState<SecretMetadata[]>([]);
  const [loading, setLoading] = useState(false);
  const [nextToken, setNextToken] = useState("");
  const [pageStack, setPageStack] = useState<string[]>([]);
  const [pageToken, setPageToken] = useState("");
  const requestRef = useRef<AbortController | null>(null);

  const seeded = useRef(false);
  useEffect(() => {
    if (!queryReady || seeded.current) return;
    seeded.current = true;
    const env = queryValues.env ?? "";
    const app = queryValues.app ?? "";
    const kp = queryValues.key_prefix ?? "";
    if (env || app) setNs({ env, app });
    if (kp) {
      setPrefixInput(kp);
      setPrefix(kp);
    }
  }, [queryReady, queryValues]);

  useEffect(() => {
    if (nsError) toast.error(nsError, "Failed to load namespaces");
  }, [nsError, toast]);

  const hasNs = !!ns.env && !!ns.app;

  const load = useCallback(
    async (token: string, selection: NamespaceSelection, activePrefix: string) => {
      requestRef.current?.abort();
      if (!selection.env || !selection.app) {
        setSecrets([]);
        setNextToken("");
        setLoading(false);
        return;
      }
      const controller = new AbortController();
      requestRef.current = controller;
      setLoading(true);
      try {
        const res = await api.listSecrets(
          { env: selection.env, app: selection.app },
          activePrefix || undefined,
          100,
          token || undefined,
          { signal: controller.signal },
        );
        if (requestRef.current !== controller) return;
        setSecrets(res.secrets ?? []);
        setNextToken(res.next_page_token ?? "");
      } catch (err) {
        if (!isAbortError(err)) toast.error(err, "Failed to load secrets");
      } finally {
        if (requestRef.current === controller) {
          requestRef.current = null;
          setLoading(false);
        }
      }
    },
    [toast],
  );

  useEffect(() => {
    void load(pageToken, ns, prefix);
    return () => {
      requestRef.current?.abort();
      requestRef.current = null;
    };
  }, [load, pageToken, ns, prefix]);

  function onSelectNamespace(next: NamespaceSelection) {
    setNs(next);
    setPageStack([]);
    setPageToken("");
  }
  function applyFilter(e: React.FormEvent) {
    e.preventDefault();
    setPageStack([]);
    setPageToken("");
    setPrefix(prefixInput.trim());
  }
  function clearFilter() {
    setPrefixInput("");
    setPageStack([]);
    setPageToken("");
    setPrefix("");
  }
  function goNext() {
    if (!nextToken) return;
    setPageStack((s) => [...s, pageToken]);
    setPageToken(nextToken);
  }
  function goPrevious() {
    const previous = pageStack[pageStack.length - 1];
    if (previous === undefined) return;
    setPageStack((s) => s.slice(0, -1));
    setPageToken(previous);
  }
  function goReset() {
    setPageStack([]);
    setPageToken("");
  }

  const newSecretLink = hasNs
    ? `/secrets/new?env=${encodeURIComponent(ns.env)}&app=${encodeURIComponent(ns.app)}`
    : "/secrets/new";

  return (
    <>
      <PageHeader
        title="Secrets"
        subtitle="Encrypted values, scoped to a namespace. Metadata only is shown here — reveal happens on the detail page."
        actions={
          <Link className="btn btn-primary" href={newSecretLink}>
            New secret
          </Link>
        }
      />

      <form className="filters" onSubmit={applyFilter}>
        <NamespacePicker namespaces={namespaces} value={ns} onChange={onSelectNamespace} />
        <div className="field filter-grow">
          <label className="field-label" htmlFor="key-prefix">
            Key prefix
          </label>
          <input
            id="key-prefix"
            className="input mono"
            placeholder="billing/"
            value={prefixInput}
            disabled={!hasNs}
            onChange={(e) => setPrefixInput(e.target.value)}
          />
        </div>
        <button type="submit" className="btn" disabled={!hasNs}>
          <Filter size={15} aria-hidden />
          Filter
        </button>
        <button type="button" className="btn btn-ghost" onClick={clearFilter} disabled={!hasNs}>
          <X size={15} aria-hidden />
          Clear
        </button>
      </form>

      {!hasNs ? (
        <EmptyState icon={<Icon.namespace size={20} />} title="Choose a namespace">
          Pick an environment and application above to list its secrets.
        </EmptyState>
      ) : loading ? (
        <TableSkeleton headers={["Key", "Type", "Current", "Versions", "Mode", "Updated"]} />
      ) : secrets.length === 0 ? (
        <EmptyState
          icon={<Icon.secret size={20} />}
          title="No secrets found"
          actions={
            prefix ? (
              <button className="btn" onClick={clearFilter}>
                <X size={15} aria-hidden />
                Clear filter
              </button>
            ) : (
              <Link className="btn btn-primary" href={newSecretLink}>
                New secret
              </Link>
            )
          }
        >
          {prefix ? "No secrets match this key prefix." : `No secrets in ${ns.env}/${ns.app} yet.`}
        </EmptyState>
      ) : (
        <div className="table-wrap card-table">
          <table className="data">
            <thead>
              <tr>
                <th>Key</th>
                <th>Type</th>
                <th>Current</th>
                <th>Versions</th>
                <th>Mode</th>
                <th>Updated</th>
              </tr>
            </thead>
            <tbody>
              {secrets.map((s) => {
                const cur = currentVersion(s);
                return (
                  <tr key={s.key}>
                    <td data-label="Key">
                      <Link className="cell-path" href={secretLink(s)}>
                        {s.key}
                      </Link>
                    </td>
                    <td className="nowrap" data-label="Type">
                      {s.content_type || <span className="faint">—</span>}
                    </td>
                    <td data-label="Current">
                      {cur !== null ? `v${cur}` : <span className="faint">—</span>}
                    </td>
                    <td data-label="Versions">{s.versions?.length ?? 0}</td>
                    <td data-label="Mode">
                      <div className="row-wrap">
                        {s.client_bound ? (
                          <Badge kind="warning">client-bound</Badge>
                        ) : (
                          <Badge kind="neutral">standard</Badge>
                        )}
                        {s.has_access_token ? <Badge kind="accent">access token</Badge> : null}
                      </div>
                    </td>
                    <td className="nowrap" data-label="Updated">
                      {formatUnixMs(s.updated_at_unix_ms)}
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}

      <Pagination
        hasNext={!!nextToken}
        onNext={goNext}
        hasPrevious={pageStack.length > 0}
        onPrevious={goPrevious}
        onReset={goReset}
        showReset={pageStack.length > 0}
      />
    </>
  );
}
