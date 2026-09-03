import { Plus, RefreshCw } from "lucide-react";
import { useRouter } from "next/router";
import { type FormEvent, useCallback, useEffect, useRef, useState } from "react";
import { Ident, ReleaseIdent } from "@/components/Ident";
import { Icon } from "@/components/icons";
import { ConfirmDialog } from "@/components/Modal";
import NamespacePicker, { type NamespaceSelection } from "@/components/NamespacePicker";
import { ReleaseBuilder } from "@/components/releases/ReleaseBuilder";
import { type ActivationFailure, ReleaseWorkspace } from "@/components/releases/ReleaseWorkspace";
import { SchemaRegistry } from "@/components/releases/SchemaRegistry";
import { parseReleaseKey, releaseKey } from "@/components/releases/utils";
import { entryHrefResolver } from "@/components/releases/ViolationTable";
import RollbackDialog from "@/components/ship/RollbackDialog";
import { headerLabels, SortHeaderRow, useSort } from "@/components/SortableTable";
import {
  Badge,
  Button,
  EmptyState,
  Field,
  Input,
  PageHeader,
  Pagination,
  TableSkeleton,
  TableSummary,
} from "@/components/ui";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { useToast } from "@/context/ToastContext";
import { ApiError, api, isAbortError } from "@/lib/api";
import { crumbs } from "@/lib/crumbs";
import { useCursorPagination, useNamespaces } from "@/lib/hooks";
import { links } from "@/lib/links";
import { isProductionEnvironment } from "@/lib/readiness";
import type { SortColumn } from "@/lib/sort";
import type {
  ConfigurationRelease,
  OverviewActiveRelease,
  ReleaseSummary,
  ReleaseValidationError,
} from "@/lib/types";
import { queryValue, useQueryReplace } from "@/lib/url";
import { validateReleaseName } from "@/lib/validation";

const NO_NS: NamespaceSelection = { env: "", app: "" };

// Module scope so the sort controller's memos stay stable across renders. The
// release column sorts by name then version — numeric collation puts v2 before
// v10 — and the state column by how live the release is.
const COLUMNS: ReadonlyArray<SortColumn<ReleaseSummary>> = [
  { id: "release", label: "Release", value: (s) => `${s.release.name} ${s.release.version}` },
  { id: "state", label: "State", value: (s) => (s.current ? 0 : s.previous ? 1 : 2) },
  {
    id: "schema",
    label: "Schema",
    value: (s) =>
      s.release.schema_version
        ? `${s.release.namespace.app}/${s.release.name}@${s.release.schema_version}`
        : null,
  },
  { id: "entries", label: "Entries", value: (s) => s.release.entries.length },
  { id: "digest", label: "Digest", value: (s) => s.release.digest },
  { id: "actions", label: "Actions" },
];

const PAGE_SORT_HINT = "Sorts the releases loaded on this page, not the whole history.";

type PendingReleaseAction =
  | { kind: "activate"; summary: ReleaseSummary }
  | { kind: "rollback"; name: string; current: ReleaseSummary; previous: ReleaseSummary };

type BusyReleaseAction = "" | "activate" | `validate:${string}`;

function activationViolations(error: unknown): ReleaseValidationError[] | null {
  if (!(error instanceof ApiError) || error.code !== "failed_precondition") return null;
  return error.validationErrors.length > 0 ? error.validationErrors : null;
}

/** The RollbackDialog's view of the active release, from two list summaries. */
function activeFromSummaries(
  current: ReleaseSummary,
  previous: ReleaseSummary | null,
): OverviewActiveRelease {
  const release = current.release;
  return {
    name: release.name,
    version: release.version,
    activation_revision: current.activation_revision,
    previous_version: previous?.release.version ?? 0,
    created_by: release.created_by,
    created_at_unix_ms: release.created_at_unix_ms,
    is_rolled_back: previous ? previous.release.version > release.version : false,
    schema_version: release.schema_version,
    digest: release.digest,
    entries: release.entries,
  };
}

export default function ReleasesPage() {
  const router = useRouter();
  const toast = useToast();
  const { namespaces, loading: namespacesLoading, error: namespaceError } = useNamespaces();
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
  // A deep-linked release that is not in the loaded page, fetched on its own.
  const [linkedSummary, setLinkedSummary] = useState<ReleaseSummary | null>(null);
  const [deepLink, setDeepLink] = useState<{ name: string; version: number } | null>(null);
  const [pendingAction, setPendingAction] = useState<PendingReleaseAction | null>(null);
  const [activationFailure, setActivationFailure] = useState<ActivationFailure | null>(null);
  // The request scope whose response is currently on screen; gates the empty
  // state so a deep link never flashes "No releases found" before the first
  // load has even started.
  const [loadedScope, setLoadedScope] = useState("");
  const [seeded, setSeeded] = useState(false);
  const refreshGeneration = useRef(0);
  const loadedReleaseScope = useRef("");
  const refreshController = useRef<AbortController | null>(null);
  const linkRun = useRef(0);
  const replaceQuery = useQueryReplace("/releases");
  const sort = useSort<ReleaseSummary>("/releases", COLUMNS);

  const queryTab = queryValue(router.query.tab);
  const queryApp = queryValue(router.query.app);
  const queryEnv = queryValue(router.query.env);
  const queryName = queryValue(router.query.name);
  const queryRelease = queryValue(router.query.release);

  // Seed from the URL exactly once. Every later change flows state → URL
  // through replaceQuery; re-reading the query here would clobber whatever the
  // user has typed into the name filter since the last Apply.
  useEffect(() => {
    if (!router.isReady || seeded) return;
    setSeeded(true);
    setActiveTab(queryTab === "schemas" ? "schemas" : "releases");
    setNS({ app: queryApp, env: queryEnv });
    setNameDraft(queryName);
    setName(queryName);
    const linked = queryRelease ? parseReleaseKey(queryRelease) : null;
    if (linked && queryApp && queryEnv) {
      setDeepLink(linked);
      setSelectedReleaseKey(`${linked.name}@${linked.version}`);
    }
  }, [queryApp, queryEnv, queryName, queryRelease, queryTab, router.isReady, seeded]);

  function changeTab(value: string | number) {
    const next = value === "schemas" ? "schemas" : "releases";
    setActiveTab(next);
    replaceQuery({ tab: next === "schemas" ? "schemas" : "" });
  }

  function openWorkspace(key: string) {
    setActivationFailure(null);
    setSelectedReleaseKey(key);
    replaceQuery({ release: key });
  }

  function closeWorkspace() {
    setSelectedReleaseKey("");
    setLinkedSummary(null);
    setActivationFailure(null);
    replaceQuery({ release: "" });
  }

  function changeNamespace(next: NamespaceSelection) {
    setActivationFailure(null);
    setPendingAction(null);
    setSelectedReleaseKey("");
    setLinkedSummary(null);
    setDeepLink(null);
    linkRun.current += 1;
    setNS(next);
    setNameDraft("");
    setName("");
    loadedReleaseScope.current = "";
    replaceQuery({ app: next.app, env: next.env, name: "", release: "" });
  }

  const hasNS = Boolean(ns.env && ns.app);
  const releaseScope = hasNS ? JSON.stringify([ns.env, ns.app, name]) : "";
  const releasePaging = useCursorPagination(releaseScope);
  const releaseRequestScope = JSON.stringify([releaseScope, releasePaging.pageToken]);
  const settled = loadedScope === releaseRequestScope;
  const nameFilterError = validateReleaseName(nameDraft.trim());

  useEffect(() => {
    if (namespaceError) toast.error(namespaceError, "Failed to load environments");
  }, [namespaceError, toast]);

  const refresh = useCallback(
    async (force = false) => {
      refreshController.current?.abort();
      const generation = ++refreshGeneration.current;
      if (!hasNS) {
        refreshController.current = null;
        loadedReleaseScope.current = "";
        setReleases([]);
        releasePaging.setNextToken("");
        setReleasesLoading(false);
        return;
      }
      // Keep the loaded list across a visit to the Schemas tab; it is
      // reloaded only if its scope changed in the meantime.
      if (activeTab !== "releases") {
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
        setLoadedScope(releaseRequestScope);
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

  // Resolve the `?release=` deep link once the list has loaded: use the loaded
  // summary when the page has it, else fetch the release and the active state
  // for its name and synthesise one. The run counter (not an AbortController
  // in the cleanup) guards the result: the effect legitimately re-runs on
  // every render because `replaceQuery` follows the router object.
  useEffect(() => {
    if (!deepLink || !settled || !hasNS) return;
    const wanted = `${deepLink.name}@${deepLink.version}`;
    setDeepLink(null);
    if (releases.some((summary) => releaseKey(summary.release) === wanted)) return;
    const run = ++linkRun.current;
    void (async () => {
      try {
        const [{ release }, active] = await Promise.all([
          api.getRelease(ns, deepLink.name, deepLink.version),
          api.getActiveRelease(ns, deepLink.name).catch((error: unknown) => {
            if (error instanceof ApiError && error.code === "not_found") return null;
            throw error;
          }),
        ]);
        if (run !== linkRun.current) return;
        const current = active?.release.version === release.version;
        setLinkedSummary({
          release,
          current,
          previous: !current && active?.previous_version === release.version,
          activation_revision: current ? (active?.activation_revision ?? 0) : 0,
        });
      } catch (error) {
        if (run !== linkRun.current || isAbortError(error)) return;
        setSelectedReleaseKey("");
        replaceQuery({ release: "" });
        toast.error(error, `Could not open ${wanted}`);
      }
    })();
  }, [deepLink, settled, hasNS, releases, ns, replaceQuery, toast]);

  // Drop a deep-link fetch that lands after unmount.
  useEffect(
    () => () => {
      linkRun.current += 1;
    },
    [],
  );

  function applyNameFilter(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setNameTouched(true);
    if (nameFilterError) return;
    const next = nameDraft.trim();
    setActivationFailure(null);
    setSelectedReleaseKey("");
    setLinkedSummary(null);
    setName(next);
    loadedReleaseScope.current = "";
    replaceQuery({ name: next, release: "" });
  }

  async function validate(release: ConfigurationRelease) {
    if (busyAction) return;
    const target = releaseKey(release);
    setBusyAction(`validate:${target}`);
    try {
      const result = await api.validateRelease(release.namespace, release.name, release.version);
      if (result.valid) {
        toast.success(`${target} is valid`);
        if (activationFailure?.operation === "Validation" && activationFailure.target === target) {
          setActivationFailure(null);
        }
      } else if (result.errors.length > 0) {
        showViolations({ operation: "Validation", target, violations: result.errors });
      } else {
        toast.error(new Error("The release did not validate."), "Validation failed");
      }
    } catch (error) {
      const violations = activationViolations(error);
      if (violations) showViolations({ operation: "Validation", target, violations });
      else toast.error(error, "Validation failed");
    } finally {
      setBusyAction("");
    }
  }

  // Violations are rendered inside the workspace, so open it on the release
  // they belong to; the table is far more readable than a joined toast string.
  function showViolations(failure: ActivationFailure) {
    setSelectedReleaseKey(failure.target);
    replaceQuery({ release: failure.target });
    setActivationFailure(failure);
  }

  async function performActivation() {
    if (pendingAction?.kind !== "activate" || busyAction) return;
    const action = pendingAction;
    setBusyAction("activate");
    setActivationFailure(null);
    const releaseName = action.summary.release.name;
    const target = releaseKey(action.summary.release);
    try {
      const active = await api.getActiveRelease(ns, releaseName).catch((error: unknown) => {
        if (error instanceof ApiError && error.code === "not_found") return null;
        throw error;
      });
      const expectedVersion = active?.release.version ?? 0;
      await api.activateRelease(ns, releaseName, action.summary.release.version, expectedVersion);
      toast.success(`Activated ${target}`);
      setPendingAction(null);
      await refresh(true);
    } catch (error) {
      const violations = activationViolations(error);
      if (violations) {
        showViolations({ operation: "Activation", target, violations });
        setPendingAction(null);
      } else {
        toast.error(error, "Activation failed");
      }
    } finally {
      setBusyAction("");
    }
  }

  const selectedSummary =
    releases.find((summary) => releaseKey(summary.release) === selectedReleaseKey) ??
    (linkedSummary && releaseKey(linkedSummary.release) === selectedReleaseKey
      ? linkedSummary
      : null);
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
  const rollbackAction = pendingAction?.kind === "rollback" ? pendingAction : null;

  return (
    <>
      <PageHeader
        title="Configuration releases"
        subtitle="Build, validate, activate, and inspect immutable configuration manifests."
        breadcrumbs={hasNS ? crumbs.environment({ env: ns.env, app: ns.app }) : undefined}
        actions={
          activeTab === "releases" ? (
            <>
              <Button
                variant="outline"
                disabled={!hasNS || Boolean(busyAction)}
                loading={releasesLoading}
                onClick={() => void refresh(true)}
              >
                {releasesLoading ? null : <RefreshCw size={16} aria-hidden />}
                Refresh
              </Button>
              <Button disabled={!hasNS || Boolean(busyAction)} onClick={() => setBuilderOpen(true)}>
                <Plus size={16} aria-hidden />
                New release
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
              loading={namespacesLoading}
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
            <div className="release-status-strip mb-4">
              <div>
                <span className="faint text-sm">Current release</span>
                <ReleaseIdent
                  name={currentNamedRelease.release.name}
                  version={currentNamedRelease.release.version}
                />
              </div>
              <div>
                <span className="faint text-sm">Activation revision</span>
                <Ident kind="revision" value={String(currentNamedRelease.activation_revision)} />
              </div>
              <div>
                <span className="faint text-sm">Previous</span>
                {previousNamedRelease ? (
                  <ReleaseIdent
                    name={previousNamedRelease.release.name}
                    version={previousNamedRelease.release.version}
                  />
                ) : (
                  <strong className="mono">—</strong>
                )}
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

          {seeded && !hasNS ? (
            <EmptyState
              icon={<Icon.namespace size={20} />}
              title="Choose an application and environment"
            >
              Release history and creation are scoped to one isolated environment.
            </EmptyState>
          ) : !seeded || !settled || releasesLoading ? (
            <TableSkeleton headers={headerLabels(COLUMNS)} />
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
                <TableSummary
                  shown={releases.length}
                  noun="releases"
                  filters={name ? 1 : 0}
                  hint={sort.sort ? PAGE_SORT_HINT : undefined}
                />
                <thead>
                  <SortHeaderRow controller={sort} hint={PAGE_SORT_HINT} />
                </thead>
                <tbody>
                  {sort.apply(releases).map((summary) => {
                    const release = summary.release;
                    return (
                      <tr key={releaseKey(release)}>
                        <td data-label="Release">
                          <ReleaseIdent name={release.name} version={release.version} />
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
                        <td data-label="Schema">
                          {release.schema_version ? (
                            <Ident
                              kind="schema"
                              value={`${release.namespace.app}/${release.name}@${release.schema_version}`}
                            />
                          ) : (
                            <span className="faint">none</span>
                          )}
                        </td>
                        <td data-label="Entries">{release.entries.length}</td>
                        <td className="mono" data-label="Digest">
                          {release.digest.slice(0, 16)}…
                        </td>
                        <td data-label="Actions">
                          <div className="row-wrap row-actions">
                            <Button
                              variant="outline"
                              size="sm"
                              onClick={() => openWorkspace(releaseKey(release))}
                            >
                              View
                            </Button>
                            <Button
                              variant="outline"
                              size="sm"
                              disabled={Boolean(busyAction)}
                              loading={busyAction === `validate:${releaseKey(release)}`}
                              onClick={() => void validate(release)}
                            >
                              Validate
                            </Button>
                            <Button
                              size="sm"
                              disabled={summary.current || Boolean(busyAction)}
                              onClick={() => setPendingAction({ kind: "activate", summary })}
                            >
                              Activate
                            </Button>
                          </div>
                        </td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>
          )}

          {hasNS && settled && !releasesLoading ? (
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
          // Same name as the active filter → no state changes, so the load
          // effect would not re-run on its own.
          if (release.name === name) void refresh(true);
        }}
      />

      <ReleaseWorkspace
        summary={selectedSummary}
        releases={releases}
        busyAction={busyAction}
        activationFailure={activationFailure}
        onDismissFailure={() => setActivationFailure(null)}
        resolveHref={
          selectedSummary
            ? entryHrefResolver(
                selectedSummary.release.entries,
                selectedSummary.release.namespace,
                links,
              )
            : undefined
        }
        onClose={closeWorkspace}
        onValidate={(release) => void validate(release)}
        onActivate={(summary) => setPendingAction({ kind: "activate", summary })}
        onRollback={(current, previous) =>
          setPendingAction({ kind: "rollback", name: current.release.name, current, previous })
        }
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
        danger={isProductionEnvironment(ns.env)}
        requireText={isProductionEnvironment(ns.env) ? ns.env : undefined}
        busy={busyAction === "activate"}
        onConfirm={() => void performActivation()}
        onCancel={() => setPendingAction(null)}
      />

      {rollbackAction ? (
        <RollbackDialog
          namespace={ns}
          name={rollbackAction.name}
          active={activeFromSummaries(rollbackAction.current, rollbackAction.previous)}
          open
          onClose={() => setPendingAction(null)}
          onDone={(result) => {
            setPendingAction(null);
            if (result.changed) {
              toast.success(
                `Rolled back ${rollbackAction.name} to version ${result.release.version}`,
              );
            } else {
              toast.info(`${releaseKey(result.release)} was already active`);
            }
            void refresh(true);
          }}
        />
      ) : null}
    </>
  );
}
