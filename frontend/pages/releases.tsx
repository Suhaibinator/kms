import { Plus, RefreshCw } from "lucide-react";
import { useRouter } from "next/router";
import { type FormEvent, useCallback, useEffect, useRef, useState } from "react";
import { Icon } from "@/components/icons";
import { ConfirmDialog } from "@/components/Modal";
import NamespacePicker, { type NamespaceSelection } from "@/components/NamespacePicker";
import { ReleaseBuilder } from "@/components/releases/ReleaseBuilder";
import { type ActivationFailure, ReleaseWorkspace } from "@/components/releases/ReleaseWorkspace";
import { SchemaRegistry } from "@/components/releases/SchemaRegistry";
import { releaseKey } from "@/components/releases/utils";
import {
  Badge,
  Button,
  EmptyState,
  Field,
  Input,
  PageHeader,
  Pagination,
  Spinner,
  TableSkeleton,
} from "@/components/ui";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { useToast } from "@/context/ToastContext";
import { ApiError, api, isAbortError } from "@/lib/api";
import { useCursorPagination, useNamespaces } from "@/lib/hooks";
import type { ConfigurationRelease, ReleaseSummary, ReleaseValidationError } from "@/lib/types";
import { validateReleaseName } from "@/lib/validation";

const NO_NS: NamespaceSelection = { env: "", app: "" };

type PendingReleaseAction =
  | { kind: "activate"; summary: ReleaseSummary }
  | { kind: "rollback"; name: string; current: ReleaseSummary; previous: ReleaseSummary };

type BusyReleaseAction = "" | "activate" | "rollback" | `validate:${string}`;

function activationViolations(error: unknown): ReleaseValidationError[] | null {
  if (!(error instanceof ApiError) || error.code !== "failed_precondition") return null;
  return error.validationErrors.length > 0 ? error.validationErrors : null;
}

function queryValue(value: string | string[] | undefined): string {
  return Array.isArray(value) ? (value[0] ?? "") : (value ?? "");
}

export default function ReleasesPage() {
  const router = useRouter();
  const toast = useToast();
  const { namespaces, error: namespaceError } = useNamespaces();
  const [activeTab, setActiveTab] = useState<"releases" | "schemas">("releases");
  const [ns, setNS] = useState<NamespaceSelection>(NO_NS);
  const [nameDraft, setNameDraft] = useState("");
  const [name, setName] = useState("");
  const [nameTouched, setNameTouched] = useState(false);
  const [releases, setReleases] = useState<ReleaseSummary[]>([]);
  const [releasesLoading, setReleasesLoading] = useState(false);
  const [busyAction, setBusyAction] = useState<BusyReleaseAction>("");
  const [builderOpen, setBuilderOpen] = useState(false);
  const [selectedReleaseKey, setSelectedReleaseKey] = useState("");
  const [pendingAction, setPendingAction] = useState<PendingReleaseAction | null>(null);
  const [activationFailure, setActivationFailure] = useState<ActivationFailure | null>(null);
  const refreshGeneration = useRef(0);
  const loadedReleaseScope = useRef("");
  const refreshController = useRef<AbortController | null>(null);

  const queryTab = queryValue(router.query.tab);
  const queryApp = queryValue(router.query.app);
  const queryEnv = queryValue(router.query.env);
  const queryName = queryValue(router.query.name);

  useEffect(() => {
    if (!router.isReady) return;
    setActiveTab(queryTab === "schemas" ? "schemas" : "releases");
    setNS({ app: queryApp, env: queryEnv });
    setNameDraft(queryName);
    setName(queryName);
  }, [queryApp, queryEnv, queryName, queryTab, router.isReady]);

  function replaceQuery(patch: Record<string, string>) {
    const next: Record<string, string> = {};
    for (const [key, value] of Object.entries(router.query)) {
      const normalized = queryValue(value);
      if (normalized) next[key] = normalized;
    }
    for (const [key, value] of Object.entries(patch)) {
      if (value) next[key] = value;
      else delete next[key];
    }
    void router.replace({ pathname: "/releases", query: next }, undefined, {
      shallow: true,
      scroll: false,
    });
  }

  function changeTab(value: string | number) {
    const next = value === "schemas" ? "schemas" : "releases";
    setActiveTab(next);
    replaceQuery({ tab: next === "schemas" ? "schemas" : "" });
  }

  function changeNamespace(next: NamespaceSelection) {
    setActivationFailure(null);
    setPendingAction(null);
    setSelectedReleaseKey("");
    setNS(next);
    setNameDraft("");
    setName("");
    loadedReleaseScope.current = "";
    replaceQuery({ app: next.app, env: next.env, name: "" });
  }

  const hasNS = Boolean(ns.env && ns.app);
  const releaseScope = hasNS ? JSON.stringify([ns.env, ns.app, name]) : "";
  const releasePaging = useCursorPagination(releaseScope);
  const releaseRequestScope = JSON.stringify([releaseScope, releasePaging.pageToken]);
  const nameFilterError = validateReleaseName(nameDraft.trim());

  useEffect(() => {
    if (namespaceError) toast.error(namespaceError, "Failed to load environments");
  }, [namespaceError, toast]);

  const refresh = useCallback(
    async (force = false) => {
      refreshController.current?.abort();
      const generation = ++refreshGeneration.current;
      if (!hasNS || activeTab !== "releases") {
        refreshController.current = null;
        loadedReleaseScope.current = "";
        setReleases([]);
        releasePaging.setNextToken("");
        setReleasesLoading(false);
        return;
      }
      const shouldLoad = force || loadedReleaseScope.current !== releaseRequestScope;
      if (!shouldLoad) return;
      const controller = new AbortController();
      refreshController.current = controller;
      setReleasesLoading(true);
      try {
        const response = await api.listReleases(
          ns,
          name || undefined,
          100,
          releasePaging.pageToken || undefined,
          { signal: controller.signal },
        );
        if (generation !== refreshGeneration.current) return;
        loadedReleaseScope.current = releaseRequestScope;
        setReleases(response.releases ?? []);
        releasePaging.setNextToken(response.next_page_token ?? "");
      } catch (error) {
        if (generation === refreshGeneration.current && !isAbortError(error)) {
          toast.error(error, "Failed to load releases");
        }
      } finally {
        if (generation === refreshGeneration.current) {
          refreshController.current = null;
          setReleasesLoading(false);
        }
      }
    },
    [
      activeTab,
      hasNS,
      name,
      ns,
      releasePaging.pageToken,
      releasePaging.setNextToken,
      releaseRequestScope,
      toast,
    ],
  );

  useEffect(() => {
    void refresh();
    return () => refreshController.current?.abort();
  }, [refresh]);

  function applyNameFilter(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setNameTouched(true);
    if (nameFilterError) return;
    const next = nameDraft.trim();
    setActivationFailure(null);
    setSelectedReleaseKey("");
    setName(next);
    loadedReleaseScope.current = "";
    replaceQuery({ name: next });
  }

  async function validate(release: ConfigurationRelease) {
    if (busyAction) return;
    setBusyAction(`validate:${releaseKey(release)}`);
    try {
      const result = await api.validateRelease(release.namespace, release.name, release.version);
      if (result.valid) toast.success(`${releaseKey(release)} is valid`);
      else {
        toast.error(
          new Error(
            result.errors
              .map((error) => `${error.alias || "release"}: ${error.message}`)
              .join("; "),
          ),
          "Validation failed",
        );
      }
    } catch (error) {
      toast.error(error, "Validation failed");
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
      const active = await api.getActiveRelease(ns, releaseName).catch((error: unknown) => {
        if (action.kind === "activate" && error instanceof ApiError && error.code === "not_found") {
          return null;
        }
        throw error;
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
    } catch (error) {
      const violations = activationViolations(error);
      if (violations) {
        const targetSummary = action.kind === "activate" ? action.summary : action.previous;
        setSelectedReleaseKey(releaseKey(targetSummary.release));
        setActivationFailure({
          operation: action.kind === "activate" ? "Activation" : "Rollback",
          target,
          violations,
        });
        setPendingAction(null);
      } else {
        toast.error(error, action.kind === "activate" ? "Activation failed" : "Rollback failed");
      }
    } finally {
      setBusyAction("");
    }
  }

  const selectedSummary =
    releases.find((summary) => releaseKey(summary.release) === selectedReleaseKey) ?? null;
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

  return (
    <>
      <PageHeader
        title="Configuration releases"
        subtitle="Build, validate, activate, and inspect immutable configuration manifests."
        actions={
          activeTab === "releases" ? (
            <>
              <Button
                variant="outline"
                disabled={!hasNS || releasesLoading || Boolean(busyAction)}
                onClick={() => void refresh(true)}
              >
                {releasesLoading ? <Spinner /> : <RefreshCw size={16} aria-hidden />}
                Refresh
              </Button>
              <Button disabled={!hasNS || Boolean(busyAction)} onClick={() => setBuilderOpen(true)}>
                <Plus size={16} aria-hidden /> New release
              </Button>
            </>
          ) : null
        }
      />

      <Tabs value={activeTab} onValueChange={changeTab} className="releases-page-tabs">
        <TabsList variant="line" aria-label="Configuration release tools" className="mb-4">
          <TabsTrigger value="releases">Releases</TabsTrigger>
          <TabsTrigger value="schemas">Schema registry</TabsTrigger>
        </TabsList>

        <TabsContent value="releases">
          <div className="release-context-bar">
            <NamespacePicker
              namespaces={namespaces}
              value={ns}
              onChange={changeNamespace}
              disabled={Boolean(busyAction)}
            />
            <form className="filters filter-grow" onSubmit={applyNameFilter}>
              <div className="filter-grow">
                <Field label="Release name" error={nameTouched ? nameFilterError : null}>
                  <Input
                    className="font-mono"
                    value={nameDraft}
                    onChange={(event) => setNameDraft(event.target.value)}
                    onBlur={() => setNameTouched(true)}
                    placeholder="All release names"
                    disabled={!hasNS || Boolean(busyAction)}
                  />
                </Field>
              </div>
              <Button
                variant="outline"
                type="submit"
                disabled={
                  !hasNS || nameDraft.trim() === name || releasesLoading || Boolean(busyAction)
                }
              >
                Apply filter
              </Button>
            </form>
          </div>

          {name && currentNamedRelease ? (
            <div className="release-status-strip mb-16">
              <div>
                <span className="faint text-sm">Current release</span>
                <strong className="mono">{releaseKey(currentNamedRelease.release)}</strong>
              </div>
              <div>
                <span className="faint text-sm">Activation revision</span>
                <strong>{currentNamedRelease.activation_revision}</strong>
              </div>
              <div>
                <span className="faint text-sm">Previous</span>
                <strong className="mono">
                  {previousNamedRelease ? releaseKey(previousNamedRelease.release) : "—"}
                </strong>
              </div>
              <Button
                variant="outline"
                disabled={!previousNamedRelease || Boolean(busyAction)}
                onClick={() => {
                  if (previousNamedRelease) {
                    setPendingAction({
                      kind: "rollback",
                      name,
                      current: currentNamedRelease,
                      previous: previousNamedRelease,
                    });
                  }
                }}
              >
                Roll back to previous
              </Button>
            </div>
          ) : null}

          {!hasNS ? (
            <EmptyState
              icon={<Icon.namespace size={20} />}
              title="Choose an application and environment"
            >
              Release history and creation are scoped to one isolated environment.
            </EmptyState>
          ) : releasesLoading ? (
            <TableSkeleton
              headers={["Release", "State", "Schema", "Entries", "Digest", "Actions"]}
            />
          ) : releases.length === 0 ? (
            <EmptyState
              icon={<Icon.release size={20} />}
              title="No releases found"
              actions={<Button onClick={() => setBuilderOpen(true)}>New release</Button>}
            >
              {name
                ? "Clear the release-name filter or create its first version."
                : "Create the first immutable release for this environment."}
            </EmptyState>
          ) : (
            <div className="table-wrap card-table">
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
                    const release = summary.release;
                    return (
                      <tr key={releaseKey(release)}>
                        <td className="mono" data-label="Release">
                          {releaseKey(release)}
                        </td>
                        <td data-label="State">
                          {summary.current ? (
                            <Badge kind="success">
                              current · rev {summary.activation_revision}
                            </Badge>
                          ) : summary.previous ? (
                            <Badge kind="warning">previous</Badge>
                          ) : (
                            <Badge>inactive</Badge>
                          )}
                        </td>
                        <td className="mono" data-label="Schema">
                          {release.schema_id
                            ? `${release.schema_id}@${release.schema_version}`
                            : "none"}
                        </td>
                        <td data-label="Entries">{release.entries.length}</td>
                        <td className="mono" data-label="Digest">
                          {release.digest.slice(0, 16)}…
                        </td>
                        <td className="row-wrap row-actions" data-label="Actions">
                          <Button
                            variant="outline"
                            size="sm"
                            onClick={() => {
                              setActivationFailure(null);
                              setSelectedReleaseKey(releaseKey(release));
                            }}
                          >
                            View
                          </Button>
                          <Button
                            variant="outline"
                            size="sm"
                            disabled={Boolean(busyAction)}
                            onClick={() => void validate(release)}
                          >
                            {busyAction === `validate:${releaseKey(release)}` ? <Spinner /> : null}
                            Validate
                          </Button>
                          <Button
                            size="sm"
                            disabled={summary.current || Boolean(busyAction)}
                            onClick={() => setPendingAction({ kind: "activate", summary })}
                          >
                            Activate
                          </Button>
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
        </TabsContent>

        <TabsContent value="schemas">
          {activeTab === "schemas" ? <SchemaRegistry /> : null}
        </TabsContent>
      </Tabs>

      <ReleaseBuilder
        open={builderOpen}
        namespace={ns}
        onClose={() => setBuilderOpen(false)}
        onCreated={(release) => {
          setNameDraft(release.name);
          setName(release.name);
          loadedReleaseScope.current = "";
          replaceQuery({ name: release.name });
        }}
      />

      <ReleaseWorkspace
        summary={selectedSummary}
        releases={releases}
        busyAction={busyAction}
        activationFailure={activationFailure}
        onDismissFailure={() => setActivationFailure(null)}
        onClose={() => {
          setSelectedReleaseKey("");
          setActivationFailure(null);
        }}
        onValidate={(release) => void validate(release)}
        onActivate={(summary) => setPendingAction({ kind: "activate", summary })}
      />

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
              previous <span className="mono">{releaseKey(pendingAction.previous.release)}</span>?
              Subscribers will receive a new activation revision; no immutable version will be
              deleted.
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
