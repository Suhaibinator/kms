import { Filter, X } from "lucide-react";
import Link from "next/link";
import { useCallback, useEffect, useState } from "react";
import { Icon } from "@/components/icons";
import NamespacePicker, { type NamespaceSelection } from "@/components/NamespacePicker";
import {
  Badge,
  EmptyState,
  Field,
  Input,
  PageHeader,
  Pagination,
  TableSkeleton,
} from "@/components/ui";
import { Button, ButtonLink } from "@/components/ui/button";
import { useToast } from "@/context/ToastContext";
import { api, isAbortError } from "@/lib/api";
import { formatUnixMs } from "@/lib/format";
import { useCursorPagination, useLatestRequest, useNamespaces, useQueryParams } from "@/lib/hooks";
import { links } from "@/lib/links";
import type { SecretMetadata } from "@/lib/types";
import { useQueryReplace } from "@/lib/url";
import { validateKeyPrefix } from "@/lib/validation";

function currentVersion(s: SecretMetadata): number | null {
  const c = s.labels?.current;
  return typeof c === "number" ? c : null;
}

const NO_NS: NamespaceSelection = { env: "", app: "" };

/** Identifies the list a response belongs to, so a stale one cannot mark a
 *  different namespace/prefix/page as loaded. */
function requestScope(selection: NamespaceSelection, prefix: string, token: string): string {
  return JSON.stringify([selection.env, selection.app, prefix, token]);
}

export default function SecretsPage() {
  const toast = useToast();
  const { namespaces, error: nsError } = useNamespaces();
  const { values: queryValues, ready: queryReady } = useQueryParams(["env", "app", "key_prefix"]);
  const replaceQuery = useQueryReplace("/secrets");

  const [ns, setNs] = useState<NamespaceSelection>(NO_NS);
  const [prefixInput, setPrefixInput] = useState("");
  const [prefix, setPrefix] = useState("");
  const [prefixTouched, setPrefixTouched] = useState(false);

  const [secrets, setSecrets] = useState<SecretMetadata[]>([]);
  const [loading, setLoading] = useState(false);
  const [loadedScope, setLoadedScope] = useState("");
  const request = useLatestRequest();

  const paging = useCursorPagination(JSON.stringify([ns.env, ns.app, prefix]));
  const { pageToken, setNextToken } = paging;

  const [seeded, setSeeded] = useState(false);
  useEffect(() => {
    if (!queryReady || seeded) return;
    setSeeded(true);
    const env = queryValues.env ?? "";
    const app = queryValues.app ?? "";
    const kp = queryValues.key_prefix ?? "";
    if (env || app) setNs({ env, app });
    if (kp) {
      setPrefixInput(kp);
      setPrefix(kp);
    }
  }, [queryReady, queryValues, seeded]);

  useEffect(() => {
    if (nsError) toast.error(nsError, "Failed to load environments");
  }, [nsError, toast]);

  const hasNs = !!ns.env && !!ns.app;

  // An empty prefix lists the whole namespace; anything else must be a legal key.
  const prefixError = validateKeyPrefix(prefixInput.trim());
  const shownPrefixError = prefixTouched ? prefixError : null;

  const load = useCallback(
    async (token: string, selection: NamespaceSelection, activePrefix: string) => {
      const run = request.begin();
      const scope = requestScope(selection, activePrefix, token);
      if (!selection.env || !selection.app) {
        setSecrets([]);
        setNextToken("");
        setLoading(false);
        setLoadedScope(scope);
        return;
      }
      setLoading(true);
      try {
        const res = await api.listSecrets(
          { env: selection.env, app: selection.app },
          activePrefix || undefined,
          100,
          token || undefined,
          { signal: run.signal },
        );
        if (!run.current) return;
        setSecrets(res.secrets ?? []);
        setNextToken(res.next_page_token ?? "");
        setLoadedScope(scope);
      } catch (err) {
        if (!run.current || isAbortError(err)) return;
        // Leaving the previous namespace's rows on screen under the new header
        // is worse than an empty table: they look like this namespace's secrets.
        setSecrets([]);
        setNextToken("");
        setLoadedScope(scope);
        toast.error(err, "Failed to load secrets");
      } finally {
        if (run.current) setLoading(false);
      }
    },
    [request, setNextToken, toast],
  );

  useEffect(() => {
    void load(pageToken, ns, prefix);
  }, [load, pageToken, ns, prefix]);

  function onSelectNamespace(next: NamespaceSelection) {
    setNs(next);
    setSecrets([]);
    replaceQuery({ env: next.env, app: next.app });
  }
  function applyFilter(e: React.FormEvent) {
    e.preventDefault();
    setPrefixTouched(true);
    if (prefixError) return;
    const next = prefixInput.trim();
    setSecrets([]);
    setPrefix(next);
    replaceQuery({ key_prefix: next });
  }
  function clearFilter() {
    setPrefixInput("");
    setPrefixTouched(false);
    setSecrets([]);
    setPrefix("");
    replaceQuery({ key_prefix: "" });
  }

  const newSecretLink = hasNs ? links.newSecret(ns) : links.newSecret();

  // A deep link's env/app land one frame after mount, so "Choose an
  // environment" would flash before the list it asked for.
  const awaitingDeepLink = !seeded && (!queryReady || !!queryValues.env || !!queryValues.app);
  // A response has arrived for exactly this namespace/prefix/page. Gating on
  // this rather than on `loading` keeps the empty state from flashing before
  // the first request has even started.
  const settled = loadedScope === requestScope(ns, prefix, pageToken);

  return (
    <>
      <PageHeader
        title="Secrets"
        subtitle="Encrypted values, isolated by application and environment. Values are revealed only on the detail page."
        actions={<ButtonLink href={newSecretLink}>New secret</ButtonLink>}
      />

      <form className="filters" onSubmit={applyFilter}>
        <NamespacePicker namespaces={namespaces} value={ns} onChange={onSelectNamespace} />
        <div className="filter-grow">
          <Field label="Key prefix" error={shownPrefixError}>
            <Input
              id="key-prefix"
              className="font-mono"
              placeholder="billing"
              value={prefixInput}
              disabled={!hasNs}
              onChange={(e) => setPrefixInput(e.target.value)}
              onBlur={() => setPrefixTouched(true)}
            />
          </Field>
        </div>
        <Button type="submit" variant="outline" disabled={!hasNs || !!shownPrefixError}>
          <Filter size={15} aria-hidden />
          Filter
        </Button>
        <Button type="button" variant="ghost" onClick={clearFilter} disabled={!hasNs}>
          <X size={15} aria-hidden />
          Clear
        </Button>
      </form>

      {awaitingDeepLink ? (
        <TableSkeleton headers={["Key", "Type", "Current", "Versions", "Mode", "Updated"]} />
      ) : !hasNs ? (
        <EmptyState icon={<Icon.namespace size={20} />} title="Choose an environment">
          Pick an application and environment above to list its secrets.
        </EmptyState>
      ) : !settled || loading ? (
        <TableSkeleton headers={["Key", "Type", "Current", "Versions", "Mode", "Updated"]} />
      ) : secrets.length === 0 ? (
        <EmptyState
          icon={<Icon.secret size={20} />}
          title="No secrets found"
          actions={
            prefix ? (
              <Button variant="outline" onClick={clearFilter}>
                <X size={15} aria-hidden />
                Clear filter
              </Button>
            ) : (
              <ButtonLink href={newSecretLink}>New secret</ButtonLink>
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
                      <Link className="cell-path" href={links.secretDetail(s)}>
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
        hasNext={paging.hasNext}
        onNext={paging.next}
        hasPrevious={paging.hasPrevious}
        onPrevious={paging.previous}
        onReset={paging.reset}
        showReset={paging.hasPrevious}
        page={paging.page}
      />
    </>
  );
}
