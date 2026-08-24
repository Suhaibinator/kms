import { ChevronRight, Plus } from "lucide-react";
import Link from "next/link";
import { Icon } from "@/components/icons";
import { EmptyState, PageHeader, TableSkeleton } from "@/components/ui";
import { Button } from "@/components/ui/button";
import type { Application } from "@/lib/types";
import { LIST_HEADERS } from "./shared";

export function ApplicationList({
  applications,
  loading,
  onCreate,
}: {
  applications: Application[];
  loading: boolean;
  onCreate: () => void;
}) {
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
      {loading ? (
        <TableSkeleton headers={LIST_HEADERS} rowHeight={62} />
      ) : applications.length === 0 ? (
        <EmptyState
          icon={<Icon.application size={20} />}
          title="No applications yet"
          actions={<Button onClick={onCreate}>New application</Button>}
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
    </>
  );
}
