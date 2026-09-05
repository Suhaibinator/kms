import { ChevronRight, Plus } from "lucide-react";
import Link from "next/link";
import { useId, useMemo, useState } from "react";
import { Icon } from "@/components/icons";
import {
  Badge,
  EmptyState,
  Field,
  Input,
  PageHeader,
  Pagination,
  TableSkeleton,
} from "@/components/ui";
import { AppSelect } from "@/components/ui/app-select";
import { Button } from "@/components/ui/button";
import type { Application } from "@/lib/types";
import { LIST_HEADERS } from "./shared";

export interface ApplicationListPaging {
  page: number;
  hasNext: boolean;
  hasPrevious: boolean;
  next: () => void;
  previous: () => void;
  reset: () => void;
}

export type ApplicationArchiveFilter = "exclude" | "include" | "only";

/** Case-insensitive match on name or description. */
export function matchesApplication(app: Application, query: string): boolean {
  const needle = query.trim().toLowerCase();
  if (!needle) return true;
  return (
    app.name.toLowerCase().includes(needle) ||
    (app.description ?? "").toLowerCase().includes(needle)
  );
}

// The skeleton reserves roughly the rows the last render had, so paging or
// refreshing does not collapse the table and grow it back.
let lastRowCount = 8;

export function ApplicationList({
  applications,
  loading,
  onCreate,
  paging,
  archiveFilter = "exclude",
  onArchiveFilterChange,
}: {
  applications: Application[];
  loading: boolean;
  onCreate: () => void;
  /** Server-side cursor over the list; the filter narrows the current page. */
  paging?: ApplicationListPaging;
  archiveFilter?: ApplicationArchiveFilter;
  onArchiveFilterChange?: (filter: ApplicationArchiveFilter) => void;
}) {
  const [query, setQuery] = useState("");
  const filterId = useId();
  const filtered = useMemo(
    () => applications.filter((app) => matchesApplication(app, query)),
    [applications, query],
  );
  if (!loading) lastRowCount = Math.max(3, applications.length);
  const firstPage = !paging || (paging.page === 1 && !paging.hasNext);

  return (
    <>
      <PageHeader
        title="Applications"
        subtitle="One application owns a shared configuration contract; each environment supplies isolated values."
        actions={
          <Button onClick={onCreate}>
            <Plus size={15} />
            New application
          </Button>
        }
      />
      <div className="info-panel mb-4">
        Create an application once, then add environments such as <code>dev</code>,{" "}
        <code>prod</code>, and <code>prod-gcp</code>. Environments never inherit values from one
        another; the application contract keeps their release shape consistent.
      </div>
      {loading || applications.length > 0 || query || onArchiveFilterChange ? (
        <div className="application-list-toolbar">
          <label htmlFor={filterId} className="sr-only">
            Filter applications
          </label>
          <Input
            id={filterId}
            type="search"
            className="application-list-filter"
            placeholder={firstPage ? "Filter by name or description" : "Filter this page"}
            value={query}
            onChange={(event) => setQuery(event.target.value)}
            disabled={loading && applications.length === 0}
          />
          {onArchiveFilterChange ? (
            <Field label="Lifecycle">
              <AppSelect
                value={archiveFilter}
                onValueChange={(value) => onArchiveFilterChange(value as ApplicationArchiveFilter)}
                options={[
                  { value: "exclude", label: "Active applications" },
                  { value: "include", label: "All applications" },
                  { value: "only", label: "Archived applications" },
                ]}
              />
            </Field>
          ) : null}
          {query && !loading ? (
            <span className="faint text-sm" role="status">
              {filtered.length} of {applications.length} shown
            </span>
          ) : null}
        </div>
      ) : null}
      {loading ? (
        <TableSkeleton headers={LIST_HEADERS} rowHeight={62} rows={lastRowCount} />
      ) : applications.length === 0 ? (
        <EmptyState
          icon={<Icon.application size={20} />}
          title="No applications yet"
          actions={<Button onClick={onCreate}>New application</Button>}
        >
          Define the application-owned shape before adding deployment environments.
        </EmptyState>
      ) : filtered.length === 0 ? (
        <EmptyState
          icon={<Icon.application size={20} />}
          title="No matching applications"
          actions={
            <Button variant="outline" onClick={() => setQuery("")}>
              Clear filter
            </Button>
          }
        >
          Nothing on this page matches <span className="mono">{query.trim()}</span>.
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
              {filtered.map((app) => (
                <tr key={app.name} className="navigable-row">
                  <td>
                    <Link
                      className="navigable-row-link"
                      href={{ pathname: "/applications", query: { app: app.name } }}
                      aria-label={`Manage ${app.name}`}
                    >
                      <strong className="mono">{app.name}</strong>
                      {app.archived_at_unix_ms ? <Badge kind="neutral">archived</Badge> : null}
                    </Link>
                    <div className="faint text-sm">{app.description || "No description"}</div>
                  </td>
                  <td>{app.environment_count}</td>
                  <td className="mono">{app.release_name}</td>
                  <td className="mono">
                    {app.schema_version
                      ? `${app.name}/${app.release_name}@${app.schema_version}`
                      : "—"}
                  </td>
                  <td>{app.contract.length} aliases</td>
                  <td className="navigable-row-chevron" aria-hidden="true">
                    <ChevronRight size={18} />
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {paging ? (
        <Pagination
          hasNext={paging.hasNext}
          onNext={paging.next}
          hasPrevious={paging.hasPrevious}
          onPrevious={paging.previous}
          onReset={paging.reset}
          showReset={paging.page > 1}
          page={paging.page}
          count={loading ? undefined : filtered.length}
          loading={loading}
          noun="applications"
        />
      ) : null}
    </>
  );
}
