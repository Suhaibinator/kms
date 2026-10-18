import { ChevronDown } from "lucide-react";
import { useEffect, useId, useState } from "react";
import { ValueView } from "@/components/JsonView";
import { SearchField } from "@/components/SearchField";
import { Badge, Button, EmptyState } from "@/components/ui";
import { api } from "@/lib/api";
import { errorMessage } from "@/lib/bulk";
import type { ConfigurationRelease, ConfigurationReleaseEntry } from "@/lib/types";
import { refText } from "./utils";

type ValueState =
  | { status: "loading" }
  | { status: "loaded"; value: string }
  | { status: "error"; message: string };

function PinnedValue({ entry }: { entry: ConfigurationReleaseEntry }) {
  const [state, setState] = useState<ValueState>({ status: "loading" });
  const [attempt, setAttempt] = useState(0);
  const { env, app } = entry.ref.namespace;
  const { key } = entry.ref;
  const { version } = entry;

  // biome-ignore lint/correctness/useExhaustiveDependencies: attempt reruns the request on Retry.
  useEffect(() => {
    const controller = new AbortController();
    setState({ status: "loading" });
    api
      .getParameter({ env, app, key }, version, undefined, { signal: controller.signal })
      .then(({ parameter }) => {
        if (!controller.signal.aborted) setState({ status: "loaded", value: parameter.value });
      })
      .catch((error: unknown) => {
        if (!controller.signal.aborted) setState({ status: "error", message: errorMessage(error) });
      });
    return () => controller.abort();
  }, [env, app, key, version, attempt]);

  if (state.status === "loading") return <p role="status">Loading pinned value…</p>;
  if (state.status === "error") {
    return (
      <div className="space-y-2">
        <p role="alert">
          Could not load v{version}: {state.message}
        </p>
        <Button variant="outline" size="sm" onClick={() => setAttempt((current) => current + 1)}>
          Retry
        </Button>
      </div>
    );
  }
  return (
    <ValueView
      value={state.value}
      contentType={entry.content_type}
      copyLabel={`Copy ${entry.alias} value`}
      maxHeight="60vh"
    />
  );
}

function ValueEntry({ entry }: { entry: ConfigurationReleaseEntry }) {
  const [open, setOpen] = useState(false);
  const [visited, setVisited] = useState(false);
  const id = useId();

  return (
    <section className="card min-w-0" aria-label={`${entry.alias} pinned value`}>
      <Button
        variant="ghost"
        className="h-auto w-full justify-start whitespace-normal text-left"
        aria-expanded={open}
        aria-controls={id}
        aria-label={`${open ? "Hide" : "View"} ${entry.alias} value`}
        onClick={() => {
          setVisited(true);
          setOpen((current) => !current);
        }}
      >
        <ChevronDown aria-hidden className={open ? "rotate-180" : ""} />
        <span className="min-w-0 flex-1 space-y-1">
          <span className="flex flex-wrap items-center gap-2">
            <span className="mono break-all">{entry.alias}</span>
            <Badge>v{entry.version}</Badge>
            <span className="faint text-xs">{entry.content_type}</span>
          </span>
          <span className="block mono break-all text-xs faint">{refText(entry)}</span>
        </span>
      </Button>
      <div id={id} hidden={!open} className="mt-3 min-w-0">
        {/* Keep a viewed value mounted when folded so reopening needs no request. */}
        {visited ? <PinnedValue entry={entry} /> : null}
      </div>
    </section>
  );
}

/** Values are fetched on demand at the manifest's exact pins, including for inactive releases. */
export function ReleaseValues({ release }: { release: ConfigurationRelease }) {
  const [filter, setFilter] = useState("");
  const parameters = release.entries.filter((entry) => entry.kind === "parameter");
  const query = filter.trim().toLowerCase();
  const shown = parameters.filter((entry) =>
    `${entry.alias} ${refText(entry)}`.toLowerCase().includes(query),
  );

  return (
    <div className="space-y-4 min-w-0">
      <p className="faint">
        Inspect parameter values pinned by {release.name}@{release.version}. Secret values are not
        included.
      </p>
      {parameters.length === 0 ? (
        <EmptyState title="No parameter values">This release has no parameter entries.</EmptyState>
      ) : (
        <>
          <SearchField
            label="Filter parameters"
            placeholder="Filter by alias or reference"
            value={filter}
            onChange={setFilter}
            onClear={() => setFilter("")}
          />
          {shown.length === 0 ? <p role="status">No parameters match your filter.</p> : null}
          {shown.map((entry) => (
            <ValueEntry
              key={JSON.stringify([entry.alias, entry.ref, entry.version])}
              entry={entry}
            />
          ))}
        </>
      )}
    </div>
  );
}
