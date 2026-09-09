import { parseSchemaVersion } from "@/lib/schema";
import { ArrowLeft } from "lucide-react";
import { useRouter } from "next/router";
import { useEffect, useState } from "react";
import { ApplicationHomeSkeleton } from "@/components/applications/ApplicationHomeSkeleton";
import { EnvironmentHome } from "@/components/applications/EnvironmentHome";
import { useApplicationOverview } from "@/components/applications/useApplicationOverview";
import { Icon } from "@/components/icons";
import { EmptyState, PageHeader, TableSkeleton } from "@/components/ui";
import { Button, ButtonLink } from "@/components/ui/button";
import { useQueryParams } from "@/lib/hooks";
import { links } from "@/lib/links";

/**
 * `/applications/environment?app=&env=` — one environment on its own page.
 * The application overview is the same read model the application page uses,
 * so both stay consistent; this file only picks the environment out of it and
 * handles the states where there is nothing to show.
 */
export default function EnvironmentPage() {
  const router = useRouter();
  const { values: query, ready } = useQueryParams([
    "app",
    "env",
    "ship",
    "rollback",
    "schema_version",
  ]);
  const name = query.app ?? "";
  const env = query.env ?? "";
  const [writing, setWriting] = useState(false);
  const schemaVersion = parseSchemaVersion(query.schema_version);
  const invalidSchema =
    query.schema_version !== null && query.schema_version !== "" && schemaVersion === undefined;
  const { slot, loading, reload, freshness } = useApplicationOverview(invalidSchema ? "" : name, {
    paused: writing,
    schemaVersion,
  });

  // Pin the schema track in the URL once it is known, so the breadcrumbs and
  // every link out of this page stay on the track that is being shown.
  // `router.replace` directly, not `useQueryReplace`: lib/url.ts forbids that
  // helper in an effect because it would fight the form state. This write is
  // not a form's — it records what the server answered with, it runs at most
  // once (guarded by `schemaVersion !== undefined`), and the application page
  // pins its own track the same way.
  useEffect(() => {
    if (!ready || !name || invalidSchema || schemaVersion !== undefined || !slot?.data) return;
    void router.replace(
      {
        pathname: "/applications/environment",
        query: { ...router.query, schema_version: String(slot.data.application.schema_version) },
      },
      undefined,
      { shallow: true, scroll: false },
    );
  }, [ready, name, invalidSchema, schemaVersion, slot?.data, router]);

  // On a static export the query is empty until the client router hydrates.
  if (!ready) {
    return (
      <TableSkeleton headers={["Alias", "Kind", "Key", "Current", "Pinned", "State"]} rows={6} />
    );
  }

  if (!name || !env) {
    return (
      <>
        <PageHeader
          title="No environment selected"
          documentTitle="Environment"
          actions={
            <ButtonLink variant="outline" href={links.applications()}>
              <ArrowLeft size={16} aria-hidden /> Back to applications
            </ButtonLink>
          }
        />
        <EmptyState icon={<Icon.namespace size={20} />} title="Nothing to show">
          This page needs both <span className="mono">app</span> and{" "}
          <span className="mono">env</span> in its link.
        </EmptyState>
      </>
    );
  }

  if (slot?.status === "not-found") {
    return (
      <>
        <PageHeader
          title="Application not found"
          documentTitle={name}
          actions={
            <ButtonLink variant="outline" href={links.applications()}>
              <ArrowLeft size={16} aria-hidden /> Back to applications
            </ButtonLink>
          }
        />
        <EmptyState icon={<Icon.application size={20} />} title="Not found">
          No application named <span className="mono">{name}</span> exists.
        </EmptyState>
      </>
    );
  }

  if (slot?.status === "forbidden") {
    return (
      <>
        <PageHeader
          title="Not permitted"
          documentTitle={name}
          actions={
            <ButtonLink variant="outline" href={links.namespaces()}>
              Open namespaces
            </ButtonLink>
          }
        />
        <EmptyState icon={<Icon.application size={20} />} title="Admin only">
          Managing <span className="mono">{name}</span> needs an admin identity. Your namespaces are
          still available.
        </EmptyState>
      </>
    );
  }

  if (slot?.status === "error" && !slot.data) {
    return (
      <>
        <PageHeader
          title="Could not load application"
          documentTitle={name}
          actions={<Button onClick={() => void reload()}>Try again</Button>}
        />
        <EmptyState icon={<Icon.application size={20} />} title="Application unavailable">
          The server could not load <span className="mono">{name}</span>. Check the connection and
          try again.
        </EmptyState>
      </>
    );
  }

  if (invalidSchema) {
    return (
      <EmptyState title="Invalid schema version">
        Use a nonnegative safe integer; 0 selects schema-free.
      </EmptyState>
    );
  }

  if (!slot?.data) {
    return <ApplicationHomeSkeleton name={name} />;
  }

  const environment = slot.data.environments.find((candidate) => candidate.namespace.env === env);

  if (!environment) {
    return (
      <>
        <PageHeader
          title="Environment not found"
          documentTitle={`${env} · ${name}`}
          actions={
            <ButtonLink variant="outline" href={links.application(name)}>
              <ArrowLeft size={16} aria-hidden /> Open {name}
            </ButtonLink>
          }
        />
        <EmptyState icon={<Icon.namespace size={20} />} title={`Environment not found in ${name}`}>
          <span className="mono">{env}</span> is not an environment of{" "}
          <span className="mono">{name}</span>. It may have been deleted, or the link may be stale.
        </EmptyState>
      </>
    );
  }

  return (
    <EnvironmentHome
      key={`${name}:${env}:${slot.data.application.schema_version}`}
      overview={slot.data}
      environment={environment}
      loading={loading}
      reload={reload}
      freshness={freshness}
      onWritingChange={setWriting}
      ship={query.ship}
      rollback={query.rollback}
      schemaVersion={slot.data.application.schema_version}
    />
  );
}
