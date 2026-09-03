import { ArrowLeft, ChevronDown } from "lucide-react";
import { useRouter } from "next/router";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Ident } from "@/components/Ident";
import { Icon } from "@/components/icons";
import { JsonDiff } from "@/components/JsonDiff";
import { JsonEditor } from "@/components/JsonEditor";
import { JsonView, ValueView } from "@/components/JsonView";
import { ConfirmDialog, Modal } from "@/components/Modal";
import { ContentTypeSelect, ParameterValueInput } from "@/components/ParameterValueInput";
import {
  Badge,
  EmptyState,
  Field,
  KeyValue,
  PageHeader,
  PageTitle,
  Skeleton,
  Spinner,
  TableSkeleton,
} from "@/components/ui";
import { Button, ButtonLink } from "@/components/ui/button";
import { useToast } from "@/context/ToastContext";
import { ApiError, api, isAbortError, type ResourceRef } from "@/lib/api";
import { crumbs } from "@/lib/crumbs";
import {
  displayNamespace,
  displayPath,
  formatUnixMs,
  isEmptyJson,
  labelEntries,
  prettyJson,
} from "@/lib/format";
import { useFocusFirstInvalid } from "@/lib/forms";
import { useLatestRequest, useQueryParams } from "@/lib/hooks";
import {
  canonicalParameterValue,
  formatJson,
  jsonEquivalent,
  valuesEquivalent,
} from "@/lib/json-text";
import { links } from "@/lib/links";
import type { Parameter, ParameterMetadata } from "@/lib/types";
import { type ParameterSchema, useParameterSchema } from "@/lib/useParameterSchema";
import {
  firstError,
  validateMetadataJson,
  validateParameterValue,
  validateValueSize,
} from "@/lib/validation";

/** The fields of the new-version form that carry their own validation. */
type VersionField = "value" | "metadata";

/** One fetched version, for the viewer and the compare slot. */
interface LoadedVersion {
  version: number;
  value: string;
  contentType: string;
}

export default function ParameterDetailPage() {
  const router = useRouter();
  const toast = useToast();
  const { values, ready } = useQueryParams(["env", "app", "key"]);
  const env = values.env ?? "";
  const app = values.app ?? "";
  const key = values.key ?? "";
  const hasRef = !!env && !!app && !!key;
  const ref = useMemo<ResourceRef>(() => ({ env, app, key }), [env, app, key]);

  const [meta, setMeta] = useState<ParameterMetadata | null>(null);
  const [current, setCurrent] = useState<Parameter | null>(null);
  const [loadState, setLoadState] = useState<
    "idle" | "loading" | "success" | "not-found" | "error"
  >("idle");
  // A reload triggered by a save refreshes in place; only a first load (or a
  // change of ref) is allowed to blank the page.
  const [refreshing, setRefreshing] = useState(false);
  const request = useLatestRequest();
  // The version viewer loads independently of the page, so it gets its own token.
  const viewRequest = useLatestRequest();

  const [viewed, setViewed] = useState<LoadedVersion | null>(null);
  // A second version beside the viewed one turns the panel into a diff.
  const [compare, setCompare] = useState<LoadedVersion | null>(null);
  const [busyVersion, setBusyVersion] = useState<number | null>(null);
  const panelRef = useRef<HTMLDivElement | null>(null);

  const [newVersionOpen, setNewVersionOpen] = useState(false);
  const [value, setValue] = useState("");
  // The value the form opened with; a schema that arrives late may only take
  // over the editor while nothing has been typed yet.
  const [openedValue, setOpenedValue] = useState("");
  // Snapshot of the schema lookup taken when the form opened, so a late result
  // cannot flip an open editor between Form and JSON under the operator.
  const [versionSchema, setVersionSchema] = useState<ParameterSchema | null>(null);
  const [contentType, setContentType] = useState("string");
  const [metadataJson, setMetadataJson] = useState("{}");
  const [metadataOpen, setMetadataOpen] = useState(false);
  const [showChanges, setShowChanges] = useState(false);
  // The version whose value the form was prefilled from, when opened via Restore.
  const [restoredFrom, setRestoredFrom] = useState<number | null>(null);
  const [saving, setSaving] = useState(false);
  const [touched, setTouched] = useState<Partial<Record<VersionField, boolean>>>({});
  const [submitAttempted, setSubmitAttempted] = useState(false);
  const valueRef = useRef<HTMLElement | null>(null);
  const { formRef, requestFocus } = useFocusFirstInvalid();

  const [deleteOpen, setDeleteOpen] = useState(false);
  const [deleting, setDeleting] = useState(false);

  // The application's pinned schema, looked up in the background from the page
  // load so it is usually in hand before the form opens. Absence is silent.
  const schemaLookup = useParameterSchema({ env, app, key, enabled: ready && hasRef });
  useEffect(() => {
    if (!newVersionOpen || versionSchema !== null) return;
    if (schemaLookup.status === "idle" || schemaLookup.status === "loading") return;
    // "none" never changes the editor; a real schema only steps in while the
    // value is still untouched.
    if (schemaLookup.status === "none" || value === openedValue) setVersionSchema(schemaLookup);
  }, [newVersionOpen, versionSchema, schemaLookup, value, openedValue]);

  // Client-side mirrors of the server's validators (see lib/validation.ts).
  // The value is checked against the content type the form will actually send,
  // which starts as the parameter's own; the server still has the last word.
  // Memoised because a value may run to a megabyte.
  const valueError = useMemo(
    () => firstError(validateValueSize(value), validateParameterValue(value, contentType)),
    [value, contentType],
  );
  const metadataError = validateMetadataJson(metadataJson);
  const versionError = firstError(valueError, metadataError);

  // A message stays hidden until the user has left the field or tried to save,
  // so reopening the form on an existing value is never already an error.
  const shownIn = (field: VersionField, error: string | null) =>
    touched[field] || submitAttempted ? error : null;
  const shownValueError = shownIn("value", valueError);
  const shownMetadataError = shownIn("metadata", metadataError);
  const shownVersionError = firstError(shownValueError, shownMetadataError);
  // Saving identical content would only add a whitespace-for-whitespace version.
  const unchanged =
    current !== null &&
    contentType === (current.content_type || "string") &&
    valuesEquivalent(value, current.value, contentType) &&
    jsonEquivalent(metadataJson.trim() || "{}", meta?.metadata_json?.trim() || "{}");
  // Anything that would be lost by closing: an edit against the current
  // version, or — with no current version to compare to — any typed value.
  const dirty = current !== null ? !unchanged : value.trim() !== "" || !isEmptyJson(metadataJson);

  function markTouched(field: VersionField) {
    setTouched((t) => ({ ...t, [field]: true }));
  }

  const load = useCallback(
    async (options?: { background?: boolean }) => {
      if (!hasRef) return;
      const run = request.begin();
      const background = options?.background === true;
      if (background) setRefreshing(true);
      else {
        setLoadState("loading");
        setMeta(null);
        setCurrent(null);
      }
      try {
        const [m, cur] = await Promise.all([
          api.parameterMetadata(ref, { signal: run.signal }),
          api.getParameter(ref, undefined, undefined, { signal: run.signal }),
        ]);
        if (!run.current) return;
        setMeta(m);
        setCurrent(cur.parameter);
        setLoadState("success");
      } catch (err) {
        if (!run.current || isAbortError(err)) return;
        if (err instanceof ApiError && err.status === 404) {
          setLoadState("not-found");
        } else {
          // A failed background refresh keeps the data it already has; only a
          // foreground load has nothing to fall back to.
          if (!background) setLoadState("error");
          toast.error(err, "Failed to load parameter");
        }
      } finally {
        if (run.current) setRefreshing(false);
      }
    },
    [hasRef, ref, request, toast],
  );

  useEffect(() => {
    if (!ready) return;
    if (hasRef) {
      void load();
    } else {
      setLoadState("idle");
      setMeta(null);
      setCurrent(null);
    }
    return () => request.abort();
  }, [ready, hasRef, load, request]);

  // The viewer renders below the whole table; bring it into view when it
  // opens or changes, so a long history does not hide what was just asked for.
  const panelKey = viewed ? `${viewed.version}:${compare?.version ?? ""}` : null;
  useEffect(() => {
    if (panelKey === null) return;
    panelRef.current?.scrollIntoView?.({ block: "nearest", behavior: "smooth" });
  }, [panelKey]);

  function openNewVersion(prefill?: {
    value: string;
    contentType: string;
    metadataJson?: string;
    restoredFrom: number;
  }) {
    const type = prefill?.contentType ?? current?.content_type ?? meta?.content_type ?? "string";
    const raw = prefill?.value ?? current?.value ?? "";
    const opened = type === "json" ? (formatJson(raw) ?? raw) : raw;
    const metadata = prefill?.metadataJson ?? meta?.metadata_json;
    setValue(opened);
    setOpenedValue(opened);
    setVersionSchema(
      schemaLookup.status === "idle" || schemaLookup.status === "loading" ? null : schemaLookup,
    );
    setContentType(type);
    setMetadataJson(!isEmptyJson(metadata) ? prettyJson(metadata) : "{}");
    setMetadataOpen(!isEmptyJson(metadata));
    setShowChanges(false);
    setRestoredFrom(prefill?.restoredFrom ?? null);
    setTouched({});
    setSubmitAttempted(false);
    setNewVersionOpen(true);
  }

  async function saveVersion(e?: React.SyntheticEvent) {
    e?.preventDefault();
    if (!hasRef || saving) return;
    setSubmitAttempted(true);
    // Every remaining problem now has an inline message next to its field;
    // move focus there so the button never looks dead.
    if (versionError) {
      if (metadataError && !valueError) setMetadataOpen(true);
      requestFocus();
      return;
    }
    if (unchanged) return;
    setSaving(true);
    try {
      const res = await api.putParameter({
        env,
        app,
        key,
        value: canonicalParameterValue(value, contentType),
        content_type: contentType || "string",
        metadata_json: metadataJson.trim() || "{}",
      });
      toast.success(`Saved version ${res.version}`, displayPath(ref));
      setNewVersionOpen(false);
      setViewed(null);
      setCompare(null);
      await load({ background: true });
    } catch (err) {
      toast.error(err, "Failed to save version");
    } finally {
      setSaving(false);
    }
  }

  /** Fetches one version's value; null when the request was superseded or failed. */
  async function loadVersion(version: number): Promise<LoadedVersion | null> {
    if (!hasRef) return null;
    const run = viewRequest.begin();
    setBusyVersion(version);
    try {
      const res = await api.getParameter(ref, version, undefined, { signal: run.signal });
      if (!run.current) return null;
      return {
        version: res.parameter.version,
        value: res.parameter.value,
        contentType: res.parameter.content_type,
      };
    } catch (err) {
      if (!run.current || isAbortError(err)) return null;
      toast.error(err, "Failed to load version value");
      return null;
    } finally {
      if (run.current) setBusyVersion(null);
    }
  }

  async function viewVersion(version: number) {
    const loaded = await loadVersion(version);
    if (!loaded) return;
    setViewed(loaded);
    // A compare slot that now equals the viewed version has nothing to show.
    setCompare((slot) => (slot && slot.version === loaded.version ? null : slot));
  }

  async function compareVersion(version: number) {
    // The current value is already on the page; everything else is fetched.
    if (current && current.version === version) {
      setCompare({
        version: current.version,
        value: current.value,
        contentType: current.content_type,
      });
      return;
    }
    const loaded = await loadVersion(version);
    if (loaded) setCompare(loaded);
  }

  async function restoreVersion(version: number) {
    const loaded = await loadVersion(version);
    if (!loaded) return;
    const entry = meta?.versions.find((v) => v.version === version);
    openNewVersion({
      value: loaded.value,
      contentType: loaded.contentType,
      metadataJson: entry?.metadata_json,
      restoredFrom: version,
    });
  }

  async function onDelete() {
    if (!hasRef) return;
    setDeleting(true);
    try {
      await api.deleteParameter(ref);
      toast.success("Parameter deleted", displayPath(ref));
      // Closed before navigating, so a back-navigation cannot land on an open
      // confirmation for a parameter that no longer exists.
      setDeleteOpen(false);
      await router.push(links.parameters({ env, app }));
    } catch (err) {
      toast.error(err, "Failed to delete parameter");
    } finally {
      setDeleting(false);
    }
  }

  const backLink = hasRef ? links.parameters({ env, app }) : links.parameters();
  const trail = hasRef ? crumbs.parameter(ref) : undefined;

  // The heading and card frames are known from the URL alone, so they render
  // immediately and only the values fill in — no full-page spinner swap.
  if (!ready || (hasRef && (loadState === "idle" || loadState === "loading"))) {
    return (
      <>
        <PageHeader
          documentTitle={hasRef ? displayPath(ref) : "Parameter"}
          title={hasRef ? <span className="mono">{displayPath(ref)}</span> : "Parameter"}
          breadcrumbs={trail}
        />
        <div className="card">
          <div className="card-title">Current value</div>
          <Skeleton height={72} />
        </div>
        <div className="card">
          <div className="card-title">Metadata</div>
          <Skeleton height={96} />
        </div>
        <div className="card">
          <div className="card-title">Version history</div>
          <TableSkeleton headers={["Version", "State", "Created by", "Created"]} rows={3} />
        </div>
      </>
    );
  }
  if (!hasRef) {
    return (
      <>
        <PageTitle title="Parameter" />
        <EmptyState
          icon={<Icon.parameter size={20} />}
          title="No parameter specified"
          actions={
            <ButtonLink variant="outline" href={links.parameters()}>
              Browse parameters
            </ButtonLink>
          }
        >
          Provide ?env=, ?app=, and ?key= query parameters.
        </EmptyState>
      </>
    );
  }
  if (loadState === "not-found") {
    return (
      <>
        <PageHeader
          title="Parameter not found"
          breadcrumbs={trail}
          actions={
            <ButtonLink variant="outline" href={backLink}>
              <ArrowLeft size={16} aria-hidden /> Back to parameters
            </ButtonLink>
          }
        />
        <EmptyState icon={<Icon.parameter size={20} />} title="Not found">
          No parameter exists at <span className="mono">{displayPath(ref)}</span>.
        </EmptyState>
      </>
    );
  }
  if (loadState === "error" || !meta) {
    return (
      <>
        <PageHeader
          title="Could not load parameter"
          breadcrumbs={trail}
          actions={<Button onClick={() => void load()}>Try again</Button>}
        />
        <EmptyState icon={<Icon.parameter size={20} />} title="Parameter unavailable">
          The server could not load <span className="mono">{displayPath(ref)}</span>. Check the
          connection and try again.
        </EmptyState>
      </>
    );
  }

  // The diff reads oldest on the left.
  const diffPair =
    viewed && compare
      ? viewed.version < compare.version
        ? ([viewed, compare] as const)
        : ([compare, viewed] as const)
      : null;
  const nextVersion = Math.max(0, ...meta.versions.map((v) => v.version)) + 1;

  return (
    <>
      <PageHeader
        documentTitle={displayPath(ref)}
        title={
          <span className="row-wrap">
            <span className="mono">{displayPath(ref)}</span>
            {refreshing ? <Spinner /> : null}
          </span>
        }
        subtitle={displayNamespace(ref)}
        breadcrumbs={trail}
        actions={
          <>
            <Button onClick={() => openNewVersion()}>New version</Button>
            <Button variant="destructive" onClick={() => setDeleteOpen(true)}>
              Delete
            </Button>
          </>
        }
      />

      <div className="card">
        <div className="card-title">
          Current value
          {current ? <Badge kind="accent">v{current.version}</Badge> : null}
        </div>
        {current ? (
          <ValueView
            value={current.value}
            contentType={current.content_type}
            copyLabel="Copy value"
            tools={<span className="faint text-sm mono">{current.content_type || "value"}</span>}
          />
        ) : (
          <span className="faint">No current value.</span>
        )}
      </div>

      <div className="card">
        <div className="card-title">Metadata</div>
        <KeyValue
          rows={[
            [
              "Namespace",
              <span className="mono" key="ns">
                {displayNamespace(ref)}
              </span>,
            ],
            [
              "Key",
              <span className="mono" key="key">
                {key}
              </span>,
            ],
            ["Content type", meta.content_type || "—"],
            ["Created", formatUnixMs(meta.created_at_unix_ms)],
            ["Updated", formatUnixMs(meta.updated_at_unix_ms)],
            [
              "Labels",
              labelEntries(meta.labels).length ? (
                <div className="row-wrap" key="labels">
                  {labelEntries(meta.labels).map(([k, v]) => (
                    <Badge key={k} kind="accent">
                      {k}: v{v}
                    </Badge>
                  ))}
                </div>
              ) : (
                "—"
              ),
            ],
          ]}
        />
        {!isEmptyJson(meta.metadata_json) ? (
          <div className="mt-4">
            <div className="field-label">Metadata JSON</div>
            <JsonView raw={prettyJson(meta.metadata_json)} copyLabel="Copy metadata" />
          </div>
        ) : null}
      </div>

      <div className="card">
        <div className="card-title">Version history</div>
        {meta.versions.length === 0 ? (
          <EmptyState icon={<Icon.parameter size={20} />} title="No versions" />
        ) : (
          <div className="table-wrap">
            <table className="data">
              <thead>
                <tr>
                  <th>Version</th>
                  <th>State</th>
                  <th>Created by</th>
                  <th>Created</th>
                  <th />
                </tr>
              </thead>
              <tbody>
                {[...meta.versions]
                  .sort((a, b) => b.version - a.version)
                  .map((v) => {
                    const isCurrent = current?.version === v.version;
                    const isViewed = viewed?.version === v.version;
                    const isCompared = compare?.version === v.version;
                    const busy = busyVersion === v.version;
                    return (
                      <tr key={v.version} aria-current={isViewed ? "true" : undefined}>
                        <td>
                          <div className="row-wrap">
                            v{v.version}
                            {isCurrent ? <Badge kind="accent">current</Badge> : null}
                            {isViewed ? <Badge kind="neutral">viewing</Badge> : null}
                            {isCompared ? <Badge kind="neutral">comparing</Badge> : null}
                          </div>
                        </td>
                        <td>
                          <Badge kind={v.state === "enabled" ? "success" : "neutral"}>
                            {v.state}
                          </Badge>
                        </td>
                        <td>{v.created_by || <span className="faint">—</span>}</td>
                        <td className="nowrap">{formatUnixMs(v.created_at_unix_ms)}</td>
                        <td>
                          <div className="row-actions">
                            <Button
                              variant="outline"
                              size="sm"
                              loading={busy}
                              disabled={busyVersion !== null || isViewed}
                              onClick={() => void viewVersion(v.version)}
                            >
                              View value
                            </Button>
                            {viewed && !isViewed ? (
                              <Button
                                variant="outline"
                                size="sm"
                                aria-label={`Compare v${v.version} with v${viewed.version}`}
                                disabled={busyVersion !== null || isCompared}
                                onClick={() => void compareVersion(v.version)}
                              >
                                Compare
                              </Button>
                            ) : null}
                            {!isCurrent ? (
                              <Button
                                variant="ghost"
                                size="sm"
                                aria-label={`Restore v${v.version}`}
                                disabled={busyVersion !== null}
                                onClick={() => void restoreVersion(v.version)}
                              >
                                Restore
                              </Button>
                            ) : null}
                          </div>
                        </td>
                      </tr>
                    );
                  })}
              </tbody>
            </table>
          </div>
        )}

        {viewed ? (
          <div className="version-panel" ref={panelRef} data-testid="version-panel">
            <div className="version-panel-head">
              <div className="version-panel-title">
                {diffPair ? (
                  <>
                    <Badge kind="accent">v{diffPair[0].version}</Badge>
                    <span className="version-arrow" aria-hidden>
                      →
                    </span>
                    <Badge kind="accent">v{diffPair[1].version}</Badge>
                    <span className="faint text-sm">changes</span>
                  </>
                ) : (
                  <>
                    <Badge kind="accent">v{viewed.version}</Badge>
                    <span className="faint text-sm">{viewed.contentType || "value"}</span>
                  </>
                )}
              </div>
              <div className="version-panel-actions">
                {!diffPair && current && current.version !== viewed.version ? (
                  <Button
                    variant="outline"
                    size="sm"
                    onClick={() => void compareVersion(current.version)}
                  >
                    Compare with current
                  </Button>
                ) : null}
                {current && current.version !== viewed.version ? (
                  <Button
                    variant="outline"
                    size="sm"
                    disabled={busyVersion !== null}
                    onClick={() => void restoreVersion(viewed.version)}
                  >
                    Restore v{viewed.version}
                  </Button>
                ) : null}
                {diffPair ? (
                  <Button variant="outline" size="sm" onClick={() => setCompare(null)}>
                    Close compare
                  </Button>
                ) : null}
                <Button
                  variant="outline"
                  size="sm"
                  onClick={() => {
                    setViewed(null);
                    setCompare(null);
                  }}
                >
                  Close
                </Button>
              </div>
            </div>
            {diffPair ? (
              <JsonDiff
                before={diffPair[0].value}
                after={diffPair[1].value}
                beforeLabel={`v${diffPair[0].version}`}
                afterLabel={`v${diffPair[1].version}`}
                contentType={
                  diffPair[0].contentType === "json" && diffPair[1].contentType === "json"
                    ? "json"
                    : diffPair[1].contentType
                }
              />
            ) : (
              <ValueView value={viewed.value} contentType={viewed.contentType} />
            )}
          </div>
        ) : null}
      </div>

      <Modal
        open={newVersionOpen}
        title="New parameter version"
        description={`Saving creates v${nextVersion} and makes it current.`}
        onClose={() => setNewVersionOpen(false)}
        dismissible={!saving}
        dirty={dirty}
        initialFocus={valueRef}
        wide
        footer={(close) => (
          <>
            {unchanged && current ? (
              <p className="footer-note" role="status" data-testid="version-unchanged">
                Nothing changed since v{current.version}.
              </p>
            ) : null}
            <Button variant="outline" onClick={close} disabled={saving}>
              Cancel
            </Button>
            <Button
              onClick={saveVersion}
              loading={saving}
              disabled={shownVersionError !== null || unchanged}
            >
              Save new version
            </Button>
          </>
        )}
      >
        <form ref={formRef} onSubmit={saveVersion}>
          {restoredFrom !== null ? (
            <div className="info-panel mb-4 value-restore-note" data-testid="restore-note">
              <span>
                Prefilled from <span className="mono">v{restoredFrom}</span>. Saving creates a new
                version with this value; v{restoredFrom} itself is untouched.
              </span>
            </div>
          ) : null}
          <Field label="Content type" className="value-type-field">
            <ContentTypeSelect
              value={contentType}
              currentValue={value}
              onValueChange={setContentType}
              onClearValue={() => setValue("")}
            />
          </Field>
          <Field label="Value" error={shownValueError}>
            <ParameterValueInput
              contentType={contentType}
              value={value}
              inputRef={valueRef}
              schema={contentType === "json" ? (versionSchema?.schema ?? null) : null}
              schemaLabel={
                versionSchema?.status === "ready" ? (
                  <>
                    <Ident
                      kind="schema"
                      value={`${versionSchema.application}/${versionSchema.releaseName}@${versionSchema.schemaVersion}`}
                    />
                    <Ident kind="alias" value={versionSchema.alias} />
                  </>
                ) : undefined
              }
              rows={12}
              onChange={setValue}
              onBlur={() => markTouched("value")}
              onSubmit={() => void saveVersion()}
            />
          </Field>
          {contentType === "json" && versionSchema === null ? (
            <p className="faint text-xs mt-1 row-wrap" role="status">
              {schemaLookup.status === "ready" ? (
                <>
                  <span>
                    A schema for <span className="mono">{schemaLookup.alias}</span> is available.
                  </span>
                  <Button
                    type="button"
                    variant="link"
                    size="xs"
                    onClick={() => setVersionSchema(schemaLookup)}
                  >
                    Use schema form
                  </Button>
                </>
              ) : (
                <>
                  <Spinner /> Looking for a schema…
                </>
              )}
            </p>
          ) : null}
          {current && !unchanged ? (
            <div className="value-disclosure">
              <button
                type="button"
                className="value-disclosure-toggle"
                aria-expanded={showChanges}
                aria-controls="version-changes"
                onClick={() => setShowChanges((open) => !open)}
              >
                <ChevronDown size={14} aria-hidden />
                {showChanges ? "Hide changes" : "Show changes"} since v{current.version}
              </button>
              {showChanges ? (
                <div id="version-changes" className="value-disclosure-body">
                  <JsonDiff
                    before={current.value}
                    after={value}
                    beforeLabel={`v${current.version}`}
                    afterLabel="This version"
                    contentType={
                      contentType === "json" && current.content_type === "json"
                        ? "json"
                        : contentType
                    }
                    maxHeight="40vh"
                  />
                </div>
              ) : null}
            </div>
          ) : null}
          <div className="value-disclosure">
            <button
              type="button"
              className="value-disclosure-toggle"
              aria-expanded={metadataOpen || shownMetadataError !== null}
              aria-controls="version-metadata"
              onClick={() => setMetadataOpen((open) => !open)}
            >
              <ChevronDown size={14} aria-hidden />
              Metadata JSON
              {!metadataOpen && !isEmptyJson(metadataJson) ? (
                <span className="faint">(set)</span>
              ) : null}
            </button>
            {metadataOpen || shownMetadataError !== null ? (
              <div id="version-metadata" className="value-disclosure-body">
                <Field label="Metadata JSON" error={shownMetadataError}>
                  <JsonEditor
                    toolbar="minimal"
                    rows={3}
                    maxHeight="30vh"
                    value={metadataJson}
                    onChange={setMetadataJson}
                    onBlur={() => markTouched("metadata")}
                    onSubmit={() => void saveVersion()}
                  />
                </Field>
              </div>
            ) : null}
          </div>
        </form>
      </Modal>

      <ConfirmDialog
        open={deleteOpen}
        title="Delete parameter?"
        danger
        message={
          <>
            Delete <span className="mono">{displayPath(ref)}</span> and all its versions?
          </>
        }
        confirmLabel="Delete parameter"
        busy={deleting}
        onConfirm={onDelete}
        onCancel={() => setDeleteOpen(false)}
      />
    </>
  );
}
