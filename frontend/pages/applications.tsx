import { ArrowLeft } from "lucide-react";
import { useRouter } from "next/router";
import { useCallback, useEffect, useState } from "react";
import { ApplicationHome } from "@/components/applications/ApplicationHome";
import { ApplicationList } from "@/components/applications/ApplicationList";
import CreateApplicationWizard from "@/components/applications/CreateApplicationWizard";
import type { SetupAction } from "@/components/applications/contracts";
import { LIST_HEADERS } from "@/components/applications/shared";
import { useApplicationOverview } from "@/components/applications/useApplicationOverview";
import { Icon } from "@/components/icons";
import { EmptyState, PageHeader, TableSkeleton } from "@/components/ui";
import { Button, ButtonLink } from "@/components/ui/button";
import { useToast } from "@/context/ToastContext";
import { api, isAbortError } from "@/lib/api";
import { useLatestRequest, useQueryParams } from "@/lib/hooks";
import { links } from "@/lib/links";
import type { Application } from "@/lib/types";

/**
 * `/applications` lists applications; `?app=` opens one. The overview read
 * model drives everything on the application page; this file only decides
 * which surface to show and handles the states where there is no overview.
 */
export default function ApplicationsPage() {
  const router = useRouter();
  const toast = useToast();
  const request = useLatestRequest();
  const { values: query, ready } = useQueryParams(["app", "env", "ship", "tab", "rollback"]);
  const name = query.app ?? "";
  const [applications, setApplications] = useState<Application[]>([]);
  const [listLoading, setListLoading] = useState(true);
  const [wizardOpen, setWizardOpen] = useState(false);
  const { slot, loading, reload } = useApplicationOverview(name);

  const loadApplications = useCallback(async () => {
    const run = request.begin();
    setListLoading(true);
    try {
      const response = await api.listApplications(200, undefined, { signal: run.signal });
      if (!run.current) return;
      setApplications(response.applications ?? []);
    } catch (error) {
      if (run.current && !isAbortError(error)) toast.error(error, "Failed to load applications");
    } finally {
      if (run.current) setListLoading(false);
    }
  }, [request, toast]);

  useEffect(() => {
    if (ready && !name) void loadApplications();
  }, [ready, name, loadApplications]);

  // The first-run checklist (F-onboard) and the empty state speak the same
  // action vocabulary; only "create-app" applies before an application exists.
  function onSetupAction(action: SetupAction) {
    if (action.kind === "create-app") setWizardOpen(true);
  }

  // On a static export the query is empty until the client router hydrates, so
  // a deep link to one application would paint the list for a frame.
  if (!ready) {
    return <TableSkeleton headers={LIST_HEADERS} rowHeight={62} />;
  }

  if (!name) {
    return (
      <>
        <ApplicationList
          applications={applications}
          loading={listLoading}
          onCreate={() => onSetupAction({ kind: "create-app" })}
        />
        <CreateApplicationWizard
          open={wizardOpen}
          onClose={() => setWizardOpen(false)}
          onCreated={(application) => {
            setWizardOpen(false);
            void router.push(links.application(application.name));
          }}
        />
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

  if (!slot?.data) {
    return (
      <>
        <PageHeader title={<span className="mono">{name}</span>} documentTitle={name} />
        <TableSkeleton headers={["Values", "Release", "Subscribers"]} />
      </>
    );
  }

  return (
    <ApplicationHome
      key={name}
      overview={slot.data}
      loading={loading}
      reload={reload}
      env={query.env}
      ship={query.ship}
      tab={query.tab}
      rollback={query.rollback}
    />
  );
}
