import Link from "next/link";
import { useCallback, useEffect, useState } from "react";
import { api } from "@/lib/api";
import type { SecretMetadata } from "@/lib/types";
import { useToast } from "@/context/ToastContext";
import { formatUnixMs } from "@/lib/format";
import { Badge, EmptyState, Loading, PageHeader, Pagination } from "@/components/ui";

function secretLink(path: string): string {
  return `/secrets/detail?path=${encodeURIComponent(path)}`;
}

function currentVersion(s: SecretMetadata): number | null {
  const c = s.labels?.current;
  return typeof c === "number" ? c : null;
}

export default function SecretsPage() {
  const toast = useToast();
  const [secrets, setSecrets] = useState<SecretMetadata[]>([]);
  const [loading, setLoading] = useState(true);
  const [nextToken, setNextToken] = useState("");
  const [pageStack, setPageStack] = useState<string[]>([]);
  const [pageToken, setPageToken] = useState("");

  const [prefixInput, setPrefixInput] = useState("");
  const [prefix, setPrefix] = useState("");

  const load = useCallback(
    async (token: string, activePrefix: string) => {
      setLoading(true);
      try {
        const res = await api.listSecrets(activePrefix || undefined, 100, token || undefined);
        setSecrets(res.secrets ?? []);
        setNextToken(res.next_page_token ?? "");
      } catch (err) {
        toast.error(err, "Failed to load secrets");
      } finally {
        setLoading(false);
      }
    },
    [toast],
  );

  useEffect(() => {
    void load(pageToken, prefix);
  }, [load, pageToken, prefix]);

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
  function goReset() {
    setPageStack([]);
    setPageToken("");
  }

  return (
    <>
      <PageHeader
        title="Secrets"
        subtitle="Encrypted values. Metadata only is shown here — reveal happens on the detail page."
        actions={
          <Link className="btn btn-primary" href="/secrets/new">
            New secret
          </Link>
        }
      />

      <form className="filters" onSubmit={applyFilter}>
        <div className="field filter-grow">
          <label className="field-label" htmlFor="prefix">
            Path prefix
          </label>
          <input
            id="prefix"
            className="input mono"
            placeholder="/prod/payments"
            value={prefixInput}
            onChange={(e) => setPrefixInput(e.target.value)}
          />
        </div>
        <button type="submit" className="btn">
          Filter
        </button>
        <button type="button" className="btn btn-ghost" onClick={clearFilter}>
          Clear
        </button>
      </form>

      {loading ? (
        <Loading />
      ) : secrets.length === 0 ? (
        <EmptyState title="No secrets found">
          {prefix ? "No secrets match this prefix." : "Create a secret to get started."}
        </EmptyState>
      ) : (
        <div className="table-wrap card-table">
          <table className="data">
            <thead>
              <tr>
                <th>Path</th>
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
                  <tr key={s.path}>
                    <td data-label="Path">
                      <Link className="cell-path" href={secretLink(s.path)}>
                        {s.path}
                      </Link>
                    </td>
                    <td className="nowrap" data-label="Type">
                      {s.content_type || <span className="faint">—</span>}
                    </td>
                    <td data-label="Current">{cur !== null ? `v${cur}` : <span className="faint">—</span>}</td>
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
        onReset={goReset}
        showReset={pageStack.length > 0}
      />
    </>
  );
}
