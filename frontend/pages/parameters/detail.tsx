import { ArrowLeft } from "lucide-react";
import Link from "next/link";
import { useRouter } from "next/router";
import { useCallback, useEffect, useMemo, useState } from "react";
import CopyButton from "@/components/CopyButton";
import { Icon } from "@/components/icons";
import { ConfirmDialog, Modal } from "@/components/Modal";
import {
  Badge,
  EmptyState,
  Field,
  Input,
  JsonView,
  KeyValue,
  PageHeader,
  PageTitle,
  Skeleton,
  Spinner,
  TableSkeleton,
  Textarea,
} from "@/components/ui";
import { AppSelect } from "@/components/ui/app-select";
import { Button, ButtonLink } from "@/components/ui/button";
import { useToast } from "@/context/ToastContext";
import { ApiError, api, isAbortError, type ResourceRef } from "@/lib/api";
import {
  displayNamespace,
  displayPath,
  formatUnixMs,
  isEmptyJson,
  labelEntries,
  prettyJson,
} from "@/lib/format";
import { useLatestRequest, useQueryParams } from "@/lib/hooks";
import { links } from "@/lib/links";
import { PARAMETER_CONTENT_TYPES, type Parameter, type ParameterMetadata } from "@/lib/types";
import {
  firstError,
  validateMetadataJson,
  validateParameterValue,
  validateValueSize,
} from "@/lib/validation";

/** The fields of the new-version form that carry their own validation. */
type VersionField = "value" | "metadata";

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

  const [viewed, setViewed] = useState<{
    version: number;
    value: string;
    contentType: string;
  } | null>(null);
  const [viewingVersion, setViewingVersion] = useState<number | null>(null);

  const [newVersionOpen, setNewVersionOpen] = useState(false);
  const [value, setValue] = useState("");
  const [contentType, setContentType] = useState("string");
  const [metadataJson, setMetadataJson] = useState("{}");
  const [saving, setSaving] = useState(false);
  const [touched, setTouched] = useState<Partial<Record<VersionField, boolean>>>({});
  const [submitAttempted, setSubmitAttempted] = useState(false);

  const [deleteOpen, setDeleteOpen] = useState(false);
  const [deleting, setDeleting] = useState(false);

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

  function openNewVersion() {
    setValue(current?.value ?? "");
    setContentType(current?.content_type ?? meta?.content_type ?? "string");
    setMetadataJson(!isEmptyJson(meta?.metadata_json) ? prettyJson(meta?.metadata_json) : "{}");
    setTouched({});
    setSubmitAttempted(false);
    setNewVersionOpen(true);
  }

  async function saveVersion(e: React.FormEvent) {
    e.preventDefault();
    if (!hasRef) return;
    setSubmitAttempted(true);
    // Every remaining problem now has an inline message next to its field.
    if (versionError) return;
    setSaving(true);
    try {
      const res = await api.putParameter({
        env,
        app,
        key,
        value,
        content_type: contentType || "string",
        metadata_json: metadataJson.trim() || "{}",
      });
      toast.success(`Saved version ${res.version}`, displayPath(ref));
      setNewVersionOpen(false);
      setViewed(null);
      await load({ background: true });
    } catch (err) {
      toast.error(err, "Failed to save version");
    } finally {
      setSaving(false);
    }
  }

  async function viewVersion(version: number) {
    if (!hasRef) return;
    const run = viewRequest.begin();
    setViewingVersion(version);
    try {
      const res = await api.getParameter(ref, version, undefined, { signal: run.signal });
      if (!run.current) return;
      setViewed({
        version: res.parameter.version,
        value: res.parameter.value,
        contentType: res.parameter.content_type,
      });
    } catch (err) {
      if (!run.current || isAbortError(err)) return;
      toast.error(err, "Failed to load version value");
    } finally {
      if (run.current) setViewingVersion(null);
    }
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

  // The heading and card frames are known from the URL alone, so they render
  // immediately and only the values fill in — no full-page spinner swap.
  if (!ready || (hasRef && (loadState === "idle" || loadState === "loading"))) {
    return (
      <>
        <PageHeader
          documentTitle={hasRef ? displayPath(ref) : "Parameter"}
          title={hasRef ? <span className="mono">{displayPath(ref)}</span> : "Parameter"}
          subtitle={
            <Link href={backLink} className="text-sm">
              <ArrowLeft size={14} aria-hidden /> {hasRef ? displayNamespace(ref) : "Parameters"}
            </Link>
          }
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
          actions={<Button onClick={() => void load()}>Try again</Button>}
        />
        <EmptyState icon={<Icon.parameter size={20} />} title="Parameter unavailable">
          The server could not load <span className="mono">{displayPath(ref)}</span>. Check the
          connection and try again.
        </EmptyState>
      </>
    );
  }

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
        subtitle={
          <Link href={backLink} className="text-sm">
            <ArrowLeft size={14} aria-hidden /> {displayNamespace(ref)}
          </Link>
        }
        actions={
          <>
            <Button onClick={openNewVersion}>New version</Button>
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
          <>
            <div className="between mb-8">
              <span className="faint text-sm">{current.content_type || "value"}</span>
              <CopyButton label="Copy value" value={current.value} />
            </div>
            <pre className="json-block">
              {current.content_type === "json" ? prettyJson(current.value) : current.value}
            </pre>
          </>
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
          <div className="mt-16">
            <div className="field-label">Metadata JSON</div>
            <JsonView raw={prettyJson(meta.metadata_json)} />
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
                  .map((v) => (
                    <tr
                      key={v.version}
                      aria-current={viewed?.version === v.version ? "true" : undefined}
                    >
                      <td>
                        <div className="row-wrap">
                          v{v.version}
                          {viewed?.version === v.version ? (
                            <Badge kind="accent">viewing</Badge>
                          ) : null}
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
                            disabled={viewingVersion === v.version}
                            onClick={() => viewVersion(v.version)}
                          >
                            {viewingVersion === v.version ? <Spinner /> : null}
                            View value
                          </Button>
                        </div>
                      </td>
                    </tr>
                  ))}
              </tbody>
            </table>
          </div>
        )}

        {viewed ? (
          <div className="mt-16">
            <div className="between mb-8">
              <div className="row-wrap">
                <Badge kind="accent">v{viewed.version}</Badge>
                <span className="faint text-sm">{viewed.contentType || "value"}</span>
              </div>
              <div className="row-wrap">
                <CopyButton label="Copy" value={viewed.value} />
                <Button variant="outline" size="sm" onClick={() => setViewed(null)}>
                  Close
                </Button>
              </div>
            </div>
            <pre className="json-block">
              {viewed.contentType === "json" ? prettyJson(viewed.value) : viewed.value}
            </pre>
          </div>
        ) : null}
      </div>

      <Modal
        open={newVersionOpen}
        title="New parameter version"
        onClose={() => setNewVersionOpen(false)}
        dismissible={!saving}
        footer={
          <>
            <Button variant="outline" onClick={() => setNewVersionOpen(false)} disabled={saving}>
              Cancel
            </Button>
            <Button onClick={saveVersion} disabled={saving || shownVersionError !== null}>
              {saving ? <Spinner /> : null}
              Save new version
            </Button>
          </>
        }
      >
        <form onSubmit={saveVersion}>
          <Field
            label="Value"
            hint="Saving creates a new version and updates the current label."
            error={shownValueError}
          >
            <Textarea
              className="font-mono"
              value={value}
              onChange={(e) => setValue(e.target.value)}
              onBlur={() => markTouched("value")}
            />
          </Field>
          <div className="form-row">
            <Field label="Content type">
              <AppSelect
                value={contentType}
                onValueChange={setContentType}
                options={PARAMETER_CONTENT_TYPES.map((contentTypeOption) => ({
                  value: contentTypeOption,
                  label: contentTypeOption,
                }))}
              />
            </Field>
            <Field label="Metadata JSON" error={shownMetadataError}>
              <Input
                className="font-mono"
                value={metadataJson}
                onChange={(e) => setMetadataJson(e.target.value)}
                onBlur={() => markTouched("metadata")}
              />
            </Field>
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
