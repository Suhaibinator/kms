import { ArrowLeft, Plus, RefreshCw, SlidersHorizontal } from "lucide-react";
import { useRouter } from "next/router";
import { useCallback, useEffect, useMemo, useState } from "react";
import { AddEnvironmentModal } from "@/components/applications/AddEnvironmentModal";
import { ApplicationList } from "@/components/applications/ApplicationList";
import { BulkParameterModal } from "@/components/applications/BulkParameterModal";
import { ConfigurationMatrix } from "@/components/applications/ConfigurationMatrix";
import { ContractSummary } from "@/components/applications/ContractSummary";
import { CreateApplicationModal } from "@/components/applications/CreateApplicationModal";
import { QuickSecretModal } from "@/components/applications/QuickSecretModal";
import { LIST_HEADERS, type QuickSecretSeed } from "@/components/applications/shared";
import { Icon } from "@/components/icons";
import { EmptyState, PageHeader, TableSkeleton } from "@/components/ui";
import { Button, ButtonLink } from "@/components/ui/button";
import { useToast } from "@/context/ToastContext";
import { ApiError, api, isAbortError } from "@/lib/api";
import { utf8ToBase64 } from "@/lib/encoding";
import { useLatestRequest } from "@/lib/hooks";
import { links } from "@/lib/links";
import type { Application, ApplicationConfigurationRow, ApplicationDashboard } from "@/lib/types";

// A stable empty list: a fresh `[]` per render would re-fire every memo and
// effect keyed on the environment list whenever no dashboard is loaded.
const NO_ENVIRONMENTS: ApplicationDashboard["environments"] = [];

type DashboardStatus = "loading" | "success" | "not-found" | "error";

/**
 * The dashboard is keyed by the application it belongs to, so a response that
 * lands after the user has switched to another application can never be
 * rendered under the new header. `data` survives a refresh so the matrix does
 * not collapse to a skeleton while a reload is in flight.
 */
interface DashboardSlot {
  name: string;
  status: DashboardStatus;
  data: ApplicationDashboard | null;
}

export default function ApplicationsPage() {
  const router = useRouter();
  const toast = useToast();
  const request = useLatestRequest();
  const selectedName = typeof router.query.app === "string" ? router.query.app : "";
  const [applications, setApplications] = useState<Application[]>([]);
  const [dashboard, setDashboard] = useState<DashboardSlot | null>(null);
  const [loading, setLoading] = useState(true);
  const [createOpen, setCreateOpen] = useState(false);
  const [editOpen, setEditOpen] = useState(false);
  const [environmentOpen, setEnvironmentOpen] = useState(false);
  const [secretSeed, setSecretSeed] = useState<QuickSecretSeed | null>(null);
  const [writeRow, setWriteRow] = useState<ApplicationConfigurationRow | null>(null);
  const [retryEnvironments, setRetryEnvironments] = useState<string[] | null>(null);
  const [saving, setSaving] = useState(false);

  const loadApplications = useCallback(async () => {
    const run = request.begin();
    setLoading(true);
    try {
      const response = await api.listApplications(200, undefined, { signal: run.signal });
      if (!run.current) return;
      setApplications(response.applications ?? []);
    } catch (error) {
      if (run.current && !isAbortError(error)) toast.error(error, "Failed to load applications");
    } finally {
      if (run.current) setLoading(false);
    }
  }, [request, toast]);

  const loadDashboard = useCallback(async () => {
    if (!selectedName) return;
    const run = request.begin();
    setLoading(true);
    setDashboard((current) =>
      current?.name === selectedName
        ? { ...current, status: "loading" }
        : { name: selectedName, status: "loading", data: null },
    );
    try {
      const data = await api.applicationDashboard(selectedName, { signal: run.signal });
      if (!run.current) return;
      setDashboard({ name: selectedName, status: "success", data });
    } catch (error) {
      if (!run.current || isAbortError(error)) return;
      if (error instanceof ApiError && error.status === 404) {
        setDashboard({ name: selectedName, status: "not-found", data: null });
        return;
      }
      setDashboard((current) => ({
        name: selectedName,
        status: "error",
        data: current?.name === selectedName ? current.data : null,
      }));
      toast.error(error, "Failed to load application");
    } finally {
      if (run.current) setLoading(false);
    }
  }, [request, selectedName, toast]);

  useEffect(() => {
    if (!router.isReady) return;
    if (selectedName) void loadDashboard();
    else void loadApplications();
  }, [router.isReady, selectedName, loadApplications, loadDashboard]);

  async function selectApplication(name: string) {
    await router.push({ pathname: "/applications", query: { app: name } });
  }

  function openWrite(row: ApplicationConfigurationRow) {
    setRetryEnvironments(null);
    setWriteRow(row);
  }

  function closeWrite() {
    setWriteRow(null);
    // A partial failure still wrote the environments that succeeded; the
    // matrix is refreshed once the user is done retrying, not underneath them.
    if (retryEnvironments) {
      setRetryEnvironments(null);
      void loadDashboard();
    }
  }

  // Derived above the early returns below: hooks must run in the same order on
  // every render, and the list view returns before reaching the detail view.
  const activeSlot = dashboard?.name === selectedName ? dashboard : null;
  const activeDashboard = activeSlot?.data ?? null;
  const environments = activeDashboard?.environments ?? NO_ENVIRONMENTS;
  const environmentNames = useMemo(
    () => environments.map((environment) => environment.env),
    [environments],
  );

  // On a static export the query is empty until the client router hydrates, so
  // a deep link to one application would paint the list for a frame.
  if (!router.isReady) {
    return <TableSkeleton headers={LIST_HEADERS} rowHeight={62} />;
  }

  if (!selectedName) {
    return (
      <>
        <ApplicationList
          applications={applications}
          loading={loading}
          onCreate={() => setCreateOpen(true)}
        />
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

  if (activeSlot?.status === "not-found") {
    return (
      <>
        <PageHeader
          title="Application not found"
          documentTitle={selectedName}
          actions={
            <ButtonLink variant="outline" href={links.applications()}>
              <ArrowLeft size={16} aria-hidden /> Back to applications
            </ButtonLink>
          }
        />
        <EmptyState icon={<Icon.application size={20} />} title="Not found">
          No application named <span className="mono">{selectedName}</span> exists.
        </EmptyState>
      </>
    );
  }

  if (activeSlot?.status === "error" && !activeDashboard) {
    return (
      <>
        <PageHeader
          title="Could not load application"
          documentTitle={selectedName}
          actions={<Button onClick={() => void loadDashboard()}>Try again</Button>}
        />
        <EmptyState icon={<Icon.application size={20} />} title="Application unavailable">
          The server could not load <span className="mono">{selectedName}</span>. Check the
          connection and try again.
        </EmptyState>
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
              size="icon-sm"
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
          activeDashboard?.application.description ||
          "Application configuration across environments."
        }
        actions={
          <>
            <Button variant="outline" onClick={() => void loadDashboard()} disabled={loading}>
              <RefreshCw size={15} />
              Refresh
            </Button>
            <Button variant="outline" onClick={() => setEditOpen(true)} disabled={!activeDashboard}>
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
      {activeDashboard ? <ContractSummary application={activeDashboard.application} /> : null}
      {!activeDashboard ? (
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
          <div className="between mb-2">
            <div>
              <h2 className="section-title">Configuration matrix</h2>
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
                onClick={() => openWrite({ key: "", kind: "parameter", environments: {} })}
              >
                <Plus size={15} />
                New parameter
              </Button>
            </div>
          </div>
          <ConfigurationMatrix
            app={selectedName}
            environments={environments}
            rows={activeDashboard.rows}
            onAddSecret={(environment, key) => setSecretSeed({ environment, key })}
            onEdit={openWrite}
          />
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
        initial={activeDashboard?.application}
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
        retryEnvironments={retryEnvironments}
        saving={saving}
        onClose={closeWrite}
        onSave={async (request) => {
          setSaving(true);
          try {
            const response = await api.putApplicationParameter(request);
            const failures = response.results.filter((result) => result.error);
            if (failures.length === 0) {
              toast.success(
                "Values updated",
                `Created independent versions in ${response.results.length} environment(s).`,
              );
              setWriteRow(null);
              setRetryEnvironments(null);
              await loadDashboard();
              return;
            }
            toast.error(
              new Error(
                failures.map((result) => `${result.environment}: ${result.error}`).join("; "),
              ),
              "Some environments failed",
            );
            // Keep the modal and its edits; narrow the targets to what failed.
            setRetryEnvironments(failures.map((result) => result.environment));
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
