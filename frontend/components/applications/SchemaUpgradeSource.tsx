import { useEffect, useState } from "react";
import { Button, EmptyState, Field, PageHeader } from "@/components/ui";
import { api, isAbortError } from "@/lib/api";
import { useSchemaRegistry } from "@/lib/useSchemaRegistry";

interface Source {
  schemaVersion: number;
  environment: string;
  releaseVersion: number;
}

/** An upgrade URL names a destination; operators explicitly choose an active source. */
export function SchemaUpgradeSource({
  application,
  destination,
  onSelect,
  onCancel,
}: {
  application: string;
  destination: number;
  onSelect: (schemaVersion: number, environment: string) => void;
  onCancel: () => void;
}) {
  const registry = useSchemaRegistry(application);
  const scope = JSON.stringify([application, destination]);
  const [result, setResult] = useState<{ scope: string; sources: Source[]; error: string } | null>(
    null,
  );
  const [selection, setSelection] = useState("");
  useEffect(() => {
    setSelection("");
    setResult(null);
    if (!registry.schemas) return;
    const controller = new AbortController();
    const versions = [...registry.schemas.map((schema) => schema.version), 0].filter(
      (version) => version < destination,
    );
    void Promise.all(
      versions.map(async (schemaVersion) => {
        const overview = await api.applicationOverview(
          application,
          undefined,
          { signal: controller.signal },
          schemaVersion,
        );
        return overview.environments.flatMap((environment): Source[] => {
          const active = environment.release.active;
          return active
            ? [
                {
                  schemaVersion,
                  environment: environment.namespace.env,
                  releaseVersion: active.version,
                },
              ]
            : [];
        });
      }),
    )
      .then((sources) => {
        if (!controller.signal.aborted) setResult({ scope, sources: sources.flat(), error: "" });
      })
      .catch((error: unknown) => {
        if (!controller.signal.aborted && !isAbortError(error)) {
          setResult({
            scope,
            sources: [],
            error: error instanceof Error ? error.message : "Could not load upgrade sources.",
          });
        }
      });
    return () => controller.abort();
  }, [application, destination, registry.schemas, scope]);
  const current = result?.scope === scope ? result : null;
  const selected = current?.sources.find(
    (source) => JSON.stringify([source.schemaVersion, source.environment]) === selection,
  );
  const error = registry.error || current?.error;
  return (
    <>
      <PageHeader
        title="Choose upgrade source"
        subtitle={`${application} · destination schema v${destination}`}
      />
      <section className="card p-4 stack" aria-label="Upgrade source">
        <p>
          Choose an active schema track and environment to copy into the destination. The source
          activation stays in place.
        </p>
        {error ? (
          <p role="alert">Could not load upgrade sources. {String(error)}</p>
        ) : !current ? (
          <p role="status">Loading active source tracks…</p>
        ) : current.sources.length === 0 ? (
          <EmptyState title="No active source release">
            Activate a release on an earlier schema track before setting up this upgrade.
          </EmptyState>
        ) : (
          <Field label="Source track and environment">
            <select
              aria-label="Source track and environment"
              value={selection}
              onChange={(event) => setSelection(event.target.value)}
            >
              <option value="">Choose an active source</option>
              {current.sources.map((source) => {
                const key = JSON.stringify([source.schemaVersion, source.environment]);
                return (
                  <option key={key} value={key}>
                    schema v{source.schemaVersion}
                    {source.schemaVersion === 0 ? " (schema-free)" : ""} · {source.environment} ·
                    release v{source.releaseVersion}
                  </option>
                );
              })}
            </select>
          </Field>
        )}
        <div className="row-wrap">
          <Button variant="outline" onClick={onCancel}>
            Cancel upgrade
          </Button>
          <Button
            disabled={!selected}
            onClick={() => selected && onSelect(selected.schemaVersion, selected.environment)}
          >
            Continue upgrade
          </Button>
        </div>
      </section>
    </>
  );
}
